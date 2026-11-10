package server

import (
	"sort"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/runtime/stateview"
	"github.com/slopster67/mcpproxy-go/internal/toolannotations"
)

// SessionRisk holds the result of analyzing all connected servers' tool annotations
// for the "lethal trifecta" risk combination.
type SessionRisk struct {
	Level          string `json:"level"`           // "high", "medium", "low"
	HasOpenWorld   bool   `json:"has_open_world"`  // Any tool with openWorldHint=true or nil
	HasDestructive bool   `json:"has_destructive"` // Any tool with destructiveHint=true or nil
	HasWrite       bool   `json:"has_write"`       // Any tool with readOnlyHint=false or nil
	LethalTrifecta bool   `json:"lethal_trifecta"` // All three categories present

}

// analyzeSessionRiskScoped examines the connected servers the caller can
// discover (discoverable admits) for the "lethal trifecta" risk: open-world
// access + destructive capabilities + write access. Per MCP spec, nil
// annotation hints default to the most permissive interpretation:
//   - openWorldHint nil → true (assumes open world)
//   - destructiveHint nil → true (assumes destructive)
//   - readOnlyHint nil → false (assumes not read-only, i.e., can write)
//
// It covers the caller's own authorized servers only, under the availability
// rules (connected, tier-blind): otherwise the level (and the verdict) would
// leak the existence and annotation shape of a server outside the caller's
// scope, even though no tool name is named.
func analyzeSessionRiskScoped(snapshot *stateview.ServerStatusSnapshot, discoverable func(serverName string) bool) SessionRisk {
	var hasOpenWorld, hasDestructive, hasWrite bool

	for _, server := range snapshot.Servers {
		if !server.Connected || !discoverable(server.Name) {
			continue
		}

		for _, tool := range server.Tools {
			classifyToolRisk(tool.Annotations, &hasOpenWorld, &hasDestructive, &hasWrite)
		}
	}

	// Count how many risk categories are present
	riskCount := 0
	if hasOpenWorld {
		riskCount++
	}
	if hasDestructive {
		riskCount++
	}
	if hasWrite {
		riskCount++
	}

	risk := SessionRisk{
		HasOpenWorld:   hasOpenWorld,
		HasDestructive: hasDestructive,
		HasWrite:       hasWrite,
	}

	switch {
	case riskCount >= 3:
		risk.Level = "high"
		risk.LethalTrifecta = true

	case riskCount == 2:
		risk.Level = "medium"
	default:
		risk.Level = "low"
	}

	return risk
}

func buildSessionRiskResponse(risk SessionRisk) map[string]interface{} {
	out := map[string]interface{}{
		"level":                 risk.Level,
		"has_open_world_tools":  risk.HasOpenWorld,
		"has_destructive_tools": risk.HasDestructive,
		"has_write_tools":       risk.HasWrite,
		"lethal_trifecta":       risk.LethalTrifecta,
	}

	return out
}

// classifyToolRisk updates the risk flags based on a single tool's annotations.
// Nil hints are treated as their MCP spec defaults (most permissive).
func classifyToolRisk(annotations *config.ToolAnnotations, hasOpenWorld, hasDestructive, hasWrite *bool) {
	if annotations == nil {
		// No annotations at all — apply MCP spec defaults (all permissive)
		*hasOpenWorld = true
		*hasDestructive = true
		*hasWrite = true
		return
	}

	// openWorldHint: nil or true → open world
	if annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
		*hasOpenWorld = true
	}

	// destructiveHint: nil or true → destructive
	if annotations.DestructiveHint == nil || *annotations.DestructiveHint {
		*hasDestructive = true
	}

	// readOnlyHint: nil or false → not read-only (write capable)
	if annotations.ReadOnlyHint == nil || !*annotations.ReadOnlyHint {
		*hasWrite = true
	}
}

// annotatedSearchResult pairs a search result with its resolved annotations
// for use in annotation-based filtering.
type annotatedSearchResult struct {
	serverName  string
	toolName    string
	annotations *config.ToolAnnotations
	resultIndex int // Index into the original search results slice
}

// reasonCounts splits one filter's omissions by remediation class
// ): missing annotations mean "fix the upstream server", an explicitly
// unsafe hint means "the filter is doing its job". Neither field is omitempty
// — a zero is information, not noise.
type reasonCounts struct {
	MissingAnnotation int `json:"missing_annotation"`
	Explicit          int `json:"explicit"`
}

