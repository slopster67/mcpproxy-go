// Package oauth provides OAuth 2.1 authentication support for MCP servers.
// This file implements enhanced logging utilities with sensitive data redaction.
package oauth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Mask MARKERS — the substrings this package's own mask renderings are built
// from. They exist as constants, and every renderer below is written in terms
// of them, because the fail-closed write-path net has to RECOGNISE what the
// read doors rendered: MaskMarkers in redactview.go is assembled from exactly
// these constants, so a rendering cannot change without the net following.
const (
	// redactedMarker is what the REGEX scrubbers (RedactSensitiveData,
	// RedactURL, RedactHeaders) put in place of a matched secret.
	redactedMarker = "***REDACTED***"

	// bulletMarker opens every MaskValue / RecordMaskValue rendering.
	bulletMarker = "••••"
)

// Sensitive header names that should be redacted in logs.
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"x-api-key":           true,
	"cookie":              true,
	"set-cookie":          true,
	"x-access-token":      true,
	"x-refresh-token":     true,
	"x-auth-token":        true,
	"proxy-authorization": true,
}

var sensitiveHeaderSegments = map[string]bool{
	"auth":          true,
	"authorization": true,
	"bearer":        true,
	"cookie":        true,
	"token":         true,
	"secret":        true,
	"key":           true,
	"apikey":        true,
	"password":      true,
	"passwd":        true,
	"credential":    true,
	"private":       true,
	"session":       true,
}

var headerNameSegmentPattern = regexp.MustCompile(`[a-z0-9]+`)

var headerNameCamelSegmentPattern = regexp.MustCompile(`[A-Z][a-z0-9]*`)

func isSensitiveHeaderKey(name string) bool {
	if sensitiveHeaders[strings.ToLower(name)] {
		return true
	}

	for _, segment := range headerNameSegmentPattern.FindAllString(strings.ToLower(name), -1) {
		if sensitiveHeaderSegments[segment] {
			return true
		}
	}

	for _, segment := range headerNameCamelSegmentPattern.FindAllString(name, -1) {
		if sensitiveHeaderSegments[strings.ToLower(segment)] {
			return true
		}
	}

	return false
}

// Sensitive parameter names in request bodies or URLs.
var sensitiveParams = []string{
	"access_token",
	"refresh_token",
	"client_secret",
	"code",
	"password",
	"token",
	"id_token",
	"assertion",
	// Azure-SAS-style URL credentials. sig/signature are not caught by
	// secretPattern (which keys off secret|password|token|key), so they are
	// listed here — this list feeds RedactSensitiveData/RedactURL (the
	// free-form last_error / health.detail scrubbers) as well as
	// sensitiveQueryParams below.
	"sig",
	"signature",
	// AWS SigV4 credential param. The regex scrubbers match `<param>=` as a
	// suffix, so "signature" already covers X-Amz-Signature and "token" covers
	// authToken / X-Amz-Security-Token; "credential" covers X-Amz-Credential.
	"credential",
}

// tokenPattern matches Bearer tokens and other sensitive token patterns.
var tokenPattern = regexp.MustCompile(`(?i)(bearer\s+)[a-zA-Z0-9\-_\.]+`)

// secretPattern matches common secret patterns.
var secretPattern = regexp.MustCompile(`(?i)(secret|password|token|key)["']?\s*[:=]\s*["']?[a-zA-Z0-9\-_\.]+`)

// urlUserinfoPattern matches the `scheme://user:password@` prefix of a URL
// embedded in free-form text.
//
// RedactURLQueryParams masks the userinfo password of a value already known to
// be a URL. The free-form scrubbers — connection errors, tailed log lines,
// health.detail — see a URL buried in a sentence, and neither tokenPattern
// (bearer only) nor secretPattern (`<name>=<value>`) nor the `<param>=` sweep
// has a `user:pass@` shape, so this pattern covers it.
//
// The username is deliberately kept: it is the operator's signal for WHICH
// credential failed, and it is not the secret.
var urlUserinfoPattern = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^\s/:@]+):([^\s/@]+)@`)

// redactURLUserinfo masks the password half of every `scheme://user:pass@`
// prefix in s, keeping the username. Shared by RedactSensitiveData (free-form
// text) and RedactURL (the parse-failure fallback) so the two cannot disagree
// about whether a basic-auth password is a secret.
func redactURLUserinfo(s string) string {
	return urlUserinfoPattern.ReplaceAllStringFunc(s, func(match string) string {
		groups := urlUserinfoPattern.FindStringSubmatch(match)
		if len(groups) != 4 {
			return match
		}
		// A ${secret:…}/${env:…} reference is a label, not a secret; masking
		// it would erase exactly what makes the line diagnosable.
		if isConfigReference(groups[3]) {
			return match
		}
		return groups[1] + groups[2] + ":" + redactedMarker + "@"
	})
}

