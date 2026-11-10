# Managing MCPProxy through MCP

Create a client in **Clients** and enable **Allow MCPProxy management** for trusted
agents. It defaults to off and can also be changed in the client's edit dialog.
Use that client's generated token with `/mcp`; a dashboard session is not needed.

This grants administration across the instance, including peers and other clients.
The client's profile and connection host still restrict its ordinary upstream tool
calls. Profiles do not grant or limit administration. Revoking the client permission
hides the management tools and rejects their next call, including existing sessions.
Rotating a token replaces it immediately and retains the client's permission.

| Tool | Operations |
| --- | --- |
| `manage_servers` | List/read, create, patch, delete, enable/disable, restart, OAuth sign-in/out, logs, convert environment/header values to secrets |
| `manage_clients` | List/read, create, update profile/host/switching/client permissions, rotate token, delete |
| `profiles` | List/read, create, update, rename, delete, assign clients, inspect effective tools, explain access |
| `manage_peers` | List, create, rotate token, delete |
| `manage_secrets` | List references and usage, set a secret store value, delete |
| `manage_tools` | List full definitions and usage, inspect reviews/diffs, approve reviewed definitions, enable/disable, classify unannotated tools |
| `manage_activity` | List/filter calls, read details, list sessions, inspect usage |

The schemas expose ordinary JSON objects and arrays. Server creation/edits, client
writes, secrets, tool listings and approvals use the same operations as the dashboard;
there is no HTTP round trip through the REST API. Approval requires the hashes from
the review and supports its server incarnation so a changed/replaced definition cannot
be approved accidentally. Management tools use the built-in `mcpproxy` namespace and are available in every
MCP routing mode. In retrieval and code-execution modes their schemas are returned
only by `retrieve_tools` (or `describe_tool` where available), not in the initial
`tools/list`. Search uses the same BM25 implementation as upstream discovery.
Call a discovered tool with `call_tool_destructive` and its canonical ID, or use
`call_tool("mcpproxy", "manage_servers", args)` inside `code_execution`. Scripts
need both client permissions to administer MCPProxy. Direct mode advertises them
as `mcpproxy__manage_servers`, etc., and follows its full/deferred schema setting.

For example:

```json
{
  "name": "call_tool_destructive",
  "arguments": {
    "name": "mcpproxy:manage_servers",
    "args": {
      "operation": "create",
      "server": {
        "name": "example",
        "url": "https://example.com/mcp",
        "enabled": true
      }
    }
  }
}
```

Client tokens remain invalid on REST, even when administration is enabled. REST requires an OIDC browser session. MCP settings cannot read or replace the
OIDC deployment configuration. See [dashboard sign-in](dashboard-oidc.md).
Management activity records include the operation and outcome, without secret values
or returned client/peer tokens.

## Peer HTTP surface

A peer listens only on `/mcp`. It exposes no dashboard, REST API, event stream,
health probes or peer-control route. Clients authenticate with their client tokens.
The peer forwards the request to the primary with its separate peer credential;
the primary validates both credentials and checks that the client is assigned to
that peer. The peer credential alone cannot authenticate an MCP client. An offline
peer returns `503` for `/mcp` until its primary connection is restored.

## Client code execution permission

**Allow code execution** is a separate client permission, also off by default.
It is stored as `allow_code_execution`; management uses `allow_managing`.
Neither permission is controlled by profiles or a global enable switch. They are
independent: a client can execute scripts without administering MCPProxy, and can
administer MCPProxy without executing scripts. Revocation applies to the next
call even in an existing session; token rotation preserves both permissions.
Scripts retain the client's normal server/tool/profile and execution-host limits.
Timeouts, concurrency and call budgets remain global execution limits.
