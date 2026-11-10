package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// ProfileSessionHook updates live sessions after deletion or reassignment.
type ProfileSessionHook interface {
	ProfileDeleted(name string)
	// ProfileTokensMoved is called after a delete-with-reassign moved these
	// tokens' pins: their sessions' base changed, so each is re-listed and its
	// stored selection cleared.
	ProfileTokensMoved(tokenNames []string)
}

type ProfilesService struct {
	rt *Runtime

	statsMu    sync.Mutex
	statsCache map[string]cachedStats
	now        func() time.Time
}

type cachedStats struct {
	at    time.Time
	stats ActivityStats24h
}

// statsTTL is how long the 24 h activity rollup is reused.
const statsTTL = 30 * time.Second

// Counter is a calls/blocked pair over the last 24 hours.
type Counter struct {
	Calls   int
	Blocked int
}

// ActivityStats24h is the last-24 h rollup of tool calls and blocked calls by
// profile and by client.
type ActivityStats24h struct {
	ByProfile map[string]Counter
	ByClient  map[string]Counter
}

func newProfilesService(rt *Runtime) *ProfilesService {
	return &ProfilesService{rt: rt, statsCache: map[string]cachedStats{}, now: time.Now}
}

// ProfilesService returns the runtime's single profiles service.
func (r *Runtime) ProfilesService() *ProfilesService { return r.profilesService }

// SetProfileEvaluator installs the server-side evaluator (the SetBindingGuard
// pattern). Until it is installed, effective-tools, try and explain answer
// ErrEvaluatorUnavailable.
func (r *Runtime) SetProfileEvaluator(e ProfileEvaluator) {
	r.profileEvaluatorMu.Lock()
	r.profileEvaluator = e
	r.profileEvaluatorMu.Unlock()
}

// SetSessionHook installs live-session updates for deletion and reassignment.
func (s *ProfilesService) SetSessionHook(h ProfileSessionHook) {
	s.rt.profileEvaluatorMu.Lock()
	s.rt.profileSessionHook = h
	s.rt.profileEvaluatorMu.Unlock()
}

func (s *ProfilesService) evaluator() ProfileEvaluator {
	s.rt.profileEvaluatorMu.RLock()
	defer s.rt.profileEvaluatorMu.RUnlock()
	return s.rt.profileEvaluator
}

func (s *ProfilesService) sessionHook() ProfileSessionHook {
	s.rt.profileEvaluatorMu.RLock()
	defer s.rt.profileEvaluatorMu.RUnlock()
	return s.rt.profileSessionHook
}

// --- reads -----------------------------------------------------------------

func (s *ProfilesService) List(ctx context.Context) (*ProfileList, error) {
	cfg := s.rt.Config()
	if cfg == nil {
		return nil, ErrConfigUnavailable
	}
	stats := s.Stats24h(ctx)
	used, err := s.usedByAll(cfg)
	if err != nil {
		return nil, err
	}
	out := &ProfileList{Profiles: make([]ProfileView, 0, len(cfg.Profiles))}

	for i := range cfg.Profiles {
		v := s.view(ctx, cfg, &cfg.Profiles[i], stats)
		v.UsedBy = used[cfg.Profiles[i].ID]
		out.Profiles = append(out.Profiles, v)
	}
	return out, nil
}

// Get returns one profile's view, or *ProfileNotFoundError.
func (s *ProfilesService) Get(ctx context.Context, name string) (*ProfileView, error) {
	cfg := s.rt.Config()
	if cfg == nil {
		return nil, ErrConfigUnavailable
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID != name {
			continue
		}
		v := s.view(ctx, cfg, &cfg.Profiles[i], s.Stats24h(ctx))
		u, err := s.UsedBy(name)
		if err != nil {
			return nil, err
		}
		v.UsedBy = &u
		return &v, nil
	}
	return nil, &ProfileNotFoundError{Name: name}
}