// RedactSensitiveData redacts sensitive information from a string.
// It replaces tokens, secrets, and other sensitive data with redacted placeholders.
func RedactSensitiveData(data string) string {
	if data == "" {
		return data
	}

	// Redact URL userinfo passwords (scheme://user:pass@host).
	result := redactURLUserinfo(data)

	// Redact Bearer tokens
	result = tokenPattern.ReplaceAllString(result, "${1}"+redactedMarker)

	// Redact secrets and passwords
	result = secretPattern.ReplaceAllStringFunc(result, func(match string) string {
		// Find the position of = or : and redact everything after
		for _, sep := range []string{"=", ":"} {
			if idx := strings.Index(match, sep); idx != -1 {
				return match[:idx+1] + redactedMarker
			}
		}
		return redactedMarker
	})

	// Redact sensitive URL parameters
	for _, param := range sensitiveParams {
		pattern := regexp.MustCompile(`(?i)(` + param + `=)[^&\s]+`)
		result = pattern.ReplaceAllString(result, "${1}"+redactedMarker)
	}

	return result
}

// RedactHeaders creates a copy of headers with sensitive values redacted.
// Returns a map suitable for logging.
func RedactHeaders(headers http.Header) map[string]string {
	redacted := make(map[string]string)

	for key, values := range headers {
		if isSensitiveHeaderKey(key) {
			redacted[key] = redactedMarker
		} else {
			// Join multiple values and redact any sensitive data within
			value := strings.Join(values, ", ")
			redacted[key] = RedactSensitiveData(value)
		}
	}

	return redacted
}

// sensitiveEnvMarkers are substrings that, when present in an env var name
// (case-insensitive), mark its value as a secret to be masked. The list is
// deliberately broad — masking a non-secret value is safe (it just becomes
// less readable), whereas leaking a real secret is not — but the markers are
// specific enough to leave ordinary configuration (LOG_LEVEL, HOME, NODE_ENV,
// HTTP_PROXY, …) readable. Covers API_KEY/APIKEY (KEY), PASSWORD/PASSWD/PASS,
// CREDENTIAL, AUTH, BEARER, PRIVATE, CERT.
// DSN / CONNECTION_STRING / CONN_STR keys hold database connection strings
// whose embedded password must be masked whole; they contain none of the
// other markers. (DATABASE_URL / REDIS_URL are
// deliberately NOT markers: they are handled by the URL-aware default branch in
// Redaction.EnvValue so scheme/host/db stay readable.)
var sensitiveEnvMarkers = []string{
	"TOKEN", "SECRET", "KEY", "PASSWORD", "PASSWD", "PASS",
	"CREDENTIAL", "AUTH", "BEARER", "PRIVATE", "CERT",
	"DSN", "CONNECTION_STRING", "CONN_STR",
}

// IsSensitiveKeyName reports whether a NAME looks like it holds a secret, using
// the same case-insensitive marker match Redaction.EnvValue applies to env-var
// keys. Exported for callers that have to decide masking from a name they hold
// separately from the value — e.g. a command-line flag naming the argv token
// that follows it, where there is no map to hand to Redaction.EnvValue.
func IsSensitiveKeyName(name string) bool {
	return isSensitiveEnvKey(name)
}

