package upstream

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
	"github.com/slopster67/mcpproxy-go/internal/upstream/types"
)

// ErrStaleGeneration is returned by the *At methods when the action was bound
// to a generation, or an instance, that is no longer the server's current
// one. It is not a failure: the caller logs it at debug level and does
// nothing.
var ErrStaleGeneration = commitview.ErrStaleGeneration

// desiredGen is the manager's record of one server name's committed state.
// present=false is a tombstone left by a removal (manager memory only); a
// later create overwrites it.
//
// binding is the committed credential binding at gen: every instance
// installed for the name at gen is bound to it, so it comes from the same
// commit as the config and the generation, never from the stored row.
type desiredGen struct {
	gen     int64
	present bool
	binding commitview.CredentialBinding
}

// GenerationChange is one server's state after a config commit, as commit
// step 7 hands it to AdvanceGenerations.
type GenerationChange struct {
	Name string
	// Gen is the server's commit generation in the published snapshot (0 when
	// the server was removed).
	Gen int64
	// Present is false when the commit removed the server.
	Present bool
	// Reconnect is true when the commit changed the connection settings,
	// Enabled (configsvc.ServerChange.Reconnect).
	Reconnect bool
	// Replaced is true for a remove and re-create in one commit: the
	// previous instance never survives, even for an equal entry.
	Replaced bool
	// Config is the committed entry (nil when removed).
	Config *config.ServerConfig
	// Binding is the server's committed credential binding in the published
	// snapshot (zero when removed).
	Binding commitview.CredentialBinding
}

// AdvanceResult reports what AdvanceGenerations did.
type AdvanceResult struct {
	// Unregistered are the instances removed from the registry. The caller
	// disconnects them after it released its own locks (they no longer
	// connect: managed.ErrInstanceUnregistered).
	Unregistered []*managed.Client
	// Restamped names the retained instances that took the committed config
	// and generation in place (no reconnect).
	Restamped []string
}

// AdvanceGenerations applies one config commit to the registry (commit step
// 7, plan.md §Commit pipeline). changes must list EVERY server of the
// published configuration (unchanged ones too, so the desired view is complete
// after the boot commit) plus every server the commit removed. For each name:
//
//   - removed: the instance is unregistered and the name tombstoned;
//   - replaced, or a connection/Enabled change whose generation
//     moved: the instance is unregistered (the commit's worker installs the
//     new one);
//   - otherwise a registered instance behind the committed generation is
//     restamped with the committed config (SetConfigAt), or unregistered if it
//     does not run the committed connection settings.
//
// It takes captureMu for writing and then mu (L3 → L4); the caller holds no
// approval lock (rule 2). It never connects or disconnects.
func (m *Manager) AdvanceGenerations(changes []GenerationChange) AdvanceResult {
	var res AdvanceResult
	if len(changes) == 0 {
		return res
	}

	m.captureMu.Lock()
	m.mu.Lock()
	for _, ch := range changes {
		if ch.Name == "" {
			continue
		}
		if !ch.Present {
			m.setDesiredLocked(ch.Name, desiredGen{present: false})
			if old := m.unregisterLocked(ch.Name); old != nil {
				res.Unregistered = append(res.Unregistered, old)
			}
			continue
		}
		prev, had := m.desired[ch.Name]
		moved := !had || !prev.present || prev.gen != ch.Gen
		m.setDesiredLocked(ch.Name, desiredGen{gen: ch.Gen, present: true, binding: ch.Binding})
		client, registered := m.clients[ch.Name]
		if !registered || client == nil {
			continue
		}
		switch {
		case ch.Replaced || (moved && ch.Reconnect),
			client.Generation() != ch.Gen && ch.Config != nil && !config.ConnectionEquivalent(client.GetConfig(), ch.Config),
			// An instance never serves a credential binding it was not
			// built for; a binding move (remove and re-add, OAuth config
			// change) always replaces it.
			!ch.Binding.IsZero() && client.CredentialBinding() != ch.Binding:
			if old := m.unregisterLocked(ch.Name); old != nil {
				res.Unregistered = append(res.Unregistered, old)
			}

		case ch.Config != nil && client.Generation() < ch.Gen:
			if client.SetConfigAt(ch.Config, ch.Gen) {
				res.Restamped = append(res.Restamped, ch.Name)
			}
		}
	}
	m.mu.Unlock()
	m.captureMu.Unlock()

	return res
}

