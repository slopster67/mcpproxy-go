---
id: environment-variables
title: Environment configuration
sidebar_label: Environment configuration
sidebar_position: 1
---

# Environment configuration

Run `mcpproxy serve` for primary mode, or `mcpproxy peer` for peer mode.
All settings come from environment variables and built-in defaults. There are no
configuration flags or JSON files. Settings are fixed at startup; restart after
changing them. See [the complete settings inventory](../../SETTINGS.md).

Only the encryption key, the OIDC settings, peer token and peer primary URL
accept `_FILE` inputs. Other settings are direct environment values only. Use one nonempty source
per setting. File values are read at startup, without exporting their contents
into the environment. Trailing line endings are removed. Credential files must
not be world-readable. Unreadable, empty or non-regular files fail startup.
Booleans use `true`/`false`, durations use values such as `30s`, and lists use
commas. Values are literal and are not expanded as secret/environment references.
Unknown `MCPPROXY_*` names and settings for the wrong mode fail startup.

## Primary

```sh
export MCPPROXY_DATA_DIR=/var/lib/mcpproxy
export MCPPROXY_LISTEN=127.0.0.1:8080
export MCPPROXY_ENCRYPTION_KEY_FILE=/run/agenix/mcpproxy-key
export MCPPROXY_OIDC_ISSUER=https://id.example.com
export MCPPROXY_OIDC_CLIENT_ID=mcpproxy
export MCPPROXY_OIDC_CLIENT_SECRET_FILE=/run/agenix/mcpproxy-oidc-secret
mcpproxy serve
```

Primary data defaults to `~/.mcpproxy`. Servers, profiles, clients, peers,
secrets and OAuth tokens live in encrypted `primary.db`; deployment settings
are never written there. Scripts live in `<data-dir>/scripts`.

## Peer

```sh
export MCPPROXY_DATA_DIR=/var/lib/mcpproxy-peer
export MCPPROXY_LISTEN=127.0.0.1:8080
export MCPPROXY_PEER_PRIMARY_URL=https://mcp.example.com
# Optional; omit it to sign the peer in through the browser instead.
export MCPPROXY_PEER_TOKEN_FILE=/run/agenix/mcpproxy-peer-token
mcpproxy peer
```

Without a peer token, open the peer's address (by default
`http://127.0.0.1:8080/`) in a browser on the same machine and choose which
peer it is. See [Client and peer sign-in](../features/client-sign-in.md).

Peer data defaults to `~/.mcpproxy-peer`; it has no settings file, encrypted
primary database or dashboard. Server grants and upstream credentials remain in
memory. A browser sign-in keeps only its refresh token, in
`<data-dir>/peer-credential.json` (owner-only). The primary URL must be an HTTP(S) origin without path or credentials.

Both modes default to `127.0.0.1:8080`, console logging at `info`, and a file-log
directory of `<data-dir>/logs` when `MCPPROXY_LOG_TO_FILE=true`.

## NixOS / agenix

Use a service `environment` attribute for ordinary settings and `_FILE` variables
for decrypted agenix paths. Do not put secret values in Nix expressions or the
Nix store. `ExecStart` only needs the binary and `serve` or `peer`.

## Upstream environments

`${env:NAME}` reads the variable on the server's execution host. Local upstream
processes inherit the configured safe allowlist plus their own explicitly set
variables. Outbound HTTP requests use Go's standard proxy environment behavior.
GUI/headless environment detection affects browser launching for upstream OAuth;
it does not configure the dashboard or disable OAuth itself.