// filterDiagnostics is the normative block. The handler attaches it to
// a retrieve_tools response only when at least one annotation filter was
// active AND omitted at least one tool, so the happy path stays byte-identical
// to the pre-feature response.
//
// Counts and one suggestion string, never identities: the block is
// therefore leak-proof even for a quarantined tool lingering in a stale index,
// and has a bounded serialized size.
type filterDiagnostics struct {
	MatchedBeforeFilters int                     `json:"matched_before_filters"`
	OmittedTotal         int                     `json:"omitted_total"`
	OmittedByFilter      map[string]reasonCounts `json:"omitted_by_filter"`
	Suggestion           string                  `json:"suggestion"`
}

// Suggestion templates. Compile-time constants over the JSON-safe
// ASCII subset [a-zA-Z0-9 .,:;()'_-] — no quotes, backslashes or
// HTML-significant characters — so JSON encoding never expands them and the
// size bound is exact arithmetic rather than an estimate.
const (
	suggestMissingPrefix  = "Tools matched but lack upstream annotations required by "
	suggestMissingSuffix  = "; publish annotations or retry without these filters."
	suggestExplicitPrefix = "Omitted tools are explicitly marked unsafe for "
	suggestExplicitSuffix = "; retry without these filters to inspect them."
)

// filterSuggestion renders the single actionable line. anyMissing selects by
// cause precedence: one unannotated omission anywhere is worth reporting as a
// fixable upstream gap, because that is the remediation the operator can act
// on. responsible must already be sorted; each name appears exactly once.
func filterSuggestion(responsible []string, anyMissing bool) string {
	names := strings.Join(responsible, ", ")
	if anyMissing {
		return suggestMissingPrefix + names + suggestMissingSuffix
	}
	return suggestExplicitPrefix + names + suggestExplicitSuffix
}

// filterByAnnotationsWithDiagnostics filters exactly like filterByAnnotations
// and additionally reports what it withheld. Diagnostics are
// derived from the candidate window this call already produced — no extra
// index queries, no upstream calls.
//
// diag is always non-nil; the handler decides whether to attach it.
func filterByAnnotationsWithDiagnostics(tools []annotatedSearchResult, readOnlyOnly, excludeDestructive, excludeOpenWorld bool) (kept []annotatedSearchResult, diag *filterDiagnostics) {
	diag = &filterDiagnostics{
		MatchedBeforeFilters: len(tools),
		OmittedByFilter:      map[string]reasonCounts{},
	}

	for _, tool := range tools {
		filterKey, explicit, excluded := excludeReason(tool.annotations, readOnlyOnly, excludeDestructive, excludeOpenWorld)
		if !excluded {
			kept = append(kept, tool)
			continue
		}
		// An entry appears only once a filter has actually omitted something,
		// which is what keeps active-but-idle filters out of the map.
		counts := diag.OmittedByFilter[filterKey]
		if explicit {
			counts.Explicit++
		} else {
			counts.MissingAnnotation++
		}
		diag.OmittedByFilter[filterKey] = counts
		diag.OmittedTotal++
	}

	responsible := make([]string, 0, len(diag.OmittedByFilter))
	anyMissing := false
	for key, counts := range diag.OmittedByFilter {
		responsible = append(responsible, key)
		if counts.MissingAnnotation > 0 {
			anyMissing = true
		}
	}
	sort.Strings(responsible)
	if len(responsible) > 0 {
		diag.Suggestion = filterSuggestion(responsible, anyMissing)
	}

	return kept, diag
}

// excludeReason delegates to the shared classifier in internal/toolannotations
// . The semantics — first-failing filter owns the omission,
// evaluated read-only → destructive → open-world, `explicit` distinguishing an
// unsafe hint from a missing one — are frozen by and now live in
// one place so the preflight evaluator and retrieve_tools cannot drift apart.
func excludeReason(annotations *config.ToolAnnotations, readOnlyOnly, excludeDestructive, excludeOpenWorld bool) (filterKey string, explicit, excluded bool) {
	return toolannotations.ExcludeReason(annotations, readOnlyOnly, excludeDestructive, excludeOpenWorld)
}
