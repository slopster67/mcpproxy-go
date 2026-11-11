---
id: reverse-proxy
title: Reverse Proxy Deployment
sidebar_label: Reverse Proxy
description: 'Run MCPProxy behind nginx or Caddy — fix "403 Forbidden: invalid Host header" with trusted_hosts, and secure the exposed endpoint.'
keywords: [reverse proxy, nginx, caddy, trusted_hosts, invalid Host header, DNS rebinding, 403 Forbidden, Host header]
---

# Reverse Proxy Deployment

MCPProxy listens on a loopback address by default (`127.0.0.1:8080`) and is designed
to run locally. You can still put it behind a reverse proxy (nginx, Caddy, Traefik,
CloudPanel) to add HTTPS, a public hostname, or shared access — but three things need
attention: **Host-header validation** and **authentication**.

## The `403 Forbidden: invalid Host header` error

When MCPProxy listens on a loopback address, it rejects any request whose `Host`
header is **not itself a loopback address**, returning:

```
403 Forbidden: invalid Host header
```

This is **DNS-rebinding protection**. It stops a malicious website from rebinding its
own domain to `127.0.0.1` and driving a victim's browser into the local MCP server.
The side effect: a reverse proxy that forwards the real public domain in the `Host`
header (e.g. `mcp.example.com`) is rejected the same way.

The fix is to add your public domain(s) to the `trusted_hosts` allowlist.

## `trusted_hosts` configuration

```json
{
  "listen": "127.0.0.1:8080",
  "trusted_hosts": ["mcp.example.com"]
}
```

| Behaviour | Detail |
|-----------|--------|
| Matching | Hostnames, compared **case-insensitively** |
| Without a port | `"mcp.example.com"` matches that host on **any** port |
| With a port | `"mcp.example.com:8443"` requires an **exact** port match |
| Subdomain wildcard | A leading dot — `".example.com"` — matches `example.com` **and every subdomain** (`mcp.example.com`, `a.b.example.com`), same convention as Django's `ALLOWED_HOSTS` and Vite's `server.allowedHosts` |
| Disable entirely | The single entry `"*"` turns Host **and Origin** validation off. **Not recommended:** it re-opens DNS rebinding — any website the local user visits can then drive requests into the proxy |
| Loopback | `localhost`, `127.0.0.1`, `[::1]` are **always** accepted — no need to list them |
| Non-loopback listeners | If `listen` is already a non-loopback address, Host validation never runs |
| Unix socket | Socket/named-pipe connections are never subject to Host validation |
| Default | Empty (`[]`) = full DNS-rebinding protection |

- **Environment override:** `MCPPROXY_TRUSTED_HOSTS` (comma-separated), e.g.
  `MCPPROXY_TRUSTED_HOSTS="mcp.example.com,mcp.internal:8443"`.
- **Hot-reloadable:** editing the config file applies without a restart.

`trusted_hosts` decides only which `Host` values a **loopback** listener accepts. It
never runs on a non-loopback listener (the Docker image's `0.0.0.0:8080`), and it is the
only setting a reverse proxy needs.

### Origin validation (browser requests)

Per the MCP specification's security best practices, MCPProxy also validates the
`Origin` header on loopback listeners: when a request **carries** an `Origin` and its
host is neither loopback nor in `trusted_hosts`, it is rejected with
`403 Forbidden: invalid Origin header`. Requests **without** an `Origin` header —
every non-browser MCP client, CLI tool, and server-side integration — are unaffected.

Note that reverse proxies forward a browser's `Origin` header as-is. A browser
client served **from your public domain** works as soon as that domain is in
`trusted_hosts` (its `Origin` matches the same entry as its `Host`). A browser
frontend hosted on a **different** origin must have its own host added to
`trusted_hosts` too, or its requests are rejected.

### CORS on the REST API and `/events`

The same allowlist decides CORS on the REST API (`/api/v1/*`) and the SSE stream
(`/events`): MCPProxy echoes the request `Origin` in
`Access-Control-Allow-Origin` only when that origin is loopback or in
`trusted_hosts`, and sends no CORS headers at all otherwise. Every response
carries `Vary: Origin`.

:::caution Behaviour change
Earlier versions sent `Access-Control-Allow-Origin: *` on these endpoints
unconditionally. If a **separate** web app calls the REST API cross-origin from a
public domain, add that app's host to `trusted_hosts` — unlike the `Host` check,
which only ever applied to the MCP endpoints, this one applies to the REST
surface, so a REST-only proxied deployment may have been running with
`trusted_hosts` empty. The embedded Web UI is same-origin under `/ui/` and is
unaffected, as are non-browser clients, which send no `Origin` at all.
:::

## nginx

With `trusted_hosts` configured, a standard nginx block works without rewriting the
`Host` header:

```nginx
server {
    listen 443 ssl;
    server_name mcp.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;


        # MCPProxy streams responses (SSE at /events, streamable HTTP at /mcp).
        # Disable proxy buffering so events are delivered as they are produced.
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
```

## Caddy

Caddy forwards the request `Host` by default, so with `mcp.example.com` in
`trusted_hosts` no extra header handling is needed. Disable response buffering so
streaming works:

```caddy
mcp.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
    }
}
```

`flush_interval -1` disables Caddy's response buffering (the equivalent of nginx's
`proxy_buffering off`), which streaming endpoints require.

## Authentication through the proxy

Both endpoint families always require a credential:

- **REST API and dashboard (`/api/v1/...`, `/ui/`)** — an OIDC browser session
  (see [Dashboard OIDC](../features/dashboard-oidc.md)). Writes also check CSRF
  and `Origin`, so the proxy must pass the original `Host` and `Origin` headers.
- **MCP endpoint (`/mcp`)** — a client token created on the **Clients** page,
  sent as `Authorization: Bearer <token>`, or an OAuth access token from
  [client sign-in](../features/client-sign-in.md). A browser session is never an
  MCP credential. Route `/.well-known/` and `/oauth/` to the primary too, so MCP
  clients can discover and complete sign-in.

## Related

- [Configuration File](../configuration/environment-variables.md) — full option reference
- [Environment Variables](../configuration/environment-variables.md) — `MCPPROXY_TRUSTED_HOSTS`
- [REST API](../api/rest-api.md) — authentication details
