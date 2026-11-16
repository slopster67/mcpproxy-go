package server

import (
	"context"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

func sessionIDFromContext(ctx context.Context) string {
	if box, ok := ctx.Value(directRequestKindCtxKey{}).(*directRequestKindBox); ok {
		return box.sessionID
	}
	return ""
}

func profilePinFromContext(ctx context.Context) string {
	if ac := auth.AuthContextFromContext(ctx); ac != nil && ac.Type == auth.AuthTypeClient {
		return ac.ProfilePin
	}
	return ""
}

// currentConfig returns the live configuration snapshot (hot-reload safe),
// falling back to the construction-time config if the runtime is unavailable.
func (p *MCPProxyServer) currentConfig() *config.Config {
	if p.mainServer != nil && p.mainServer.runtime != nil {
		if cfg := p.mainServer.runtime.Config(); cfg != nil {
			return cfg
		}
	}
	return p.config
}

// effectiveToolResponseMode resolves the retrieve_tools serialization mode
// for one call. Precedence: per-call `detail`
// override > configured tool_response_mode > full. It reads the LIVE config
// snapshot via currentConfig() — never the construction-time p.config the
// retrieve path historically used — so a hot-reload (config file or API
// apply) changes the resolved mode on the very next call, without
// reconstructing the server. Unknown detail values fall through to the
// configured mode (the tool schema's enum is the real gate).
func (p *MCPProxyServer) effectiveToolResponseMode(detail string) string {
	if detail == config.ToolResponseModeFull || detail == config.ToolResponseModeCompact {
		return detail
	}
	if cfg := p.currentConfig(); cfg != nil && cfg.ToolResponseMode != "" {
		return cfg.ToolResponseMode
	}
	return config.ToolResponseModeFull
}

func (p *MCPProxyServer) effectiveDirectToolResponseMode() string {
	if cfg := p.currentConfig(); cfg != nil && cfg.DirectToolResponseMode != "" {
		return cfg.DirectToolResponseMode
	}
	return config.DirectToolResponseModeFull
}

// profileScopeForSlugIn is profileScopeForSlug against an explicit config
// snapshot, for callers that must not re-read the live config between a
// check and the scope they build from it.
func profileScopeForSlugIn(cfg *config.Config, slug string) *profile.ProfileScope {
	if slug == "" || cfg == nil {
		return nil
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == slug {
			return profile.NewProfileScope(slug, cfg.Profiles[i].EffectiveServers(cfg))
		}
	}
	return nil
}

// resolveActiveProfileWithIndex is resolveActiveProfile, but also returns the
// (index, snapshot) the (name, scope) pair was resolved against — the SAME
// pair, never a second, independent lookup. A caller that must derive
// something else from that identical pair — cacheAuthorizationWith's
// caller-intersected ProfileServers stamp — uses this seam instead of re-resolving the index on its own,
// which could pair a decision made against one published snapshot with an
// index built from a later one on a request that pauses in between, exactly
// a class of bug on the admission and resolution paths.
func (p *MCPProxyServer) resolveActiveProfileWithIndex(ctx context.Context) (string, *profile.ProfileScope, *profileIndex) {
	name, scope, idx, _ := p.resolveActiveProfileWithSource(ctx)
	return name, scope, idx
}

// resolveActiveProfileWithSource is resolveActiveProfileWithIndex, additionally
// reporting which resolution tier produced the (name, scope) pair:
// retrieve_tools needs this to decide whether its `profile` field may
// name the effective slug at all — only when the caller itself selected it
// (source session) — never when it merely inherited a token pin,
// which would tell a caller it is pinned and to what. Only
// the pin and session tiers are distinguished here.
func (p *MCPProxyServer) resolveActiveProfileWithSource(ctx context.Context) (string, *profile.ProfileScope, *profileIndex, profile.Source) {
	idx := p.profileIndexFor(p.currentConfig())
	name, scope, source := p.resolveActiveProfileWithSourceFromIndex(ctx, idx)
	return name, scope, idx, source
}

// resolveActiveProfileWithSourceFromIndex is resolveActiveProfileFromIndex,
// additionally reporting the profile.Source tier the (name, scope) pair
// resolved through. It is the ONE place this precedence is
// implemented; resolveActiveProfileFromIndex is a thin wrapper over it so the
// two can never drift.
func (p *MCPProxyServer) resolveActiveProfileWithSourceFromIndex(ctx context.Context, idx *profileIndex) (string, *profile.ProfileScope, profile.Source) {
	// 1. Client token pin (T3). When present it is authoritative and bounds
	//    everything below — including the case where the pinned profile has been
	//    removed from config since the token was minted.
	//
	//    A stale pin resolves to a DENY-ALL scope that keeps the removed
	//    profile's name, never to the next resolver tier. Falling through would
	//    hand the session the token's own (wider) server scope — precisely the
	//    privilege widening the pin exists to prevent — and it would do so
	//    silently, from an operator action (deleting a profile) that reads as a
	//    restriction. This mirrors resolvePreflightScope
	//    (internal/server/preflight_glue.go), which intersects an unresolvable
	//    pin against an empty server set for the same reason, so the session and
	//    preflight paths cannot disagree about what a pinned token may see.
	if pin := profilePinFromContext(ctx); pin != "" {
		if scope := profileScopeFromIndex(idx, pin); scope != nil {
			return pin, scope, profile.SourcePin
		}
		if p.logger != nil {
			p.logger.Warn("client-token profile_pin no longer matches any configured profile; resolving to a deny-all scope",
				zap.String("profile_pin", pin))
		}
		return pin, profile.NewProfileScope(pin, nil), profile.SourcePin
	}

	return "", nil, profile.SourceNone
}

// profileScopeFromIndex builds the ProfileScope for slug's FULL membership
// (declared servers ∩ configured servers, unintersected with any caller
// credential — see resolveActiveProfileIn's doc comment) from idx, or nil
// when idx's snapshot has no such profile. It is profileScopeForSlugIn's
// O(profile size) counterpart, resolving through the index's precomputed
// data instead of rebuilding a fleet-sized set on every call.
func profileScopeFromIndex(idx *profileIndex, slug string) *profile.ProfileScope {
	if idx == nil || idx.cfg == nil {
		return nil
	}
	candidate := idx.position(slug)
	if candidate < 0 {
		return nil
	}
	return profile.NewProfileScope(slug, idx.effectiveServersForCandidate(candidate, []string{"*"}))
}
