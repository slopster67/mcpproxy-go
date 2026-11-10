package runtime

import (
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
)

// Entities on the dashboard change stream (GET /api/v1/events). Each name is
// the REST collection the change belongs to.
const (
	EntityServers  = "servers"
	EntityTools    = "tools"
	EntityClients  = "clients"
	EntityPeers    = "peers"
	EntityProfiles = "profiles"
	EntitySecrets  = "secrets"
	EntitySessions = "sessions"
	EntityActivity = "activity"
	EntityUsage    = "usage"
)

// Change operations.
const (
	OpUpsert     = "upsert"
	OpDelete     = "delete"
	OpAppend     = "append"
	OpInvalidate = "invalidate"
)

// Change is one frame of the dashboard change stream.
type Change struct {
	Seq    uint64 `json:"seq"`
	Entity string `json:"entity"`
	Op     string `json:"op"`
	ID     string `json:"id,omitempty"`
	Data   any    `json:"data,omitempty"`
}

// Hydrator returns the object GET /api/v1/{entity}/{id} serves for id, or
// found=false when it no longer exists (the change becomes a delete).
type Hydrator func(id string) (data any, found bool, err error)

const (
	defaultChangeWindow   = 150 * time.Millisecond
	changeSubscriberQueue = 256
)

// invalidateIntervals throttles invalidations per (entity, id). Entities not
// listed use defaultInvalidateInterval.
var invalidateIntervals = map[string]time.Duration{EntityUsage: 2 * time.Second}

const defaultInvalidateInterval = 250 * time.Millisecond

// ChangeHub is the one publisher of the dashboard change stream. Producers say
// WHAT changed; the hub decides when and with which payload to publish:
//
//   - Dirty marks objects changed. Dirty ids are collected in a per-entity set
//     and flushed once per window, so a burst on one id is one frame while two
//     ids are two frames. At flush each id is rebuilt by the entity's builder
//     (servers: the runtime server builder; others: a Hydrator registered by
//     the REST layer) and published as upsert, or as delete when it is gone.
//   - Append publishes an append-only record immediately (activity).
//   - Invalidate publishes a throttled invalidation (leading and trailing edge).
//
// Seq is assigned under the hub lock at publish time, so it is monotonic and
// every subscriber sees the same order. A subscriber that cannot keep up is
// dropped (its channel closed): the client reconnects, gets a new hello and
// refetches, which is cheaper and more correct than silently missing frames.
type ChangeHub struct {
	window  time.Duration
	servers func() ([]contracts.Server, error)
	logger  *zap.Logger

	flushMu sync.Mutex // serialises flushes

	mu         sync.Mutex
	seq        uint64
	subs       map[chan Change]struct{}
	hydrators  map[string]Hydrator
	dirty      map[string]map[string]struct{}
	allServers bool
	armed      bool
	throttles  map[string]*invalidateThrottle
	observers  []func()
	closed     bool
}

type invalidateThrottle struct {
	last    time.Time
	pending bool
}

// NewChangeHub builds a hub. servers is the server builder (Runtime.Servers);
// window is the coalescing window for dirty ids (0 = default).
func NewChangeHub(servers func() ([]contracts.Server, error), window time.Duration, logger *zap.Logger) *ChangeHub {
	if window <= 0 {
		window = defaultChangeWindow
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ChangeHub{
		window:    window,
		servers:   servers,
		logger:    logger,
		subs:      map[chan Change]struct{}{},
		hydrators: map[string]Hydrator{},
		dirty:     map[string]map[string]struct{}{},
		throttles: map[string]*invalidateThrottle{},
	}
}

// Close stops publishing and ends every subscription.
func (h *ChangeHub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// SetHydrator installs the builder for entity's upserts.
func (h *ChangeHub) SetHydrator(entity string, fn Hydrator) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.hydrators[entity] = fn
	h.mu.Unlock()
}

// OnServersChanged registers an in-process observer called after every flush
// that published server changes. It runs on the flush goroutine and must not
// block.
func (h *ChangeHub) OnServersChanged(fn func()) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.observers = append(h.observers, fn)
	h.mu.Unlock()
}

// Subscribe returns a new subscriber channel and the seq of the last frame
// published before it, which the stream sends as its hello.
func (h *ChangeHub) Subscribe() (chan Change, uint64) {
	ch := make(chan Change, changeSubscriberQueue)
	h.mu.Lock()
	if h.closed {
		close(ch)
	} else {
		h.subs[ch] = struct{}{}
	}
	seq := h.seq
	h.mu.Unlock()
	return ch, seq
}

// Unsubscribe removes and closes ch. Safe after the hub already dropped it.
func (h *ChangeHub) Unsubscribe(ch chan Change) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Dirty marks ids of entity changed; each is published once per window.
func (h *ChangeHub) Dirty(entity string, ids ...string) {
	if h == nil || len(ids) == 0 {
		return
	}
	h.mu.Lock()
	set := h.dirty[entity]
	if set == nil {
		set = map[string]struct{}{}
		h.dirty[entity] = set
	}
	for _, id := range ids {
		if id != "" {
			set[id] = struct{}{}
		}
	}
	h.armLocked()
	h.mu.Unlock()
}

