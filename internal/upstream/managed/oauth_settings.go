package managed

import (
	"context"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

func (mc *Client) OAuthDefaults(ctx context.Context) (config.OAuthConfig, string) {
	return mc.coreClient.OAuthDefaults(ctx)
}
