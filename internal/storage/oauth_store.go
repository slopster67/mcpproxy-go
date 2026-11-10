package storage

import "github.com/slopster67/mcpproxy-go/internal/commitview"

// OAuthStore is the credential state used by an execution host. The primary
// persists it; peers use memory and synchronize changes to the primary.
type OAuthStore interface {
	GetOAuthTokenBound(string, commitview.CredentialBinding) (*OAuthTokenRecord, error)
	GetOAuthClientCredentialsBound(string, commitview.CredentialBinding) (string, string, int, string, uint64, error)
	UpdateOAuthTokenIfBound(string, string, commitview.CredentialBinding, func(*OAuthTokenRecord) error) (bool, error)
	SaveOAuthClientIfBound(string, string, commitview.CredentialBinding, OAuthClientSaveMode, string, string, int, string) (bool, error)
	DeleteOAuthTokenIfBound(string, string, commitview.CredentialBinding, uint64) (bool, error)
	ClearOAuthClientIfBound(string, string, commitview.CredentialBinding, uint64) (bool, error)
	ClearOAuthClientIfBoundAndClientID(string, string, commitview.CredentialBinding, string) (bool, error)
	SaveOAuthCompletionEventIfBound(*OAuthCompletionEvent, commitview.CredentialBinding) (bool, error)
}

// OAuthSnapshot is the primary's durable grant plus its current writer lease.
type OAuthSnapshot struct {
	LoggedOut bool                         `json:"logged_out"`
	Binding   commitview.CredentialBinding `json:"binding"`
	Revision  uint64                       `json:"revision"`
	Lease     string                       `json:"lease"`
	Record    *OAuthTokenRecord            `json:"record"`
}
type OAuthUpdate struct {
	Binding  commitview.CredentialBinding `json:"binding"`
	Revision uint64                       `json:"revision"`
	Lease    string                       `json:"lease"`
	ID       string                       `json:"id"`
	Key      string                       `json:"key"`
	Record   *OAuthTokenRecord            `json:"record"`
}
