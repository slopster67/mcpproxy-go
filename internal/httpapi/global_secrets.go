package httpapi

import (
	"context"
	"sort"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/secret"
)

func (s *Server) inspectGlobalSecrets(ctx context.Context, cfg *config.Config) (*secret.ConfigSecretsResponse, error) {
	resolver := s.controller.GetSecretResolver()
	refs := []secret.Ref{}
	remote := map[string]secret.EnvVarStatus{}
	type usageKey struct{ kind, name, host string }
	usage := map[usageKey]map[string]bool{}
	serversFor := func(kind, name, host string) []string {
		names := []string{}
		for name := range usage[usageKey{kind, name, host}] {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	// Stored secret store entries remain manageable before any server references them.
	stored, err := resolver.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, ref := range stored {
		if ref.Type == "secret" {
			refs = append(refs, ref)
		}
	}
	for _, server := range cfg.Servers {
		if server == nil {
			continue
		}
		for _, ref := range resolver.References(server) {
			host := ""
			if ref.Type == "env" {
				host = server.ExecutionHost()
			}
			key := usageKey{ref.Type, ref.Name, host}
			if usage[key] == nil {
				usage[key] = map[string]bool{}
			}
			usage[key][server.Name] = true
			if ref.Type != "env" || server.ExecutionHost() == "" {
				refs = append(refs, ref)
				continue
			}
			host = server.ExecutionHost()
			set, known := s.controller.GetPeerHub().Environment(host, ref.Name)
			remote[host+"/"+ref.Name] = secret.EnvVarStatus{Ref: ref, HostID: host, HostName: s.controller.GetPeerHub().Name(host), Available: known, IsSet: set}
		}
	}
	result, err := resolver.InspectReferences(ctx, refs)
	if err != nil {
		return nil, err
	}
	for i := range result.Secrets {
		result.Secrets[i].Servers = serversFor("secret", result.Secrets[i].Ref.Name, "")
	}
	for i := range result.EnvironmentVars {
		result.EnvironmentVars[i].Servers = serversFor("env", result.EnvironmentVars[i].Ref.Name, "")
	}
	for _, entry := range remote {
		entry.Servers = serversFor("env", entry.Ref.Name, entry.HostID)
		result.EnvironmentVars = append(result.EnvironmentVars, entry)
	}
	result.TotalEnvVars = len(result.EnvironmentVars)
	return result, nil
}
