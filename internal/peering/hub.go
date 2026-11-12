package peering

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

// Peers already post hello reports every 10 seconds. Missing three reports
// expires presence even when TCP/SSE writes still succeed into network buffers.
const peerPresenceTimeout = 30 * time.Second

var bucket = []byte("peers")
var ErrNotFound = errors.New("peer not found")

type connection struct {
	lastHello   time.Time
	done        chan struct{}
	send        chan Frame
	pending     map[string]chan Frame
	address     string
	states      map[string]ServerState
	processID   string
	environment map[string]bool
}

type Hub struct {
	db                *database.DB
	credentials       sync.RWMutex
	closed            bool
	mu                sync.Mutex
	connections       map[string]*connection
	listeners         map[string]map[string]func(Frame)
	Changed           func(string, bool)
	ServerChanged     func(string, string)
	PrepareConnection func(context.Context, string, *Connection) error
	SaveCredentials   func(context.Context, string, string, Frame) (any, error)
	// AuthenticateBearer resolves an OAuth access token to a peer ID and the
	// token's expiry. Static mcp_peer_ tokens are checked by the hub itself.
	AuthenticateBearer func(string) (string, time.Time, bool)
}

func NewHub(db *database.DB) *Hub {
	return &Hub{db: db, connections: map[string]*connection{}, listeners: map[string]map[string]func(Frame){}}
}
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func newToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "mcp_peer_" + hex.EncodeToString(data), nil
}
func (h *Hub) read(id string) (Peer, error) {
	var peer Peer
	err := h.db.View(func(tx *database.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return ErrNotFound
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &peer)
	})
	return peer, err
}
func (h *Hub) save(peer Peer) error {
	return h.db.Update(func(tx *database.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucket)
		if err != nil {
			return err
		}
		data, err := json.Marshal(peer)
		if err != nil {
			return err
		}
		return b.Put([]byte(peer.ID), data)
	})
}
func (h *Hub) List() ([]Peer, error) {
	peers := []Peer{}
	err := h.db.View(func(tx *database.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, raw []byte) error {
			var p Peer
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			p.TokenHash, p.OAuthSession = "", ""
			peers = append(peers, p)
			return nil
		})
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range peers {
		peers[i].Online = h.connections[peers[i].ID] != nil
		if c := h.connections[peers[i].ID]; c != nil {
			peers[i].Address = c.address
		}
	}
	return peers, err
}

// Get returns one peer as List shows it (no token hash, live presence).
func (h *Hub) Get(id string) (Peer, error) {
	p, err := h.read(id)
	if err != nil {
		return Peer{}, err
	}
	p.TokenHash, p.OAuthSession = "", ""
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.connections[id]; c != nil {
		p.Online, p.Address = true, c.address
	}
	return p, nil
}