// isSensitiveEnvKey reports whether an env var name looks like it holds a
// secret, based on a case-insensitive substring match against
// sensitiveEnvMarkers.
func isSensitiveEnvKey(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range sensitiveEnvMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// maskedEnvValue is the single source of truth for how Redaction.EnvValue renders
// one (key, value) pair. It is reused by UnmaskEnvValues on the write path so a
// value that was echoed back masked can be recognised and reverted.
//
// Sensitive-looking keys mask the whole value. For every other key the value is
// still run through RedactSensitiveData (defence in depth), and when it parses as a connection URL (postgres://user:pass@host/db,
// redis://:pass@host, …) it is passed through RedactURLQueryParams so the
// embedded userinfo password and any credential query params are masked while
// scheme/host/db stay readable.
func maskedEnvValue(key, value string) string {
	return maskedEnvValueWith(key, value, MaskValue)
}

func maskedEnvValueWith(key, value string, mask func(string) string) string {
	if isSensitiveEnvKey(key) {
		return mask(value)
	}
	if strings.Contains(value, "://") {
		return RedactURLQueryParams(value, mask)
	}
	return RedactSensitiveData(value)
}

// normalizeParamName folds a query-parameter (or env) name to a canonical form
// for sensitivity matching: lowercase with '-' and '_' separators stripped.
// This makes X-Amz-Signature, x_amz_signature and xamzsignature all compare
// equal, and access-token equal to access_token.
func normalizeParamName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		if r == '-' || r == '_' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sensitiveQueryParams holds the NORMALIZED (see normalizeParamName) query
// parameter names whose values are masked by RedactURLQueryParams. It extends
// the base sensitiveParams set (used by the log redactors) with the parameter
// names commonly seen carrying credentials in MCP / presigned-URL query strings.
// Because matching is done on the normalized name, exact-name misses such as
// X-Amz-Credential, X-Amz-Signature, X-Amz-Security-Token, authToken and
// access-token are all covered.
var sensitiveQueryParams = func() map[string]bool {
	names := make([]string, 0, len(sensitiveParams)+10)
	names = append(names, sensitiveParams...) // token, sig, signature, credential, …
	names = append(names,
		"apikey", "api_key", "key", "secret",
		"authtoken",
		"x-amz-credential", "x-amz-signature", "x-amz-security-token",
		"security-token", "session-token",
	)
	m := make(map[string]bool, len(names))
	for _, p := range names {
		m[normalizeParamName(p)] = true
	}
	return m
}()

// isSensitiveQueryParam reports whether a query-parameter name (in its raw,
// possibly percent-decoded form) is sensitive, matched on its normalized form.
func isSensitiveQueryParam(name string) bool {
	return sensitiveQueryParams[normalizeParamName(name)]
}

// RedactURLQueryParams masks the userinfo password and the sensitive query
// parameters of rawURL with mask, keeping everything else byte-identical.
func RedactURLQueryParams(rawURL string, mask func(string) string) string {
	if rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return RedactURL(rawURL)
	}
	changed := false

	// Basic-auth credentials embedded in the URL userinfo (https://user:pass@host) are just as sensitive as a query secret. Mask
	// the password, keep the username. u.String() writes RawQuery verbatim, so
	// re-encoding the userinfo does not disturb the query bytes we hand-edit
	// below.
	if u.User != nil {
		if pw, hasPW := u.User.Password(); hasPW && !isConfigReference(pw) {
			u.User = url.UserPassword(u.User.Username(), mask(pw))
			changed = true
		}
	}

	if u.RawQuery != "" {
		if masked := redactRawQueryParams(u.RawQuery, mask); masked != u.RawQuery {
			u.RawQuery = masked
			changed = true
		}
	}

	if !changed {
		return rawURL
	}
	return u.String()
}

// redactRawQueryParams masks the values of sensitive parameters in a BARE query
// string — no scheme, host or path, and no url.Parse anywhere in it.
//
// It is the one place the name rule is applied to a query, called both by
// RedactURLQueryParams (which hands it a parsed u.RawQuery) and by
// LogSafeQueryString (which has only the raw bytes of an http.Request's
// RawQuery). Keeping it parse-free is what makes the second caller FAIL-CLOSED:
// a query is not required to be parseable as part of a URL — an HTAB is legal
// in an HTTP header value and a literal '#' is legal in a request target — and
// a regex fallback on those inputs would miss the credential.
//
// The query is edited by hand rather than through url.Values.Encode(): Encode
// re-percent-encodes and reorders every parameter, which would mangle reference
// values like ${env:NAME} into an unrecognizable form and defeat the UI's
// secret store-chip detection. Here only a masked value's bytes change; every
// untouched parameter keeps its exact original bytes and position.
//
// A component with no '=' has no name to judge and is left alone; the
// value-shaped detector the callers run afterwards is what covers it.
func redactRawQueryParams(rawQuery string, mask func(string) string) string {
	if rawQuery == "" {
		return rawQuery
	}
	parts := strings.Split(rawQuery, "&")
	changed := false
	for i, part := range parts {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := part[:eq]
		decKey, keyErr := url.QueryUnescape(key)
		if keyErr != nil {
			decKey = key
		}
		if !isSensitiveQueryParam(decKey) {
			continue
		}
		decVal, valErr := url.QueryUnescape(part[eq+1:])
		if valErr != nil {
			decVal = part[eq+1:]
		}
		if isConfigReference(decVal) {
			continue
		}
		parts[i] = key + "=" + url.QueryEscape(mask(decVal))
		changed = true
	}
	if !changed {
		return rawQuery
	}
	return strings.Join(parts, "&")
}

// configRefPattern matches a value that is ENTIRELY a single ${secret:NAME} or
// ${env:VAR} reference. Anchored on both ends so a composite like `${env:NAME}garbage` — which a prefix check would wave through
// unmasked — is treated as a secret. Mirrors internal/secret's canonical
// ${type:name} shape, restricted to the two reference types the masker honours.
var configRefPattern = regexp.MustCompile(`^\$\{(?:secret|env):[^}]+\}$`)

// isConfigReference reports whether the given value is a complete
// ${secret:NAME} or ${env:VAR} reference. These aren't secrets — they
// are public labels pointing at the actual secret store — so the
// backend masker passes them through unchanged. A value that merely starts
// with a reference but has extra bytes is NOT a reference and is masked.
func isConfigReference(v string) bool {
	return configRefPattern.MatchString(v)
}

