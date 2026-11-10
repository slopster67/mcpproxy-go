package runtime

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

type pendingPeerOAuthCallback struct {
	server, host string
	expires      time.Time
}

// Only authenticated management requests can start a peer sign-in. Keep its
// state on the primary so anonymous traffic cannot enter the peer control queue.
func (r *Runtime) registerPeerOAuthCallback(server, host, authURL string) error {
	u, err := url.Parse(authURL)
	if err != nil {
		return fmt.Errorf("peer returned an invalid authorization URL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	state := query.Get("state")
	callback, callbackErr := url.Parse(query.Get("redirect_uri"))
	if err != nil || len(query["state"]) != 1 || state == "" || len(state) > 2048 || callbackErr != nil {
		return fmt.Errorf("peer returned an invalid OAuth flow")
	}
	// Loopback callbacks do not pass through the primary.
	if !strings.HasPrefix(callback.Path, config.OAuthRemoteCallbackPrefix) {
		return nil
	}
	expected := config.OAuthRemoteCallbackPrefix + url.PathEscape(server)
	if callback.EscapedPath() != expected {
		return fmt.Errorf("peer returned a callback for another server")
	}
	r.peerOAuthMu.Lock()
	defer r.peerOAuthMu.Unlock()
	if r.peerOAuthCallbacks == nil {
		r.peerOAuthCallbacks = make(map[[32]byte]pendingPeerOAuthCallback)
	}
	now := time.Now()
	for state, pending := range r.peerOAuthCallbacks {
		if !now.Before(pending.expires) {
			delete(r.peerOAuthCallbacks, state)
		}
	}
	r.peerOAuthCallbacks[sha256.Sum256([]byte(state))] = pendingPeerOAuthCallback{server: server, host: host, expires: now.Add(30 * time.Minute)}
	return nil
}

// ConsumePeerOAuthCallback authorizes one callback, bound to its server and
// current execution host. Invalid requests never remove a legitimate waiter.
func (r *Runtime) ConsumePeerOAuthCallback(server, state string) (string, bool) {
	if state == "" || len(state) > 2048 {
		return "", false
	}
	key := sha256.Sum256([]byte(state))
	r.peerOAuthMu.Lock()
	defer r.peerOAuthMu.Unlock()
	pending, ok := r.peerOAuthCallbacks[key]
	if !ok || pending.server != server {
		return "", false
	}
	if !time.Now().Before(pending.expires) || r.ServerHost(server) != pending.host {
		delete(r.peerOAuthCallbacks, key)
		return "", false
	}
	delete(r.peerOAuthCallbacks, key)
	return pending.host, true
}
