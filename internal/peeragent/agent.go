// Package peeragent runs a host's upstream connections without a management UI,
// profiles, client credentials, an index, or an activity database.
package peeragent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	sdktransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
	"go.uber.org/zap"
)

type Agent struct {
	opMu         sync.RWMutex
	syncGen      int64
	versions     map[string]int64
	assigned     map[string]bool
	connectedAt  time.Time
	refresh      *oauth.RefreshManager
	cfg          *config.Config
	url          *url.URL
	client       *http.Client
	logger       *zap.Logger
	tokens       map[string]*storage.MemoryOAuth
	resolvers    map[string]*secret.Resolver
	secrets      map[string]map[string]string
	processID    string
	environment  []string
	global       *config.Config
	online       atomic.Bool
	entryMu      sync.RWMutex
	entryContext context.Context
	mu           sync.Mutex
	servers      map[string]*managed.Client
	settings     map[string]*config.ServerConfig
	calls        map[string]context.CancelFunc
	locks        map[string]*sync.Mutex
}

func Run(ctx context.Context, cfg *config.Config, logger *zap.Logger) error {
	remote, err := url.Parse(cfg.PeerPrimaryURL)
	if err != nil {
		return err
	}
	cfg.PeerPrimaryURL = strings.TrimRight(cfg.PeerPrimaryURL, "/")
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return err
	}
	global := cfg
	global.Logging = global.EffectiveLogConfig()
	if err := os.MkdirAll(global.Logging.LogDir, 0700); err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	controlClient := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer controlClient.CloseIdleConnections()
	a := &Agent{versions: map[string]int64{}, assigned: map[string]bool{}, cfg: cfg, url: remote, client: controlClient, logger: logger, tokens: map[string]*storage.MemoryOAuth{}, resolvers: map[string]*secret.Resolver{}, secrets: map[string]map[string]string{}, processID: uuid.NewString(), global: global, servers: map[string]*managed.Client{}, settings: map[string]*config.ServerConfig{}, calls: map[string]context.CancelFunc{}, locks: map[string]*sync.Mutex{}}
	a.refresh = oauth.NewRefreshManager(a, oauth.GetGlobalCoordinator(), logger)
	a.refresh.SetRuntime(a)
	oauth.GetTokenStoreManager().SetTokenSavedBoundCallback(func(name string, expires time.Time, binding commitview.CredentialBinding) {
		a.refresh.OnTokenSavedFor(name, expires, binding)
		a.mu.Lock()
		instance := a.servers[name]
		a.mu.Unlock()
		if instance != nil && instance.CredentialBinding() == binding && !instance.IsConnected() {
			go func() {
				instance.SetUserLoggedOut(false)
				if !instance.PrepareConnectionRetry(false) {
					return
				}
				_ = a.sendHello(context.Background())
				_ = a.post(context.Background(), peering.Frame{Op: "server_changed", Server: name})
			}()
		}
	})
	if err := a.refresh.Start(ctx); err != nil {
		return err
	}
	defer a.refresh.Stop()
	defer a.closeServers()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	cfg.Listen = listener.Addr().String()
	a.cfg.Listen = cfg.Listen
	server := &http.Server{Handler: a.mcpHandler(), ReadHeaderTimeout: 10 * time.Second}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.Serve(listener) }()
	controlCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	controlDone := make(chan struct{})
	go func() { defer close(controlDone); a.controlLoop(controlCtx) }()
	defer func() { cancel(); <-controlDone }()
	logger.Info("Peer MCP endpoint listening", zap.String("listen", listener.Addr().String()))
	select {
	case <-ctx.Done():
	case err = <-errorsCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	a.online.Store(false)
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	return server.Shutdown(shutdownCtx)
}
func (a *Agent) mcpHandler() http.Handler {
	target := *a.url
	proxy := httputil.NewSingleHostReverseProxy(&target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.URL.Path = strings.TrimSuffix(target.Path, "/") + "/mcp"
		r.Host = target.Host
		r.Header.Set(peering.Header, a.cfg.PeerToken)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "Primary unavailable", http.StatusServiceUnavailable)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if !a.online.Load() {
			http.Error(w, "Primary unavailable", http.StatusServiceUnavailable)
			return
		}
		a.entryMu.RLock()
		session := a.entryContext
		a.entryMu.RUnlock()
		if session == nil || session.Err() != nil {
			http.Error(w, "Primary unavailable", 503)
			return
		}
		requestCtx, stop := context.WithCancel(r.Context())
		defer stop()
		detach := context.AfterFunc(session, stop)
		defer detach()
		proxy.ServeHTTP(w, r.WithContext(requestCtx))
	})

	return mux
}

