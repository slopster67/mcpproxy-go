package runtime

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/runtime/configsvc"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

// ServerChange is one server's change in a commit.
type ServerChange = configsvc.ServerChange

// CommitResult describes a commit that returned without error.
type CommitResult struct {
	// Generation is the commit generation G, or 0 when nothing was published.
	Generation int64
	// Snapshot is the published snapshot (nil when nothing was published).
	Snapshot *configsvc.Snapshot
	// Changes is the per-server change set.
	Changes map[string]ServerChange
	// Published reports whether the commit published a configuration.
	Published bool

	// Restamped lists the retained instances step 7 updated in place.
	Restamped []string
	// Purged reports, per removed or replaced server, what the storage step
	// deleted from the purge set.
	Purged map[string]storage.PurgeReport
	// unregistered are the instances step 7 removed from the manager; they
	// are disconnected after the commit lock is released.
	unregistered []*managed.Client
}

// ServerNotFoundError reports a server that is not in the live configuration.
type ServerNotFoundError struct{ Name string }

func (e *ServerNotFoundError) Error() string { return fmt.Sprintf("server '%s' not found", e.Name) }

// Is makes errors.Is(err, ErrServerNotFound) match.
func (e *ServerNotFoundError) Is(target error) bool { return target == ErrServerNotFound }

// ErrServerNotFound matches every *ServerNotFoundError.
var ErrServerNotFound = errors.New("server not found")

// ServerExistsError is returned when a create names a server that is already
// configured (or still stored). Its text is a contract: the
// REST 409 mapping, cliclient and the registry add all match "already exists".
type ServerExistsError struct{ Name string }

func (e *ServerExistsError) Error() string { return fmt.Sprintf("server '%s' already exists", e.Name) }

// ErrServerReplaced reports that the server a caller bound to (by generation or
// incarnation) was removed or replaced before its commit ran. It is
// commitview.ErrServerReplaced, so the scanner and the HTTP layer match it
// without importing the runtime.
var ErrServerReplaced = commitview.ErrServerReplaced

// ErrSettingsImmutable rejects attempts to alter startup settings in an entity commit.
var ErrSettingsImmutable = errors.New("deployment settings are immutable; restart to apply changes")

// ErrCommitInvariant marks an invalid entity commit.
var ErrCommitInvariant = errors.New("config commit invariant violated")

// errCommitReentry is returned when code running inside a commit calls Commit
// again on the same goroutine, which would otherwise deadlock on L0.
var errCommitReentry = fmt.Errorf("%w: Commit called from inside a commit", ErrCommitInvariant)

// CommitTx is the working copy a commit's fn mutates. It is only valid inside
// fn. Its methods never touch storage, disk or the manager; the pipeline does.
type CommitTx struct {
	r      *Runtime
	reason string
	base   *configsvc.Snapshot
	next   *config.Config

	baseNames map[string]bool
	created   map[string]bool // created in this tx and absent from base
	removed   map[string]bool // present in base and removed in this tx
	replaced  map[string]bool // removed and re-created in this tx (review R8.2)

	hooks []approvalHook

	clientProfileMove *storage.ClientProfileMove
	bootstrap         bool

	after      []func(*CommitResult)
	eventExtra map[string]any

	err error
}

type approvalHook struct {
	name string
	fn   func() error
}

// Server returns a deep copy of the working entry for name.
func (tx *CommitTx) Server(name string) (*config.ServerConfig, bool) {
	if sc := tx.entry(name); sc != nil {
		return config.CopyServerConfig(sc), true
	}
	return nil, false
}

// Generation returns name's committed generation in the base snapshot.
func (tx *CommitTx) Generation(name string) (configsvc.ServerGen, bool) {
	return tx.base.ServerGeneration(name)
}

// Base returns the live configuration the commit started from. Read-only.
func (tx *CommitTx) Base() *config.Config { return tx.base.Config }

// Config returns the working configuration. Callers may read it; they change
// it only through entity operations. Global settings cannot change.
func (tx *CommitTx) Config() *config.Config { return tx.next }

