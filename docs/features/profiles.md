---
title: "Profiles"
sidebar_label: "Profiles"
description: "Choose which servers and tools each client can use."
---

# Profiles

A profile defines the servers and tools a client can use. Create a client in
**Clients**, optionally assign a profile, and use its generated token to connect
to `/mcp`. MCPProxy resolves the profile from that token on every request.
Clients without a profile use **All servers**. Every client uses the global
routing mode; there are no per-mode or per-profile endpoints.

## Configure a profile

Profiles are canonical records in encrypted `primary.db`, not JSON settings.
Each has a backend-generated ID, one editable name and permission settings. In the profile editor:

- Select the servers and tools to expose.
- Use the Read, Read + write, All or None presets as a starting point.
- Select the profiles a switchable client may switch to.

Tool tiers come from server annotations or a manual classification in Tools.
Profiles select tools; they do not redefine their tiers. Tool approval and
individual enable/disable settings still apply. Code execution and MCPProxy
management are independent client permissions, configured in **Clients**.

## Tool selection

The profile API accepts this payload; the backend generates its immutable ID:

```json
{
  "name": "work",
  "servers": ["github", "docs"],
  "selected_tools": {
    "github": ["search_repositories", "get_issue"]
  },
  "switchable_to": []
}
```

A server in `servers` without a `selected_tools` entry exposes all its approved,
enabled tools, including tools added later. An entry lists exact tool names;
an empty list exposes none. A profile with no servers exposes no upstream tools.

## Client switching

For a client assigned to a profile, **Allow switching** is off by default. When
off, its assigned profile is locked. When on, the agent may use `set_profile`
to select a profile permitted by the assigned profile's `switchable_to` list.
The selection lasts for that MCP session and is checked on subsequent requests.
A client on All servers always allows switching; that setting is not editable.

Manage client assignments from Clients or the profile list. Renaming a profile
preserves its ID and client assignments. Deleting an in-use profile requires choosing
what to do with those references; a missing assigned profile denies access.