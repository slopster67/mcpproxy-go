package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	sdktransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
)

// Transport presents one peer-owned upstream to the primary's existing runtime.
// It carries original MCP definitions/results, never another proxy's router tools.
type Transport struct {
	executor   Executor
	cfg        *config.ServerConfig
	binding    commitview.CredentialBinding
	generation int64
	info       mcp.InitializeResult
	mu         sync.Mutex
	handler    func(mcp.JSONRPCNotification)
	unlisten   func()
}

func NewTransport(executor Executor, cfg *config.ServerConfig, binding commitview.CredentialBinding, generation int64) *Transport {
	return &Transport{executor: executor, cfg: config.CopyServerConfig(cfg), binding: binding, generation: generation}
}
func (t *Transport) Start(ctx context.Context) error {
	if t.executor == nil {
		return ErrOffline
	}
	desired := Connection{Config: t.cfg, Binding: t.binding, Generation: t.generation}
	if hub, ok := t.executor.(*Hub); ok && hub.PrepareConnection != nil {
		if err := hub.PrepareConnection(ctx, t.cfg.ExecutionHost(), &desired); err != nil {
			return err
		}
	}
	if err := t.executor.Call(ctx, t.cfg.ExecutionHost(), "connect", t.cfg.Name, desired, &t.info); err != nil {
		return err
	}
	if hub, ok := t.executor.(*Hub); ok {
		t.unlisten = hub.Listen(t.cfg.ExecutionHost()+"/"+t.cfg.Name, func(f Frame) {
			var notification mcp.JSONRPCNotification
			if json.Unmarshal(f.Data, &notification) == nil {
				t.mu.Lock()
				fn := t.handler
				t.mu.Unlock()
				if fn != nil {
					fn(notification)
				}
			}
		})
	}
	return nil
}
func (t *Transport) SendRequest(ctx context.Context, request sdktransport.JSONRPCRequest) (*sdktransport.JSONRPCResponse, error) {
	if request.Method == "initialize" {
		data, err := json.Marshal(t.info)
		return &sdktransport.JSONRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: data}, err
	}
	timeout := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	var result sdktransport.JSONRPCResponse
	if err := t.executor.Call(ctx, t.cfg.ExecutionHost(), "rpc", t.cfg.Name, RPC{Timeout: timeout, Request: request}, &result); err != nil {
		return nil, err
	}
	result.ID = request.ID
	return &result, nil
}
func (t *Transport) SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error {
	if notification.Method == "notifications/initialized" {
		return nil
	}
	return t.executor.Call(ctx, t.cfg.ExecutionHost(), "notify", t.cfg.Name, notification, nil)
}
func (t *Transport) SetNotificationHandler(fn func(mcp.JSONRPCNotification)) {
	t.mu.Lock()
	t.handler = fn
	t.mu.Unlock()
}
func (t *Transport) GetSessionId() string {
	return fmt.Sprintf("peer:%s:%s", t.cfg.ExecutionHost(), t.cfg.Name)
}
func (t *Transport) Close() error {
	if t.unlisten != nil {
		t.unlisten()
	}
	if t.executor == nil || !t.executor.Online(t.cfg.ExecutionHost()) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.executor.Call(ctx, t.cfg.ExecutionHost(), "disconnect", t.cfg.Name, nil, nil)
}