// view builds a ProfileView from stored config plus its tool counts and 24 h
// stats.
func (s *ProfilesService) view(ctx context.Context, cfg *config.Config, p *config.ProfileConfig, stats ActivityStats24h) ProfileView {
	v := ProfileViewFromConfig(cfg, p)
	if ev := s.evaluator(); ev != nil {
		v.ToolCounts = ev.ToolCounts(ctx, p.ID)
	}
	c := stats.ByProfile[p.ID]
	v.Calls24h, v.Blocked24h = c.Calls, c.Blocked
	return v
}

// ProfileViewFromConfig projects a stored profile and its derived effective
// values (no tool counts, stats or used_by: those need the runtime). It copies
// every slice and map, so the view never aliases the live config.
func ProfileViewFromConfig(cfg *config.Config, p *config.ProfileConfig) ProfileView {
	v := ProfileView{
		ID:               p.ID,
		Name:             p.Name,
		Servers:          append([]string{}, p.Servers...),
		EffectiveServers: p.EffectiveServers(cfg),
	}
	if p.SelectedTools != nil {
		v.SelectedTools = map[string][]string{}
		for server, tools := range p.SelectedTools {
			v.SelectedTools[server] = append([]string{}, tools...)
		}
	}
	if p.SwitchableTo != nil {
		st := append([]string{}, (*p.SwitchableTo)...)
		v.SwitchableTo = &st
	}
	return v
}

// usedByAll computes used_by for every profile in one pass over the token
// store.
func (s *ProfilesService) usedByAll(cfg *config.Config) (map[string]*UsedBy, error) {
	tokens, err := s.tokens()
	if err != nil {
		return nil, err
	}
	out := make(map[string]*UsedBy, len(cfg.Profiles))
	for i := range cfg.Profiles {
		out[cfg.Profiles[i].ID] = &UsedBy{Clients: []UsedByClient{}}
	}
	for i := range tokens {
		t := &tokens[i]
		u, ok := out[t.ProfilePin]
		if !ok {
			continue
		}
		if t.Kind == auth.KindClient {
			u.Clients = append(u.Clients, UsedByClient{ID: t.ClientID, Mode: t.ProfileMode})
		}
	}
	for _, u := range out {
		sortUsedBy(u)
	}
	return out, nil
}

func sortUsedBy(u *UsedBy) {
	sort.Slice(u.Clients, func(i, j int) bool { return u.Clients[i].ID < u.Clients[j].ID })
}

func (s *ProfilesService) tokens() ([]auth.ClientCredential, error) {
	sm := s.rt.StorageManager()
	if sm == nil {
		return nil, nil
	}
	all, err := sm.ListClientCredentials()
	if err != nil {
		return nil, fmt.Errorf("cannot read tokens: %w", err)
	}
	return all, nil
}

// UsedBy lists what points at the named profile.
func (s *ProfilesService) UsedBy(name string) (UsedBy, error) {
	u := UsedBy{Clients: []UsedByClient{}}
	tokens, err := s.tokens()
	if err != nil {
		return u, err
	}
	for i := range tokens {
		t := &tokens[i]
		if t.ProfilePin != name {
			continue
		}
		if t.Kind == auth.KindClient {
			u.Clients = append(u.Clients, UsedByClient{ID: t.ClientID, Mode: t.ProfileMode})
		}
	}
	sortUsedBy(&u)
	return u, nil
}

// Stats24h returns the last-24 h rollup of calls and blocked calls by profile
// and by client, cached for 30 s. It is one streaming pass over a 24 h
// window: no new aggregate exists to keep.
func (s *ProfilesService) Stats24h(ctx context.Context) ActivityStats24h {
	const key = ""
	now := s.now()
	s.statsMu.Lock()
	if c, ok := s.statsCache[key]; ok && now.Sub(c.at) < statsTTL {
		s.statsMu.Unlock()
		return c.stats
	}
	s.statsMu.Unlock()

	stats := ActivityStats24h{ByProfile: map[string]Counter{}, ByClient: map[string]Counter{}}
	filter := storage.ActivityFilter{
		Types:     []string{string(storage.ActivityTypeToolCall), string(storage.ActivityTypePolicyDecision)},
		StartTime: now.Add(-24 * time.Hour),
	}
	for rec := range s.rt.StreamActivities(filter) {
		if ctx.Err() != nil {
			break
		}
		blocked := rec.Status == "blocked"
		isCall := rec.Type == storage.ActivityTypeToolCall
		if !isCall && !blocked {
			continue
		}
		add := func(m map[string]Counter, k string) {
			if k == "" {
				return
			}
			c := m[k]
			if isCall {
				c.Calls++
			}
			if blocked {
				c.Blocked++
			}
			m[k] = c
		}
		add(stats.ByProfile, rec.EffectiveProfile())
		add(stats.ByClient, rec.ClientID)
	}

	s.statsMu.Lock()
	s.statsCache[key] = cachedStats{at: now, stats: stats}
	s.statsMu.Unlock()
	return stats
}

