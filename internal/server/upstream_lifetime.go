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

func (p *MCPProxyServer) signalUpstreamLifetime() {
	select {
	case p.lifetimeWake <- struct{}{}:
	default:
	}
}

func (p *MCPProxyServer) trackUpstreamRequest(ctx context.Context, next http.Handler, w http.ResponseWriter, r *http.Request, credential *auth.ClientCredential) {
	ctx = context.WithValue(ctx, lifetimeCredentialKey{}, credential.TokenHash)
	id := r.Header.Get(mcpserver.HeaderKeySessionID)
	store := p.sessionStore
	store.mu.Lock()
	session := store.sessions[id]
	if session == nil {
		store.mu.Unlock()
		next.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	if session.ClientID != credential.ClientID || session.credentialHash == "" ||
		(session.inFlight == 0 && time.Since(session.lastActivity) >= upstreamSessionIdle) {
		store.mu.Unlock()
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	if session.credentialHash != credential.TokenHash {
		session.credentialHash = credential.TokenHash
		p.signalUpstreamLifetime()
	}
	ctx = managed.WithLifetimeSession(ctx, id)
	if r.Method == http.MethodPost {
		session.inFlight++
		defer func() {
			store.mu.Lock()
			session.inFlight--
			session.lastActivity = time.Now()
			store.mu.Unlock()
		}()
	}
	store.mu.Unlock()
	if r.Method == http.MethodDelete {
		response := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(response, r.WithContext(ctx))
		if response.statusCode >= 200 && response.statusCode < 300 {
			store.mu.Lock()
			session.credentialHash = ""
			store.mu.Unlock()
			p.signalUpstreamLifetime()
		}
		return
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (p *MCPProxyServer) runUpstreamLifetime(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.lifetimeWake:
		case <-ticker.C:
		}
		p.syncUpstreamLifetime(ctx)
	}
}

func (p *MCPProxyServer) syncUpstreamLifetime(ctx context.Context) {
	p.lifetimeMu.Lock()
	defer p.lifetimeMu.Unlock()
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
	store := p.sessionStore
	store.mu.RLock()
	var sessions []SessionInfo
	for _, session := range store.sessions {
		if session.credentialHash != "" && (session.inFlight > 0 || time.Since(session.lastActivity) < upstreamSessionIdle) {
			sessions = append(sessions, *session)
		}
	}
	store.mu.RUnlock()
	for _, session := range sessions {
		record, err := p.storage.GetClientCredentialByHash(session.credentialHash)
		if err != nil || record == nil {
			continue
		}
		caller := record.AuthContext()
		caller.ServerAvailable = func(name string) bool { return p.hostAllows(caller, name) }
		callerCtx := auth.WithAuthContext(ctx, caller)
		scope := p.resolveSessionProfile(callerCtx, index, session.SessionID).Scope
		for _, name := range servers {
			if !directScopeAllows(caller, scope, name) {
				continue
			}
			if desired[name] == nil {
				desired[name] = make(map[string]mcp.Implementation)
			}
			desired[name][session.SessionID] = mcp.Implementation{Name: session.ClientName, Title: record.DisplayName, Version: session.ClientVersion}
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
			client.SyncLifetimeSessions(connectCtx, desired[name], p.mainServer.runtime.RefreshSessionCatalog)
			if client.IdleChanged() {
				p.mainServer.runtime.Changes().Invalidate(runtime.EntityServers, name)
			}
		})
	}
	workers.Wait()
}
