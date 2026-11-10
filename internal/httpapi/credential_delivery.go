package httpapi

import (
	"encoding/json"
	"net"
	"strings"
)

// The delivery helpers of an issued credential (POST /clients and the MCP
// `credentials` tool): one snippet format, one endpoint, one
// set of UI deep links, in both editions.

// ClientSnippet is the paste-ready config of a custom client. The credential is
// always a header, never a query parameter.
type ClientSnippet struct {
	GenericHTTP string `json:"generic_http"`
}

// mcpEndpointURL is this instance's MCP endpoint for a snippet. The scheme
// follows the listener: with TLS enabled the same listener serves only HTTPS,
// so a delivered snippet must never point a worker (and its credential
// header) at plain HTTP.
func (s *Server) mcpEndpointURL() string {
	addr := "127.0.0.1:8080"
	scheme := "http"
	if cfg, err := s.controller.GetConfig(); err == nil && cfg != nil {
		if cfg.Listen != "" {
			addr = cfg.Listen
		}

	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return scheme + "://" + addr + "/mcp"
}

func (s *Server) clientSnippet(credential, host string) ClientSnippet {
	type entry struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	endpoint := s.mcpEndpointURL()
	if host != "" {
		endpoint = "http://127.0.0.1:8080/mcp"
		if peers, err := s.controller.GetPeerHub().List(); err == nil {
			for _, p := range peers {
				if p.ID == host && p.Address != "" {
					if _, port, err := net.SplitHostPort(p.Address); err == nil {
						endpoint = "http://127.0.0.1:" + port + "/mcp"
					}
				}
			}
		}
	}
	doc := struct {
		MCPServers map[string]entry `json:"mcpServers"`
	}{MCPServers: map[string]entry{"mcpproxy": {URL: endpoint, Headers: map[string]string{"Authorization": "Bearer " + credential}}}}
	b, _ := json.Marshal(doc)
	return ClientSnippet{GenericHTTP: string(b)}
}
