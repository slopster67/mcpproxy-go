package oauth

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The redaction RULES live here rather than in internal/server because two
// doors serve the same server config — the MCP `upstream_servers list` payload
// and `GET /api/v1/servers` — and internal/httpapi sits BELOW internal/server
// in the import graph. One definition keeps both doors in agreement.
//
// internal/oauth also owns the mask RENDERINGS (MaskValue, RecordMaskValue),
// the name matchers (IsSensitiveKeyName) and the write-path unmaskers
// (UnmaskURL, UnmaskEnvValues, UnmaskHeaders), so the read rendering and the
// write revert are defined side by side.

// Redaction is the shared rule set for masking one server's secret-bearing
// leaves. The RULES — which key names are sensitive, which values look like
// credentials, how argv pairs up, how a URL is rendered component-wise — are
// shared by EVERY door: the MCP `upstream_servers list` /
// server reads payloads, `GET /api/v1/servers`, the
// `/events` SSE `servers.changed` payload and the activity store. Only the
// mask RENDERING differs, and that difference is the one thing this struct
// carries.
type Redaction struct {
	// Mask renders one masked secret.
	Mask func(string) string

	// DetectContracted collapses a URL-shaped rendering: the value-shaped
	// detector runs over the WHOLE string rather than component by component.
	//
	// Only the RECORD policy sets it. Activity records are never written back and are
	// never edited by an operator, so nothing constrains their rendering and
	// maximal masking wins.
	//
	// A LIVE surface cannot collapse the rendering: a connection string is
	// rendered component-wise (`postgres://u:••••(N chars)@host/db`) so an
	// operator can edit the database name without the password mask being
	// persisted as the password, and UnmaskURL recognises exactly that
	// rendering on the write path. A live surface still runs the detector, per
	// component (see maskDetectedPreservingURL), so a URL holding two
	// credentials masks both.
	DetectContracted bool

	// Limit caps each string leaf the generic walker produces, or 0 for no cap.
	//
	// It is a property of the DESTINATION, not of the rules: the activity store
	// caps every leaf so one mutation carrying a large env map cannot bloat
	// encrypted, while a LIVE read surface must not cap — a truncated URL or arg is
	// no longer equal to mask(stored), so the write-path unmasker would write
	// the truncation through.
	Limit int
}

// WithLimit returns a copy of the policy that caps each string leaf at n bytes.
func (r Redaction) WithLimit(n int) Redaction {
	r.Limit = n
	return r
}

// LiveRedaction is the policy for the LIVE read surfaces (the MCP
// `upstream_servers list` / server reads payloads and
// `GET /api/v1/servers`). It keeps MaskValue's `••••<last2> (<N> chars)`
// rendering, which the write-path unmaskers recognise.
var LiveRedaction = Redaction{Mask: MaskValue}

// RecordRedaction is the policy for anything PERSISTED or EXPORTED: the fixed
// `••••` marker, carrying neither the secret's length nor its trailing bytes.
var RecordRedaction = Redaction{Mask: RecordMaskValue, DetectContracted: true}

// Leaf masks one string leaf, judged by the field NAME that encloses it and
// then by the value's own shape.
//
// The name rule decides first — it is what keeps the payload readable and says
// WHICH credential is configured — and whatever survives it is offered to the
// value-shaped detector. A key name is never the only rule: `--endpoint=mcp_agt_…`,
// a credential under a header name no matcher recognises, or a token pasted
// into a field nobody thought of is invisible to name matching and obvious to
// the detector.
//
// `key` is the enclosing field name; pass "" for a leaf with no key (a
// positional argv token).
func (r Redaction) Leaf(key, s string) string {
	switch {
	case IsHeadersFieldName(key):
		return r.HeaderValue(key, s)
	case strings.EqualFold(key, "url"):
		return r.URLValue(s)
	default:
		// An UNCONTRACTED leaf: a plain config field, a future field, or an
		// argv token's value. The env-var rule is this package's single source
		// of truth for "does this key name look like it holds a secret": it
		// masks sensitive-looking keys, passes ${secret:…}/${env:…}
		// references through, URL-redacts connection strings, and runs
		// RedactSensitiveData over everything else.
		return r.finish(s, maskedEnvValueWith(key, s, r.Mask))
	}
}

