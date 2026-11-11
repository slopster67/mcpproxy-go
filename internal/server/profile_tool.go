package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

func buildSetProfileTool() mcp.Tool {
	return newProxyTool("set_profile",
		mcp.WithDescription("Switch the active profile for THIS session. A profile scopes tool discovery "+
			"(retrieve_tools) and tool calls to a named subset of upstream servers — useful to focus an agent "+
			"on one task domain (e.g. 'research', 'deploy'). The selection persists for the lifetime of the "+
			"current MCP session and applies to subsequent retrieve_tools / call_tool_* / code_execution calls "+
			"on the base /mcp endpoint without re-indexing. Pass an empty string to clear the selection and go "+
			"back to all servers. A locked client cannot switch away from its assigned profile."),
		mcp.WithTitleAnnotation("Set Profile"),
		mcp.WithToolTitle("Set Profile"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("profile",
			mcp.Description("Profile ID from the profiles list to activate for this session. Pass \"\" (empty) to "+
				"clear the active profile and return to all servers."),
		),
	)
}

func setProfileRefusal(ctx context.Context, slug string) *mcp.CallToolResult {
	if ac := auth.AuthContextFromContext(ctx); ac.IsClientCredential() {
		format := profile.SetProfileNotSwitchableRefusalFormat
		if ac.ProfileMode == auth.ProfileModeLocked {
			format = profile.SetProfileLockedRefusalFormat
		}
		return mcp.NewToolResultError(fmt.Sprintf(format, slug))
	}
	return mcp.NewToolResultError(fmt.Sprintf("unknown profile '%s'", slug))
}

func (p *MCPProxyServer) handleSetProfile(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	slug := strings.TrimSpace(request.GetString("profile", ""))

	sessionID := sessionIDFromContext(ctx)
	if sessionID == "" {
		return mcp.NewToolResultError("set_profile requires an active MCP session; no session id is bound to this request"), nil
	}

	profiles := p.profileIndexCurrent(ctx)
	if profiles == nil {
		// Acquire's bounded retries could not pair a config snapshot with its
		// index (a publication storm outran them); profileIndexCurrent only
		// ever returns nil for a scoped caller — an administrator falls back
		// to a fresh build instead. Fail closed with the exact uniform
		// refusal every other non-selectable slug gets: no state change, no
		// disclosure, no build.
		return setProfileRefusal(ctx, slug), nil
	}

	if slug != "" {
		if !profiles.selectable(ctx, slug) {
			return setProfileRefusal(ctx, slug), nil
		}
	}

	p.sessionStore.SetActiveProfile(sessionID, slug)

	if slug != "" {
		p.logger.Info("set_profile: session profile updated",
			zap.String("session_id", sessionID),
			zap.String("profile", slug),
		)
	}

	effectiveProfileName := p.resolveEffectiveProfileForJustSetSlug(ctx, profiles, slug)
	var allowed []string
	if ac := auth.AuthContextFromContext(ctx); ac != nil {
		allowed = ac.AllowedServers
	}
	return setProfileResult(slug, profiles.EffectiveServersFor(effectiveProfileName, allowed))
}

// setProfileResult renders the standard set_profile success payload.
func setProfileResult(activeProfile string, servers []string) (*mcp.CallToolResult, error) {
	if servers == nil {
		servers = []string{}
	}
	profileSource := string(profile.SourceNone)
	if activeProfile != "" {
		profileSource = string(profile.SourceSession)
	}
	payload := map[string]interface{}{
		"active_profile": activeProfile,
		"profile_source": profileSource,
		"servers":        servers,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode set_profile result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}

// allServerNames returns the names of every configured server (the "all
// servers" set returned when a profile selection is cleared).
func allServerNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s != nil {
			names = append(names, s.Name)
		}
	}
	return names
}

type profileIndex struct {
	cfg  *config.Config
	byID map[string]int // ID → position in cfg.Profiles

	// serverPos maps a configured server's name to its position in
	// cfg.Servers (first named, non-nil occurrence), so a granted name
	// resolves to its membership bit in O(1).
	serverPos map[string]int

	// words is the bitset length in uint64 words: ceil(len(cfg.Servers)/64).
	// members holds len(cfg.Profiles) consecutive bitsets of that length —
	// bit i of profile p's set is on when cfg.Servers[i] is one of p's
	// declared servers (EffectiveServers as a set). nonEmpty[p] is on when
	// that set has at least one bit — the whole reach test for a reader
	// that may see every server. none is the all-zero placeholder read for
	// a slug the snapshot has no profile for.
	words    int
	members  []uint64
	nonEmpty []bool
	none     []uint64

	// declaredOccurrences[p] maps a server name to the ordered list of
	// indices at which it appears in cfg.Profiles[p].Servers — that
	// profile's OWN declared order, duplicates tracked individually.
	// Precomputed once per snapshot (same amortization as members/
	// serverPos) so effectiveServersForCandidate's restricted-grant branch
	// can render "profile-declared order, duplicates kept" by touching only
	// the caller's own grant, never a walk of the profile's full declared
	// list per request: walking `declared` would cost O(profile size) and
	// leak the size of a profile the caller cannot fully see through timing.
	declaredOccurrences []map[string][]int

	policies []*profile.CompiledPolicy
}

