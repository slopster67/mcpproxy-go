package runtime

import (
	"sync"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// lastUsedTimes holds each server's latest tool call. It is read from the
// activity log once, on first use, and advanced as calls are recorded.
type lastUsedTimes struct {
	mu     sync.Mutex
	loaded bool
	times  map[string]time.Time
}

func (l *lastUsedTimes) load(store *storage.Manager) {
	if l.loaded {
		return
	}
	l.loaded = true
	l.times = map[string]time.Time{}
	if store == nil {
		return
	}
	if times, err := store.LastCallPerServer(); err == nil {
		l.times = times
	}
}

func (l *lastUsedTimes) get(server string, store *storage.Manager) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load(store)
	at, ok := l.times[server]
	return at, ok
}

// record notes a call at `at` and reports whether it is the server's latest.
// Before the first read it only marks the server changed; the log read then
// includes the call.
func (l *lastUsedTimes) record(server string, at time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		return true
	}
	if previous, ok := l.times[server]; ok && !at.After(previous) {
		return false
	}
	l.times[server] = at
	return true
}