// EnvValue masks one environment-variable leaf, reproducing maskedEnvValue's
// NAME rule exactly so UnmaskEnvValues can recognise the rendering if a client
// echoes it back on the write path, then running the value-shaped detector over
// whatever the name rule left of the original value.
func (r Redaction) EnvValue(name, value string) string {
	return r.finish(value, maskedEnvValueWith(name, value, r.Mask))
}

// HeaderValue masks one HTTP header leaf, judged by the header NAME first and
// then by the value's own shape. Both rules are needed and neither is
// sufficient: the name matcher cannot enumerate every custom credential header,
// and the detector cannot recognise an opaque value with no credential shape.
func (r Redaction) HeaderValue(name, value string) string {
	return r.finish(value, maskedHeaderValueWith(name, value, r.Mask))
}

// URLValue masks one URL leaf: the name rule first (sensitive query parameters
// and the userinfo password, rendered component-wise), then the
// value-shaped detector over every component the name rule left alone.
func (r Redaction) URLValue(s string) string {
	return r.finish(s, RedactURLQueryParams(s, r.Mask))
}

// finish runs the value-shaped detector over the output of a NAME rule.
// `original` is the leaf before the name rule ran, `masked` after it.
//
// The detector is what closes the gap the name rules structurally cannot see: a
// credential-shaped value under a benign key (`env: {BENIGN: mcp_agt_…}`), a
// header name no matcher enumerates, a query parameter called `opaque`.
//
// It is skipped in exactly ONE case: when the name rule already replaced the
// WHOLE value with this package's own mask rendering. No byte of the secret
// survives there for the detector to find, and re-masking would produce a
// rendering UnmaskEnvValues / UnmaskHeaders could not recognise on the write
// path, so a read-modify-write would persist the mask over the secret.
//
// Everything else — a partially-rewritten value, a URL rendered component-wise,
// an untouched one — still carries original bytes, so the detector runs. For a
// URL it runs PER COMPONENT so the scheme, host and component-wise userinfo
// mask the write path binds to are preserved.
func (r Redaction) finish(original, masked string) string {
	if r.DetectContracted {
		return MaskDetectedSecrets(masked)
	}
	if masked == r.Mask(original) {
		return masked
	}
	return maskDetectedPreservingURL(masked)
}

// maskDetectedPreservingURL runs the value-shaped detector over s, component by
// component when s is a whole absolute URL.
//
// The detector must not be handed a URL whole on a LIVE surface: it would
// re-mask the `••••<last2> (<N> chars)` userinfo/query renderings that
// UnmaskURL binds to on the write path. Skipping it for the whole string would
// let one masked component hide a second credential.
//
// So it runs on the components the name rule does not own: the path, the
// fragment, and every query parameter whose name no matcher recognises. Those
// are exactly the components UnmaskLiveURLParams reverts, and the ones it
// cannot are refused by the write doors' residual-mask check.
func maskDetectedPreservingURL(s string) string {
	if u, err := url.Parse(s); err != nil || u.Scheme == "" || u.Host == "" {
		return MaskDetectedSecrets(s)
	}

	main, fragment, hasFragment := strings.Cut(s, "#")
	base, rawQuery, hasQuery := strings.Cut(main, "?")

	out := maskDetectedInURLPath(base)
	if hasQuery {
		out += "?" + maskDetectedInURLQuery(rawQuery)
	}
	if hasFragment {
		out += "#" + MaskDetectedSecrets(fragment)
	}
	return out
}

