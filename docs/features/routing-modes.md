---
id: routing-modes
title: Routing Modes
sidebar_label: Routing Modes
sidebar_position: 2
description: Configure how MCPProxy exposes upstream tools to AI agents
keywords: [routing, modes, direct, code_execution, retrieve_tools, mcp]
---

# Routing Modes

MCPProxy supports three routing modes that control how upstream MCP tools are exposed to AI agents. Each mode offers different tradeoffs between token efficiency, flexibility, and simplicity.

## Overview

| Mode | Tool Exposure | Best For |
|------|---------------|----------|
| `retrieve_tools` | BM25 search via `retrieve_tools` + `call_tool_read/write/destructive` | Large tool sets (50+ tools), token-sensitive workloads |
| `direct` | All tools exposed as `serverName__toolName` | Small tool sets, simple setups, maximum compatibility |
| `code_execution` | `code_execution` + `retrieve_tools` for discovery | Multi-step orchestration, reduced round-trips |

All clients connect to `/mcp`. `MCPPROXY_ROUTING_MODE` (default `retrieve_tools`)
selects the tool interface for every client; client tokens determine profile
permissions. The mode is fixed at startup. There are no per-mode or per-profile
MCP URLs.

Every mode also offers `set_profile` for switchable clients, `code_execution` for
clients with **Allow code execution**, and the [management tools](mcp-credential-lifecycle.md)
for clients with **Allow MCPProxy management**.

## Mode Details

### retrieve_tools (Default)

The default mode uses BM25 full-text search to help AI agents discover relevant tools without exposing the entire tool catalog. This approach — sometimes called "lazy tool loading" or "tool search" — is used by Anthropic's own MCP implementation and is the recommended pattern for large tool sets.

**Endpoint:** `/mcp` with `routing_mode: "retrieve_tools"`

**Tools exposed:**
- `retrieve_tools` — Search for tools by natural language query
- `call_tool_read` — Execute read-only tool calls
- `call_tool_write` — Execute write tool calls
- `call_tool_destructive` — Execute destructive tool calls
- `describe_tool` — Return a tool's full definition when search results are compact
- `read_cache` — Page through a truncated response. Each cached page is bound to the authorization that produced it; a caller that could not have produced the entry gets the same `cache key not found` answer as for a missing key.

**How it works:**
1. AI agent calls `retrieve_tools` with a natural language query
2. MCPProxy returns matching tools ranked by BM25 relevance, with `call_with` recommendations
3. AI agent calls the appropriate `call_tool_*` variant with the exact tool name from results

**Pros:**
- Massive token savings: only matched tools are sent to the model, not the full catalog
- Scales to hundreds of tools across many servers without context window bloat
- Intent-based permission control (read/write/destructive variants) enables granular IDE approval flows
- Activity logging captures operation type and intent metadata for auditing

**Cons:**
- Two-step workflow (search then call) adds one round-trip compared to direct mode
- BM25 search quality depends on tool descriptions — poorly described tools may not surface
- The AI agent must learn the retrieve-then-call pattern (most modern models handle this well)

**When to use:**
- You have many upstream servers with dozens or hundreds of tools
- Token usage is a concern (common with paid API usage)
- You want intent-based permission control in IDE auto-approve settings
- Production deployments where audit trails matter

### direct

Direct mode exposes every upstream tool directly to the AI agent. Each tool appears in the standard MCP `tools/list` with a `serverName__toolName` name. This is the simplest mode and the closest to how individual MCP servers work natively.

**Endpoint:** `/mcp` with `routing_mode: "direct"`

**Tools exposed:**
- Every approved, enabled tool the client's profile allows
- Named as `serverName__toolName` (e.g., `github__create_issue`, `filesystem__read_file`)

**How it works:**
1. AI agent sees all available tools in `tools/list`
2. AI agent calls tools directly by their `serverName__toolName` name
3. MCPProxy routes the call to the correct upstream server

**Tool naming:**
- Separator is `__` (double underscore) to avoid conflicts with single underscores in tool names
- The split happens on the FIRST `__` only, so tool names containing `__` are preserved
- Descriptions are prefixed with `[serverName]` for clarity

**Access:** the client's profile and connection host restrict the listing and every call.

**Pros:**
- Zero learning curve: tools work exactly like native MCP tools
- Single round-trip: no search step needed, call any tool directly
- Maximum compatibility: works with any MCP client without special handling
- Tool annotations (readOnlyHint, destructiveHint) are preserved from upstream

