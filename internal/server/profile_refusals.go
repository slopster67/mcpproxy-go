package server

import (
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/profile"
)

func profileDisclosedTo(source profile.Source) bool {
	switch source {
	case profile.SourcePin, profile.SourceBinding, profile.SourceSession:
		return true
	default:
		return false
	}
}

// refusalSubject is the profile a tool refusal may name. Disclose is false for
// every caller that must not learn it; the refusal then keeps the original,
// non-disclosing "this profile" wording.
type refusalSubject struct {
	Slug     string
	Disclose bool
}

// profileRefusalSubject decides what a refusal under res may name. A dangling
// base has no compiled policy (and never reaches a tool refusal), and an empty
// name is the "All servers" base: neither names anything.
func profileRefusalSubject(res ProfileResolution) refusalSubject {
	if res.Policy == nil || res.Name == "" || !profileDisclosedTo(profile.Source(res.Source)) {
		return refusalSubject{}
	}
	return refusalSubject{Slug: res.Name, Disclose: true}
}

// profileToolPolicyRefusal renders the refusal text for a tool excluded by a
// compiled profile policy. A caller that may learn its own profile is told
// which one it is (subj.Disclose); every other caller gets the stable,
// non-disclosing text. The two shapes are pinned by
// internal/profile/testdata/contract/tool_refusals.json.
func profileToolPolicyRefusal(reason profile.Reason, tier profile.Tier, server, tool string, subj refusalSubject) (string, profile.BlockReason) {
	switch reason {
	case profile.ReasonNotSelected:
		return fmt.Sprintf("blocked by profile: %s:%s is not selected for this profile", server, tool), profile.BlockReasonRule
	default:
		return "blocked by profile: tool is not allowed by the active profile", profile.BlockReasonRule
	}
}
