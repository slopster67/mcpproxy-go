package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/dashboardauth"
	"github.com/slopster67/mcpproxy-go/internal/secret"

	"github.com/slopster67/mcpproxy-go/internal/secureenv"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
)

const (
	defaultPort = "127.0.0.1:8080" // Localhost-only binding by default for security

	// Routing mode constants
	RoutingModeRetrieveTools = "retrieve_tools" // Default: BM25 search via retrieve_tools + call_tool_read/write/destructive
	RoutingModeDirect        = "direct"         // All upstream tools exposed directly with serverName__toolName naming
	RoutingModeCodeExecution = "code_execution" // JS orchestration via code_execution tool with tool catalog

	// Tool response mode constants
	ToolResponseModeFull    = "full"    // Default: today's schema-bearing retrieve_tools entries
	ToolResponseModeCompact = "compact" // Compact signatures + first-sentence descriptions

	DirectToolResponseModeFull     = "full"     // Default: today's schema-bearing direct entries
	DirectToolResponseModeDeferred = "deferred" // Signature-suffixed descriptions, permissive schema
)

// Duration is a wrapper around time.Duration that can be marshaled to/from JSON.
// When serialized to JSON, it is represented as a string (e.g., "30s", "5m").
// @swaggertype string
type Duration time.Duration

// MarshalJSON implements json.Marshaler interface
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler interface
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration format: %w", err)
	}

	*d = Duration(parsed)
	return nil
}

// Duration returns the underlying time.Duration
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// Built-in defaults for the discovery/health-check loops. They live here (not
// in DefaultConfig) so an unset key resolves to the built-in default.
const (
	defaultHealthCheckInterval   = 30 * time.Second
	defaultToolDiscoveryInterval = 5 * time.Minute
	// defaultInitTimeout is the deadline applied to a stdio/HTTP upstream's MCP
	// `initialize` handshake when no per-server or global override is set.
	defaultInitTimeout = 30 * time.Second

	// Built-in defaults for the HTTP server's request deadlines.
	// Like the intervals above they live here (not in DefaultConfig) so an
	// unset key resolves to the built-in behaviour and existing configs are
	// unchanged.
	defaultHTTPReadTimeout  = 120 * time.Second
	defaultHTTPWriteTimeout = 120 * time.Second
	defaultHTTPIdleTimeout  = 180 * time.Second
)

// resolveHTTPTimeout applies the tri-state contract shared by the three HTTP
// server deadlines: nil = the built-in default, a pointer to 0 =
// DISABLED (net/http's zero value means "no deadline" — except IdleTimeout,
// where net/http falls back to ReadTimeout; see ResolveHTTPIdleTimeout), a
// positive value = that
// value. A negative value falls back to the default — validation rejects those
// anyway, this only keeps a hand-edited file from producing a nonsense deadline.
//
// Note the deliberate asymmetry with ResolveInitTimeout, where 0 maps back to
// the default: a connect handshake must always have a ceiling, whereas "no
// response deadline at all" is a legitimate HTTP setting an operator may want.
func resolveHTTPTimeout(v *Duration, def time.Duration) time.Duration {
	if v == nil {
		return def
	}
	if d := v.Duration(); d >= 0 {
		return d
	}
	return def
}

// ResolveHTTPReadTimeout resolves http.Server.ReadTimeout: unset → 120s,
// 0 → disabled, positive → that value.
func (c *Config) ResolveHTTPReadTimeout() time.Duration {
	return resolveHTTPTimeout(c.HTTPReadTimeout, defaultHTTPReadTimeout)
}

// ResolveHTTPWriteTimeout resolves http.Server.WriteTimeout: unset → 120s,
// 0 → disabled, positive → that value. Streaming routes (MCP + SSE
// /events) clear the resulting deadline per-request, so this value only
// governs non-streaming endpoints — see defaultHTTPWriteTimeout.
func (c *Config) ResolveHTTPWriteTimeout() time.Duration {
	return resolveHTTPTimeout(c.HTTPWriteTimeout, defaultHTTPWriteTimeout)
}

// ResolveHTTPIdleTimeout resolves http.Server.IdleTimeout: unset → 180s,
// 0 → no idle deadline of its own, positive → that value. NOTE:
// net/http falls back to ReadTimeout when IdleTimeout is zero, so an explicit
// "0s" here fully disables the idle deadline only when http_read_timeout is
// also 0 — otherwise idle connections are reaped after the read timeout.
func (c *Config) ResolveHTTPIdleTimeout() time.Duration {
	return resolveHTTPTimeout(c.HTTPIdleTimeout, defaultHTTPIdleTimeout)
}

// resolveInterval returns the configured global cadence or its default.
// Zero disables periodic health checks or discovery.
func resolveInterval(value *Duration, def time.Duration) time.Duration {
	if value != nil {
		return value.Duration()
	}
	return def
}

// ResolveHealthCheckInterval returns the global health-check cadence.
func (c *Config) ResolveHealthCheckInterval() time.Duration {
	return resolveInterval(c.HealthCheckInterval, defaultHealthCheckInterval)
}

// ResolveToolDiscoveryInterval returns the global discovery cadence.
// Zero disables periodic discovery; connection and notification discovery remain.
func (c *Config) ResolveToolDiscoveryInterval() time.Duration {
	return resolveInterval(c.ToolDiscoveryInterval, defaultToolDiscoveryInterval)
}

// ResolveInitTimeout returns the global MCP initialization deadline.
// Zero uses the default so initialization always has a deadline.
func (c *Config) ResolveInitTimeout() time.Duration {
	resolved := resolveInterval(c.InitTimeout, defaultInitTimeout)
	if resolved <= 0 {
		return defaultInitTimeout
	}
	return resolved
}

