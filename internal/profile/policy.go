package profile

import (
	"crypto/sha256"
	"encoding/json"
	"sort"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
)

// Tier is a tool's capability classification under a profile: none (an
// unannotated tool with no cap-relevant classification) < read < write <
// destructive. It is an int, unlike contracts.Tier (a string), because
// tiers are ordered numerically.
type Tier int

const (
	TierUnannotated Tier = iota // 0: no tier hint and no classification/policy resolved one
	TierRead
	TierWrite
	TierDestructive
)

// String returns the contracts.Tier spelling, so every surface that prints a
// profile tier prints the same word.
func (t Tier) String() string {
	switch t {
	case TierRead:
		return string(contracts.TierRead)
	case TierWrite:
		return string(contracts.TierWrite)
	case TierDestructive:
		return string(contracts.TierDestructive)
	default:
		return string(contracts.TierUnannotated)
	}
}

// IntrinsicTier derives a tool's intrinsic tier from its effective
// annotations through contracts.AnnotationTier, the one place the
// annotation→tier rule is computed. found=false (the tool's identity could
// not be resolved, e.g. a stale/unknown server:tool pair) fails closed to
// TierDestructive regardless of a.
func IntrinsicTier(a *config.ToolAnnotations, found bool) Tier {
	if !found {
		return TierDestructive
	}
	return tierFromContractsTier(contracts.AnnotationTier(a))
}

// tierFromContractsTier is the exhaustive contracts.Tier -> Tier adapter.
func tierFromContractsTier(ct contracts.Tier) Tier {
	switch ct {
	case contracts.TierRead:
		return TierRead
	case contracts.TierWrite:
		return TierWrite
	case contracts.TierDestructive:
		return TierDestructive
	case contracts.TierUnannotated:
		return TierUnannotated
	default:
		// contracts.TierUnknown or any future value: fail closed.
		return TierDestructive
	}
}

// CompiledPolicy is the compiled, request-ready form of one profile's policy,
// built once per published config snapshot by Compile and cached inside
// profileIndex (internal/server) alongside its server-scope data, taken with
// the snapshot as one immutable pair — never recompiled per request.
type CompiledPolicy struct {
	Name    string
	Servers map[string]struct{}

	SelectedTools map[string]map[string]struct{}

	SwitchableTo map[string]struct{}

	// Fingerprint hashes the canonical policy fields. Name is an identity and
	// Servers is tracked separately; neither participates in this hash.
	Fingerprint [32]byte
}

// fingerprintFields is the canonical, ordered projection of exactly the
// policy fields hashed into CompiledPolicy.Fingerprint.
type fingerprintFields struct {
	SelectedTools map[string][]string `json:"selected_tools,omitempty"`
	SwitchableTo  *[]string           `json:"switchable_to,omitempty"`
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// fingerprintOf computes the Fingerprint for pc's raw (uncompiled) policy
// fields, called by Compile.
func fingerprintOf(pc *config.ProfileConfig) [32]byte {
	f := fingerprintFields{}
	if len(pc.SelectedTools) > 0 {
		f.SelectedTools = map[string][]string{}
		for server, tools := range pc.SelectedTools {
			values := sortedCopy(tools)
			if values == nil {
				values = []string{}
			}
			f.SelectedTools[server] = values
		}
	}
	if pc.SwitchableTo != nil {
		sorted := sortedCopy(*pc.SwitchableTo)
		if sorted == nil {
			sorted = []string{}
		}
		f.SwitchableTo = &sorted
	}
	b, err := json.Marshal(f)
	if err != nil {
		// f contains only strings, maps and slices of strings/bools —
		// Marshal cannot fail on this shape.
		panic("profile: fingerprintOf: " + err.Error())
	}
	return sha256.Sum256(b)
}

// Compile builds an immutable policy from the current permission selection.
func Compile(pc *config.ProfileConfig) *CompiledPolicy {
	cp := &CompiledPolicy{
		Name:        pc.ID,
		Servers:     make(map[string]struct{}, len(pc.Servers)),
		Fingerprint: fingerprintOf(pc),
	}
	for _, s := range pc.Servers {
		cp.Servers[s] = struct{}{}
	}
	cp.SelectedTools = map[string]map[string]struct{}{}
	for server, tools := range pc.SelectedTools {
		selected := map[string]struct{}{}
		for _, tool := range tools {
			selected[tool] = struct{}{}
		}
		cp.SelectedTools[server] = selected
	}

	if pc.SwitchableTo != nil {
		cp.SwitchableTo = make(map[string]struct{}, len(*pc.SwitchableTo))
		for _, name := range *pc.SwitchableTo {
			cp.SwitchableTo[name] = struct{}{}
		}
	}

	return cp
}

// Decide checks server membership and exact tool selections.
func (p *CompiledPolicy) Decide(server, tool string, intrinsic Tier) (admitted bool, reason Reason, profileTier Tier) {
	profileTier = intrinsic

	if p == nil {
		return true, ReasonNone, profileTier
	}
	if _, ok := p.Servers[server]; !ok {
		return false, ReasonServerNotInProfile, profileTier
	}
	if selected, restricted := p.SelectedTools[server]; restricted {
		if _, ok := selected[tool]; !ok {
			return false, ReasonNotSelected, profileTier
		}
	}
	return true, ReasonNone, profileTier
}