// maskDetectedInURLPath runs the detector over the PATH of `scheme://authority/path`,
// leaving the scheme, the host and the userinfo — whose password the name rule
// has already rendered component-wise — byte-identical.
func maskDetectedInURLPath(base string) string {
	start := 0
	if i := strings.Index(base, "://"); i >= 0 {
		start = i + len("://")
	}
	slash := strings.IndexByte(base[start:], '/')
	if slash < 0 {
		return base
	}
	cut := start + slash
	return base[:cut] + MaskDetectedSecrets(base[cut:])
}

// maskDetectedInURLQuery runs the detector over each query parameter value the
// NAME rule did not already mask.
//
// The detector sees the RAW (still percent-encoded) value bytes, because that
// is what UnmaskLiveURLParams compares against when it reverts an echoed mask
// per parameter — the two must render the same bytes or the revert silently
// stops working.
func maskDetectedInURLQuery(rawQuery string) string {
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			// A bare token carries no parameter name at all, so nothing but the
			// detector can judge it.
			parts[i] = MaskDetectedSecrets(part)
			continue
		}
		key, value := part[:eq], part[eq+1:]
		if isSensitiveQueryParam(queryUnescapeOr(key)) {
			// The name rule already replaced this value with MaskValue's
			// rendering; re-masking it would defeat UnmaskURL.
			continue
		}
		if isConfigReference(queryUnescapeOr(value)) {
			// A ${secret:…} / ${env:…} label, not a secret.
			continue
		}
		parts[i] = key + "=" + MaskDetectedSecrets(value)
	}
	return strings.Join(parts, "&")
}

// Argv masks a command-line argument vector. Returns nil for nil.
//
// Two rules, because argv secrets announce themselves in two ways:
//
//   - The FLAG names the credential — `--api-key VALUE` and `--api-key=VALUE`.
//     Delegated to Leaf with the de-dashed flag as the key, so the same single
//     source of truth the env map uses decides, and the flag itself stays
//     readable (it is the audit signal: WHICH credential moved).
//   - The VALUE is recognisably a credential on its own — an mcpproxy-issued
//     client token or client credential — wherever it sits.
//
// BOTH rules run on BOTH spellings: Leaf ends in the detector for every leaf,
// which keeps `--endpoint=mcp_agt_…` and `--endpoint mcp_agt_…` in step by construction
// rather than by two call sites remembering to agree.
//
// Everything else round-trips verbatim: package names, subcommands, paths and
// ports are what make the payload useful.
func (r Redaction) Argv(argv []string) []string {
	return r.argvWith(argv, false, nil)
}

// argvWith is Argv with two optional log-only relaxations, both off for
// `spawnRules == false` (which reproduces Argv's behaviour byte for byte, so
// the read/write mask-echo contract Argv backs is untouched):
//
//   - commandStringRule handles elements that carry embedded whitespace — a
//     whole command LINE squeezed into one argv element, which the login-shell
//     wrap produces (`["-l","-c","npx some-mcp --api-key ..."]`) and which the
//     per-token rules below cannot see into.
//   - spawnRules swaps IsSensitiveKeyName's substring match for
//     isSensitiveSpawnFlag's qualifier-aware one, so `--max-tokens 4096` and
//     `--public-key-path /etc/x` keep the values an operator reads the spawn
//     log for. See benignFlagQualifiers.
func (r Redaction) argvWith(argv []string, spawnRules bool, commandStringRule func(string) string) []string {
	if argv == nil {
		return nil
	}
	leaf := r.Leaf
	flagNamesSecret := func(flag string) bool { return IsSensitiveKeyName(ArgvFlagKey(flag)) }
	if spawnRules {
		leaf = r.spawnLeaf
		flagNamesSecret = isSensitiveSpawnFlag
	}
	out := make([]string, len(argv))
	maskNext := false
	for i, s := range argv {
		if maskNext {
			out[i] = r.Mask(s)
			maskNext = false
			continue
		}
		if commandStringRule != nil && strings.ContainsAny(s, " \t\n\r") {
			out[i] = commandStringRule(s)
			continue
		}
		if flag, value, inline := strings.Cut(s, "="); inline && IsArgvFlag(flag) {
			out[i] = flag + "=" + leaf(ArgvFlagKey(flag), value)
			continue
		}
		// An unpaired token — a positional argument, a flag name, or the value
		// of a flag whose name says nothing. Routing through Leaf with NO
		// enclosing key is what keeps the inline and space-separated spellings
		// in step: it is the same function the inline branch delegates to, so
		// the URL rule, the env-name rule and the detector all apply to both.
		out[i] = leaf("", s)
		maskNext = IsArgvFlag(s) && flagNamesSecret(s)
	}
	return out
}

