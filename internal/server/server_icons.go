package server

import (
	"github.com/mark3labs/mcp-go/v2/mcp"
	"github.com/slopster67/mcpproxy-go/internal/servericon"
)

func (p *MCPProxyServer) serverIcon(name string) string {
	var uploaded *string
	if cfg := p.currentConfig(); cfg != nil {
		for _, sc := range cfg.Servers {
			if sc != nil && sc.Name == name {
				uploaded = sc.Icon
				break
			}
		}
	}
	var advertised string
	if p.upstreamManager != nil {
		advertised = p.upstreamManager.GetServerIcon(name)
	}
	return servericon.Effective(uploaded, advertised)
}

func (s *Server) GetServerIcon(name string) string {
	var uploaded *string
	if cfg := s.GetCurrentConfig(); cfg != nil {
		for _, sc := range cfg.Servers {
			if sc != nil && sc.Name == name {
				uploaded = sc.Icon
				break
			}
		}
	}
	return servericon.Effective(uploaded, s.runtime.UpstreamManager().GetServerIcon(name))
}

func serverIcons(icon string) []mcp.Icon {
	if icon == "" {
		return nil
	}
	image := mcp.Icon{Src: servericon.DataURI(icon), MIMEType: servericon.MIMEType(icon)}
	if image.MIMEType == "image/svg+xml" {
		image.Sizes = []string{"any"}
	}
	return []mcp.Icon{image}
}
