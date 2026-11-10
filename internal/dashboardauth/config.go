package dashboardauth

import (
	"fmt"
	"net/url"
	"strings"
)

// Config authenticates dashboard administrators. The identity provider
// controls who may use this application; every admitted user is an administrator.
type Config struct {
	Issuer       string `env:"MCPPROXY_OIDC_ISSUER" file:"true"`
	ClientID     string `env:"MCPPROXY_OIDC_CLIENT_ID" file:"true"`
	ClientSecret string `env:"MCPPROXY_OIDC_CLIENT_SECRET" file:"true"`
	RedirectURL  string `env:"MCPPROXY_OIDC_REDIRECT_URL" file:"true"`
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("OIDC configuration is required in primary mode")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("MCPPROXY_OIDC_CLIENT_ID is required")
	}
	for field, raw := range map[string]string{"MCPPROXY_OIDC_ISSUER": c.Issuer, "MCPPROXY_OIDC_REDIRECT_URL": c.RedirectURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return fmt.Errorf("%s must be an HTTPS URL (HTTP is allowed on loopback)", field)
		}
		if field == "MCPPROXY_OIDC_REDIRECT_URL" && u.Path != "/auth/callback" {
			return fmt.Errorf("MCPPROXY_OIDC_REDIRECT_URL must end with /auth/callback")
		}
	}
	return nil
}
