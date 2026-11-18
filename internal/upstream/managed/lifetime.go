package managed

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
)

type demandConnections struct {
	mu           sync.RWMutex
	idle         atomic.Bool
	reportedIdle atomic.Bool
	usesOAuth    atomic.Bool
	sessions     map[string]*core.Client
	shared       bool
	verified     bool
}

type lifetimeSessionKey struct{}

func WithLifetimeSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, lifetimeSessionKey{}, id)
}

// Idle is an intentionally closed transport with a verified catalog.
func (mc *Client) Idle() bool {
	return mc.GetConfig().OnDemand() && mc.IsConnected() && mc.demand.idle.Load()
}

func (mc *Client) IdleChanged() bool {
	idle := mc.Idle()
	return mc.demand.reportedIdle.Swap(idle) != idle
}

func (d *demandConnections) updateIdle() {
	d.idle.Store(d.verified && !d.shared && len(d.sessions) == 0)
}

func (mc *Client) listDemandTools(ctx context.Context) ([]*config.ToolMetadata, error) {
	d := &mc.demand
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()

	if !mc.IsConnected() || mc.Retired() {
		return nil, ErrConnectionGenerationChanged
	}
	if !mc.coreClient.IsConnected() {
		if err := mc.coreClient.Connect(ctx); err != nil {
			return nil, err
		}
	}
	if !d.shared {
		defer mc.coreClient.Disconnect()
	}
	tools, err := mc.coreClient.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	d.usesOAuth.Store(mc.coreClient.AuthStrategy() == core.AuthStrategyOAuth)
	d.verified = true
	return tools, nil
}

// SyncLifetimeSessions keeps only the connections with an eligible live
// downstream session.
func (mc *Client) SyncLifetimeSessions(ctx context.Context, sessions map[string]mcp.Implementation, refresh func(context.Context, *Client, string)) {
	d := &mc.demand
	d.mu.Lock()
	opened := mc.syncLifetimeConnections(ctx, sessions, refresh)
	d.updateIdle()
	d.mu.Unlock()
	for _, id := range opened {
		refresh(ctx, mc, id)
	}
}

func (mc *Client) syncLifetimeConnections(ctx context.Context, sessions map[string]mcp.Implementation, refresh func(context.Context, *Client, string)) []string {
	d := &mc.demand
	cfg := mc.GetConfig()
	if !cfg.OnDemand() || !mc.IsConnected() || mc.Retired() || mc.IsUserLoggedOut() {
		return nil
	}
	for id, connection := range d.sessions {
		if _, keep := sessions[id]; !keep {
			delete(d.sessions, id)
			_ = connection.Disconnect()
		}
	}
	if len(sessions) == 0 {
		if d.shared {
			d.shared = false
			_ = mc.coreClient.Disconnect()
		}
		return nil
	}

	var opened []string
	for id, info := range sessions {
		if ctx.Err() != nil {
			break
		}
		if mc.activeDemandConnection(id) != nil {
			continue
		}
		if !d.shared && (cfg.ConnectionLifetime == config.KeepRunningShared || cfg.LaunchesHTTP()) {
			if !mc.coreClient.IsConnected() && mc.coreClient.Connect(ctx) != nil {
				break
			}
			d.shared = true
			if cfg.ConnectionLifetime == config.KeepRunningShared {
				return []string{id}
			}
		}
		client, err := mc.NewSessionConnection(uuid.NewString(), info)
		if err != nil {
			continue
		}
		client.SetOnToolsChangedCallback(func(string) {
			go func() {
				ctx, cancel := context.WithTimeout(client.Gate().Context(), 30*time.Second)
				defer cancel()
				refresh(ctx, mc, id)
			}()
		})
		if err := client.Connect(ctx); err != nil {
			_ = client.Disconnect()
			continue
		}
		if d.sessions == nil {
			d.sessions = make(map[string]*core.Client)
		}
		d.sessions[id] = client
		opened = append(opened, id)
	}
	return opened
}

// ListSessionTools captures a discovery from the session for the server-wide catalog.
func (mc *Client) ListSessionTools(ctx context.Context, id string) ([]*config.ToolMetadata, InventoryCapture, error) {
	mc.demand.mu.RLock()
	defer mc.demand.mu.RUnlock()
	connection := mc.activeDemandConnection(id)
	if connection == nil || mc.Retired() || !mc.IsConnected() {
		return nil, InventoryCapture{}, ErrConnectionGenerationChanged
	}
	capture := mc.newCapture()
	tools, err := connection.ListTools(ctx)
	return tools, capture, err
}

func (mc *Client) closeDemandSessionsLocked() {
	d := &mc.demand
	defer d.updateIdle()
	for _, connection := range d.sessions {
		_ = connection.Disconnect()
	}
	d.sessions = nil
	d.shared = false
	d.usesOAuth.Store(false)
	d.verified = false
}

func (mc *Client) callDemandTool(ctx context.Context, name string, args map[string]interface{}, epoch *int64) (*mcp.CallToolResult, error) {
	id, _ := ctx.Value(lifetimeSessionKey{}).(string)
	if id == "" {
		return nil, fmt.Errorf("an active MCP session is required")
	}
	d := &mc.demand
	d.mu.RLock()
	connection := mc.activeDemandConnection(id)
	if connection == nil {
		d.mu.RUnlock()
		return nil, fmt.Errorf("upstream connection unavailable for this MCP session")
	}
	if mc.Retired() || !mc.IsConnected() || (epoch != nil && !mc.generationIs(*epoch)) {
		d.mu.RUnlock()
		return nil, ErrConnectionGenerationChanged
	}

	// A shared core can reconnect in place. A dedicated client is never
	// reused, so its tool call need not delay another session's disconnect.
	if connection == mc.coreClient {
		defer d.mu.RUnlock()
	} else {
		d.mu.RUnlock()
	}
	return connection.CallTool(ctx, name, args)
}

func (mc *Client) activeDemandConnection(id string) *core.Client {
	if mc.GetConfig().ConnectionLifetime == config.KeepRunningPerSession {
		return mc.demand.sessions[id]
	}
	if mc.demand.shared {
		return mc.coreClient
	}
	return nil
}
