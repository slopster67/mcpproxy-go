package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

const redactedValue = "[redacted]"

// subCallActivityKey marks a management call made from a code_execution script:
// the script's runner records that call as a sub-call itself, so the management
// handler does not record it a second time.
type subCallActivityKey struct{}

// credentialFields are management payload fields that always hold a credential,
// whatever their key looks like: secret values, and the one-time credential and
// connection snippet returned when a client or peer is created or rotated.
var credentialFields = map[string]bool{
	"value":        true,
	"credential":   true,
	"snippet":      true,
	"generic_http": true,
}

// redactManagementPayload returns a copy of a management tool's arguments or
// result with every credential replaced by "[redacted]", so the activity log can
// show what an administration call did (which server, which operation) without
// storing secrets or newly minted tokens. Names stay readable: an env or headers
// map keeps its keys, an argv list keeps its length.
func redactManagementPayload(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = redactManagementField(key, item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = redactManagementPayload(item)
		}
		return out
	case string:
		return oauth.MaskDetectedSecrets(value)
	default:
		return value
	}
}

func redactManagementField(key string, item any) any {
	switch {
	case oauth.IsEnvFieldName(key) || oauth.IsHeadersFieldName(key):
		if entries, ok := item.(map[string]any); ok {
			masked := make(map[string]any, len(entries))
			for name := range entries {
				masked[name] = redactedValue
			}
			return masked
		}
		return redactedValue
	case oauth.IsArgvFieldName(key):
		if list, ok := item.([]any); ok {
			masked := make([]any, len(list))
			for i := range list {
				masked[i] = redactedValue
			}
			return masked
		}
		return redactedValue
	case credentialFields[strings.ToLower(key)] || oauth.IsSensitiveKeyName(key):
		return redactedValue
	}
	if text, ok := item.(string); ok && (strings.EqualFold(key, "url") || strings.HasSuffix(strings.ToLower(key), "_url")) {
		return oauth.RedactURL(oauth.MaskDetectedSecrets(text))
	}
	return redactManagementPayload(item)
}

// redactedManagementMap redacts a decoded-JSON-compatible value and returns it
// as a map, or nil when it is not an object.
func redactedManagementMap(v any) map[string]any {
	redacted, _ := redactManagementJSON(v).(map[string]any)
	return redacted
}

// redactManagementJSON round-trips v through JSON so typed values (structs) are
// redacted the same way as decoded maps. A value that cannot be encoded yields
// nil.
func redactManagementJSON(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil
	}
	return redactManagementPayload(decoded)
}

func withSubCallActivity(ctx context.Context) context.Context {
	return context.WithValue(ctx, subCallActivityKey{}, true)
}

func isSubCallActivity(ctx context.Context) bool {
	marked, _ := ctx.Value(subCallActivityKey{}).(bool)
	return marked
}
