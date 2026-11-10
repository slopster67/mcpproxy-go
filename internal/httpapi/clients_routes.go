package httpapi

import (
	"github.com/go-chi/chi/v5"
)

// registerClientRoutes registers the client credential routes. Client writes that can change
// a binding's reach go through the clients service, which runs the
// BindingGuardDelta before anything is written.
func (s *Server) registerClientRoutes(r chi.Router) {
	r.Get("/clients", s.handleGetClients)
	r.Get("/clients/{client}", s.handleGetClient)
	r.Post("/clients", s.handleCreateClient)
	r.Patch("/clients/{client}", s.handlePatchClient)
	r.Post("/clients/{client}/rotate", s.handleRotateClient)
	r.Delete("/clients/{client}", s.handleDeleteClient)
}