// setDesiredLocked records name's committed state. Caller holds captureMu W +
// mu W.
func (m *Manager) setDesiredLocked(name string, d desiredGen) {
	m.desired[name] = d
}

// checkDesiredLocked is the generation check of every bound action: name must
// be present in the desired view at exactly gen. Caller holds mu (R or W).
func (m *Manager) checkDesiredLocked(name string, gen int64) error {
	d, ok := m.desired[name]
	if !ok || !d.present || d.gen != gen {
		return ErrStaleGeneration
	}
	return nil
}

// CurrentCredentialBinding reports the credential binding of the instance
// registered for name. The RefreshManager applies a refresh
// flight's outcome only when it ran under this binding.
func (m *Manager) CurrentCredentialBinding(name string) (commitview.CredentialBinding, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.clients[name]
	if !ok || c == nil {
		return commitview.CredentialBinding{}, false
	}
	return c.CredentialBinding(), true
}

// SetCommittedViewChecker installs the construction-time check of the
// committed view (storage.Manager.CheckCommittedView): before an instance is
// built for (name, gen), the
// stored row must still carry exactly the committed {gen, incarnation, OAuth
// epoch} the manager holds for it. nil (CLI-only managers) skips it.
func (m *Manager) SetCommittedViewChecker(check func(name string, sg commitview.ServerGen) error) {
	if check == nil {
		m.committedViewCheck.Store(nil)
		return
	}
	m.committedViewCheck.Store(&check)
}

// checkCommittedView runs the construction-time check for (name, gen) with
// no manager lock held (the check is one database View, an L2 leaf). It returns
// ErrStaleGeneration when the stored row was already written by a newer
// commit (or deleted by a removal): the instance must not be built, because
// it would pair this commit's configuration with credentials that no longer
// belong to it. A check that passes is not a lease: a commit that lands
// afterwards leaves the instance with an older binding, whose OAuth writes
// storage refuses, and step 7 unregisters it.
func (m *Manager) checkCommittedView(name string, gen int64) error {
	p := m.committedViewCheck.Load()
	if p == nil || gen <= 0 {
		return nil
	}
	m.mu.RLock()
	d, ok := m.desired[name]
	m.mu.RUnlock()
	if !ok || !d.present || d.gen != gen {
		return ErrStaleGeneration
	}
	if d.binding.IsZero() {
		return nil
	}
	sg := commitview.ServerGen{Gen: gen, Incarnation: d.binding.Incarnation, OAuthEpoch: d.binding.OAuthEpoch}

	err := (*p)(name, sg)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrStaleGeneration):
		m.logger.Debug("Instance not built: the committed view moved on before construction",
			zap.String("server", name), zap.Int64("generation", gen))
		return ErrStaleGeneration
	default:
		// Storage could not be read: the bound OAuth writes still guard the
		// credentials, so a degraded store does not keep the server down.
		m.logger.Warn("Committed-view check failed; building the instance anyway",
			zap.String("server", name), zap.Error(err))
		return nil
	}
}