func RecordMaskValue(v string) string {
	if v == "" {
		return "(empty)"
	}
	if isConfigReference(v) {
		return v
	}
	return bulletMarker
}

func MaskValue(v string) string {
	if v == "" {
		return "(empty)"
	}
	if isConfigReference(v) {
		return v
	}
	if len(v) <= 4 {
		return bulletMarker
	}
	return bulletMarker + v[len(v)-2:] + " (" + strconv.Itoa(len(v)) + " chars)"
}

func UnmaskURL(incoming, stored string) string {
	if incoming == "" || stored == "" {
		return incoming
	}
	inU, err := url.Parse(incoming)
	if err != nil {
		return incoming
	}
	stU, err := url.Parse(stored)
	if err != nil {
		return incoming
	}

	// Never move a stored secret onto a different scheme/host.
	if inU.Scheme != stU.Scheme || inU.Host != stU.Host {
		return incoming
	}

	changed := false

	// Userinfo password: restore only if the incoming password is the mask of
	// the stored one AND the username is unchanged (same principal).
	if inU.User != nil && stU.User != nil {
		if inPW, hasPW := inU.User.Password(); hasPW {
			if stPW, stHas := stU.User.Password(); stHas &&
				inU.User.Username() == stU.User.Username() &&
				inPW == MaskValue(stPW) {
				inU.User = url.UserPassword(inU.User.Username(), stPW)
				changed = true
			}
		}
	}

	// Query params: hand-edit RawQuery (never url.Values.Encode, which reorders
	// and re-escapes) so untouched params keep their exact original bytes,
	// mirroring RedactURLQueryParams. Duplicate keys are masked at every
	// occurrence by the read path, so restore POSITIONALLY — the i-th masked
	// occurrence maps to the i-th stored occurrence — and only when the incoming
	// and stored occurrence counts for that key match.
	// A count mismatch is ambiguous, so those masks are left literal.
	if inU.RawQuery != "" {
		storedByKey := map[string][]string{} // decoded key -> ordered stored raw values
		for _, part := range strings.Split(stU.RawQuery, "&") {
			eq := strings.IndexByte(part, '=')
			if eq < 0 {
				continue
			}
			dk := queryUnescapeOr(part[:eq])
			storedByKey[dk] = append(storedByKey[dk], part[eq+1:])
		}

		parts := strings.Split(inU.RawQuery, "&")
		incomingCountByKey := map[string]int{}
		for _, part := range parts {
			eq := strings.IndexByte(part, '=')
			if eq < 0 {
				continue
			}
			incomingCountByKey[queryUnescapeOr(part[:eq])]++
		}

		occ := map[string]int{} // per-key occurrence index as we walk incoming
		queryChanged := false
		for i, part := range parts {
			eq := strings.IndexByte(part, '=')
			if eq < 0 {
				continue
			}
			key := part[:eq]
			decKey := queryUnescapeOr(key)
			if !isSensitiveQueryParam(decKey) {
				continue
			}
			n := occ[decKey]
			occ[decKey]++
			storedList, ok := storedByKey[decKey]
			if !ok || incomingCountByKey[decKey] != len(storedList) || n >= len(storedList) {
				continue
			}
			storedRaw := storedList[n]
			if queryUnescapeOr(part[eq+1:]) == MaskValue(queryUnescapeOr(storedRaw)) {
				// Restore the stored value's exact original bytes.
				parts[i] = key + "=" + storedRaw
				queryChanged = true
			}
		}
		if queryChanged {
			inU.RawQuery = strings.Join(parts, "&")
			changed = true
		}
	}

	if !changed {
		return incoming
	}
	return inU.String()
}

// queryUnescapeOr percent-decodes a query token, returning the original bytes
// when decoding fails.
func queryUnescapeOr(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return s
}

// UnmaskEnvValues is the env-map analogue of UnmaskURL. It guards against a
// client echoing a masked `env` map back on the write path. Returns incoming
// unchanged for nil.
//
// The whole-value revert is tried FIRST: if incoming exactly equals the masked
// rendering of the stored value it is reverted to the stored value outright.
// This covers whole-value masks (sensitive keys, RedactSensitiveData fallback)
// and a MALFORMED URL-shaped value that
// RedactURLQueryParams masked via its RedactURL regex fallback: UnmaskURL cannot
// re-parse such a value, so without this ordering the mask would be persisted
// over the stored secret on an unedited echo.
//
// Only when incoming differs (a genuine edit) does a well-formed URL-shaped
// value under a non-sensitive key fall to component-wise unmasking via UnmaskURL
// — which restores just the masked userinfo password / sensitive query params
// and carries the authority safeguard, letting an operator edit a non-secret
// part of a connection string (db name, an option) without the password mask
// being persisted as the real password.
func UnmaskEnvValues(incoming, stored map[string]string) map[string]string {
	if incoming == nil {
		return incoming
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		sv, ok := stored[k]
		switch {
		case !ok:
			out[k] = v
		case v == maskedEnvValue(k, sv):
			out[k] = sv
		case !isSensitiveEnvKey(k) && strings.Contains(sv, "://") && strings.Contains(v, "://"):
			out[k] = UnmaskURL(v, sv)
		default:
			out[k] = v
		}
	}
	return out
}

