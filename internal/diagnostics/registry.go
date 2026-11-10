package diagnostics

var registry = map[Code]CatalogEntry{}

func init() {
	seedSTDIO()
	seedOAUTH()
	seedHTTP()
	seedCONFIG()
	seedQUARANTINE()
	seedNETWORK()
	seedUNKNOWN()
}

func register(e CatalogEntry) {
	registry[e.Code] = e
}

// --- STDIO ---------------------------------------------------------------

func seedSTDIO() {
	register(CatalogEntry{
		Code:        STDIOSpawnENOENT,
		Retry:       RetryPermanent,
		Severity:    SeverityError,
		UserMessage: "The configured command for this stdio server was not found on PATH.",
	})
	register(CatalogEntry{
		Code:        STDIOSpawnEACCES,
		Retry:       RetryPermanent,
		Severity:    SeverityError,
		UserMessage: "Permission denied executing the configured command.",
	})
	register(CatalogEntry{
		Code:        STDIOSpawnExecFormat,
		Retry:       RetryPermanent,
		Severity:    SeverityError,
		UserMessage: "The configured command is the wrong CPU architecture or not an executable (exec format error). Install a build that matches this machine.",
	})
	register(CatalogEntry{
		Code:        STDIOExitNonzero,
		Severity:    SeverityError,
		UserMessage: "The stdio server process exited with a non-zero status before handshake completed.",
	})
	register(CatalogEntry{
		Code:        STDIOHandshakeTimeout,
		Severity:    SeverityError,
		UserMessage: "The stdio server did not complete the MCP handshake within the expected time.",
	})
}

// --- OAUTH ---------------------------------------------------------------

func seedOAUTH() {
	register(CatalogEntry{
		Code:        OAuthTokenUnavailable,
		Severity:    SeverityError,
		UserMessage: "The stored sign-in for this server could not be used; please sign in again.",
	})
	register(CatalogEntry{
		Code:        OAuthLoginRequired,
		Severity:    SeverityWarn, // amber: an expected setup step, not a fault
		UserMessage: "This server needs you to sign in before it can connect.",
	})
	register(CatalogEntry{
		Code:        OAuthReauthRequired,
		Severity:    SeverityError, // red: a previously-working token broke
		UserMessage: "Your stored sign-in for this server is no longer valid; please sign in again.",
	})
	register(CatalogEntry{
		Code:        OAuthRefreshExpired,
		Severity:    SeverityError,
		UserMessage: "The OAuth refresh token has expired; you need to log in again.",
	})
	register(CatalogEntry{
		Code:        OAuthRefresh403,
		Severity:    SeverityError,
		UserMessage: "The OAuth provider rejected the refresh token (403). The token was likely revoked or the client configuration changed.",
	})
	register(CatalogEntry{
		Code:        OAuthDiscoveryFailed,
		Severity:    SeverityError,
		UserMessage: "Could not discover the OAuth metadata endpoint for this server.",
	})
	register(CatalogEntry{
		Code:        OAuthCallbackTimeout,
		Severity:    SeverityWarn,
		UserMessage: "The OAuth browser callback did not complete in time.",
	})
	register(CatalogEntry{
		Code:        OAuthCallbackMismatch,
		Severity:    SeverityError,
		UserMessage: "The OAuth callback redirect URI did not match the expected value.",
	})
}

// --- HTTP ----------------------------------------------------------------