func (tx *CommitTx) entry(name string) *config.ServerConfig {
	for _, sc := range tx.next.Servers {
		if sc != nil && sc.Name == name {
			return sc
		}
	}
	return nil
}

func (tx *CommitTx) entryIndex(name string) int {
	for i, sc := range tx.next.Servers {
		if sc != nil && sc.Name == name {
			return i
		}
	}
	return -1
}

// CreateServer adds a new server. It fails with *ServerExistsError when the
// name is already present in the working snapshot.
func (tx *CommitTx) CreateServer(sc *config.ServerConfig) error {
	if sc == nil || strings.TrimSpace(sc.Name) == "" {
		return fmt.Errorf("server name is required")
	}
	if tx.entry(sc.Name) != nil {
		return &ServerExistsError{Name: sc.Name}
	}

	tx.next.Servers = append(tx.next.Servers, config.CopyServerConfig(sc))
	if tx.removed[sc.Name] {
		delete(tx.removed, sc.Name)
		tx.replaced[sc.Name] = true
	} else if !tx.baseNames[sc.Name] {
		tx.created[sc.Name] = true
	}
	return nil
}

// UpdateServer runs fn on a deep copy of the live entry and stores the result.
// fn must not change the name. Unknown names fail with *ServerNotFoundError.
func (tx *CommitTx) UpdateServer(name string, fn func(cur *config.ServerConfig) error) error {
	i := tx.entryIndex(name)
	if i < 0 {
		return &ServerNotFoundError{Name: name}
	}
	cp := config.CopyServerConfig(tx.next.Servers[i])
	if err := fn(cp); err != nil {
		return err
	}
	if cp.Name != name {
		return fmt.Errorf("%w: UpdateServer cannot rename %q to %q", ErrCommitInvariant, name, cp.Name)
	}

	tx.next.Servers[i] = cp
	return nil
}

// SetEnabled sets a server's enabled flag.
func (tx *CommitTx) SetEnabled(name string, enabled bool) error {
	return tx.UpdateServer(name, func(cur *config.ServerConfig) error {
		cur.Enabled = enabled
		return nil
	})
}

// RemoveServer removes a server. Unknown names fail with *ServerNotFoundError.
func (tx *CommitTx) RemoveServer(name string) error {
	i := tx.entryIndex(name)
	if i < 0 {
		return &ServerNotFoundError{Name: name}
	}
	tx.next.Servers = append(tx.next.Servers[:i:i], tx.next.Servers[i+1:]...)
	switch {
	case tx.created[name]:
		delete(tx.created, name)
	case tx.replaced[name]:
		delete(tx.replaced, name)
		tx.removed[name] = true
	default:
		tx.removed[name] = true
	}
	return nil
}

// Bootstrap is for startup only: it
// forces the pipeline to run and schedules a connect for every enabled server.
// Anywhere else it is an invariant error (review R9.6).
func (tx *CommitTx) Bootstrap() {
	if tx.reason != commitReasonBoot || tx.r.bootstrapped.Load() {
		tx.err = fmt.Errorf("%w: Bootstrap() outside the boot commit (reason %q)", ErrCommitInvariant, tx.reason)
		return
	}
	tx.bootstrap = true
}

// --- unexported plumbing used by the runtime's own commit callers ---

// afterCommit runs after the commit lock is released, only on success.
func (tx *CommitTx) afterCommit(fn func(*CommitResult)) { tx.after = append(tx.after, fn) }

func (tx *CommitTx) EventExtra(extra map[string]any) {
	if tx.eventExtra == nil {
		tx.eventExtra = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		tx.eventExtra[k] = v
	}
}

const (
	commitReasonBoot = "boot"
)

// commitState is the runtime's commit bookkeeping (embedded in Runtime).
type commitState struct {
	commitOwner  atomic.Int64 // goroutine id holding configCommitMu inside Commit
	bootstrapped atomic.Bool

	// telemetryMu orders the after-unlock telemetry notifications by commit
	// generation: telemetryGen is the newest generation
	// whose configuration the telemetry service was given.
}

