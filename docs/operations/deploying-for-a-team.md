---
id: deploying-for-a-team
title: Deploying primary and peers
sidebar_label: Deploying primary and peers
---

# Deploying primary and peers

Run one primary with persistent storage and an HTTPS reverse proxy. Configure
its OIDC application to admit the people allowed to administer MCPProxy. Every
admitted dashboard user is an administrator. MCP clients use their generated
client tokens or [sign in](../features/client-sign-in.md) through the dashboard,
choosing the client they connect as. Dashboard sessions never authenticate MCP.

All deployment settings are environment variables. The primary needs a
base64-encoded 32-byte encryption key through `MCPPROXY_ENCRYPTION_KEY` or
`MCPPROXY_ENCRYPTION_KEY_FILE`. OIDC needs issuer and client ID. Register
`<address>/auth/callback` with the provider for every address you open the
dashboard on, for example `https://mcp.example.com/auth/callback`. Use
`MCPPROXY_OIDC_CLIENT_SECRET_FILE` for an agenix secret, or omit the secret for a
public client. See [Environment configuration](../configuration/environment-variables.md)
and [Encrypted storage](../features/encrypted-storage.md) for NixOS examples.

Set `MCPPROXY_DATA_DIR` to a persistent service directory. The encrypted
`primary.db/`, index, logs and `scripts/` live under that directory. Back up the
database and encryption key separately. No JSON settings document is read or
written, so there is no writable configuration volume to maintain.

Create a peer on **Clients**, then run its host with only the primary URL:

```sh
MCPPROXY_PEER_PRIMARY_URL=https://mcp.example.com mcpproxy peer
```

Open the peer's address (`http://127.0.0.1:8080/` by default) in a browser on
that machine, sign in to the primary and choose the peer. The peer keeps a
refresh token in its data directory and reconnects on its own after restarts.
Rotating or deleting the peer signs it out.

On a machine without a browser, use the peer's token instead:

```sh
MCPPROXY_DATA_DIR=/var/lib/mcpproxy-peer \
MCPPROXY_PEER_PRIMARY_URL=https://mcp.example.com \
MCPPROXY_PEER_TOKEN_FILE=/run/agenix/mcpproxy-peer-token \
mcpproxy peer
```

The peer has no management UI or primary database. Configure its servers and
clients from the primary dashboard. A peer being offline is expected; it must
reconnect to the primary before its configured grants and services are usable.

Set client profile and management/code-execution permissions on **Clients**.
Set server/tool scope on **Profiles**. Restart a process after changing its
deployment environment; ordinary entity edits apply through the dashboard/MCP.
