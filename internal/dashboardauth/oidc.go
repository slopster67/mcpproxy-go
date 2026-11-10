// Package dashboardauth implements browser-only OIDC administration sessions.
// MCP client and peer credentials are authenticated by their own transports.
package dashboardauth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/slopster67/mcpproxy-go/internal/auth"
	"golang.org/x/oauth2"
)

const sessionCookie = "mcpproxy_session"
const flowCookie = "mcpproxy_oidc_flow"
const sessionLifetime = 8 * time.Hour
const flowLifetime = 10 * time.Minute

type User struct {
	Subject string `json:"subject"`
	Name    string `json:"name"`
	Email   string `json:"email,omitempty"`
}
type session struct {
	User    User
	CSRF    string
	Expires time.Time
	ctx     context.Context
	cancel  context.CancelFunc
}
type loginFlow struct {
	State                     string
	Nonce, Verifier, ReturnTo string
	Expires                   time.Time
}
type Manager struct {
	metadata   providerMetadata
	cfg        *Config
	client     *http.Client
	providerMu sync.Mutex
	oauth      *oauth2.Config
	verifier   *oidc.IDTokenVerifier
	mu         sync.Mutex
	sessions   map[[32]byte]session
	flowCipher cipher.AEAD
	usedFlows  map[[32]byte]time.Time
}

