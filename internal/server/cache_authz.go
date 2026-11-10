package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/v2/mcp"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/cache"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// cacheAuthorization derives the authorization a request acts under, in the
// shape the cache stamps on entries and checks on reads:
// caller kind, principal, agent server scope, permission tier, profile pin and
// the effective profile (token pin > URL > session set_profile).
//
// A request with no auth context at all is the unauthenticated /mcp path,
// which the auth middleware would otherwise have handed an anonymous admin
// context — so it is recorded as such rather than as an agent with nothing.
func (p *MCPProxyServer) cacheAuthorization(ctx context.Context) cache.Authorization {
	name, scope, idx := p.resolveActiveProfileWithIndex(ctx)
	return p.cacheAuthorizationWith(ctx, name, scope, idx)
}

// cacheAuthorizationWith is cacheAuthorization for a handler that has already
// resolved the request's effective profile — the same (name, scope, idx)
// triple it authorized the call against (idx is the (index, snapshot) pair
// resolveActiveProfileWithIndex resolved that (name, scope) pair from — see
// its doc comment). Handlers capture the producer stamp HERE, before the
// upstream call, not when the response comes back to be truncated: a profile
// deleted or narrowed while the call is in flight must not re-stamp a
// response that was authorized under the wider scope.
func (p *MCPProxyServer) cacheAuthorizationWith(ctx context.Context, profileName string, scope *profile.ProfileScope, idx *profileIndex) cache.Authorization {
	a := cache.Authorization{CallerKind: cache.CallerKindAnonymous}
	var callerAllowed []string
	callerBounded := false
	if ac := auth.AuthContextFromContext(ctx); ac != nil {
		switch {
		case ac.Type == auth.AuthTypeClient:
			a.CallerKind = cache.CallerKindAgent
			a.Principal = ac.AgentName
			a.AllowedServers = append([]string(nil), ac.AllowedServers...)
			a.Permissions = append([]string(nil), ac.Permissions...)
			a.ProfilePin = ac.ProfilePin
			callerAllowed = ac.AllowedServers
			callerBounded = true
		case ac.Type == auth.AuthTypeAdminUser:
			a.CallerKind = cache.CallerKindAdminUser
			a.Principal = ac.UserID
		default:
			a.CallerKind = cache.CallerKindAdmin
		}
	}
	a.Profile = profileName
	if scope != nil {
		a.ProfileScoped = true
		// A caller-bounded token's stamp is the
		// CALLER-INTERSECTED profile membership — the same
		// EffectiveServersFor helper handleSetProfile's own scoped-visible
		// path already renders through (profile_tool.go), O(len(callerAllowed))
		// via idx's precomputed serverPos/members data, never a fleet- or
		// profile-declared-size walk. A profile member entirely outside the
		// token's own grant (the token never had, and never will have,
		// access to it) must not appear in the stamp: left in, its later
		// removal narrows what THIS token's cache read resolves to on the
		// next request and fails the redemption set-covering comparison
		// (internal/cache/authorization.go CouldHaveProduced/coversAll) for
		// an entry produced from a server the token remains fully authorized
		// for — an unrelated, never-authorized server's continued existence
		// becoming an observable side-channel through the cache layer
		// (-class disclosure). Only truly unbounded scoped callers
		// (admin/anonymous reading through a profile URL, which carry no
		// AllowedServers of their own to intersect against) keep the
		// resolver's full profile membership — exactly resolveActiveProfileIn's
		// documented wildcard/profile's-own-membership semantic, untouched
		// here.
		if callerBounded && idx != nil {
			a.ProfileServers = idx.EffectiveServersFor(profileName, callerAllowed)
		} else {
			a.ProfileServers = scope.AllowedServerNames()
		}
		sort.Strings(a.ProfileServers)

		if idx != nil {
			if pol := idx.PolicyFor(profileName); pol != nil {
				a.PolicyFingerprint = hex.EncodeToString(pol.Fingerprint[:])
			}
		}
		if p.mainServer != nil && p.mainServer.runtime != nil {
			if sup := p.mainServer.runtime.Supervisor(); sup != nil {
				a.ToolTierGeneration = sup.ToolTierGeneration()
			}
		}
	}
	return a
}

// producerCacheStore is the CacheStore the truncation helpers write through.
// It carries the producing request's authorization so every entry lands
// stamped, without the helpers (which have no ctx) needing to know about auth.
type producerCacheStore struct {
	store    *cache.Manager
	producer cache.Authorization
}

func (s producerCacheStore) Store(key, toolName string, args map[string]interface{}, content, recordPath string, totalRecords int) error {

	return s.store.StoreAs(key, toolName, args, content, recordPath, totalRecords, s.producer)
}

// cacheStoreAs returns the CacheStore a handler must write truncated payloads
// through, stamping every entry with producer — the authorization the handler
// captured when it authorized the call (never re-sampled at truncation time:
// read_cache gates the read and re-caches an oversize page, and a concurrent
// set_profile or profile deletion must not stamp the page under a different
// scope than the one that passed the gate). Returns an untyped nil when there
// is no cache manager so the helpers' `cacheStore != nil` short-circuit holds.
func (p *MCPProxyServer) cacheStoreAs(producer cache.Authorization) CacheStore {
	if p.cacheManager == nil {
		return nil
	}
	return producerCacheStore{store: p.cacheManager, producer: producer}
}

// childPageProducer is the snapshot a recursively re-truncated read_cache
// page is stamped with: the parent entry's own producer,
// monotone recursive provenance). GetRecordsAs refuses invalid provenance
// before a page exists, so a paged entry always carries a producer; the
// redeemer is the fallback only for a page that somehow arrives without one.
func childPageProducer(page *cache.ReadCacheResponse, redeemer cache.Authorization) cache.Authorization {
	if page != nil && page.Producer != nil {
		return *page.Producer
	}
	return redeemer
}

func readCacheRefusal(err error, reader cache.Authorization) *mcp.CallToolResult {
	if !reader.IsAdministrator() && (errors.Is(err, cache.ErrUnauthorizedRead) || errors.Is(err, cache.ErrKeyExpired)) {
		err = cache.ErrKeyNotFound
	}
	switch {
	case errors.Is(err, cache.ErrEntryUnreadable):
		return mcp.NewToolResultError("Cache entry is unreadable: its stored record could not be decoded and it has been invalidated. Re-run the original tool call to obtain a new cache key.")
	case errors.Is(err, cache.ErrInvalidProvenance):
		return mcp.NewToolResultError("Cache entry is not readable: its authorization provenance is invalid and it has been invalidated. Re-run the original tool call to obtain a new cache key.")
	case errors.Is(err, cache.ErrInternalEntry):
		return mcp.NewToolResultError("Cache entry is not readable: it is internal to mcpproxy (registry or repository metadata) and cannot be paged through read_cache.")
	case errors.Is(err, cache.ErrUnauthorizedRead):
		return mcp.NewToolResultError("Cache entry is not readable with this credential: it was produced under a broader authorization (server scope, permission tier or profile) than this request holds. Re-run the original tool call with this credential to obtain your own cache key.")
	}
	return mcp.NewToolResultError(fmt.Sprintf("Failed to retrieve cached data: %v", err))
}
