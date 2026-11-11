// Package mcpauth is the primary's OAuth 2.1 authorization server. MCP clients
// (harnesses) and peers sign in through the dashboard's OIDC session and pick
// the saved client or peer they act as. Issued tokens are stateless: they are
// HMAC-signed and bound to the subject's current static credential, so rotating
// or deleting that client or peer revokes every OAuth token issued for it.
package mcpauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

const (
	AccessPrefix  = "mcp_oat_"
	RefreshPrefix = "mcp_ort_"
	appPrefix     = "mcp_app_"
	// PeerAppID is the built-in public client a peer signs in with. It accepts
	// any loopback redirect ending in PeerCallbackPath.
	PeerAppID        = "mcpproxy-peer"
	PeerCallbackPath = "/oauth/callback"

	AccessLifetime  = time.Hour
	RefreshLifetime = 90 * 24 * time.Hour
	codeLifetime    = time.Minute
	requestLifetime = 10 * time.Minute
	maxTokenLength  = 4096
)

type Kind string

const (
	KindClient Kind = "client"
	KindPeer   Kind = "peer"
)

type claims struct {
	Type    string `json:"t"` // "a" access, "r" refresh
	Kind    Kind   `json:"k"`
	Subject string `json:"s"`
	Version string `json:"v"`
	App     string `json:"c"`
	Expires int64  `json:"e"`
}

// app is a registered OAuth client, carried inside its signed client_id.
type app struct {
	Name         string   `json:"n,omitempty"`
	RedirectURIs []string `json:"r"`
}

func (s *Server) mac(prefix, payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(prefix))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) sign(prefix string, value any) string {
	data, _ := json.Marshal(value)
	payload := base64.RawURLEncoding.EncodeToString(data)
	return prefix + payload + "." + s.mac(prefix, payload)
}

func (s *Server) open(prefix, raw string, value any) bool {
	if len(raw) > maxTokenLength || !strings.HasPrefix(raw, prefix) {
		return false
	}
	payload, signature, ok := strings.Cut(strings.TrimPrefix(raw, prefix), ".")
	if !ok || !hmac.Equal([]byte(signature), []byte(s.mac(prefix, payload))) {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	return err == nil && json.Unmarshal(data, value) == nil
}

// version fingerprints a subject's static credential hash. A rotation changes
// the hash, which invalidates every token carrying the old fingerprint.
func version(credentialHash string) string {
	if credentialHash == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("mcpproxy-oauth-subject:" + credentialHash))
	return hex.EncodeToString(sum[:12])
}
