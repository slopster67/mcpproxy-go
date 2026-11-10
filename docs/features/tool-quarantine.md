---
title: Tool review
sidebar_label: Tool review
description: Review new and changed upstream tools before clients can call them.
---

# Tool review

New tools and changes to approved tool definitions require review. This policy
is always enabled. The Tools page and each server's Tools tab show the same
inventory, including tools waiting for approval. The Needs review filter selects
New and Changed tools. Inspect the definition or diff, then approve individually
or approve the server's pending tools together.

The server's **Trust all tools** setting is the explicit exception: it approves
new and changed tools automatically. It is off by default. Server-level
quarantine does not exist.

Approval records, definitions and approved baselines live in the encrypted
primary database. A changed tool keeps its previous approved definition so the
UI can show the difference. Tool enable/disable is independent: a disabled tool
stays disabled even on a trusted server. Profiles and connection-host scope also
apply before a client can call a tool.