// IsArgvFlag reports whether a token is an option rather than a positional
// argument. A bare "-" or "--" is a convention, not a flag.
func IsArgvFlag(token string) bool {
	return strings.HasPrefix(token, "-") && strings.Trim(token, "-") != ""
}

// ArgvFlagKey turns `--api-key` into the key name the env/header redactors
// match on.
func ArgvFlagKey(flag string) string {
	return strings.TrimLeft(flag, "-")
}

// IsHeadersFieldName reports whether a map/leaf should be masked with HTTP
// header semantics. Covers both the `headers` config field and the
// `headers_json` tool parameter (the suffix is trimmed before the walk).
func IsHeadersFieldName(key string) bool {
	return strings.EqualFold(key, "headers")
}

// issuedCredentialPattern matches an mcpproxy-issued secret: the mcp_agt_ or
// mcp_cli_ prefix followed by at least 8 more characters. The 12-character
// display prefix (`mcp_agt_ab12`, 4 characters after the prefix) is not a
// match and stays readable.
var issuedCredentialPattern = regexp.MustCompile(`(?i)(mcp_(?:agt|cli)_)[A-Za-z0-9]{8,}`)

// MaskDetectedSecrets is the value-shaped detector: any issued client token or
// client-credential secret inside s is masked, whatever key encloses it. Text
// it does not recognise is left untouched.
func MaskDetectedSecrets(s string) string {
	if !strings.Contains(strings.ToLower(s), "mcp_") {
		return s
	}
	return issuedCredentialPattern.ReplaceAllString(s, "${1}••••")
}

// ScrubUpstreamText is the ONE rule for free-form text that originated OUTSIDE
// mcpproxy's own structured fields — an upstream connection error, a health
// detail, a diagnostic cause, a line tailed from a child server's log.
//
// Such a string has no enclosing key to judge it by, so BOTH rules run: the
// name rule (RedactSensitiveData, which masks `<param>=<value>` shapes, bearer
// tokens and URL userinfo) and then the value-shaped detector (which masks a
// credential-shaped value wherever it sits — `?opaque=mcp_agt_…`, a token a
// child process printed).
//
// Every door that publishes `last_error`, `health.detail` or
// `diagnostic.cause` uses this function, so the same string is masked the same
// way everywhere.
func ScrubUpstreamText(s string) string {
	if s == "" {
		return s
	}
	return MaskDetectedSecrets(RedactSensitiveData(s))
}

// MaskMarkers are the substrings every mask rendering that reaches a READ DOOR
// carries: bulletMarker (MaskValue, RecordMaskValue) and redactedMarker
// (RedactSensitiveData, RedactURL), each also in the percent-encoded spelling
// net/url produces when it re-serialises a URL carrying the mask. The
// fail-closed write-path net recognises both spellings: the read doors publish
// the escaped one inside URLs and the write doors compare against whatever the
// client echoes back. The doubly-escaped bullet covers a masked URL nested as
// a query-parameter value of another URL.
//
// They back up the byte-for-byte echo checks so a mask is still recognised when
// the stored value has since changed and the exact comparison no longer
// matches.
//
// maskOAuthSecret / maskExtraParams (`abc***wxyz` / `***`) are deliberately
// not markers: they render for debug logs only, which no client echoes back at
// a write, and recognising `***` would refuse legitimate values.
var MaskMarkers = []string{
	bulletMarker, url.QueryEscape(bulletMarker), url.QueryEscape(url.QueryEscape(bulletMarker)),
	redactedMarker, url.QueryEscape(redactedMarker),
}

