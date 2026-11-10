// Package profile defines profile permissions and request scopes.
package profile

// ProfileScope is the immutable set of servers exposed by a profile.
type ProfileScope struct {
	// Name is the resolved profile slug, used in rejection messages and activity
	// metadata.
	Name string
	// servers is the effective set after unknown-server warn-skip. A non-nil but
	// empty set is a legal "deny everything" profile.
	servers map[string]struct{}
}

// NewProfileScope builds a scope for the named profile over the given effective
// server set. The returned scope is always non-nil (an empty/nil server list is
// a legal profile that allows nothing).
func NewProfileScope(name string, servers []string) *ProfileScope {
	set := make(map[string]struct{}, len(servers))
	for _, s := range servers {
		set[s] = struct{}{}
	}
	return &ProfileScope{Name: name, servers: set}
}

// Allows reports whether the named server is visible under this scope.
//
// A nil receiver means no profile is in effect for this request — no URL slug,
// no session selection, no token pin — so it is not profile-filtered and every
// server is allowed. A non-nil scope allows only servers in its set;
// the empty server name is never allowed for a real scope.
func (p *ProfileScope) Allows(serverName string) bool {
	if p == nil {
		return true
	}
	if serverName == "" {
		return false
	}
	_, ok := p.servers[serverName]
	return ok
}

// DeniesAll reports whether this scope allows nothing at all — an empty
// profile, or the deny-all scope a stale client token pin resolves to. A nil
// receiver is "no profile filtering" and therefore never denies all.
//
// It exists so callers can distinguish "filtered to nothing" from "not
// filtered" without allocating an AllowedServerNames slice, notably to avoid
// lazily creating a per-profile Bleve index for a scope that can return no
// results anyway.
func (p *ProfileScope) DeniesAll() bool {
	return p != nil && len(p.servers) == 0
}

// AllowedServerNames returns the list of server names in this profile scope.
// Returns nil for a nil receiver (allow-all — no restriction list).
// Returns an empty slice for a non-nil scope with no servers (deny-all).
func (p *ProfileScope) AllowedServerNames() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.servers))
	for s := range p.servers {
		out = append(out, s)
	}
	return out
}
