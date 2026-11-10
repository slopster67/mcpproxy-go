---
title: "Output-Schema Validation"
sidebar_label: "Output Schema Validation"
description: "Verify that a tool's structured response conforms to its declared outputSchema before it reaches the agent."
---

# Output-Schema Validation

When an upstream MCP tool declares an `outputSchema`, MCPProxy can verify that
the tool's **structured response** conforms to that schema *before* it reaches
your agent. This protects the agent's context from a buggy or compromised
server injecting malformed, oversized, or unexpected data.

Validation covers `structuredContent` only.

## Configuration

Set these environment variables on the primary:

| Variable | Values | Default | Meaning |
|-------|--------|---------|---------|
| `MCPPROXY_OUTPUT_VALIDATION_MODE` | `off` \| `warn` \| `strict` | `warn` | `off` disables validation; `warn` forwards violations but logs them; `strict` blocks violations. |
| `MCPPROXY_OUTPUT_VALIDATION_MAX_BYTES` | integer | `5242880` (5 MiB) | Max serialized size of the structured payload; larger payloads are a guard violation. |
| `MCPPROXY_OUTPUT_VALIDATION_MAX_DEPTH` | integer | `64` | Max nesting depth of the structured payload; deeper payloads are a guard violation. |
| `MCPPROXY_OUTPUT_VALIDATION_MISSING_STRUCTURED_CONTENT` | `allow` \| `block` | `allow` | In **strict** mode only: what to do when a tool declares a schema but returns no `structuredContent`. `allow` forwards it (recommended); `block` rejects it. |

By default validation runs in `warn` mode. It never blocks a working agent; it
only records violations for tools that declare an output schema. Switch to
`strict` once you have reviewed the warnings. Settings are fixed at startup.

## Behaviour

| Tool declares `outputSchema`? | Response has `structuredContent`? | Conforms? | `warn` | `strict` |
|---|---|---|---|---|
| No | — | — | forward (no-op) | forward (no-op) |
| Yes | No (text only) | — | forward (no-op) | forward, unless `missing_structured_content=block` |
| Yes | Yes | Yes | **forward unchanged** | **forward unchanged** |
| Yes | Yes | No | forward + audit | **block** + audit |
| Yes | Yes (oversized / too deep) | — | forward + audit (guard) | **block** + audit (guard) |
| Yes | upstream error result | — | forward (skip) | forward (skip) |

Key guarantees:

- **Lossless on success**: a conforming `structuredContent` is forwarded
  byte-for-byte unchanged (validation runs on a read-only view).
- **Never blocks on a bad schema**: if a tool's declared schema is itself not
  compilable, validation degrades to a no-op (logged once) — it never blocks
  traffic on the proxy's inability to compile a schema.
- **Strict blocks return a clear error** to the agent:
  `output schema validation failed: <keyword> at <path>: <detail>`.

## Auditing

Every violation (block or warn) is recorded as a `policy_decision` activity
record with `decision = "blocked"` or `"warning"` and the violation detail. These
records appear in **Activity** (event type Policy Decision) and over the REST API:

```bash
curl -s -b "$DASHBOARD_COOKIES" \
  "http://127.0.0.1:8080/api/v1/activity?type=policy_decision&limit=5" | jq .
```
