package runtime

import (
	"context"
	"errors"

	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// The wire types of the profiles REST surface. REST, the CLI
// (-o json), the MCP `profiles` tool, the Web UI and the macOS app all read
// these shapes; the field names here are the contract (contracts/rest-api.md).

// ToolCounts is the number of a profile's VISIBLE tools by the tier the profile
// gives them, plus the unannotated tools it hides.
type ToolCounts struct {
	Unannotated int `json:"unannotated"`
	Read        int `json:"read"`
	Write       int `json:"write"`
	Destructive int `json:"destructive"`
}

// UsedByClient names one client credential bound to a profile.
type UsedByClient struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
}

type UsedBy struct {
	Clients []UsedByClient `json:"clients"`
}

// tokenNames lists the token-store names of every referencing credential.
func (u UsedBy) tokenNames() []string {
	out := make([]string, 0, len(u.Clients))
	for _, c := range u.Clients {
		out = append(out, "client-"+c.ID)
	}
	return out
}

func (u UsedBy) Empty() bool { return len(u.Clients) == 0 }

// ProfileView is one profile as REST returns it: the config fields as stored,
// the derived effective values, tool counts, 24 h stats and (administrators
// only) used_by.
type ProfileView struct {
	ID            string              `json:"id"`
	SelectedTools map[string][]string `json:"selected_tools,omitempty"`
	Name          string              `json:"name"`
	Servers       []string            `json:"servers"`

	SwitchableTo *[]string `json:"switchable_to,omitempty"`

	EffectiveServers []string `json:"effective_servers"`

	ToolCounts ToolCounts `json:"tool_counts"`
	Calls24h   int        `json:"calls_24h"`
	Blocked24h int        `json:"blocked_24h"`
	UsedBy     *UsedBy    `json:"used_by,omitempty"`
}

type ProfileList struct {
	Profiles []ProfileView `json:"profiles"`
}

// MovedRefs identifies clients moved by delete-with-reassign.
type MovedRefs struct {
	Clients []string `json:"clients"`
}

// RenameResult is the outcome of ProfilesService.Rename.
type RenameResult struct {
	Profile ProfileView `json:"profile"`
}

// DeleteResult is the outcome of ProfilesService.Delete.
type DeleteResult struct {
	Deleted string    `json:"deleted"`
	Moved   MovedRefs `json:"moved"`

	movedTokenNames []string
}

// WriteResult is the outcome of a create or update: the stored profile and the
// validator's warnings about it.
type WriteResult struct {
	Profile  ProfileView `json:"profile"`
	Warnings []string    `json:"warnings"`
}

// --- effective tools -------------------------------------------------------

// EffectiveToolsOptions selects the rows of ProfileEvaluator.EffectiveTools.
type EffectiveToolsOptions struct {
	// Client evaluates that client's credential UNDER the profile ("what
	// would Cursor get on this profile"). Administrators only.
	Client string
	// Server keeps only that server's rows.
	Server string
	// Reason keeps only rows with that access reason. Administrators only.
	Reason string
}

// ToolAccessView is a row's verdict: visible = listed by discovery, callable =
// a real call would run.
type ToolAccessView struct {
	Visible  bool   `json:"visible"`
	Callable bool   `json:"callable"`
	Reason   string `json:"reason"`
}

// EffectiveTool is one row of a profile's effective tools (data-model §7).
type EffectiveTool struct {
	Server        string         `json:"server"`
	Tool          string         `json:"tool"`
	IntrinsicTier string         `json:"intrinsic_tier"`
	ProfileTier   string         `json:"profile_tier"`
	Access        ToolAccessView `json:"access"`
}

// EffectiveCounts are the response-level counts. Visible and Hidden are for
// every caller; the rest are administrator-only.
type EffectiveCounts struct {
	Visible  int            `json:"visible"`
	Hidden   int            `json:"hidden"`
	Callable *int           `json:"callable,omitempty"`
	ByReason map[string]int `json:"by_reason,omitempty"`
}

// EffectiveToolsResult is GET /profiles/{name}/effective-tools.
type EffectiveToolsResult struct {
	Tools   []EffectiveTool `json:"tools"`
	Profile string          `json:"profile"`
	Counts  EffectiveCounts `json:"counts"`
}

// --- try ---------------------------------------------------------------------

// --- access explanation ----------------------------------------------------

// ExplainSubjectView names whose access was explained.
type ExplainSubjectView struct {
	Kind profile.AccessSubjectKind `json:"kind"`
	Name string                    `json:"name,omitempty"`
}

// AccessExplanation reports availability and only the reasons preventing use.
type AccessExplanation struct {
	Subject   ExplainSubjectView    `json:"subject"`
	Tool      string                `json:"tool"`
	Available bool                  `json:"available"`
	Issues    []profile.AccessIssue `json:"issues"`
}

// ProfileEvaluator is everything the profiles service needs from the server
// package's unexported predicates (the profile index, the shared per-tool gate,
// the search index). *server.MCPProxyServer implements it and
// Runtime.SetProfileEvaluator installs it (the SetBindingGuard pattern); an
// unwired evaluator answers ErrEvaluatorUnavailable (503), never an empty
// "allowed".
type ProfileEvaluator interface {
	EffectiveTools(ctx context.Context, name string, opt EffectiveToolsOptions) (*EffectiveToolsResult, error)
	Explain(ctx context.Context, subject profile.AccessSubject, tool string) (*AccessExplanation, error)
	ToolCounts(ctx context.Context, name string) ToolCounts
}

// ErrEvaluatorUnavailable is returned when no ProfileEvaluator is installed.
var ErrEvaluatorUnavailable = errors.New("profile evaluator unavailable")

// --- typed refusals ---------------------------------------------------------

// ProfileNotFoundError is `404 profile not found`.
type ProfileNotFoundError struct{ Name string }

func (e *ProfileNotFoundError) Error() string { return "profile not found" }

// IDMismatchError is `409 id_mismatch`: a PUT whose body names another
// profile than its path.
type IDMismatchError struct{ Path, Body string }

func (e *IDMismatchError) Error() string {
	return "profile ID must equal the path"
}

// Code is the wire `code`.
func (e *IDMismatchError) Code() string { return profile.ErrorCodeIDMismatch }

// ProfileInUseError is `409 profile_in_use`: clients or tokens still point at
// the profile and neither reassign_to nor force was given.
type ProfileInUseError struct{ UsedBy UsedBy }

func (e *ProfileInUseError) Error() string { return "profile in use" }

// Code is the wire `code`.
func (e *ProfileInUseError) Code() string { return profile.ErrorCodeProfileInUse }
