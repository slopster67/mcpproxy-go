package server

import (
	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
)

func (p *MCPProxyServer) NotifyBindingChanged(_ string) {
	for _, srv := range []*mcpserver.MCPServer{p.server, p.directServer, p.codeExecServer, p.callToolServer} {
		if srv != nil {
			srv.SendNotificationToAllClients(mcp.MethodNotificationToolsListChanged, nil)
		}
	}
}