// UnmaskHeaders is the header-map analogue of UnmaskEnvValues. Headers are
// always whole-value masked by Redaction.HeaderValue (no component-wise URL
// masking), so a plain whole-value comparison is sufficient.
func UnmaskHeaders(incoming, stored map[string]string) map[string]string {
	return unmaskMapValues(incoming, stored, maskedHeaderValue)
}

// maskedHeaderValue is the single source of truth for how Redaction.HeaderValue
// renders one (key, value) pair; reused by UnmaskHeaders to recognise echoed
// masks.
func maskedHeaderValue(key, value string) string {
	return maskedHeaderValueWith(key, value, MaskValue)
}

// maskedHeaderValueWith is maskedHeaderValue with a caller-chosen mask. See
// Redaction.Mask for why the live and record policies render differently.
func maskedHeaderValueWith(key, value string, mask func(string) string) string {
	if isSensitiveHeaderKey(key) {
		return mask(value)
	}
	return RedactSensitiveData(value)
}

// unmaskMapValues reverts values that equal the masked rendering of their
// stored counterpart. rendered(key, storedValue) must reproduce exactly what the
// read path emitted for that pair.
func unmaskMapValues(incoming, stored map[string]string, rendered func(k, v string) string) map[string]string {
	if incoming == nil {
		return incoming
	}
	out := make(map[string]string, len(incoming))
	for k, v := range incoming {
		if sv, ok := stored[k]; ok && v == rendered(k, sv) {
			out[k] = sv
		} else {
			out[k] = v
		}
	}
	return out
}

// RedactURL redacts sensitive query parameters and the userinfo password from
// a URL string.
//
// It is also the fallback RedactURLQueryParams takes when url.Parse fails,
// which is precisely the URL a connection error is being logged about. It
// shares RedactSensitiveData's urlUserinfoPattern so the two scrubbers cannot
// drift apart.
func RedactURL(urlStr string) string {
	if urlStr == "" {
		return urlStr
	}

	result := redactURLUserinfo(urlStr)
	for _, param := range sensitiveParams {
		pattern := regexp.MustCompile(`(?i)(` + param + `=)[^&]+`)
		result = pattern.ReplaceAllString(result, "${1}"+redactedMarker)
	}

	return result
}

// LogOAuthRequest logs an OAuth HTTP request with redacted sensitive data.
// Use at debug level for comprehensive request tracing.
func LogOAuthRequest(logger *zap.Logger, method, url string, headers http.Header) {
	logger.Debug("OAuth HTTP request",
		zap.String("method", method),
		zap.String("url", RedactURL(url)),
		zap.Any("headers", RedactHeaders(headers)),
		zap.Time("timestamp", time.Now()),
	)
}

// LogOAuthResponse logs an OAuth HTTP response with redacted sensitive data.
// Use at debug level for comprehensive response tracing.
func LogOAuthResponse(logger *zap.Logger, statusCode int, headers http.Header, duration time.Duration) {
	logger.Debug("OAuth HTTP response",
		zap.Int("status_code", statusCode),
		zap.String("status", http.StatusText(statusCode)),
		zap.Any("headers", RedactHeaders(headers)),
		zap.Duration("duration", duration),
		zap.Time("timestamp", time.Now()),
	)
}

// TokenMetadata contains non-sensitive token information for logging.
type TokenMetadata struct {
	TokenType       string
	ExpiresAt       time.Time
	ExpiresIn       time.Duration
	Scope           string
	HasRefreshToken bool
}

// LogTokenMetadata logs token metadata without exposing actual token values.
// Safe to use at info level as no sensitive data is included.
func LogTokenMetadata(logger *zap.Logger, metadata TokenMetadata) {
	logger.Info("OAuth token metadata",
		zap.String("token_type", metadata.TokenType),
		zap.Time("expires_at", metadata.ExpiresAt),
		zap.Duration("expires_in", metadata.ExpiresIn),
		zap.String("scope", metadata.Scope),
		zap.Bool("has_refresh_token", metadata.HasRefreshToken),
	)
}

// LogClientConnectionAttempt logs a client connection attempt (not an actual token refresh).
// Note: This is called when retrying client.Start(), which may trigger automatic
// token refresh internally by mcp-go, but we cannot observe whether refresh actually occurred.
func LogClientConnectionAttempt(logger *zap.Logger, attempt int, maxAttempts int) {
	logger.Info("OAuth client connection attempt",
		zap.Int("attempt", attempt),
		zap.Int("max_attempts", maxAttempts),
	)
}

