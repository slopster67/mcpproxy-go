// Package oauth provides OAuth authentication functionality for MCP servers.
package oauth

import (
	"strings"
)

// OAuthStatus represents the current authentication state of an OAuth server.
type OAuthStatus string

const (
	// OAuthStatusNone indicates the server does not use OAuth.
	OAuthStatusNone OAuthStatus = "none"

	// OAuthStatusAuthenticated indicates valid OAuth token is available.
	OAuthStatusAuthenticated OAuthStatus = "authenticated"

	// OAuthStatusExpired indicates OAuth token has expired.
	OAuthStatusExpired OAuthStatus = "expired"

	// OAuthStatusError indicates OAuth authentication error.
	OAuthStatusError OAuthStatus = "error"
)

// IsOAuthError checks if an error message indicates an OAuth-related problem.
func IsOAuthError(err string) bool {
	lowerErr := strings.ToLower(err)
	oauthIndicators := []string{
		"oauth",
		"authentication",
		"unauthorized",
		"401",
		"token",
		"authorization",
		"access denied",
	}
	for _, indicator := range oauthIndicators {
		if strings.Contains(lowerErr, indicator) {
			return true
		}
	}
	return false
}
