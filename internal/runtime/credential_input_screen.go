package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

// The secret-shaped input screen. It
// runs FIRST on every credential issuance, on every surface, so a credential,
// the API key or a detector-flagged secret pasted into issuance metadata
// (purpose, display_name, an id, an expiry, a profile alias, a REST array
// element) is refused before anything is generated, stored, audited, echoed
// or logged.

// UnknownArgumentField is the fixed placeholder a refusal names instead of a
// caller-supplied key: a key is never echoed.
const UnknownArgumentField = "(unknown argument)"

// SecretInputError is the `secret_in_argument` refusal. Fields are the KNOWN
// offending argument names (sorted, deduplicated).
type SecretInputError struct {
	Fields []string
}

// Field is the first offending known argument, or the fixed placeholder.
func (e *SecretInputError) Field() string {
	if len(e.Fields) > 0 {
		return e.Fields[0]
	}
	return UnknownArgumentField
}

// Error names the field, never the value.
func (e *SecretInputError) Error() string {
	if len(e.Fields) == 0 {
		return "an unrecognised argument looks like a credential or secret; it was not stored. Remove it and retry"
	}
	return fmt.Sprintf("argument %q looks like a credential or secret; it was not stored. Remove it and retry", e.Field())
}

// Code is the wire `code`.
func (e *SecretInputError) Code() string { return profile.CredentialErrorCodeSecretInArgument }

// CredentialScreen is one evaluation context of the screen: the forced
// detector and the live API key.
type CredentialScreen struct{}

func NewCredentialScreen() *CredentialScreen { return &CredentialScreen{} }

// SecretShaped reports whether s carries an issued-credential prefix, the
// configured API key, or anything the forced detector flags. A truncated scan
// is treated as a hit (fail closed).
func (c *CredentialScreen) SecretShaped(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "mcp_agt_") || strings.Contains(lower, auth.ClientTokenPrefixStr) {
		return true
	}
	return false
}

func (c *CredentialScreen) ScreenFields(fields []ScreenField) *SecretInputError {
	hit := map[string]bool{}
	for _, f := range fields {
		for _, v := range f.Values {
			if c.SecretShaped(v) {
				hit[f.Name] = true
				break
			}
		}
	}
	if len(hit) == 0 {
		return nil
	}
	names := make([]string, 0, len(hit))
	for n := range hit {
		names = append(names, n)
	}
	sort.Strings(names)
	return &SecretInputError{Fields: names}
}

// ScreenField is one named, caller-supplied field and its raw value(s).
type ScreenField struct {
	Name   string
	Values []string
}
