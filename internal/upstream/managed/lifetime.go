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
	sessions     map[string]*demandSession
	shared       *demandSession
	tools        []*config.ToolMetadata
	probedAt     time.Time
}

// A session's client identity can affect the tools its upstream advertises.
type demandSession struct {
	client *core.Client
	tools  []*config.ToolMetadata
}

func (s *demandSession) discover(ctx context.Context) error {
	tools, err := s.client.ListTools(ctx)
	s.tools = tools
	return err
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
	d.idle.Store(!d.probedAt.IsZero() && d.shared == nil && len(d.sessions) == 0)
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
	probe := d.shared
	if probe == nil {
		var err error
		probe, err = connectAndDiscover(ctx, mc.coreClient)
		if err != nil {
			return nil, err
		}
		defer mc.coreClient.Disconnect()
	} else if err := probe.discover(ctx); err != nil {
		return nil, err
	}
	d.tools = probe.tools
	for id, session := range d.sessions {
		if err := session.discover(ctx); err != nil {
			delete(d.sessions, id)
			_ = session.client.Disconnect()
		}
	}
	d.usesOAuth.Store(mc.coreClient.AuthStrategy() == core.AuthStrategyOAuth)
	d.probedAt = time.Now()
	return d.tools, nil
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
			_ = connection.client.Disconnect()
		}
	}
	if len(sessions) == 0 && d.shared != nil {
		d.shared = nil
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
	if d.shared == nil && (cfg.ConnectionLifetime == config.KeepRunningShared || cfg.LaunchesHTTP()) {
		session, err := connectAndDiscover(ctx, mc.coreClient)
		if err != nil {
			return err
		}
		d.shared = session
		if cfg.ConnectionLifetime == config.KeepRunningShared {
			return nil
		}
	}
	connection, err := mc.NewSessionConnection(uuid.NewString(), info)
	if err != nil {
		return err
	}
	session, err := connectAndDiscover(ctx, connection)
	if err != nil {
		return err
	}
	if d.sessions == nil {
		d.sessions = make(map[string]*demandSession)
	}
	d.sessions[id] = session
	return nil
}

func connectAndDiscover(ctx context.Context, client *core.Client) (*demandSession, error) {
	if !client.IsConnected() {
		if err := client.Connect(ctx); err != nil {
			_ = client.Disconnect()
			return nil, err
		}
	}
	session := &demandSession{client: client}
	if err := session.discover(ctx); err != nil {
		_ = client.Disconnect()
		return nil, err
	}
	return session, nil
}

func (mc *Client) closeDemandSessionsLocked() {
	d := &mc.demand
	defer d.updateIdle()
	for _, connection := range d.sessions {
		_ = connection.client.Disconnect()
	}
	d.sessions = nil
	d.shared = nil
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

	tools := connection.tools
	// A shared core can reconnect in place. A dedicated client is never
	// reused, so its tool call need not delay another session's disconnect.
	if connection == d.shared {
		defer d.mu.RUnlock()
	} else {
		d.mu.RUnlock()
	}
	for _, actual := range tools {
		if config.RawToolName(actual) == name && actual.Hash == expected.hash &&
			reflect.DeepEqual(actual.Annotations, expected.annotations) {
			return connection.client.CallTool(ctx, name, args)
		}
	}
	return nil, ErrConnectionGenerationChanged
}

func (mc *Client) activeDemandConnection(id string) *demandSession {
	if mc.GetConfig().ConnectionLifetime == config.KeepRunningPerSession {
		return mc.demand.sessions[id]
	}
	return mc.demand.shared
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
