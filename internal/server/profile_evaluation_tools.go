package server

import "github.com/slopster67/mcpproxy-go/internal/profile"

type profileEvaluationTool struct {
	server string
	tool   string
	tier   profile.Tier
}

func (p *MCPProxyServer) profileEvaluationTools() []profileEvaluationTool {
	if p.mainServer == nil || p.mainServer.runtime == nil {
		return nil
	}
	supervisor := p.mainServer.runtime.Supervisor()
	if supervisor == nil || supervisor.StateView() == nil {
		return nil
	}
	snapshot := supervisor.StateView().Snapshot()
	if snapshot == nil {
		return nil
	}
	var tools []profileEvaluationTool
	for serverName, status := range snapshot.Servers {
		if status == nil || !status.ToolsDiscovered {
			continue
		}
		for _, info := range status.Tools {
			// StateView stores the upstream's raw tool name. A raw name may
			// itself start with "<server>:"; keep it intact so the guard
			// compares the same exact registration identity as dispatch.
			annotations, found := p.EffectiveAnnotations(serverName, info.Name)
			tools = append(tools, profileEvaluationTool{
				server: serverName,
				tool:   info.Name,
				tier:   profile.IntrinsicTier(annotations, found),
			})
		}
	}
	return tools
}
