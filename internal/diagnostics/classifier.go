package diagnostics

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strings"
	"syscall"

	"github.com/mark3labs/mcp-go/v2/client/transport"
)

// Classify maps a raw error to a stable Code. It prefers typed-error inspection
// via errors.Is / errors.As over string matching; falls back to string matching
// only when the underlying library does not expose structured error types.
//
// The hints parameter lets callers nudge the classifier with context ("this
// error came from the stdio spawn path", etc.).
//
// If no specific classification applies, Classify returns UnknownUnclassified.
func Classify(err error, hints ClassifierHints) Code {
	if err == nil {
		return ""
	}

	var coded interface{ Code() Code }
	if errors.As(err, &coded) {
		if c := coded.Code(); c != "" {
			return c
		}
	}

	// This marker is produced only by the OAuth-header acquisition path. Its
	// nested timeout belongs to authentication, not an MCP request in flight.
	if strings.Contains(err.Error(), OAuthCredentialsUnavailableMessage) {
		return OAuthTokenUnavailable
	}

	if c := classifyStdio(err, hints); c != "" {
		return c
	}
	if c := classifyHTTP(err, hints); c != "" {
		return c
	}
	if c := classifyNetwork(err, hints); c != "" {
		return c
	}
	if c := classifyOAuth(err, hints); c != "" {
		return c
	}

	if c := classifyConfig(err, hints); c != "" {
		return c
	}
	if c := classifyQuarantine(err, hints); c != "" {
		return c
	}

	return UnknownUnclassified
}

// classifyOAuth recognises OAuth 2.1 / PKCE failure surface-strings emitted
// by the upstream manager and mcp-go. Producers that want deterministic
// classification should wrap their terminal error with WrapError(code, err).
func classifyOAuth(err error, _ ClassifierHints) Code {
	msg := strings.ToLower(err.Error())
	switch {
	// Re-auth (a previously-working stored token broke) is matched BEFORE the
	// login-required backstop because "re-login available" contains the
	// "login available" substring; the order of these two cases is load-bearing.
	case strings.Contains(msg, "re-login available"),
		strings.Contains(msg, "re-authentication required"),
		strings.Contains(msg, "server error with stored token"):
		return OAuthReauthRequired
	// First-time sign-in deferred to the user (ErrOAuthPending text). These are
	// actionable user-states, not faults — keep them out of UNKNOWN.
	case strings.Contains(msg, "oauth authentication required"),
		strings.Contains(msg, "login available"),
		strings.Contains(msg, "mcpproxy auth login"):
		return OAuthLoginRequired
	case strings.Contains(msg, "refresh_token") && strings.Contains(msg, "expired"),
		strings.Contains(msg, "refresh token has expired"),
		strings.Contains(msg, "refresh token is expired"):
		return OAuthRefreshExpired
	case strings.Contains(msg, "refresh") && strings.Contains(msg, "403"),
		strings.Contains(msg, "refresh") && strings.Contains(msg, "invalid_grant"):
		return OAuthRefresh403
	case strings.Contains(msg, "oauth metadata unavailable"),
		strings.Contains(msg, "oauth discovery failed"),
		strings.Contains(msg, "discover") && strings.Contains(msg, "oauth"),
		strings.Contains(msg, ".well-known/oauth"):
		return OAuthDiscoveryFailed
	case strings.Contains(msg, "oauth callback") && strings.Contains(msg, "timeout"),
		strings.Contains(msg, "authorization timeout"):
		return OAuthCallbackTimeout
	case strings.Contains(msg, "redirect_uri") && strings.Contains(msg, "mismatch"),
		strings.Contains(msg, "redirect uri") && strings.Contains(msg, "mismatch"):
		return OAuthCallbackMismatch
	}
	return ""
}

