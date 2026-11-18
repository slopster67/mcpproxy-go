package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

const upstreamSessionIdle = 30 * time.Minute

type lifetimeCredentialKey struct{}

type upstreamClientSession struct {
	session        mcpserver.ClientSession
	credentialHash string
	lastActivity   time.Time
	inFlight       int
}

type upstreamLifetime struct {
	mu       sync.Mutex
	sessions map[string]upstreamClientSession
	wake     chan struct{}
}

func (p *MCPProxyServer) touchUpstreamSession(ctx context.Context, session mcpserver.ClientSession) {
	hash, _ := ctx.Value(lifetimeCredentialKey{}).(string)
	caller := auth.AuthContextFromContext(ctx)
	info := p.sessionStore.GetSession(session.SessionID())
	if hash == "" || caller == nil || info == nil || info.ClientID != caller.ClientID {
		return
	}
	l := &p.upstreamLifetime
	l.mu.Lock()
	entry := l.sessions[session.SessionID()]
	entry.session, entry.credentialHash, entry.lastActivity = session, hash, time.Now()
	l.sessions[session.SessionID()] = entry
	l.mu.Unlock()
	l.signal()
}

func (l *upstreamLifetime) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (p *MCPProxyServer) trackUpstreamRequest(ctx context.Context, next http.Handler, w http.ResponseWriter, r *http.Request, credential *auth.ClientCredential) {
	id := r.Header.Get(mcpserver.HeaderKeySessionID)
	info := p.sessionStore.GetSession(id)
	if info != nil && info.ClientID == credential.ClientID {
		ctx = managed.WithLifetimeSession(ctx, managed.LifetimeSession{ID: id, ClientInfo: mcp.Implementation{
			Name: info.ClientName, Version: info.ClientVersion, Title: credential.DisplayName,
		}})
		l := &p.upstreamLifetime
		switch r.Method {
		case http.MethodDelete:
			response := &sessionDeleteResponse{ResponseWriter: w}
			next.ServeHTTP(response, r.WithContext(ctx))
			if response.status >= 200 && response.status < 300 {
				l.mu.Lock()
				delete(l.sessions, id)
				l.mu.Unlock()
				l.signal()
			}
			return
		case http.MethodPost:
			l.mu.Lock()
			entry, ok := l.sessions[id]
			if ok {
				entry.inFlight++
				l.sessions[id] = entry
			}
			l.mu.Unlock()
			defer func() {
				l.mu.Lock()
				if entry, exists := l.sessions[id]; exists {
					entry.inFlight = max(0, entry.inFlight-1)
					entry.lastActivity = time.Now()
					l.sessions[id] = entry
				}
				l.mu.Unlock()
				l.signal()
			}()
		}
	}
	next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, lifetimeCredentialKey{}, credential.TokenHash)))
}

type sessionDeleteResponse struct {
	http.ResponseWriter
	status int
}

func (w *sessionDeleteResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (p *MCPProxyServer) runUpstreamLifetime(ctx context.Context) {
	l := &p.upstreamLifetime
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-ticker.C:
		}
		p.syncUpstreamLifetime(ctx)
	}
}

func (p *MCPProxyServer) syncUpstreamLifetime(ctx context.Context) {
	l := &p.upstreamLifetime
	l.mu.Lock()
	sessions := make([]upstreamClientSession, 0, len(l.sessions))
	for id, session := range l.sessions {
		if session.inFlight == 0 && time.Since(session.lastActivity) >= upstreamSessionIdle {
			delete(l.sessions, id)
		} else {
			sessions = append(sessions, session)
		}
	}
	l.mu.Unlock()

	cfg := p.currentConfig()
	var servers []string
	for _, server := range cfg.Servers {
		if server != nil && server.Enabled && server.OnDemand() {
			servers = append(servers, server.Name)
		}
	}
	if len(servers) == 0 {
		return
	}
	desired := make(map[string]map[string]mcp.Implementation)
	index := p.profileIndexFor(cfg)
	for _, session := range sessions {
		record, err := p.storage.GetClientCredentialByHash(session.credentialHash)
		if err != nil || record == nil {
			continue
		}
		id := session.session.SessionID()
		info := p.sessionStore.GetSession(id)
		if info == nil {
			continue
		}
		caller := record.AuthContext()
		caller.ServerAvailable = func(name string) bool { return p.hostAllows(caller, name) }
		callerCtx := p.server.WithContext(auth.WithAuthContext(ctx, caller), session.session)
		scope := p.resolveProfileV3(callerCtx, index).Scope
		for _, name := range servers {
			if !directScopeAllows(caller, scope, name) {
				continue
			}
			if desired[name] == nil {
				desired[name] = make(map[string]mcp.Implementation)
			}
			desired[name][id] = mcp.Implementation{Name: info.ClientName, Title: record.DisplayName, Version: info.ClientVersion}
		}
	}

	var workers sync.WaitGroup
	for _, name := range servers {
		client, ok := p.upstreamManager.GetClient(name)
		if !ok {
			continue
		}
		workers.Go(func() {
			connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			client.SyncLifetimeSessions(connectCtx, desired[name])
			if client.IdleChanged() {
				p.mainServer.runtime.Changes().Invalidate(runtime.EntityServers, name)
			}
		})
	}
	workers.Wait()
}
