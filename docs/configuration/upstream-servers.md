---
id: upstream-servers
title: Upstream Servers
sidebar_label: Upstream Servers
sidebar_position: 2
description: Configure MCP servers to connect through MCPProxy
keywords: [upstream, servers, mcp, stdio, http, oauth]
---

# Upstream Servers

MCPProxy connects to multiple MCP servers through a single endpoint. Add and edit
servers on **Servers**, or use the dedicated server API/management tools. Each
server has one canonical record in encrypted `primary.db`; it is not a deployment environment setting. The JSON examples below are server API payloads.

## Server Types

MCPProxy supports three types of upstream connections:

### stdio Servers

Local servers that communicate via standard input/output:

```json
{
  "name": "filesystem",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/projects"],
  "protocol": "stdio",
  "enabled": true
}
```

### HTTP Servers

Remote servers accessible via HTTP/HTTPS:

```json
{
  "name": "remote-server",
  "url": "https://api.example.com/mcp",
  "protocol": "http",
  "enabled": true
}
```

### OAuth Servers

Servers requiring OAuth 2.1 authentication:

```json
{
  "name": "github-server",
  "url": "https://api.github.com/mcp",
  "protocol": "http",
  "oauth": {
    "client_id": "your-client-id",
    "scopes": ["repo", "user"]
  },
  "enabled": true
}
```

#### Pinning the callback port with `redirect_uri`

By default mcpproxy asks the OS for a free loopback port on the first OAuth
login, then persists that port and reuses it. If the saved port is taken next
time, a different one is allocated — so the callback URL is not guaranteed to
stay put.

Some providers require the port to match the registered callback URL exactly.
GitHub OAuth Apps are the common case: even with GitHub's wildcard matching
enabled, that matching covers subdomains and subdirectory paths — the host and
port must still match exactly. Set `redirect_uri` to pin it:

```json
{
  "oauth": {
    "client_id": "Iv1.abc123",
    "redirect_uri": "http://127.0.0.1:54108/oauth/callback"
  }
}
```

mcpproxy binds that exact port and path and sends that exact string to the
provider. Register the identical URL with the provider.

The value must be an RFC 8252 loopback redirect: `http` scheme, a loopback host,
and an explicit port. The path can be anything — some providers publish a
single shared OAuth application with a fixed callback path an operator cannot
change (e.g. `http://localhost:18080/callback`), and mcpproxy's own callback
path is just an implementation detail, so it binds its listener to whatever
path the pin specifies. A pin with no path at all binds `/`; `/oauth/callback`
is only the default when `redirect_uri` is omitted entirely and mcpproxy
allocates a dynamic port. Prefer `127.0.0.1`;
`localhost` is accepted but the listener binds `127.0.0.1`, while
`http://[::1]:PORT/oauth/callback` binds the IPv6 loopback.

A malformed value, or a pinned port already in use, fails the login with an
error that names `oauth.redirect_uri` — in the connection error and the
server's `health.detail`, and in the main
log. Server edits through the dashboard or management MCP tools validate the
redirect URI before storing it in the primary database.

## Configuration Options

| Option | Type | Required | Description |
|--------|------|----------|-------------|
| `name` | string | Yes | Unique identifier for the server |
| `command` | string | For stdio | Command to execute |
| `args` | array | No | Command arguments |
| `url` | string | For HTTP | Server URL |
| `protocol` | string | Yes | `stdio` or `http` |
| `enabled` | boolean | No | Whether server is active (default: true) |
| `working_dir` | string | No | Working directory for stdio servers |
| `env` | object | No | Environment variables to pass |
| `oauth` | object | No | OAuth configuration |

See [Tool Discovery & Health Check Intervals](environment-variables.md#tool-discovery--health-check-intervals) for the global defaults, accepted ranges, and trade-offs.

## Headers, Environment Variables, and Secrets

HTTP `headers` and stdio `env` are ordinary config fields, edited from the
Server Detail page or the REST API.

### Who sees raw values

- **Dashboard and REST API** (`/api/v1`, OIDC admin session): values are
  returned exactly as stored. `${secret:NAME}` and `${env:VAR}` references
  stay references; they are not resolved.
- **MCP `manage_servers` tool**: sensitive header, env, argument and OAuth
  values are masked (`••••59 (71 chars)`). A masked value echoed back on a
  write is reverted to the stored value for the same key; a mask that cannot
  be bound to its key (for example in `args`) is refused. This keeps a
  prompt-injected agent from reading another upstream's credentials.

### Editing

The Server Detail page has **Headers** and **Environment Variables** cards
with add, edit, delete and **Convert to secret**, which moves the value into
the encrypted store and replaces it with `${secret:NAME}`.

`PATCH /api/v1/servers/{name}` follows JSON Merge Patch
([RFC 7396](https://www.rfc-editor.org/rfc/rfc7396)) for maps: a string value
upserts, `null` deletes, an absent key is preserved.

```json
{"headers": {"X-New": "value", "X-Stale": null}}
```

See [REST API](../api/rest-api.md) for the full reference, including
`POST /api/v1/servers/{name}/config-to-secret`.

### Secret references

`headers` and `env` values support two reference shapes that get resolved
at request time rather than stored in plaintext:

| Reference | Source | Lifetime |
|---|---|---|
| `${secret:NAME}` | encrypted primary database entry called `NAME` | Persists across restarts; managed via the Secrets page |
| `${env:VAR}` | The `VAR` environment variable on the mcpproxy process | Tied to the shell/launcher that started mcpproxy |

When mcpproxy connects to an upstream it substitutes the reference with
the actual secret. The reference itself never reaches the upstream MCP
server. See [Encrypted storage](../features/encrypted-storage.md)
for the full secret-storage story.

## Process Lifecycle

### Startup

When MCPProxy starts, it:
1. Loads canonical server records from encrypted `primary.db`
2. Creates MCP clients for each enabled server
3. Connects to servers in the background (async)
4. Indexes tools once connections are established

### Shutdown

When MCPProxy stops, it performs graceful shutdown of all subprocesses:

1. **Graceful Close** (10s): Close MCP connection, wait for process to exit
2. **Force Kill** (9s): If still running, SIGTERM → poll → SIGKILL

**Process groups**: Child processes (spawned by npm/npx/uvx) are placed in a process group, ensuring all related processes are terminated together.

See [Shutdown Behavior](../operations/shutdown-behavior.md) for detailed documentation.

## Quarantine System

New servers added via AI clients are automatically quarantined for security review. See [Security Quarantine](../features/tool-quarantine.md) for details.
