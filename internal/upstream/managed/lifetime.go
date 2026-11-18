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
	discovered   bool
	sessions     map[string]*core.Client
	shared       bool
	tools        []*config.ToolMetadata
	probedAt     time.Time
}

type lifetimeSessionKey struct{}

type LifetimeSession struct {
	ID         string
	ClientInfo mcp.Implementation
}

type expectedToolKey struct{}

type expectedTool struct {
	hash        string
	annotations *config.ToolAnnotations
}

func WithExpectedTool(ctx context.Context, hash string, annotations *config.ToolAnnotations) context.Context {
	return context.WithValue(ctx, expectedToolKey{}, expectedTool{hash, annotations})
}

func WithLifetimeSession(ctx context.Context, session LifetimeSession) context.Context {
	return context.WithValue(ctx, lifetimeSessionKey{}, session)
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
	d.idle.Store(d.discovered && !d.shared && len(d.sessions) == 0)
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
	d.discovered = true
	d.usesOAuth.Store(mc.coreClient.AuthStrategy() == core.AuthStrategyOAuth)
	d.probedAt = time.Now()
	return tools, nil
}

// SyncLifetimeSessions keeps only the connections with an eligible live
// downstream session. A call holds a read lock until its upstream reply.
func (mc *Client) SyncLifetimeSessions(ctx context.Context, sessions map[string]mcp.Implementation) {
	cfg := mc.GetConfig()
	if !cfg.OnDemand() || !mc.IsConnected() || mc.Retired() {
		return
	}
	d := &mc.demand
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()

	if !mc.IsConnected() || mc.Retired() || mc.IsUserLoggedOut() {
		return
	}
	perSession := cfg.ConnectionLifetime == config.KeepRunningPerSession
	for id, connection := range d.sessions {
		if _, keep := sessions[id]; !keep {
			delete(d.sessions, id)
			_ = connection.Disconnect()
		}
	}
	if !perSession || cfg.LaunchesHTTP() {
		if len(sessions) != 0 {
			if err := mc.openSharedDemandConnection(ctx); err != nil {
				return
			}
		} else if d.shared {
			d.shared = false
			_ = mc.coreClient.Disconnect()
		}
	}
	if !perSession {
		return
	}
	for id, info := range sessions {
		if d.sessions[id] == nil && ctx.Err() == nil {
			_, _ = mc.openDemandSession(ctx, LifetimeSession{id, info})
		}
	}
}

func (mc *Client) openDemandSession(ctx context.Context, session LifetimeSession) (*core.Client, error) {
	d := &mc.demand
	if connection := d.sessions[session.ID]; connection != nil {
		return connection, nil
	}
	connection, err := mc.NewSessionConnection(uuid.NewString(), session.ClientInfo)
	if err != nil {
		return nil, err
	}
	if err := connection.Connect(ctx); err != nil {
		_ = connection.Disconnect()
		return nil, err
	}
	if d.sessions == nil {
		d.sessions = make(map[string]*core.Client)
	}
	d.sessions[session.ID] = connection
	return connection, nil
}

func (mc *Client) closeDemandSessions() {
	d := &mc.demand
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()
	for _, connection := range d.sessions {
		_ = connection.Disconnect()
	}
	d.sessions = nil
	d.shared = false
	d.tools = nil
	d.discovered = false
	d.usesOAuth.Store(false)
	d.probedAt = time.Time{}
}

func (mc *Client) callDemandTool(ctx context.Context, name string, args map[string]interface{}, epoch *int64) (*mcp.CallToolResult, error) {
	session, ok := ctx.Value(lifetimeSessionKey{}).(LifetimeSession)
	if !ok || session.ID == "" {
		return nil, fmt.Errorf("an active MCP session is required")
	}
	expected, certified := ctx.Value(expectedToolKey{}).(expectedTool)
	if !certified || expected.hash == "" {
		return nil, ErrConnectionGenerationChanged
	}
	connection, err := mc.demandConnection(ctx, session)
	if err != nil {
		return nil, err
	}
	d := &mc.demand
	d.mu.RLock()
	if mc.Retired() || !mc.IsConnected() || (epoch != nil && !mc.generationIs(*epoch)) ||
		mc.activeDemandConnection(session.ID) != connection {
		d.mu.RUnlock()
		return nil, ErrConnectionGenerationChanged
	}

	// The independently initialized transport may expose a different tool set.
	// Validate its definition against the catalog identity authorized for this call.
	tools, err := connection.ListTools(ctx)
	if err != nil {
		d.mu.RUnlock()
		if ctx.Err() == nil {
			mc.dropDemandConnection(session.ID, connection)
		}
		return nil, err
	}
	defer d.mu.RUnlock()
	for _, actual := range tools {
		if config.RawToolName(actual) == name && actual.Hash == expected.hash &&
			reflect.DeepEqual(actual.Annotations, expected.annotations) {
			return connection.CallTool(ctx, name, args)
		}
	}
	return nil, ErrConnectionGenerationChanged
}

func (mc *Client) demandConnection(ctx context.Context, session LifetimeSession) (*core.Client, error) {
	d := &mc.demand
	d.mu.RLock()
	connection := mc.activeDemandConnection(session.ID)
	d.mu.RUnlock()
	if connection != nil {
		return connection, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.updateIdle()
	if mc.Retired() || !mc.IsConnected() || mc.IsUserLoggedOut() {
		return nil, ErrInstanceRetired
	}
	cfg := mc.GetConfig()
	if cfg.ConnectionLifetime != config.KeepRunningPerSession || cfg.LaunchesHTTP() {
		if err := mc.openSharedDemandConnection(ctx); err != nil {
			return nil, err
		}
	}
	if cfg.ConnectionLifetime == config.KeepRunningPerSession {
		return mc.openDemandSession(ctx, session)
	}
	return mc.coreClient, nil
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

func (mc *Client) openSharedDemandConnection(ctx context.Context) error {
	if !mc.coreClient.IsConnected() {
		if err := mc.coreClient.Connect(ctx); err != nil {
			return err
		}
	}
	mc.demand.shared = true
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
