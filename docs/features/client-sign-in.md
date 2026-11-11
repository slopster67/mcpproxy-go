# Client and peer sign-in

MCP clients and peers can sign in through the dashboard instead of using a
copied token. You sign in with the dashboard's OIDC provider and choose which
saved client or peer the connection acts as. Static tokens keep working; sign-in
is an alternative.

The primary is a standard OAuth 2.1 authorization server for the MCP
authorization specification: protected resource metadata (RFC 9728),
authorization server metadata (RFC 8414), dynamic client registration
(RFC 7591), the authorization code flow with PKCE (S256 only) and refresh tokens.

## MCP clients

Add only the MCP URL to your MCP client, without an `Authorization` header:

| Where the client connects | URL | Clients you can choose |
| --- | --- | --- |
| A peer on the same machine | `http://127.0.0.1:8080/mcp` (the peer's address) | Clients assigned to that peer |
| The primary, locally or over the internet | `https://mcp.example.com/mcp` | Clients that run on the primary |

On its first request the MCP client receives `401` with a `WWW-Authenticate`
challenge, registers itself and opens a browser. Sign in if needed, choose the
client and select **Allow**. The consent page shows the address the sign-in
returns to and warns when it is not on your computer; only approve sign-ins you
started.

A peer advertises the primary as its authorization server under a per-peer
issuer (`https://mcp.example.com/oauth/peers/<peer-id>`), so the consent page
lists only that peer's clients. The MCP client registers and exchanges tokens
with the primary directly; the peer forwards `/mcp` requests unchanged.

The client's assigned host decides where its tool calls run, whichever address
it connected through.

## Peers

Start a peer with only the primary URL:

```sh
MCPPROXY_PEER_PRIMARY_URL=https://mcp.example.com mcpproxy peer
```

Open the peer's address (`http://127.0.0.1:8080/` by default) in a browser on the
same machine and select **Sign in**. Choose which peer this machine
is. The peer stores a refresh token in `<data-dir>/peer-credential.json`
(owner-only), refreshes its access token on its own and reconnects after
restarts. Once signed in, the peer's `/` redirects to the primary's dashboard.

Sign-in must start from a loopback address (`localhost`, `127.0.0.1` or `::1`)
because the primary only returns peer sign-ins to loopback addresses. For a
machine without a browser, either keep using `MCPPROXY_PEER_TOKEN_FILE` or
forward the port over SSH and sign in from your own browser.

A peer runs on one machine at a time. Signing a peer in on another machine signs
out the machine that used it before; that machine shows its sign-in page again.
This applies to browser sign-ins only: a machine connected with the peer's
static token keeps connecting until you rotate the token.

To sign a peer in as a different peer, open `/oauth/start` on the peer again, or
delete `peer-credential.json` and restart it.

## Tokens and revocation

| Token | Lifetime |
| --- | --- |
| Authorization code | 1 minute, single use |
| Access token (`mcp_oat_…`) | 1 hour |
| Refresh token (`mcp_ort_…`) | 90 days, renewed on every refresh |

Tokens are signed with a key derived from the encrypted database, so they
survive primary restarts and nothing is stored per connection. Every token is
bound to the subject's current static token:

- **Rotating** a client's or peer's token signs out all of its OAuth connections.
- **Deleting** a client or peer does the same.

There is no per-connection list or revocation. A control stream opened with an
OAuth access token ends when that token expires; the peer reconnects with a
refreshed one. Access tokens for clients work only on `/mcp`, and access tokens
for peers work only on `/peer/control`.

## Endpoints

| Endpoint | Purpose |
| --- | --- |
| `/.well-known/oauth-protected-resource`, `…/mcp` | Resource metadata for the primary's `/mcp` |
| `/.well-known/oauth-authorization-server` | Primary issuer metadata |
| `/.well-known/oauth-authorization-server/oauth/peers/{peer}` | Per-peer issuer metadata |
| `/oauth/register` | Dynamic client registration (public clients only) |
| `/oauth/authorize`, `/oauth/peers/{peer}/authorize` | Starts sign-in; continues on `/ui/oauth/authorize` |
| `/oauth/token` | Code and refresh-token exchange |

Registered client IDs are signed documents that carry their redirect URIs, so
registration stores nothing. HTTPS, loopback HTTP and application-scheme
redirect URIs are accepted; loopback redirects may use any port.

Pending sign-ins and authorization codes live in memory. Restarting the primary
cancels sign-ins in progress but not completed ones.