// classifyConfig recognises configuration parsing / secret resolution failures.
func classifyConfig(err error, _ ClassifierHints) Code {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "missing secret"),
		strings.Contains(msg, "secret reference") && (strings.Contains(msg, "not found") || strings.Contains(msg, "unresolved")),
		strings.Contains(msg, "unresolved secret"):
		return ConfigMissingSecret
	// A package-runner command with nothing to run: mcpproxy's own pre-spawn
	// validation message, a config mistake the user can fix.
	case strings.Contains(msg, "has no args"),
		strings.Contains(msg, "no args") && strings.Contains(msg, "required"):
		return ConfigInvalidCommand
	}
	return ""
}

// classifyQuarantine recognises security-quarantine rejections.
func classifyQuarantine(err error, _ ClassifierHints) Code {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "quarantine") && (strings.Contains(msg, "pending") || strings.Contains(msg, "requires approval") || strings.Contains(msg, "not approved")):
		return QuarantinePendingApproval
	case strings.Contains(msg, "tool") && strings.Contains(msg, "changed") && (strings.Contains(msg, "re-approval") || strings.Contains(msg, "reapprove") || strings.Contains(msg, "rug pull")):
		return QuarantineToolChanged
	}
	return ""
}

// isMCPProxyHandshakeTimeout matches only the sentence mcpproxy itself writes
// (connection_lifecycle.go). Unlike the looser "handshake timeout" substring —
// which Go's own `net/http: TLS handshake timeout` also carries — it is
// only used with an explicit stdio hint; remote initialization can time out too.
func isMCPProxyHandshakeTimeout(msg string) bool {
	return strings.Contains(msg, "did not respond to mcp initialize")
}

// classifyStdio handles os/exec spawn errors and handshake failures.
func classifyStdio(err error, hints ClassifierHints) Code {
	if hints.Transport == TransportStdio && isMCPProxyHandshakeTimeout(strings.ToLower(err.Error())) {
		return STDIOHandshakeTimeout
	}

	var execErr *exec.Error
	if errors.As(err, &execErr) {
		// exec.Error wraps os.PathError which wraps syscall.Errno; ENOENT/EACCES
		// are the two we care about.
		if errors.Is(execErr.Err, syscall.ENOENT) {
			return STDIOSpawnENOENT
		}
		if errors.Is(execErr.Err, syscall.EACCES) {
			return STDIOSpawnEACCES
		}
		if errors.Is(execErr.Err, syscall.ENOEXEC) {
			return STDIOSpawnExecFormat
		}
	}

	// exec.ExitError — process started but exited non-zero during handshake.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return STDIOExitNonzero
	}

	if hints.Transport == TransportStdio && errors.Is(err, context.DeadlineExceeded) {
		return STDIOHandshakeTimeout
	}

	return ""
}

// Canonical transport families the classifier reasons about.
//
// ClassifierHints.Transport carries whatever the resolver produced, which is
// not a closed set: config.Protocol verbatim when set ("http", "sse",
// "streamable-http", "auto"), and "streamable-http" when not. All of them
// name the same HTTP transport family.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// CanonicalTransport folds every spelling of the HTTP transport family down to
// TransportHTTP, and passes anything else through lower-cased.
//
// It is the single definition of "this failure came over HTTP", used by
// hints.For and by classifyHTTP itself, so a hand-built ClassifierHints is
// normalized too.
//
// The empty string is deliberately NOT a member. It reaches hints.For only
// when nothing is known about the transport, and several HTTP-gated arms carry
// no HTTP-specific evidence: context.DeadlineExceeded, its stringified form and
// context.Canceled are transport-agnostic, and the status-text arm would also
// match a stdio child's stderr quoting an HTTP status the child saw. Unknown
// transports therefore stay MCPX_UNKNOWN_UNCLASSIFIED.
func CanonicalTransport(t string) string {
	normalized := strings.ToLower(strings.TrimSpace(t))
	switch normalized {
	case "auto", "http", "https", "sse", "streamable-http", "streamable_http", "streamablehttp":
		return TransportHTTP
	default:
		return normalized
	}
}