func (a *Agent) endpoint() string {
	return strings.TrimSuffix(a.cfg.PeerPrimaryURL, "/") + "/peer/control"
}
func (a *Agent) post(ctx context.Context, f peering.Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(), strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.PeerToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("primary rejected peer message (%d)", response.StatusCode)
	}
	return nil
}
func (a *Agent) controlLoop(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		attempt := time.Now()
		err := a.control(ctx)
		if a.connectedAt.After(attempt) {
			delay = time.Second
		}
		a.online.Store(false)
		if ctx.Err() != nil {
			return
		}
		a.logger.Info("Primary connection offline; reconnecting", zap.Duration("retry_in", delay))
		_ = err // disconnection is a normal peer state; token/URLs never reach logs
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 15*time.Second {
			delay *= 2
		}
	}
}
func (a *Agent) control(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.PeerToken)
	req.Header.Set("X-MCPProxy-Peer-Process", a.processID)
	response, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("primary rejected connection (%d)", response.StatusCode)
	}
	a.entryMu.Lock()
	a.entryContext = ctx
	a.entryMu.Unlock()
	a.online.Store(true)
	defer a.online.Store(false)
	a.connectedAt = time.Now()
	a.logger.Info("Connected to primary")
	hello, _ := json.Marshal(a.hello())
	if err = a.post(ctx, peering.Frame{Op: "hello", Data: hello}); err != nil {
		return err
	}
	a.flushCredentials()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if a.sendHello(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	var workers sync.WaitGroup
	defer func() {
		cancel()
		a.mu.Lock()
		for _, stop := range a.calls {
			stop()
		}
		a.mu.Unlock()
		workers.Wait()
	}()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), peering.MaxMessage)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var f peering.Frame
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f) != nil {
			return fmt.Errorf("invalid control frame")
		}
		if f.Op == "cancel" {
			a.mu.Lock()
			if stop := a.calls[f.ID]; stop != nil {
				stop()
			}
			a.mu.Unlock()
			continue
		}
		requestCtx, stop := context.WithCancel(ctx)
		a.mu.Lock()
		a.calls[f.ID] = stop
		a.mu.Unlock()
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer stop()
			reply := peering.Frame{ID: f.ID, Op: "result"}
			data, err := a.execute(requestCtx, f)
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.Data, _ = json.Marshal(data)
				if len(reply.Data) > peering.MaxMessage-4096 {
					reply.Data = nil
					reply.Error = "Peer result exceeds 16 MiB"
				}
			}
			_ = a.sendHello(ctx)
			_ = a.post(ctx, reply)
			a.mu.Lock()
			delete(a.calls, f.ID)
			a.mu.Unlock()
		}()
	}
	return scanner.Err()
}
func (a *Agent) serverLock(name string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locks[name] == nil {
		a.locks[name] = &sync.Mutex{}
	}
	return a.locks[name]
}
func (a *Agent) execute(ctx context.Context, f peering.Frame) (any, error) {
	switch f.Op {
	case "sync":
		a.opMu.Lock()
		defer a.opMu.Unlock()
		var desired peering.Sync
		if err := json.Unmarshal(f.Data, &desired); err != nil {
			return nil, err
		}
		if desired.Generation < a.syncGen {
			return nil, nil
		}
		a.syncGen = desired.Generation
		a.assigned = map[string]bool{}
		keep := map[string]bool{}
		for _, assignment := range desired.Servers {
			keep[assignment.Name] = true
			a.assigned[assignment.Name] = assignment.Enabled
		}
		a.mu.Lock()
		a.environment = desired.Environment
		removed := map[string]*managed.Client{}
		for name, instance := range a.servers {
			if !keep[name] && a.versions[name] <= desired.Generation {
				removed[name] = instance
				delete(a.servers, name)
				delete(a.settings, name)
				if token := a.tokens[name]; token != nil {
					token.Retire()
				}
				delete(a.tokens, name)
				delete(a.resolvers, name)
				delete(a.secrets, name)
			}
		}
		a.mu.Unlock()
		for name, instance := range removed {
			instance.Retire()
			_ = instance.Disconnect()
			a.refresh.OnTokenCleared(name)
		}
		for _, connection := range desired.Connections {
			if _, err := a.configure(ctx, connection); err != nil && !errors.Is(err, commitview.ErrStaleGeneration) {
				return nil, err
			}
		}
		return nil, nil

	case "oauth_settings":
		a.opMu.RLock()
		defer a.opMu.RUnlock()
		var desired peering.Connection
		if err := json.Unmarshal(f.Data, &desired); err != nil {
			return nil, err
		}
		if desired.Config == nil || desired.Config.Name != f.Server {
			return nil, fmt.Errorf("invalid server for OAuth discovery")
		}
		if _, assigned := a.assigned[f.Server]; desired.Generation <= a.syncGen && !assigned {
			return nil, commitview.ErrStaleGeneration
		}
		// Reading settings must not configure, reconnect or retire a running server.
		cfg := config.CopyServerConfig(desired.Config)
		cfg.HostID = nil
		resolver := secret.ExecutionResolver(desired.Secrets)
		var store storage.OAuthStore
		a.mu.Lock()
		if live := a.servers[f.Server]; live != nil && live.CredentialBinding() == desired.Binding {
			if token := a.tokens[f.Server]; token != nil {
				store = token
			}
		}
		a.mu.Unlock()
		probe, err := core.NewClient(f.Server, cfg, a.logger, nil, a.global, store, resolver)
		if err != nil {
			return nil, err
		}
		probe.SetCredentialBinding(desired.Binding)
		defer probe.Retire()
		values, warning := probe.OAuthDefaults(ctx)
		if a.url.Scheme == "https" {
			values.CallbackBaseURL = a.cfg.PeerPrimaryURL
			values.RedirectURI, _ = config.OAuthRemoteCallbackURL(a.cfg.PeerPrimaryURL, f.Server)
		}
		return &oauth.Settings{Automatic: values, DiscoveryError: warning}, nil

	case "connect":
		a.opMu.RLock()
		defer a.opMu.RUnlock()
		var desired peering.Connection
		if err := json.Unmarshal(f.Data, &desired); err != nil {
			return nil, err
		}
		if desired.Generation <= a.syncGen && !a.assigned[f.Server] {
			return nil, commitview.ErrStaleGeneration
		}
		instance, err := a.configure(ctx, desired)
		if err != nil {
			return nil, err
		}
		if !instance.IsConnected() {
			if err = instance.Connect(ctx); err != nil {
				return nil, err
			}
		}
		instance.OnPeerNotification(func(notification mcp.JSONRPCNotification) {
			data, _ := json.Marshal(notification)
			_ = a.post(context.Background(), peering.Frame{Op: "notification", Server: f.Server, Data: data})
		})
		if token := a.token(f.Server); token != nil {
			if record := token.Token(); record != nil && record.RefreshToken != "" {
				a.refresh.OnTokenSavedFor(f.Server, record.ExpiresAt, desired.Binding)
			}
		}
		// Artwork is fetched and validated on the execution host too. The
		// primary receives inline artwork and never probes a peer-local URL.
		info := *instance.GetServerInfo()
		info.ServerInfo.Icons = nil
		if icon := instance.GetServerIcon(); icon != "" {
			info.ServerInfo.Icons = []mcp.Icon{{Src: servericon.DataURI(icon), MIMEType: servericon.MIMEType(icon)}}
		}
		return &info, nil
	case "rpc", "notify", "disconnect":
		a.mu.Lock()
		instance := a.servers[f.Server]
		a.mu.Unlock()
		if instance == nil {
			return nil, fmt.Errorf("server is not running")
		}
		if f.Op == "disconnect" {
			lock := a.serverLock(f.Server)
			lock.Lock()
			defer lock.Unlock()
			return nil, instance.Disconnect()
		}
		if f.Op == "notify" {
			var notification mcp.JSONRPCNotification
			if err := json.Unmarshal(f.Data, &notification); err != nil {
				return nil, err
			}
			return nil, instance.PeerNotify(ctx, notification)
		}
		var payload peering.RPC
		if err := json.Unmarshal(f.Data, &payload); err != nil {
			return nil, err
		}
		request := payload.Request
		if request.Method == "tools/call" {
			ctx = core.WithPeerCallTimeout(ctx, payload.Timeout)
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			raw, err := json.Marshal(request.Params)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			result, err := instance.CallTool(ctx, params.Name, params.Arguments)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(result)
			return &sdktransport.JSONRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: data}, err
		}
		return instance.PeerRPC(ctx, request)
	default:
		return a.manage(ctx, f)
	}
}
func (a *Agent) closeServers() {
	a.mu.Lock()
	clients := make([]*managed.Client, 0, len(a.servers))
	for _, client := range a.servers {
		clients = append(clients, client)
	}
	a.mu.Unlock()
	for _, client := range clients {
		client.Retire()
		_ = client.Disconnect()
	}
}
