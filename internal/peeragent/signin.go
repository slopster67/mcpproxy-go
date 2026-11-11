package peeragent

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/httpx"
	"github.com/slopster67/mcpproxy-go/internal/mcpauth"
	"github.com/slopster67/mcpproxy-go/internal/peering"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

const credentialFile = "peer-credential.json"

var errSignedOut = errors.New("peer is not signed in to the primary")

// savedCredential is the peer's OAuth sign-in, kept in its data directory.
type savedCredential struct {
	Primary      string `json:"primary"`
	RefreshToken string `json:"refresh_token"`
	PeerID       string `json:"peer_id"`
	PeerName     string `json:"peer_name,omitempty"`
}

type signin struct {
	Verifier, Redirect string
	Expires            time.Time
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	PeerID       string `json:"peer_id"`
	PeerName     string `json:"peer_name"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

// oauthMode reports whether the peer signs in through the browser instead of
// using a configured static token.
func (a *Agent) oauthMode() bool { return a.cfg.PeerToken == "" }

func (a *Agent) primary() string { return strings.TrimRight(a.cfg.PeerPrimaryURL, "/") }

func (a *Agent) credentialPath() string { return filepath.Join(a.cfg.DataDir, credentialFile) }

func (a *Agent) loadCredential() {
	if !a.oauthMode() {
		return
	}
	data, err := os.ReadFile(a.credentialPath())
	if err != nil {
		return
	}
	var saved savedCredential
	if json.Unmarshal(data, &saved) != nil || saved.RefreshToken == "" {
		a.logger.Warn("Ignoring unreadable peer credential file")
		return
	}
	if saved.Primary != a.primary() {
		a.logger.Info("Saved peer sign-in belongs to another primary; sign in again")
		return
	}
	a.credMu.Lock()
	a.refreshToken, a.peerID, a.peerName = saved.RefreshToken, saved.PeerID, saved.PeerName
	a.credMu.Unlock()
}

// saveCredentialLocked writes the refresh token atomically with owner-only permissions.
func (a *Agent) saveCredentialLocked() error {
	data, err := json.Marshal(savedCredential{Primary: a.primary(), RefreshToken: a.refreshToken, PeerID: a.peerID, PeerName: a.peerName})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(a.cfg.DataDir, ".peer-credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporary.Name(), a.credentialPath())
}

func (a *Agent) notifyCredential() {
	select {
	case a.credChanged <- struct{}{}:
	default:
	}
}

func (a *Agent) signOutLocked(reason string) {
	if a.refreshToken == "" {
		return
	}
	a.refreshToken, a.accessToken, a.accessExpires = "", "", time.Time{}
	_ = os.Remove(a.credentialPath())
	a.logger.Warn("Peer sign-in is no longer valid; sign in again", zap.String("reason", reason), zap.String("url", a.localURL()))
}

func (a *Agent) signedIn() bool {
	if !a.oauthMode() {
		return true
	}
	a.credMu.Lock()
	defer a.credMu.Unlock()
	return a.refreshToken != ""
}

func (a *Agent) identity() (string, string) {
	a.credMu.Lock()
	defer a.credMu.Unlock()
	return a.peerID, a.peerName
}

func (a *Agent) setIdentity(welcome peering.Welcome) {
	a.credMu.Lock()
	defer a.credMu.Unlock()
	if welcome.PeerID == "" || (a.peerID == welcome.PeerID && a.peerName == welcome.Name) {
		return
	}
	a.peerID, a.peerName = welcome.PeerID, welcome.Name
	if a.oauthMode() && a.refreshToken != "" {
		_ = a.saveCredentialLocked()
	}
}

// invalidateAccess forces the next bearer call to refresh, after a 401.
func (a *Agent) invalidateAccess() {
	a.credMu.Lock()
	a.accessToken = ""
	a.credMu.Unlock()
}

// bearer returns the credential for the primary's control channel.
func (a *Agent) bearer(ctx context.Context) (string, error) {
	if !a.oauthMode() {
		return a.cfg.PeerToken, nil
	}
	a.credMu.Lock()
	defer a.credMu.Unlock()
	if a.refreshToken == "" {
		return "", errSignedOut
	}
	if a.accessToken != "" && time.Until(a.accessExpires) > time.Minute {
		return a.accessToken, nil
	}
	response, err := a.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {a.refreshToken}, "client_id": {mcpauth.PeerAppID}})
	if err != nil {
		var rejected *grantError
		if errors.As(err, &rejected) {
			a.signOutLocked(rejected.Error())
			a.notifyCredential()
			return "", errSignedOut
		}
		return "", err
	}
	a.storeLocked(response)
	return a.accessToken, nil
}

func (a *Agent) storeLocked(response *tokenResponse) {
	a.accessToken = response.AccessToken
	a.accessExpires = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	if response.RefreshToken != "" {
		a.refreshToken = response.RefreshToken
	}
	if response.PeerID != "" {
		a.peerID, a.peerName = response.PeerID, response.PeerName
	}
	if err := a.saveCredentialLocked(); err != nil {
		a.logger.Warn("Could not save peer sign-in; it will be lost on restart", zap.Error(err))
	}
}

type grantError struct{ description string }

func (e *grantError) Error() string { return e.description }

func (a *Agent) tokenRequest(ctx context.Context, form url.Values) (*tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.primary()+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var body tokenResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body); err != nil {
		return nil, fmt.Errorf("primary token endpoint returned HTTP %d", response.StatusCode)
	}
	if response.StatusCode == http.StatusBadRequest && body.Error == "invalid_grant" {
		return nil, &grantError{description: body.Description}
	}
	if response.StatusCode != http.StatusOK || body.AccessToken == "" {
		return nil, fmt.Errorf("primary token endpoint returned HTTP %d %s", response.StatusCode, body.Error)
	}
	return &body, nil
}

// localURL is the address to open on this machine to sign the peer in.
func (a *Agent) localURL() string {
	host, port, err := net.SplitHostPort(a.cfg.Listen)
	if err != nil {
		return "http://" + a.cfg.Listen + "/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

//go:embed assets/geist.woff2
var geistFont []byte

// page mirrors the dashboard's consent screen: the same dark theme tokens,
// Geist type, logo header, card and primary button.
var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - MCPProxy</title>
<style>
@font-face{font-family:"Geist Variable";font-style:normal;font-display:swap;font-weight:100 900;src:url(/assets/geist.woff2) format("woff2-variations")}
:root{color-scheme:dark;--radius:.625rem;--background:oklch(.145 0 0);--foreground:oklch(.985 0 0);--card:oklch(.205 0 0);--primary:oklch(.922 0 0);--primary-foreground:oklch(.205 0 0);--muted-foreground:oklch(.708 0 0);--ring:oklch(.556 0 0)}
*{box-sizing:border-box}
body{margin:0;background:var(--background);color:var(--foreground);font-family:"Geist Variable",sans-serif;-webkit-font-smoothing:antialiased;-moz-osx-font-smoothing:grayscale}
.page{min-height:100svh;padding:1rem}
@media (min-width:40rem){.page{padding:2rem}}
.column{margin:0 auto;display:flex;min-height:calc(100svh - 4rem);width:100%;max-width:28rem;flex-direction:column;justify-content:center;gap:1.5rem}
.brand{display:flex;align-items:center;gap:.5rem;font-size:.875rem;line-height:1.25rem;font-weight:500}
.brand svg{width:1.25rem;height:1.25rem}
.card{display:flex;flex-direction:column;gap:1.5rem;overflow:hidden;border-radius:calc(var(--radius)*2.6);background:var(--card);padding:1.5rem 0;font-size:.875rem;line-height:1.25rem;box-shadow:0 0 0 1px oklch(.985 0 0/10%),0 4px 6px -1px rgb(0 0 0/.1),0 2px 4px -2px rgb(0 0 0/.1)}
.header{display:grid;gap:.375rem;padding:0 1.5rem}
h1{margin:0;font-size:1.125rem;line-height:1.75rem;font-weight:500}
.description{color:var(--muted-foreground)}
.description p{margin:0}
.description p+p{margin-top:.5rem}
.content{display:flex;justify-content:flex-end;gap:.5rem;padding:0 1.5rem}
.button{display:inline-flex;height:2.25rem;align-items:center;justify-content:center;border:1px solid transparent;border-radius:calc(var(--radius)*2.6);background:var(--primary);padding:0 .75rem;color:var(--primary-foreground);font-size:.875rem;font-weight:500;text-decoration:none;white-space:nowrap;transition:all .15s}
.button:hover{background:oklch(.922 0 0/80%)}
.button:active{transform:translateY(1px)}
.button:focus-visible{outline:none;border-color:var(--ring);box-shadow:0 0 0 3px oklch(.556 0 0/30%)}
</style></head><body><main class="page"><div class="column">
<div class="brand"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32" fill="none" stroke="white" stroke-width="3.3" stroke-linecap="round" aria-hidden="true"><path d="M4 5h3c7 0 6 11 14 11h2M4 16h19M4 27h3c7 0 6-11 14-11h2"/><circle cx="26" cy="16" r="3.5"/></svg><span>MCPProxy</span></div>
<div class="card"><div class="header"><h1>{{.Title}}</h1><div class="description">{{range .Message}}<p>{{.}}</p>{{end}}</div></div>
{{if .Action}}<div class="content"><a class="button" href="{{.Action}}">{{.ActionLabel}}</a></div>{{end}}</div>
</div></main></body></html>`))

