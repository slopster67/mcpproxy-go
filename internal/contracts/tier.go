package contracts

import "github.com/slopster67/mcpproxy-go/internal/config"

// Tier is a tool's capability classification, computed once from its
// annotations by AnnotationTier and reused by every surface: the Web Tools
// page, CLI `tools list --tier` and `GET /tools` / `GET /servers/{id}/tools`.
// No surface may compute its own. The review payload is the one place a fifth
// value, TierUnknown, appears: a captured-but-unannotated record is still
// TierUnannotated; TierUnknown is reserved for a record with no captured
// annotations at all.
type Tier string

const (
	TierRead        Tier = "read"
	TierWrite       Tier = "write"
	TierDestructive Tier = "destructive"
	TierUnannotated Tier = "unannotated"
	// TierUnknown is returned only by the review composer (internal/runtime
	// review.go), never by AnnotationTier — see the type doc above.
	TierUnknown Tier = "unknown"
)

// AnnotationTier computes a tool's tier from its MCP behavior-hint
// annotations. This is the ONE place the classification happens;
// IntrinsicTier calls this same function rather than reimplementing it.
//
// Priority (highest first):
//  1. destructiveHint == true            -> TierDestructive
//  2. readOnlyHint == false (explicit)   -> TierWrite
//  3. readOnlyHint == true               -> TierRead
//  4. nil annotations, or neither hint set -> TierUnannotated
//
// This is deliberately distinct from DeriveCallWith (call-variant routing),
// which defaults an unannotated tool to "read" for safety when
// picking which call_tool_* variant to use — AnnotationTier never guesses:
// an unannotated tool is TierUnannotated, not TierRead.
func AnnotationTier(a *config.ToolAnnotations) Tier {
	if a == nil {
		return TierUnannotated
	}
	if a.DestructiveHint != nil && *a.DestructiveHint {
		return TierDestructive
	}
	if a.ReadOnlyHint != nil {
		if !*a.ReadOnlyHint {
			return TierWrite
		}
		return TierRead
	}
	return TierUnannotated
}
