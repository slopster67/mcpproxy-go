package peeragent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/httpx"
	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
	"github.com/slopster67/mcpproxy-go/internal/upstream/types"
)

func (a *Agent) manage(ctx context.Context, f peering.Frame) (any, error) {
	switch f.Op {
	case "logs":
		var request struct {
			Tail int `json:"tail"`
		}
		_ = json.Unmarshal(f.Data, &request)
		if request.Tail <= 0 || request.Tail > 1000 {
			request.Tail = 100
		}
		return logs.ReadUpstreamServerLogTail(a.global.Logging, f.Server, request.Tail)
	case "oauth/login":
		a.mu.Lock()
		cfg := config.CopyServerConfig(a.settings[f.Server])
		instance := a.servers[f.Server]
		token, resolver := a.tokens[f.Server], a.resolvers[f.Server]
		a.mu.Unlock()
		if cfg == nil || instance == nil {
			return nil, fmt.Errorf("server has not connected to this peer")
		}
		if cfg.OAuthMode() == config.OAuthModeOff {
			return nil, fmt.Errorf("OAuth is off for %s", f.Server)
		}
		if cfg.OAuth == nil {
			cfg.OAuth = &config.OAuthConfig{}
		}
		var request struct {
			Origin string `json:"origin"`
		}
		_ = json.Unmarshal(f.Data, &request)
		origin := strings.TrimRight(strings.TrimSpace(request.Origin), "/")
		if origin == "" && a.url.Scheme == "https" {
			origin = strings.TrimRight(a.cfg.PeerPrimaryURL, "/")
		}
		client, err := core.NewClient(cfg.Name, cfg, a.logger, a.global.Logging, a.global, token, resolver)
		if err != nil {
			return nil, err
		}
		client.SuppressBrowser()
		client.SetGate(instance.Gate())
		client.SetCredentialBinding(instance.CredentialBinding())
		flowCtx, cancel := context.WithTimeout(instance.Gate().Context(), 30*time.Minute)
		if origin != "" {
			flowCtx = httpx.WithPublicOrigin(flowCtx, origin)
		}
		result, err := client.StartOAuthFlowQuick(flowCtx)
		if err != nil {
			cancel()
			return result, err
		}
		go func() { <-flowCtx.Done(); cancel() }()
		return result, nil
	case "oauth/logout":
		var snapshot storage.OAuthSnapshot
		if err := json.Unmarshal(f.Data, &snapshot); err != nil {
			return nil, err
		}
		a.mu.Lock()
		cfg := config.CopyServerConfig(a.settings[f.Server])
		values := a.secrets[f.Server]
		generation := a.versions[f.Server]
		a.mu.Unlock()
		if cfg == nil {
			return nil, fmt.Errorf("server not configured")
		}
		instance, err := a.configure(ctx, peering.Connection{Config: cfg, Secrets: values, OAuth: snapshot, Binding: snapshot.Binding, Generation: generation})
		if err != nil {
			return nil, err
		}
		instance.SetUserLoggedOut(true)
		a.refresh.OnTokenCleared(f.Server)
		return nil, nil
	case "oauth/callback":
		var query string
		if err := json.Unmarshal(f.Data, &query); err != nil {
			return nil, err
		}
		request := httptest.NewRequest("GET", a.cfg.PeerPrimaryURL+config.OAuthRemoteCallbackPrefix+url.PathEscape(f.Server)+"?"+query, nil)
		response := httptest.NewRecorder()
		oauth.GetGlobalCallbackManager().RemoteCallbackHandler(response, request)
		return map[string]any{"status": response.Code, "body": response.Body.String(), "content_type": response.Header().Get("Content-Type")}, nil
	}
	return nil, fmt.Errorf("unknown peer operation %q", f.Op)
}

func (a *Agent) RefreshOAuthToken(name string) error {
	a.mu.Lock()
	instance := a.servers[name]
	a.mu.Unlock()
	if instance == nil {
		return fmt.Errorf("server not connected")
	}
	a.entryMu.RLock()
	session := a.entryContext
	a.entryMu.RUnlock()
	if !a.online.Load() || session == nil {
		return peering.ErrOffline
	}
	ctx, cancel := context.WithTimeout(session, 30*time.Second)
	defer cancel()
	return instance.RefreshOAuthTokenDirect(ctx)
}
func (a *Agent) hello() peering.Hello {
	a.mu.Lock()
	defer a.mu.Unlock()
	hello := peering.Hello{ProcessID: a.processID, Environment: map[string]bool{}, Address: a.cfg.Listen, Servers: map[string]peering.ServerState{}}
	for _, name := range a.environment {
		_, set := os.LookupEnv(name)
		hello.Environment[name] = set
	}
	for name, instance := range a.servers {
		info := instance.GetConnectionInfo()
		state := peering.ServerState{UserLoggedOut: instance.IsUserLoggedOut(), Connected: instance.IsConnected(), Authenticated: instance.ConnectedWithOAuth(), AuthRequired: info.IsOAuthError || info.State == types.StatePendingAuth || instance.IsOAuthCallRequired()}
		if info.LastError != nil {
			state.LastError = oauth.ScrubUpstreamText(info.LastError.Error())
		}
		if store := a.tokens[name]; store != nil && state.Authenticated {
			if token := store.Token(); token != nil && !token.ExpiresAt.IsZero() {
				expires := token.ExpiresAt
				state.ExpiresAt = &expires
			}
		}
		hello.Servers[name] = state
	}
	return hello
}
func (a *Agent) sendHello(ctx context.Context) error {
	a.flushCredentials()
	data, err := json.Marshal(a.hello())
	if err != nil {
		return err
	}
	return a.post(ctx, peering.Frame{Op: "hello", Data: data})
}
