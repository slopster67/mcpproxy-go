# Dashboard sign-in with OIDC

The dashboard and REST API use OpenID Connect browser sign-in. There is no admin
API key, bearer-token login, URL credential or anonymous administration mode.
MCP clients and peers continue to use their own generated tokens.

Configure an OIDC application in Pocket ID or another standards-compliant provider:

1. Register an application with this exact redirect URI:
   `https://mcp.example.com/auth/callback`.
2. Allow the users who should administer MCPProxy to access that application.
   **Every user admitted by this application has full administrator access.**
3. Supply all provider details through the daemon's environment:

```sh
export MCPPROXY_OIDC_ISSUER=https://id.example.com
export MCPPROXY_OIDC_CLIENT_ID=your-application-client-id
export MCPPROXY_OIDC_CLIENT_SECRET=your-application-client-secret
export MCPPROXY_OIDC_REDIRECT_URL=https://mcp.example.com/auth/callback
```

`MCPPROXY_OIDC_CLIENT_SECRET` can be omitted for a public client if the provider
supports it. All other fields are required when any OIDC variable is set.
Incomplete or invalid settings prevent primary startup. If all OIDC variables are absent,
dashboard sign-in stays unavailable; it never enables anonymous administration.

OIDC settings are read once at startup, never written to disk, exposed
through a settings API or stored as named secrets in the encrypted database.
Values are literal: `${env:...}` and `${secret:...}` are not expanded here. There
are no config-file aliases. For agenix or systemd credentials, supply the file path:

```sh
export MCPPROXY_OIDC_CLIENT_SECRET_FILE=/run/agenix/mcpproxy-oidc-client-secret
```

Set only one of `MCPPROXY_OIDC_CLIENT_SECRET` and
`MCPPROXY_OIDC_CLIENT_SECRET_FILE`. The file is read at startup; trailing line
endings are removed while other whitespace is preserved. Unreadable or empty
files fail startup. Omit both variables for a public client. Keep secrets out
of the Nix store and source-controlled environment files.

Discovery comes from the issuer; no Pocket ID endpoint paths or provider-specific
code are required.

Restart after changing OIDC settings. Open `/ui/`; unauthenticated visits redirect immediately to the provider. The UI
returns to the requested page after sign-in. With no OIDC configuration, MCP and
peer connections still work; the dashboard reports that sign-in is not configured and the REST API remains
inaccessible.

Use HTTPS for both the issuer and callback in deployment. HTTP is accepted only
on loopback for local development. The callback URL is explicit rather than
inferred from request headers. A reverse proxy must route `/auth/`, `/ui/`,
`/api/` (including the `/api/v1/events` stream) and `/mcp` to the primary. In Vite development, register
`http://localhost:3000/auth/callback` and access the UI on that same origin.

UI responses include `Content-Security-Policy: frame-ancestors 'none'` to prevent
embedding the dashboard in frames, including in the Vite demo.

## Session behavior

The authorization-code flow uses PKCE (S256), a browser-bound single-use state,
a nonce and verified ID tokens. Verification covers the provider signature,
issuer, client audience and expiry. Provider access/ID tokens stay on the backend
and are discarded after sign-in; the browser receives only an opaque session
cookie. HTTPS cookies use the `__Host-` prefix, Secure, HttpOnly and SameSite=Lax.

Pending sign-in data (state, nonce, PKCE verifier and return path) lives in an
AES-GCM authenticated-encrypted, HttpOnly cookie expiring after ten minutes.
Starting sign-in allocates no server-side flow record and cannot fill a shared
pending-login table. Only successfully verified callbacks enter the short-lived
replay set. Serialized return paths are limited to 1,024 bytes so the cookie stays below
browser size limits. Restarting the server invalidates pending cookies.

Sessions last eight hours, stay in memory and end on server restart. Sign out
invalidates the local session and cancels its open event streams. It does not sign
out the user from Pocket ID or other applications. There is no token refresh or
separate MCPProxy user/password database.

REST writes require a CSRF token and validate a supplied request Origin. The UI
gets its CSRF token from `/auth/session`; cookies authenticate reads, exports and
the event stream. A client or peer token cannot authenticate REST. Trusted agents
administer MCPProxy through their management-enabled MCP client instead.

OIDC settings belong to deployment environment variables, not MCP management tools.
The protocol implementation uses [go-oidc](https://github.com/coreos/go-oidc)
and [Go OAuth2](https://pkg.go.dev/golang.org/x/oauth2).

## HTTP authentication map

| Authentication | Primary routes |
| --- | --- |
| None | `/healthz`, `/readyz`, root redirect `/`, `/ui/favicon.svg`, `/server-icons/{asset}`, `/auth/login`, unauthenticated status from `/auth/session`, CORS preflight |
| OIDC browser session | Dashboard documents and assets under `/ui/` except the favicon, all `/api/v1/*` (including `/api/v1/events`), signed-in user/CSRF details from `/auth/session`, `/auth/logout` |
| OIDC authorization flow | `/auth/callback`: browser-bound single-use state, authorization code, PKCE and verified ID token; creates the browser session |
| Client bearer token | `/mcp`; includes management tools only for clients with management permission |
| Peer bearer token | `/peer/control`, the primary/peer control channel |
| Client token plus peer credential | `/mcp` requests forwarded by a peer: client bearer token plus the peer's `X-MCPProxy-Peer-Token`; both and the client's assigned host are checked |
| Upstream OAuth flow | `/oauth/callback/{server}` and temporary local callback listeners: valid outstanding single-use OAuth state/code; no dashboard session required |

Dashboard writes and sign-out also require CSRF protection. HTTPS is terminated
by your reverse proxy; MCPProxy itself serves HTTP only.

A peer's own HTTP listener exposes only `/mcp`, authenticated by its assigned
client tokens through the primary. It has no dashboard, API, probe or callback
routes. Health aliases `/health`, `/livez`, `/ready` and bare dashboard route
aliases have been removed.

Unauthenticated UI asset requests return `401`, while document navigations
redirect directly to OIDC. Authenticated assets use private caching with
revalidation. The Vite development server checks the same backend session.

## Upstream callbacks through peers

The primary records a state hash, server, peer and expiry when an authenticated
management request starts a peer sign-in. A public callback must match that record
and consumes it before entering the peer queue. Missing, malformed, unknown,
expired and replayed callbacks return 404 without contacting the peer. The peer
independently validates and consumes its own state. Local callbacks also require
an outstanding state, keeping invalid requests from exposing execution hosts.
