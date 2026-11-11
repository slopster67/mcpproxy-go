---
id: rest-api
title: REST API
sidebar_label: REST API
sidebar_position: 1
description: MCPProxy REST API reference
keywords: [api, rest, http, endpoints]
---

# REST API

MCPProxy's REST API serves one audience: the authenticated admin dashboard.
Every route returns stored values as written (no masking); secret references
such as `${secret:x}` stay references. MCP clients manage MCPProxy through the
`manage_*` MCP tools instead, which keep their own authorization and masking.
The design contract is [docs/dev/dashboard-api.md](../dev/dashboard-api.md).

## Authentication

The REST API is the dashboard interface and requires an OIDC browser session.
Start at `/auth/login`, complete the provider sign-in and use the returned
HttpOnly session cookie. `/auth/session` returns the signed-in user and CSRF token;
write requests must send that token in `X-CSRF-Token`. `/auth/logout` is a
CSRF-protected POST. The UI manages this flow automatically.

API keys, Authorization bearer tokens, MCP client tokens and peer tokens cannot
authenticate REST. The change stream and activity exports use the same session cookie;
credentials never appear in their URLs. Trusted agents use MCP management tools.
See [OIDC setup](../features/dashboard-oidc.md).

## Base URL

```
http://127.0.0.1:8080/api/v1
```

## Request ID Tracking

All API responses include an `X-Request-Id` header for request tracing and log correlation. This is useful for debugging issues and correlating errors with server logs.

### Request Header

You can optionally provide your own request ID:

```bash
curl -b "$DASHBOARD_COOKIES" \
     -H "X-Request-Id: my-custom-id-123" \
     http://127.0.0.1:8080/api/v1/servers
```

**Validation rules:**
- Pattern: `^[a-zA-Z0-9_-]{1,256}$`
- Max length: 256 characters
- If missing or invalid, MCPProxy generates a UUID v4

### Response Header

Every response includes the request ID:

```
X-Request-Id: my-custom-id-123
```

### Error Responses

Error responses include the `request_id` in the JSON body for easy correlation:

```json
{
  "success": false,
  "error": "server 'nonexistent' not found",
  "request_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
}
```

### Log Correlation

Use the request ID to find related activity logs:

```bash
curl "http://127.0.0.1:8080/api/v1/activity?request_id=a1b2c3d4-e5f6-7890-abcd-ef1234567890"
```


## Entities and routes

Every entity uses one shape. Lists answer `{"<entity>": [...]}` inside the
usual `{success, data}` envelope; a single object is the `data` itself, in the
same shape as a list element. Create and PATCH return the full object; DELETE
returns `{"deleted": "<id>"}` (profiles add `moved`). Only operations that are
not field writes are action routes.

| Method and path | Purpose |
|---|---|
| `GET /api/v1/servers` | List servers (`{servers, stats}`) |
| `POST /api/v1/servers` | Create a server; returns the server object |
| `GET /api/v1/servers/{id}` | One server object |
| `PATCH /api/v1/servers/{id}` | Partial config update (incl. `enabled`); returns the server object |
| `DELETE /api/v1/servers/{id}` | Remove a server |
| `POST /api/v1/servers/{id}/restart` | Restart; returns the server object |
| `POST /api/v1/servers/{id}/login` | Start OAuth sign-in |
| `POST /api/v1/servers/{id}/logout` | Sign out; returns the server object |
| `POST /api/v1/servers/{id}/config-to-secret` | Move a header/env value into the secret store |
| `GET /api/v1/servers/{id}/logs` | Recent log lines |
| `GET /api/v1/servers/{id}/oauth/discovered` | Discovered OAuth values |
| `POST /api/v1/servers/{id}/tools/approve` | Approve reviewed tool definitions (hash-checked) |
| `POST /api/v1/servers/{id}/tools/block` | Approve and disable tools |
| `GET /api/v1/tools` | Every tool of every server (view-as: `profile`, `client`; `server`) |
| `GET /api/v1/tools/{id}` | One tool; `id` is `server:tool`, URL-encoded |
| `PATCH /api/v1/tools/{id}` | Set `enabled` and/or `classification`; returns the tool |
| `GET /api/v1/index/search` | Ranked tool search |
| `GET /api/v1/clients` | List clients (`{clients, warnings}`) |
| `POST /api/v1/clients` | Create a client; returns the client plus its credential (once) |
| `GET /api/v1/clients/{id}` | One client (with `sessions`) |
| `PATCH /api/v1/clients/{id}` | Binding fields; returns the client |
| `POST /api/v1/clients/{id}/rotate` | New credential; returns the client plus the credential (once) |
| `DELETE /api/v1/clients/{id}` | Delete a client and its credential |
| `GET /api/v1/peers` | List peers |
| `POST /api/v1/peers` | Create a peer; returns the peer plus its token (once) |
| `GET /api/v1/peers/{id}` | One peer |
| `POST /api/v1/peers/{id}/rotate` | New token; returns the peer plus the token (once) |
| `DELETE /api/v1/peers/{id}` | Delete an empty peer |
| `GET /api/v1/profiles` | List profiles |
| `POST /api/v1/profiles` | Create a profile; returns it |
| `GET /api/v1/profiles/{id}` | One profile |
| `PATCH /api/v1/profiles/{id}` | Partial update, `name` included; returns the profile |
| `DELETE /api/v1/profiles/{id}` | Delete (`reassign_to`, `force`) |
| `GET /api/v1/profiles/{id}/effective-tools` | Access verdict of every tool under the profile |
| `GET /api/v1/access/explain` | Why a client/profile can or cannot use a tool |
| `GET /api/v1/secrets` | Stored and referenced secrets (never values) |
| `POST /api/v1/secrets` | Store a secret; returns its list element |
| `PATCH /api/v1/secrets/{name}` | Replace a stored secret's value; returns its list element |
| `DELETE /api/v1/secrets/{name}` | Delete a stored secret |
| `GET /api/v1/sessions` | Recent MCP sessions |
| `GET /api/v1/sessions/{id}` | One session |
| `GET /api/v1/activity` | Activity records |
| `GET /api/v1/activity/{id}` | One activity record |
| `GET /api/v1/usage` | Usage aggregate |
| `GET /api/v1/events` | Change stream (Server-Sent Events) |

