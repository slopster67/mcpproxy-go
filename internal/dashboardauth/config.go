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
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("OIDC configuration is required in primary mode")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("MCPPROXY_OIDC_CLIENT_ID is required")
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return fmt.Errorf("MCPPROXY_OIDC_ISSUER must be an HTTPS URL (HTTP is allowed on loopback)")
	}
	return nil
}