// ContainsMaskMarker reports whether a string carries a mask this proxy
// rendered.
func ContainsMaskMarker(s string) bool {
	for _, marker := range MaskMarkers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// CheckArgvMaskEcho reports an error when an incoming argv vector still carries
// a mask this proxy rendered — either the exact masking of a stored token, or
// any token bearing a mask marker. `field` names the request parameter to quote
// back to the caller (`args_json` on the MCP surface, `args` on REST).
//
// Argv masks are NEVER reverted, they are REFUSED. An argv token has NO KEY — env values bind to the variable name, headers to the
// header name, a URL secret to its scheme+host, but an argv slot has only its
// index and its neighbours, and the caller supplies the WHOLE vector *and*
// `command` in the same request. So every candidate binding is
// caller-controlled and any revert can be steered into relocating the live
// credential into an attacker-chosen command line.
//
// Refusing rather than silently keeping the stored vector is deliberate: the
// argv field REPLACES the vector on both surfaces, so silently ignoring it
// would make the write look applied when it was not.
//
// Returns nil for a vector that carries no mask, which is every legitimate
// write: a caller that changed nothing sends the real values it already has, a
// caller that rotated a credential sends the new one.
func CheckArgvMaskEcho(field string, incoming, stored []string) error {
	if len(incoming) == 0 {
		return nil
	}

	echoed := make(map[string]struct{}, len(stored))
	for i, masked := range LiveRedaction.Argv(stored) {
		if masked != stored[i] {
			echoed[masked] = struct{}{}
		}
	}

	for i, token := range incoming {
		if _, ok := echoed[token]; !ok && !ContainsMaskMarker(token) {
			continue
		}
		return fmt.Errorf("%s[%d] is a redaction placeholder, not an argument value: "+
			"credential-shaped argv tokens are masked on read and are never restored on write, "+
			"because an argv slot carries no key to bind the secret to. "+
			"Resend the real value for that argument, or omit %s to leave the stored arguments unchanged",
			field, i, field)
	}
	return nil
}

// UnmaskLiveURLParams reverts, PER QUERY PARAMETER, a value that is exactly the
// value-shaped detector's masking of the stored value for the same parameter
// name.
//
// UnmaskURL only knows the SENSITIVE query params — the ones the name rule
// masked — so this covers a credential the live surface masked through the
// DETECTOR, under a parameter name no matcher recognises (`?opaque=mcp_agt_…`),
// when the URL was otherwise edited and the whole-URL echo check cannot match.
//
// The revert is bound to the PARAMETER NAME and to the authority, and to
// nothing else — not to the path, not to the other parameters — which is what
// makes it survive an unrelated edit. As in UnmaskURL, a stored secret is
// restored ONLY when incoming keeps the stored scheme and host:port, so a URL
// redirected at another host never receives it; duplicate keys are restored
// positionally and only when the occurrence counts match.
//
// Anything it cannot bind is left masked on purpose: the caller is then refused
// by the surface's residual-marker check rather than having a mask persisted
// over a credential.
func UnmaskLiveURLParams(incoming, stored string) string {
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
	if inU.Scheme != stU.Scheme || inU.Host != stU.Host || inU.RawQuery == "" {
		return incoming
	}

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
		if eq := strings.IndexByte(part, '='); eq >= 0 {
			incomingCountByKey[queryUnescapeOr(part[:eq])]++
		}
	}

	occ := map[string]int{}
	changed := false
	for i, part := range parts {
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := part[:eq]
		decKey := queryUnescapeOr(key)
		n := occ[decKey]
		occ[decKey]++
		storedList, ok := storedByKey[decKey]
		if !ok || incomingCountByKey[decKey] != len(storedList) || n >= len(storedList) {
			continue
		}
		storedRaw := storedList[n]
		// The detector runs over the URL's RAW bytes on the read path, so the
		// comparison is against the raw stored bytes too.
		if part[eq+1:] == MaskDetectedSecrets(storedRaw) {
			parts[i] = key + "=" + storedRaw
			changed = true
		}
	}
	if !changed {
		return incoming
	}
	inU.RawQuery = strings.Join(parts, "&")
	return inU.String()
}