## Endpoints

### Servers

Every route below serves the authenticated admin dashboard and works with one
**server object**: the stored server configuration exactly as written —
the same JSON field names `PATCH` accepts (`url`, `protocol`, `command`,
`args`, `working_dir`, `env`, `headers`, the full `oauth` block including
`mode` and `client_secret`, `enabled`, `trust_mode`, `host_id`,
`available_to_peers`, `icon`, `description`, …) — flattened together with read-only runtime
fields. Values are not masked and secret references such as
`${secret:name}` stay references (they are not resolved). Responses carrying
server objects use `Cache-Control: no-store`.

Read-only runtime fields:

| Field | Description |
|-------|-------------|
| `id` | The server name |
| `connected`, `connecting`, `status`, `last_error`, `reconnect_count` | Connection state |
| `tool_count` | Tools the server currently lists |
| `health` | Unified health status (see below) |
| `diagnostic`, `error_code` | Classified failure, when failed ([error codes](../errors/README.md)) |
| `retry_stopped`, `retry_stopped_code`, `retry_stopped_reason` | Automatic reconnection given up for good |
| `oauth_active` | OAuth is in use: `oauth.mode` is `on`, or automatic with a stored token or an OAuth-required error |
| `oauth_mode` | Effective OAuth mode (`auto`, `on`, `off`); remote servers only |
| `oauth_status`, `authenticated`, `token_expires_at`, `user_logged_out` | OAuth sign-in state |
| `quarantine` | `{pending_count, changed_count, blocked_count}` tool review counters, omitted when all zero |
| `last_used_at` | Time of the server's latest tool call in the retained activity log; omitted when it has none |
| `generation`, `incarnation` | Committed identity (see [Removal and incarnation](#removal-and-incarnation)) |
| `host_name`, `host_online` | Execution host |
| `icon_url`, `icon_uploaded` | Effective icon |

The MCP `manage_servers` tool returns the same object with secret-bearing
fields masked (headers, env, URL credentials, argv tokens, `oauth` secrets);
its `patch` and `create` operations revert or refuse echoed masks. The REST
API applies no masking and no mask-echo checks: values sent are stored as
sent.

#### GET /api/v1/servers

List all upstream servers: `{"servers": [<server object>...], "stats": {...}}`.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `profile` | string | Restrict to the named profile's effective servers. Each row's `tool_count` becomes the number of that server's tools **visible** under the profile, and `stats` are recomputed over the returned rows. An unknown profile returns `404 profile not found`. `-` returns `400`. `client` and `token` are not server filters and return `400 unsupported_scope_filter` |

**Response:**
```json
{
  "success": true,
  "data": {
    "servers": [
      {
        "id": "github-server",
        "name": "github-server",
        "url": "https://api.example.com/mcp",
        "protocol": "http",
        "headers": {"Authorization": "${secret:github}"},
        "oauth": {"mode": "on", "client_id": "registered-client"},
        "enabled": true,
        "connected": true,
        "status": "ready",
        "tool_count": 15,
        "oauth_active": true,
        "authenticated": true,
        "oauth_status": "authenticated",
        "generation": 42,
        "incarnation": 17,
        "health": {
          "level": "healthy",
          "admin_state": "enabled",
          "summary": "Connected (15 tools)",
          "status": "ready",
          "usable": true,
          "actions": []
        }
      }
    ],
    "stats": {"total_servers": 1, "connected_servers": 1, "total_tools": 15}
  }
}
```

**Health Object Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `level` | string | Severity signal for badge coloring only: `healthy`, `degraded`, or `unhealthy`. Render `status` as text instead |
| `admin_state` | string | Admin state: `enabled` or `disabled` |
| `summary` | string | Human-readable status message |
| `detail` | string | Optional additional context about the status |
| `action` | string | Suggested remediation, always equal to `actions[0]` (or empty when `actions` is empty): `login`, `restart`, `enable`, `approve`, `view_logs`, `set_secret`, `configure`, `edit_url`, or empty |
| `status` | string | The status vocabulary rendered as text: `ready`, `connecting`, `sign_in_required`, `needs_secret`, `needs_config`, `error`, `disabled` |
| `usable` | boolean | True only when `status == "ready"` — whether the server can currently serve tool calls |
| `actions` | string[] | Every applicable next step, in priority order: `login` > `set_secret` > `configure` > `edit_url` > `approve` > `restart` > `view_logs` > `enable` |

#### GET /api/v1/servers/{name}

One server object, the same shape as a list element. `404` when the server is
not configured.

#### POST /api/v1/servers

Create a server. The body takes the writable fields of the server object
(`name` plus either `url` or `command`); `protocol` defaults to `stdio` for a
command and `streamable-http` for a URL, `enabled` defaults to `true`. Returns
the created server object. `409` when the name already exists.

#### PATCH /api/v1/servers/{name}

Partial update of an existing upstream server; returns the updated server
object. All request fields are optional; omitted fields are preserved as-is.
`enabled` is a field here — there are no enable/disable routes.

The map-typed fields `headers` and `env` follow **JSON Merge Patch
([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396))** semantics:

| Value in patch body | Effect on stored map |
|---|---|
| key present with a non-null string value | upsert (add or replace that key) |
| key present with JSON `null` | delete that key |
| key absent from the patch body | preserve as-is |

This is the same convention the MCP `manage_servers patch` operation uses.

**Request body** ([`AddServerRequest`](https://github.com/smart-mcp-proxy/mcpproxy-go/blob/main/internal/httpapi/server.go) — all fields optional):

```json
{
  "url": "https://api.example.com/mcp",
  "command": "uvx",
  "args": ["mcp-server-foo"],
  "env": {"API_KEY": "new-value", "OLD_VAR": null},
  "headers": {"X-Trace": "on", "X-Stale": null},
  "working_dir": "/path/to/dir",
  "protocol": "http",
  "enabled": true,
  "trust_mode": "auto",
  "oauth": {"mode": "on", "client_id": "registered-client-id", "scopes": ["read"], "token_endpoint": null}
}
```

**Notes:**

- Empty string `""` is **set-to-empty**, NOT delete. JSON Merge Patch is
  explicit about this — only the JSON `null` token deletes.
- Boolean fields use pointer-style semantics: absent = preserve, present =
  explicit value. Server trust is configured through `trust_mode` (`manual` or
  `auto`).
- `oauth`: omitted OAuth fields remain unchanged; `null` removes that field's
  override and restores automatic resolution. Resetting the last override
  restores automatic OAuth detection. Supported fields: `mode`, `client_id`,
  `client_secret` (a literal or `${secret:name}`), `scopes`, `extra_params`,
  `redirect_uri` (a fixed loopback callback), `callback_base_url`,
  `authorization_endpoint`, `token_endpoint`, `registration_endpoint`, and
  `auth_server_metadata_url`. PKCE remains enabled.

#### DELETE /api/v1/servers/{name}

Remove a server (see [Removal and incarnation](#removal-and-incarnation)).
Returns `{"deleted": "<name>"}`.
`404` when the server is not configured.

#### GET /api/v1/servers/{name}/oauth/discovered

Returns `{ automatic, discovery_error? }`: the OAuth values discovery resolves for
the server (endpoints, scopes, resource parameter, registered client ID).
Responses use `Cache-Control: no-store`. Discovery runs on the server's
execution host and never signs in, registers an OAuth client or starts an MCP
connection. The configured overrides are the server object's `oauth` block.
The `manage_servers` MCP tool exposes the same values through its
`oauth_settings` operation.

#### POST /api/v1/servers/{name}/config-to-secret

Atomically move a literal header or env value into the encrypted primary
database. The backend reads the value from the loaded config, stores it in the
secret store under `secret_name`, and rewrites the config field with
`${secret:<secret_name>}`.

**Request body:**

```json
{
  "scope": "header",
  "key": "Authorization",
  "secret_name": "synapbus-auth"
}
```

| Field | Type | Description |
|---|---|---|
| `scope` | string | `header` or `env` |
| `key` | string | The key on the server's headers / env map |
| `secret_name` | string | Name to store the value under in the encrypted primary database |

**Response (200 OK):**

```json
{
  "success": true,
  "data": {
    "message": "header \"Authorization\" on \"synapbus\" now references stored secret \"synapbus-auth\"",
    "reference": "${secret:synapbus-auth}"
  }
}
```

**Failure cases:**

| Status | Cause |
|---|---|
| 400 | Missing `scope` / `key` / `secret_name`, invalid scope, value is already a `${secret:…}` or `${env:…}` reference, or value is empty |
| 404 | Server or key not found |
| 500 | Secret resolver unavailable, secret store store failed, or config update failed |

#### GET /api/v1/servers/{name}/logs

The server's recent log lines; `tail` (default 100) limits the count.

#### POST /api/v1/servers/{name}/restart

Restart a server. Returns the server object after the restart; a restart that
ends needing OAuth sign-in is a success and the object says so.

#### POST /api/v1/servers/{name}/login

Initiate OAuth authentication flow for a server.

**Response (200 OK):**
```json
{
  "success": true,
  "data": {
    "success": true,
    "server_name": "github-server",
    "correlation_id": "a1b2c3d4e5f6789012345678",
    "browser_opened": true,
    "message": "OAuth authentication started for server 'github-server'. Please complete authentication in browser."
  }
}
```

**OAuthStartResponse Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `success` | boolean | Always `true` for successful initiation |
| `server_name` | string | Name of the server being authenticated |
| `correlation_id` | string | Unique ID for tracking this OAuth flow |
| `auth_url` | string | Authorization URL (for manual browser opening) |
| `browser_opened` | boolean | Whether browser was automatically opened |
| `browser_error` | string | Error message if browser opening failed |
| `message` | string | Human-readable status message |

**Error Response (400 Bad Request):**

OAuth errors return structured error responses for better debugging:

```json
{
  "success": false,
  "error_type": "dcr_failed",
  "server_name": "github-server",
  "message": "Dynamic Client Registration failed: 403 Forbidden",
  "suggestion": "Check if the OAuth server requires pre-registered clients",
  "correlation_id": "a1b2c3d4e5f6789012345678",
  "request_id": "req-xyz-123",
  "details": {
    "metadata": {
      "protected_resource_url": "https://api.example.com/.well-known/oauth-protected-resource",
      "authorization_server_url": "https://auth.example.com/.well-known/oauth-authorization-server",
      "status": "ok"
    },
    "dcr": {
      "attempted": true,
      "status": "failed",
      "error": "403 Forbidden"
    }
  },
  "debug_hint": "Open the Logs tab for server \"github-server\"."
}
```

**OAuthFlowError Fields:**

| Field | Type | Description |
|-------|------|-------------|
| `error_type` | string | Error category: `client_id_required`, `dcr_failed`, `metadata_discovery_failed`, `code_flow_failed` |
| `server_name` | string | Name of the server |
| `message` | string | Human-readable error description |
| `suggestion` | string | Actionable remediation hint |
| `correlation_id` | string | Flow tracking ID |
| `request_id` | string | HTTP request ID for log correlation |
| `details` | object | Diagnostic details (metadata status, DCR status) |
| `debug_hint` | string | Where to find the server's logs |

#### POST /api/v1/servers/{name}/logout

Clear OAuth tokens and disconnect a server. Returns the server object.

#### Removal and incarnation

Removing a server (`DELETE /api/v1/servers/{id}`, MCP `manage_servers delete`,
or the Web UI delete) is one commit: the server leaves the live configuration
and **everything stored under its name is deleted in the same storage
transaction**: tool and prompt approvals, OAuth tokens and DCR client
registration (exact key match, so removing `foo` never touches `foo_bar`), the
integrity baseline, tool-call history and identities, and tool hashes, together
with its search-index entries and refresh schedule. A server added again under
the same name inherits none of those records: no approval and no stored OAuth
token. Under the default manual trust its tools wait for review; with
`trust_mode: "auto"` its tool discovery records a fresh approved baseline.
Activity log removal records carry the per-bucket counts in `metadata.purged`.

OAuth credential writes are bound to the server's incarnation and OAuth
configuration: a token save, login completion or DCR
registration that the removed server's connection had already started is
discarded when it lands, so a same-name server added right after it starts
signed out.

Every server payload (`GET /api/v1/servers`, the single-server routes and the
review payload) carries two committed identifiers:

- `generation` (int64) changes whenever the server's configuration changes.
- `incarnation` (int64) changes only when the name is removed and created
  again; ordinary edits keep it.

The displayed configuration and these identifiers always come from the same
committed state, so a payload never shows one server's command or URL with
another server's `incarnation`. The tool listings carry the identity of the
server their rows were read for: the tool rows of `GET /api/v1/tools` carry
`server_incarnation`. Echo the value of the rows you acted on as
`expected_incarnation`. A server that was removed and not yet re-added is not
listed: `GET /api/v1/servers` and `GET /api/v1/tools` omit it. If the server is
removed or replaced while a listing reads its rows, the listing reads them
again; if it keeps changing, `GET /api/v1/tools` lists it under
`failed_servers`.

The approval writes — `tools/approve`, `tools/block` and `PATCH /tools/{id}`
— accept an optional `"expected_incarnation"`: the
`incarnation` of the server the decision was made on. If the server was
removed (and possibly re-added) since, nothing is written and the call answers:

```http
HTTP/1.1 409 Conflict

{"success": false, "error": "server_replaced: server 'github' was removed or replaced since it was reviewed"}
```

Without the field the call is bound to the incarnation current when it
arrives, so a removal that lands while the call is in flight still refuses it.
A call for a server that is not configured answers `404`. An unrelated edit of
the server (for example a header) never refuses an approval.

### Tool approval

#### POST /api/v1/servers/{name}/tools/approve

Approve pending or changed tools for a server. See [Tool review](../features/tool-quarantine.md) for details.

**Request Body:** name the tools, or send `"approve_all": true` for every
pending and changed tool. `expected_hashes` is required and binds the approval
to the review the client showed (see [Review-bound approvals](#review-bound-approvals)):
```json
{
  "tools": ["create_issue"],
  "expected_hashes": {"create_issue": "rv2-3f1c..."},
  "expected_incarnation": 17
}
```
With `tools`, every requested tool that has a record must still match its
fingerprint; with `approve_all`, every pending and changed tool must. A
mismatch answers `409 review_out_of_date` and approves nothing.
Either form also takes an optional `"expected_incarnation"`
([Removal and incarnation](#removal-and-incarnation)). The two bindings are
independent and may be sent together: a server removed and re-added since the
review answers `409 server_replaced`, changed definitions answer
`409 review_out_of_date`, and neither writes anything.

**Response:**
```json
{
  "success": true,
  "data": {
    "approved": 2,
    "tools": ["create_issue", "delete_repo"],
    "message": "Approved 2 of 2 requested tools for server github-server",
    "approved_count": 40,
    "blocked_count": 3,
    "still_pending": 0,
    "still_changed": 0
  }
}
```

The response is read back from the stored approval records after the write, so
it reports what actually applied:

- `approved`: requested tools whose record now reads approved (with
  `approve_all`, the number of records approved). A requested name with no
  record, or one that stayed held, is listed in `not_approved`.
- `approved_count` / `blocked_count`: the server's approved tools that are
  enabled / disabled (blocked).
- `still_pending` / `still_changed`: tools on the server still held for review,
  named (sorted, at most 50) in `held_tools`. A tool that changed or appeared
  after the decision is held here by design.
- `outcome_unavailable: true` (no counts; `approved` is 0 for named tools and
  the write count for `approve_all`): the records could not be read back, so
  the remaining holds are unknown; check `GET /api/v1/tools?server={id}`.

#### POST /api/v1/servers/{name}/tools/block

Atomically **block** tools = approve **and** disable them in a single server-side
operation. Use this to acknowledge a pending/changed tool while keeping it hidden from MCP clients. The approve and
disable land in one write per tool, so a tool is never left in the
approved+enabled state.

**Request Body:**
```json
{
  "tools": ["create_issue", "delete_repo"]
}
```

Or block all pending/changed tools:
```json
{
  "block_all": true
}
```

**Response:**
```json
{
  "success": true,
  "data": {
    "blocked": 2,
    "tools": ["create_issue", "delete_repo"],
    "message": "Blocked 2 tools for server github-server"
  }
}
```

Returns `400` if neither `tools` nor `block_all` is provided.


### Tools

#### GET /api/v1/tools

Optional `server` query parameter limits the catalogue and its statistics to one
server.

Global tools overview: every tool from **every** configured
server — including disabled servers and individually disabled tools —
enriched with approval state and 30-day usage. Read-only; consumers apply their own
search/filter/sort over the full set. For relevance-ranked discovery use
`GET /api/v1/index/search` instead.

**Query Parameters (view-as):**

| Parameter | Type | Description |
|-----------|------|-------------|
| `client` | string | Show what this client would see and be able to call. The client's credential, binding and current state are resolved exactly as its connection resolves them. An unknown client gets `404 client not found` |
| `profile` | string | Show what this profile would expose. An unknown profile returns `404 profile not found` |

`client` and `profile` are mutually exclusive (`400 use either client or profile, not both`), `-` is not valid here (`400`), and `token` is not a tools filter (`400 unsupported_scope_filter`).

With either parameter, rows gain `access {visible, callable, reason}`. Visible
means the profile, server/host scope, and tool selection allow the tool. Callable
also requires the server connection and tool approval checks to pass. The first
failure is reported as `profile`, `server_in_scope`, `tool_rule`, `server_state`,
`tool_approval`, `server_not_in_profile`, or `not_selected`. HTTP token
validation is a prerequisite, not a preview check. Hidden rows are kept for
inspection.


`stats` are recomputed over the returned rows. Without `client` or `profile` the response is unchanged.

**Response:**
```json
{
  "success": true,
  "data": {
    "tools": [
      {
        "name": "create_issue",
        "server_name": "github",
        "description": "Create a new GitHub issue",
        "approval_status": "approved",
        "disabled": false,
        "usage": 42,
        "last_used": "2026-05-18T09:30:51Z"
      }
    ],
    "stats": { "total": 478, "enabled": 450, "disabled": 28, "pending_approval": 0 },
    "partial": false,
    "failed_servers": []
  }
}
```

`enabled` is derived by the consumer as `!disabled`. When a
server cannot be read the endpoint still returns every tool it could gather and
sets `partial: true` with `failed_servers` (it does not fail the whole request).

Each row carries `id` (`server:tool`) and, for an approved tool, its current
schema-hash pin in `hash` (`sha256/v{N}:{hex}`) and `classification_hash`.

#### GET /api/v1/tools/{id}

One tool row; `id` is `server:tool`, URL-encoded (`github%3Acreate_issue`).
Only the first colon separates the server, so raw tool names with colons
(`ns:erase`) work. `404` when the tool is unknown.

#### PATCH /api/v1/tools/{id}

Writes the tool's fields and returns the updated row:

```json
{"enabled": false, "classification": "write", "expected_hash": "…", "expected_incarnation": 17}
```

| Field | Meaning |
|---|---|
| `enabled` | Show or hide the tool from MCP clients |
| `classification` | Manual tier of an unannotated tool: `read`, `write`, `destructive`, or `""` to clear. Requires `expected_hash` = the row's `classification_hash`; a stale hash or an annotated tool answers `409` |
| `expected_incarnation` | Optional; see [Removal and incarnation](#removal-and-incarnation) |

An empty patch answers `400`.


### Review

A tool awaiting review (`approval_status` `pending` or `changed`) carries a
`review` object in its `GET /api/v1/tools` row: the current definition, the
`previous` approved definition and a `diff` for a changed tool, the
`default_allowed` selection, and `current_hash`, the fingerprint an approval
binds to. A tool awaiting review that the server no longer lists still gets a
row (`cached: true`). The review badge is the sum of each server's
`quarantine.pending_count` and `quarantine.changed_count`.

#### Review-bound approvals {#review-bound-approvals}

An approval is bound to the review the operator saw, so a tool definition
that changes between reading its review and the approval is never approved unseen.
`POST .../tools/approve` requires `expected_hashes`, an object mapping tool name
to the `current_hash` of the review; without it the request answers `400`:

| `expected_hashes` | Meaning |
|---|---|
| `{}` | Bound to the empty snapshot: any tool the approval would make callable fails it (use it for an empty review) |
| `{"name": "rv2-...", ...}` | Bound: every tool the approval makes callable must be listed with an equal fingerprint. Extra names are ignored |

For a server approval the checked set is every pending tool and every enabled
approved tool, minus `block`; a `block` target that no longer exists is out of
date too. The check, the integrity baseline, the blocks, the promotion of the
pending tools run as one step under the server's tool-approval lock. On a
mismatch nothing is written: no integrity baseline, no record, no baseline
marker and no event.

```json
HTTP/1.1 409 Conflict
{
  "success": false,
  "error": "tool review for server 'notes2' is out of date (definition changed since review: read_note; not in the review: drop_db); nothing was approved — fetch the review again",
  "error_code": "review_out_of_date",
  "stale_tools": {"changed": ["read_note"], "unreviewed": ["drop_db"], "missing_blocks": []},
  "request_id": "..."
}
```

The message always contains "out of date". Clients reload the review and show
the message; they do not offer a force approval.


### Clients

#### GET /api/v1/clients

`{clients: [...], warnings: [...]}`. A client row carries `id`, `display_name`,
`kind`, `state`, `connected`, `last_seen`, `active_sessions`, `calls_24h`,
`blocked_24h`, `host_id`, `allow_managing`, `allow_code_execution`,
`credential_state`, `token_name`, `profile`, `profile_mode`, `profile_source`,
`profile_state`, `profile_missing` and `issuer`.

| Query | Meaning |
|-------|---------|
| `profile` | Rows whose active credential is bound to this profile; `-` = bound to All servers |
| `client` | Exact client id |

`warnings[]` items are `{code, severity, client_id?, message, action?, bindings?, fixes?}`.

#### GET /api/v1/clients/{id}

The row plus `sessions[]` (the latest 20). This is the object change events
carry for `clients`.

#### POST /api/v1/clients

Body `{display_name, host_id?, profile?, mode?, allow_managing?, allow_code_execution?}`.
Returns `201` with the client object plus `credential` and `snippet`. The
credential is shown once; the response is `Cache-Control: no-store`.

#### PATCH /api/v1/clients/{id}

Partial update: `display_name` (rename), `profile` (`""` = All servers; omitted keeps
the current one), `mode` (`locked` or `switchable`), `host_id`,
`allow_managing`, `allow_code_execution`. Returns the client object. Writes
run the binding guard (`409 binding_bypassable_without_auth`).

#### POST /api/v1/clients/{id}/rotate

Replaces the client's token immediately. Returns the client object plus the
new `credential` and `snippet` (no-store).

#### DELETE /api/v1/clients/{id}

Deletes the client and its credential: `{"deleted": "<id>"}`.

### Peers

A peer object is `{id, name, created_at, last_seen?, online, address?}`.

| Route | Answer |
|---|---|
| `GET /api/v1/peers` | `{peers: [...]}` |
| `GET /api/v1/peers/{id}` | The peer |
| `POST /api/v1/peers` | Body `{name}`; the peer plus `token` (shown once, no-store) |
| `POST /api/v1/peers/{id}/rotate` | The peer plus its new `token` (no-store); the old token stops working and the peer is disconnected |
| `DELETE /api/v1/peers/{id}` | `{"deleted": "<id>"}`; `409` while servers or clients still use the peer |

### Profiles

Every write records one `profile_change` activity record. A write that would
let a bound client escape its profile is refused `409
binding_bypassable_without_auth` and writes nothing.

#### GET /api/v1/profiles

`{profiles: [...]}`. A profile has `id`, `name`, `servers`, optional
`selected_tools` and `switchable_to`, `effective_servers`, `tool_counts`,
`calls_24h`, `blocked_24h` and `used_by`.

#### GET /api/v1/profiles/{id}

One profile; `404` when unknown.

#### POST /api/v1/profiles

Body: the profile fields without `id` (the backend generates it). Returns
`201` with the profile.

#### PATCH /api/v1/profiles/{id}

Partial update: any of `name`, `servers`, `selected_tools`, `switchable_to`
(`null` clears the last two). `id` may be sent only when it equals the path
(`409 id_mismatch` otherwise); unknown fields answer `400`. A patch that only
changes `name` is recorded as a rename. Returns the profile.

#### DELETE /api/v1/profiles/{id}

Query `reassign_to=<profile ID>` or `force=true`. Without either option, an
assigned profile returns `409 profile_in_use`. Returns `{deleted, moved: {clients}}`.

#### GET /api/v1/profiles/{id}/effective-tools

Query `client`, `server`, `reason`. Evaluates the profile's server/tool
selections and returns an access verdict per tool.

### Secrets

#### GET /api/v1/secrets

`{secrets: [...]}`: every stored secret plus every secret or environment
variable a server config references. Values are never returned.

| Field | Meaning |
|---|---|
| `id` | `type:name`; `env:name@host` for an environment variable read on a peer |
| `name`, `type` | `type` is `secret` (secret store) or `env` |
| `reference` | The reference to put in a config, e.g. `${secret:github}` |
| `servers` | Servers that reference it (empty for a stored, unused secret) |
| `host_id`, `host_name` | Peer that reads an environment variable |
| `is_set` | The value exists |
| `available` | `false` when the state is unknown (the peer is offline) |

#### POST /api/v1/secrets

Body `{name, value, type?}` (only `type: "secret"` is writable). Stores the
secret and returns its list element.

#### PATCH /api/v1/secrets/{name}

Body `{value}`; query `type` defaults to `secret`. Replaces the stored value
and returns the list element.

#### DELETE /api/v1/secrets/{name}

Query `type` defaults to `secret`. Returns `{"deleted": "<name>"}`.

Servers that reference a changed secret are restarted.


### Sessions

#### GET /api/v1/sessions

List recent MCP sessions.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `limit` | integer | Max sessions (1-100, default: 10) |
| `offset` | integer | Pagination offset (default: 0) |
| `status` | string | Filter by session status: `active`, `closed`. Any other value returns `400`. |
| `profile` | string | Sessions whose **latest effective** profile is this name; `-` selects sessions with none |
| `client` | string | Sessions whose credential is bound to this client id; `-` selects sessions with none |
| `token` | string | Sessions that initialized with this token name; `-` selects sessions with none. `agent` is an alias of `token`; naming two different tokens returns `400` |

Each row carries `client_id`, `token_name`, `profile` and `profile_source` (`pin`, `binding`, `url`, `session`, `anonymous` or `none`). `client_name` is not a sessions filter and returns `400` (`client_name is not supported on this endpoint; filter by client`). The scope filters are applied before the `limit` truncation, so `total` is the filtered count.

The `status` filter is applied during the storage walk, **before** the `limit`
truncation, so a long-running session that is still active is returned even when
newer sessions would otherwise fill the page. When `status` is set, `total`
counts the matching sessions rather than every stored session.

Handshake-only sessions are not persisted, so a connected but
idle client does not appear until its first tool call.

#### GET /api/v1/sessions/{id}

One session, the same shape as a list row; `404` when unknown.


### Activity

Track and audit AI agent tool calls. See [Activity Log](../features/activity-log.md) for detailed documentation.

#### GET /api/v1/activity

List activity records with filtering and pagination.

**Query Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `type` | string | Filter by type, such as `tool_call`, `internal_tool_call`, `policy_decision`, `tool_quarantine_change`, `server_change`, `config_change`, `profile_change` or `preflight` |
| `server` | string | Filter by server name |
| `tool` | string | Filter by tool name |
| `session_id` | string | Filter by MCP session ID |
| `status` | string | Filter by status: `success`, `error`, `blocked` |
| `profile` | string | Filter by the profile in effect when the call ran; `-` selects records with none |
| `client` | string | Filter by client id (the client's binding); `-` selects records with none |
| `token` | string | Filter by the token name in effect; `-` selects records with none. `agent` is an alias of `token`; naming two different tokens returns `400` |
| `start_time` | string | Filter after this time (RFC3339) |
| `end_time` | string | Filter before this time (RFC3339) |
| `limit` | integer | Max records (1-100, default: 50) |
| `offset` | integer | Pagination offset (default: 0) |

Records are returned as stored. MCPProxy's internal `_auth_*` arguments are
dropped from `arguments` (they are identity plumbing, surfaced as the typed
`auth_type` / `agent_name` fields).

The same `profile`, `client` and `token` filters apply to `GET /api/v1/usage`. Under a scope filter `/usage` is computed from the matching records of the requested window (per-tool figures are window-bounded rather than lifetime).

**Response:**
```json
{
  "success": true,
  "data": {
    "activities": [
      {
        "id": "01JFXYZ123ABC",
        "type": "tool_call",
        "server_name": "github-server",
        "tool_name": "create_issue",
        "status": "success",
        "duration_ms": 245,
        "timestamp": "2025-01-15T10:30:00Z"
      }
    ],
    "total": 150,
    "limit": 50,
    "offset": 0
  }
}
```

#### GET /api/v1/activity/{id}

Get full activity record details including request arguments and response data.

#### GET /api/v1/usage

The usage aggregate behind the Usage page: `window` (`1h`, `24h`, `7d`, `30d`,
`all`), `server`, `tool`, `status`, `top`, `sort`, plus the scope filters
above. Unscoped reads come from the in-memory aggregate.


### Access explain

#### GET /api/v1/access/explain

Evaluates access to `tool=server:tool` for exactly one `client=<id>` or
`profile=<id>`. Returns `subject`, `tool`, `available` and `issues`.

`available` is true when the tool can be used now. `issues` contains only
concrete blockers as `{code, message}` objects, such as missing profile,
profile exclusion, host availability, server sign-in/connection, tool disablement,
or approval needed. New and changed tools share the same approval message.
An available tool has an empty issues array. Missing profiles do not produce
cascading profile-membership failures. No passing checks, internal profile source,
first-failure field, or suggested-action fields are returned.


### Change stream

#### GET /api/v1/events

Server-Sent Events, same session authentication, no request deadline. Frames:

- `event: hello`, `data: {"seq": N}` — sent on every (re)connect; `N` is the
  last `seq` published before the subscription. The client refetches every
  collection it has loaded, so frames missed while disconnected never matter.
- `event: change`, `data: {"seq", "entity", "op", "id", "data"}`:
  - `upsert` — `data` is the object exactly as `GET /api/v1/{entity}/{id}` returns it.
  - `delete` — `id` only.
  - `append` — activity only; `data` is the new record in the `GET /activity` shape.
  - `invalidate` — refetch what you show of the entity (`id` optional).
- `: ping` comment every 30 s.

`seq` increases by one per frame, process-wide. A subscriber that falls more
than 256 frames behind is disconnected; it reconnects and gets a new `hello`.

| Entity | Ops | Emitted when |
|---|---|---|
| `servers` | upsert, delete | Config commits (create, patch, delete, enable), connection state changes, health changes from call failures, OAuth token refresh / refresh failure / logout, tool approval/enable/classification/discovery (counts), peer presence of the server's host. Upserts are coalesced per id over 150 ms: two servers changed are two frames, one server changed twice is one |
| `tools` | invalidate (`id` = server; empty after a full discovery sweep) | Approve, block, enable/disable, classification, discovery that changed the tools or the review queue, server removal/replacement |
| `review` | invalidate | Same triggers as `tools` |
| `clients` | upsert, delete | Create, patch, rotate, delete, session open/close/activity of the client, peer presence, profile changes |
| `peers` | upsert, delete | Create, rotate, delete, connect, disconnect |
| `profiles` | upsert, delete | Create, patch, rename, delete; every profile after server or tool changes (effective servers, tool counts) |
| `secrets` | invalidate | Secret set/delete, config-to-secret, server config commits (references) |
| `sessions` | upsert | Session open, close, reopen, profile/workspace change, activity |
| `activity` | append | Every stored activity record |
| `usage` | invalidate | Every stored activity record, at most every 2 s |

Invalidations are throttled per `(entity, id)` (leading and trailing edge,
250 ms; `usage` 2 s).


## Error Responses

```json
{
  "error": "error message",
  "code": "ERROR_CODE"
}
```

| Code | Description |
|------|-------------|
| 401 | Unauthorized - no valid dashboard session |
| 404 | Not Found - Server or resource not found |
| 500 | Internal Server Error |

