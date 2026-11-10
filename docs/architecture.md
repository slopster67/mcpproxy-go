# MCPProxy Architecture

This document describes the internal architecture of MCPProxy, including the management service, runtime lifecycle, and event system.

## Management Service Architecture

The management service (`internal/management/`) provides a centralized business logic layer for upstream server management operations, eliminating code duplication across the REST API and MCP interfaces.

### Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                    Client Interfaces                         │
├───────────────┬─────────────────┬───────────────────────────┤
│          REST API           │   MCP Protocol                │
│          (/api/v1/*)        │   (upstream_servers tool)     │
└──────────────┬──────────────┴───────────────┬───────────────┘
               │                              │
               └──────────────┬───────────────┘
                         │
                         ▼
              ┌─────────────────────┐
              │  Management Service │
              │  (internal/mgmt/)   │
              └──────────┬──────────┘
                         │
        ┌────────────────┼────────────────┐
        │                │                │
        ▼                ▼                ▼
  ┌──────────┐    ┌──────────┐    ┌──────────┐
  │ Runtime  │    │  Config  │    │  Events  │
  │Operations│    │  Gates   │    │  Emitter │
  └──────────┘    └──────────┘    └──────────┘
```

### Key Components

- **Service Interface** (`service.go:16-102`): Defines all management operations
  - Single-server: `RestartServer()`, `EnableServer()`, `DisableServer()`
  - Bulk operations: `RestartAll()`, `EnableAll()`, `DisableAll()`
  - Diagnostics: `GetServerHealth()`, `RunDiagnostics()`
  - Server CRUD: `AddServer()`, `RemoveServer()`, `QuarantineServer()`
  - Tool operations: `GetServerTools()`, `TriggerOAuthLogin()`

- **Configuration Gates**: All operations respect centralized configuration guards
  - `disable_management`: Blocks all write operations when true
  - `read_only_mode`: Blocks all configuration modifications

- **Bulk Operations** (`service.go:243-388`): Efficient multi-server management
  - Sequential execution with partial failure handling
  - Returns `BulkOperationResult` with success/failure counts
  - Collects per-server errors in results map
  - Continues on individual failures, reports aggregate results

- **Event Integration**: All operations emit events through event bus
  - `servers.changed`: Notifies UI of server state changes
  - Triggers SSE updates to the web UI
  - Enables real-time synchronization across interfaces

### Benefits

- **Code Deduplication**: 40%+ reduction in duplicate code across interfaces
- **Consistent Behavior**: All interfaces use identical business logic
- **Centralized Validation**: Configuration gates enforced in one place
- **Easier Testing**: Unit tests cover all interfaces through service layer
- **Future Extensibility**: New interfaces can reuse existing service methods

### Usage Examples

```go
// REST API usage (internal/httpapi/server.go:772-866)
mgmtSvc := s.controller.GetManagementService().(ManagementService)
result, err := mgmtSvc.RestartAll(r.Context())
s.writeSuccess(w, result)
```

## Runtime Architecture

The runtime package (`internal/runtime/`) provides core non-HTTP lifecycle management, separating concerns from the HTTP server layer.

### Runtime Package Components

- **Configuration Management**: Centralized config loading, validation, and hot-reload
- **Background Services**: Connection management, tool indexing, and health monitoring
- **State Management**: Thread-safe status tracking and upstream server state
- **Event System**: Real-time event broadcasting for UI and SSE consumers

### Event Bus System

The event bus enables real-time communication between runtime and UI components:

**Event Types**:
- `servers.changed` - Server configuration or state changes
- `config.reloaded` - Configuration file reloaded from disk

**Event Flow**:
1. Runtime operations trigger events via `emitServersChanged()` and `emitConfigReloaded()`
2. Events are broadcast to subscribers through buffered channels
3. Server forwards events to SSE endpoints
4. Web UI receives live updates via `/events` SSE endpoint

**SSE Integration**:
- `/events` endpoint streams both status updates and runtime events
- Automatic connection management with proper cleanup
- JSON-formatted event payloads for easy consumption

### Config Commit

Global deployment settings are parsed from environment variables at startup and cannot be changed by
runtime commits. There is no settings API, JSON configuration file, file watcher or reload pipeline.

Server and profile definitions are canonical records in encrypted `primary.db`.
`Runtime.Commit` serializes entity mutations, rejects global settings changes,
prepares server generations, and commits entity records, OAuth clearing and
client profile reassignment in a single database transaction. Failed transactions
publish nothing. Successful transactions publish the read-only runtime snapshot,
update upstream instances, notify subscribers and schedule connection work.
Entity changes never persist deployment settings.

**Retirement barrier**. Each upstream instance owns a
dial gate (`internal/upstream/dialgate`). Everything the instance sends goes
through it: every TCP dial of its HTTP, SSE and OAuth traffic and every write
on a connection it dialed (a per-instance gated `*http.Transport`), every
process it launches (stdio and the local launcher start in an
admitted section that also registers the process tree with the gate, so
`Retire` kills it; the OAuth browser opener is only admitted, since the
browser it hands the URL to is not the instance's process), every MCP request and notification its mcp-go transport sends (a
capability-preserving `dialgate.WrapTransport`), and every OAuth preflight,
discovery, registration, code exchange and refresh (`oauth.Env` carries the
instance transport). `unregisterLocked` retires the instance before it
returns, so a commit retires a removed, replaced or quarantined instance
before it publishes, and a restart, or `ForceReconnectAll`
retires the old instance before it installs the new one. `Retire` sets the
latch, cancels the gate context, closes tracked connections, kills tracked
process groups, waits for admitted sections that began before the latch (a
write-lock barrier; each section is one leaf operation whose connection it
has already closed, so the wait is bounded), and sweeps again. After it
returns nothing new is launched, dialed or written: a dial, a launch or a
write fails with `ErrRetired` without touching the network, and a tool call
on the retired instance fails as "server unavailable" without marking the
replacement unhealthy. Teardown (the MCP close and the process-group kill) still runs; the manual OAuth flow and token refresh refuse a
retired instance and report it as "server not found". A launched process
whose tree cannot be contained (a Windows Job Object that cannot be created
or assigned) is killed and its start fails. On Windows the child is
created `CREATE_SUSPENDED`, assigned to its Job Object and only then resumed
(`NtResumeProcess`), so it cannot fork a descendant outside the Job. On Unix the launcher keeps an
exited leader unreaped while a descendant it left in its process group runs,
so the group ID stays pinned and the retirement kill (or `Stop`) reaches that
descendant; a group whose leader was reaped is never signalled, since its ID
may have been reused. `scripts/check-upstream-write-sites.sh` also fails on
ungated HTTP in `internal/oauth`, `internal/upstream/core` and
`internal/transport` (allowlist: `scripts/ungated-http-allowlist.txt`).
Lock order: the gate's lock is innermost (`captureMu` → `Manager.mu` →
`Gate.mu` → `Gate.connsMu`) and is held only around one dial, one write or
one process start.

**OAuth credential binding**. Each committed server
view carries an OAuth epoch next to its incarnation: the generation of the
commit that created the name or last cleared its OAuth state because its OAuth
configuration changed. `{Incarnation, OAuthEpoch}` is the server's credential
binding. The commit stamps it on the snapshot, the `upstreams` row and the
manager's desired view in one pass, and the manager binds every instance it
builds to the binding of the generation it was built for (never read from the
row). Every OAuth write and clear an instance makes goes through a storage
method that checks the row's binding in the same bbolt transaction: token
saves (a login's code exchange with its paired client, a refresh), DCR client
saves, completion events, the 5xx token clear, the config-build DCR hygiene
clears and the clears after a rejected refresh or login. The removal purge
deletes the row and the OAuth-config clear raises the row's epoch in the
transaction that deletes the credentials, so an old instance's write that was
already past its network round-trip either committed first (and was deleted)
or finds the row gone or re-bound and writes nothing; a same-name re-add never
sees the old server's tokens. Reads are epoch-filtered only: a bound read
treats a record of another epoch as absent, but does not check the row, so a
removed name stays unreadable because its removal purges the records. Every
write to the `oauth_tokens` bucket takes a new `Revision` from the bucket
sequence, and a binding-conditional clear acts only on the revision its
caller inspected, so a parked clear never erases what a same-binding
replacement (a restart) rewrote. The one exception is the login flow's
stale-DCR clear (`ClearOAuthClientIfBoundAndClientID`): it holds the client
id it sent, not a record it read, so it checks the binding and that client
id; a parked login-path clear can still erase a same-binding rewrite that
kept the same client id. Refresh flights, the terminal latch, the cooldown
and the re-registration state are keyed by `(server key, binding)`, so an
old instance never joins the flight of a replacement with a different
binding (a removal and re-add, or an OAuth-config change) or receives its
token. A same-binding replacement (a restart) shares the key and can join
the old instance's flight; a joiner whose flight failed because its
initiator was retired retries on its own transport rather than taking that
answer. A refresh schedule built from a token save records the binding the
token was saved under, and a reactive flight's terminal outcome is applied
only to a schedule of the flight's binding, checked when that schedule is
selected. A late save notification from an older binding (bindings are
commit generations, so older is known) does not replace the schedule of a
newer one. The exception is a schedule loaded from storage at startup: it
carries the zero binding, so no binding comparison is made for it, and a
terminal outcome of any flight for the server applies to it (the resolver
check that the flight ran under the server's current binding still
applies). Its first token save replaces it with a bound schedule. A token
store guards its 5xx-recovery clear with the revision the refresh flight
itself wrote or read, never a later re-read, so a same-binding rewrite with
the same access token survives the clear. Before it builds an instance the manager also checks that the
stored row still carries exactly the committed view it holds (one bbolt
`View`), so a stale worker builds no OAuth config at all. Logout
stays current-state clears. `scripts/check-upstream-write-sites.sh` fails on an
unconditional OAuth mutator in `internal/upstream` or `internal/oauth`. No
lock edge is added: the checks are bbolt leaves taken with no runtime lock
held, and the coordinator's mutex stays a leaf.

**Incarnation-bound approvals**. Each server has an
incarnation that changes only when its name is removed and created again.
Every approval writer (tool approve/block/toggle, the unquarantine baseline
promotion, prompt approvals, the scanner's baseline and blocks) captures the
incarnation when it starts — or takes the client's `expected_incarnation` —
and re-checks it against the stored row under the server's approval lock,
the lock the removal purges under. A mismatch writes nothing and returns
`ErrServerReplaced` (REST 409 `server_replaced`). Scanner approval
unquarantines through a commit with `ExpectIncarnation`. Asynchronous
writers that can outlive a removal (scan job and report saves, identity
registration, tool-call history) use storage methods that check the row's
incarnation in the same bbolt transaction as the write. A discovery pass makes its final eligibility check (stored row
present, enabled, not quarantined, generation unchanged) and its index writes
in one hold of the server's approval lock, and approval policy is read from
storage, so a pass can never re-index a server a commit has just quarantined
or removed.

**Lock order** (outermost first): `configCommitMu` → server approval locks
(sorted; released before step 7) → `storage.Manager.mu`/bbolt; `configCommitMu`
→ `upstream.Manager.captureMu` (W) → `Manager.mu` in step 7; `configCommitMu`
→ `configsvc` update lock → `Runtime.mu`. Code holding any of the inner locks
must never call `Commit`. `scripts/check-upstream-write-sites.sh` (CI) fails
when an `upstreams` row is written anywhere but the storage package and
`internal/runtime/commit*.go`.

### Runtime Lifecycle

**Initialization**:
1. Runtime created with config, logger, and manager dependencies
2. Background initialization starts server connections and tool indexing
3. Status updates broadcast through event system

**Background Services**:
- **Connection Management**: Periodic reconnection attempts with exponential backoff
- **Tool Indexing**: Automatic discovery and search index updates every 15 minutes
- **Configuration Sync**: File-based config changes trigger runtime resync

**Shutdown**:
- Graceful context cancellation cascades to all background services
- Upstream servers disconnected with proper subprocess cleanup
- Resources closed in dependency order (upstream → cache → index → storage)

**Subprocess Shutdown Flow**:
1. **Graceful Close** (10s timeout): Close MCP client connection, wait for subprocess to exit cleanly
2. **Force Kill** (9s timeout): If graceful close fails, send SIGTERM to process group, poll for exit, then SIGKILL

| Timeout | Value | Purpose |
|---------|-------|---------|
| MCP Client Close | 10s | Wait for graceful stdin/stdout close |
| SIGTERM → SIGKILL | 9s | Time between graceful and force kill |

See [Shutdown Behavior](operations/shutdown-behavior.md) for detailed documentation.

## Related Documentation

- [Configuration](configuration.md) - Full configuration reference