// classifyHTTP handles HTTP/SSE transport errors including TLS, DNS, and
// structured HTTP status errors. HTTP status classification prefers a typed
// statusError (DiagnoseHTTPStatus below) but also falls back to a string match
// because the upstream layer commonly stringifies the error before bubbling it
// up.
func classifyHTTP(err error, hints ClassifierHints) Code {
	httpFamily := CanonicalTransport(hints.Transport) == TransportHTTP

	// DNS lookup errors are reported as *net.DNSError.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return HTTPDNSFailed
	}

	// TLS verification: mcp-go surfaces these as *tls.CertificateVerificationError
	// in recent releases; we avoid a direct import dependency by string match.
	msg := err.Error()
	lmsg := strings.ToLower(msg)
	if strings.Contains(msg, "x509:") || strings.Contains(msg, "tls: ") || strings.Contains(msg, "certificate") {
		return HTTPTLSFailed
	}

	// Connection refused — syscall.ECONNREFUSED wrapped by net.OpError.
	if errors.Is(err, syscall.ECONNREFUSED) {
		return HTTPConnRefuse
	}

	// Connection reset by peer. Ungated, like the ECONNREFUSED arm above: a
	// socket errno is positive evidence of a socket. EOF/EPIPE are not matched
	// here; the upstream layer rewrites those into the stdio
	// "exited before completing the MCP initialize handshake" shape.
	if errors.Is(err, syscall.ECONNRESET) {
		return HTTPConnReset
	}

	// HTTP request timeouts. The upstream HTTP transport bubbles
	// context.DeadlineExceeded up wrapped in a free-text "transport error: ...
	// context deadline exceeded" string. Try the typed errors.Is path first
	// (cheap, exact); fall back to substring on the http transport hint to
	// catch the stringified form.
	if errors.Is(err, context.DeadlineExceeded) && httpFamily {
		return HTTPTimeout
	}
	if httpFamily && strings.Contains(lmsg, "context deadline exceeded") {
		return HTTPTimeout
	}

	// The other timeout shape, which neither arm above can see: a socket
	// deadline (`read tcp …: i/o timeout`) and an http.Client Timeout are
	// net.Error values reporting Timeout()==true, and neither is
	// context.DeadlineExceeded. Checked after the DNS and refused/reset errnos
	// so a more specific typed cause keeps its own code (those report
	// Timeout()==false in any case).
	var netErr net.Error
	if httpFamily && errors.As(err, &netErr) && netErr.Timeout() {
		return HTTPTimeout
	}

	// Cancellation is mcpproxy's OWN decision to stop waiting — shutdown, a
	// config reload swapping the client, a manual disconnect. It is a lifecycle
	// event, not a fault, so it gets an info-severity code of its own rather
	// than the "please file a bug report" CTA that MCPX_UNKNOWN_UNCLASSIFIED
	// carries. Typed only: context.Canceled has no stable stringification worth
	// matching, and errors.Is cannot confuse it with DeadlineExceeded.
	if httpFamily && errors.Is(err, context.Canceled) {
		return HTTPCanceled
	}

	// A 4xx on the streamable-HTTP `initialize` POST is the signature of a
	// server speaking the older HTTP+SSE MCP transport, not an auth or routing
	// failure. Matched BEFORE the generic status-text fallback below, which
	// would otherwise claim it as a bare MCPX_HTTP_404/403.
	//
	// The typed check uses mcp-go's exported sentinel; the substring fallback
	// covers errors the upstream layer stringified, which breaks errors.Is.
	if errors.Is(err, transport.ErrLegacySSEServer) ||
		strings.Contains(lmsg, "likely a legacy sse server") ||
		(strings.Contains(lmsg, "4xx for initialize") && strings.Contains(lmsg, "post")) {
		return HTTPLegacySSE
	}

	// HTTP status text fallback. The upstream layer wraps non-2xx responses
	// as a plain string ("transport error: request failed with status 504: ..."),
	// so the canonical phrasing is substring-matched here.
	if httpFamily {
		if code := matchHTTPStatusText(lmsg); code != "" {
			return code
		}
	}

	return ""
}