// AllServersDirty marks every configured server changed (a change without a
// server name: phase transitions, bulk tool discovery).
func (h *ChangeHub) AllServersDirty() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.allServers = true
	h.armLocked()
	h.mu.Unlock()
}

func (h *ChangeHub) armLocked() {
	if h.armed {
		return
	}
	h.armed = true
	time.AfterFunc(h.window, h.Flush)
}

// Append publishes an append-only record immediately.
func (h *ChangeHub) Append(entity, id string, data any) {
	if h == nil {
		return
	}
	h.publish(Change{Entity: entity, Op: OpAppend, ID: id, Data: data})
}

// Invalidate publishes a throttled invalidation of entity (id optional, e.g.
// the server whose tools changed).
func (h *ChangeHub) Invalidate(entity, id string) {
	if h == nil {
		return
	}
	interval := defaultInvalidateInterval
	if d, ok := invalidateIntervals[entity]; ok {
		interval = d
	}
	key := entity + "\x00" + id
	h.mu.Lock()
	t := h.throttles[key]
	if t == nil {
		t = &invalidateThrottle{}
		h.throttles[key] = t
	}
	if t.pending {
		h.mu.Unlock()
		return
	}
	wait := interval - time.Since(t.last)
	if wait <= 0 {
		t.last = time.Now()
		h.publishLocked(Change{Entity: entity, Op: OpInvalidate, ID: id})
		h.mu.Unlock()
		return
	}
	t.pending = true
	h.mu.Unlock()
	time.AfterFunc(wait, func() {
		h.mu.Lock()
		t.pending = false
		t.last = time.Now()
		h.publishLocked(Change{Entity: entity, Op: OpInvalidate, ID: id})
		h.mu.Unlock()
	})
}

// Flush publishes every pending dirty id now. The timer calls it; tests call
// it directly instead of sleeping.
func (h *ChangeHub) Flush() {
	if h == nil {
		return
	}
	h.flushMu.Lock()
	defer h.flushMu.Unlock()

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	dirty, allServers := h.dirty, h.allServers
	h.dirty, h.allServers, h.armed = map[string]map[string]struct{}{}, false, false
	hydrators := make(map[string]Hydrator, len(h.hydrators))
	for k, v := range h.hydrators {
		hydrators[k] = v
	}
	observers := append([]func(){}, h.observers...)
	h.mu.Unlock()

	var out []Change
	serverIDs := sortedIDs(dirty[EntityServers])
	delete(dirty, EntityServers)
	if allServers || len(serverIDs) > 0 {
		out = append(out, h.serverChanges(serverIDs, allServers)...)
	}
	entities := make([]string, 0, len(dirty))
	for entity := range dirty {
		entities = append(entities, entity)
	}
	sort.Strings(entities)
	for _, entity := range entities {
		fn := hydrators[entity]
		if fn == nil {
			continue
		}
		for _, id := range sortedIDs(dirty[entity]) {
			data, found, err := fn(id)
			if err != nil {
				h.logger.Debug("change hydration failed", zap.String("entity", entity), zap.String("id", id), zap.Error(err))
				continue
			}
			if found {
				out = append(out, Change{Entity: entity, Op: OpUpsert, ID: id, Data: data})
			} else {
				out = append(out, Change{Entity: entity, Op: OpDelete, ID: id})
			}
		}
	}

	h.mu.Lock()
	for _, c := range out {
		h.publishLocked(c)
	}
	h.mu.Unlock()

	if allServers || len(serverIDs) > 0 {
		for _, fn := range observers {
			fn()
		}
	}
}

func (h *ChangeHub) serverChanges(ids []string, all bool) []Change {
	if h.servers == nil {
		return nil
	}
	list, err := h.servers()
	if err != nil {
		h.logger.Debug("change hub: building servers failed", zap.Error(err))
		return nil
	}
	byName := make(map[string]*contracts.Server, len(list))
	for i := range list {
		byName[list[i].Name] = &list[i]
	}
	var out []Change
	seen := map[string]bool{}
	if all {
		for i := range list {
			out = append(out, Change{Entity: EntityServers, Op: OpUpsert, ID: list[i].Name, Data: list[i]})
			seen[list[i].Name] = true
		}
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		if srv, ok := byName[id]; ok {
			out = append(out, Change{Entity: EntityServers, Op: OpUpsert, ID: id, Data: *srv})
		} else {
			out = append(out, Change{Entity: EntityServers, Op: OpDelete, ID: id})
		}
	}
	return out
}

func (h *ChangeHub) publish(c Change) {
	h.mu.Lock()
	h.publishLocked(c)
	h.mu.Unlock()
}

func (h *ChangeHub) publishLocked(c Change) {
	if h.closed {
		return
	}
	h.seq++
	c.Seq = h.seq
	for ch := range h.subs {
		select {
		case ch <- c:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func sortedIDs(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