func (h *Hub) Create(name string) (Peer, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Peer{}, "", fmt.Errorf("peer name must contain 1–100 characters")
	}
	token, err := newToken()
	if err != nil {
		return Peer{}, "", err
	}
	p := Peer{ID: uuid.NewString(), Name: name, TokenHash: tokenHash(token), CreatedAt: time.Now().UTC()}
	err = h.save(p)
	p.TokenHash = ""
	if err == nil && h.Changed != nil {
		h.Changed(p.ID, false)
	}
	return p, token, err
}
func (h *Hub) Rotate(id string) (string, error) {
	h.credentials.Lock()
	defer h.credentials.Unlock()
	p, err := h.read(id)
	if err != nil {
		return "", err
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	p.TokenHash = tokenHash(token)
	if err = h.save(p); err != nil {
		return "", err
	}
	online := h.Online(id)
	h.disconnect(id)
	if !online && h.Changed != nil {
		h.Changed(id, false)
	}
	return token, nil
}
func (h *Hub) Delete(id string) error {
	h.credentials.Lock()
	defer h.credentials.Unlock()
	err := h.db.Update(func(tx *database.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil || b.Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return b.Delete([]byte(id))
	})
	if err == nil {
		online := h.Online(id)
		h.disconnect(id)
		if !online && h.Changed != nil {
			h.Changed(id, false)
		}
	}
	return err
}

// CredentialHash fingerprints the peer's static token and current OAuth
// sign-in. OAuth grants are bound to it, so rotating the token or signing the
// peer in on another machine revokes them.
func (h *Hub) CredentialHash(id string) (string, bool) {
	p, err := h.read(id)
	return p.TokenHash + "|" + p.OAuthSession, err == nil && p.TokenHash != ""
}

// StartOAuthSession records a new browser sign-in for the peer, replacing the
// previous one.
func (h *Hub) StartOAuthSession(id string) error {
	h.credentials.Lock()
	defer h.credentials.Unlock()
	p, err := h.read(id)
	if err != nil {
		return err
	}
	session, err := newToken()
	if err != nil {
		return err
	}
	p.OAuthSession = tokenHash(session)
	return h.save(p)
}

// authenticate accepts a static peer token or an OAuth access token. The
// returned expiry is zero for static tokens.
func (h *Hub) authenticate(token string) (Peer, time.Time, bool) {
	if strings.HasPrefix(token, "mcp_peer_") {
		p, ok := h.Authenticate(token)
		return p, time.Time{}, ok
	}
	if h.AuthenticateBearer == nil {
		return Peer{}, time.Time{}, false
	}
	id, expires, ok := h.AuthenticateBearer(token)
	if !ok {
		return Peer{}, time.Time{}, false
	}
	p, err := h.read(id)
	return p, expires, err == nil
}

func (h *Hub) Authenticate(token string) (Peer, bool) {
	if !strings.HasPrefix(token, "mcp_peer_") || len(token) != 73 {
		return Peer{}, false
	}
	hash := tokenHash(token)
	var found Peer
	_ = h.db.View(func(tx *database.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, raw []byte) error {
			var p Peer
			if json.Unmarshal(raw, &p) == nil && p.TokenHash == hash {
				found = p
			}
			return nil
		})
	})
	return found, found.ID != ""
}
func (h *Hub) Online(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connections[id] != nil
}
func (h *Hub) disconnect(id string) { h.disconnectIf(id, nil) }
func (h *Hub) disconnectIf(id string, expected *connection) {
	h.mu.Lock()
	c := h.connections[id]
	if expected != nil && c != expected {
		h.mu.Unlock()
		return
	}
	if c != nil {
		delete(h.connections, id)
		close(c.done)
	}
	h.mu.Unlock()
	if c != nil && h.Changed != nil {
		h.Changed(id, false)
	}
}
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	ids := make([]string, 0, len(h.connections))
	for id := range h.connections {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.disconnect(id)
	}
}