func newProfileIndex(cfg *config.Config) *profileIndex {
	idx := &profileIndex{cfg: cfg, byID: map[string]int{}}
	if cfg == nil {
		return idx
	}
	idx.byID = make(map[string]int, len(cfg.Profiles))
	for i := range cfg.Profiles {
		if _, dup := idx.byID[cfg.Profiles[i].ID]; !dup {
			idx.byID[cfg.Profiles[i].ID] = i
		}
	}

	// Reach sets: server name → position once, then one bit per declared
	// server that exists in the snapshot (the EffectiveServers rule; nil
	// entries and empty names excluded — CanAccessServer never grants an
	// empty name, so such a server is reachable by nobody).
	idx.serverPos = make(map[string]int, len(cfg.Servers))
	for i, s := range cfg.Servers {
		if s == nil || s.Name == "" {
			continue
		}
		if _, dup := idx.serverPos[s.Name]; !dup {
			idx.serverPos[s.Name] = i
		}
	}
	idx.words = (len(cfg.Servers) + 63) / 64
	idx.none = make([]uint64, idx.words)
	idx.members = make([]uint64, len(cfg.Profiles)*idx.words)
	idx.nonEmpty = make([]bool, len(cfg.Profiles))
	idx.declaredOccurrences = make([]map[string][]int, len(cfg.Profiles))
	idx.policies = make([]*profile.CompiledPolicy, len(cfg.Profiles))
	for p := range cfg.Profiles {
		set := idx.membersOf(p)
		occ := make(map[string][]int, len(cfg.Profiles[p].Servers))
		for declaredPos, name := range cfg.Profiles[p].Servers {
			if i, ok := idx.serverPos[name]; ok {
				set[i/64] |= 1 << (uint(i) % 64)
				idx.nonEmpty[p] = true
				occ[name] = append(occ[name], declaredPos)
			}
		}
		idx.declaredOccurrences[p] = occ
		idx.policies[p] = profile.Compile(&cfg.Profiles[p])
	}
	return idx
}

// PolicyAt returns the compiled policy for the profile at an
// ALREADY resolved position (candidate < 0: no such profile) — the int-keyed
// counterpart to PolicyFor, for a caller that already paid for
// position(slug) elsewhere in the same request.
func (idx *profileIndex) PolicyAt(candidate int) *profile.CompiledPolicy {
	if candidate < 0 || candidate >= len(idx.policies) {
		return nil
	}
	return idx.policies[candidate]
}

// PolicyFor returns the compiled policy for the named profile, or
// nil when the snapshot has no such profile.
func (idx *profileIndex) PolicyFor(slug string) *profile.CompiledPolicy {
	return idx.PolicyAt(idx.position(slug))
}

// membersOf returns profile p's reach bitset, or the all-zero placeholder
// for p < 0 (no such profile).
func (idx *profileIndex) membersOf(p int) []uint64 {
	if p < 0 {
		return idx.none
	}
	return idx.members[p*idx.words : (p+1)*idx.words]
}

// position resolves one slug to its position in cfg.Profiles in O(1), or -1
// when the snapshot has no such profile.
func (idx *profileIndex) position(slug string) int {
	if i, ok := idx.byID[slug]; ok {
		return i
	}
	return -1
}

func (idx *profileIndex) EffectiveServersFor(profileName string, allowed []string) []string {
	if profileName == "" {
		return idx.effectiveServersForAllowed(allowed)
	}
	return idx.effectiveServersForCandidate(idx.position(profileName), allowed)
}

