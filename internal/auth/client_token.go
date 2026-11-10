package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const ClientTokenPrefixStr = "mcp_cli_"
const KindClient = "client"
const (
	ProfileModeLocked     = "locked"
	ProfileModeSwitchable = "switchable"
)
const (
	PermRead        = "read"
	PermWrite       = "write"
	PermDestructive = "destructive"
)
const MaxTokens = 100
const MaxClientDisplayName = 64

var clientIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,55}$`)
var errMalformedCredential = errors.New("malformed client credential")

func ValidClientID(id string) bool     { return clientIDPattern.MatchString(id) }
func ClientTokenName(id string) string { return "client-" + id }

// ClientCredential is the persisted token and profile binding of a saved client.
type ClientCredential struct {
	AllowCodeExecution bool   `json:"allow_code_execution"`
	AllowManaging      bool   `json:"allow_managing"`
	HostID             string `json:"host_id,omitempty"`
	Name               string `json:"name"`
	TokenHash          string `json:"token_hash"`
	TokenPrefix        string `json:"token_prefix"`
	Kind               string `json:"kind"`
	ClientID           string `json:"client_id"`
	DisplayName        string `json:"display_name,omitempty"`

	ProfilePin  string     `json:"profile_pin,omitempty"`
	ProfileMode string     `json:"profile_mode"`
	CreatedAt   time.Time  `json:"created_at"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`

	Issuer *CredentialIssuer `json:"issuer,omitempty"`
}
type CredentialIssuer struct {
	ActorKind string `json:"actor_kind"`
	ActorName string `json:"actor_name,omitempty"`
	Surface   string `json:"surface"`
}

func (t *ClientCredential) AuthContext() *AuthContext {
	if t == nil {
		return nil
	}
	return &AuthContext{AllowCodeExecution: t.AllowCodeExecution, AllowManaging: t.AllowManaging, HostID: t.HostID, Type: AuthTypeClient, AgentName: t.Name, TokenPrefix: t.TokenPrefix, AllowedServers: []string{"*"}, Permissions: []string{PermRead, PermWrite, PermDestructive}, ProfilePin: t.ProfilePin, TokenKind: KindClient, ClientID: t.ClientID, ProfileMode: t.ProfileMode, CredentialKind: CredentialKindClientToken}
}
func ValidateAnyTokenFormat(raw string) (string, bool) {
	if !strings.HasPrefix(raw, ClientTokenPrefixStr) || len(raw) != len(ClientTokenPrefixStr)+64 {
		return "", false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(raw, ClientTokenPrefixStr))
	return KindClient, err == nil
}
func ValidateTokenInvariants(t *ClientCredential, claimedKind string) error {
	if t == nil || t.Kind != KindClient || (claimedKind != "" && claimedKind != KindClient) || !ValidClientID(t.ClientID) || t.Name != ClientTokenName(t.ClientID) || utf8.RuneCountInString(t.DisplayName) > MaxClientDisplayName {
		return errMalformedCredential
	}
	if t.ProfileMode != ProfileModeLocked && t.ProfileMode != ProfileModeSwitchable {
		return errMalformedCredential
	}
	return nil
}
func GenerateClientToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return ClientTokenPrefixStr + hex.EncodeToString(b), nil
}

// HashToken computes HMAC-SHA256 of the token using the given key
// and returns the hex-encoded digest.
func HashToken(token string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func TokenPrefix(token string) string {
	if len(token) < 12 {
		return token
	}
	return token[:12]
}