// Config represents the main configuration structure
type Config struct {
	Listen string `env:"MCPPROXY_LISTEN" mode:"both" json:"listen"`

	DataDir string `env:"MCPPROXY_DATA_DIR" mode:"both" json:"data_dir"`

	Servers           []*ServerConfig `env:"-" json:"-"`
	Profiles          []ProfileConfig `env:"-" json:"-"`
	ToolsLimit        int             `env:"MCPPROXY_TOOLS_LIMIT" json:"tools_limit"`
	ToolResponseLimit int             `env:"MCPPROXY_TOOL_RESPONSE_LIMIT" json:"tool_response_limit"`
	CallToolTimeout   Duration        `env:"MCPPROXY_CALL_TOOL_TIMEOUT" json:"call_tool_timeout" swaggertype:"string"`
	// MaxResultSizeChars is advertised on every tool as
	// `_meta.anthropic/maxResultSizeChars`; it raises Claude Code's
	// inline-response ceiling from 50k to up to 500k chars. Omit the key for
	// the 500000 default; set it to 0 to disable the annotation.
	MaxResultSizeChars *int `env:"MCPPROXY_MAX_RESULT_SIZE_CHARS" json:"max_result_size_chars,omitempty"`

	// Discovery & health-check cadence. Both are *Duration
	// tri-state pointers: nil = inherit the built-in default; a pointer to 0s =
	// the loop is disabled; a positive value = that interval. Defaults live only
	// in the resolvers (ResolveHealthCheckInterval / ResolveToolDiscoveryInterval).
	// Validated
	// in Validate(): health-check ∈ {0} ∪ [5s,1h]; tool-discovery ∈ {0} ∪ [30s,24h].
	HealthCheckInterval   *Duration `env:"MCPPROXY_HEALTH_CHECK_INTERVAL" json:"health_check_interval,omitempty" swaggertype:"string"`
	ToolDiscoveryInterval *Duration `env:"MCPPROXY_TOOL_DISCOVERY_INTERVAL" json:"tool_discovery_interval,omitempty" swaggertype:"string"`

	// InitTimeout is the global default deadline for an upstream's MCP
	// `initialize` handshake. *Duration tri-state: nil =
	// inherit the built-in 30s default; a positive value = that deadline. A
	// Resolved by ResolveInitTimeout;
	// validated to {0} ∪ [1s, 30m] in Validate(). Servers doing legitimate
	// first-run warmup (cache/index build) before answering `initialize` can
	// raise this so they are not killed mid-startup.
	InitTimeout *Duration `env:"MCPPROXY_INIT_TIMEOUT" json:"init_timeout,omitempty" swaggertype:"string"`

	// HTTPReadTimeout caps how long reading a whole request (headers + body)
	// may take. Unset = 120s; "0s" disables it. Requires a restart.
	HTTPReadTimeout *Duration `env:"MCPPROXY_HTTP_READ_TIMEOUT" json:"http_read_timeout,omitempty" swaggertype:"string"`
	// HTTPWriteTimeout caps how long producing a whole response may take on
	// non-streaming endpoints (REST, Web UI, health). Unset = 120s; "0s"
	// disables it globally. MCP and SSE /events routes are exempt by design.
	HTTPWriteTimeout *Duration `env:"MCPPROXY_HTTP_WRITE_TIMEOUT" json:"http_write_timeout,omitempty" swaggertype:"string"`
	// HTTPIdleTimeout caps how long an idle keep-alive connection is kept open.
	// Unset = 180s. "0s" removes the dedicated idle deadline, but net/http then
	// falls back to ReadTimeout — idle is fully unbounded only when
	// http_read_timeout is also "0s". Requires a restart.
	HTTPIdleTimeout *Duration `env:"MCPPROXY_HTTP_IDLE_TIMEOUT" json:"http_idle_timeout,omitempty" swaggertype:"string"`

	// Environment configuration for secure variable filtering
	Environment *secureenv.EnvConfig `env:",init" json:"environment,omitempty"`

	// Logging configuration
	Logging *LogConfig `env:",init" mode:"both" json:"logging,omitempty"`

	// Code execution settings
	CodeExecutionTimeoutMs    int `env:"MCPPROXY_CODE_EXECUTION_TIMEOUT_MS" json:"code_execution_timeout_ms,omitempty"`         // Timeout in milliseconds (default: 120000, max: 600000)
	CodeExecutionMaxToolCalls int `env:"MCPPROXY_CODE_EXECUTION_MAX_TOOL_CALLS" json:"code_execution_max_tool_calls,omitempty"` // Max tool calls per execution (0 = unlimited, default: 0)
	CodeExecutionPoolSize     int `env:"MCPPROXY_CODE_EXECUTION_POOL_SIZE" json:"code_execution_pool_size,omitempty"`           // JavaScript runtime pool size (default: 10)
	CodeExecutionMaxParallel  int `env:"MCPPROXY_CODE_EXECUTION_MAX_PARALLEL" json:"code_execution_max_parallel,omitempty"`     // Default concurrency for call_tools() batches (1-32, default: 8)

	// Health status settings
	OAuthExpiryWarningHours float64 `env:"MCPPROXY_OAUTH_EXPIRY_WARNING_HOURS" json:"oauth_expiry_warning_hours,omitempty"` // Hours before token expiry to show degraded status (default: 1.0)

	// Activity logging settings (RFC-003)
	ActivityRetentionDays int `env:"MCPPROXY_ACTIVITY_RETENTION_DAYS" json:"activity_retention_days,omitempty"` // Max age before pruning (default: 90)
	ActivityMaxRecords    int `env:"MCPPROXY_ACTIVITY_MAX_RECORDS" json:"activity_max_records,omitempty"`       // Max records before pruning (default: 100000)
	// ActivityMaxSizeMB caps the total activity-log size in MB before the
	// oldest records are pruned. Omit the key for the 256MB default; set it to
	// 0 to disable the size cap.
	ActivityMaxSizeMB          *int `env:"MCPPROXY_ACTIVITY_MAX_SIZE_MB" json:"activity_max_size_mb,omitempty"`
	ActivityMaxResponseSize    int  `env:"MCPPROXY_ACTIVITY_MAX_RESPONSE_SIZE" json:"activity_max_response_size,omitempty"`       // Response truncation limit in bytes (default: 65536)
	ActivityCleanupIntervalMin int  `env:"MCPPROXY_ACTIVITY_CLEANUP_INTERVAL_MIN" json:"activity_cleanup_interval_min,omitempty"` // Background cleanup interval in minutes (default: 60)

	// Bounds for the per-server tool-call history behind GET /api/v1/tool-calls.
	// It is a recent-debugging window, not an audit log — the activity
	// log is the durable record — and it kept every upstream response whole,
	// per server, forever. A non-positive value means "use the default", not
	// "disable": this store must never be unbounded again, so there is
	// deliberately no off switch.
	ToolCallMaxResponseSize     int `env:"MCPPROXY_TOOL_CALL_MAX_RESPONSE_SIZE" json:"tool_call_max_response_size,omitempty"`           // Cap on a stored response, in bytes (default: 65536)
	ToolCallMaxRecordsPerServer int `env:"MCPPROXY_TOOL_CALL_MAX_RECORDS_PER_SERVER" json:"tool_call_max_records_per_server,omitempty"` // Calls retained per server (default: 1000)

	// Sensitive data detection settings

	// Output-schema validation settings
	OutputValidation *OutputValidationConfig `env:",init" json:"output_validation,omitempty"`

	// Output sanitisation settings

	// Observability settings: usage aggregate cache/persistence cadence.
	Observability *ObservabilityConfig `env:",init" json:"observability,omitempty"`

	// Routing mode: how MCP tools are exposed to clients
	// Valid values: "retrieve_tools" (default), "direct", "code_execution"
	RoutingMode string `env:"MCPPROXY_ROUTING_MODE" json:"routing_mode,omitempty"`

	// Tool response mode: how retrieve_tools serializes results.
	// Valid values: "" (= full), "full" (default: today's schema-bearing
	// entries), "compact" (signature + first-sentence entries). Orthogonal to
	// routing_mode — routing_mode selects the tool SURFACE, this selects the
	// SERIALIZATION within the retrieve_tools surface. Serialization-only: it
	// never affects the query, ranking, or result set. Fixed at startup.
	ToolResponseMode string `env:"MCPPROXY_TOOL_RESPONSE_MODE" json:"tool_response_mode,omitempty"`

	DirectToolResponseMode string `env:"MCPPROXY_DIRECT_TOOL_RESPONSE_MODE" json:"direct_tool_response_mode,omitempty"`

	// Instructions text returned in the MCP initialize response to guide AI agents.
	// When empty, a built-in default is used that explains retrieve_tools workflow.
	Instructions string `env:"MCPPROXY_INSTRUCTIONS" json:"instructions,omitempty"`

	// AdvertiseUpstreamServers controls whether MCP clients are told the
	// names of the upstream servers they can reach — in the initialize
	// instructions and the retrieve_tools description — so agents use the
	// proxied tools instead of falling back to shell CLIs. Names are always
	// filtered to the caller's profile and client token scope. Default true;
	// set false to keep server names out of client context. Read live.
	AdvertiseUpstreamServers *bool `env:"MCPPROXY_ADVERTISE_UPSTREAM_SERVERS" json:"advertise_upstream_servers,omitempty"`

	// Security scanner settings

	// Deployment credentials are never serialized or included in activity records.
	EncryptionKey  EncryptionKey         `env:"MCPPROXY_ENCRYPTION_KEY" file:"true" json:"-"`
	OIDC           *dashboardauth.Config `env:",init" json:"-"`
	PeerPrimaryURL string                `env:"MCPPROXY_PEER_PRIMARY_URL" file:"true" mode:"peer" json:"-"`
	PeerToken      string                `env:"MCPPROXY_PEER_TOKEN" file:"true" mode:"peer" json:"-"`
}

