// Package oauth provides OAuth authentication functionality for MCP servers.
package oauth

import (
	"errors"
)

// OAuth-specific sentinel errors. ErrFlowInProgress and ErrFlowTimeout are
// defined in coordinator.go.
var (
	// ErrServerNotOAuth indicates server doesn't use OAuth authentication.
	// This is returned when attempting OAuth operations on a non-OAuth server.
	ErrServerNotOAuth = errors.New("server does not use OAuth")

	// ErrRefreshFailed indicates token refresh failed after all retry attempts.
	// This typically requires manual re-authentication via browser.
	ErrRefreshFailed = errors.New("OAuth token refresh failed")

	// ErrNoRefreshToken indicates refresh token is not available.
	// Some OAuth providers don't issue refresh tokens.
	ErrNoRefreshToken = errors.New("no refresh token available")
)
