package servericon

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/v2/mcp"
)

// Effective gives an uploaded override precedence over advertised artwork.
func Effective(uploaded *string, advertised string) string {
	if uploaded != nil && *uploaded != "" {
		return *uploaded
	}
	return advertised
}

// Resolve loads advertised server artwork once per connection. No credentials,
// cookies or third-party redirects are sent. Results use the same bounded,
// validated image representation as uploaded icons.
func Resolve(ctx context.Context, icons []mcp.Icon, serverURL string) string {
	if len(icons) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	candidates := slices.Clone(icons)
	rank := func(icon mcp.Icon) int {
		if icon.Theme == mcp.IconThemeDark {
			return 0
		}
		if icon.Theme == "" {
			return 1
		}
		return 2
	}
	slices.SortStableFunc(candidates, func(a, b mcp.Icon) int { return rank(a) - rank(b) })
	for i, icon := range candidates {
		if i == 4 || ctx.Err() != nil {
			break
		}
		data, mime, err := readAdvertised(ctx, icon, serverURL)
		if err != nil {
			continue
		}
		normalized := ""
		switch mime {
		case "image/svg+xml":
			normalized = string(data)
		case "image/png":
			normalized = pngPrefix + base64.StdEncoding.EncodeToString(data)
		case "image/jpeg", "image/jpg":
			dimensions, err := jpeg.DecodeConfig(bytes.NewReader(data))
			if err != nil || dimensions.Width > 1024 || dimensions.Height > 1024 {
				continue
			}
			image, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				continue
			}
			var encoded bytes.Buffer
			if png.Encode(&encoded, image) != nil {
				continue
			}
			normalized = pngPrefix + base64.StdEncoding.EncodeToString(encoded.Bytes())
		}
		if normalized != "" {
			if normalized, err := Normalize(normalized); err == nil {
				return normalized
			}
		}
	}
	return ""
}

func readAdvertised(ctx context.Context, icon mcp.Icon, serverURL string) ([]byte, string, error) {
	if len(icon.Src) > len("data:image/svg+xml;base64,")+base64.StdEncoding.EncodedLen(MaxSize) {
		return nil, "", fmt.Errorf("oversized icon URI")
	}
	if strings.HasPrefix(icon.Src, "data:") {
		header, encoded, found := strings.Cut(icon.Src, ",")
		if !found || !strings.HasSuffix(header, ";base64") {
			return nil, "", fmt.Errorf("invalid image data URI")
		}
		mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if icon.MIMEType != "" && icon.MIMEType != mime {
			return nil, "", fmt.Errorf("icon MIME type mismatch")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if len(data) > MaxSize {
			return nil, "", fmt.Errorf("oversized icon")
		}
		return data, mime, err
	}
	target, err := url.Parse(icon.Src)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil {
		return nil, "", fmt.Errorf("icon requires a credential-free HTTPS URL")
	}
	origin, _ := url.Parse(serverURL)
	sameOrigin := origin != nil && origin.Scheme == target.Scheme && strings.EqualFold(origin.Host, target.Host)
	if serverURL != "" && !sameOrigin {
		return nil, "", fmt.Errorf("icon must share the server origin")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// A stdio upstream has no HTTP origin to constrain its icon URL. Restrict
	// those requests to public addresses and dial the checked IP directly.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			ip := address.IP
			if !sameOrigin && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
				return nil, fmt.Errorf("icon address is not public")
			}
		}
		for _, address := range addresses {
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		return nil, fmt.Errorf("icon host is unreachable")
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/svg+xml, image/png, image/jpeg")
	response, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("icon HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxSize+1))
	if err != nil || len(data) > MaxSize {
		return nil, "", fmt.Errorf("cannot read bounded icon")
	}
	mime := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if icon.MIMEType != "" {
		mime = icon.MIMEType
	}
	if mime == "" || mime == "application/octet-stream" {
		mime = http.DetectContentType(data)
		if mime == "text/xml; charset=utf-8" || mime == "text/plain; charset=utf-8" {
			mime = "image/svg+xml"
		}
	}
	return data, mime, nil
}
