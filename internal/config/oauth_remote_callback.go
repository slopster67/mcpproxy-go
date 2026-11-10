package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const OAuthRemoteCallbackPrefix = "/oauth/callback/"

// ValidateOAuthCallbackBaseURL accepts the origin of the request that started
// a sign-in. http and https are both allowed; the origin must not carry
// credentials, a path, a query or a fragment.
func ValidateOAuthCallbackBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(raw) != raw || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return fmt.Errorf("oauth callback origin must be an absolute http(s) origin without credentials, path, query or fragment")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("oauth callback origin port %q must be between 1 and 65535", port)
		}
	}
	if host := u.Hostname(); strings.HasPrefix(u.Host, "[") && net.ParseIP(host) == nil {
		return fmt.Errorf("oauth callback origin host %q is not a valid IPv6 literal", host)
	}
	return nil
}

// PublicOAuthCallbackURI reports whether raw is a non-loopback callback that
// was registered from a browser request. Background connects reuse it so a
// later refresh keeps the redirect the provider already has.
func PublicOAuthCallbackURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		return false
	}
	return strings.HasPrefix(u.EscapedPath(), OAuthRemoteCallbackPrefix)
}

func OAuthRemoteCallbackURL(base, serverName string) (string, error) {
	if err := ValidateOAuthCallbackBaseURL(base); err != nil {
		return "", err
	}
	if base == "" {
		return "", nil
	}
	// "." and ".." survive PathEscape and the mux would normalise them away.
	if serverName == "." || serverName == ".." {
		return "", fmt.Errorf("server name %q cannot be used in a remote OAuth callback path", serverName)
	}
	return strings.TrimSuffix(base, "/") + OAuthRemoteCallbackPrefix + url.PathEscape(serverName), nil
}