// LogConfig represents logging configuration
type LogConfig struct {
	Level         string `env:"MCPPROXY_LOG_LEVEL" json:"level"`
	EnableFile    bool   `env:"MCPPROXY_LOG_TO_FILE" json:"enable_file"`
	EnableConsole bool   `env:"MCPPROXY_LOG_TO_CONSOLE" json:"enable_console"`
	Filename      string `env:"MCPPROXY_LOG_FILENAME" json:"filename"`
	LogDir        string `env:"MCPPROXY_LOG_DIR" json:"log_dir,omitempty"`   // Custom log directory
	MaxSize       int    `env:"MCPPROXY_LOG_MAX_SIZE" json:"max_size"`       // MB
	MaxBackups    int    `env:"MCPPROXY_LOG_MAX_BACKUPS" json:"max_backups"` // number of backup files
	MaxAge        int    `env:"MCPPROXY_LOG_MAX_AGE" json:"max_age"`         // days
	Compress      bool   `env:"MCPPROXY_LOG_COMPRESS" json:"compress"`
}

type TrustMode string

const (
	TrustModeAuto   TrustMode = "auto"
	TrustModeManual TrustMode = "manual"
)

// ServerConfig represents upstream MCP server configuration
type ServerConfig struct {
	HostID           *string           `json:"host_id,omitempty"`
	AvailableToPeers *bool             `json:"available_to_peers,omitempty"`
	Icon             *string           `json:"icon,omitempty"`
	Name             string            `json:"name,omitempty"`
	URL              string            `json:"url,omitempty"`
	Protocol         string            `json:"protocol,omitempty"` // stdio, http, sse, streamable-http, auto
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	WorkingDir       string            `json:"working_dir,omitempty"` // Working directory for stdio servers
	Env              map[string]string `json:"env,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"` // For HTTP servers
	OAuth            *OAuthConfig      `json:"oauth,omitempty"`   // OAuth mode and overrides of discovered values
	Enabled          bool              `json:"enabled"`

	TrustMode string    `json:"trust_mode,omitempty"` // auto approves tools; manual requires review
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated,omitempty"`

	// LauncherWaitTimeout caps how long mcpproxy will wait for a locally-launched
	// HTTP/SSE upstream's URL to become reachable after Spawn(). Only consulted
	// when the server is configured with both Command and an HTTP/SSE URL — i.e.,
	// mcpproxy starts the process AND connects via network. Stdio servers ignore
	// this field. Zero or unset → 30s default.
	LauncherWaitTimeout Duration `json:"launcher_wait_timeout,omitempty" swaggertype:"string"`
}

