// Package health provides unified health status calculation for upstream MCP servers.
//
// IMPORTANT: These constants are mirrored in TypeScript. When adding or modifying
// health levels, admin states, or actions, update frontend/src/types/contracts.ts
// to match.
package health

// Health levels
const (
	LevelHealthy   = "healthy"
	LevelDegraded  = "degraded"
	LevelUnhealthy = "unhealthy"
)

// Admin states
const (
	StateEnabled  = "enabled"
	StateDisabled = "disabled"
)

// Actions - suggested remediation for health issues
const (
	ActionNone      = ""
	ActionLogin     = "login"
	ActionRestart   = "restart"
	ActionEnable    = "enable"
	ActionApprove   = "approve"
	ActionViewLogs  = "view_logs"
	ActionSetSecret = "set_secret"
	ActionConfigure = "configure"
	// ActionEditURL is offered when the endpoint itself is unusable — the host
	// does not resolve, the scheme is unsupported, or the URL is malformed.
	// Restarting cannot fix any of those; editing the address can.
	ActionEditURL = "edit_url"
)

const (
	StatusReady          = "ready"
	StatusConnecting     = "connecting"
	StatusSignInRequired = "sign_in_required"

	StatusNeedsSecret = "needs_secret"
	StatusNeedsConfig = "needs_config"
	StatusError       = "error"
	StatusDisabled    = "disabled"
)
