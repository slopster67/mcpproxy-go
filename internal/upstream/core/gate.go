package core

import (
	"context"

	"github.com/mark3labs/mcp-go/v2/client"
	"github.com/mark3labs/mcp-go/v2/client/transport"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/upstream/dialgate"
)

// Gate returns the instance's retirement barrier (see internal/upstream/dialgate).
func (c *Client) Gate() *dialgate.Gate {
	return c.gate.Load()
}

// SetGate makes this client share g. The upstream manager uses it for the
// transient clients of a manual OAuth flow, so retiring the registered
// instance also stops the login it started. Call it before the
// client is used.
func (c *Client) SetGate(g *dialgate.Gate) {
	if g != nil {
		c.gate.Store(g)
	}
}

func (c *Client) Retire() { c.Gate().Retire() }

// Retired reports whether the instance's gate is retired.
func (c *Client) Retired() bool { return c.Gate().Retired() }

// innerTransport is the ONE place core asserts capabilities on an mcp-go
// client's transport: the transport core installs is
// a dialgate wrapper, and the OAuth handler lookup and the Streamable HTTP
// session snapshot need the concrete transport underneath it.
func innerTransport(cl *client.Client) transport.Interface {
	if cl == nil {
		return nil
	}
	return dialgate.Unwrap(cl.GetTransport())
}

// oauthEnv is the OAuth environment of this instance: every OAuth
// preflight, resource auto-detection, metadata discovery and cache fetch, and
// the OAuth HTTP client mcp-go uses for registration, code exchange and
// refresh, run on the instance's gated transport.
func (c *Client) oauthEnv(ctx context.Context) oauth.Env {
	return oauth.Env{Ctx: ctx, Transport: c.Gate().HTTPTransport(), Binding: c.CredentialBinding()}
}

// SetCredentialBinding binds the instance's OAuth state to b, the committed
// credential binding of the view it was built for.
// The upstream manager sets it before the instance becomes reachable, and on
// the transient clients of a manual OAuth flow (the registered instance's
// binding), so every token, client registration and completion record the
// instance writes, reads or clears is bound to it. Call it before the client
// is used.
func (c *Client) SetCredentialBinding(b commitview.CredentialBinding) {
	c.binding.Store(&b)
}

// CredentialBinding returns the instance's credential binding (zero: unbound,
// as for CLI clients).
func (c *Client) CredentialBinding() commitview.CredentialBinding {
	if b := c.binding.Load(); b != nil {
		return *b
	}
	return commitview.CredentialBinding{}
}
