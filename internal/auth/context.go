package auth

import (
	"context"
	"errors"
)

// Auth type constants.
const (
	AuthTypeAdmin     = "admin"      // Internal administrator context
	AuthTypeClient    = "client"     // Saved client token authentication
	AuthTypeAdminUser = "admin_user" // OIDC-authenticated dashboard administrator
)

type CredentialKind string

// The credential kinds an AuthContext can carry.
const (
	CredentialKindSession     CredentialKind = "session"      // OIDC browser session
	CredentialKindClientToken CredentialKind = "client_token" // an mcp_cli_ client token
	CredentialKindAnonymous   CredentialKind = "anonymous"    // no authenticated credential
)

// AuthContext carries authentication identity through request context.
type AuthContext struct {
	AllowCodeExecution bool
	AllowManaging      bool
	ServerAvailable    func(string) bool
	HostID             string
	Type               string   // one of the AuthType* constants
	AgentName          string   // Name of the client token (empty for admin)
	TokenPrefix        string   // First 12 chars of raw token (empty for admin)
	AllowedServers     []string // Servers this token can access (nil = all for admin)
	Permissions        []string // Permission tiers (nil = all for admin)
	ProfilePin         string   // Profile this client token is pinned to (empty if unpinned)

	// TokenKind, ClientID and ProfileMode are the client-credential identity,
	// copied from ClientCredential.AuthContext. TokenKind is KindClient for a
	// client-token context and "" otherwise (admin, user, anonymous).
	TokenKind   string
	ClientID    string
	ProfileMode string

	// OIDC user identity. Empty for non-user auth types.
	UserID      string // OIDC subject identifier
	Email       string // User's email from the OIDC provider
	DisplayName string // User's display name

	// CredentialKind is the credential source that authenticated this request.
	// Stamped by the dashboard session middleware and mcpAuthMiddleware; empty
	// for contexts built in-process.
	CredentialKind CredentialKind
}

// contextKey is an unexported type used as context key to avoid collisions.
type contextKey struct{}

// authContextKey is the context key for AuthContext values.
var authContextKey = contextKey{}

// WithAuthContext returns a new context with the given AuthContext attached.
func WithAuthContext(ctx context.Context, ac *AuthContext) context.Context {
	return context.WithValue(ctx, authContextKey, ac)
}

// AuthContextFromContext extracts the AuthContext from the context.
// Returns nil if no AuthContext is present.
func AuthContextFromContext(ctx context.Context) *AuthContext {
	ac, _ := ctx.Value(authContextKey).(*AuthContext)
	return ac
}

// IsAdmin returns true if this is an admin authentication context.
// Internal admins ("admin") and OIDC-authenticated admins ("admin_user") are considered admin.
func (ac *AuthContext) IsAdmin() bool {
	return ac.Type == AuthTypeAdmin || ac.Type == AuthTypeAdminUser
}

// IsClientCredential reports whether this context authenticated with a client
// credential (kind=client, secret prefix mcp_cli_). Nil-safe.
func (ac *AuthContext) IsClientCredential() bool {
	return ac != nil && ac.TokenKind == KindClient
}

// CanAccessServer checks that the caller has an authenticated client or administrator identity.
// The effective profile separately decides which servers are in scope.
func (ac *AuthContext) CanAccessServer(name string) bool {
	return ac != nil && name != "" && (ac.IsAdmin() || ac.IsClientCredential()) && (ac.ServerAvailable == nil || ac.ServerAvailable(name))
}

// AdminContext returns an AuthContext representing full internal administrator access.
func AdminContext() *AuthContext {
	return &AuthContext{
		Type: AuthTypeAdmin,
	}
}

// IsScopedCaller reports whether ctx carries a non-admin identity whose
// visibility is limited by AllowedServers. An admin (or an absent context)
// sees everything.
func IsScopedCaller(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	ac := AuthContextFromContext(ctx)
	return ac != nil && !ac.IsAdmin()
}

func (ac *AuthContext) CanManageMCPProxy() bool {
	return ac != nil && (ac.IsAdmin() || ac.IsClientCredential() && ac.AllowManaging)
}

var ErrCodeExecutionDenied = errors.New("Code execution is not allowed for this client")
