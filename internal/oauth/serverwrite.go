package oauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The WRITE half of the redaction rule set lives here, next to the read half in
// redactview.go, and every write door — MCP `upstream_servers
// add|update|patch`, REST `POST`/`PATCH /api/v1/servers` — calls exactly these
// functions. It lives in internal/oauth for the same reason the read half
// does: internal/httpapi sits BELOW internal/server in the import graph.
//
// The shape every door follows:
//
//  1. Revert what can be BOUND TO A KEY — a map key, a query-parameter name
//     plus the stored scheme+host, a named struct field.
//  2. REFUSE whatever still carries a mask this proxy rendered
//     (CheckServerWriteMasks). That covers the fields with no binding (argv,
//     scopes, isolation.extra_args) AND every field added to the config struct
//     later, so a new field fails CLOSED instead of persisting its own mask
//     over a live credential.

// UnmaskLiveEnvValues reverts env values a client echoed back exactly as a LIVE
// read door rendered them.
//
// It exists alongside UnmaskEnvValues because the live rendering runs the
// value-shaped detector over leaves the NAME rule left alone (`env: {BENIGN:
// mcp_agt_…}`) — a rendering UnmaskEnvValues cannot recognise, since it compares
// against maskedEnvValue. Without it, a read-modify-write would persist that
// mask over the secret.
//
// Binding is by KEY, exactly as UnmaskEnvValues binds, so a value can only ever
// be restored to the variable it was read from.
func UnmaskLiveEnvValues(incoming, stored map[string]string) map[string]string {
	return unmaskMapValues(incoming, stored, LiveRedaction.EnvValue)
}

// UnmaskLiveHeaders is UnmaskLiveEnvValues for header maps.
func UnmaskLiveHeaders(incoming, stored map[string]string) map[string]string {
	return unmaskMapValues(incoming, stored, LiveRedaction.HeaderValue)
}

// UnmaskLiveURL reverts a masked URL on the write path, and REFUSES the write
// when a mask it cannot bind survives.
//
// Three steps, widest binding first:
//
//  1. The whole URL echoed back byte for byte — revert to the stored URL.
//  2. A genuine edit — UnmaskURL restores the userinfo password and the
//     SENSITIVE query params under its own authority safeguard, then
//     UnmaskLiveURLParams restores, PER PARAMETER, anything the value-shaped
//     detector masked under a parameter name no matcher recognises.
//  3. Whatever still carries a mask marker is refused.
//
// (2) needs the per-parameter pass and (3) must exist: for
// `https://host/old?opaque=mcp_agt_…` the LIVE detector masks the value, but
// changing `/old` to `/new` defeats the whole-URL comparison in (1) and
// UnmaskURL ignores the unrecognised `opaque` parameter. The per-parameter
// revert is bound to the parameter name and the authority and to nothing else,
// so it survives an unrelated edit; the refusal covers everything that binding cannot reach (a
// URL moved to another host, a mask in the path or the fragment, a parameter
// whose stored counterpart is gone).
func UnmaskLiveURL(incoming, stored string) (string, error) {
	if incoming == "" {
		return incoming, nil
	}
	if stored != "" {
		if incoming == LiveRedaction.URLValue(stored) {
			return stored, nil
		}
		incoming = UnmaskLiveURLParams(UnmaskURL(incoming, stored), stored)
	}
	if ContainsMaskMarker(incoming) {
		return "", errors.New("url is a redaction placeholder, not a URL: " +
			"credentials inside the URL are masked on read, and this one cannot be bound back to the stored " +
			"value (the scheme/host changed, or the credential does not sit in a query parameter). " +
			"Resend the real URL, or omit url to leave the stored one unchanged")
	}
	return incoming, nil
}

// CheckServerWriteMasks is the fail-closed net on EVERY server write door.
//
// After the key-bound reverts above have restored what they can bind, anything
// still carrying a mask this proxy rendered is refused. That is what makes the
// scheme total rather than a list of fields somebody remembered: a field with
// no revert (args, oauth.scopes, isolation.extra_args), a mask relocated to a
// field it was never read from, and a field ADDED to config.ServerConfig
// later all fail CLOSED
// instead of persisting the mask over a live credential.
//
// `what` names the thing being written, for the operator-facing message.
func CheckServerWriteMasks(what string, v interface{}) error {
	path, ok := FindMaskMarker("", NormalizeForRedaction(v))
	if !ok {
		return nil
	}
	return fmt.Errorf("%s%s is a redaction placeholder, not a value: "+
		"secrets are masked on read and this mask carries no key this proxy can bind the stored value to, "+
		"so it is never restored on write. Resend the real value, or omit the field to leave the stored one unchanged",
		what, strings.TrimPrefix(path, "."))
}

// FindMaskMarker walks a generic JSON value and returns the dotted path of the
// first string leaf still carrying a mask this proxy rendered.
//
// Walking the whole value rather than checking a hand-written field list is the
// point: it is what makes a field ADDED to the struct later fail closed instead
// of quietly round-tripping its own mask into the config.
func FindMaskMarker(path string, v interface{}) (string, bool) {
	switch typed := v.(type) {
	case map[string]interface{}:
		for k, val := range typed {
			if p, ok := FindMaskMarker(path+"."+k, val); ok {
				return p, true
			}
		}
	case []interface{}:
		for i, val := range typed {
			if p, ok := FindMaskMarker(fmt.Sprintf("%s[%d]", path, i), val); ok {
				return p, true
			}
		}
	case string:
		if ContainsMaskMarker(typed) {
			return path, true
		}
	}
	return "", false
}

// NormalizeForRedaction turns an arbitrary typed config value into generic JSON
// (maps / slices / json.Number / string / bool) so the walkers above and the
// redaction walker in internal/server can inspect it key by key. json.Number
// keeps integers exact instead of degrading them to float64 and rendering as
// 1.048576e+06. On failure the value is returned unchanged; the walkers' default
// branch then passes it through.
func NormalizeForRedaction(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	var out interface{}
	if err := dec.Decode(&out); err != nil {
		return v
	}
	return out
}
