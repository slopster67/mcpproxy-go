package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// sseHeartbeatInterval is how often the change stream sends a `: ping`
// comment. A var so tests can shrink it.
var sseHeartbeatInterval = 30 * time.Second

// changeHydrateTimeout bounds one hydration build on the flush goroutine.
const changeHydrateTimeout = 10 * time.Second

// RegisterChangeHydrators installs the builders the change hub uses for
// upserts of the entities whose objects this layer assembles, so an event
// carries exactly what GET /api/v1/{entity}/{id} returns.
func (s *Server) RegisterChangeHydrators(hub *internalRuntime.ChangeHub) {
	if hub == nil {
		return
	}
	withCtx := func(build func(ctx context.Context, id string) (any, bool, error)) internalRuntime.Hydrator {
		return func(id string) (any, bool, error) {
			ctx, cancel := context.WithTimeout(context.Background(), changeHydrateTimeout)
			defer cancel()
			return build(ctx, id)
		}
	}
	hub.SetHydrator(internalRuntime.EntityClients, withCtx(func(ctx context.Context, id string) (any, bool, error) {
		row, err := s.clientObject(ctx, id)
		if err != nil || row == nil {
			return nil, false, err
		}
		return row, true, nil
	}))
	hub.SetHydrator(internalRuntime.EntityProfiles, withCtx(func(ctx context.Context, id string) (any, bool, error) {
		v, err := s.profiles().Get(ctx, id)
		if err != nil {
			var notFound *internalRuntime.ProfileNotFoundError
			if errors.As(err, &notFound) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return v, true, nil
	}))
	hub.SetHydrator(internalRuntime.EntityPeers, func(id string) (any, bool, error) {
		peer, err := s.controller.GetPeerHub().Get(id)
		if err != nil {
			return nil, false, nil
		}
		return peer, true, nil
	})
	hub.SetHydrator(internalRuntime.EntitySessions, func(id string) (any, bool, error) {
		session, err := s.sessionObject(id)
		if err != nil || session == nil {
			return nil, false, err
		}
		return session, true, nil
	})
}

// handleEvents serves the dashboard change stream (GET /api/v1/events).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	hub := s.controller.Changes()
	if hub == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "change stream unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	ch, seq := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if _, err := fmt.Fprint(w, "retry: 5000\n\n"); err != nil {
		return
	}
	if err := writeSSEFrame(w, "hello", map[string]uint64{"seq": seq}); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case change, open := <-ch:
			if !open {
				// Dropped for falling behind: end the stream so the client
				// reconnects, receives a new hello and refetches.
				return
			}
			if err := writeSSEFrame(w, "change", changeView(change)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// changeView converts runtime records to their REST shape.
func changeView(c internalRuntime.Change) internalRuntime.Change {
	if rec, ok := c.Data.(*storage.ActivityRecord); ok {
		view := storageToContractActivity(rec)
		activityView(&view)
		c.Data = view
	}
	return c
}

func writeSSEFrame(w http.ResponseWriter, event string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	return err
}