func handleFont(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	_, _ = w.Write(geistFont)
}

type pageData struct {
	Title               string
	Message             []string // one paragraph each
	Action, ActionLabel string
}

func renderPage(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; font-src 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = page.Execute(w, data)
}

func (a *Agent) handleHome(w http.ResponseWriter, r *http.Request) {
	if a.signedIn() {
		http.Redirect(w, r, a.primary()+"/ui/", http.StatusFound)
		return
	}
	renderPage(w, http.StatusOK, pageData{Title: "Authenticate this peer", Message: []string{"This peer is not connected to " + a.primary() + " yet.", "Sign in to the primary and choose which peer this machine is."}, Action: "/oauth/start", ActionLabel: "Sign in"})
}

func (a *Agent) handleSigninStart(w http.ResponseWriter, r *http.Request) {
	if !a.oauthMode() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if !httpx.IsLoopbackHost(r.Host) {
		renderPage(w, http.StatusBadRequest, pageData{Title: "Open this page locally", Message: []string{"Sign in from the machine the peer runs on, at " + a.localURL()}})
		return
	}
	state, verifier := randomState(), oauth2.GenerateVerifier()
	redirect := "http://" + r.Host + mcpauth.PeerCallbackPath
	a.credMu.Lock()
	now := time.Now()
	for key, pending := range a.signins {
		if !now.Before(pending.Expires) {
			delete(a.signins, key)
		}
	}
	if len(a.signins) >= 20 {
		a.credMu.Unlock()
		renderPage(w, http.StatusTooManyRequests, pageData{Title: "Too many sign-ins", Message: []string{"Finish or wait for the open sign-ins to expire, then try again."}})
		return
	}
	a.signins[state] = signin{Verifier: verifier, Redirect: redirect, Expires: now.Add(10 * time.Minute)}
	a.credMu.Unlock()
	target := a.primary() + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {mcpauth.PeerAppID}, "redirect_uri": {redirect}, "state": {state},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"},
	}.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *Agent) handleSigninCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.credMu.Lock()
	pending, ok := a.signins[q.Get("state")]
	delete(a.signins, q.Get("state"))
	a.credMu.Unlock()
	if !ok || !time.Now().Before(pending.Expires) {
		renderPage(w, http.StatusBadRequest, pageData{Title: "Sign-in expired", Message: []string{"Start the sign-in again."}, Action: "/", ActionLabel: "Start again"})
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		renderPage(w, http.StatusForbidden, pageData{Title: "Sign-in was not completed", Message: []string{"The primary declined the sign-in."}, Action: "/", ActionLabel: "Start again"})
		return
	}
	response, err := a.tokenRequest(r.Context(), url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {pending.Redirect}, "client_id": {mcpauth.PeerAppID}, "code_verifier": {pending.Verifier}})
	if err != nil || response.RefreshToken == "" {
		a.logger.Warn("Peer sign-in failed", zap.Error(err))
		renderPage(w, http.StatusBadGateway, pageData{Title: "Sign-in failed", Message: []string{"The primary did not accept the sign-in."}, Action: "/", ActionLabel: "Start again"})
		return
	}
	a.credMu.Lock()
	a.storeLocked(response)
	a.credMu.Unlock()
	a.logger.Info("Peer signed in", zap.String("peer", response.PeerName))
	a.notifyCredential()
	http.Redirect(w, r, a.primary()+"/ui/", http.StatusFound)
}