func (h *Hub) Call(ctx context.Context, peer, op, server string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	f := Frame{ID: uuid.NewString(), Op: op, Server: server, Data: data}
	response := make(chan Frame, 1)
	h.mu.Lock()
	c := h.connections[peer]
	if c != nil {
		c.pending[f.ID] = response
	}
	h.mu.Unlock()
	if c == nil {
		return ErrOffline
	}
	defer func() { h.mu.Lock(); delete(c.pending, f.ID); h.mu.Unlock() }()
	select {
	case c.send <- f:
	case <-c.done:
		return ErrOffline
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case reply := <-response:
		if reply.Error != "" {
			return errors.New(reply.Error)
		}
		if output != nil && len(reply.Data) > 0 {
			return json.Unmarshal(reply.Data, output)
		}
		return nil
	case <-c.done:
		return ErrOffline
	case <-ctx.Done():
		select {
		case c.send <- Frame{ID: f.ID, Op: "cancel"}:
		default:
		}
		return ctx.Err()
	}
}
func (h *Hub) Listen(server string, callback func(Frame)) func() {
	id := uuid.NewString()
	h.mu.Lock()
	if h.listeners[server] == nil {
		h.listeners[server] = map[string]func(Frame){}
	}
	h.listeners[server][id] = callback
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.listeners[server], id)
		if len(h.listeners[server]) == 0 {
			delete(h.listeners, server)
		}
		h.mu.Unlock()
	}
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.credentials.RLock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	peer, expires, ok := h.authenticate(token)
	if !ok {
		h.credentials.RUnlock()
		http.Error(w, "Invalid peer token", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.credentials.RUnlock()
		h.stream(w, r, peer, expires)
	case http.MethodPost:
		defer h.credentials.RUnlock()
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(20 * time.Second))
		var f Frame
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxMessage)).Decode(&f) != nil {
			http.Error(w, "Invalid peer message", 400)
			return
		}
		h.mu.Lock()
		c := h.connections[peer.ID]
		if f.Op == "hello" && c != nil {
			var hello Hello
			if json.Unmarshal(f.Data, &hello) == nil {
				c.address = hello.Address
				c.processID = hello.ProcessID
				c.environment = hello.Environment
				c.states = hello.Servers
				c.lastHello = time.Now()
			}
		}

		var pending chan Frame
		if c != nil {
			pending = c.pending[f.ID]
		}
		callbacks := []func(Frame){}
		if f.Op == "notification" {
			for _, callback := range h.listeners[peer.ID+"/"+f.Server] {
				callbacks = append(callbacks, callback)
			}
		}
		h.mu.Unlock()
		if c == nil {
			http.Error(w, "Peer disconnected", 409)
			return
		}
		if f.Op == "hello" {
			h.touch(peer.ID)
		}
		if f.Op == "credentials" {
			h.mu.Lock()
			writer := c.processID
			h.mu.Unlock()
			if h.SaveCredentials == nil {
				http.Error(w, "Credential storage unavailable", 503)
				return
			}
			result, err := h.SaveCredentials(r.Context(), peer.ID, writer, f)
			if err != nil {
				code := http.StatusBadRequest
				if errors.Is(err, commitview.ErrStaleGeneration) {
					code = http.StatusConflict
				}
				http.Error(w, "Credential update rejected", code)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
			return
		}

		if pending != nil {
			select {
			case pending <- f:
			default:
			}
		}
		if f.Op == "server_changed" && h.ServerChanged != nil {
			h.ServerChanged(peer.ID, f.Server)
		}
		for _, callback := range callbacks {
			callback(f)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		h.credentials.RUnlock()
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Welcome is the first frame of a control stream: it tells the peer who it is.
type Welcome struct {
	PeerID string `json:"peer_id"`
	Name   string `json:"name"`
}

func (h *Hub) stream(w http.ResponseWriter, r *http.Request, p Peer, expires time.Time) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	c := &connection{lastHello: time.Now(), processID: r.Header.Get("X-MCPProxy-Peer-Process"), done: make(chan struct{}), send: make(chan Frame, 128), pending: map[string]chan Frame{}}
	h.credentials.RLock()
	currentPeer, err := h.read(p.ID)
	if err != nil || currentPeer.TokenHash != p.TokenHash {
		h.credentials.RUnlock()
		http.Error(w, "Invalid peer token", 401)
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		h.credentials.RUnlock()
		http.Error(w, "Primary is shutting down", 503)
		return
	}
	if previous := h.connections[p.ID]; previous != nil {
		close(previous.done)
	}
	h.connections[p.ID] = c
	h.mu.Unlock()
	h.credentials.RUnlock()
	defer func() {
		h.disconnectIf(p.ID, c)
		h.touch(p.ID)
	}()
	welcome, _ := json.Marshal(Welcome{PeerID: p.ID, Name: p.Name})
	hello, _ := json.Marshal(Frame{Op: "welcome", Data: welcome})
	fmt.Fprintf(w, ": connected\n\ndata: %s\n\n", hello)
	if rc.Flush() != nil {
		return
	}
	// A stream opened with an OAuth access token ends when the token expires;
	// the peer reconnects with a refreshed one.
	var expired <-chan time.Time
	if !expires.IsZero() {
		timer := time.NewTimer(time.Until(expires))
		defer timer.Stop()
		expired = timer.C
	}
	if h.Changed != nil {
		h.Changed(p.ID, true)
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case f := <-c.send:
			_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
			data, err := json.Marshal(f)
			if err != nil {
				return
			}
			if _, err = fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		case <-heartbeat.C:
			h.mu.Lock()
			stale := time.Since(c.lastHello) >= peerPresenceTimeout
			h.mu.Unlock()
			if stale {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case <-expired:
			return
		}
	}
}

func (h *Hub) Exists(id string) bool { _, err := h.read(id); return err == nil }

func (h *Hub) Name(id string) string {
	p, err := h.read(id)
	if err != nil {
		return "Unknown peer"
	}
	return p.Name
}

func (h *Hub) ServerState(peer, server string) (ServerState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.connections[peer]
	if c == nil {
		return ServerState{}, false
	}
	state, ok := c.states[server]
	return state, ok
}

func (h *Hub) touch(id string) {
	h.mu.Lock()
	address := ""
	if connection := h.connections[id]; connection != nil {
		address = connection.address
	}
	h.mu.Unlock()
	_ = h.db.Update(func(tx *database.Tx) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			return nil
		}
		var current Peer
		if err := json.Unmarshal(raw, &current); err != nil {
			return err
		}
		now := time.Now().UTC()
		current.LastSeen = &now
		if address != "" {
			current.Address = address
		}
		data, err := json.Marshal(current)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), data)
	})
}

func (h *Hub) ProcessID(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.connections[id]; c != nil {
		return c.processID
	}
	return ""
}
func (h *Hub) Environment(id, name string) (bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.connections[id]; c != nil {
		value, ok := c.environment[name]
		return value, ok
	}
	return false, false
}