func New(cfg *Config) *Manager {
	if cfg != nil {
		copy := *cfg
		cfg = &copy
	}
	manager := &Manager{cfg: cfg, sessions: map[[32]byte]session{}, usedFlows: map[[32]byte]time.Time{}}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	manager.flowCipher, err = cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		panic(err)
	}
	manager.client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many identity-provider redirects")
		}
		if len(via) > 0 && via[0].Method == http.MethodPost &&
			(!strings.EqualFold(via[0].URL.Scheme, r.URL.Scheme) || !strings.EqualFold(via[0].URL.Host, r.URL.Host)) {
			return fmt.Errorf("identity provider redirected a credential request to another origin")
		}
		if !manager.providerURLAllowed(r.URL.String()) {
			return fmt.Errorf("identity provider redirected to an insecure URL")
		}
		return nil
	}}
	return manager
}
func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func digest(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func (m *Manager) secure() bool {
	return m.cfg != nil && strings.HasPrefix(m.cfg.RedirectURL, "https://")
}
func (m *Manager) cookieName(name string) string {
	if m.secure() {
		return "__Host-" + name
	}
	return name
}
func (m *Manager) cookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	if m.secure() {
		path = "/"
	}
	http.SetCookie(w, &http.Cookie{Name: m.cookieName(name), Value: value, Path: path, HttpOnly: true, Secure: m.secure(), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (m *Manager) provider(ctx context.Context) (*oauth2.Config, *oidc.IDTokenVerifier, error) {
	m.providerMu.Lock()
	defer m.providerMu.Unlock()
	if m.oauth != nil {
		return m.oauth, m.verifier, nil
	}
	if m.cfg == nil {
		return nil, nil, fmt.Errorf("dashboard sign-in is not configured")
	}
	ctx = oidc.ClientContext(ctx, m.client)
	provider, err := oidc.NewProvider(ctx, m.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("could not discover the identity provider")
	}
	var metadata providerMetadata
	if err = provider.Claims(&metadata); err != nil {
		return nil, nil, fmt.Errorf("invalid identity-provider discovery document")
	}
	endpoint := provider.Endpoint()
	for _, raw := range []string{endpoint.AuthURL, endpoint.TokenURL, metadata.JWKSURL} {
		if !m.providerURLAllowed(raw) {
			return nil, nil, fmt.Errorf("identity provider must use HTTPS endpoints")
		}
	}
	if metadata.IntrospectionURL != "" && !m.providerURLAllowed(metadata.IntrospectionURL) {
		return nil, nil, fmt.Errorf("identity provider must use an HTTPS introspection endpoint")
	}
	m.metadata = metadata
	m.oauth = &oauth2.Config{ClientID: m.cfg.ClientID, ClientSecret: m.cfg.ClientSecret, RedirectURL: m.cfg.RedirectURL, Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	m.verifier = provider.VerifierContext(oidc.ClientContext(context.Background(), m.client), &oidc.Config{ClientID: m.cfg.ClientID})
	return m.oauth, m.verifier, nil
}
func (m *Manager) pruneLocked(now time.Time) {
	for key, s := range m.sessions {
		if !now.Before(s.Expires) {
			m.deleteSessionLocked(key)
		}
	}
	for key, expires := range m.usedFlows {
		if !now.Before(expires) {
			delete(m.usedFlows, key)
		}
	}
}
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"success": false, "error": message})
}
func returnPath(raw string) string {
	if len(raw) > 1024 {
		return "/ui/"
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/ui/") || strings.Contains(raw, "\\") {
		return "/ui/"
	}
	normalized := u.String()
	encoded, _ := json.Marshal(normalized)
	if len(encoded) > 1024 {
		return "/ui/"
	}
	return normalized
}
func (m *Manager) Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	conf, _, err := m.provider(r.Context())
	if err != nil {
		failure(w, 503, err.Error())
		return
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	flow := loginFlow{State: state, Nonce: nonce, Verifier: verifier, ReturnTo: returnPath(r.URL.Query().Get("return_to")), Expires: time.Now().Add(flowLifetime)}
	payload, err := json.Marshal(flow)
	if err != nil {
		failure(w, 500, "Could not start sign-in")
		return
	}
	sealed := m.flowCipher.Seal(nil, nil, payload, []byte(m.cookieName(flowCookie)))
	m.cookie(w, flowCookie, base64.RawURLEncoding.EncodeToString(sealed), "/auth/", int(flowLifetime.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, conf.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (m *Manager) Callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(m.cookieName(flowCookie))
	if err != nil || state == "" || len(state) > 256 || len(cookie.Value) > 4096 {
		failure(w, 400, "Invalid or expired sign-in")
		return
	}
	var flow loginFlow
	sealed, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		failure(w, 400, "Invalid or expired sign-in")
		return
	}
	payload, err := m.flowCipher.Open(nil, nil, sealed, []byte(m.cookieName(flowCookie)))
	if err != nil || json.Unmarshal(payload, &flow) != nil || !time.Now().Before(flow.Expires) || !auth.ConstantTimeEqual(state, flow.State) {
		failure(w, 400, "Invalid or expired sign-in")
		return
	}
	m.mu.Lock()
	_, used := m.usedFlows[digest(state)]
	m.mu.Unlock()
	if used {
		failure(w, 400, "Invalid or expired sign-in")
		return
	}
	m.cookie(w, flowCookie, "", "/auth/", -1)
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		failure(w, 403, "Sign-in was cancelled or denied")
		return
	}
	conf, verifier, err := m.provider(r.Context())
	if err != nil {
		failure(w, 503, err.Error())
		return
	}
	token, err := conf.Exchange(oidc.ClientContext(r.Context(), m.client), r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		failure(w, 401, "Sign-in failed")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		failure(w, 401, "Identity provider did not return an ID token")
		return
	}
	id, err := verifier.Verify(oidc.ClientContext(r.Context(), m.client), raw)
	if err != nil || id.Subject == "" || !auth.ConstantTimeEqual(id.Nonce, flow.Nonce) {
		failure(w, 401, "Invalid identity token")
		return
	}
	var claims struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	if err = id.Claims(&claims); err != nil {
		failure(w, 401, "Invalid identity claims")
		return
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	if name == "" {
		name = id.Subject
	}
	// Only verified identity callbacks enter the replay set. Anonymous login
	// starts and failed exchanges allocate no server-side flow records.
	m.mu.Lock()
	if _, used := m.usedFlows[digest(state)]; used {
		m.mu.Unlock()
		failure(w, 400, "Invalid or expired sign-in")
		return
	}
	m.usedFlows[digest(state)] = flow.Expires
	m.mu.Unlock()
	value := randomToken()
	saved := session{User: User{Subject: id.Subject, Name: name, Email: claims.Email}, CSRF: randomToken(), Expires: time.Now().Add(sessionLifetime)}
	m.mu.Lock()
	m.pruneLocked(time.Now())
	if len(m.sessions) >= 10000 {
		m.mu.Unlock()
		failure(w, 503, "Too many active sessions")
		return
	}
	// A new sign-in replaces the existing browser session rather than leaving it usable.
	if previous, e := r.Cookie(m.cookieName(sessionCookie)); e == nil {
		m.deleteSessionLocked(digest(previous.Value))
	}
	saved.ctx, saved.cancel = context.WithDeadline(context.Background(), saved.Expires)
	m.sessions[digest(value)] = saved
	m.mu.Unlock()
	m.cookie(w, sessionCookie, value, "/", int(sessionLifetime.Seconds()))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, flow.ReturnTo, http.StatusSeeOther)
}
func (m *Manager) lookup(r *http.Request) (session, bool) {
	c, err := r.Cookie(m.cookieName(sessionCookie))
	if err != nil || c.Value == "" {
		return session{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[digest(c.Value)]
	if !ok || !time.Now().Before(s.Expires) {
		m.deleteSessionLocked(digest(c.Value))
		return session{}, false
	}
	return s, true
}
func (m *Manager) Session(w http.ResponseWriter, r *http.Request) {
	s, ok := m.lookup(r)
	if !ok {
		reply(w, 200, map[string]any{"configured": m.cfg != nil, "authenticated": false})
		return
	}
	reply(w, 200, map[string]any{"configured": true, "authenticated": true, "user": s.User, "csrf_token": s.CSRF, "expires_at": s.Expires})
}
func (m *Manager) allowedWrite(r *http.Request, s session) bool {
	if !auth.ConstantTimeEqual(r.Header.Get("X-CSRF-Token"), s.CSRF) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, _ := url.Parse(m.cfg.RedirectURL)
		if origin != u.Scheme+"://"+u.Host {
			return false
		}
	}
	return true
}
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.lookup(r)
		if !ok {
			failure(w, 401, "Sign in to continue")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !m.allowedWrite(r, s) {
			failure(w, 403, "Invalid CSRF token or request origin")
			return
		}
		ac := &auth.AuthContext{Type: auth.AuthTypeAdminUser, CredentialKind: auth.CredentialKindSession, UserID: s.User.Subject, DisplayName: s.User.Name, Email: s.User.Email}
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		detach := context.AfterFunc(s.ctx, cancel)
		defer detach()
		next.ServeHTTP(w, r.WithContext(auth.WithAuthContext(ctx, ac)))
	})
}
func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	s, ok := m.lookup(r)
	if !ok {
		failure(w, 401, "Sign in to continue")
		return
	}
	if !m.allowedWrite(r, s) {
		failure(w, 403, "Invalid CSRF token or request origin")
		return
	}
	if c, err := r.Cookie(m.cookieName(sessionCookie)); err == nil {
		m.mu.Lock()
		m.deleteSessionLocked(digest(c.Value))
		m.mu.Unlock()
	}
	m.cookie(w, sessionCookie, "", "/", -1)
	reply(w, 200, map[string]bool{"success": true})
}