// LogClientConnectionSuccess logs a successful client connection.
// Note: This does NOT mean a token refresh occurred - it means the client connected.
// The mcp-go library may have used a cached token or performed automatic refresh internally.
func LogClientConnectionSuccess(logger *zap.Logger, duration time.Duration) {
	logger.Info("OAuth client connection successful",
		zap.Duration("duration", duration),
	)
}

// LogClientConnectionFailure logs a failed client connection attempt.
func LogClientConnectionFailure(logger *zap.Logger, attempt int, err error) {
	logger.Warn("OAuth client connection failed",
		zap.Int("attempt", attempt),
		zap.Error(err),
	)
}

// LogActualTokenRefreshAttempt logs an actual proactive token refresh attempt.
// This is called by RefreshManager when it initiates a token refresh operation.
func LogActualTokenRefreshAttempt(logger *zap.Logger, serverName string, tokenAge time.Duration) {
	logger.Info("OAuth token refresh attempt",
		zap.String("server", serverName),
		zap.Duration("token_age", tokenAge),
	)
}

// LogActualTokenRefreshResult logs the result of an actual token refresh operation.
// This is called by RefreshManager after a refresh attempt completes.
func LogActualTokenRefreshResult(logger *zap.Logger, serverName string, success bool, duration time.Duration, err error) {
	if success {
		logger.Info("OAuth token refresh succeeded",
			zap.String("server", serverName),
			zap.Duration("duration", duration),
		)
	} else {
		logger.Warn("OAuth token refresh failed",
			zap.String("server", serverName),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
	}
}

// LogOAuthFlowStart logs the start of an OAuth flow.
func LogOAuthFlowStart(logger *zap.Logger, serverName string, correlationID string) {
	logger.Info("Starting OAuth flow",
		zap.String("server", serverName),
		zap.String("correlation_id", correlationID),
		zap.Time("start_time", time.Now()),
	)
}

// LogOAuthFlowEnd logs the end of an OAuth flow.
func LogOAuthFlowEnd(logger *zap.Logger, serverName string, correlationID string, success bool, duration time.Duration) {
	if success {
		logger.Info("OAuth flow completed successfully",
			zap.String("server", serverName),
			zap.String("correlation_id", correlationID),
			zap.Duration("total_duration", duration),
		)
	} else {
		logger.Warn("OAuth flow failed",
			zap.String("server", serverName),
			zap.String("correlation_id", correlationID),
			zap.Duration("total_duration", duration),
		)
	}
}

// logSafeURL is LogSafeURL, the in-package spelling.
func logSafeURL(rawURL string) string {
	return LogSafeURL(rawURL)
}

// LogSafeURL renders a URL for a LOG FIELD. The configured upstream URL and
// every URL derived from it (RFC 8414 / RFC 9728 metadata candidates, the
// RFC 8707 resource, the token-store key) can carry a `?token=…`.
//
// It uses the RECORD policy rather than the name rule alone: a credential
// under an unrecognised parameter name (`?opaque=mcp_agt_…`) is invisible to
// the name rule, and a log sink has no write-path echo to protect, so the
// value-shaped detector runs as well. URLValueDeep also decodes one level of
// nesting, which is where the authorize URL carries the upstream URL.
//
// url.URL.Redacted() is deliberately NOT used: it masks the userinfo password
// only and leaves `?token=` intact.
func LogSafeURL(rawURL string) string {
	return RecordRedaction.URLValueDeep(rawURL)
}

// maxLogSafeRequestBytes bounds the work LogSafeRequestPath, LogSafeQueryString
// and LogSafeRequestURL will do on a single UNTRUSTED input.
//
// internal/httpapi's httpLoggingMiddleware runs these renderers on r.URL.Path,
// r.URL.RawQuery and r.Referer() for EVERY request, before authentication and
// after the response is written (so outside http.Server's timeouts).
// logSafeURLComponent runs the redaction rules once PER PATH SEGMENT, so an
// unbounded path built from many minimal segments would be an unauthenticated
// CPU/allocation amplifier within the 1MB MaxHeaderBytes budget.
//
// A few KB is generous for an access log line, so every entry point cuts its
// argument to this length, on a rune boundary, before any redaction rule runs:
// the cost of rendering one log field cannot scale with what the client sent.
const maxLogSafeRequestBytes = 4096

// capLogSafeRequestInput cuts s to maxLogSafeRequestBytes, marking the cut, so
// every redaction rule downstream runs over a bounded string regardless of how
// long the caller-supplied s is.
func capLogSafeRequestInput(s string) string {
	if len(s) <= maxLogSafeRequestBytes {
		return s
	}
	return s[:safeTruncateBytes(s, maxLogSafeRequestBytes)] + capEllipsis
}

// LogSafeQueryString is LogSafeURL for a BARE query string — `r.URL.RawQuery`,
// with no scheme, host or path in front of it.
//
// internal/httpapi's access logger logs the query of every request, and
// `?apikey=` is an accepted credential source.
//
// It runs the shared name rule (redactRawQueryParams) and then the same
// value-shaped detector RecordRedaction applies to a whole URL, so a credential
// under an unrecognised parameter name is still caught. It deliberately does
// NOT go through url.Parse: a request's query is not required to be parseable
// as part of a URL — an HTAB is legal inside an HTTP header value, and
// url.ParseRequestURI leaves a literal '#' in RawQuery — and the
// parse-failure fallback is a regex with no `apikey` rule.
func LogSafeQueryString(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}
	rawQuery = capLogSafeRequestInput(rawQuery)
	return RecordRedaction.finish(rawQuery, redactRawQueryParams(rawQuery, RecordRedaction.Mask))
}

