package httpx

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type publicOriginKey struct{}

// PublicOrigin is the origin the client used for this request: scheme and host,
// with no path. TLS on the connection wins. Otherwise the first
// X-Forwarded-Proto token is used when it is http or https, so a proxy that
// terminates TLS can keep the public scheme. Anything else is http.
func PublicOrigin(r *http.Request) string {
	if r == nil || r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if proto := forwardedProto(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}

func forwardedProto(raw string) string {
	if raw == "" {
		return ""
	}
	token := strings.ToLower(strings.TrimSpace(strings.Split(raw, ",")[0]))
	if token == "http" || token == "https" {
		return token
	}
	return ""
}

// WithPublicOrigin stores the origin of the HTTP request that started a flow.
// Later work, including a login that outlives the request, reads it back with
// PublicOriginFrom. An empty origin leaves ctx unchanged.
func WithPublicOrigin(ctx context.Context, origin string) context.Context {
	if origin == "" {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, publicOriginKey{}, origin)
}

// PublicOriginFrom returns the origin stored by WithPublicOrigin.
func PublicOriginFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	origin, _ := ctx.Value(publicOriginKey{}).(string)
	return origin
}

// OriginMatchesRequest reports whether a browser Origin may be reflected for
// this request. Loopback origins are accepted. Any other origin must be a
// well-formed origin whose host is this request's host and whose scheme matches
// the request scheme.
func OriginMatchesRequest(origin string, r *http.Request) bool {
	if r == nil || r.Host == "" || !OriginAllowed(origin, []string{r.Host}) {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if IsLoopbackHost(u.Host) {
		return true
	}
	https := strings.HasPrefix(PublicOrigin(r), "https://")
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		return https
	case "http", "ws":
		return !https
	default:
		return false
	}
}