func seedHTTP() {
	register(CatalogEntry{
		Code:        HTTPDNSFailed,
		Severity:    SeverityError,
		UserMessage: "DNS lookup failed for the configured server URL.",
	})
	register(CatalogEntry{
		Code:        HTTPTLSFailed,
		Severity:    SeverityError,
		UserMessage: "TLS verification failed when connecting to the server.",
	})
	register(CatalogEntry{
		Code:        HTTPUnauth,
		Severity:    SeverityError,
		UserMessage: "The server returned 401 Unauthorized.",
	})
	register(CatalogEntry{
		Code:        HTTPForbidden,
		Severity:    SeverityError,
		UserMessage: "The server returned 403 Forbidden.",
	})
	register(CatalogEntry{
		Code:        HTTPNotFound,
		Severity:    SeverityError,
		UserMessage: "The server returned 404 at the configured URL.",
	})
	register(CatalogEntry{
		Code:        HTTPServerErr,
		Severity:    SeverityWarn,
		UserMessage: "The server returned a 5xx response.",
	})
	register(CatalogEntry{
		Code:        HTTPConnRefuse,
		Severity:    SeverityError,
		UserMessage: "Connection refused by the server at the configured URL.",
	})
	register(CatalogEntry{
		Code:        HTTPTimeout,
		Severity:    SeverityWarn,
		UserMessage: "The upstream server did not respond in time. This is usually transient.",
	})
	// The four codes below are deliberately NOT RetryPermanent. A rate limit
	// clears, a reset connection re-dials, a cancellation was ours, and even the
	// generic 4xx bucket holds 408 (request timeout).
	register(CatalogEntry{
		Code:        HTTPConnReset,
		Severity:    SeverityWarn,
		UserMessage: "The connection to the server was reset before a reply arrived. This is usually transient.",
	})
	register(CatalogEntry{
		Code:        HTTPRateLimited,
		Severity:    SeverityWarn,
		UserMessage: "The server is rate-limiting mcpproxy (429 Too Many Requests). mcpproxy backs off before retrying, and waits out the server's Retry-After header when it sends one.",
	})
	register(CatalogEntry{
		Code:        HTTPClientErr,
		Severity:    SeverityError,
		UserMessage: "The server rejected the request with a 4xx status. The exact status is in the error detail below.",
	})
	register(CatalogEntry{
		Code:        HTTPCanceled,
		Severity:    SeverityInfo,
		UserMessage: "The connection attempt was canceled — usually a shutdown, a config reload, or a manual disconnect. No action needed unless it repeats.",
	})
	// NOT RetryPermanent, however deterministic the name sounds: mcp-go returns
	// its ErrLegacySSEServer sentinel for ANY 4xx on the initialize POST except
	// 401 (client/transport/streamable_http.go). A 429 from a rate-limited
	// upstream — or a 403/404/408 during a deploy — arrives as this exact code,
	// and parking on it would strand a server that is merely busy.
	register(CatalogEntry{
		Code:        HTTPLegacySSE,
		Severity:    SeverityError,
		UserMessage: "This endpoint rejected the streamable-HTTP handshake; it looks like a legacy SSE server.",
	})
}

// --- CONFIG --------------------------------------------------------------

func seedCONFIG() {
	register(CatalogEntry{
		Code:        ConfigInvalidCommand,
		Retry:       RetryPermanent,
		Severity:    SeverityError,
		UserMessage: "This server's command has nothing to run — a package runner like npx or uvx needs the package name in \"args\".",
	})
	register(CatalogEntry{
		Code:        ConfigMissingSecret,
		Severity:    SeverityError,
		UserMessage: "The configuration references a secret that is not defined.",
	})
}

// --- QUARANTINE ----------------------------------------------------------

func seedQUARANTINE() {
	register(CatalogEntry{
		Code:        QuarantinePendingApproval,
		Severity:    SeverityWarn,
		UserMessage: "This server has tools pending security approval; they will not run until approved.",
	})
	register(CatalogEntry{
		Code:        QuarantineToolChanged,
		Severity:    SeverityWarn,
		UserMessage: "One or more tools changed since last approval; re-approval is required (rug-pull protection).",
	})
}

// --- NETWORK -------------------------------------------------------------

func seedNETWORK() {
	register(CatalogEntry{
		Code:        NetworkOffline,
		Severity:    SeverityError,
		UserMessage: "Network appears to be offline.",
	})
}

// --- UNKNOWN -------------------------------------------------------------

func seedUNKNOWN() {
	// No message: surfaces show the raw error instead.
	register(CatalogEntry{
		Code:     UnknownUnclassified,
		Severity: SeverityError,
	})
}
