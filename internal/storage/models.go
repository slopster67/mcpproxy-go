package storage

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// ErrToolApprovalNotFound is returned by GetToolApproval when no record
// exists for the requested server+tool key. Callers that want to distinguish
// "first time we've seen this tool" from a real read error (corrupt JSON,
// closed DB, mmap remap during compaction) MUST use errors.Is, not a generic
// `err != nil` check — a transient decode error must not be misread as
// "missing", which would otherwise silently overwrite a pending/changed
// record with a synthesized approved one.
var ErrToolApprovalNotFound = errors.New("tool approval not found")

// ErrUpstreamNotFound is returned by GetServer/GetUpstreamServer when no
// record exists for the requested server name. Callers that decide policy from
// the record MUST use errors.Is to tell it apart from a real read failure
// (corrupt record, closed DB, mmap remap): "no such server" is a verdict the
// caller can state, while an unreadable record means the caller knows nothing
// and must fail closed rather than answer "not configured".
var ErrUpstreamNotFound = errors.New("upstream not found")

// Bucket names for the encrypted database
const (
	ServersBucket      = "servers"
	ToolStatsBucket    = "toolstats"
	ToolApprovalBucket = "tool_approvals"

	OAuthTokenBucket      = "oauth_tokens" //nolint:gosec // bucket name, not a credential
	OAuthCompletionBucket = "oauth_completion"
	MetaBucket            = "meta"

	// SessionsBucket holds MCP transport/work session records.
	SessionsBucket = "mcp_sessions"
)

// ToolApprovalHashSchemaVersion identifies the current approval hash format.
const ToolApprovalHashSchemaVersion = 5

// ServerRecord is the canonical server configuration and its concurrency stamps.
// The shared ServerConfig prevents field-by-field persistence mappings.
type ServerRecord struct {
	config.ServerConfig
	Generation  int64 `json:"generation"`
	Incarnation int64 `json:"incarnation"`
	OAuthEpoch  int64 `json:"oauth_epoch"`
}

// ToolStatRecord represents tool usage statistics
type ToolStatRecord struct {
	ToolName string    `json:"tool_name"`
	Count    uint64    `json:"count"`
	LastUsed time.Time `json:"last_used"`
}

// ToolApprovalStatus constants for tool-level quarantine
const (
	ToolApprovalStatusApproved = "approved"
	ToolApprovalStatusPending  = "pending"
	ToolApprovalStatusChanged  = "changed"
)

// ToolApprovalRecord represents a tool's approval status for tool-level quarantine.
// When a tool is first discovered, it starts as "pending". Once approved, it becomes "approved".
// If the tool's description or schema changes after approval, it becomes "changed".
type ToolApprovalRecord struct {
	ServerName        string `json:"server_name"`
	ToolName          string `json:"tool_name"`
	ApprovedHash      string `json:"approved_hash"`
	CurrentHash       string `json:"current_hash"`
	HashSchemaVersion uint64 `json:"hash_schema_version,omitempty"`
	Status            string `json:"status"` // "approved", "pending", "changed"
	// TransientImplicit marks an in-memory synthesized pending record (never persisted).
	TransientImplicit    bool                    `json:"-"`
	ApprovedAt           time.Time               `json:"approved_at"`
	ApprovedBy           string                  `json:"approved_by"`
	PreviousDescription  string                  `json:"previous_description,omitempty"`
	CurrentDescription   string                  `json:"current_description,omitempty"`
	PreviousAnnotations  *config.ToolAnnotations `json:"previous_annotations,omitempty"`
	CurrentAnnotations   *config.ToolAnnotations `json:"current_annotations,omitempty"`
	PreviousSchema       string                  `json:"previous_schema,omitempty"`
	CurrentSchema        string                  `json:"current_schema,omitempty"`
	PreviousOutputSchema string                  `json:"previous_output_schema,omitempty"`
	CurrentOutputSchema  string                  `json:"current_output_schema,omitempty"`
	Disabled             bool                    `json:"disabled,omitempty"`
}