// effectiveServersForCandidate is EffectiveServersFor for an ALREADY resolved
// profile position.
func (idx *profileIndex) effectiveServersForCandidate(candidate int, allowed []string) []string {
	if idx.cfg == nil || candidate < 0 || candidate >= len(idx.cfg.Profiles) || len(allowed) == 0 {
		return nil
	}
	if hasWildcardGrant(allowed) {
		declared := idx.cfg.Profiles[candidate].Servers
		out := make([]string, 0, len(declared))
		for _, name := range declared {
			if _, ok := idx.serverPos[name]; ok {
				out = append(out, name)
			}
		}
		return out
	}
	// Restricted grant: touch only the caller's own allowed entries — never
	// declared (the profile's full, possibly hidden-from-this-caller, size).
	// declaredOccurrences[candidate] was precomputed once per snapshot
	// (newProfileIndex), so a hit costs one map lookup per grant entry (plus
	// one append per occurrence, for the rare authored duplicate — bounded
	// by the admin-authored declared list, never by anything the caller
	// controls) instead of a walk of the profile's own declared list.
	//
	// allowed itself may repeat a name (an unvalidated AllowedServers list);
	// dedupeSeen guards against that so a repeated grant entry doesn't
	// re-expand the same occurrences: only declared's own duplicates drive
	// the output count.
	occ := idx.declaredOccurrences[candidate]
	type hit struct {
		pos  int
		name string
	}
	hits := make([]hit, 0, len(allowed))
	dedupeSeen := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		if _, dup := dedupeSeen[name]; dup {
			continue
		}
		dedupeSeen[name] = struct{}{}
		for _, pos := range occ[name] {
			hits = append(hits, hit{pos: pos, name: name})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

// effectiveServersForAllowed is EffectiveServersFor("", allowed): every
// configured server allowed grants, in config order — the "no profile in
// effect" / cleared-selection rendering. A wildcard grant is unrestricted by
// definition (the same full list, at the same cost administrators pay for
// it), so it returns allServerNames
// directly rather than resolving each configured server through allowed. A
// restricted grant resolves each of the reader's OWN entries to its config
// position via serverPos — O(1) each — and sorts the (small) result by that
// position, so it reproduces config order without ever walking cfg.Servers:
// its cost is O(len(allowed) log len(allowed)), never the fleet's.
func (idx *profileIndex) effectiveServersForAllowed(allowed []string) []string {
	if idx.cfg == nil || len(allowed) == 0 {
		return nil
	}
	if hasWildcardGrant(allowed) {
		return allServerNames(idx.cfg)
	}
	type grantHit struct {
		pos  int
		name string
	}
	hits := make([]grantHit, 0, len(allowed))
	seen := make(map[int]struct{}, len(allowed))
	for _, name := range allowed {
		pos, ok := idx.serverPos[name]
		if !ok {
			continue
		}
		if _, dup := seen[pos]; dup {
			continue
		}
		seen[pos] = struct{}{}
		hits = append(hits, grantHit{pos: pos, name: name})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].pos < hits[j].pos })
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

// hasWildcardGrant reports whether allowed carries the "*" entry
// AuthContext.CanAccessServer treats as unrestricted.
func hasWildcardGrant(allowed []string) bool {
	for _, name := range allowed {
		if name == "*" {
			return true
		}
	}
	return false
}

// hasMember reports whether profile candidate's reach set is non-empty —
// false for the placeholder (candidate < 0).
func (idx *profileIndex) hasMember(candidate int) bool {
	return candidate >= 0 && idx.nonEmpty[candidate]
}

// reach reports whether the caller can enumerate at least one server of
// profile candidate (-1: no such profile) — the rule behind
// selectableNames, i.e.
// len(callerVisibleServers(ctx, p.EffectiveServers(cfg))) > 0, without
// either allocation. Its cost is O(|reader grant|), never O(|fleet|): a
// reader that may see every server (administrator, absent context, or a
// wildcard entry) needs one test — is the set non-empty — and a restricted
// reader needs one membership test per entry of its own AllowedServers
// (the same "*" / exact-name rule as AuthContext.CanAccessServer; an empty
// name never matches). It never returns early and does the same work for
// the placeholder set, an empty profile and one declaring every server, so
// neither the outcome, the candidate nor the number of configured servers
// can be told from the work — only the caller's own grant, which it knows.
func (idx *profileIndex) reach(ctx context.Context, candidate int) bool {
	if idx.cfg == nil {
		return false
	}
	if !auth.IsScopedCaller(ctx) {
		return idx.hasMember(candidate)
	}
	members := idx.membersOf(candidate)
	reach := false
	// IsScopedCaller guarantees a non-nil, non-admin AuthContext.
	for _, name := range auth.AuthContextFromContext(ctx).AllowedServers {
		hit := false
		if name == "*" {
			hit = idx.hasMember(candidate)
		} else if i, ok := idx.serverPos[name]; ok {
			hit = members[i/64]&(1<<(uint(i)%64)) != 0
		}
		if hit {
			reach = true
		}
	}
	return reach
}

// selectable reports whether the caller may select the profile named slug. It
// is the predicate of the URL gate and of set_profile's
// admission, and must never consult the selectable list: whatever the
// outcome (slug absent, profile out of reach, pin deleted, pin zero-reach,
// pin mismatch, empty fleet) it does the same work — one lookup of the slug,
// one lookup of the pin when the caller is pinned, and exactly one
// allocation-free reach test of the caller's own grant against the
// candidate's precomputed set or the all-zero placeholder when there is none
// — so no branch can be told from another by its cost, and none of it
// depends on how many other profiles exist, on how many servers the
// candidate declares or on how many servers are configured.
func (idx *profileIndex) selectable(ctx context.Context, slug string) bool {
	candidate := idx.position(slug)
	if !idx.pinAllowsSelection(ctx, slug) {
		candidate = -1
	}
	reach := idx.reach(ctx, candidate)
	// Administrators (and absent contexts) select any configured profile,
	// including empty or ghost ones; everyone else needs reach.
	pin := profilePinFromContext(ctx)
	needsReach := pin != "" || auth.IsScopedCaller(ctx)
	return candidate >= 0 && (!needsReach || reach)
}

func (idx *profileIndex) pinAllowsSelection(ctx context.Context, slug string) bool {
	if ac := auth.AuthContextFromContext(ctx); ac.IsClientCredential() && ac.ProfileMode == auth.ProfileModeLocked {
		return slug == ac.ProfilePin
	}

	if pin, mode, ok := clientCredentialFromContext(ctx); ok && mode == auth.ProfileModeSwitchable {
		if pin == "" {
			return true
		}
		return admittedBySwitchableTo(idx.PolicyFor(pin), pin, slug)
	}

	if pin := profilePinFromContext(ctx); pin != "" {
		return slug == pin
	}
	return true
}

// profileIndexCache holds the profileIndex of the config snapshot a request
// decides over. Config snapshots are replaced, never mutated in place
// (configsvc copy-on-write; every reload publishes a new *Config), so an
// index is keyed on its snapshot's pointer identity and retains the snapshot
// it was built from (idx.cfg), so its address cannot be recycled under it.
// The zero value is ready to use.
//
// A request must decide over the snapshot that is actually PUBLISHED —
// runtime.Config() is the one atomic publication boundary every reader
// agrees on — never over a snapshot merely prepared ahead of it. Published
// returns the prepared pair whose cfg equals the caller's own read of
// runtime.Config(): the latest pair when it already covers it, or the
// previous one during the narrow window in which the pre-publish observer
// has warmed the NEXT snapshot but configsvc has not yet stored it (warm
// already points past runtime.Config(); previous still matches it). Taking
// the unconditional warm slot — always the latest prepared pair, whether or
// not it was stored yet — would let a request be admitted against the next
// config while resolveActiveProfileIn's pin tier, reading runtime.Config()
// independently, still answered the previous one: admission and the
// effective scope could disagree within one request.
// Keeping the last TWO prepared pairs closes that window without ever
// building: the observer runs under updateMu, so at most one
// prepared-but-not-yet-stored pair can exist at any time, and "latest, else
// previous" is therefore always either the published pair or the one about
// to replace it — never a third, older one a request could still be reading
// runtime.Config() as.
//
// For is the build fallback when no prepared pair matches: it never writes
// the warm slot and builds into the lazy slot, so it cannot roll the warm
// index back.
type profileIndexCache struct {
	warm     atomic.Pointer[profileIndex] // latest prepared pair
	previous atomic.Pointer[profileIndex] // second-latest prepared pair (see Published)
	lazy     atomic.Pointer[profileIndex]

	// warmMu serialises warm-slot writers; observed flips once the
	// pre-publish observer has warmed a snapshot, after which the event-driven
	// warm defers to it (a late event's build could otherwise land over a
	// newer observer build).
	warmMu   sync.Mutex
	observed bool
}

func (c *profileIndexCache) Current() *profileIndex {
	return c.warm.Load()
}

// Published returns the prepared pair whose snapshot is published — cfg ==
// the caller's own runtime.Config() read — or nil when neither the latest
// nor the previous prepared pair covers it: a snapshot more than one
// publication further back than what warmPublishing has seen. Callers fall
// back to For(published) in that case.
func (c *profileIndexCache) Published(published *config.Config) *profileIndex {
	if idx := c.warm.Load(); idx != nil && idx.cfg == published {
		return idx
	}
	if idx := c.previous.Load(); idx != nil && idx.cfg == published {
		return idx
	}
	return nil
}

// warmPublishing indexes cfg as the configsvc pre-publish observer, on the
// exact snapshot about to be published, and returns the index. It runs under
// the config update mutex, so it does one insertion per profile and nothing
// else. The prior warm pair is demoted to previous (never dropped outright)
// so a request whose own runtime.Config() read still answers it — because
// this snapshot has not been Store'd yet — can still find it via Published.
// Requests that already hold either pair keep it (Current/Published hand out
// the pointer, not the slot).
func (c *profileIndexCache) warmPublishing(cfg *config.Config) *profileIndex {
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	c.observed = true
	old := c.warm.Load()
	if old != nil && old.cfg == cfg {
		return old
	}
	idx := newProfileIndex(cfg)
	if old != nil {
		c.previous.Store(old)
	}
	c.warm.Store(idx)
	return idx
}

// warmCurrent indexes the runtime's current snapshot cfg from outside the
// publication path (construction, config events). It is the whole warm path
// for the initial snapshot — NewService stores it without running any
// observer — and belt-and-braces afterwards: once the observer has warmed a
// snapshot it is a no-op, so a late event can never roll the slot back.
func (c *profileIndexCache) warmCurrent(cfg *config.Config) {
	c.warmMu.Lock()
	defer c.warmMu.Unlock()
	if c.observed {
		return
	}
	if idx := c.warm.Load(); idx != nil && idx.cfg == cfg {
		return
	}
	c.warm.Store(newProfileIndex(cfg))
}

// For returns the index for cfg: the warm slot when it covers cfg, else the
// lazy slot, else a build of its own into the lazy slot. It never writes the
// warm slot. Two goroutines racing on a lazy build may both build; either
// result is correct and the later Store wins.
func (c *profileIndexCache) For(cfg *config.Config) *profileIndex {
	if idx := c.warm.Load(); idx != nil && idx.cfg == cfg {
		return idx
	}
	if idx := c.lazy.Load(); idx != nil && idx.cfg == cfg {
		return idx
	}
	idx := newProfileIndex(cfg)
	c.lazy.Store(idx)
	return idx
}

// profileIndexAcquireRetries bounds Acquire's read-and-match loop. Each
// iteration is O(1) (Published never builds), so the bound caps the cost of
// even a publication storm at O(retries) — never a profile-index build.
const profileIndexAcquireRetries = 8

func (c *profileIndexCache) Acquire(readCfg func() *config.Config) *profileIndex {
	for i := 0; i < profileIndexAcquireRetries; i++ {
		cfg := readCfg()
		if idx := c.Published(cfg); idx != nil {
			return idx
		}
	}
	return nil
}

// setProfileServerTool wraps buildSetProfileTool as a ServerTool for routing-mode registration.
func (p *MCPProxyServer) setProfileServerTool() mcpserver.ServerTool {
	return mcpserver.ServerTool{Tool: buildSetProfileTool(), Handler: p.handleSetProfile}
}

func (p *MCPProxyServer) profileIndexCurrent(ctx context.Context) *profileIndex {
	if idx := p.mainServer.profileIndexes.Acquire(p.currentConfig); idx != nil {
		return idx
	}
	if auth.IsScopedCaller(ctx) {
		return nil
	}
	return p.mainServer.profileIndexes.For(p.currentConfig())
}

// profileIndexFor returns the profile index for an EXPLICIT snapshot cfg —
// the one resolveActiveProfileIn and its callers already hold, never a fresh
// runtime.Config() read (cfg may be older than the live config, e.g. the
// snapshot handleSetProfile admitted a selection against). Over a wired
// runtime it prefers the cache's Published pair — an O(1) pointer-identity
// match against a snapshot the warm path already indexed, never a build —
// and falls back to For only when cfg is not one of the two most recently
// prepared pairs (a snapshot captured further back than the cache retains).
// Unlike profileIndexCurrent it never reads currentConfig() itself and never
// returns nil — cfg is already fixed, so there is nothing to (re-)match
// atomically the way Acquire does.
func (p *MCPProxyServer) profileIndexFor(cfg *config.Config) *profileIndex {
	if idx := p.mainServer.profileIndexes.Published(cfg); idx != nil {
		return idx
	}
	return p.mainServer.profileIndexes.For(cfg)
}
