package stateview

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
)

// ToolInfo represents a cached tool definition.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
	Annotations *config.ToolAnnotations
	// OutputSchemaJSON is the tool's declared output schema (raw JSON), empty
	// when the tool declares none. Used for output-schema validation.
	OutputSchemaJSON string
}

// ServerStatus represents the runtime status of an upstream server.
type ServerStatus struct {
	Name    string
	Config  *config.ServerConfig
	State   string // Actor state: idle, connecting, connected, error, etc.
	Enabled bool

	Connected      bool
	LastError      string
	LastErrorTime  *time.Time
	ConnectedAt    *time.Time
	DisconnectedAt *time.Time
	RetryCount     int
	ToolCount      int
	Tools          []ToolInfo // Cached tool list for lock-free reads
	// ToolsDiscovered reports that a discovery pass has COMPLETED for this
	// connection and Tools is its authoritative result — including an
	// authoritative EMPTY result for an upstream that lists zero tools. It is
	// stamped by the supervisor when discovery publishes the tool set
	// (RefreshToolsFromDiscovery / RefreshServerToolsFromDiscovery) and
	// cleared together with Tools on disconnect, so a freshly connected server
	// whose discovery has not run yet reads Connected=true, Tools=nil,
	// ToolsDiscovered=false. Tool-identity
	// resolution on it rather than on len(Tools) > 0: the connect→discovery
	// window and a genuinely tool-less server both fail CLOSED instead of
	// admitting any name with the destructive-tier fallback.
	ToolsDiscovered bool
	// DiscoveryEpoch is the live client's connection-instance token
	// (managed.Client.ConnectionEpoch) captured BEFORE the tools/list that
	// produced Tools, stamped together with ToolsDiscovered. Identity
	// resolution compares it with the client's current token: a mismatch
	// means the connection changed since discovery ran — its events dropped
	// or lagging, no reconcile edge observed yet — and the stamp certifies
	// nothing for the live connection. Zero
	// when no discovery has been published for this connection.
	DiscoveryEpoch int64
	// Incarnation is the committed incarnation of the server configuration
	// this status was reconciled from.
	// A same-name replacement carries a different incarnation, so a reader
	// that pairs this status's Tools with a committed incarnation can tell a
	// lagging view of the removed server from the replacement's own. Zero
	// when the configuration snapshot carries no generations.
	Incarnation int64
	Metadata    map[string]interface{}
	// Diagnostic is the most recent classified failure for this server, or nil
	// when the server is healthy (or the last failure has not yet been classified).
	Diagnostic *diagnostics.DiagnosticError

	RetryStopped bool
	// RetryStoppedCode is the stable MCPX_* code that justified stopping.
	RetryStoppedCode string
	// RetryStoppedReason is the human-readable cause, taken from the diagnostics
	// catalog entry for RetryStoppedCode (falling back to the raw error).
	RetryStoppedReason string
}

// ServerStatusSnapshot is an immutable snapshot of all server statuses.
type ServerStatusSnapshot struct {
	Servers   map[string]*ServerStatus
	Timestamp time.Time
}

// View provides a read-only view of server statuses.
// It maintains an in-memory snapshot updated via supervisor events.
type View struct {
	snapshot atomic.Value // *ServerStatusSnapshot
	mu       sync.RWMutex // Protects updates only
}

// New creates a new state view.
func New() *View {
	v := &View{}
	v.snapshot.Store(&ServerStatusSnapshot{
		Servers:   make(map[string]*ServerStatus),
		Timestamp: time.Now(),
	})
	return v
}

// Snapshot returns the current immutable snapshot of all server statuses.
// This is a lock-free read operation.
func (v *View) Snapshot() *ServerStatusSnapshot {
	return v.snapshot.Load().(*ServerStatusSnapshot)
}

// UpdateServer updates or creates a server status entry.
func (v *View) UpdateServer(name string, fn func(*ServerStatus)) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Get current snapshot
	oldSnap := v.snapshot.Load().(*ServerStatusSnapshot)

	// Deep clone the map
	newServers := make(map[string]*ServerStatus, len(oldSnap.Servers))
	for k, vs := range oldSnap.Servers {
		// Clone each status
		newStatus := *vs
		if vs.LastErrorTime != nil {
			t := *vs.LastErrorTime
			newStatus.LastErrorTime = &t
		}
		if vs.ConnectedAt != nil {
			t := *vs.ConnectedAt
			newStatus.ConnectedAt = &t
		}
		if vs.DisconnectedAt != nil {
			t := *vs.DisconnectedAt
			newStatus.DisconnectedAt = &t
		}
		if vs.Tools != nil {
			newStatus.Tools = make([]ToolInfo, len(vs.Tools))
			copy(newStatus.Tools, vs.Tools)
		}
		if vs.Metadata != nil {
			newStatus.Metadata = make(map[string]interface{}, len(vs.Metadata))
			for mk, mv := range vs.Metadata {
				newStatus.Metadata[mk] = mv
			}
		}
		if vs.Diagnostic != nil {
			d := *vs.Diagnostic
			newStatus.Diagnostic = &d
		}
		newServers[k] = &newStatus
	}

	// Get or create the server status
	status, exists := newServers[name]
	if !exists {
		status = &ServerStatus{
			Name:     name,
			Metadata: make(map[string]interface{}),
		}
		newServers[name] = status
	}

	// Apply the update function
	fn(status)

	// Store the new snapshot
	v.snapshot.Store(&ServerStatusSnapshot{
		Servers:   newServers,
		Timestamp: time.Now(),
	})
}

// RemoveServer removes a server from the view.
func (v *View) RemoveServer(name string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Get current snapshot
	oldSnap := v.snapshot.Load().(*ServerStatusSnapshot)

	// Check if server exists
	if _, exists := oldSnap.Servers[name]; !exists {
		return
	}

	// Deep clone the map without the removed server
	newServers := make(map[string]*ServerStatus, len(oldSnap.Servers)-1)
	for k, vs := range oldSnap.Servers {
		if k == name {
			continue
		}
		// Clone each status
		newStatus := *vs
		if vs.LastErrorTime != nil {
			t := *vs.LastErrorTime
			newStatus.LastErrorTime = &t
		}
		if vs.ConnectedAt != nil {
			t := *vs.ConnectedAt
			newStatus.ConnectedAt = &t
		}
		if vs.DisconnectedAt != nil {
			t := *vs.DisconnectedAt
			newStatus.DisconnectedAt = &t
		}
		if vs.Tools != nil {
			newStatus.Tools = make([]ToolInfo, len(vs.Tools))
			copy(newStatus.Tools, vs.Tools)
		}
		if vs.Metadata != nil {
			newStatus.Metadata = make(map[string]interface{}, len(vs.Metadata))
			for mk, mv := range vs.Metadata {
				newStatus.Metadata[mk] = mv
			}
		}
		if vs.Diagnostic != nil {
			d := *vs.Diagnostic
			newStatus.Diagnostic = &d
		}
		newServers[k] = &newStatus
	}

	// Store the new snapshot
	v.snapshot.Store(&ServerStatusSnapshot{
		Servers:   newServers,
		Timestamp: time.Now(),
	})
}

// Count returns the number of servers in the view (lock-free).
func (v *View) Count() int {
	return len(v.Snapshot().Servers)
}
