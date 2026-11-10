package server

import (
	"context"
	"net/http"

	"github.com/mark3labs/mcp-go/v2/mcp"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// directToolStampMetaKey is the _meta key under which renderDirectTools
// stamps a rendered direct-mode tool with the identity of the publication
// that produced it. Namespaced per the MCP _meta convention.
const directToolStampMetaKey = "app.mcpproxy/direct-tool-identity"

// directToolStamp is the value stored under directToolStampMetaKey
// ).
//
// It exists so the scope and callability filters can authorize a rendered
// tool against the EXACT identity of the publication that produced it,
// without a second catalog lookup by display name. A lookup can resolve
// against whatever catalog generation happens to be published RIGHT NOW,
// which — during the SetTools-then-publish window every rebuild opens — can
// be a different generation than the one that rendered and registered this
// particular tool object and its handler (the publication-skew an origin flip
// exploits: "a__b__c" can mean server "a" tool "b__c" in one generation and
// server "a__b" tool "c" in the next, and a catalog lookup mid-window answers
// for whichever generation happens to be live, not the one this tool came
// from). Riding the stamp on the tool itself makes the filter's answer
// inseparable from the tool it is filtering.
//
// A private struct rather than bare strings,
// so an upstream tool that happens to carry our key in its own _meta can never
// be mistaken for a stamp (a string there does not type-assert to this
// struct), and the upstream's own Meta travels with it for restoration by
// stripDirectToolStamp. Marshalling it directly (which only an unfiltered
// reader could do) yields "{}": the fields are unexported.
type directToolStamp struct {
	owner    string
	rawName  string
	tier     string // requiredPermission ("read"/"write"/"destructive"/"")
	upstream *mcp.Meta
}

// stampDirectTool returns tool with a directToolStamp recording entry's
// identity written into its _meta, preserving whatever _meta the tool already
// carried.
func stampDirectTool(tool mcp.Tool, entry *directCatalogEntry) mcp.Tool {
	meta := &mcp.Meta{AdditionalFields: map[string]any{}}
	if tool.Meta != nil {
		meta.ProgressToken = tool.Meta.ProgressToken
		for k, v := range tool.Meta.AdditionalFields {
			meta.AdditionalFields[k] = v
		}
	}
	meta.AdditionalFields[directToolStampMetaKey] = directToolStamp{
		owner:    entry.ServerName,
		rawName:  entry.ToolName,
		tier:     entry.RequiredPermission,
		upstream: tool.Meta,
	}
	tool.Meta = meta
	return tool
}

// readDirectToolStamp reads the identity stamped by stampDirectTool. ok is
// false for a tool that carries no stamp: every built-in (registered outside
// renderDirectTools), or any mcp.Tool value a caller constructs directly
// rather than through it (as unit tests exercising the discovery filters do).
func readDirectToolStamp(tool mcp.Tool) (directToolStamp, bool) {
	if tool.Meta == nil {
		return directToolStamp{}, false
	}
	stamp, ok := tool.Meta.AdditionalFields[directToolStampMetaKey].(directToolStamp)
	return stamp, ok
}

// stripDirectToolStamp returns tool with the internal identity stamp removed
// and its _meta restored to exactly what was there before stampDirectTool ran
// (nil stays nil), so client-visible output never carries mcpproxy's internal
// bookkeeping — for every caller, administrators included.
// The registered tool itself is never mutated: mcp-go hands filters the
// stored value, and only the copy's Meta pointer is replaced.
func stripDirectToolStamp(tool mcp.Tool) mcp.Tool {
	stamp, ok := readDirectToolStamp(tool)
	if !ok {
		return tool
	}
	tool.Meta = stamp.upstream
	return tool
}

// stripDirectToolStampFilter is the TERMINAL tool filter registered on the
// direct server. mcp-go runs WithToolFilter filters in registration order,
// feeding each filter's output to the next (both for tools/list and for the
// call-time re-evaluation of a single tool), so registering this one LAST
// guarantees the scope and callability filters above it still see the stamp
// they authorize against, while every tool that survives them — for every
// caller, including administrators, who never went through those two filters'
// per-tool loop before this change — leaves with it removed.
func stripDirectToolStampFilter(_ context.Context, tools []mcp.Tool) []mcp.Tool {
	if len(tools) == 0 {
		return tools
	}
	out := make([]mcp.Tool, len(tools))
	for i, tool := range tools {
		out[i] = stripDirectToolStamp(tool)
	}
	return out
}

// directScopeAllows is the SERVER-only scope test: profile scope plus the
// token's server scope — deliberately never the permission tier. It lets the
// call-time re-evaluation of the direct discovery filters authorize a tool's
// SERVER without also excluding it for its TIER, leaving that decision to the
// handler, which answers insufficient-permission instead of an unregistered-
// name -32602. See directRequestKindFromContext's doc comment for why list
// time and call time need different answers from the same filter chain.
func directScopeAllows(
	authCtx *auth.AuthContext,
	profileScope *profile.ProfileScope,
	owner string,
) bool {
	return profileScope.Allows(owner) && authCtx.CanAccessServer(owner)
}

// directRequestKind is the two-value signal above (plus "unknown").
type directRequestKind int

const (
	directRequestKindUnknown directRequestKind = iota
	directRequestKindList
	directRequestKindCall
)

// directRequestKindCtxKey is the ctx key for the mutable box below.
type directRequestKindCtxKey struct{}

// directRequestKindBox is the mutable cell mcpAuthMiddleware installs and
// the BeforeListTools/BeforeCallTool hooks write into. A pointer, not a
// value: context.WithValue makes a new immutable binding per call, but every
// holder of THIS pointer — the hook, and the filter that reads it later in
// the same synchronous call chain — shares the one cell it points to.
type directRequestKindBox struct {
	kind directRequestKind
}

// withDirectRequestKindBox installs an empty (unknown) box on ctx. Call
// exactly once per inbound request, before mcp-go's HandleMessage runs.
func withDirectRequestKindBox(ctx context.Context) context.Context {
	return context.WithValue(ctx, directRequestKindCtxKey{}, &directRequestKindBox{})
}

// setDirectRequestKind writes kind into ctx's box. A no-op when ctx carries
// no box (nothing to synchronize through), never an error: the hooks fire on
// every routing-mode server, most of which never look at the box at all.
func setDirectRequestKind(ctx context.Context, kind directRequestKind) {
	if box, ok := ctx.Value(directRequestKindCtxKey{}).(*directRequestKindBox); ok {
		box.kind = kind
	}
}

// directRequestKindFromContext reads the box's current value, or
// directRequestKindUnknown when ctx carries none.
func directRequestKindFromContext(ctx context.Context) directRequestKind {
	if box, ok := ctx.Value(directRequestKindCtxKey{}).(*directRequestKindBox); ok {
		return box.kind
	}
	return directRequestKindUnknown
}

// isDirectCallTimeRequest reports whether ctx is inside the call-time
// re-evaluation of one tool, as opposed to a tools/list enumeration (or an
// uninstrumented caller, which reads as list-time — see the package doc
// comment above for why that is the safe default).
func isDirectCallTimeRequest(ctx context.Context) bool {
	return directRequestKindFromContext(ctx) == directRequestKindCall
}

// directPolicyExcludedForListing reports whether the tool policy
// excludes (owner, rawName) from the direct surface's LISTING only (T023,
// ). It deliberately mirrors directCallTimeTierExceeded's own
// list/call-time split: at call time (isDirectCallTimeRequest) the tool must
// stay visible to mcp-go's WithToolFilter chain, or a policy-excluded call
// would be answered with the chain's generic "tool not found" instead of the
// descriptive profile refusal 108-d's own call-time gate provides
// (handleCallToolVariant's refusal, wired through the direct dispatch path)
// — exactly the reason an over-tier tool is let through here too instead of
// being excluded by this filter.
func (p *MCPProxyServer) directPolicyExcludedForListing(ctx context.Context, policy *profile.CompiledPolicy, owner, rawName string) bool {
	if policy == nil || isDirectCallTimeRequest(ctx) {
		return false
	}
	annotations, found := p.EffectiveAnnotations(owner, rawName)
	intrinsic := profile.IntrinsicTier(annotations, found)
	admitted, _, _ := policy.Decide(owner, rawName, intrinsic)
	return !admitted
}

// directRequestKindMiddleware installs an empty directRequestKindBox on every
// request's ctx. See mcpAuthMiddleware (server.go), the one production
// caller, for why this must wrap the handler chain BEFORE mcp-go's
// HandleMessage runs.
func directRequestKindMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(withDirectRequestKindBox(r.Context())))
	})
}

