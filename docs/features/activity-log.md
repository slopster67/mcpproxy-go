---
id: activity-log
title: Activity Log
sidebar_label: Activity Log
description: Inspect tool calls, policy decisions and system events recorded by MCPProxy
keywords: [activity, logging, observability]
---

# Activity Log

MCPProxy records tool calls, policy decisions and system events in the encrypted
primary database. Records are pruned by age, count and total size, and stored
responses are truncated, so the log is a debugging and inspection aid rather
than a complete or tamper-proof audit trail.

## What gets logged

| Type | Description |
|------|-------------|
| `tool_call` | Every upstream tool call made through MCPProxy |
| `internal_tool_call` | Built-in tool calls (`retrieve_tools`, `call_tool_*`, `code_execution`, management tools, ...) |
| `policy_decision` | Calls blocked or flagged by policy (profiles, approvals, output validation) |
| `tool_quarantine_change` | Tool approval, block and review changes |
| `server_change` | Server enable/disable/restart events |
| `config_change` | Server configuration changes |
| `profile_change` | Profile writes |
| `preflight` | Required-tools preflight checks |
| `system_start`, `system_stop` | MCPProxy startup and shutdown |

Successful `call_tool_*` internal records are hidden from listings because each
has a matching `tool_call` record with the same `request_id`.

### Tool call records

```json
{
  "id": "01JFXYZ123ABC",
  "type": "tool_call",
  "server_name": "github",
  "tool_name": "create_issue",
  "tool_variant": "call_tool_write",
  "arguments": {"title": "Bug report", "body": "..."},
  "response": "Issue created",
  "status": "success",
  "duration_ms": 245,
  "timestamp": "2026-01-15T10:30:00Z",
  "session_id": "mcp-session-abc123",
  "request_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "intent": {
    "operation_type": "write",
    "data_sensitivity": "internal",
    "reason": "Creating bug report per user request"
  }
}
```

`status` is `success`, `error` or `blocked`. Records also carry the client,
token, profile, client host and execution host that applied to the call.
`intent.operation_type` comes from the variant the client called; the reason
and data sensitivity are what the client declared
(see [Tool-call intent](intent-declaration.md)).

### Sub-calls made by code_execution

Every upstream tool call a `code_execution` script makes is recorded as its own
`tool_call` record with a `parent_id` equal to the parent `code_execution`
record's `request_id`. A sub-call refused by policy is recorded with status
`blocked`; profile refusals also carry `block_reason` (`profile_tier`,
`profile_rule`, `profile_unannotated` or `profile_server_scope`). Filter with
`parent_id=<parent request_id>` to list a script's sub-calls, or
`request_id=<child's parent_id>` to find the parent.

## Web UI

**Activity** has Tool calls, Sessions, System events and All views. Records are
listed newest first, with filters for server, result, profile, client, event
type and time range, plus a local search of the current page. Open a row for its
arguments, response and metadata. New records appear live. **Usage** shows
calls, errors, error rates, slow calls and large responses per server and tool.

## REST API

`GET /api/v1/activity`, `GET /api/v1/activity/{id}` and
`GET /api/v1/usage` are documented in the [REST API](../api/rest-api.md#activity).
List filters include `type` (comma-separated), `server`, `tool`, `session_id`,
`status`, `request_id`, `parent_id`, `profile`, `client`, `token`,
`start_time` and `end_time`. `exclude_payloads=true` omits `arguments` and
`response` for summary views. New records are published on the
`GET /api/v1/events` change stream as `activity` `append` frames.

```bash
curl -b "$DASHBOARD_COOKIES" \
  "http://127.0.0.1:8080/api/v1/activity?type=tool_call&status=error&limit=10"
```

## Retention

| Variable | Default | Description |
|----------|---------|-------------|
| `MCPPROXY_ACTIVITY_RETENTION_DAYS` | `90` | Days to retain records |
| `MCPPROXY_ACTIVITY_MAX_RECORDS` | `100000` | Records retained before the oldest are pruned |
| `MCPPROXY_ACTIVITY_MAX_SIZE_MB` | `256` | Total size before the oldest are pruned; `0` disables the size cap |
| `MCPPROXY_ACTIVITY_MAX_RESPONSE_SIZE` | `65536` | Response bytes stored per record; longer text ends with `...[truncated]` |
| `MCPPROXY_ACTIVITY_CLEANUP_INTERVAL_MIN` | `60` | Background cleanup interval in minutes |

MCPProxy also keeps a short per-server tool-call history with full arguments
and results for debugging:

| Variable | Default | Description |
|----------|---------|-------------|
| `MCPPROXY_TOOL_CALL_MAX_RESPONSE_SIZE` | `65536` | Stored response bytes per call; larger responses are replaced by a truncated preview |
| `MCPPROXY_TOOL_CALL_MAX_RECORDS_PER_SERVER` | `1000` | Calls retained per server; the oldest are evicted on write |

A non-positive value uses the default. Removing a server deletes its history.
Pruning frees space for reuse but does not shrink the database files on disk.
