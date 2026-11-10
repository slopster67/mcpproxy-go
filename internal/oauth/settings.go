package oauth

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/storage"
	"go.uber.org/zap"
)

// Settings are the automatically resolved OAuth values for a server. Reading
// them never registers a client, starts a callback listener or signs in.
type Settings struct {
	Automatic      config.OAuthConfig `json:"automatic"`
	DiscoveryError string             `json:"discovery_error,omitempty"`
}

// DiscoverAutomatic reads discovery metadata even when individual endpoint or
// scope overrides exist. Results share the normal OAuth discovery cache/TTL.
func DiscoverAutomatic(env Env, server *config.ServerConfig, store storage.OAuthStore, logger *zap.Logger) (config.OAuthConfig, string) {
	// Discovery is best-effort and must not consume the connection deadline
	// needed to use explicit endpoints when the upstream's discovery is broken.
	ctx, cancel := context.WithTimeout(env.ctx(), 6*time.Second)
	defer cancel()
	env.Ctx = ctx
	automatic := config.OAuthConfig{}
	source := config.CopyServerConfig(server)
	source.OAuth = nil
	ov := discoveryOverrides{}
	metadataURL, doc := preflightMetadataURL(env, source, ov, logger)
	automatic.AuthServerMetadataURL = metadataURL
	// A metadata URL override selects the source for the other automatic fields;
	// the metadata URL's own reset value still comes from upstream discovery.
	if server.OAuth != nil && server.OAuth.AuthServerMetadataURL != "" {
		selected := server.OAuth.AuthServerMetadataURL
		var err error
		doc, err = cachedASMetadataDoc(source.URL, ov, selected, func() ([]byte, error) { return httpFetchRaw(env, selected, 5*time.Second) })
		if err != nil {
			doc = nil
		}
	}
	if doc != nil {
		automatic.AuthorizationEndpoint = doc.meta.AuthorizationEndpoint
		automatic.TokenEndpoint = doc.meta.TokenEndpoint
		automatic.RegistrationEndpoint = doc.meta.RegistrationEndpoint
		automatic.Scopes = append([]string(nil), doc.meta.ScopesSupported...)
	}
	result, err := cachedDiscover(makeDiscoveryKey("prm-scopes", source.URL, ov), source.URL, func() (prmScopeResult, error) { return preflightPRMScopes(env, source, ov, logger) })
	if err == nil && len(result.scopes) > 0 {
		automatic.Scopes = append([]string(nil), result.scopes...)
		if doc != nil && containsString(doc.meta.ScopesSupported, "offline_access") && !containsString(automatic.Scopes, "offline_access") {
			automatic.Scopes = append(automatic.Scopes, "offline_access")
		}
	}
	automatic.ExtraParams = map[string]string{}
	if resource := autoDetectResource(env, source, logger); resource != "" {
		automatic.ExtraParams["resource"] = resource
	}
	if server.OAuth == nil || server.OAuth.RedirectURI == "" {
		if callback, ok := GetCallbackServer(server.Name); ok {
			automatic.RedirectURI = callback.RedirectURI
		}
	}
	if store != nil {
		id, _, port, redirect, _, err := store.GetOAuthClientCredentialsBound(GenerateServerKey(server.Name, server.URL), env.Binding)
		if err == nil {
			automatic.ClientID = id
			if port > 0 {
				automatic.RedirectURI = fmt.Sprintf("http://%s%s", net.JoinHostPort(config.LoopbackIPv4Host, strconv.Itoa(port)), DefaultRedirectPath)
			}
			// Only an automatically allocated callback is a default, not an earlier
			// pinned redirect saved with a registration.
			if redirect != "" && (server.OAuth == nil || server.OAuth.RedirectURI == "") {
				automatic.RedirectURI = redirect
			}
		}
	}
	if doc == nil {
		return automatic, "No OAuth metadata discovered"
	}
	return automatic, ""
}
