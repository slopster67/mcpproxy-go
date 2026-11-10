package server

import (
	"encoding/json"

	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

func scrubUpstreamText(s string) string { return oauth.ScrubUpstreamText(s) }
func scrubUpstreamTextForRecord(s string) string {
	return activityRedaction.CapString(scrubUpstreamText(s))
}
func redactBuiltinResponseForActivity(responseText string) interface{} {
	if responseText == "" {
		return responseText
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(responseText), &parsed); err != nil || parsed == nil {
		return scrubUpstreamTextForRecord(responseText)
	}
	return redactValueWith("", parsed, activityRedaction)
}