// OAuth modes. Auto (the empty mode) uses OAuth only when the upstream asks
// for it; on and off force the decision.
const (
	OAuthModeAuto = "auto"
	OAuthModeOn   = "on"
	OAuthModeOff  = "off"
)

// OAuthMode reports the server's OAuth mode: OAuthModeAuto, OAuthModeOn or
// OAuthModeOff.
func (sc *ServerConfig) OAuthMode() string {
	if sc == nil || sc.OAuth == nil || sc.OAuth.Mode == "" {
		return OAuthModeAuto
	}
	return sc.OAuth.Mode
}

// OAuthConfig represents OAuth configuration for a server
type OAuthConfig struct {
	// Mode is "on" (always use OAuth), "off" (never use OAuth) or empty
	// (detect: use OAuth when the upstream requires it).
	Mode string `json:"mode,omitempty"`

	ClientID     string            `json:"client_id,omitempty"`
	ClientSecret string            `json:"client_secret,omitempty"`
	RedirectURI  string            `json:"redirect_uri,omitempty"`
	Scopes       []string          `json:"scopes,omitempty"`
	ExtraParams  map[string]string `json:"extra_params,omitempty"` // Additional OAuth parameters (e.g., RFC 8707 resource)

	// Optional per-server discovery overrides. Each is an absolute
	// https URL (http only on a loopback host) with no query, fragment or
	// userinfo; validated at write time only (ValidateOAuthEndpointOverrides).
	// The endpoint overrides rewrite the matching fields of the authorization
	// server metadata mcp-go reads; AuthServerMetadataURL replaces discovery.
	AuthorizationEndpoint string `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string `json:"token_endpoint,omitempty"`
	RegistrationEndpoint  string `json:"registration_endpoint,omitempty"`
	AuthServerMetadataURL string `json:"auth_server_metadata_url,omitempty"`
}

// OutputValidationConfig controls output-schema validation behaviour.
type OutputValidationConfig struct {
	Mode                     string `env:"MCPPROXY_OUTPUT_VALIDATION_MODE" json:"mode,omitempty"`                                             // "off" | "warn" | "strict"; default "warn"
	MaxBytes                 int    `env:"MCPPROXY_OUTPUT_VALIDATION_MAX_BYTES" json:"max_bytes,omitempty"`                                   // structured payload byte cap; default 5<<20
	MaxDepth                 int    `env:"MCPPROXY_OUTPUT_VALIDATION_MAX_DEPTH" json:"max_depth,omitempty"`                                   // nesting depth cap; default 64
	MissingStructuredContent string `env:"MCPPROXY_OUTPUT_VALIDATION_MISSING_STRUCTURED_CONTENT" json:"missing_structured_content,omitempty"` // "allow" | "block"; default "allow"
}

// DefaultOutputValidationConfig returns the default configuration for output-schema validation.
func DefaultOutputValidationConfig() *OutputValidationConfig {
	return &OutputValidationConfig{
		Mode:                     "warn",
		MaxBytes:                 5 << 20,
		MaxDepth:                 64,
		MissingStructuredContent: "allow",
	}
}

// IsStrict returns true when Mode is "strict". A nil receiver returns false.
func (c *OutputValidationConfig) IsStrict() bool {
	if c == nil {
		return false
	}
	return c.Mode == "strict"
}

// EffectiveMaxBytes returns MaxBytes, falling back to 5<<20 when zero or nil.
func (c *OutputValidationConfig) EffectiveMaxBytes() int {
	if c == nil || c.MaxBytes <= 0 {
		return 5 << 20
	}
	return c.MaxBytes
}

// EffectiveMaxDepth returns MaxDepth, falling back to 64 when zero or nil.
func (c *OutputValidationConfig) EffectiveMaxDepth() int {
	if c == nil || c.MaxDepth <= 0 {
		return 64
	}
	return c.MaxDepth
}

// BlockOnMissingStructured returns true when MissingStructuredContent is "block".
// A nil receiver returns false (default is "allow").
func (c *OutputValidationConfig) BlockOnMissingStructured() bool {
	if c == nil {
		return false
	}
	return c.MissingStructuredContent == "block"
}

// ToolMetadata represents tool information stored in the index
// ManagementServerName is the built-in administration tool namespace.
const ManagementServerName = "mcpproxy"

type ToolMetadata struct {
	// Name is the tool's display/lookup name. Discovery stores the RAW upstream
	// name here (no server prefix); index reads return the canonical
	// "<server>:<raw>" id. Consumers that need the exact upstream name
	// must go through RawToolName, never strip a prefix by hand.
	Name       string `json:"name"`
	ServerName string `json:"server_name"`
	// RawName is the exact upstream-reported tool name, stamped at discovery.
	// It is the identity every producer keys on — approval
	// records, index docIDs — so "ns:erase" never collapses to "erase". Empty
	// on metadata built outside discovery; RawToolName derives it from Name.
	RawName          string           `json:"raw_name,omitempty"`
	Description      string           `json:"description"`
	ParamsJSON       string           `json:"params_json"`
	OutputSchemaJSON string           `json:"output_schema_json,omitempty"` // declared output schema, raw JSON bytes
	Hash             string           `json:"hash"`
	Annotations      *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations represents MCP tool behavior hints
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

type ObservabilityConfig struct {
	// UsageCacheTTL bounds the freshness of the usage endpoint's read cache for
	// wide windows. Default 5s.
	UsageCacheTTL Duration `env:"MCPPROXY_OBSERVABILITY_USAGE_CACHE_TTL" json:"usage_cache_ttl,omitempty" swaggertype:"string"`
	// UsagePersistInterval is how often the actor-owned usage aggregate snapshot
	// is flushed to storage. Default 30s.
	UsagePersistInterval Duration `env:"MCPPROXY_OBSERVABILITY_USAGE_PERSIST_INTERVAL" json:"usage_persist_interval,omitempty" swaggertype:"string"`
}

// DefaultObservabilityConfig returns the default observability configuration.
func DefaultObservabilityConfig() *ObservabilityConfig {
	return &ObservabilityConfig{
		UsageCacheTTL:        Duration(5 * time.Second),
		UsagePersistInterval: Duration(30 * time.Second),
	}
}

// SearchResult represents a search result with score
type SearchResult struct {
	Tool  *ToolMetadata `json:"tool"`
	Score float64       `json:"score"`
}

// ToolStats represents tool statistics
type ToolStats struct {
	TotalTools int             `json:"total_tools"`
	TopTools   []ToolStatEntry `json:"top_tools"`
}

// ToolStatEntry represents a single tool stat entry
type ToolStatEntry struct {
	ToolName string `json:"tool_name"`
	Count    uint64 `json:"count"`
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Listen: defaultPort,

		DataDir: "", // Resolved from the selected process mode by Load

		Servers:           []*ServerConfig{},
		ToolsLimit:        15,
		ToolResponseLimit: 20000,                     // Default 20000 characters
		CallToolTimeout:   Duration(2 * time.Minute), // Default 2 minutes for tool calls

		// Default secure environment configuration
		Environment: secureenv.DefaultEnvConfig(),

		// Default logging configuration
		Logging: &LogConfig{
			Level:         "info",
			EnableFile:    false, // Changed: Console by default
			EnableConsole: true,
			Filename:      "main.log",
			MaxSize:       10, // 10MB
			MaxBackups:    5,  // 5 backup files
			MaxAge:        30, // 30 days
			Compress:      true,
		},

		// Repository detection enabled by default

		// Default sensitive data detection settings (enabled by default for security)

		// Default output-schema validation settings
		OutputValidation: DefaultOutputValidationConfig(),

		// Default output sanitisation settings

		// Execution limits for clients allowed to run sandboxed JavaScript.
		CodeExecutionTimeoutMs:    120000, // 2 minutes (120,000ms)
		CodeExecutionMaxToolCalls: 0,      // Unlimited by default (0 = no limit)
		CodeExecutionPoolSize:     10,     // 10 JavaScript runtime instances
		CodeExecutionMaxParallel:  8,      // 8 concurrent upstream calls per call_tools() batch

		// Session risk warning prose disabled by default to reduce token overhead
		// and LLM distraction in trusted setups. Structured risk
		// fields are still emitted; only the prose `warning` is gated.

		// Activity logging defaults (RFC-003)
		ActivityRetentionDays:   90,     // 90 days retention
		ActivityMaxRecords:      100000, // 100K records max
		ActivityMaxResponseSize: 65536,  // 64KB response truncation

		ToolCallMaxResponseSize:     65536, // 64KB per stored tool-call response
		ToolCallMaxRecordsPerServer: 1000,  // recent-history window per server

		ActivityCleanupIntervalMin: 60, // 1 hour cleanup interval

		// Observability defaults
		Observability: DefaultObservabilityConfig(),
	}
}

// ValidTrustModes returns the accepted trust_mode values in their canonical
// order. It is the single source of truth for the operator-facing error text
// produced by config validation and by the REST layer.
func ValidTrustModes() []string {
	return []string{string(TrustModeAuto), string(TrustModeManual)}
}

func IsValidTrustMode(s string) bool {
	switch TrustMode(s) {
	case "", TrustModeAuto, TrustModeManual:
		return true
	default:
		return false
	}
}

func (sc *ServerConfig) EffectiveTrustMode() TrustMode {
	switch TrustMode(sc.TrustMode) {
	case TrustModeAuto, TrustModeManual:
		return TrustMode(sc.TrustMode)
	}

	return TrustModeManual
}

// IsQuarantineSkipped returns whether this server should skip tool-level
// quarantine. Thin wrapper over EffectiveTrustMode(): only trust_mode auto
// skips quarantine; scan and manual do not.
func (sc *ServerConfig) IsQuarantineSkipped() bool {
	return sc.EffectiveTrustMode() == TrustModeAuto
}

// ServerContributesTools reports whether a server is administratively enabled.
func ServerContributesTools(enabled bool) bool { return enabled }

// ContributesTools is the *ServerConfig form of ServerContributesTools. A nil
// receiver contributes nothing.
func (sc *ServerConfig) ContributesTools() bool {
	return sc != nil && ServerContributesTools(sc.Enabled)
}

// HasStaticAuthorizationHeader reports whether the server is configured to
// authenticate with a static credential in the `Authorization` header (name
// matched case-insensitively, value non-blank).
//
// OAuth and a static Authorization header are mutually exclusive on the wire:
// the OAuth transport populates that very header, and the headers-auth
// strategy runs before OAuth is ever attempted. A config that declares one
// has therefore stopped using the other, which lets the server projection
// ignore an OAuth token record left behind by a previous OAuth login
// without a heuristic on other header names. A nil receiver has no
// headers.
func (sc *ServerConfig) HasStaticAuthorizationHeader() bool {
	if sc == nil {
		return false
	}
	for name, value := range sc.Headers {
		if strings.EqualFold(name, "Authorization") && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error implements the error interface
func (v ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", v.Field, v.Message)
}

// ValidateDetailed validates settings and database-managed entity definitions.
// Profile edits use it before committing the new entity snapshot.
func (c *Config) ValidateDetailed() []ValidationError {
	errors := append(c.validateDetailedCore(), c.oauthRedirectURIErrors()...)
	errors = append(errors, c.oauthEndpointOverrideErrors()...)
	if _, err := ValidateProfiles(c); err != nil {
		errors = append(errors, ValidationError{Field: "profiles", Message: err.Error()})
	}
	return errors
}

// oauthRedirectURIErrors reports every per-server `oauth.redirect_uri` that the
// loopback callback listener cannot honor.
//
// Kept separate from validateDetailedCore because the two callers want opposite
// behavior: a write surface must reject the value (it is a permanent connect
// failure otherwise, with an error that never names redirect_uri), while
// config.Load must tolerate one already on disk and let it fail loudly at
// connect time instead.
func (c *Config) oauthRedirectURIErrors() []ValidationError {
	var errors []ValidationError
	for i, server := range c.Servers {
		if server == nil || server.OAuth == nil {
			continue
		}
		if strings.TrimSpace(server.OAuth.RedirectURI) == "" {
			continue
		}
		if _, _, _, err := ParseLoopbackRedirectURI(server.OAuth.RedirectURI); err != nil {
			errors = append(errors, ValidationError{
				Field:   fmt.Sprintf("servers[%d].oauth.redirect_uri", i),
				Message: err.Error(),
			})
		}
	}
	return errors
}

func (c *Config) validateDetailedCore() []ValidationError {
	var errors []ValidationError
	for _, ref := range secret.NewResolver(nil).References(c) {
		if ref.Type != "env" && ref.Type != "secret" {
			errors = append(errors, ValidationError{Field: "secret reference", Message: "unsupported reference type; use ${secret:name} or ${env:NAME}"})
			break
		}
	}

	// Validate listen address format
	if c.Listen != "" {
		// Check for valid format (host:port or :port)
		if !isValidListenAddr(c.Listen) {
			errors = append(errors, ValidationError{
				Field:   "listen",
				Message: "invalid listen address format (expected host:port or :port)",
			})
		}
	}

	// Validate ToolsLimit range
	if c.ToolsLimit < 1 || c.ToolsLimit > 1000 {
		errors = append(errors, ValidationError{
			Field:   "tools_limit",
			Message: "must be between 1 and 1000",
		})
	}

	// Validate ToolResponseLimit
	if c.ToolResponseLimit < 0 {
		errors = append(errors, ValidationError{
			Field:   "tool_response_limit",
			Message: "cannot be negative",
		})
	}

	// Validate timeout
	if c.CallToolTimeout.Duration() <= 0 {
		errors = append(errors, ValidationError{
			Field:   "call_tool_timeout",
			Message: "must be a positive duration",
		})
	}

	// Validate global discovery/health-check intervals.
	if e := validateIntervalBound("init_timeout", c.InitTimeout, time.Second, 30*time.Minute); e != nil {
		errors = append(errors, *e)
	}
	if e := validateIntervalBound("health_check_interval", c.HealthCheckInterval, 5*time.Second, time.Hour); e != nil {
		errors = append(errors, *e)
	}
	if e := validateIntervalBound("tool_discovery_interval", c.ToolDiscoveryInterval, 30*time.Second, 24*time.Hour); e != nil {
		errors = append(errors, *e)
	}

	// HTTP server request deadlines. {0} ∪ [1s, 24h]; 0 means
	// "no deadline", which validateIntervalBound already accepts.
	if e := validateIntervalBound("http_read_timeout", c.HTTPReadTimeout, time.Second, 24*time.Hour); e != nil {
		errors = append(errors, *e)
	}
	if e := validateIntervalBound("http_write_timeout", c.HTTPWriteTimeout, time.Second, 24*time.Hour); e != nil {
		errors = append(errors, *e)
	}
	if e := validateIntervalBound("http_idle_timeout", c.HTTPIdleTimeout, time.Second, 24*time.Hour); e != nil {
		errors = append(errors, *e)
	}

	// Validate code execution configuration (0 means use default)
	if c.CodeExecutionTimeoutMs != 0 && (c.CodeExecutionTimeoutMs < 1 || c.CodeExecutionTimeoutMs > 600000) {
		errors = append(errors, ValidationError{
			Field:   "code_execution_timeout_ms",
			Message: "must be between 1 and 600000 milliseconds (or 0 for default)",
		})
	}

	if c.CodeExecutionMaxToolCalls < 0 {
		errors = append(errors, ValidationError{
			Field:   "code_execution_max_tool_calls",
			Message: "cannot be negative (0 means unlimited)",
		})
	}

	if c.CodeExecutionPoolSize != 0 && (c.CodeExecutionPoolSize < 1 || c.CodeExecutionPoolSize > 100) {
		errors = append(errors, ValidationError{
			Field:   "code_execution_pool_size",
			Message: "must be between 1 and 100 (or 0 for default)",
		})
	}

	if c.CodeExecutionMaxParallel != 0 && (c.CodeExecutionMaxParallel < 1 || c.CodeExecutionMaxParallel > 32) {
		errors = append(errors, ValidationError{
			Field:   "code_execution_max_parallel",
			Message: "must be between 1 and 32 (or 0 for default)",
		})
	}

	// Validate routing mode
	if c.RoutingMode != "" {
		validRoutingModes := map[string]bool{
			RoutingModeRetrieveTools: true,
			RoutingModeDirect:        true,
			RoutingModeCodeExecution: true,
		}
		if !validRoutingModes[c.RoutingMode] {
			msg := fmt.Sprintf("invalid routing mode: %s (must be retrieve_tools, direct, or code_execution)", c.RoutingMode)
			// "schema_deferred" is not a routing mode: the same capability is a
			// composition of an existing mode and the serialization axis.
			// Say so, rather than making the user infer it from a list of three.
			if c.RoutingMode == "schema_deferred" {
				msg += `; "schema_deferred" is not a routing mode — use routing_mode: "direct" with direct_tool_response_mode: "deferred"`
			}
			errors = append(errors, ValidationError{
				Field:   "routing_mode",
				Message: msg,
			})
		}
	}

	// Validate tool response mode. Empty is allowed (= full).
	if c.ToolResponseMode != "" &&
		c.ToolResponseMode != ToolResponseModeFull && c.ToolResponseMode != ToolResponseModeCompact {
		errors = append(errors, ValidationError{
			Field:   "tool_response_mode",
			Message: fmt.Sprintf("invalid tool response mode: %s (must be full or compact)", c.ToolResponseMode),
		})
	}

	// Validate direct-surface response mode. Empty is allowed
	// (= full). "compact" is called out because it is the likeliest wrong value:
	// it is legal on the OTHER axis, so an operator who knows tool_response_mode
	// will reach for it here.
	if c.DirectToolResponseMode != "" &&
		c.DirectToolResponseMode != DirectToolResponseModeFull &&
		c.DirectToolResponseMode != DirectToolResponseModeDeferred {
		errors = append(errors, ValidationError{
			Field: "direct_tool_response_mode",
			Message: fmt.Sprintf("invalid direct tool response mode: %s (must be %q or %q)",
				c.DirectToolResponseMode, DirectToolResponseModeFull, DirectToolResponseModeDeferred),
		})
	}

	// Validate update-check channel. Empty is allowed
	// (resolves to stable); only a non-empty unknown value is invalid.

	// Validate server configurations
	serverNames := make(map[string]bool)
	for i, server := range c.Servers {
		fieldPrefix := fmt.Sprintf("servers[%d]", i)

		// Validate server name
		if server.Name == "" {
			errors = append(errors, ValidationError{
				Field:   fieldPrefix + ".name",
				Message: "server name is required",
			})
		} else if server.Name == ManagementServerName {
			errors = append(errors, ValidationError{Field: fieldPrefix + ".name", Message: "server name mcpproxy is reserved for built-in management tools"})
		} else if serverNames[server.Name] {
			errors = append(errors, ValidationError{
				Field:   fieldPrefix + ".name",
				Message: fmt.Sprintf("duplicate server name: %s", server.Name),
			})
		} else if strings.Contains(server.Name, ":") {
			errors = append(errors, ValidationError{
				Field:   fieldPrefix + ".name",
				Message: fmt.Sprintf("server name %q must not contain ':' (reserved as the server:tool routing separator)", server.Name),
			})
		} else {
			serverNames[server.Name] = true
		}

		// Validate protocol
		validProtocols := map[string]bool{"stdio": true, "http": true, "sse": true, "streamable-http": true, "auto": true}
		if server.Protocol != "" && !validProtocols[server.Protocol] {
			errors = append(errors, ValidationError{
				Field:   fieldPrefix + ".protocol",
				Message: fmt.Sprintf("invalid protocol: %s (must be stdio, http, sse, streamable-http, or auto)", server.Protocol),
			})
		}

		// Validate stdio server requirements
		if server.Protocol == "stdio" || (server.Protocol == "" && server.Command != "") {
			if server.Command == "" {
				errors = append(errors, ValidationError{
					Field:   fieldPrefix + ".command",
					Message: "command is required for stdio protocol",
				})
			}
			// Validate working directory exists if specified
			if server.WorkingDir != "" {
				if _, err := os.Stat(server.WorkingDir); os.IsNotExist(err) {
					errors = append(errors, ValidationError{
						Field:   fieldPrefix + ".working_dir",
						Message: fmt.Sprintf("directory does not exist: %s", server.WorkingDir),
					})
				}
			}
		}

		// Validate HTTP server requirements
		if server.Protocol == "http" || server.Protocol == "sse" || server.Protocol == "streamable-http" {
			if server.URL == "" {
				errors = append(errors, ValidationError{
					Field:   fieldPrefix + ".url",
					Message: fmt.Sprintf("url is required for %s protocol", server.Protocol),
				})
			}
		}

		// Note: OAuth configuration is optional. client_id is optional (uses Dynamic Client Registration RFC 7591 if empty).
		// ClientSecret can be a secret reference, so we don't validate it as empty.

		// Per-server trust_mode. EffectiveTrustMode() fails
		// closed to manual on an unrecognized value, so a hand-edited config
		// carrying a typo ("Scan", "yolo") would otherwise behave as manual while
		// every read surface echoed the typo back as if it were a real mode.
		// Surface it as a validation error instead. Empty = inherit (valid).
		if server.Icon != nil {
			if err := servericon.Validate(*server.Icon); err != nil {
				errors = append(errors, ValidationError{Field: fieldPrefix + ".icon", Message: err.Error()})
			}
		}
		if !IsValidTrustMode(server.TrustMode) {
			errors = append(errors, ValidationError{
				Field: fieldPrefix + ".trust_mode",
				Message: fmt.Sprintf("invalid trust_mode: %q (must be one of: %s — or empty to inherit the default)",
					server.TrustMode, strings.Join(ValidTrustModes(), ", ")),
			})
		}

	}

	// Storage creates new data directories at startup; configuration loading must not create them.
	if c.DataDir != "" && !strings.Contains(c.DataDir, "${") {
		if info, err := os.Stat(c.DataDir); err == nil && !info.IsDir() {
			errors = append(errors, ValidationError{Field: "data_dir", Message: "must be a directory"})
		} else if err != nil && !os.IsNotExist(err) {
			errors = append(errors, ValidationError{Field: "data_dir", Message: err.Error()})
		}
	}

	// Validate logging configuration
	if c.Logging != nil {
		validLevels := map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true}
		if c.Logging.Level != "" && !validLevels[c.Logging.Level] {
			errors = append(errors, ValidationError{
				Field:   "logging.level",
				Message: fmt.Sprintf("invalid log level: %s (must be trace, debug, info, warn, or error)", c.Logging.Level),
			})
		}
	}

	// NOTE: profiles are deliberately NOT validated here —
	// see ValidateDetailed's doc comment for why folding it into this
	// function would run ValidateProfiles twice on every boot/reload and
	// change the boot path's error text.

	return errors
}

// validateIntervalBound enforces the tri-state interval contract: nil is fine (inherit), 0s is fine (disabled), and any other value
// must fall within [min, max]. Returns nil when valid, or a ValidationError
// with a human-readable, actionable message.
func validateIntervalBound(field string, d *Duration, minVal, maxVal time.Duration) *ValidationError {
	if d == nil {
		return nil
	}
	v := d.Duration()
	if v == 0 {
		return nil // explicit "disabled"
	}
	if v < minVal || v > maxVal {
		return &ValidationError{
			Field:   field,
			Message: fmt.Sprintf("must be 0s (disabled) or between %s and %s, got %s", minVal, maxVal, v),
		}
	}
	return nil
}

// isValidListenAddr checks if the listen address format is valid
func isValidListenAddr(addr string) bool {
	// Allow :port format
	if addr != "" && addr[0] == ':' {
		return true
	}
	// Allow host:port format (simple check)
	return addr != "" && (addr[0] != ':' || len(addr) > 1)
}

func (c *Config) Validate() error {
	if errs := c.validateDetailedCore(); len(errs) > 0 {
		return errs[0]
	}
	_, err := ValidateProfiles(c)
	if err != nil {
		return err
	}

	if c.Observability != nil && (c.Observability.UsageCacheTTL <= 0 || c.Observability.UsagePersistInterval <= 0) {
		return fmt.Errorf("observability cache and persistence intervals must be positive")
	}
	return nil
}

// OAuthConfigChanged checks if OAuth configuration has changed between two configs.
// Returns true if any OAuth field differs (ClientID, Scopes, ExtraParams, etc.)
func OAuthConfigChanged(old, new *OAuthConfig) bool {
	// Both nil - no change
	if old == nil && new == nil {
		return false
	}

	// One nil, one not - changed
	if (old == nil) != (new == nil) {
		return true
	}

	// Compare all fields
	if old.Mode != new.Mode ||
		old.ClientID != new.ClientID ||
		old.ClientSecret != new.ClientSecret ||
		old.RedirectURI != new.RedirectURI ||
		old.AuthorizationEndpoint != new.AuthorizationEndpoint ||
		old.TokenEndpoint != new.TokenEndpoint ||
		old.RegistrationEndpoint != new.RegistrationEndpoint ||
		old.AuthServerMetadataURL != new.AuthServerMetadataURL {
		return true
	}

	// Compare scopes (order matters for OAuth)
	if len(old.Scopes) != len(new.Scopes) {
		return true
	}
	for i := range old.Scopes {
		if old.Scopes[i] != new.Scopes[i] {
			return true
		}
	}

	// Compare extra params
	if len(old.ExtraParams) != len(new.ExtraParams) {
		return true
	}
	for key, oldVal := range old.ExtraParams {
		newVal, exists := new.ExtraParams[key]
		if !exists || oldVal != newVal {
			return true
		}
	}

	return false
}

// AdvertiseUpstreamServersEnabled reports the effective
// advertise_upstream_servers value (default true when unset).
func (c *Config) AdvertiseUpstreamServersEnabled() bool {
	return c == nil || c.AdvertiseUpstreamServers == nil || *c.AdvertiseUpstreamServers
}

// ExecutionHost is empty for the primary; peer IDs name execution hosts.
func (sc *ServerConfig) ExecutionHost() string {
	if sc == nil || sc.HostID == nil {
		return ""
	}
	return *sc.HostID
}
func (sc *ServerConfig) SharedWithPeers() bool {
	return sc != nil && sc.AvailableToPeers != nil && *sc.AvailableToPeers
}

func (c *OutputValidationConfig) IsEnabled() bool { return c == nil || c.Mode != "off" }