func (m *Manager) unregisterLocked(name string) *managed.Client {
	old, ok := m.clients[name]
	statsName := name
	if ok {
		delete(m.clients, name)
		if old != nil {
			if cfg := old.GetConfig(); cfg != nil && cfg.Name != "" {
				statsName = cfg.Name
			}
			old.MarkUnregistered()
			// Retire the replaced or removed client while captureMu is
			// held for writing, so a tools/list it captured cannot be
			// applied. Retire also closes the dial
			// gate before this returns. Its barrier takes Gate.mu (L5)
			// under captureMu W + mu W (L3 → L4 → L5, the documented
			// order) and waits only for admitted leaf operations (a dial,
			// one write, a process start), whose connections it has
			// already closed, so it is bounded and never waits on the
			// network or on a manager lock.

			old.Retire()

		}
	}
	m.callStats.Drop(statsName)
	m.dropCallHealth(statsName)
	if !ok {
		return nil
	}
	return old
}

// installLocked is the ONLY code that adds an instance to m.clients. The
// name must not be registered (callers unregister first). Caller holds
// captureMu W + mu W.
func (m *Manager) installLocked(name string, client *managed.Client) {
	m.clients[name] = client
}

// newWiredClientLocked builds an instance for cfg stamped with gen and wires
// the manager's callbacks into it before it becomes
// reachable. Caller holds mu.
func (m *Manager) newWiredClientLocked(id string, cfg *config.ServerConfig, gen int64) (*managed.Client, error) {
	client, err := managed.NewClientAt(id, cfg, gen, m.logger, m.logConfig, m.globalConfig.Load(), m.storage, m.secretResolver)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for server %s: %w", cfg.Name, err)
	}
	client.SetPeerExecutor(m.peerExecutor)
	// The instance's OAuth state is bound to the committed credential
	// binding at gen, taken from the same desired view the generation check
	// read (never from the stored row).
	if d, ok := m.desired[id]; ok && d.present {
		client.SetCredentialBinding(d.binding)
	}

	// Set up notification callback for state changes
	if m.notificationMgr != nil {
		notifierCallback := StateChangeNotifier(m.notificationMgr, cfg.Name)
		existingCallback := client.StateManager.GetStateChangeCallback()
		client.StateManager.SetStateChangeCallback(func(oldState, newState types.ConnectionState, info *types.ConnectionInfo) {
			if existingCallback != nil {
				existingCallback(oldState, newState, info)
			}
			// A superseded instance's transitions (the Disconnect of an
			// instance a commit or replacement unregistered) are notified
			// under the server NAME and would overwrite the status of the
			// instance that now serves it: the replacement could show as
			// disconnected while it is connected. They are not reported.
			if client.Unregistered() {
				return
			}
			notifierCallback(oldState, newState, info)
		})
	}
	// Tool discovery callback for notifications/tools/list_changed.
	if m.toolDiscoveryCallback != nil {
		client.SetToolDiscoveryCallback(m.toolDiscoveryCallback)
	}

	return client, nil
}

// installAtLocked makes the registry serve cfg at gen for id: it keeps a
// registered instance that already runs the same connection settings
// (restamping it when it is behind gen), and otherwise unregisters it and
// installs a new instance. It returns the instance now registered and the
// one it unregistered (nil if none). Caller holds captureMu W + mu W and has
// already checked the generation.
func (m *Manager) installAtLocked(id string, cfg *config.ServerConfig, gen int64) (inst, unregistered *managed.Client, err error) {
	if existing, ok := m.clients[id]; ok && existing != nil {
		d := m.desired[id]
		if config.ConnectionEquivalent(existing.GetConfig(), cfg) && (d.binding.IsZero() || existing.CredentialBinding() == d.binding) {
			m.logger.Debug("Server configuration unchanged, keeping existing client",
				zap.String("id", id),
				zap.String("name", cfg.Name),
				zap.Int64("generation", gen),
				zap.String("current_state", existing.GetState().String()))
			if gen > 0 {
				// Only forward: an instance at this or a newer generation
				// already holds the committed config.
				existing.SetConfigAt(cfg, gen)
			} else {
				existing.SetConfig(cfg)
			}
			return existing, nil, nil
		}
		m.logger.Info("Server configuration changed, replacing existing client",
			zap.String("id", id),
			zap.String("name", cfg.Name),
			zap.Int64("generation", gen),
			zap.String("current_state", existing.GetState().String()),
			zap.Bool("is_connected", existing.IsConnected()))
		unregistered = m.unregisterLocked(id)
	}
	client, err := m.newWiredClientLocked(id, cfg, gen)
	if err != nil {
		return nil, unregistered, err
	}
	m.installLocked(id, client)
	m.logger.Info("Added upstream server configuration",
		zap.String("id", id),
		zap.String("name", cfg.Name),
		zap.Int64("generation", gen),
		zap.Uint64("instance", uint64(client.InstanceID())))
	return client, unregistered, nil
}

