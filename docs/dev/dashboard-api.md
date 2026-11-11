# Dashboard API contract

The REST API under `/api/v1` and its event stream serve one audience: the
authenticated admin dashboard. Every request passes `dashboardAuth.Middleware`
(OIDC session; CSRF + Origin on writes), which always yields an admin
`AuthContext`. Handlers therefore do no per-caller scoping, no visibility
filtering and no secret masking. Stored values are returned exactly as written
(secret references such as `${secret:x}` stay references; they are not
resolved).

MCP clients manage mcpproxy through the `manage_*` MCP tools, not REST. That
surface keeps its own authorization (`CanManageMCPProxy`, agent-denied ops) and
masking (`oauth.RedactServerSecretFields`, mask-echo write protection).

## Entities and routes

Every entity uses the same shape:

| Route | Meaning |
|---|---|
| `GET /api/v1/{entity}` | list (`{ "<entity>": [...] }` inside the usual `{success,data}` envelope) |
| `GET /api/v1/{entity}/{id}` | one object, same shape as a list element |
| `POST /api/v1/{entity}` | create; returns the created object |
| `PATCH /api/v1/{entity}/{id}` | partial update of writable fields; returns the updated object |
| `DELETE /api/v1/{entity}/{id}` | delete |

Only operations that are not field writes stay as action routes
(`POST /api/v1/{entity}/{id}/{action}`). DELETE answers `{"deleted": id}`
(profiles add `moved`). Creating a client or peer, and rotating either,
answers the object plus its one-time `credential`/`snippet` (clients) or
`token` (peers), with `Cache-Control: no-store`. `restart` and `logout`
answer the server object.

| Entity | id | Writable via PATCH | Actions |
|---|---|---|---|
| `servers` | name | every config field (url, protocol, command, args, working_dir, env, headers, oauth, enabled, trust_mode, host_id, available_to_peers, icon, description, …) | `restart`, `login`, `logout`, `config-to-secret`; `GET /servers/{id}/oauth/discovered`, `GET /servers/{id}/logs` (reads) |
| `tools` | `server:tool` (URL-encoded; split at the first colon) | `enabled`, `classification` (+ `expected_hash`, `expected_incarnation`) | `approve`, `block` (hash-checked, per server: `POST /servers/{id}/tools/approve|block`) |
| `clients` | client id | `display_name`, `profile`, `mode`, `host_id`, `allow_managing`, `allow_code_execution` | `rotate` |
| `peers` | peer id | — (no PATCH) | `rotate` |
| `profiles` | profile id | every field including `name` (no separate rename) | — |
| `secrets` | name (+ `type` query, default `secret`); list elements carry `id` = `type:name` | value (POST/PATCH sets it) | — |
| `sessions` | session id | — (read-only) | — |
| `activity` | activity id | — (read-only, append-only) | — |
| `usage` | — | — (read-only aggregate: `GET /api/v1/usage?…`) | — |

### Servers

A server object is flat: the stored `config.ServerConfig` fields exactly as
written (same JSON names PATCH accepts, including the full `oauth` block with
`mode` and `client_secret`), plus read-only runtime fields (connection state,
health, diagnostic, oauth_status, authenticated, token_expires_at, oauth_mode,
tool counts, quarantine stats, generation/incarnation, host fields). One
builder produces it; REST list, REST get, events and the MCP tools (which mask
its output) all use that builder.

`enabled` is a PATCH field; there are no enable/disable routes.

## Event stream

`GET /api/v1/events` (Server-Sent Events, same auth). Every frame is one of:

- `event: hello` — `data: {"seq": N}`. Sent on every (re)connect. The client
  refetches every collection it has loaded, so events missed while
  disconnected never matter.
- `event: change` — `data: {"seq": N, "entity": "...", "op": "...", "id": "...", "data": {...}}`
  - `op: "upsert"` — `data` is the full object as `GET /{entity}/{id}` returns it.
  - `op: "delete"` — `id` only.
  - `op: "append"` — activity only; `data` is the new activity record.
  - `op: "invalidate"` — the entity's data changed in a way not worth
    shipping per object (`usage`, `secrets`, `tools` with `id` =
    server name, or no id after a full discovery sweep); the client refetches
    what it shows. Invalidations are throttled per (entity, id): 250 ms,
    `usage` 2 s.
- `: ping` comments as heartbeat.

`seq` increases monotonically per process. Upserts of every entity are
coalesced per id over 150 ms (a dirty-id set, never last-write-wins across
ids); at flush each id is rebuilt by the entity's builder and published as
`upsert`, or as `delete` when it no longer exists. A subscriber more than 256
frames behind is disconnected and reconnects (new `hello`).

`secrets` uses `invalidate`, not per-object frames: the list is derived from
the secret store and every server config, so a server edit changes it as much
as a secret write does.

There is no `/api/v1/status`; nothing in the dashboard reads it.

Every write path of every entity emits its change: server config commits and
runtime state transitions, tool approvals/enable/classification, client
create/update/rotate/delete and presence, peer create/rotate/delete/presence,
profile CRUD, secret set/delete, session open/close, activity records, usage.

## Dashboard data flow

Each page loads the collections it renders on mount through one generic
collection store (`ensureLoaded`), then keeps them current from `change`
events, and refetches loaded collections on `hello`. There is no polling and
no refresh button except on Tools.