// requiredPermissionForDirectTool derives the client token permission a direct
// tool requires from its annotations. It reuses the same variant->operation-type
// mapping that call-time authorization uses (see handleDirectToolCall in
// mcp_routing.go) so discovery filtering can never diverge from execution
// enforcement.
func requiredPermissionForDirectTool(annotations *config.ToolAnnotations) string {
	return contracts.ToolVariantToOperationType[contracts.DeriveCallWith(annotations)]
}

func (p *MCPProxyServer) filterDirectModeToolsForAuth(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
	if len(tools) == 0 {
		return tools
	}

	authCtx := auth.AuthContextFromContext(ctx)
	profileName, profileScope, profileIdx := p.resolveActiveProfileWithIndex(ctx)
	profileResolution := p.ResolveProfileV3(ctx, profileIdx)
	if profileResolution.Scope != nil {
		profileName = profileResolution.Name
		profileScope = profileResolution.Scope
	}
	var policy *profile.CompiledPolicy
	if profileName != "" {
		policy = profileIdx.PolicyFor(profileName)
	}

	// A tool with no registration identity is withheld from EVERY caller,
	// administrators included, so this filter never short-circuits for an
	// unscoped/no-profile caller: the per-tool loop and its withholding check
	// always run.

	filtered := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if stamp, stamped := readDirectToolStamp(tool); stamped {
			// Authorize against the identity STAMPED on this exact tool
			// object — see directToolStamp's doc comment for why this must
			// never be re-derived from a fresh catalog lookup by name.
			if stamp.rawName == "" {
				// Defense in depth: buildDirectCatalog refuses to admit an
				// empty raw name, so a stamp should never carry one.
				continue
			}
			// D13/gap G6: at call time, an over-tier tool on
			// an AUTHORIZED server is let through so the registered handler
			// (which checks tier before callability) answers
			// insufficient-permission, instead of this filter excluding it
			// into mcp-go's unregistered-name -32602. Listing is unaffected —
			// isDirectCallTimeRequest is false there, so directIdentityInScope
			// keeps applying the tier check exactly as before.

			if !directScopeAllows(authCtx, profileScope, stamp.owner) {
				continue
			}
			if p.directPolicyExcludedForListing(ctx, policy, stamp.owner, stamp.rawName) {
				continue
			}
			filtered = append(filtered, tool)
			continue
		}

		entry, decision := p.resolveDirectTool(tool.Name)

		switch decision {
		case directResolveBuiltin:
			// A tool this proxy serves itself. It has no owning upstream server,
			// so neither profile scope nor token server-scope applies.
			filtered = append(filtered, tool)
			continue
		case directResolveDenied:
			// A catalog exists and does not admit this name: an unknown tool, or
			// one withheld for a display-name collision, or one refused
			// admission for carrying no registration identity.
			// Dropping it is the point — re-parsing would pick an origin the
			// catalog refused to choose.
			continue
		case directResolveNoCatalog:
			// Nothing published yet — a proxy still coming up. Fall back to the
			// pre-catalog behaviour rather than deny, or startup would serve an
			// empty listing to everyone.
			serverName, _, _ := ParseDirectToolName(tool.Name)
			if !profileScope.Allows(serverName) {
				continue
			}
			// With no catalog there is no permission tier to check. Failing
			// closed on an unknown tier is the safe direction, and this window
			// is one rebuild long.
			continue
		case directResolveFound:
		}

		if !directEntryInScope(authCtx, profileScope, entry) {
			continue
		}

		filtered = append(filtered, tool)
	}

	return filtered
}

// directEntryInScope is the scope+tier half of the direct listing gate, factored
// out of the loop above so describe_tool can apply the SAME test rather than a
// second copy of it.
//
// Sharing it is the point: listing-parity is the whole contract of describe on
// this surface — no id may be describable-but-unlisted, and none listed-but-
// undescribable — and a mirrored predicate is exactly how that drifts. The
// remaining half (agent callability) is directEntryCallable below.
func directEntryInScope(
	authCtx *auth.AuthContext,
	profileScope *profile.ProfileScope,
	entry *directCatalogEntry,
) bool {
	if entry == nil {
		return false
	}
	// The tier is the catalog entry's, derived from UPSTREAM annotations
	// exactly as dispatch derives it. Deriving it from the registered
	// mcp.Tool would read mcp-go's NewTool defaults — destructiveHint=true on
	// essentially every tool — and hide the catalog from read- and
	// write-scoped tokens while dispatch happily allowed the same calls
	// (D13 rule 3).
	return directScopeAllows(authCtx, profileScope, entry.ServerName)
}