// statusTextMarkers are the phrasings mcp-go puts a status code behind:
//
//   - "status " covers `request failed with status %d: %s` and
//     `notification failed with status %d: %s`.
//   - "status code: " covers `unexpected status code: %d` on the SSE connect
//     path, where the digits do not follow "status ".
//
// "status " is a strict prefix of "status code: ", so at a position where both
// match, the longer marker is the one that describes the message. See statusAt.
var statusTextMarkers = [...]string{"status code: ", "status "}

// matchHTTPStatusText extracts a status code from the phrasings above. Returns
// empty when no recognised status appears.
//
// The scan is positional — left to right through the message, trying every
// marker at each position — and it stops at the FIRST status token it can
// parse, whether or not that status has a code. mcp-go interpolates the raw
// response body into `request failed with status %d: %s`, and a body may quote
// statuses of its own: the first token is the transport's and must never be
// outranked by later text. For example, `request failed with status 402:
// status 401` yields no code (402 is deliberately unmapped), not MCPX_HTTP_401.
func matchHTTPStatusText(lmsg string) Code {
	for i := 0; i < len(lmsg); i++ {
		if code, found := statusAt(lmsg, i); found {
			return code
		}
	}
	return ""
}

// statusAt reads the status token at exactly position i. found reports whether
// a well-formed status token starts here at all; code is its Code, which is
// empty for a status DiagnoseHTTPStatus deliberately does not map.
//
// Markers are tried longest-first so the "status code: " reading wins over the
// "status " prefix reading of the same position ("status " is a strict prefix
// of "status code: ", and at a shared position only the longer one describes
// the message).
func statusAt(lmsg string, i int) (code Code, found bool) {
	for _, marker := range statusTextMarkers {
		if !strings.HasPrefix(lmsg[i:], marker) {
			continue
		}
		rest := lmsg[i+len(marker):]
		// Exactly three digits, NOT followed by a fourth: "status 4011" holds
		// no HTTP status, so it is not a token and the scan keeps going.
		if len(rest) >= 3 && isDigit(rest[0]) && isDigit(rest[1]) && isDigit(rest[2]) &&
			(len(rest) == 3 || !isDigit(rest[3])) {
			status := int(rest[0]-'0')*100 + int(rest[1]-'0')*10 + int(rest[2]-'0')
			return DiagnoseHTTPStatus(status), true
		}
	}
	return "", false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// classifyNetwork handles host-environment network issues.
func classifyNetwork(err error, _ ClassifierHints) Code {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// "network is unreachable" / "no route to host"
		if errors.Is(opErr.Err, syscall.ENETUNREACH) || errors.Is(opErr.Err, syscall.EHOSTUNREACH) {
			return NetworkOffline
		}
	}
	return ""
}

// DiagnoseHTTPStatus maps an HTTP status code to a Code. Returns empty if
// the status is not a known failure.
//
// The 4xx set is enumerated rather than ranged. A blanket `status >= 400 &&
// status < 500` would swallow statuses whose remediation is genuinely
// different — and, more importantly, would claim the ones an upstream layer
// resolves better than a status number can (a 401 that is really a deferred
// OAuth sign-in, a 4xx on the initialize POST that is really a legacy-SSE
// server). Each status listed here is one whose only honest advice is "read the
// status and the body"; the rest keep falling through.
func DiagnoseHTTPStatus(status int) Code {
	switch {
	case status == 401:
		return HTTPUnauth
	case status == 403:
		return HTTPForbidden
	case status == 404:
		return HTTPNotFound
	case status == 429:
		return HTTPRateLimited
	// 400 Bad Request, 408 Request Timeout, 409 Conflict, 410 Gone,
	// 451 Unavailable For Legal Reasons. All name a real server verdict. The
	// status itself reaches the user in DiagnosticError.Cause, which carries
	// the raw error text.
	case status == 400, status == 408, status == 409, status == 410, status == 451:
		return HTTPClientErr
	case status >= 500 && status <= 599:
		return HTTPServerErr
	}
	return ""
}
