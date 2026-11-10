---
id: MCPX_QUARANTINE_PENDING_APPROVAL
title: MCPX_QUARANTINE_PENDING_APPROVAL
sidebar_label: PENDING_APPROVAL
description: This server has tools awaiting security approval; they will not run until approved.
---

# `MCPX_QUARANTINE_PENDING_APPROVAL`

**Severity:** warn
**Domain:** Quarantine

## What happened

mcpproxy quarantines newly added MCP servers (and newly added tools on existing
servers) by default. The quarantine is mcpproxy's defense against Tool Poisoning
Attacks (TPA) — the user must review tool descriptions and JSON schemas before
they're routed to AI clients.

This warning means the server connected and reported its tools, but at least
one tool is pending your approval.

## How to fix

### Approve via the web UI

Open the server's detail page → **Quarantine** panel → review the tool
descriptions / schemas → click **Approve all** or per-tool **Approve**.

### Skip approval for trusted servers

For servers you fully trust, set `trust_mode` to `auto` on that server (or globally) so
new and changed tools are approved automatically. The default `manual` mode requires review.

## Related

- [Security Quarantine](../features/tool-quarantine.md)
- [`MCPX_QUARANTINE_TOOL_CHANGED`](MCPX_QUARANTINE_TOOL_CHANGED.md) — rug-pull detection
