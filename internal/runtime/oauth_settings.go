package runtime

import (
	"context"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/upstream/core"
)

func (r *Runtime) GetServerOAuthSettings(ctx context.Context, name string) (*oauth.Settings, error) {
	snapshot := r.ConfigSnapshot()
	server := snapshot.GetServer(name)
	if server == nil {
		return nil, &ServerNotFoundError{Name: name}
	}
	result := &oauth.Settings{}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	stamp, _ := snapshot.ServerGeneration(name)
	if host := server.ExecutionHost(); host != "" {
		desired := peering.Connection{Config: server, Generation: stamp.Gen, Binding: stamp.Binding()}
		desired.Secrets = r.resolveServerSecrets(ctx, server)
		if err := r.peers.Call(ctx, host, "oauth_settings", name, desired, result); err != nil {
			result.DiscoveryError = "Peer is unavailable; automatic values could not be loaded"
		}
		return result, nil
	}
	if client, ok := r.upstreamManager.GetClient(name); ok && !client.Retired() && client.Generation() == stamp.Gen && client.CredentialBinding() == stamp.Binding() {
		result.Automatic, result.DiscoveryError = client.OAuthDefaults(ctx)
	} else {
		// Disabled servers still support discovery without starting a connection.
		client, err := core.NewClient(name, server, r.logger, nil, r.Config(), r.storageManager.GetDatabase(), r.secretResolver)
		if err != nil {
			return nil, err
		}
		client.SetCredentialBinding(stamp.Binding())
		defer client.Retire()
		result.Automatic, result.DiscoveryError = client.OAuthDefaults(ctx)
	}
	return result, nil
}