// Commit runs fn against a working copy of the live configuration while holding
// configCommitMu, then persists and publishes the result (see the pipeline
// above). An error from fn, or from any step before publication, aborts the
// commit with nothing published and storage untouched.
func (r *Runtime) Commit(reason string, fn func(tx *CommitTx) error) (*CommitResult, error) {
	if r.configSvc == nil {
		return nil, fmt.Errorf("config service not initialized")
	}
	gid := currentGoroutineID()
	if gid != 0 && r.commitOwner.Load() == gid {
		return nil, errCommitReentry
	}
	r.configCommitMu.Lock()
	r.commitOwner.Store(gid)
	res, after, err := r.commitLocked(reason, fn)
	r.commitOwner.Store(0)
	r.configCommitMu.Unlock()
	if err != nil {
		return nil, err
	}
	if after != nil {

		after()
	}
	return res, nil
}

func cloneCommitConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return &config.Config{}
	}
	return config.CopyConfig(cfg)
}

func serverNameSet(cfg *config.Config) map[string]bool {
	out := make(map[string]bool, len(cfg.Servers))
	for _, sc := range cfg.Servers {
		if sc != nil {
			out[sc.Name] = true
		}
	}
	return out
}

func (r *Runtime) commitLocked(reason string, fn func(tx *CommitTx) error) (*CommitResult, func(), error) {
	base := r.configSvc.Current()
	tx := &CommitTx{
		r:         r,
		reason:    reason,
		base:      base,
		next:      cloneCommitConfig(base.Config),
		baseNames: serverNameSet(base.Config),
		created:   map[string]bool{},
		removed:   map[string]bool{},
		replaced:  map[string]bool{},
	}

	// Step 1: build.
	if fn != nil {
		if err := fn(tx); err != nil {
			return nil, nil, err
		}
	}
	if tx.err != nil {
		return nil, nil, tx.err
	}

	if !globalSettingsEqual(base.Config, tx.next) {
		return nil, nil, ErrSettingsImmutable
	}

	// Prepare the next committed snapshot from the current database-backed state.
	replaced := tx.replaced
	if len(replaced) == 0 {
		replaced = nil
	}
	updateType := configsvc.UpdateTypeModify

	// The servers whose OAuth state this commit clears get a
	// new OAuth epoch in the prepared snapshot, so the snapshot, the rows and
	// the commit result carry one committed credential binding.
	var oauthCleared map[string]bool
	if r.storageManager != nil {
		oauthCleared = r.commitOAuthClearSet(tx, replaced)
	}
	prepared, err := r.configSvc.Prepare(configsvc.PrepareInput{
		Config: tx.next, Source: reason, Type: updateType,
		Replaced:     replaced,
		OAuthCleared: oauthCleared,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCommitInvariant, err)
	}

	changes := prepared.Changes
	gens := prepared.Snapshot().ServerGenerations
	configChanged := len(changes) > 0 || !configsEquivalent(base.Config, tx.next)

	batch := &storage.EntityCommit{Generation: prepared.Generation(), ClientProfileMove: tx.clientProfileMove}
	for name, change := range changes {
		switch change.Kind {
		case configsvc.ChangeRemoved:
			batch.Removes = append(batch.Removes, name)
		default:
			server := tx.entry(name)
			if server == nil {
				return nil, nil, fmt.Errorf("missing changed server %q", name)
			}
			batch.Saves = append(batch.Saves, storage.ServerSave{Config: server, Stamp: gens[name]})
			if change.Kind == configsvc.ChangeReplaced {
				batch.Replaces = append(batch.Replaces, name)
			}
		}
	}
	sort.Strings(batch.Removes)
	sort.Strings(batch.Replaces)
	profiles := make(map[string]config.ProfileConfig, len(tx.base.Config.Profiles))
	for _, profile := range tx.base.Config.Profiles {
		profiles[profile.ID] = profile
	}
	for _, profile := range tx.next.Profiles {
		previous, exists := profiles[profile.ID]
		if !exists || !reflect.DeepEqual(previous, profile) {
			batch.ProfileSaves = append(batch.ProfileSaves, profile)
		}
		delete(profiles, profile.ID)
	}
	for id := range profiles {
		batch.ProfileRemoves = append(batch.ProfileRemoves, id)
	}
	sort.Strings(batch.ProfileRemoves)
	batch.OAuthClears = r.commitOAuthClears(oauthCleared, batch.Saves)
	storageNeeded := len(batch.Saves) > 0 || len(batch.Removes) > 0 || len(batch.ProfileSaves) > 0 || len(batch.ProfileRemoves) > 0 || batch.ClientProfileMove != nil || tx.bootstrap
	if !configChanged && !storageNeeded && len(tx.hooks) == 0 {
		return &CommitResult{Changes: changes}, nil, nil
	}

	// Step 4: approval locks for changed names (sorted), then the hooks.
	lockSet := map[string]bool{}
	for name := range changes {
		lockSet[name] = true
	}
	for _, h := range tx.hooks {
		lockSet[h.name] = true
	}
	if batch != nil {
		for _, n := range batch.Removes {
			lockSet[n] = true
		}
		// Server writes share the lock used by tool approval and discovery.
		for _, s := range batch.Saves {
			lockSet[s.Config.Name] = true
		}
		for _, n := range batch.Replaces {
			lockSet[n] = true
		}
		for _, n := range batch.OAuthClears {
			lockSet[n] = true
		}
	}
	lockNames := make([]string, 0, len(lockSet))
	for n := range lockSet {
		lockNames = append(lockNames, n)
	}
	sort.Strings(lockNames)
	unlocks := make([]func(), 0, len(lockNames))
	releaseLocks := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
		unlocks = nil
	}
	for _, n := range lockNames {
		unlocks = append(unlocks, r.lockToolApprovals(n))
	}
	for _, h := range tx.hooks {
		if err := h.fn(); err != nil {
			releaseLocks()
			return nil, nil, err
		}
	}

	// Step 6: storage (with the removal purge), then index
	// purge, then the in-memory purge, all under the approval locks.
	var purgeReports map[string]storage.PurgeReport
	if storageNeeded {
		var err error
		purgeReports, err = r.storageManager.ApplyEntityCommit(batch)
		if err != nil {
			releaseLocks()
			return nil, nil, fmt.Errorf("commit entities: %w", err)
		}
	}

	purged := r.commitIndexPurge(tx, changes, batch)
	r.commitMemoryPurgeHeld(purgeReports)
	releaseLocks()
	r.commitDropPurgedSchedules(purgeReports)
	if batch != nil {
		r.commitForgetRefreshState(tx, purgeReports, batch.OAuthClears)
	}

	// Step 7: advance the manager. AdvanceGenerations records
	// every configured server's committed generation (and a tombstone for a
	// removed one), unregisters the instances that must not survive this
	// commit and restamps the retained ones, under captureMu W + Manager.mu,
	// before the snapshot is published. Approval locks were released in step
	// 6 (lock rule 2).
	var advanced upstream.AdvanceResult
	if r.upstreamManager != nil {
		advanced = r.upstreamManager.AdvanceGenerations(generationChanges(tx, changes, prepared.Snapshot()))
	}
	restamped := advanced.Restamped

	// Step 8: publish.
	snap, perr := r.configSvc.Publish(prepared)
	if perr != nil {
		r.logger.Error("Config commit: publish failed after storage committed (invariant violation)",
			zap.String("reason", reason), zap.Error(perr))
		return nil, nil, fmt.Errorf("%w: %v", ErrCommitInvariant, perr)
	}
	r.mu.Lock()
	r.cfg = tx.next
	r.mu.Unlock()
	if r.upstreamManager != nil {
		r.upstreamManager.SetGlobalConfig(tx.next)
	}
	if tx.bootstrap {
		r.bootstrapped.Store(true)
	}

	res := &CommitResult{
		Generation:   snap.Version,
		Snapshot:     snap,
		Changes:      changes,
		Published:    true,
		Restamped:    restamped,
		Purged:       purgeReports,
		unregistered: advanced.Unregistered,
	}
	return res, r.afterCommitFunc(tx, res, snap, purged), nil
}

