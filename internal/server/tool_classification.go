package server

import (
	"github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
)

func (s *Server) GetToolClassification(server, tool string) (string, error) {
	return s.runtime.GetToolClassification(server, tool)
}
func (s *Server) SetToolClassificationAt(server, tool, tier, hash string, incarnation int64) error {
	if err := s.runtime.SetToolClassificationAt(server, tool, tier, hash, incarnation); err != nil {
		return err
	}
	if proxy := s.mcpProxy; proxy != nil {
		for _, instance := range []*mcpserver.MCPServer{proxy.server, proxy.directServer, proxy.callToolServer, proxy.codeExecServer} {
			if instance != nil {
				instance.SendNotificationToAllClients(mcp.MethodNotificationToolsListChanged, nil)
			}
		}
	}
	return nil
}
