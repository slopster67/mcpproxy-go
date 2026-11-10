package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/secret"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

func (r *Runtime) Peers() *peering.Hub { return r.peers }
func (r *Runtime) initializePeers() {
	r.peers = peering.NewHub(r.storageManager.GetDB())
	r.upstreamManager.SetPeerExecutor(r.peers)
	r.peers.PrepareConnection = r.preparePeerConnection
	r.peers.SaveCredentials = func(ctx context.Context, peer, writer string, frame peering.Frame) (any, error) {
		var update storage.OAuthUpdate
		if err := json.Unmarshal(frame.Data, &update); err != nil {
			return nil, err
		}
		revision, err := r.storageManager.GetDatabase().SavePeerOAuth(peer, frame.Server, writer, update)
		return map[string]uint64{"revision": revision}, err
	}
	r.peers.ServerChanged = func(peer, name string) {
		if r.ServerHost(name) != peer {
			return
		}
		r.upstreamManager.InvalidateConnection(name)
	}
	r.peers.Changed = func(id string, online bool) {
		cfg := r.Config()
		if cfg == nil {
			return
		}
		go func() {
			if online {
				if err := r.syncPeer(id); err != nil {
					return
				}
			}

			for _, server := range cfg.Servers {
				if server == nil || server.ExecutionHost() != id {
					continue
				}
				if client, ok := r.upstreamManager.GetClient(server.Name); ok {
					if online && r.peers.Online(id) && server.Enabled {
						if state, ok := r.peers.ServerState(id, server.Name); ok && state.Authenticated && !state.UserLoggedOut {
							client.SetUserLoggedOut(false)
						}
						_ = r.upstreamManager.RetryConnection(server.Name)
					} else if !online && !r.peers.Online(id) {
						go client.Disconnect()
					}
				}
			}
		}()
		r.changes.Dirty(EntityPeers, id)
		r.changes.Dirty(EntityServers, r.serversOnHost(id)...)
		r.changes.Dirty(EntityClients, r.clientsOnHost(id)...)
	}
}

// clientsOnHost names the saved clients that connect through peer id (their
// presence follows the peer's).
func (r *Runtime) clientsOnHost(id string) []string {
	if r.clientsService == nil {
		return nil
	}
	records, err := r.clientsService.Records()
	if err != nil {
		return nil
	}
	var ids []string
	for i := range records {
		if records[i].HostID == id {
			ids = append(ids, records[i].ClientID)
		}
	}
	return ids
}

// serversOnHost names the configured servers executing on peer id.
func (r *Runtime) serversOnHost(id string) []string {
	cfg := r.Config()
	if cfg == nil {
		return nil
	}
	var names []string
	for _, server := range cfg.Servers {
		if server != nil && server.ExecutionHost() == id {
			names = append(names, server.Name)
		}
	}
	return names
}

// projectServerHost adds location without treating a sleeping laptop as a fault.
func (r *Runtime) projectServerHost(srv *contracts.Server) {
	host := srv.ExecutionHost()
	if host == "" {
		srv.HostName = "Primary"
		srv.HostOnline = true
		return
	}
	online := r.peers != nil && r.peers.Online(host)
	name := "Unknown peer"
	if r.peers != nil {
		name = r.peers.Name(host)
	}
	srv.HostName = name
	srv.HostOnline = online
	if !online {
		srv.Connected = false
		srv.Connecting = false
		srv.Status = "peer_offline"
		srv.LastError = ""
		srv.Diagnostic = nil
		srv.ErrorCode = ""
		srv.RetryStopped, srv.RetryStoppedCode, srv.RetryStoppedReason = false, "", ""
		srv.Health = &contracts.HealthStatus{Level: "healthy", AdminState: "enabled", Summary: name + " is offline", Status: "peer_offline", Usable: false}
		return
	}
	state, ok := r.peers.ServerState(host, srv.Name)
	if !ok {
		return
	}
	srv.UserLoggedOut = state.UserLoggedOut
	srv.Authenticated = state.Authenticated
	if state.ExpiresAt != nil {
		expires := *state.ExpiresAt
		srv.TokenExpiresAt = &expires
	}
	if !state.Connected && state.LastError != "" {
		srv.Connected = false
		srv.LastError = state.LastError
	}
	switch {
	case state.Authenticated:
		srv.OAuthStatus = "authenticated"
	case state.AuthRequired:
		srv.OAuthStatus = "error"
	default:
		srv.OAuthStatus = "none"
	}
	if state.Authenticated || state.AuthRequired {
		srv.OAuthActive = true
	}
	if state.UserLoggedOut || state.AuthRequired {
		srv.Health = &contracts.HealthStatus{Level: "unhealthy", AdminState: "enabled", Summary: "Sign-in required", Status: "sign_in_required", Action: "login", Actions: []string{"login"}, Usable: false}
	} else if state.Connected && state.Authenticated {
		srv.Health = &contracts.HealthStatus{Level: "healthy", AdminState: "enabled", Summary: "Connected", Status: "ready", Usable: true}
	}
}