// commitIndexPurge removes index entries for removed, replaced, or disabled
// servers under their approval locks, before publishing the configuration.
func (r *Runtime) commitIndexPurge(tx *CommitTx, changes map[string]ServerChange, batch *storage.EntityCommit) []string {
	if r.indexManager == nil {
		return nil
	}
	targets := map[string]bool{}
	for name, ch := range changes {
		switch ch.Kind {
		case configsvc.ChangeRemoved, configsvc.ChangeReplaced:
			targets[name] = true
		default:
			if sc := tx.entry(name); sc != nil && !sc.Enabled {
				if prev := baseEntry(tx.base.Config, name); prev == nil || prev.Enabled {
					targets[name] = true
				}
			}
		}
	}
	if batch != nil {
		for _, n := range batch.Removes {
			targets[n] = true
		}
	}
	names := make([]string, 0, len(targets))
	for n := range targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := r.indexManager.DeleteServerTools(name); err != nil {
			r.logger.Warn("Config commit: failed to purge server tools from the index",
				zap.String("server", name), zap.Error(err))
			continue
		}
		r.logger.Info("Removed server tools from index (config commit)", zap.String("server", name))
	}
	return names
}

func baseEntry(cfg *config.Config, name string) *config.ServerConfig {
	if cfg == nil {
		return nil
	}
	for _, sc := range cfg.Servers {
		if sc != nil && sc.Name == name {
			return sc
		}
	}
	return nil
}

