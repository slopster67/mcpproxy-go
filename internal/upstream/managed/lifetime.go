package managed

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
)

const demandProbeInterval = time.Hour

type demandConnections struct {
	mu           sync.RWMutex
	idle         atomic.Bool
	reportedIdle atomic.Bool
	usesOAuth    atomic.Bool
	sessions     map[string]*core.Client
	shared       bool
	tools        []*config.ToolMetadata
	probedAt     time.Time
}

type lifetimeSessionKey struct{}

type expectedToolKey struct{}

type expectedTool struct {
	hash        string
	annotations *config.ToolAnnotations
}

func WithExpectedTool(ctx context.Context, hash string, annotations *config.ToolAnnotations) context.Context {
	return context.WithValue(ctx, expectedToolKey{}, expectedTool{hash, annotations})
}

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
	d.idle.Store(!d.probedAt.IsZero() && !d.shared && len(d.sessions) == 0)
}

func (mc *Client) demandProbeDue() bool {
	d := &mc.demand
	d.mu.RLock()
	defer d.mu.RUnlock()
	return time.Since(d.probedAt) >= demandProbeInterval
}

func (mc *Client) listDemandTools(ctx context.Context) ([]*config.ToolMetadata, error) {
	d := &mc.demand
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()

	if !mc.IsConnected() || mc.Retired() {
		return nil, ErrConnectionGenerationChanged
	}
	if time.Since(d.probedAt) < demandProbeInterval {
		return d.tools, nil
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
	d.tools = tools
	d.usesOAuth.Store(mc.coreClient.AuthStrategy() == core.AuthStrategyOAuth)
	d.probedAt = time.Now()
	return tools, nil
}

// SyncLifetimeSessions keeps only the connections with an eligible live
// downstream session.
func (mc *Client) SyncLifetimeSessions(ctx context.Context, sessions map[string]mcp.Implementation) {
	cfg := mc.GetConfig()
	if !cfg.OnDemand() {
		return
	}
	d := &mc.demand
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()

	if !mc.IsConnected() || mc.Retired() || mc.IsUserLoggedOut() {
		return
	}
	for id, connection := range d.sessions {
		if _, keep := sessions[id]; !keep {
			delete(d.sessions, id)
			_ = connection.Disconnect()
		}
	}
	if len(sessions) == 0 && d.shared {
		d.shared = false
		_ = mc.coreClient.Disconnect()
	}
	for id, info := range sessions {
		if ctx.Err() != nil {
			return
		}
		_ = mc.openSessionConnection(ctx, id, info)
		if cfg.ConnectionLifetime == config.KeepRunningShared {
			return
		}
	}
}

// Caller holds demand.mu. HTTP launchers own one listening port across sessions.
func (mc *Client) openSessionConnection(ctx context.Context, id string, info mcp.Implementation) error {
	if mc.activeDemandConnection(id) != nil {
		return nil
	}
	d := &mc.demand
	cfg := mc.GetConfig()
	if cfg.ConnectionLifetime == config.KeepRunningShared || cfg.LaunchesHTTP() {
		if !mc.coreClient.IsConnected() {
			if err := mc.coreClient.Connect(ctx); err != nil {
				return err
			}
		}
		d.shared = true
		if cfg.ConnectionLifetime == config.KeepRunningShared {
			return nil
		}
	}
	connection, err := mc.NewSessionConnection(uuid.NewString(), info)
	if err != nil {
		return err
	}
	if err := connection.Connect(ctx); err != nil {
		_ = connection.Disconnect()
		return err
	}
	if d.sessions == nil {
		d.sessions = make(map[string]*core.Client)
	}
	d.sessions[id] = connection
	return nil
}

func (mc *Client) closeDemandSessionsLocked() {
	d := &mc.demand
	defer d.updateIdle()
	for _, connection := range d.sessions {
		_ = connection.Disconnect()
	}
	d.sessions = nil
	d.shared = false
	d.tools = nil
	d.usesOAuth.Store(false)
	d.probedAt = time.Time{}
}

func (mc *Client) callDemandTool(ctx context.Context, name string, args map[string]interface{}, epoch *int64) (*mcp.CallToolResult, error) {
	id, _ := ctx.Value(lifetimeSessionKey{}).(string)
	if id == "" {
		return nil, fmt.Errorf("an active MCP session is required")
	}
	expected, certified := ctx.Value(expectedToolKey{}).(expectedTool)
	if !certified || expected.hash == "" {
		return nil, ErrConnectionGenerationChanged
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

	tools, err := connection.ListTools(ctx)
	if err != nil {
		d.mu.RUnlock()
		if ctx.Err() == nil {
			mc.dropDemandConnection(id, connection)
		}
		return nil, err
	}
	// A shared core can reconnect in place. A dedicated client is never
	// reused, so its tool call need not delay another session's disconnect.
	if connection == mc.coreClient {
		defer d.mu.RUnlock()
	} else {
		d.mu.RUnlock()
	}
	for _, actual := range tools {
		if config.RawToolName(actual) == name && actual.Hash == expected.hash &&
			reflect.DeepEqual(actual.Annotations, expected.annotations) {
			return connection.CallTool(ctx, name, args)
		}
	}
	return nil, ErrConnectionGenerationChanged
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

func (mc *Client) dropDemandConnection(id string, connection *core.Client) {
	d := &mc.demand
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()
	if d.sessions[id] == connection {
		delete(d.sessions, id)
		_ = connection.Disconnect()
	} else if d.shared && connection == mc.coreClient {
		d.shared = false
		_ = connection.Disconnect()
	}
}

func (mc *Client) probeDemandConnection() {
	if !mc.IsConnected() || mc.IsUserLoggedOut() || !mc.demandProbeDue() {
		return
	}
	mc.mu.RLock()
	discover := mc.toolDiscoveryCallback
	mc.mu.RUnlock()
	if discover != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = discover(ctx, mc.GetConfig().Name)
	}
}