// --- evaluator-backed reads ---------------------------------------------------

// EffectiveTools delegates to the evaluator (ErrEvaluatorUnavailable when none).
func (s *ProfilesService) EffectiveTools(ctx context.Context, name string, opt EffectiveToolsOptions) (*EffectiveToolsResult, error) {
	ev := s.evaluator()
	if ev == nil {
		return nil, ErrEvaluatorUnavailable
	}
	return ev.EffectiveTools(ctx, name, opt)
}

// Explain delegates to the evaluator (ErrEvaluatorUnavailable when none).
func (s *ProfilesService) Explain(ctx context.Context, subject profile.AccessSubject, tool string) (*AccessExplanation, error) {
	ev := s.evaluator()
	if ev == nil {
		return nil, ErrEvaluatorUnavailable
	}
	return ev.Explain(ctx, subject, tool)
}

// --- validation ---------------------------------------------------------------

// validateProfilesFor runs the ONE profile validator over cfg and returns the
// warnings about the named profile; a fatal rule is a *ValidationError naming
// the field.
func validateProfilesFor(cfg *config.Config, name string) ([]string, error) {
	warnings, err := config.ValidateProfilesDetailed(cfg)
	if err != nil {
		var pe *config.ProfileValidationError
		if errors.As(err, &pe) {
			return nil, &ValidationError{Field: pe.Field, Message: pe.Message}
		}
		return nil, &ValidationError{Message: err.Error()}
	}
	out := []string{}
	for _, w := range warnings {
		if w.Profile == name {
			out = append(out, w.Message)
		}
	}
	return out, nil
}

func indexOfProfile(cfg *config.Config, name string) int {
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == name {
			return i
		}
	}
	return -1
}

// --- mutations ------------------------------------------------------------------

