---
id: agent-instructions
title: Agent Instructions (CLAUDE.md / AGENTS.md)
sidebar_label: Agent Instructions
sidebar_position: 13
description: Help Claude Code, Codex and other agents find the tools behind mcpproxy instead of falling back to shell CLIs — what the proxy tells each client automatically, and a snippet for CLAUDE.md / AGENTS.md
keywords: [agents, claude code, codex, AGENTS.md, CLAUDE.md, retrieve_tools, instructions, tool search]
---

# Agent Instructions

With the default `retrieve_tools` [routing mode](routing-modes.md), an agent
connected to mcpproxy sees only a few built-in tools (`retrieve_tools`,
`call_tool_read`/`write`/`destructive`, …). It does **not** see `github:create_issue`
or `jira:search` in its tool list. This keeps the context small, but an agent that
doesn't know where the tools are may decide that GitHub is unavailable and reach
for the `gh` CLI, `curl` or a raw API.

This is more likely in clients that defer MCP tools behind their own keyword
search, like Claude Code's tool search. There the model sees only
`mcp__mcpproxy__retrieve_tools` and searches for "github". If nothing mentions
GitHub, it gives up.

## What mcpproxy tells every client automatically

Each connection gets guidance built for **that caller**:

- **Initialize instructions** (also on the 2026 `server/discover` handshake).
  - They state that upstream tools are reachable only through the proxy.
  - They say to search with `retrieve_tools` before using a shell CLI or HTTP API.
  - They end with a **YOUR ACCESS** block that lists:
    - the active profile;
    - the connected upstream servers this caller can use;
    - the operations it may perform;
    - built-ins it cannot use (`code_execution` without the client permission).
- **`retrieve_tools` description.** It ends with
  `CONNECTED SERVERS searchable here: github, jira, …`, so a client-side tool
  search for "github" lands on `retrieve_tools`.

Example for a client whose profile `work` exposes only `github` read and write tools:

```text
YOUR ACCESS (this connection): profile 'work' is active — servers outside it are not reachable.
Connected upstream servers you can use: github. Find their tools with 'retrieve_tools'
(query: the server name plus the task). Allowed operations: read, write — destructive
tool calls will be refused (individual tools may also be restricted).
```

Everything in this block is derived from the same checks the proxy applies when
a tool is called, so it stays in sync with what the caller can actually do:

| Source | Effect on the text |
|--------|--------------------|
| [Profile](profiles.md) (assigned or selected with `set_profile`) | Only the profile's servers are listed. The tiers of its selected tools set "Allowed operations". |
| Client permissions | `code_execution` is reported as unavailable without **Allow code execution**. |
| Server state | Only enabled, **connected** servers on the connection host are listed. |

A caller never sees the name of a server outside its scope. When the set of
connected servers changes, mcpproxy sends `notifications/tools/list_changed` so
clients re-list and pick up the new `retrieve_tools` description.

:::note
Initialize instructions are read once per connection, and `set_profile` does not
push `tools/list_changed`, so after a mid-session switch the client keeps the text
it already holds until it re-lists or reconnects. `retrieve_tools` results and
tool calls always follow the new profile.
:::

### Turning server names off

Set `MCPPROXY_ADVERTISE_UPSTREAM_SERVERS=false` to keep server names out of client
context. You might want this if your LLM provider must not see your internal
server names. The operation limits and the "use `retrieve_tools` first"
guidance stay.

Custom instructions (`MCPPROXY_INSTRUCTIONS`) replace the built-in
guidance and are sent verbatim to every caller. The per-caller **YOUR ACCESS**
block is still appended.

## Add a snippet to CLAUDE.md / AGENTS.md

Clients don't always weigh MCP server instructions heavily. A line in the
agent's own memory file is the most reliable nudge. Paste this into:

| Client | File |
|--------|------|
| Claude Code | `~/.claude/CLAUDE.md` (all projects) or `CLAUDE.md` in the repo |
| Codex, opencode, ZCode, Gemini CLI and other AGENTS.md readers | `~/.codex/AGENTS.md` (Codex, global) or `AGENTS.md` in the repo |
| Cursor | `.cursor/rules/mcpproxy.mdc` |

```markdown
## MCP tools via mcpproxy

External services (GitHub, Jira, Slack, databases, cloud APIs, …) are available
as MCP tools through the `mcpproxy` MCP server. Its tools are NOT listed
individually.

- Before using a shell CLI (`gh`, `aws`, `kubectl`, `curl`, …) or a raw HTTP API
  for an external service, call `mcpproxy` `retrieve_tools` with the task and
  the service name (e.g. "create github issue").
- Call the tool it returns via the `call_tool_*` variant named in `call_with`.
- If nothing relevant comes back, retry with different wording or just the
  service name before concluding the tool does not exist.
```

Adjust the snippet for direct and code_execution modes, which expose different
built-ins.

## Alternatives

- **List every tool directly.** Set `MCPPROXY_ROUTING_MODE=direct` and connect to `/mcp`. Each upstream tool then appears under its own
  `server__tool` name. Clients with their own tool search (Claude Code) handle
  large lists well this way. Others pay the context cost. See
  [Routing Modes](routing-modes.md) and
  [Schema-Deferred Direct Mode](schema-deferred-direct-mode.md).
- **Narrow the scope.** Assign a [profile](profiles.md) to the client for a shorter, more relevant server list.