// handleProtectedResource advertises the primary's per-peer issuer, so the
// consent page lists only this peer's clients.
func (a *Agent) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	peer, _ := a.identity()
	if peer == "" {
		http.Error(w, "Peer has not connected to the primary yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 httpx.PublicOrigin(r) + "/mcp",
		"authorization_servers":    []string{a.primary() + "/oauth/peers/" + peer},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "MCPProxy",
	})
}

// handleAuthorizationServer serves the primary's per-peer metadata for MCP
// clients that look for it on the MCP server's own address.
func (a *Agent) handleAuthorizationServer(w http.ResponseWriter, r *http.Request) {
	peer, _ := a.identity()
	if peer == "" {
		http.Error(w, "Peer has not connected to the primary yet", http.StatusServiceUnavailable)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.primary()+"/.well-known/oauth-authorization-server/oauth/peers/"+url.PathEscape(peer), nil)
	if err != nil {
		http.Error(w, "Primary unavailable", http.StatusBadGateway)
		return
	}
	response, err := a.client.Do(request)
	if err != nil {
		http.Error(w, "Primary unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 64<<10))
}

var resourceMetadata = regexp.MustCompile(`resource_metadata="[^"]*"`)

type originKey struct{}

// rewriteChallenge points a primary 401 at this peer's own resource metadata.
func rewriteChallenge(response *http.Response) error {
	if response.StatusCode != http.StatusUnauthorized {
		return nil
	}
	origin, _ := response.Request.Context().Value(originKey{}).(string)
	if challenge := response.Header.Get("WWW-Authenticate"); challenge != "" && origin != "" {
		response.Header.Set("WWW-Authenticate", resourceMetadata.ReplaceAllLiteralString(challenge, `resource_metadata="`+origin+`/.well-known/oauth-protected-resource/mcp"`))
	}
	return nil
}

func randomState() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