// Create generates a stable ID and adds the profile.
func (s *ProfilesService) Create(ctx context.Context, a Actor, p config.ProfileConfig) (*WriteResult, error) {
	if p.ID != "" {
		return nil, &ValidationError{Field: "id", Message: "profile IDs are generated by the server"}
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	p.ID = "profile-" + id.String()
	p.Name = strings.TrimSpace(p.Name)
	var warnings []string
	err = s.rt.mutateProfiles(ctx, a, func(d *config.Config) (ChangeHint, error) {
		d.Profiles = append(d.Profiles, p)
		var err error
		warnings, err = validateProfilesFor(d, p.ID)
		return ChangeHint{}, err
	}, TokenRewrite{})
	if err != nil {
		return nil, err
	}
	return s.written(ctx, p.ID, warnings)
}

func (s *ProfilesService) written(ctx context.Context, name string, warnings []string) (*WriteResult, error) {
	v, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	if warnings == nil {
		warnings = []string{}
	}
	return &WriteResult{Profile: *v, Warnings: warnings}, nil
}

func (s *ProfilesService) Update(ctx context.Context, a Actor, name string, p config.ProfileConfig) (*WriteResult, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.ID != name {
		return nil, &IDMismatchError{Path: name, Body: p.ID}
	}
	var warnings []string
	err := s.rt.mutateProfiles(ctx, a, func(d *config.Config) (ChangeHint, error) {
		i := indexOfProfile(d, name)
		if i < 0 {
			return ChangeHint{}, &ProfileNotFoundError{Name: name}
		}
		d.Profiles[i] = p
		var err error
		if warnings, err = validateProfilesFor(d, name); err != nil {
			return ChangeHint{}, err
		}
		return ChangeHint{}, nil
	}, TokenRewrite{})
	if err != nil {
		return nil, err
	}
	return s.written(ctx, name, warnings)
}

// Rename changes only the display name. IDs, bindings and live selections remain stable.
func (s *ProfilesService) Rename(ctx context.Context, a Actor, id, newName string) (*RenameResult, error) {
	newName = strings.TrimSpace(newName)
	err := s.rt.mutateProfiles(ctx, a, func(d *config.Config) (ChangeHint, error) {
		i := indexOfProfile(d, id)
		if i < 0 {
			return ChangeHint{}, &ProfileNotFoundError{Name: id}
		}
		previousName := d.Profiles[i].Name
		d.Profiles[i].Name = newName
		if _, err := validateProfilesFor(d, id); err != nil {
			return ChangeHint{}, err
		}
		return ChangeHint{Kind: profile.ChangeRename, Profile: id, PreviousProfile: id, Diff: map[string]interface{}{"name": map[string]interface{}{"from": previousName, "to": newName}}}, nil
	}, TokenRewrite{})
	if err != nil {
		return nil, err
	}
	v, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &RenameResult{Profile: *v}, nil
}

func movedRefs(u UsedBy) MovedRefs {
	m := MovedRefs{Clients: []string{}}
	for _, c := range u.Clients {
		m.Clients = append(m.Clients, c.ID)
	}
	return m
}

func (s *ProfilesService) Delete(ctx context.Context, a Actor, name, reassignTo string, force bool) (*DeleteResult, error) {
	res := &DeleteResult{Deleted: name, Moved: MovedRefs{Clients: []string{}}}
	tokens := TokenRewrite{}
	if reassignTo != "" && reassignTo != name {
		tokens = TokenRewrite{From: name, To: reassignTo}
	}
	err := s.rt.mutateProfiles(ctx, a, func(d *config.Config) (ChangeHint, error) {
		i := indexOfProfile(d, name)
		if i < 0 {
			return ChangeHint{}, &ProfileNotFoundError{Name: name}
		}
		u, err := s.UsedBy(name)
		if err != nil {
			return ChangeHint{}, err
		}

		if !u.Empty() && reassignTo == "" && !force {
			return ChangeHint{}, &ProfileInUseError{UsedBy: u}
		}
		if reassignTo != "" {
			if reassignTo == name || indexOfProfile(d, reassignTo) < 0 {
				return ChangeHint{}, &ValidationError{Field: "reassign_to", Message: fmt.Sprintf("reassign_to must name another existing profile, got %q", reassignTo)}
			}
			res.Moved = movedRefs(u)
			res.movedTokenNames = u.tokenNames()

		}
		d.Profiles = append(d.Profiles[:i:i], d.Profiles[i+1:]...)
		for j := range d.Profiles {
			if d.Profiles[j].SwitchableTo == nil {
				continue
			}
			kept := []string{}
			for _, t := range *d.Profiles[j].SwitchableTo {
				if t != name {
					kept = append(kept, t)
				}
			}
			d.Profiles[j].SwitchableTo = &kept
		}
		diff := map[string]interface{}{"force": force}
		if reassignTo != "" {
			diff["reassign_to"] = reassignTo
			diff["moved"] = map[string]interface{}{"clients": res.Moved.Clients}
		}

		return ChangeHint{Kind: profile.ChangeDelete, Profile: reassignTo, PreviousProfile: name, Diff: diff}, nil
	}, tokens)
	if err != nil {
		return nil, err
	}
	if h := s.sessionHook(); h != nil {
		h.ProfileDeleted(name)
		if reassignTo != "" {
			h.ProfileTokensMoved(res.movedTokenNames)
		}
	}
	return res, nil
}

// Classify sets (or, with tier "", removes) the classification of one exact
// server:tool under the named profile, atomically under the write lock, and
// records a `classify` change. Like a create or an update it returns the
// stored profile and the validator's warnings about it.
