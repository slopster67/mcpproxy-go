package mcpauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/httpx"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

var (
	ErrRequestNotFound = errors.New("sign-in request not found or expired")
	ErrInvalidSubject  = errors.New("choose one of the listed options")
)

const maxPending = 1000

// pending is an authorization request waiting for the dashboard consent page.
type pending struct {
	ID          string
	Issuer      string
	Kind        Kind
	Host        string // client sign-in: the execution host whose clients are listed ("" = primary)
	AppID       string
	App         app
	RedirectURI string
	State       string
	Challenge   string
	Expires     time.Time
}

// grant is an issued, not yet exchanged authorization code.
type grant struct {
	Kind        Kind
	Subject     string
	Version     string
	AppID       string
	RedirectURI string
	Challenge   string
	Expires     time.Time
}

type Server struct {
	key      []byte
	store    *storage.Manager
	peers    *peering.Hub
	mu       sync.Mutex
	requests map[string]*pending
	codes    map[[32]byte]*grant
}

// New derives the token key from the database authentication key, so tokens
// survive restarts and are useless with another database.
func New(authenticationKey []byte, store *storage.Manager, peers *peering.Hub) *Server {
	m := hmac.New(sha256.New, authenticationKey)
	m.Write([]byte("mcpproxy oauth tokens v1"))
	return &Server{key: m.Sum(nil), store: store, peers: peers, requests: map[string]*pending{}, codes: map[[32]byte]*grant{}}
}

// Register mounts the public OAuth endpoints on the primary's root mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/oauth/peers/{peer}", s.metadata)
	mux.HandleFunc("GET /oauth/peers/{peer}/.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("/oauth/register", s.register)
	mux.HandleFunc("/oauth/token", s.token)
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("GET /oauth/peers/{peer}/authorize", s.authorize)
}

