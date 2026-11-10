package diagnostics

const (
	STDIOSpawnENOENT Code = "MCPX_STDIO_SPAWN_ENOENT"
	STDIOSpawnEACCES Code = "MCPX_STDIO_SPAWN_EACCES"

	STDIOSpawnExecFormat  Code = "MCPX_STDIO_SPAWN_EXEC_FORMAT"
	STDIOExitNonzero      Code = "MCPX_STDIO_EXIT_NONZERO"
	STDIOHandshakeTimeout Code = "MCPX_STDIO_HANDSHAKE_TIMEOUT"
)

// OAUTH domain — OAuth 2.1 / PKCE flow failures.
//
// OAuthLoginRequired and OAuthReauthRequired are actionable user-states, NOT
// faults: they describe a server waiting for the user to sign in, so they must
// never drive a "file a bug" CTA. Login-required is the first-time sign-in
// (amber/degraded); re-auth-required is a previously-working token that broke
// (red/unhealthy).
const (
	OAuthTokenUnavailable Code = "MCPX_OAUTH_TOKEN_UNAVAILABLE"
	OAuthLoginRequired    Code = "MCPX_OAUTH_LOGIN_REQUIRED"
	OAuthReauthRequired   Code = "MCPX_OAUTH_REAUTH_REQUIRED"
	OAuthRefreshExpired   Code = "MCPX_OAUTH_REFRESH_EXPIRED"
	OAuthRefresh403       Code = "MCPX_OAUTH_REFRESH_403"
	OAuthDiscoveryFailed  Code = "MCPX_OAUTH_DISCOVERY_FAILED"
	OAuthCallbackTimeout  Code = "MCPX_OAUTH_CALLBACK_TIMEOUT"
	OAuthCallbackMismatch Code = "MCPX_OAUTH_CALLBACK_MISMATCH"
)

// HTTP domain — HTTP/SSE transport failures.
const (
	HTTPDNSFailed  Code = "MCPX_HTTP_DNS_FAILED"
	HTTPTLSFailed  Code = "MCPX_HTTP_TLS_FAILED"
	HTTPUnauth     Code = "MCPX_HTTP_401"
	HTTPForbidden  Code = "MCPX_HTTP_403"
	HTTPNotFound   Code = "MCPX_HTTP_404"
	HTTPServerErr  Code = "MCPX_HTTP_5XX"
	HTTPConnRefuse Code = "MCPX_HTTP_CONN_REFUSED"
	HTTPTimeout    Code = "MCPX_HTTP_TIMEOUT"
	// HTTPConnReset: the TCP connection was reset by the peer (ECONNRESET) —
	// an idle-timeout kill by a proxy/load balancer in front of the MCP server,
	// or the upstream closing mid-response. Distinct from CONN_REFUSED (nothing
	// listening at all) and transient by nature, so it must never be parked.
	HTTPConnReset Code = "MCPX_HTTP_CONN_RESET"
	// HTTPRateLimited: the server returned 429 Too Many Requests. Its own
	// category rather than a generic 4xx because the remediation is "wait / slow
	// down", never "fix your config" — and mcpproxy already honours the
	// Retry-After header on this status (internal/transport/retry_after.go).
	HTTPRateLimited Code = "MCPX_HTTP_RATE_LIMITED"
	// HTTPClientErr: the enumerated client-error statuses that have no more
	// specific code of their own — 400, 408, 409, 410 and 451. (401, 403, 404
	// and 429 each have their own code; every other 4xx falls through
	// deliberately, see DiagnoseHTTPStatus.) The exact status travels in
	// DiagnosticError.Cause, the raw error text.
	HTTPClientErr Code = "MCPX_HTTP_4XX"
	// HTTPCanceled: the attempt was canceled before it finished — shutdown, a
	// config reload replacing the client, or a user-initiated disconnect. Not a
	// fault, hence severity info: it describes mcpproxy's own decision to stop
	// waiting, and telling the user to file a bug about it is noise.
	HTTPCanceled  Code = "MCPX_HTTP_CANCELED"
	HTTPLegacySSE Code = "MCPX_HTTP_LEGACY_SSE"
)

// CONFIG domain — configuration parsing and validation failures.
const (
	ConfigMissingSecret Code = "MCPX_CONFIG_MISSING_SECRET"
	// ConfigInvalidCommand: the server's stdio `command`/`args` pair cannot
	// spawn anything useful — e.g. a package-runner (`npx`, `uvx`) with no
	// package to run. mcpproxy detects this itself before spawning, so the
	// message is ours and the user can fix it in the deployment settings.
	ConfigInvalidCommand Code = "MCPX_CONFIG_INVALID_COMMAND"
)

// QUARANTINE domain — security quarantine failures.
const (
	QuarantinePendingApproval Code = "MCPX_QUARANTINE_PENDING_APPROVAL"
	QuarantineToolChanged     Code = "MCPX_QUARANTINE_TOOL_CHANGED"
)

// NETWORK domain — network environment failures.
const (
	NetworkOffline Code = "MCPX_NETWORK_OFFLINE"
)

// UNKNOWN — fallback when no specific classification applies.
const (
	UnknownUnclassified Code = "MCPX_UNKNOWN_UNCLASSIFIED"
)

// OAuthCredentialsUnavailableMessage identifies a failure before a request can
// obtain its OAuth header, including when a peer transports the error as text.
const OAuthCredentialsUnavailableMessage = "OAuth credentials unavailable"
