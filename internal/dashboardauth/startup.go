package dashboardauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

type providerMetadata struct {
	JWKSURL                  string   `json:"jwks_uri"`
	IntrospectionURL         string   `json:"introspection_endpoint"`
	IntrospectionAuthMethods []string `json:"introspection_endpoint_auth_methods_supported"`
}

// Initialize verifies dashboard sign-in before primary startup opens storage or
// listeners. Client credentials can be checked non-interactively when the
// provider offers authenticated introspection; otherwise login must verify them.
func (m *Manager) Initialize(ctx context.Context) error {
	if err := m.cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, _, err := m.provider(ctx); err != nil {
		return err
	}
	m.providerMu.Lock()
	metadata := m.metadata
	m.providerMu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("invalid identity-provider signing key URL")
	}
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("could not load identity-provider signing keys")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("identity-provider signing keys returned HTTP %d", response.StatusCode)
	}
	var keys jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&keys); err != nil {
		return fmt.Errorf("invalid identity-provider signing keys")
	}
	usable := false
	for _, key := range keys.Keys {
		if key.Valid() && key.IsPublic() && (key.Use == "" || key.Use == "sig") {
			usable = true
			break
		}
	}
	if !usable {
		return fmt.Errorf("identity provider has no usable public signing keys")
	}
	return m.checkClient(ctx, metadata)
}

func (m *Manager) checkClient(ctx context.Context, metadata providerMetadata) error {
	if metadata.IntrospectionURL == "" || m.cfg.ClientSecret == "" {
		return nil
	}
	methods := metadata.IntrospectionAuthMethods
	basic := len(methods) == 0 || slices.Contains(methods, "client_secret_basic")
	if !basic && !slices.Contains(methods, "client_secret_post") {
		return nil
	}
	// An intentionally invalid token checks client authentication without minting
	// credentials, signing in a user or touching any real token.
	values := url.Values{"token": {"mcpproxy-startup-invalid-token"}}
	if !basic {
		values.Set("client_id", m.cfg.ClientID)
		values.Set("client_secret", m.cfg.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.IntrospectionURL, strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("invalid identity-provider introspection URL")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basic {
		request.SetBasicAuth(url.QueryEscape(m.cfg.ClientID), url.QueryEscape(m.cfg.ClientSecret))
	}
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("could not validate OIDC client credentials")
	}
	defer response.Body.Close()
	var result struct {
		Active *bool  `json:"active"`
		Error  string `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result)
	if response.StatusCode == http.StatusUnauthorized || result.Error == "invalid_client" {
		return fmt.Errorf("identity provider rejected OIDC client credentials")
	}
	// Introspection permission is independent of being an OIDC sign-in client.
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusBadRequest {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OIDC client validation returned HTTP %d", response.StatusCode)
	}
	if decodeErr != nil || result.Active == nil {
		return fmt.Errorf("invalid identity-provider introspection response")
	}
	return nil
}