// Challenge writes the 401 that starts an MCP client's OAuth discovery.
func Challenge(w http.ResponseWriter, r *http.Request, invalid bool, message string) {
	value := `Bearer resource_metadata="` + httpx.PublicOrigin(r) + `/.well-known/oauth-protected-resource/mcp"`
	if invalid {
		value += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", value)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func randomID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func digest(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// cors admits browser-based MCP clients. These endpoints use no cookies.
func cors(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (s *Server) protectedResource(w http.ResponseWriter, r *http.Request) {
	cors(w, r)
	origin := httpx.PublicOrigin(r)
	writeJSON(w, 200, map[string]any{
		"resource":                 origin + "/mcp",
		"authorization_servers":    []string{origin},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "MCPProxy",
	})
}

// AuthorizationMetadata is the RFC 8414 document for issuer. A peer serves the
// same document for its own address.
func AuthorizationMetadata(origin, issuer string) map[string]any {
	return map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/authorize",
		"token_endpoint":                                 origin + "/oauth/token",
		"registration_endpoint":                          origin + "/oauth/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": true,
		"client_id_metadata_document_supported":          false,
	}
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	cors(w, r)
	origin := httpx.PublicOrigin(r)
	issuer := origin + "/oauth"
	if peer := r.PathValue("peer"); peer != "" {
		if !s.peers.Exists(peer) {
			http.NotFound(w, r)
			return
		}
		issuer = origin + "/oauth/peers/" + peer
	}
	document := AuthorizationMetadata(origin, issuer)
	if r.PathValue("peer") == "" {
		// The primary's own issuer is its origin; endpoints live under /oauth.
		document["issuer"] = origin
	}
	writeJSON(w, 200, document)
}

func validRedirect(raw string) bool {
	if raw == "" || len(raw) > 1024 || strings.ContainsAny(raw, " \t\r\n\\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return u.Host != ""
	case "http":
		return u.Host != "" && httpx.IsLoopbackHost(u.Host)
	case "", "javascript", "data", "file", "vbscript", "blob", "about":
		return false
	default:
		return true // native application scheme
	}
}

// redirectMatches applies exact matching, except that a loopback redirect may
// use any port (RFC 8252 §7.3).
func redirectMatches(registered, given string) bool {
	if registered == given {
		return true
	}
	a, errA := url.Parse(registered)
	b, errB := url.Parse(given)
	if errA != nil || errB != nil || a.Scheme != "http" || b.Scheme != "http" || !httpx.IsLoopbackHost(a.Host) {
		return false
	}
	return a.Hostname() == b.Hostname() && a.Path == b.Path && a.RawQuery == b.RawQuery
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request) != nil {
		oauthError(w, 400, "invalid_client_metadata", "Invalid registration document")
		return
	}
	if len(request.RedirectURIs) == 0 || len(request.RedirectURIs) > 10 {
		oauthError(w, 400, "invalid_redirect_uri", "Register 1–10 redirect URIs")
		return
	}
	for _, uri := range request.RedirectURIs {
		if !validRedirect(uri) {
			oauthError(w, 400, "invalid_redirect_uri", "Redirect URIs must be HTTPS, loopback HTTP or an application scheme")
			return
		}
	}
	name := strings.TrimSpace(request.ClientName)
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	a := app{Name: name, RedirectURIs: request.RedirectURIs}
	response := map[string]any{
		"client_id":                  s.sign(appPrefix, a),
		"client_id_issued_at":        time.Now().Unix(),
		"redirect_uris":              a.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if name != "" {
		response["client_name"] = name
	}
	writeJSON(w, http.StatusCreated, response)
}

func failPage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n"))
}

func withParams(target string, values map[string]string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	q := u.Query()
	for k, v := range values {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := r.URL.Query()
	origin := httpx.PublicOrigin(r)
	peer := r.PathValue("peer")
	request := &pending{Kind: KindClient, Host: peer, AppID: q.Get("client_id"), Issuer: origin}
	if peer != "" {
		if !s.peers.Exists(peer) {
			failPage(w, 404, "This peer no longer exists.")
			return
		}
		request.Issuer = origin + "/oauth/peers/" + peer
	}
	redirect := q.Get("redirect_uri")
	if request.AppID == PeerAppID {
		u, err := url.Parse(redirect)
		if peer != "" || err != nil || u.Scheme != "http" || !httpx.IsLoopbackHost(u.Host) || u.Path != PeerCallbackPath || u.RawQuery != "" || u.Fragment != "" {
			failPage(w, 400, "Invalid peer sign-in request. Open the peer's own address on the machine it runs on.")
			return
		}
		request.Kind, request.App = KindPeer, app{Name: "MCPProxy peer"}
	} else {
		if !s.open(appPrefix, request.AppID, &request.App) {
			failPage(w, 400, "Unknown application. Remove the server from your MCP client and add it again so it can register.")
			return
		}
		if redirect == "" && len(request.App.RedirectURIs) == 1 {
			redirect = request.App.RedirectURIs[0]
		}
		matched := false
		for _, registered := range request.App.RedirectURIs {
			if redirectMatches(registered, redirect) {
				matched = true
				break
			}
		}
		if !matched {
			failPage(w, 400, "The redirect URI was not registered by this application.")
			return
		}
	}
	request.RedirectURI, request.State = redirect, q.Get("state")
	reject := func(code, description string) {
		http.Redirect(w, r, withParams(redirect, map[string]string{"error": code, "error_description": description, "state": request.State, "iss": request.Issuer}), http.StatusFound)
	}
	if q.Get("response_type") != "code" {
		reject("unsupported_response_type", "Only the authorization code flow is supported")
		return
	}
	request.Challenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || len(request.Challenge) < 43 || len(request.Challenge) > 128 {
		reject("invalid_request", "PKCE with S256 is required")
		return
	}
	request.ID, request.Expires = randomID(), time.Now().Add(requestLifetime)
	s.mu.Lock()
	s.pruneLocked(time.Now())
	if len(s.requests) >= maxPending {
		s.mu.Unlock()
		reject("temporarily_unavailable", "Too many pending sign-ins")
		return
	}
	s.requests[request.ID] = request
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/ui/oauth/authorize?request="+url.QueryEscape(request.ID), http.StatusFound)
}

func (s *Server) pruneLocked(now time.Time) {
	for id, request := range s.requests {
		if !now.Before(request.Expires) {
			delete(s.requests, id)
		}
	}
	for key, code := range s.codes {
		if !now.Before(code.Expires) {
			delete(s.codes, key)
		}
	}
}

// ConsentOption is one client or peer the signed-in administrator can choose.
type ConsentOption struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// Consent describes a pending sign-in to the dashboard.
type Consent struct {
	Kind         Kind            `json:"kind"`
	AppName      string          `json:"app_name,omitempty"`
	RedirectURI  string          `json:"redirect_uri"`
	RedirectHost string          `json:"redirect_host"`
	Loopback     bool            `json:"loopback"`
	HostID       string          `json:"host_id,omitempty"`
	HostName     string          `json:"host_name"`
	Options      []ConsentOption `json:"options"`
}

func (s *Server) lookup(id string) (*pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request := s.requests[id]
	if request == nil || !time.Now().Before(request.Expires) {
		delete(s.requests, id)
		return nil, false
	}
	return request, true
}

func (s *Server) options(request *pending) ([]ConsentOption, error) {
	options := []ConsentOption{}
	if request.Kind == KindPeer {
		peers, err := s.peers.List()
		if err != nil {
			return nil, err
		}
		for _, peer := range peers {
			detail := "Offline"
			if peer.Online {
				detail = "Online — signing in here signs out the machine using it now"
			}
			options = append(options, ConsentOption{ID: peer.ID, Name: peer.Name, Detail: detail})
		}
		return options, nil
	}
	clients, err := s.store.ListClientCredentials()
	if err != nil {
		return nil, err
	}
	for _, client := range clients {
		if client.HostID != request.Host || auth.ValidateTokenInvariants(&client, "") != nil {
			continue
		}
		name := client.DisplayName
		if name == "" {
			name = client.ClientID
		}
		options = append(options, ConsentOption{ID: client.ClientID, Name: name})
	}
	return options, nil
}

// Describe returns the consent page's view of a pending sign-in.
func (s *Server) Describe(id string) (*Consent, error) {
	request, ok := s.lookup(id)
	if !ok {
		return nil, ErrRequestNotFound
	}
	options, err := s.options(request)
	if err != nil {
		return nil, err
	}
	consent := &Consent{Kind: request.Kind, AppName: request.App.Name, RedirectURI: request.RedirectURI, HostID: request.Host, HostName: "Primary", Options: options}
	if u, err := url.Parse(request.RedirectURI); err == nil {
		consent.RedirectHost = u.Host
		if u.Host == "" {
			consent.RedirectHost = u.Scheme + ":"
		}
		consent.Loopback = u.Scheme == "http" && httpx.IsLoopbackHost(u.Host)
	}
	if request.Host != "" {
		consent.HostName = s.peers.Name(request.Host)
	}
	return consent, nil
}

// Decide consumes a pending sign-in and returns where to send the browser.
func (s *Server) Decide(id string, approve bool, subject string) (string, error) {
	request, ok := s.lookup(id)
	if !ok {
		return "", ErrRequestNotFound
	}
	if !approve {
		s.mu.Lock()
		delete(s.requests, id)
		s.mu.Unlock()
		return withParams(request.RedirectURI, map[string]string{"error": "access_denied", "error_description": "Sign-in was declined", "state": request.State, "iss": request.Issuer}), nil
	}
	options, err := s.options(request)
	if err != nil {
		return "", err
	}
	allowed := false
	for _, option := range options {
		allowed = allowed || option.ID == subject
	}
	current, exists := s.subjectVersion(request.Kind, subject)
	if !allowed || !exists {
		return "", ErrInvalidSubject
	}
	code := randomID()
	s.mu.Lock()
	if s.requests[id] != request {
		s.mu.Unlock()
		return "", ErrRequestNotFound
	}
	delete(s.requests, id)
	s.codes[digest(code)] = &grant{Kind: request.Kind, Subject: subject, Version: current, AppID: request.AppID, RedirectURI: request.RedirectURI, Challenge: request.Challenge, Expires: time.Now().Add(codeLifetime)}
	s.mu.Unlock()
	return withParams(request.RedirectURI, map[string]string{"code": code, "state": request.State, "iss": request.Issuer}), nil
}

func (s *Server) client(id string) *auth.ClientCredential {
	clients, err := s.store.ListClientCredentials()
	if err != nil {
		return nil
	}
	for i := range clients {
		if clients[i].ClientID == id && auth.ValidateTokenInvariants(&clients[i], "") == nil {
			return &clients[i]
		}
	}
	return nil
}

func (s *Server) subjectVersion(kind Kind, id string) (string, bool) {
	switch kind {
	case KindClient:
		if client := s.client(id); client != nil {
			return version(client.TokenHash), true
		}
	case KindPeer:
		if hash, ok := s.peers.CredentialHash(id); ok {
			return version(hash), true
		}
	}
	return "", false
}

func (s *Server) current(c claims) bool {
	value, ok := s.subjectVersion(c.Kind, c.Subject)
	return ok && value != "" && hmac.Equal([]byte(value), []byte(c.Version))
}

// AuthenticateClient resolves an OAuth access token to its saved client.
func (s *Server) AuthenticateClient(raw string) (*auth.ClientCredential, bool) {
	var c claims
	if !s.open(AccessPrefix, raw, &c) || c.Type != "a" || c.Kind != KindClient || time.Now().Unix() >= c.Expires {
		return nil, false
	}
	client := s.client(c.Subject)
	if client == nil || !hmac.Equal([]byte(version(client.TokenHash)), []byte(c.Version)) {
		return nil, false
	}
	return client, true
}

// AuthenticatePeer resolves an OAuth access token to its peer.
func (s *Server) AuthenticatePeer(raw string) (string, time.Time, bool) {
	var c claims
	if !s.open(AccessPrefix, raw, &c) || c.Type != "a" || c.Kind != KindPeer || time.Now().Unix() >= c.Expires || !s.current(c) {
		return "", time.Time{}, false
	}
	return c.Subject, time.Unix(c.Expires, 0), true
}

func (s *Server) issue(w http.ResponseWriter, kind Kind, subject, subjectVersion, appID string) {
	now := time.Now()
	access := s.sign(AccessPrefix, claims{Type: "a", Kind: kind, Subject: subject, Version: subjectVersion, App: appID, Expires: now.Add(AccessLifetime).Unix()})
	refresh := s.sign(RefreshPrefix, claims{Type: "r", Kind: kind, Subject: subject, Version: subjectVersion, App: appID, Expires: now.Add(RefreshLifetime).Unix()})
	response := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(AccessLifetime.Seconds()), "refresh_token": refresh}
	if kind == KindPeer {
		response["peer_id"], response["peer_name"] = subject, s.peers.Name(subject)
	}
	writeJSON(w, 200, response)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "Invalid form body")
		return
	}
	appID := r.PostForm.Get("client_id")
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		key := digest(r.PostForm.Get("code"))
		s.mu.Lock()
		g := s.codes[key]
		delete(s.codes, key)
		s.mu.Unlock()
		if g == nil || !time.Now().Before(g.Expires) {
			oauthError(w, 400, "invalid_grant", "The authorization code is invalid or expired")
			return
		}
		if appID != g.AppID || (r.PostForm.Get("redirect_uri") != "" && r.PostForm.Get("redirect_uri") != g.RedirectURI) {
			oauthError(w, 400, "invalid_grant", "The authorization code was issued to another client")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !hmac.Equal([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(g.Challenge)) {
			oauthError(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		c := claims{Kind: g.Kind, Subject: g.Subject, Version: g.Version}
		if !s.current(c) {
			oauthError(w, 400, "invalid_grant", "The chosen client or peer changed; sign in again")
			return
		}
		if g.Kind == KindPeer {
			// One machine per peer: this sign-in revokes the previous one's tokens.
			current, ok := "", false
			if err := s.peers.StartOAuthSession(g.Subject); err == nil {
				current, ok = s.subjectVersion(KindPeer, g.Subject)
			}
			if !ok {
				oauthError(w, 500, "server_error", "Could not record the peer sign-in")
				return
			}
			g.Version = current
		}
		s.issue(w, g.Kind, g.Subject, g.Version, g.AppID)
	case "refresh_token":
		var c claims
		if !s.open(RefreshPrefix, r.PostForm.Get("refresh_token"), &c) || c.Type != "r" || time.Now().Unix() >= c.Expires || (appID != "" && appID != c.App) || !s.current(c) {
			oauthError(w, 400, "invalid_grant", "The refresh token is invalid, expired or revoked")
			return
		}
		s.issue(w, c.Kind, c.Subject, c.Version, c.App)
	default:
		oauthError(w, 400, "unsupported_grant_type", "Use authorization_code or refresh_token")
	}
}