// ---------------------------------------------------------------------------
// The generic walk.
//
// The walk that applies the leaf rules to a nested payload lives beside the
// rules so every door can call it. A field added under a server payload is
// masked because the walk reaches it, not because a door lists it.
// ---------------------------------------------------------------------------

// Value walks a generic JSON value (as produced by NormalizeForRedaction),
// masking every string leaf according to the key that encloses it. `key` is the
// enclosing field name — pass "" at the root, and for a slice the name of the
// slice itself.
func (r Redaction) Value(key string, v interface{}) interface{} {
	switch typed := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		isHeaders := IsHeadersFieldName(key)
		isEnv := IsEnvFieldName(key)
		for k, val := range typed {
			if s, ok := val.(string); ok {
				switch {
				case isHeaders:
					// HTTP header semantics: mask by header name.
					out[k] = r.CapString(r.HeaderValue(k, s))
					continue
				case isEnv:
					// Environment-variable semantics: mask by var name, using
					// oauth's own rendering so the write path can recognise an
					// echoed mask.
					out[k] = r.CapString(r.EnvValue(k, s))
					continue
				}
			}
			out[k] = r.Value(k, val)
		}
		return out
	case []interface{}:
		if IsArgvFieldName(key) {
			return r.argvValue(typed)
		}
		out := make([]interface{}, len(typed))
		for i, val := range typed {
			out[i] = r.Value(key, val)
		}
		return out
	case string:
		if strings.EqualFold(key, "command") {
			return r.CapString(r.CommandString(typed))
		}
		return r.CapString(r.Leaf(key, typed))
	default:
		// Bools, json.Number and nil carry no secrets and stay verbatim.
		return v
	}
}

// argvValue masks a command-line argument vector reached through the generic
// walk, using the same Argv rules the typed doors use.
//
// Non-string elements (only reachable from a malformed `args_json` payload)
// carry no flag semantics: they are walked with no enclosing key and stand in
// as an empty token, which resets the flag pairing exactly as the typed walk
// does.
func (r Redaction) argvValue(argv []interface{}) []interface{} {
	tokens := make([]string, len(argv))
	for i, item := range argv {
		if s, ok := item.(string); ok {
			tokens[i] = s
		}
	}
	masked := r.Argv(tokens)

	out := make([]interface{}, len(argv))
	for i, item := range argv {
		if _, ok := item.(string); !ok {
			out[i] = r.Value("", item)
			continue
		}
		out[i] = r.CapString(masked[i])
	}
	return out
}

// CapString applies the policy's per-leaf length cap, if any, cutting on a rune
// boundary and marking the cut.
func (r Redaction) CapString(s string) string {
	if r.Limit <= 0 || len(s) <= r.Limit {
		return s
	}
	return s[:safeTruncateBytes(s, r.Limit)] + capEllipsis
}

// capEllipsis marks a leaf the cap shortened.
const capEllipsis = "…"

// safeTruncateBytes returns the largest cut <= limit that does not split a
// UTF-8 rune.
func safeTruncateBytes(s string, limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit >= len(s) {
		return len(s)
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return cut
}

// IsEnvFieldName reports whether a map should be masked with
// environment-variable semantics. Covers both the `env` config field and the
// `env_json` tool parameter (the suffix is trimmed before the walk).
func IsEnvFieldName(key string) bool {
	return strings.EqualFold(key, "env")
}

func IsArgvFieldName(key string) bool {
	return strings.EqualFold(key, "args")
}
