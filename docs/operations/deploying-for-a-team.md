---
id: deploying-for-a-team
title: Deploying primary and peers
sidebar_label: Deploying primary and peers
---

# Deploying primary and peers

Run one primary with persistent storage and an HTTPS reverse proxy. Configure
its OIDC application to admit the people allowed to administer MCPProxy. Every
admitted dashboard user is an administrator. MCP clients use their generated
client tokens, not dashboard sessions.

All deployment settings are environment variables. The primary needs a
base64-encoded 32-byte encryption key through `MCPPROXY_ENCRYPTION_KEY` or
`MCPPROXY_ENCRYPTION_KEY_FILE`. OIDC needs issuer, client ID and the public
callback URL ending in `/auth/callback`. Use
`MCPPROXY_OIDC_CLIENT_SECRET_FILE` for an agenix secret, or omit the secret for a
public client. See [Environment configuration](../configuration/environment-variables.md)
and [Encrypted storage](../features/encrypted-storage.md) for NixOS examples.

Set `MCPPROXY_DATA_DIR` to a persistent service directory. The encrypted
`primary.db/`, index, logs and `scripts/` live under that directory. Back up the
database and encryption key separately. No JSON settings document is read or
written, so there is no writable configuration volume to maintain.

If the reverse proxy forwards a public Host/Origin header to a loopback
listener, set `MCPPROXY_TRUSTED_HOSTS` to the accepted public hostname. The
setting accepts a comma-separated list.

Create a peer on **Clients**, then run its host with:

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
