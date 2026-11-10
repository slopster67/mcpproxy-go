package peeragent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

func (a *Agent) token(name string) *storage.MemoryOAuth {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens[name]
}
func (a *Agent) newTokenStore(name string, snapshot storage.OAuthSnapshot) *storage.MemoryOAuth {
	return storage.NewMemoryOAuth(name, snapshot, func(update storage.OAuthUpdate) (uint64, error) {
		data, _ := json.Marshal(update)
		body, _ := json.Marshal(peering.Frame{Op: "credentials", Server: name, Data: data})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, "POST", a.endpoint(), strings.NewReader(string(body)))
		if err != nil {
			return 0, err
		}
		request.Header.Set("Authorization", "Bearer "+a.cfg.PeerToken)
		request.Header.Set("Content-Type", "application/json")
		response, err := a.client.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		if response.StatusCode == http.StatusConflict {
			return 0, commitview.ErrStaleGeneration
		}
		if response.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("primary credential storage unavailable (%d)", response.StatusCode)
		}
		var result struct {
			Revision uint64 `json:"revision"`
		}
		err = json.NewDecoder(response.Body).Decode(&result)
		return result.Revision, err
	})
}
func (a *Agent) flushCredentials() {
	a.mu.Lock()
	stores := []*storage.MemoryOAuth{}
	for _, store := range a.tokens {
		stores = append(stores, store)
	}
	a.mu.Unlock()
	for _, store := range stores {
		_ = store.Flush()
	}
}
func (a *Agent) configure(ctx context.Context, desired peering.Connection) (*managed.Client, error) {
	if desired.Config == nil {
		return nil, fmt.Errorf("server configuration required")
	}
	name := desired.Config.Name
	lock := a.serverLock(name)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := config.CopyServerConfig(desired.Config)
	cfg.HostID = nil
	cfg.AvailableToPeers = nil
	if cfg.OAuth != nil && cfg.OAuth.CallbackBaseURL == "" && a.url.Scheme == "https" {
		cfg.OAuth.CallbackBaseURL = a.cfg.PeerPrimaryURL
	}
	a.mu.Lock()
	current, previous, token := a.servers[name], a.settings[name], a.tokens[name]
	version := a.versions[name]
	supplied := a.secrets[name]
	a.mu.Unlock()
	if desired.Generation < version {
		return nil, commitview.ErrStaleGeneration
	}
	// A delayed sync must not replace a newer logout or credential lease.
	if token != nil && desired.OAuth.Revision < token.Revision() && token.Lease() != desired.OAuth.Lease {
		return nil, commitview.ErrStaleGeneration
	}
	if token != nil && token.Lease() != desired.OAuth.Lease {
		token.Retire()
		token = nil
	}
	if token == nil {
		token = a.newTokenStore(name, desired.OAuth)
	}
	if current != nil && !current.Retired() && previous != nil && config.ServerEntryEqual(previous, cfg) && current.CredentialBinding() == desired.Binding && maps.Equal(supplied, desired.Secrets) && a.token(name) == token {
		return current, nil
	}
	if current != nil {
		current.Retire()
		_ = current.Disconnect()
	}
	a.refresh.OnTokenCleared(name)
	resolver := secret.ExecutionResolver(desired.Secrets)
	instance, err := managed.NewClientAt(name, cfg, desired.Generation, a.logger, a.global.Logging, a.global, token, resolver)
	if err != nil {
		return nil, err
	}
	instance.SetCredentialBinding(desired.Binding)
	instance.SetUserLoggedOut(desired.OAuth.LoggedOut)
	instance.SuppressBrowser()
	a.mu.Lock()
	a.tokens[name] = token
	a.resolvers[name] = resolver
	a.secrets[name] = maps.Clone(desired.Secrets)
	a.versions[name] = desired.Generation
	a.servers[name] = instance
	a.settings[name] = cfg
	a.mu.Unlock()
	return instance, nil
}
func (a *Agent) ListOAuthTokens() ([]*storage.OAuthTokenRecord, error) {
	a.mu.Lock()
	stores := []*storage.MemoryOAuth{}
	for _, store := range a.tokens {
		stores = append(stores, store)
	}
	a.mu.Unlock()
	records := []*storage.OAuthTokenRecord{}
	for _, store := range stores {
		if record := store.Token(); record != nil {
			records = append(records, record)
		}
	}
	return records, nil
}
func (a *Agent) GetOAuthToken(key string) (*storage.OAuthTokenRecord, error) {
	records, _ := a.ListOAuthTokens()
	for _, record := range records {
		if record.ServerName == key || record.DisplayName == key {
			return record, nil
		}
	}
	return nil, fmt.Errorf("oauth token not found")
}
