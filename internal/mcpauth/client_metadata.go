package mcpauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/httpx"
)

func clientMetadata(ctx context.Context, id string) (app, error) {
	var document struct {
		ClientID     string   `json:"client_id"`
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	target, err := url.Parse(id)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" || target.Path == "" || target.Path == "/" {
		return app{}, fmt.Errorf("invalid client metadata URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, addr := range addresses {
			ip := addr.IP
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("client metadata address is not public")
			}
		}
		for _, addr := range addresses {
			conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("client metadata host is unreachable")
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return app{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return app{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return app{}, fmt.Errorf("client metadata HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return app{}, fmt.Errorf("invalid client metadata size")
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return app{}, err
	}
	if document.ClientID != id || len(document.RedirectURIs) == 0 || len(document.RedirectURIs) > 10 || (document.AuthMethod != "" && document.AuthMethod != "none") || document.ClientName == "" {
		return app{}, fmt.Errorf("invalid client metadata")
	}
	for _, redirect := range document.RedirectURIs {
		if !validRedirect(redirect) {
			return app{}, fmt.Errorf("invalid client redirect URI")
		}
	}
	return app{Name: document.ClientName, RedirectURIs: document.RedirectURIs}, nil
}

func (s *Server) ownClientMetadata(w http.ResponseWriter, r *http.Request) {
	redirect := r.URL.Query().Get("redirect_uri")
	target, err := url.Parse(redirect)
	origin := httpx.PublicOrigin(r)
	if err != nil || !validRedirect(redirect) || target.Scheme+"://"+target.Host != origin || !strings.HasPrefix(target.Path, config.OAuthRemoteCallbackPrefix) {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_id":                  origin + r.URL.RequestURI(),
		"client_name":                "MCPProxy",
		"redirect_uris":              []string{redirect},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"application_type":           "web",
	})
}
