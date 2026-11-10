package oauth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// configureRemoteCallback retains the loopback listener for local callers, but
// makes the same single-use state dispatcher reachable from the main HTTP mux.
// Each waiting sign-in keeps its own redirect, so a later login can use a
// different host without cancelling the one already in progress. An empty URL
// leaves an in-flight public callback alone.
func (c *CallbackServer) configureRemoteCallback(callbackURL string) {
	c.waitersMu.Lock()
	defer c.waitersMu.Unlock()
	if callbackURL == "" && len(c.waiters) > 0 {
		return
	}
	c.remoteCallbackURL = callbackURL
}

// NoteRedirect records the redirect URI this state was registered with.
// Token exchange uses that exact string.
func (c *CallbackServer) NoteRedirect(state, redirectURI string) {
	if state == "" || redirectURI == "" {
		return
	}
	c.waitersMu.Lock()
	defer c.waitersMu.Unlock()
	if waiter, ok := c.waiters[state]; ok {
		waiter.redirectURI = redirectURI
	}
}

// RedirectFor is the redirect URI registered for state. A sign-in that did
// not record one falls back to the server's current public callback, then to
// the loopback listener.
func (c *CallbackServer) RedirectFor(state string) string {
	c.waitersMu.Lock()
	defer c.waitersMu.Unlock()
	if waiter, ok := c.waiters[state]; ok && waiter.redirectURI != "" {
		return waiter.redirectURI
	}
	if c.remoteCallbackURL != "" {
		return c.remoteCallbackURL
	}
	return c.RedirectURI
}

// EffectiveRedirectURI is the exact URI registered with the provider, which
// can differ from the private loopback listener behind a reverse proxy.
func (c *CallbackServer) EffectiveRedirectURI() string {
	c.waitersMu.Lock()
	defer c.waitersMu.Unlock()
	if c.remoteCallbackURL != "" {
		return c.remoteCallbackURL
	}
	return c.RedirectURI
}

// RemoteCallbackHandler accepts provider callbacks without dashboard sessions.
// The unpredictable, single-use state authenticates the callback.
func (m *CallbackServerManager) RemoteCallbackHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	encodedName := strings.TrimPrefix(r.URL.EscapedPath(), config.OAuthRemoteCallbackPrefix)
	serverName, err := url.PathUnescape(encodedName)
	if err != nil || encodedName == "" || strings.Contains(encodedName, "/") {
		http.NotFound(w, r)
		return
	}
	c, exists := m.GetCallbackServer(serverName)
	if !exists {
		http.NotFound(w, r)
		return
	}
	// Reject missing/duplicate parameters before they can consume a waiter.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || query.Get("state") == "" || len(query["code"]) > 1 || len(query["error"]) > 1 || (query.Get("code") == "" && query.Get("error") == "") || (query.Get("code") != "" && query.Get("error") != "") {
		http.Error(w, "invalid OAuth callback", http.StatusBadRequest)
		return
	}
	c.waitersMu.Lock()
	waiter, pending := c.waiters[query.Get("state")]
	pending = pending && waiter != nil && !waiter.delivered
	c.waitersMu.Unlock()
	if !pending {
		http.NotFound(w, r)
		return
	}
	// Invalid public requests must not mark an otherwise healthy upstream as
	// failed. Delivery still atomically consumes the matching waiter once.
	c.handleCallbackWithFailureNotification(w, r, false)
}
