package server

import (
	"context"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// ProfileResolution is the per-request result
// (data-model.md §4): one call returns the effective profile, WHERE it came
// from, and the (scope, policy) pair to decide against — never a second,
// independent lookup for any of the three.
type ProfileResolution struct {
	Name string
	// Source is one of the profile.Source* constants: pin, binding,
	// request, anonymous, none.
	Source string
	// Scope is the resolved profile's server scope; nil for "none" and for
	// the empty-pin "All servers" binding/anonymous case.
	Scope *profile.ProfileScope
	// Policy is the resolved profile's compiled policy; nil whenever Scope
	// is nil, and also for a dangling base (a name that no longer resolves).
	Policy *profile.CompiledPolicy
	// Base is the bound or anonymous base profile used for switchable_to
	// admission — "" for source none, and for a source
	// pin/binding/anonymous whose base is "All servers" (empty pin).
	Base string
}

// clientCredentialFromContext returns (pin, mode, ok) for a
// client-credential AuthContext; ok is false for every other context
// (admin, a regular client token, user, anonymous/no-context).
func clientCredentialFromContext(ctx context.Context) (pin, mode string, ok bool) {
	ac := auth.AuthContextFromContext(ctx)
	if !ac.IsClientCredential() {
		return "", "", false
	}
	return ac.ProfilePin, ac.ProfileMode, true
}

func admittedBySwitchableTo(basePolicy *profile.CompiledPolicy, base, target string) bool {
	if target == base {
		return true
	}
	if basePolicy == nil || basePolicy.SwitchableTo == nil {
		return false
	}
	_, ok := basePolicy.SwitchableTo[target]
	return ok
}

func resolveV3Base(ctx context.Context, idx *profileIndex) (name string, source profile.Source, dangling bool) {
	if pin, mode, ok := clientCredentialFromContext(ctx); ok {
		if mode == auth.ProfileModeLocked {
			return pin, profile.SourcePin, pin != "" && idx.position(pin) < 0
		}
		// Switchable: pin (possibly "" = "All servers") is the BOUND base.
		return pin, profile.SourceBinding, pin != "" && idx.position(pin) < 0
	}

	return "", profile.SourceNone, false
}

func (p *MCPProxyServer) ResolveProfileV3(ctx context.Context, idx *profileIndex) ProfileResolution {
	res := p.resolveProfileV3(ctx, idx)
	p.recordSessionResolution(ctx, res)
	return res
}

// recordSessionResolution stores the latest effective profile and source on
// the calling session — the one place a resolution is
// recorded, so session rows and reassignment notifications can find it.
func (p *MCPProxyServer) recordSessionResolution(ctx context.Context, res ProfileResolution) {
	if p.sessionStore == nil || ctx.Value(noResolutionRecordKey{}) != nil {
		return
	}
	if sid := sessionIDFromContext(ctx); sid != "" {
		p.sessionStore.UpdateSessionProfile(sid, res.Name, res.Source)
	}
}

// noResolutionRecordKey marks a context whose profile resolutions are probes
// (e.g. composing initialize instructions) and must not be recorded as the
// session's resolution: that record steers which sessions a later profile
// edit notifies, and only real requests may set it.
type noResolutionRecordKey struct{}

func (p *MCPProxyServer) resolveProfileV3(ctx context.Context, idx *profileIndex) ProfileResolution {

	if idx == nil {
		// profileIndexCurrent deliberately returns nil for a scoped request
		// during a publication gap instead of building an index on the request
		// path. Preserve that fail-closed contract without dereferencing the
		// missing snapshot or falling through to an unscoped view.
		if pin, mode, ok := clientCredentialFromContext(ctx); ok {
			source := profile.SourceBinding
			if mode == auth.ProfileModeLocked {
				source = profile.SourcePin
			}
			return ProfileResolution{Name: pin, Source: string(source), Scope: profile.NewProfileScope(pin, nil), Base: pin}
		}
		if pin := profilePinFromContext(ctx); pin != "" {
			return ProfileResolution{Name: pin, Source: string(profile.SourcePin), Scope: profile.NewProfileScope(pin, nil), Base: pin}
		}
		ac := auth.AuthContextFromContext(ctx)
		if auth.IsScopedCaller(ctx) || (ac != nil && ac.IsClientCredential()) {
			return ProfileResolution{Source: string(profile.SourceNone), Scope: profile.NewProfileScope("", nil)}
		}
		return ProfileResolution{Source: string(profile.SourceNone)}
	}
	base, source, dangling := resolveV3Base(ctx, idx)

	// Tier 1: pin. Authoritative — never falls through, including when
	// dangling.
	if source == profile.SourcePin {
		if base == "" {
			return ProfileResolution{Source: string(source)}
		}
		if dangling {
			return ProfileResolution{Name: base, Source: string(source), Scope: profile.NewProfileScope(base, nil), Base: base}
		}
		return ProfileResolution{
			Name: base, Source: string(source),
			Scope: profileScopeFromIndex(idx, base), Policy: idx.PolicyFor(base), Base: base,
		}
	}

	// A dangling binding or anonymous base is likewise authoritative deny-all
	//: no request selection is admitted (its switchable_to is
	// unknown).
	if (source == profile.SourceBinding) && dangling {
		return ProfileResolution{Name: base, Source: string(source), Scope: profile.NewProfileScope(base, nil), Base: base}
	}

	basePolicy := idx.PolicyFor(base) // nil when base == "" (no such profile — expected)

	if box, ok := ctx.Value(directRequestKindCtxKey{}).(*directRequestKindBox); ok && box.profile != "" {
		if base == "" || admittedBySwitchableTo(basePolicy, base, box.profile) {
			if scope := profileScopeFromIndex(idx, box.profile); scope != nil {
				return ProfileResolution{Name: box.profile, Source: string(profile.SourceRequest), Scope: scope, Policy: idx.PolicyFor(box.profile), Base: base}
			}
		}
		return ProfileResolution{Name: box.profile, Source: string(profile.SourceRequest), Scope: profile.NewProfileScope(box.profile, nil), Base: base}
	}

	if source == profile.SourceBinding {
		if base == "" {
			return ProfileResolution{Source: string(source)}
		}
		return ProfileResolution{
			Name: base, Source: string(source),
			Scope: profileScopeFromIndex(idx, base), Policy: basePolicy, Base: base,
		}
	}

	return ProfileResolution{Source: string(profile.SourceNone)}
}
