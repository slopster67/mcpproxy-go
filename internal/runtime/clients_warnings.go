package runtime

import (
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// WarningAction points to the client whose profile needs updating.
type WarningAction struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

// Warning is a client issue shown consistently across surfaces.
type Warning struct {
	Code     profile.WarningCode     `json:"code"`
	Severity profile.WarningSeverity `json:"severity"`
	ClientID string                  `json:"client_id,omitempty"`
	Message  string                  `json:"message"`
	Action   *WarningAction          `json:"action,omitempty"`
}

// Warnings derives the credential and guard warnings. states maps client id
// to its classified credential_state where the caller has it (an on-demand
// classification or the persisted last observation); rows not in the map
// contribute no admin-key warning.
func (s *ClientsService) Warnings() []Warning {
	all, err := s.records()
	if err != nil {
		return nil
	}
	var out []Warning
	cfg := s.cfg()
	for i := range all {
		t := &all[i]
		switch {
		case t.Kind == auth.KindClient && s.stateOf(t) == profile.CredentialStateClient:

			if t.ProfilePin != "" && !s.profileExists(cfg, t.ProfilePin) {
				out = append(out, Warning{Code: profile.WarningProfileMissing, Severity: profile.WarningSeverityWarn, ClientID: t.ClientID,
					Message: fmt.Sprintf("the profile %s bound to %s no longer exists; the client is denied everything", t.ProfilePin, t.ClientID),
					Action:  &WarningAction{Kind: string(profile.FixMoveClient), Target: t.ClientID}})
			}
		}
	}

	return out
}
