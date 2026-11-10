package storage

import "strings"

var glanceDiscoveryBuiltins = map[string]bool{
	"retrieve_tools": true,
	"describe_tool":  true,
}

// glanceManagementBuiltins never appear in the glance at all, whatever their
// status: they are mcpproxy's own housekeeping, not work the user asked an
// upstream for.
var glanceManagementBuiltins = map[string]bool{
	"upstream_servers":    true,
	"quarantine_security": true,
}

func CountsAsCall(rec *ActivityRecord) (counted, isError bool) {
	if rec == nil || rec.ToolName == "" {
		return false, false
	}

	switch rec.Type {
	case ActivityTypeToolCall:
		switch rec.Status {
		case ActivityStatusBlocked:
			return true, true
		default:
			return true, rec.Status == ActivityStatusError
		}

	case ActivityTypeInternalToolCall:
		if strings.HasPrefix(rec.ToolName, InternalCallToolPrefix) {
			return false, false
		}
		if glanceManagementBuiltins[rec.ToolName] {
			return false, false
		}
		if rec.Status == ActivityStatusSuccess && !glanceDiscoveryBuiltins[rec.ToolName] {
			return false, false
		}
		// Anything a built-in did not complete is a failure, whether it errored
		// or a policy refused it.
		return true, rec.Status != ActivityStatusSuccess

	case ActivityTypePolicyDecision:
		if rec.Status == ActivityStatusBlocked {
			return true, true
		}
		return false, false

	default:
		return false, false
	}
}