**Cons:**
- High token cost: all tool definitions are sent in every request context
- Does not scale well beyond ~50 tools (context window fills up, model accuracy degrades)
- No intent-based permission tiers (the model just calls tools)
- All tools visible upfront increases attack surface for prompt injection

**When to use:**
- Small setups with fewer than 50 total tools
- Quick prototyping and testing
- AI clients that don't support the retrieve-then-call pattern
- Agents that know exactly which tools they need

Direct mode can also advertise deferred schemas; see
[Deferred schemas in direct mode](schema-deferred-direct-mode.md).

### code_execution

Code execution mode is designed for multi-step orchestration workflows. Instead of making separate tool calls for each step, the AI agent writes JavaScript or TypeScript code that chains multiple tool calls together in a single request. This is inspired by patterns from OpenAI's code interpreter and similar "tool-as-code" approaches.

**Endpoint:** `/mcp` with `routing_mode: "code_execution"`

**Tools exposed:**
- `code_execution` — Execute JavaScript/TypeScript that orchestrates upstream tools
- `retrieve_tools` — Search for tools (instructs to use `code_execution`, not `call_tool_*`)
- `read_cache` — Page through a truncated response

**How it works:**
1. AI agent finds tools with `retrieve_tools`
2. AI agent writes JavaScript/TypeScript code that calls `call_tool(serverName, toolName, args)`
3. MCPProxy executes the code in a sandboxed ES2020+ VM, routing tool calls to upstream servers
4. Results are returned as a single response

**Pros:**
- Minimal round-trips: complex multi-step workflows execute in one request
- Full programming power: conditionals, loops, error handling, data transformation
- TypeScript support with type safety (auto-transpiled via esbuild)
- Sandboxed execution: no filesystem or network access, timeout enforcement

**Cons:**
- Requires the AI model to write correct JavaScript/TypeScript code
- Debugging is harder: errors come from inside the sandbox, not from MCP tool calls
- Higher latency per request (VM startup + multiple sequential tool calls)
- Not all AI models are equally good at writing code for tool orchestration

**When to use:**
- Workflows that chain 2+ tool calls with data dependencies between them
- Batch operations (e.g., "for each repo, check CI status and create issue if failing")
- Complex conditional logic that would require many round-trips in other modes
- Data transformation pipelines (fetch from one tool, transform, send to another)

Code execution requires **Allow code execution** on the client in **Clients**. There is no global or profile-level enable switch.

## Choosing the Right Mode

In `retrieve_tools` and `code_execution` modes the agent does not see upstream tools
in its tool list. mcpproxy names the caller's reachable servers in its instructions
and in the `retrieve_tools` description; see [Agent Instructions](agent-instructions.md)
for a `CLAUDE.md` / `AGENTS.md` snippet that makes agents use them reliably.

| Factor | retrieve_tools | direct | code_execution |
|--------|---------------|--------|----------------|
| **Token cost** | Low (only matched tools) | High (all tools) | Low (only matched tools) |
| **Round-trips per task** | 2 (search + call) | 1 (direct call) | 1 (code handles all) |
| **Max practical tools** | 500+ | ~50 | 500+ |
| **Setup complexity** | None (default) | None | Client permission required |
| **Model requirements** | Any modern LLM | Any LLM | Code-capable LLM |
| **Audit granularity** | High (intent metadata) | Medium (annotations) | Medium (code logged) |
| **IDE auto-approve** | Per-variant rules | Per-tool rules | Single rule |

## Changing Routing Mode

Set `MCPPROXY_ROUTING_MODE` to `retrieve_tools`, `direct` or `code_execution`,
then restart the primary. `/mcp` uses that mode for every client. Per-client
permissions still control code execution and MCPProxy management.

`MCPPROXY_TOOL_RESPONSE_MODE` controls search-result detail;
`MCPPROXY_DIRECT_TOOL_RESPONSE_MODE` controls direct-listing detail. These are
also fixed at startup. See [Settings](../../SETTINGS.md).

## Tool Refresh

When upstream servers connect, disconnect, or update their tools:

- **Direct mode**: Tool list is automatically rebuilt. The AI client receives a `notifications/tools/list_changed` notification.
- **Retrieve tools and code execution modes**: The BM25 search index is updated.

## Security Considerations

- **Direct mode** exposes all allowed tools upfront, which increases the initial token cost but simplifies the interaction. Use profiles to limit exposure.
- **Retrieve tools mode** shows agents only the tools matching their search query.
- Code execution requires **Allow code execution** on the client in **Clients**. There is no global or profile-level enable switch.
- All modes enforce tool approval, tool enable/disable, profiles and connection-host scope.