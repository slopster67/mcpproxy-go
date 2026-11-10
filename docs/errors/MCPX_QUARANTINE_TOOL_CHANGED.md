---
id: MCPX_QUARANTINE_TOOL_CHANGED
title: MCPX_QUARANTINE_TOOL_CHANGED
sidebar_label: TOOL_CHANGED
description: A previously approved tool changed its description or schema; re-approval is required.
---

# `MCPX_QUARANTINE_TOOL_CHANGED`

**Severity:** warn
**Domain:** Quarantine

## What happened

mcpproxy hashes the description and JSON schema of every tool at approval time.
On every reconnect / poll, it re-hashes and compares. A tool whose hash changed
is **rug-pull-protected**: it will not run until you re-approve it.

This catches the case where a previously-trusted MCP server silently swaps a
tool's instructions to inject malicious behaviour into AI agents.

## How to fix

### Review the diff

The web UI's Quarantine panel renders the diff side-by-side.

### Re-approve

If the change is legitimate (vendor updated the docstring, added a new
parameter, etc.), approve the tool from the Quarantine panel.

### If the change is not legitimate

Disable the server until you understand what changed.

Then file an issue with the upstream maintainer.

## Related

- [Security Quarantine — rug-pull detection](../features/tool-quarantine.md)
- [`MCPX_QUARANTINE_PENDING_APPROVAL`](MCPX_QUARANTINE_PENDING_APPROVAL.md)