// AddServerConfigAt installs (or keeps) the instance serving id at commit
// generation gen without connecting it. It returns ErrStaleGeneration, and
// changes nothing, unless gen is id's current committed generation.
func (m *Manager) AddServerConfigAt(id string, serverConfig *config.ServerConfig, gen int64) error {
	_, err := m.addServerConfigAt(id, serverConfig, gen)
	return err
}

func (m *Manager) addServerConfigAt(id string, serverConfig *config.ServerConfig, gen int64) (*managed.Client, error) {
	if serverConfig == nil {
		return nil, fmt.Errorf("nil config for server %s", id)
	}
	if err := m.checkCommittedView(id, gen); err != nil {
		return nil, err
	}
	m.captureMu.Lock()
	m.mu.Lock()
	if err := m.checkDesiredLocked(id, gen); err != nil {
		m.mu.Unlock()
		m.captureMu.Unlock()
		return nil, err
	}
	inst, old, err := m.installAtLocked(id, serverConfig, gen)
	m.mu.Unlock()
	m.captureMu.Unlock()

	if old != nil {
		_ = old.Disconnect()
	}
	return inst, err
}

// ConnectServerAt connects the instance serving name at generation gen.
// ErrStaleGeneration when gen is not name's committed generation, or the
// registered instance does not run it yet.
func (m *Manager) ConnectServerAt(ctx context.Context, name string, gen int64) error {
	m.mu.RLock()
	err := m.checkDesiredLocked(name, gen)
	client := m.clients[name]
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("server %s not found", name)
	}
	if gen > 0 && client.Generation() != gen {
		return ErrStaleGeneration
	}
	return m.connectInstanceCtx(ctx, name, client)
}

func (m *Manager) DisconnectServerAt(name string, gen int64, inst managed.InstanceID) error {
	m.mu.RLock()
	err := m.checkDesiredLocked(name, gen)
	client := m.clients[name]
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if client == nil || client.InstanceID() != inst {
		return ErrStaleGeneration
	}
	if err := client.Disconnect(); err != nil {
		m.logger.Warn("Error during server disconnect",
			zap.String("server", name),
			zap.Error(err))
	}
	return nil
}