func (r *ToolApprovalRecord) ApproveCurrentHash() {
	r.ApprovedHash = r.CurrentHash
	r.HashSchemaVersion = ToolApprovalHashSchemaVersion
}

// ToolApprovalKey returns the storage key for a tool approval record.
func ToolApprovalKey(serverName, toolName string) string {
	return serverName + ":" + toolName
}

// Key returns the storage key for this tool approval record.
func (r *ToolApprovalRecord) Key() string {
	return ToolApprovalKey(r.ServerName, r.ToolName)
}

// MarshalBinary implements encoding.BinaryMarshaler
func (r *ToolApprovalRecord) MarshalBinary() ([]byte, error) {
	return json.Marshal(r)
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler
func (r *ToolApprovalRecord) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, r)
}

// OAuthTokenRecord represents stored OAuth tokens for a server
type OAuthTokenRecord struct {
	ServerName   string    `json:"server_name"`            // Storage key (serverName_hash format)
	DisplayName  string    `json:"display_name,omitempty"` // Actual server name (for RefreshManager lookup)
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes,omitempty"`
	// Issuer is the authorization server that issued the token. mcp-go
	// refreshes only a token whose issuer it knows (RFC 9207 mix-up defense).
	Issuer  string    `json:"issuer,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// ClientID and ClientSecret are persisted for DCR (Dynamic Client Registration)
	// These are required for token refresh when using DCR-obtained credentials
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	// CallbackPort and RedirectURI ensure re-authentication uses the same
	// redirect URI registered during DCR.
	CallbackPort int    `json:"callback_port,omitempty"` // Port used during DCR for redirect_uri
	RedirectURI  string `json:"redirect_uri,omitempty"`  // Full redirect URI registered with DCR
	// OAuthEpoch is the credential binding epoch the record was written
	// under: the OAuthEpoch of the server's row when a bound writer saved
	// it. A bound reader ignores a record of another epoch.
	OAuthEpoch int64 `json:"oauth_epoch,omitempty"`
	// Revision is set from the oauth_tokens bucket's sequence on every write
	// of the record. A binding-conditional clear acts only on
	// the exact revision its caller inspected, so a parked clear never
	// erases a record somebody rewrote meanwhile.
	Revision uint64 `json:"revision,omitempty"`
}

func (r *OAuthTokenRecord) GetServerName() string {
	return r.DisplayName
}

// OAuthCompletionEvent represents an OAuth completion event for cross-process notification
type OAuthCompletionEvent struct {
	ServerName  string     `json:"server_name"`
	CompletedAt time.Time  `json:"completed_at"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"` // Nil if not yet processed by server
	// Incarnation and OAuthEpoch are the credential binding of the instance
	// that completed the login. The consumer drops an event whose binding is
	// not the registered instance's. Zero on an event written by an unbound
	// caller.
	Incarnation int64 `json:"incarnation,omitempty"`
	OAuthEpoch  int64 `json:"oauth_epoch,omitempty"`
}

// MarshalBinary implements encoding.BinaryMarshaler
func (u *ServerRecord) MarshalBinary() ([]byte, error) {
	return json.Marshal(u)
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler
func (u *ServerRecord) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, u)
}

// MarshalBinary implements encoding.BinaryMarshaler
func (t *ToolStatRecord) MarshalBinary() ([]byte, error) {
	return json.Marshal(t)
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler
func (t *ToolStatRecord) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, t)
}

func (o *OAuthTokenRecord) MarshalBinary() ([]byte, error)        { return json.Marshal(o) }
func (o *OAuthTokenRecord) UnmarshalBinary(data []byte) error     { return json.Unmarshal(data, o) }
func (e *OAuthCompletionEvent) MarshalBinary() ([]byte, error)    { return json.Marshal(e) }
func (e *OAuthCompletionEvent) UnmarshalBinary(data []byte) error { return json.Unmarshal(data, e) }
