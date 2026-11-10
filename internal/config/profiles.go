package config

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ProfileConfig identifies a tool policy independently of its editable display name.
type ProfileConfig struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Servers []string `json:"servers"`
	// SelectedTools narrows a listed server to exact raw tool names. Missing key means all tools; [] means none.
	SelectedTools map[string][]string `json:"selected_tools,omitempty"`

	SwitchableTo *[]string `json:"switchable_to,omitempty"`
}

// IsValidProfileID accepts only backend-generated, filesystem-safe profile IDs.
func IsValidProfileID(id string) bool {
	if !strings.HasPrefix(id, "profile-") {
		return false
	}
	parsed, err := uuid.Parse(strings.TrimPrefix(id, "profile-"))
	return err == nil && id == "profile-"+parsed.String()
}

func ValidateProfiles(cfg *Config) (warnings []string, err error) {
	detailed, err := ValidateProfilesDetailed(cfg)
	for _, w := range detailed {
		warnings = append(warnings, w.Message)
	}
	return warnings, err
}

type ProfileValidationError struct {
	Index   int
	Field   string
	Message string
}

func (e *ProfileValidationError) Error() string { return e.Message }

func profileErr(index int, field, format string, args ...interface{}) *ProfileValidationError {
	return &ProfileValidationError{Index: index, Field: field, Message: fmt.Sprintf(format, args...)}
}

type ProfileWarning struct {
	Profile string
	Message string
}

// ValidateProfilesDetailed validates profile names, selections and switch targets.
// Empty profiles are valid. Missing server or switch-target references produce
// diagnostics without granting access to those missing entries.
func ValidateProfilesDetailed(cfg *Config) (warnings []ProfileWarning, err error) {

	if cfg == nil || len(cfg.Profiles) == 0 {
		return nil, nil
	}

	// Build the set of known server names for membership checks.
	known := make(map[string]struct{}, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s != nil {
			known[s.Name] = struct{}{}
		}
	}

	profileIDs := make(map[string]struct{}, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		profileIDs[p.ID] = struct{}{}
	}

	seen := make(map[string]int, len(cfg.Profiles)) // ID -> first index
	for i, p := range cfg.Profiles {
		// IDs are distinct from editable display names.
		if !IsValidProfileID(p.ID) {
			return warnings, profileErr(i, "id", "profiles[%d]: invalid profile ID %q", i, p.ID)
		}
		if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 64 {
			return warnings, profileErr(i, "name", "profile name must contain 1–64 characters")
		}
		// Reject duplicate identities.
		if first, dup := seen[p.ID]; dup {
			return warnings, profileErr(i, "id", "profiles[%d]: duplicate profile ID %q (already defined at profiles[%d])", i, p.ID, first)
		}
		seen[p.ID] = i

		// Fatal: switchable_to self-reference.
		if p.SwitchableTo != nil {
			for _, target := range *p.SwitchableTo {
				if target == p.ID {
					return warnings, profileErr(i, "switchable_to", "profiles[%d]: switchable_to cannot include the profile itself", i)
				}
			}
		}

		for server, tools := range p.SelectedTools {
			found := false
			for _, selectedServer := range p.Servers {
				if server == selectedServer {
					found = true
					break
				}
			}
			if !found {
				return warnings, profileErr(i, "selected_tools", "profiles[%d]: selected tools name a server outside this profile: %q", i, server)
			}
			for _, tool := range tools {
				if strings.TrimSpace(tool) == "" {
					return warnings, profileErr(i, "selected_tools", "profiles[%d]: selected tool names cannot be empty", i)
				}
			}
		}
		// Warning: unknown server references (warn-and-skip).
		for _, srv := range p.Servers {
			if _, ok := known[srv]; !ok {
				warnings = append(warnings, ProfileWarning{Profile: p.ID, Message: fmt.Sprintf("profile %q references unknown server %q; it will be skipped", p.Name, srv)})
			}
		}
		// Warning: switchable_to names an unknown profile (fatal cases
		// handled above; the target may be defined later in the list, so
		// this is checked against the full profileIDs set built up
		// front).
		if p.SwitchableTo != nil {
			for _, target := range *p.SwitchableTo {
				if _, ok := profileIDs[target]; !ok {
					warnings = append(warnings, ProfileWarning{Profile: p.ID, Message: fmt.Sprintf("profile %q switchable_to references unknown profile %q; ignored", p.Name, target)})
				}
			}
		}
	}

	return warnings, nil
}

// EffectiveServers returns the profile's server list filtered to those that
// actually exist in cfg (the warn-skip result). Order is preserved and unknown
// names are dropped. Used by the profile middleware to build the request scope.
func (p ProfileConfig) EffectiveServers(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	known := make(map[string]struct{}, len(cfg.Servers))
	for _, s := range cfg.Servers {
		if s != nil {
			known[s.Name] = struct{}{}
		}
	}
	out := make([]string, 0, len(p.Servers))
	for _, srv := range p.Servers {
		if _, ok := known[srv]; ok {
			out = append(out, srv)
		}
	}
	return out
}