// RemoveServerAt unregisters and disconnects the instance serving name. With
// gen > 0 it acts only when gen is name's current committed generation (a
// disabled server's worker); with gen == 0 it acts only when the committed
// configuration no longer has the name (the supervisor's removal of a server
// the commit already tombstoned). ErrStaleGeneration otherwise.
func (m *Manager) RemoveServerAt(name string, gen int64) error {
	m.captureMu.Lock()
	m.mu.Lock()
	var err error
	if gen == 0 {
		if d, ok := m.desired[name]; !ok || d.present {
			err = ErrStaleGeneration
		}
	} else {
		err = m.checkDesiredLocked(name, gen)
	}
	var old *managed.Client
	if err == nil {
		old = m.unregisterLocked(name)
	}
	if err != nil {
		m.mu.Unlock()
		m.captureMu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.captureMu.Unlock()

	if old != nil {
		m.logger.Info("Removing upstream server",
			zap.String("id", name),
			zap.String("state", old.GetState().String()))
		_ = old.Disconnect()
	}
	return nil
}

func (m *Manager) ReplaceInstanceAt(name string, gen int64, expected *managed.Client, cfg *config.ServerConfig) (*managed.Client, error) {
	if expected == nil {
		return nil, fmt.Errorf("replace %s: no instance to replace", name)
	}
	if cfg == nil {
		cfg = cloneServerConfig(expected.GetConfig())
	}
	if cfg == nil {
		return nil, fmt.Errorf("replace %s: no config", name)
	}
	if err := m.checkCommittedView(name, gen); err != nil {
		return nil, err
	}
	m.captureMu.Lock()
	m.mu.Lock()
	if err := m.checkDesiredLocked(name, gen); err != nil {
		m.mu.Unlock()
		m.captureMu.Unlock()
		return nil, err
	}
	if cur := m.clients[name]; cur != expected {
		m.mu.Unlock()
		m.captureMu.Unlock()
		return nil, ErrStaleGeneration
	}
	old := m.unregisterLocked(name)
	client, err := m.newWiredClientLocked(name, cfg, gen)
	if err == nil {
		m.installLocked(name, client)
	}
	m.mu.Unlock()
	m.captureMu.Unlock()

	if old != nil {
		_ = old.Disconnect()
	}
	if err != nil {
		return nil, err
	}
	m.logger.Info("Replaced upstream server instance",
		zap.String("server", name),
		zap.Int64("generation", gen),
		zap.Uint64("old_instance", uint64(expected.InstanceID())),
		zap.Uint64("new_instance", uint64(client.InstanceID())))
	return client, nil
}

// connectInstanceCtx is the connect step shared by every bound path. A
// connected instance, a connect already in flight, a dial superseded by a
// disconnect and an unregistered instance are not failures; an OAuth
// requirement is not either (the connection completes after login).
func (m *Manager) connectInstanceCtx(parent context.Context, name string, inst *managed.Client) error {
	if inst == nil {
		return fmt.Errorf("server %s not found", name)
	}
	cfg := inst.GetConfig()
	if cfg.ExecutionHost() != "" && (m.peerExecutor == nil || !m.peerExecutor.Online(cfg.ExecutionHost())) {
		return nil
	}
	serverName := name
	if cfg != nil && cfg.Name != "" {
		serverName = cfg.Name
	}
	if inst.IsConnected() {
		m.logger.Debug("Server is already connected, skipping connection attempt",
			zap.String("id", name),
			zap.String("name", serverName))
		return nil
	}
	// The resolved init_timeout bounds the handshake.
	ctx, cancel := context.WithTimeout(parent, m.resolveConnectTimeout())
	defer cancel()
	err := inst.Connect(ctx)
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, managed.ErrConnectAlreadyActive):
		m.logger.Debug("Connect already in progress or established, not starting another",
			zap.String("id", name),
			zap.String("name", serverName),
			zap.String("state", inst.GetState().String()))
		return nil
	case errors.Is(err, managed.ErrConnectAborted):
		m.logger.Debug("Connect superseded by a concurrent disconnect",
			zap.String("id", name),
			zap.String("name", serverName))
		return nil
	case errors.Is(err, managed.ErrInstanceUnregistered):
		m.logger.Debug("Connect skipped: the instance was superseded by a newer commit or replacement",
			zap.String("id", name),
			zap.String("name", serverName),
			zap.Uint64("instance", uint64(inst.InstanceID())))
		return nil
	}
	errStr := err.Error()
	if strings.Contains(errStr, "OAuth") || strings.Contains(errStr, "oauth") || strings.Contains(errStr, "authorization required") {
		m.logger.Info("Server requires OAuth authorization - connection will complete after OAuth login",
			zap.String("id", name),
			zap.String("name", serverName),
			zap.Error(err))
		return nil
	}
	return fmt.Errorf("failed to connect to server %s: %w", serverName, err)
}
