package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// ServerIdentity indexes historical calls without retaining server configuration.
type ServerIdentity struct {
	ID         string    `json:"id"`          // SHA256 hash of the server name
	ServerName string    `json:"server_name"` // Human-readable name
	FirstSeen  time.Time `json:"first_seen"`  // When first encountered
	LastSeen   time.Time `json:"last_seen"`   // When last active
}

// ToolCallRecord represents a tool call with server context
type ToolCallRecord struct {
	ID         string                 `json:"id"`          // UUID
	ServerID   string                 `json:"server_id"`   // Server identity
	ServerName string                 `json:"server_name"` // For quick reference
	ToolName   string                 `json:"tool_name"`   // Original tool name (without server prefix)
	Arguments  map[string]interface{} `json:"arguments"`   // Tool arguments
	Response   interface{}            `json:"response"`    // Tool response
	Error      string                 `json:"error"`       // Error if failed
	Duration   int64                  `json:"duration"`    // Duration in nanoseconds
	Timestamp  time.Time              `json:"timestamp"`   // When the call was made
	RequestID  string                 `json:"request_id"`  // For correlation

	ParentCallID     string                  `json:"parent_call_id,omitempty"`     // Links nested calls to parent code_execution
	ExecutionType    string                  `json:"execution_type,omitempty"`     // "direct" or "code_execution"
	MCPSessionID     string                  `json:"mcp_session_id,omitempty"`     // MCP session identifier
	MCPClientName    string                  `json:"mcp_client_name,omitempty"`    // MCP client name from InitializeRequest
	MCPClientVersion string                  `json:"mcp_client_version,omitempty"` // MCP client version
	Annotations      *config.ToolAnnotations `json:"annotations,omitempty"`        // Tool behavior hints snapshot

	// ResponseTruncated and ResponseBytes describe a STORAGE-side cut:
	// the caller received the response whole, and only this persisted copy was
	// shortened to tool_call_max_response_size. ResponseBytes is the size of
	// the marshalled response before the cut, so a consumer can tell a
	// shortened record from a small one.
	ResponseTruncated bool  `json:"response_truncated,omitempty"`
	ResponseBytes     int64 `json:"response_bytes,omitempty"`

	// ArgumentsTruncated says the stored Arguments are a placeholder, not the
	// arguments the tool was called with. Replay MUST refuse to reuse them:
	// re-dispatching the placeholder would call the upstream tool with
	// nonsense, and call_tool_write / destructive variants reach real systems.
	ArgumentsTruncated bool `json:"arguments_truncated,omitempty"`
}

// GenerateServerID creates a unique, stable identity for a server
func GenerateServerID(server *config.ServerConfig) string {
	return hashString(server.Name)
}

// hashString creates a simple SHA256 hash of a string
func hashString(s string) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])
}

// NewServerIdentity creates a new ServerIdentity from config
func NewServerIdentity(server *config.ServerConfig) *ServerIdentity {
	id := GenerateServerID(server)

	now := time.Now()

	return &ServerIdentity{
		ID:         id,
		ServerName: server.Name,
		FirstSeen:  now,
		LastSeen:   now,
	}
}

// UpdateLastSeen updates the last seen timestamp.
func (si *ServerIdentity) UpdateLastSeen() { si.LastSeen = time.Now() }