func (m *Manager) deleteSessionLocked(key [32]byte) {
	if saved, ok := m.sessions[key]; ok {
		if saved.cancel != nil {
			saved.cancel()
		}
		delete(m.sessions, key)
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.sessions {
		m.deleteSessionLocked(key)
	}
	clear(m.usedFlows)
}

func (m *Manager) providerURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if m.cfg == nil || !strings.HasPrefix(m.cfg.Issuer, "http://") {
		return false
	}
	return u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}

// ProtectUI redirects document navigations directly to the provider, before the
// SPA is served. UI assets require the same session and return 401 instead
// of redirecting an asset fetch to an HTML sign-in page.
func (m *Manager) ProtectUI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		if r.URL.Path == "/ui/favicon.svg" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			failure(w, 405, "Method not allowed")
			return
		}
		if _, ok := m.lookup(r); !ok {
			if strings.HasPrefix(r.URL.Path, "/ui/assets/") {
				failure(w, http.StatusUnauthorized, "Sign in to continue")
				return
			}
			login := r.Clone(r.Context())
			target := *r.URL
			target.RawQuery = url.Values{"return_to": {r.URL.RequestURI()}}.Encode()
			login.URL = &target
			m.Login(w, login)
			return
		}
		w.Header().Set("Cache-Control", "private, no-cache")
		next.ServeHTTP(w, r)
	})
}