func (r *Runtime) ServerHost(name string) string {
	if cfg := r.Config(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.Name == name {
				return server.ExecutionHost()
			}
		}
	}
	return ""
}

// serverEnabled reports whether name is configured and enabled.
func (r *Runtime) serverEnabled(name string) bool {
	if cfg := r.Config(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.Name == name {
				return server.Enabled
			}
		}
	}
	return false
}

// oauthAllowed refuses a sign-in for a server whose OAuth mode is "off".
func (r *Runtime) oauthAllowed(name string) error {
	if cfg := r.Config(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.Name == name && server.OAuthMode() == config.OAuthModeOff {
				return fmt.Errorf("OAuth is off for %s", name)
			}
		}
	}
	return nil
}

func (r *Runtime) peerOAuthLogin(name string) error {
	_, err := r.TriggerOAuthLoginQuick(name)
	return err
}

func (r *Runtime) syncPeer(id string) error {
	snapshot := r.ConfigSnapshot()
	if snapshot == nil || snapshot.Config == nil {
		return nil
	}
	desired := peering.Sync{Generation: snapshot.Version, Servers: []peering.Assignment{}}
	environment := map[string]bool{}
	for _, server := range snapshot.Config.Servers {
		if server == nil || server.ExecutionHost() != id {
			continue
		}
		stamp, _ := snapshot.ServerGeneration(server.Name)
		desired.Servers = append(desired.Servers, peering.Assignment{Name: server.Name, Enabled: server.Enabled, Binding: stamp.Binding(), Generation: stamp.Gen})
		connection := peering.Connection{Config: config.CopyServerConfig(server), Generation: stamp.Gen, Binding: stamp.Binding()}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := r.preparePeerConnection(ctx, id, &connection)
		cancel()
		if err != nil {
			return err
		}
		desired.Connections = append(desired.Connections, connection)
		for _, ref := range r.secretResolver.References(server) {
			if ref.Type == "env" {
				environment[ref.Name] = true
			}
		}

	}
	for name := range environment {
		desired.Environment = append(desired.Environment, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.peers.Call(ctx, id, "sync", "", desired, nil)
}
func (r *Runtime) syncOnlinePeers() {
	if r.peers == nil {
		return
	}
	peers, err := r.peers.List()
	if err != nil {
		return
	}
	for _, peer := range peers {
		if peer.Online {
			go func(id string) { _ = r.syncPeer(id) }(peer.ID)
		}
	}
}

func (r *Runtime) preparePeerConnection(ctx context.Context, peer string, desired *peering.Connection) error {
	desired.Secrets = r.resolveServerSecrets(ctx, desired.Config)
	snapshot, err := r.storageManager.GetDatabase().PeerOAuthSnapshot(peer, desired.Config.Name, r.peers.ProcessID(peer), desired.Binding)
	desired.OAuth = snapshot
	return err
}

func (r *Runtime) resolveServerSecrets(ctx context.Context, server *config.ServerConfig) map[string]string {
	values := map[string]string{}
	for _, ref := range r.secretResolver.References(server) {
		if ref.Type != "secret" {
			continue
		}
		if value, err := r.secretResolver.Resolve(ctx, secret.Ref{Type: "secret", Name: ref.Name}); err == nil {
			values[ref.Name] = value
		}
	}
	return values
}

// The primary never rotates grants owned by peers or kept by servers whose
// OAuth is off, including on startup.
type primaryRefreshStore struct {
	db     *storage.Database
	config func() *config.Config
}

func (s primaryRefreshStore) ListOAuthTokens() ([]*storage.OAuthTokenRecord, error) {
	records, err := s.db.ListOAuthTokens()
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	if cfg := s.config(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.ExecutionHost() == "" && server.OAuthMode() != config.OAuthModeOff {
				local[server.Name] = true
			}
		}
	}
	result := []*storage.OAuthTokenRecord{}
	for _, record := range records {
		if local[record.DisplayName] {
			result = append(result, record)
		}
	}
	return result, nil
}
func (s primaryRefreshStore) GetOAuthToken(key string) (*storage.OAuthTokenRecord, error) {
	return s.db.GetOAuthToken(key)
}