// commitOAuthClearSet returns the servers whose cached OAuth state (tokens
// and DCR client credentials) the storage step deletes in the row
// transaction, because their OAuth
// configuration changed:
//
//   - relative to the base snapshot (an edit through any writer), and
//   - relative to the stored row: an OAuth-only file
//     edit made while the daemon was stopped is already in the boot commit's
//     base, so only the stored row still holds the prior OAuth config.
//
// A replaced name is a new server whose whole purge set the storage step
// deletes. It runs before Prepare, which raises the OAuth epoch of every name
// in the set.
func (r *Runtime) commitOAuthClearSet(tx *CommitTx, replaced map[string]bool) map[string]bool {
	set := map[string]bool{}
	for _, next := range tx.next.Servers {
		if next == nil || replaced[next.Name] {
			continue
		}
		name := next.Name
		clear := false
		if prev := baseEntry(tx.base.Config, name); prev != nil && config.OAuthConfigChanged(prev.OAuth, next.OAuth) {
			clear = true
		}
		if clear {
			set[name] = true
		}
	}
	return set
}

// commitOAuthClears returns the sorted names of set whose row this commit
// writes (an OAuth config difference always makes the row differ, so this is
// all of them in practice; ApplyServerCommit refuses a clear without a save).
func (r *Runtime) commitOAuthClears(set map[string]bool, saves []storage.ServerSave) []string {
	var names []string
	for _, s := range saves {
		if name := s.Config.Name; set[name] {
			r.logger.Info("OAuth config changed, clearing cached OAuth state in the config commit", zap.String("server", name))
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// commitForgetRefreshState runs in commit step 6 next to
// commitDropPurgedSchedules (under configCommitMu, after the approval locks):
// for every purged server and every server whose OAuth state the commit
// cleared, it drops the refresh coordinator's flights, latch and cooldown of
// the superseded credential, and cancels the refresh
// schedule of an OAuth-cleared server (the purge already did for removed
// ones). The coordinator's mutex is a leaf, so this adds no lock edge. The
// replacement's binding has no state yet: its instance is built after the
// commit publishes.
func (r *Runtime) commitForgetRefreshState(tx *CommitTx, purged map[string]storage.PurgeReport, oauthCleared []string) {
	forget := func(name string) {
		if prev := baseEntry(tx.base.Config, name); prev != nil {
			oauth.DefaultRefreshCoordinator().ForgetServer(oauth.GenerateServerKey(name, prev.URL))
		}
	}
	for name := range purged {
		forget(name)
	}
	for _, name := range oauthCleared {
		forget(name)
		if r.refreshManager != nil {
			r.refreshManager.OnTokenCleared(name)
		}
	}
}

// afterCommitFunc builds the work that runs after the commit lock is released:
// manager removals, connect workers, discovery for restamped instances, the
// per-profile index rebuild, events and telemetry (at most once per
// commit).
func (r *Runtime) afterCommitFunc(tx *CommitTx, res *CommitResult, snap *configsvc.Snapshot, purged []string) func() {
	return func() {
		for _, name := range purged {
			r.reindexAffectedProfiles(name)
		}
		r.afterServerPurge(res.Purged)
		if res.Published {
			r.runCommitWorkers(tx, res, snap)
			if len(res.Changes) > 0 {
				names := make([]string, 0, len(res.Changes))
				for n := range res.Changes {
					names = append(names, n)
				}
				sort.Strings(names)
				r.serversChanged(names...)
				// The secrets list holds every server's secret references.
				if r.secretReferencesChanged(tx.base, snap, names) {
					r.changes.Invalidate(EntitySecrets, "")
				}
				r.allProfilesChanged()
				r.syncOnlinePeers()
			}
			for name, ch := range res.Changes {
				if ch.Kind == configsvc.ChangeRemoved || ch.Kind == configsvc.ChangeReplaced {
					if ch.Kind == configsvc.ChangeRemoved {
						r.reviewQueueSigs.Delete(name)
					}
					r.toolsChanged(name)
				}
			}
		}

		for _, fn := range tx.after {
			fn(res)
		}
	}
}

// generationChanges is commit step 7's input: every server of the new
// configuration with its generation in the prepared snapshot, plus every
// server the commit removed. Sorted by name for a deterministic order.
func generationChanges(tx *CommitTx, changes map[string]ServerChange, snap *configsvc.Snapshot) []upstream.GenerationChange {
	out := make([]upstream.GenerationChange, 0, len(tx.next.Servers)+len(changes))
	seen := make(map[string]bool, len(tx.next.Servers))
	for _, sc := range tx.next.Servers {
		if sc == nil || seen[sc.Name] {
			continue
		}
		seen[sc.Name] = true
		sg, _ := snap.ServerGeneration(sc.Name)
		ch := changes[sc.Name]
		out = append(out, upstream.GenerationChange{
			Name:      sc.Name,
			Gen:       sg.Gen,
			Present:   true,
			Reconnect: ch.Reconnect,
			Replaced:  ch.Kind == configsvc.ChangeReplaced,
			Config:    sc,
			Binding:   sg.Binding(),
		})
	}
	for name, ch := range changes {
		if ch.Kind == configsvc.ChangeRemoved && !seen[name] {
			out = append(out, upstream.GenerationChange{Name: name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// runCommitWorkers is the after-unlock scheduling.
// The instances step 7 unregistered are disconnected first. Created, replaced
// and reconnecting servers then get a connect (enabled) or disconnect
// (disabled) worker bound to (name, generation): the manager re-checks that
// generation under its own lock when the worker acts, so a worker that a
// newer commit superseded is inert. The boot commit schedules every
// server. Restamped connected servers get one discovery pass.
func (r *Runtime) runCommitWorkers(tx *CommitTx, res *CommitResult, snap *configsvc.Snapshot) {
	if r.upstreamManager == nil {
		return
	}
	for _, old := range res.unregistered {
		go func(c *managed.Client) { _ = c.Disconnect() }(old)
	}
	schedule := map[string]bool{}
	for name, ch := range res.Changes {
		if ch.Kind != configsvc.ChangeRemoved && ch.Reconnect {
			schedule[name] = true
		}
	}
	if tx.bootstrap {
		for _, sc := range tx.next.Servers {
			schedule[sc.Name] = true
		}
	}

	for _, sc := range tx.next.Servers {
		if !schedule[sc.Name] {
			continue
		}
		sg, _ := snap.ServerGeneration(sc.Name)
		if sc.Enabled {
			go r.connectWorker(sc, sg.Gen, sg.Incarnation)
		} else {
			go r.disconnectWorker(sc.Name, sg.Gen)
		}
	}
	ctx := r.AppContext()
	for _, name := range res.Restamped {
		if c, ok := r.upstreamManager.GetClient(name); ok && c != nil && c.IsConnected() && ctx != nil {
			go func(name string) {
				if err := r.DiscoverAndIndexToolsForServer(ctx, name); err != nil {
					r.logger.Debug("Discovery after a retained-instance restamp failed", zap.String("server", name), zap.Error(err))
				}
			}(name)
		}
	}
}

// connectWorker installs the instance and notifies the controller at commit
// generation gen. It is inert (ErrStaleGeneration, debug log) once a newer
// commit changed, removed or re-created the server.
func (r *Runtime) connectWorker(cfg *config.ServerConfig, gen, inc int64) {

	if err := r.upstreamManager.AddServerConfigAt(cfg.Name, cfg, gen); err != nil {
		if errors.Is(err, upstream.ErrStaleGeneration) {
			r.logger.Debug("Connect worker superseded by a newer config commit; nothing to do",
				zap.String("server", cfg.Name), zap.Int64("generation", gen))
			return
		}
		r.logger.Error("Failed to add/update upstream server", zap.Error(err), zap.String("server", cfg.Name))
		return
	}
	if r.storageManager != nil {

		// Incarnation-conditional: a worker that outlives a
		// removal registers nothing for the removed server or its
		// replacement.
		if _, written, err := r.storageManager.RegisterServerIdentityIfIncarnation(cfg, inc); err != nil {
			r.logger.Warn("Failed to register server identity", zap.Error(err), zap.String("server", cfg.Name))
		} else if !written {
			r.logger.Debug("Server identity not registered: the server was removed or replaced",
				zap.String("server", cfg.Name), zap.Int64("incarnation", inc))
		}
	}
	r.supervisor.Wake()
}

// disconnectWorker removes the instance of a disabled server committed at
// generation gen. It is inert once a newer commit re-enabled (or otherwise
// changed) the server.
func (r *Runtime) disconnectWorker(name string, gen int64) {

	if err := r.upstreamManager.RemoveServerAt(name, gen); err != nil && errors.Is(err, upstream.ErrStaleGeneration) {
		r.logger.Debug("Disconnect worker superseded by a newer config commit; nothing to do",
			zap.String("server", name), zap.Int64("generation", gen))
	}
}

// currentGoroutineID parses the running goroutine's id from its stack header.
// Used only to turn a re-entrant Commit into an error instead of a deadlock.
func currentGoroutineID() int64 {
	var buf [64]byte
	n := goruntime.Stack(buf[:], false)
	fields := bytes.Fields(buf[:n])
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// secretReferencesChanged reports whether any named server's secret
// references (or, where it has some, the host that resolves them) differ
// between the base and committed snapshots: the only parts of a server config
// the secrets list shows.
func (r *Runtime) secretReferencesChanged(base, next *configsvc.Snapshot, names []string) bool {
	view := func(snap *configsvc.Snapshot, name string) (refs []secret.Ref, host string) {
		if snap == nil || r.secretResolver == nil {
			return nil, ""
		}
		server := snap.GetServer(name)
		if server == nil {
			return nil, ""
		}
		refs = r.secretResolver.References(server)
		if len(refs) == 0 {
			return nil, ""
		}
		return refs, server.ExecutionHost()
	}
	for _, name := range names {
		oldRefs, oldHost := view(base, name)
		newRefs, newHost := view(next, name)
		if oldHost != newHost || !reflect.DeepEqual(oldRefs, newRefs) {
			return true
		}
	}
	return false
}
