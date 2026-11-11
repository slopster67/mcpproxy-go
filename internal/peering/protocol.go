// Package peering provides the primary's authenticated execution channel.
// Peers execute connections and cache assigned credentials in memory;
// persistent credentials, policy and management stay on the primary.
package peering

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdktransport "github.com/mark3labs/mcp-go/v2/client/transport"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

const MaxMessage = 16 << 20

var ErrOffline = errors.New("peer is offline")

type Frame struct {
	ID     string          `json:"id,omitempty"`
	Op     string          `json:"op"`
	Server string          `json:"server,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type Executor interface {
	Call(context.Context, string, string, string, any, any) error
	Online(string) bool
}

type Peer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenHash string `json:"token_hash,omitempty"`
	// OAuthSession identifies the peer's current browser sign-in. A new
	// sign-in replaces it, which revokes the previous machine's OAuth tokens.
	OAuthSession string     `json:"oauth_session,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	Online       bool       `json:"online"`
	Address      string     `json:"address,omitempty"`
}

type ServerState struct {
	UserLoggedOut bool       `json:"user_logged_out"`
	Connected     bool       `json:"connected"`
	Authenticated bool       `json:"authenticated"`
	AuthRequired  bool       `json:"auth_required"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}
type Hello struct {
	ProcessID   string                 `json:"process_id"`
	Environment map[string]bool        `json:"environment"`
	Address     string                 `json:"address"`
	Servers     map[string]ServerState `json:"servers"`
}

// RPC carries only headers already admitted by the primary's live forwarding policy.
type RPC struct {
	Timeout time.Duration               `json:"timeout"`
	Request sdktransport.JSONRPCRequest `json:"request"`
}

type Connection struct {
	Secrets    map[string]string            `json:"secrets"`
	OAuth      storage.OAuthSnapshot        `json:"oauth"`
	Generation int64                        `json:"generation"`
	Config     *config.ServerConfig         `json:"config"`
	Binding    commitview.CredentialBinding `json:"binding"`
}
type Assignment struct {
	Generation int64                        `json:"generation"`
	Name       string                       `json:"name"`
	Enabled    bool                         `json:"enabled"`
	Binding    commitview.CredentialBinding `json:"binding"`
}
type Sync struct {
	Connections []Connection `json:"connections"`
	Environment []string     `json:"environment"`
	Generation  int64        `json:"generation"`
	Servers     []Assignment `json:"servers"`
}
