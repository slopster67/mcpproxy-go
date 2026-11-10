package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/slopster67/mcpproxy-go/internal/peering"
)

func (s *Server) registerPeerRoutes(r chi.Router) {
	r.Get("/peers", s.handleListPeers)
	r.Get("/peers/{peer}", s.handleGetPeer)
	r.Post("/peers", s.handleCreatePeer)
	r.Post("/peers/{peer}/rotate", s.handleRotatePeer)
	r.Delete("/peers/{peer}", s.handleDeletePeer)
}

// PeerTokenResponse is the data of POST /peers and POST /peers/{id}/rotate:
// the peer object plus its connection token, shown ONCE (no-store).
type PeerTokenResponse struct {
	peering.Peer
	Token string `json:"token"`
}

func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := s.controller.GetPeerHub().List()
	if err != nil {
		s.writeError(w, r, 500, "Could not list peers")
		return
	}
	s.writeSuccess(w, map[string]any{"peers": peers})
}
func (s *Server) handleGetPeer(w http.ResponseWriter, r *http.Request) {
	peer, err := s.controller.GetPeerHub().Get(chi.URLParam(r, "peer"))
	if err != nil {
		s.peerError(w, r, err)
		return
	}
	s.writeSuccess(w, peer)
}
func (s *Server) handleCreatePeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		s.writeError(w, r, 400, "Invalid peer")
		return
	}
	peer, token, err := s.controller.GetPeerHub().Create(req.Name)
	if err != nil {
		s.writeError(w, r, 400, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeSuccess(w, PeerTokenResponse{Peer: peer, Token: token})
}
func (s *Server) handleRotatePeer(w http.ResponseWriter, r *http.Request) {
	hub := s.controller.GetPeerHub()
	id := chi.URLParam(r, "peer")
	token, err := hub.Rotate(id)
	if err != nil {
		s.peerError(w, r, err)
		return
	}
	peer, err := hub.Get(id)
	if err != nil {
		s.peerError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeSuccess(w, PeerTokenResponse{Peer: peer, Token: token})
}
func (s *Server) deletePeer(id string) error {
	if cfg := s.controller.GetCurrentConfig(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.ExecutionHost() == id {
				return &requestError{409, "Move or delete this peer’s servers before deleting the peer"}
			}
		}
	}
	if s.clientsService != nil {
		clients, err := s.clientsService.List()
		if err != nil {
			return &requestError{500, "Could not check peer clients"}
		}
		for _, client := range clients {
			if client.HostID == id {
				return &requestError{409, "Move or delete this peer’s clients before deleting the peer"}
			}
		}
	}
	return s.controller.GetPeerHub().Delete(id)
}
func (s *Server) handleDeletePeer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "peer")
	if err := s.deletePeer(id); err != nil {
		s.writeManagementError(w, r, err)
		return
	}
	s.writeSuccess(w, map[string]any{"deleted": id})
}

func (s *Server) peerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, peering.ErrNotFound) {
		s.writeError(w, r, 404, "Peer not found")
		return
	}
	s.writeError(w, r, 500, "Could not update peer")
}