// LogSafeRequestURL renders a client-supplied URL (such as the Referer) for a
// log field: userinfo and bearer tokens, the name rule per path segment, query
// and fragment, then the value-shaped detector.
func LogSafeRequestURL(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	rawURL = capLogSafeRequestInput(rawURL)
	scrubbed := redactURLUserinfo(rawURL)
	main, fragment, hasFragment := strings.Cut(scrubbed, "#")
	base, rawQuery, hasQuery := strings.Cut(main, "?")

	out := logSafeURLComponent(base)
	if hasQuery {
		out += "?" + LogSafeQueryString(rawQuery)
	}
	if hasFragment {
		out += "#" + logSafeFragment(fragment)
	}
	return out
}

// LogSafeRequestPath renders a request PATH for a log field.
//
// internal/httpapi logs `r.URL.Path` on the access line and on every
// authentication decision, and a path is as client-controlled as the query.
// The input is capped at entry; see maxLogSafeRequestBytes.
func LogSafeRequestPath(path string) string {
	return logSafeURLComponent(capLogSafeRequestInput(path))
}

// logSafeURLComponent renders the part of a URL that is NOT a query string -
// scheme, host and path, or the part of a fragment before its own '?'.
//
// The name rule runs per PATH SEGMENT, which is what makes
// `…/ui/api%6bey=<KEY>` reachable by it at all: it matches on the DECODED,
// normalised name, and only when that name is the whole of what precedes the
// '='. Then the value-shaped detector runs over the result, for a credential
// no name rule can see.
//
// ScrubUpstreamText is deliberately NOT used whole, though it is the standard
// rule for free-form text. Its secretPattern is
// unanchored, so it rewrites `/monkey=banana` to `/monkey=***REDACTED***` -
// acceptable for an error string nobody parses, a daily annoyance in the `path`
// field of an access log, and redundant here because the per-segment name rule
// covers the same ground with decoding and a real word boundary.
//
// Its other two rules are applied on their own: a `user:pass@` in an embedded URL and a `Bearer <token>` - which a
// decoded path really can carry, since r.URL.Path arrives percent-decoded - are
// exactly the shapes no name rule and no value detector recognise.
func logSafeURLComponent(s string) string {
	if s == "" {
		return s
	}
	s = tokenPattern.ReplaceAllString(redactURLUserinfo(s), "${1}"+redactedMarker)
	segments := strings.Split(s, "/")
	for i, segment := range segments {
		segments[i] = logSafePathSegment(segment)
	}
	return MaskDetectedSecrets(strings.Join(segments, "/"))
}

// logSafePathSegment applies the name rule to ONE path segment, treating
// anything after a '?' inside it as a query of its own.
//
// r.URL.Path arrives percent-DECODED, so a client that encodes its URL suffix
// — `GET /mcp/%3Fapikey%3D<KEY>`, which ServeMux still matches against the
// `/mcp/` subtree pattern — produces the path `/mcp/?apikey=<KEY>`. Splitting
// on '/' alone would leave the segment `?apikey=<KEY>`, whose parameter name
// reads as "?apikey" and matches no name rule. logSafeFragment makes the same
// allowance for a fragment.
//
// Every '?'-delimited piece is redacted, not just the first: a second '?' is
// otherwise swallowed into a parameter VALUE, where the name rule stops
// looking. The cost stays bounded by the caller's input cap — the pieces
// partition the segment, so the total work over a path is still linear in its
// (capped) length.
func logSafePathSegment(segment string) string {
	if !strings.Contains(segment, "?") {
		return LogSafeQueryString(segment)
	}
	pieces := strings.Split(segment, "?")
	for i, piece := range pieces {
		pieces[i] = LogSafeQueryString(piece)
	}
	return strings.Join(pieces, "?")
}

