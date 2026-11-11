package server

import (
	"encoding/base64"
	"fmt"

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

// mcpproxyMark is the MCPProxy logo (the same artwork as the dashboard's
// frontend/src/assets/logo.svg); the stroke colour is filled in per theme.
const mcpproxyMark = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none" stroke="%s" stroke-width="3.3" stroke-linecap="round"><path d="M4 5h3c7 0 6 11 14 11h2M4 16h19M4 27h3c7 0 6-11 14-11h2"/><circle cx="26" cy="16" r="3.5"/></svg>`

// mcpproxyIcons is the icon MCPProxy advertises for itself in its initialize
// response and on its own tools: the logo with a black stroke for light
// backgrounds and a white one for dark backgrounds.
func mcpproxyIcons() []mcp.Icon {
	icon := func(stroke string, theme mcp.IconTheme) mcp.Icon {
		svg := fmt.Sprintf(mcpproxyMark, stroke)
		return mcp.Icon{
			Src:      "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)),
			MIMEType: "image/svg+xml",
			Sizes:    []string{"any"},
			Theme:    theme,
		}
	}
	return []mcp.Icon{icon("black", mcp.IconThemeLight), icon("white", mcp.IconThemeDark)}
}

// newProxyTool builds one of MCPProxy's own tools, carrying the MCPProxy icon.
// Upstream tools get their own server's icon instead.
func newProxyTool(name string, opts ...mcp.ToolOption) mcp.Tool {
	return mcp.NewTool(name, append(opts, mcp.WithToolIcons(mcpproxyIcons()...))...)
}
