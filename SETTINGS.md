# MCPProxy settings

MCPProxy is configured only through environment variables, read once at startup.
There are no deployment flags, JSON configuration files, file discovery, writes,
or reloads. Unset or empty variables use built-in defaults. Restart to apply changes.

`primary` means `mcpproxy serve`; `peer` means `mcpproxy peer`; `both` works in
either mode. Unknown `MCPPROXY_*` variables and nonempty variables for the wrong
mode fail startup. General help/version controls are not settings.

Only the encryption key, OIDC client secret, peer token, trusted hosts and peer
primary URL accept `_FILE` inputs (the last two so a deployment can keep its
hostnames out of public configuration). All other settings accept direct
environment values only.
Set only one nonempty source. Files must be regular, nonempty and at most 1 MiB.
Trailing CR/LF characters are removed; other whitespace is preserved. Credential
files (encryption key, OIDC client secret, peer token) must not be world-readable;
secrets-manager group-readable files are supported. File contents are never exported back
into the process environment.

Booleans accept `true` / `false`; integers and decimals use plain numbers;
durations use Go notation such as `30s`, `2m` or `0s`. Lists are comma-separated.
Values are literal: no shell, `${env:...}` or `${secret:...}` interpolation.

Every row below comes from the single typed schema in
`internal/config/config.go` and its nested structs. Servers, profiles, clients, peers and named secrets are database-managed entities,
not deployment settings. Upstream `${env:NAME}` references and ordinary OS/proxy
variables are separate from this configuration schema.

| env | mode |
| --- | --- |
| `MCPPROXY_LISTEN` | both |
| `MCPPROXY_DATA_DIR` | both |
| `MCPPROXY_TOOLS_LIMIT` | primary |
| `MCPPROXY_TOOL_RESPONSE_LIMIT` | primary |
| `MCPPROXY_CALL_TOOL_TIMEOUT` | primary |
| `MCPPROXY_MAX_RESULT_SIZE_CHARS` | primary |
| `MCPPROXY_HEALTH_CHECK_INTERVAL` | primary |
| `MCPPROXY_TOOL_DISCOVERY_INTERVAL` | primary |
| `MCPPROXY_INIT_TIMEOUT` | primary |
| `MCPPROXY_HTTP_READ_TIMEOUT` | primary |
| `MCPPROXY_HTTP_WRITE_TIMEOUT` | primary |
| `MCPPROXY_HTTP_IDLE_TIMEOUT` | primary |
| `MCPPROXY_ENVIRONMENT_ALLOWED_SYSTEM_VARS` | primary |
| `MCPPROXY_LOG_LEVEL` | both |
| `MCPPROXY_LOG_TO_FILE` | both |
| `MCPPROXY_LOG_TO_CONSOLE` | both |
| `MCPPROXY_LOG_FILENAME` | both |
| `MCPPROXY_LOG_DIR` | both |
| `MCPPROXY_LOG_MAX_SIZE` | both |
| `MCPPROXY_LOG_MAX_BACKUPS` | both |
| `MCPPROXY_LOG_MAX_AGE` | both |
| `MCPPROXY_LOG_COMPRESS` | both |
| `MCPPROXY_TRUSTED_HOSTS`, `MCPPROXY_TRUSTED_HOSTS_FILE` | primary |
| `MCPPROXY_CODE_EXECUTION_TIMEOUT_MS` | primary |
| `MCPPROXY_CODE_EXECUTION_MAX_TOOL_CALLS` | primary |
| `MCPPROXY_CODE_EXECUTION_POOL_SIZE` | primary |
| `MCPPROXY_CODE_EXECUTION_MAX_PARALLEL` | primary |
| `MCPPROXY_OAUTH_EXPIRY_WARNING_HOURS` | primary |
| `MCPPROXY_ACTIVITY_RETENTION_DAYS` | primary |
| `MCPPROXY_ACTIVITY_MAX_RECORDS` | primary |
| `MCPPROXY_ACTIVITY_MAX_SIZE_MB` | primary |
| `MCPPROXY_ACTIVITY_MAX_RESPONSE_SIZE` | primary |
| `MCPPROXY_ACTIVITY_CLEANUP_INTERVAL_MIN` | primary |
| `MCPPROXY_TOOL_CALL_MAX_RESPONSE_SIZE` | primary |
| `MCPPROXY_TOOL_CALL_MAX_RECORDS_PER_SERVER` | primary |
| `MCPPROXY_OUTPUT_VALIDATION_MODE` | primary |
| `MCPPROXY_OUTPUT_VALIDATION_MAX_BYTES` | primary |
| `MCPPROXY_OUTPUT_VALIDATION_MAX_DEPTH` | primary |
| `MCPPROXY_OUTPUT_VALIDATION_MISSING_STRUCTURED_CONTENT` | primary |
| `MCPPROXY_OBSERVABILITY_USAGE_CACHE_TTL` | primary |
| `MCPPROXY_OBSERVABILITY_USAGE_PERSIST_INTERVAL` | primary |
| `MCPPROXY_ROUTING_MODE` | primary |
| `MCPPROXY_TOOL_RESPONSE_MODE` | primary |
| `MCPPROXY_DIRECT_TOOL_RESPONSE_MODE` | primary |
| `MCPPROXY_INSTRUCTIONS` | primary |
| `MCPPROXY_ADVERTISE_UPSTREAM_SERVERS` | primary |
| `MCPPROXY_ENCRYPTION_KEY`, `MCPPROXY_ENCRYPTION_KEY_FILE` | primary |
| `MCPPROXY_OIDC_ISSUER` | primary |
| `MCPPROXY_OIDC_CLIENT_ID` | primary |
| `MCPPROXY_OIDC_CLIENT_SECRET`, `MCPPROXY_OIDC_CLIENT_SECRET_FILE` | primary |
| `MCPPROXY_OIDC_REDIRECT_URL` | primary |
| `MCPPROXY_PEER_PRIMARY_URL`, `MCPPROXY_PEER_PRIMARY_URL_FILE` | peer |
| `MCPPROXY_PEER_TOKEN`, `MCPPROXY_PEER_TOKEN_FILE` | peer |

`MCPPROXY_LOG_TO_FILE` is a boolean controlling log output, not a file input.

Primary storage defaults to `~/.mcpproxy`; peer storage defaults to
`~/.mcpproxy-peer`. Both listen on `127.0.0.1:8080` by default and put logs in
`<data-dir>/logs`. Primary stored scripts live in `<data-dir>/scripts`.
Primary requires a base64-encoded 32-byte encryption key. Peer requires the
primary HTTP(S) origin and its peer token. Primary requires OIDC issuer,
client ID and callback URL. Omit the OIDC secret for a public client. Startup
validates provider discovery and signing keys before opening the database or
listener. Confidential client credentials are also checked when the provider
supports authenticated introspection; otherwise they are verified at sign-in.