// logSafeFragment redacts a URL fragment. No URL renderer applies the name rule
// there, but a fragment carries `k=v` pairs as readily as a query does — hash
// routing puts a whole path and query after the '#' — so the same rule runs over
// whatever follows its first '?', or over the whole fragment when it has none.
func logSafeFragment(fragment string) string {
	if fragment == "" {
		return fragment
	}
	base, rawQuery, hasQuery := strings.Cut(fragment, "?")
	if !hasQuery {
		return logSafeURLComponent(fragment)
	}
	return logSafeURLComponent(base) + "?" + LogSafeQueryString(rawQuery)
}

// logSafeURLs is logSafeURL over a slice — the RFC 8414 candidate list is
// logged whole (`urls_tried`) and every entry is derived from the configured
// server URL.
func logSafeURLs(urls []string) []string {
	if urls == nil {
		return nil
	}
	out := make([]string, len(urls))
	for i, u := range urls {
		out[i] = logSafeURL(u)
	}
	return out
}

// logSafeErrorField renders an error as a LOG FIELD with any credential its
// text carries removed.
//
// Masking the `url` FIELD of a log statement is only half the job on an HTTP
// failure path: net/http quotes the request URL inside the error it returns, so
// `client.Post("https://host/mcp?token=SECRET", …)` renders as
//
//	Post "https://host/mcp?token=SECRET": dial tcp 10.0.0.1:443: i/o timeout
//
// and the credential the sibling `zap.String("metadata_url", logSafeURL(…))`
// masks would reach the identical log line through `zap.Error(err)`.
//
// ScrubUpstreamText rather than RedactSensitiveData: an error string has no
// enclosing key to judge it by, so the value-shaped detector has to run as well
// (`?opaque=mcp_agt_…` is invisible to the name rule). The field is named
// "error" like zap.Error's.
func logSafeErrorField(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("error", ScrubUpstreamText(err.Error()))
}

// LogSafeErrorText renders an error's text for a log field or a structured
// error leaf, with any credential it quotes removed. Exported for
// internal/upstream/core, which builds the same OAuth failure payloads.
func LogSafeErrorText(err error) string {
	if err == nil {
		return ""
	}
	return ScrubUpstreamText(err.Error())
}

// StateFingerprint renders an OAuth `state` for a LOG FIELD or an error message
//
// `state` is the CSRF nonce that binds a callback to the flow that minted it.
// It is not an access credential on its own, but it is the other half of the
// pair an attacker who has read the log needs: with the authorization code AND
// the state, a callback can be replayed against the waiting flow.
//
// The fingerprint keeps what the log lines actually use it for — correlating
// "Registered waiter" with "callback received" with "dropped, nobody waiting"
// across one flow — while carrying no bytes of the nonce itself. A truncated
// SHA-256 is enough: the values it distinguishes are the handful of flows live
// in one process.
func StateFingerprint(state string) string {
	if state == "" {
		return "(none)"
	}
	sum := sha256.Sum256([]byte(state))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

// callbackParamField renders ONE OAuth callback query parameter for a log
// field, by name.
//
// The authorization `code` is a single-use credential exchangeable for an
// access token at the token endpoint, so it is masked whole: unlike a URL, no
// part of it is diagnostic.
func callbackParamField(name, value string) string {
	switch strings.ToLower(name) {
	case "state":
		return StateFingerprint(value)
	case "code", "access_token", "id_token", "refresh_token", "token", "client_secret":
		if value == "" {
			return ""
		}
		return RecordMaskValue(value)
	case "error", "error_description", "error_uri":
		// Provider-authored free text: it has no enclosing key to judge it by
		// and providers do echo request parameters back into it.
		return ScrubUpstreamText(value)
	default:
		return RecordRedaction.Leaf(name, value)
	}
}

// LogSafeCallbackQuery renders a whole OAuth callback query string for a log
// field, parameter by parameter.
//
// The raw callback query carries the authorization code. Rendering per
// parameter rather than dropping the field keeps the diagnostic the line
// exists for: WHICH parameters the provider sent back.
func LogSafeCallbackQuery(rawQuery string) string {
	if rawQuery == "" {
		return rawQuery
	}
	if !strings.Contains(rawQuery, "=") {
		// Not a `k=v` query: a bare token, or something that is not a query
		// string at all. ParseQuery would turn it into `<whole thing>=`, which
		// is a rewrite that says nothing; the free-form rule is the honest
		// answer for a string with no parameter names to judge it by.
		return ScrubUpstreamText(rawQuery)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		// Unparseable: fall back to the free-form rule rather than publishing
		// it, and say so.
		return ScrubUpstreamText(rawQuery)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		for _, v := range values[name] {
			parts = append(parts, name+"="+callbackParamField(name, v))
		}
	}
	return strings.Join(parts, "&")
}
