package core

import (
	"context"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

func (c *Client) OAuthDefaults(ctx context.Context) (config.OAuthConfig, string) {
	return oauth.DiscoverAutomatic(c.oauthEnv(ctx), c.GetConfig(), c.storage, c.logger)
}
