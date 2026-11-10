package server

import (
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

const activityArgValueLimit = 512

type redactionPolicy = oauth.Redaction

var activityRedaction = oauth.RecordRedaction.WithLimit(activityArgValueLimit)
var liveRedaction = oauth.LiveRedaction

func activityArgsFromRequest(request mcp.CallToolRequest) map[string]interface{} {
	raw := stripInternalArgsLocal(request.GetArguments())
	if len(raw) == 0 {
		return nil
	}
	return activityRedaction.Value("", raw).(map[string]interface{})
}
func redactValueWith(key string, v interface{}, r redactionPolicy) interface{} {
	return r.Value(key, v)
}
func stripInternalArgsLocal(args map[string]interface{}) map[string]interface{} {
	has := false
	for k := range args {
		if len(k) >= 6 && k[:6] == "_auth_" {
			has = true
			break
		}
	}
	if !has {
		return args
	}
	out := make(map[string]interface{}, len(args))
	for k, v := range args {
		if len(k) >= 6 && k[:6] == "_auth_" {
			continue
		}
		out[k] = v
	}
	return out
}
