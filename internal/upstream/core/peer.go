package core

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/v2/client"
	sdktransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/peering"
)

func (c *Client) SetPeerExecutor(executor peering.Executor) {
	c.mu.Lock()
	c.peerExecutor = executor
	c.mu.Unlock()
}
func (c *Client) connectPeer(ctx context.Context) error {
	t := peering.NewTransport(c.peerExecutor, c.config, c.CredentialBinding(), c.peerGeneration)
	c.client = client.NewClient(t)
	if err := c.client.Start(ctx); err != nil {
		return err
	}
	return c.initialize(ctx)
}
func (c *Client) PeerRPC(ctx context.Context, request sdktransport.JSONRPCRequest) (*sdktransport.JSONRPCResponse, error) {
	c.mu.RLock()
	sdk := c.client
	connected := c.connected
	c.mu.RUnlock()
	if !connected || sdk == nil {
		return nil, fmt.Errorf("upstream is not connected")
	}
	// The peer's own refresh/health requests use the SDK counter. Keep forwarded
	// request IDs disjoint on transports that multiplex responses by ID.
	original := request.ID
	request.ID = mcp.NewRequestId("peer-" + uuid.NewString())
	response, err := sdk.GetTransport().SendRequest(ctx, request)
	if response != nil {
		response.ID = original
	}
	return response, err
}
func (c *Client) PeerNotify(ctx context.Context, notification mcp.JSONRPCNotification) error {
	c.mu.RLock()
	sdk := c.client
	c.mu.RUnlock()
	if sdk == nil {
		return fmt.Errorf("upstream is not connected")
	}
	return sdk.GetTransport().SendNotification(ctx, notification)
}
func (c *Client) OnPeerNotification(fn func(mcp.JSONRPCNotification)) {
	c.mu.RLock()
	sdk := c.client
	c.mu.RUnlock()
	if sdk != nil {
		sdk.OnNotification(fn)
	}
}

func (c *Client) SuppressBrowser() { c.suppressBrowser = true }

func (c *Client) SetPeerGeneration(generation int64) { c.peerGeneration = generation }

type peerCallTimeoutKey struct{}

// WithPeerCallTimeout carries the execution budget already imposed centrally.
func WithPeerCallTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, peerCallTimeoutKey{}, timeout)
}
