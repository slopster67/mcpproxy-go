package secret

import (
	"context"
)

// Ref represents a reference to a secret
type Ref struct {
	Type     string `json:"type"`     // env or secret
	Name     string `json:"name"`     // environment variable name, secret name, etc.
	Original string `json:"original"` // original reference string
}

// Provider interface for secret resolution
type Provider interface {
	// CanResolve returns true if this provider can handle the given secret type
	CanResolve(secretType string) bool

	// Resolve retrieves the actual secret value
	Resolve(ctx context.Context, ref Ref) (string, error)

	// Store saves a secret (if supported by the provider)
	Store(ctx context.Context, ref Ref, value string) error

	// Delete removes a secret (if supported by the provider)
	Delete(ctx context.Context, ref Ref) error

	// List returns all secret references handled by this provider
	List(ctx context.Context) ([]Ref, error)

	// IsAvailable checks if the provider is available on the current system
	IsAvailable() bool
}

// Resolver manages secret resolution using multiple providers
type Resolver struct {
	providers map[string]Provider
}

// EnvVarStatus represents the status of an environment variable reference
type EnvVarStatus struct {
	Servers   []string `json:"servers"`
	HostID    string   `json:"host_id,omitempty"`
	HostName  string   `json:"host_name,omitempty"`
	Available bool     `json:"available"`
	Ref       Ref      `json:"secret_ref"`
	IsSet     bool     `json:"is_set"`
}

// SecretStatus represents the status of a stored secret reference
type SecretStatus struct {
	Servers []string `json:"servers"`
	Ref     Ref      `json:"secret_ref"`
	IsSet   bool     `json:"is_set"`
}

// ConfigSecretsResponse contains secrets and environment variables referenced in config
type ConfigSecretsResponse struct {
	Secrets         []SecretStatus `json:"secrets"`
	EnvironmentVars []EnvVarStatus `json:"environment_vars"`
	TotalSecrets    int            `json:"total_secrets"`
	TotalEnvVars    int            `json:"total_env_vars"`
}
