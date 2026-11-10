---
id: MCPX_UNKNOWN_UNCLASSIFIED
title: MCPX_UNKNOWN_UNCLASSIFIED
sidebar_label: UNCLASSIFIED
description: mcpproxy could not classify this failure into a specific error code.
---

# `MCPX_UNKNOWN_UNCLASSIFIED`

**Severity:** error
**Domain:** Unknown

## What happened

mcpproxy's error classifier didn't recognise the underlying failure. This is a
fallback code: every failure gets *some* code, but for novel error shapes the
catalog doesn't have a specific one yet.

When you see this code, it usually means **mcpproxy needs a new specific code**
for the situation — please file a bug so we can add it.

## How to fix

### File a bug report

The error panel includes a **Report a bug** fix-step that pre-fills the issue
template. Or:

```bash
mcpproxy feedback "MCPX_UNKNOWN_UNCLASSIFIED for <server-name>: <one-line summary>"
```

Please include:

- The full error text shown in the panel.
- Your platform and mcpproxy version (`mcpproxy version`).
- Whether the upstream is HTTP, stdio, isolated, etc.

### Workarounds

  it sometimes catches issues the runtime classifier misses.

## Related

- [Diagnostics overview](README.md)
