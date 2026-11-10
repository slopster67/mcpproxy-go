package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// ProfilesAPI is what the /profiles and /access/explain handlers need from the
// profiles service. *runtime.ProfilesService implements it.
type ProfilesAPI interface {
	List(ctx context.Context) (*internalRuntime.ProfileList, error)
	Get(ctx context.Context, name string) (*internalRuntime.ProfileView, error)
	Create(ctx context.Context, a internalRuntime.Actor, p config.ProfileConfig) (*internalRuntime.WriteResult, error)
	Update(ctx context.Context, a internalRuntime.Actor, name string, p config.ProfileConfig) (*internalRuntime.WriteResult, error)
	Rename(ctx context.Context, a internalRuntime.Actor, name, newName string) (*internalRuntime.RenameResult, error)
	Delete(ctx context.Context, a internalRuntime.Actor, name, reassignTo string, force bool) (*internalRuntime.DeleteResult, error)
	EffectiveTools(ctx context.Context, name string, opt internalRuntime.EffectiveToolsOptions) (*internalRuntime.EffectiveToolsResult, error)
	Explain(ctx context.Context, subject profile.AccessSubject, tool string) (*internalRuntime.AccessExplanation, error)
}

var _ ProfilesAPI = (*internalRuntime.ProfilesService)(nil)

// SetProfilesService installs the profiles service behind the /profiles routes.
func (s *Server) SetProfilesService(svc ProfilesAPI) { s.profilesService = svc }

// profiles returns the installed profiles service.
func (s *Server) profiles() ProfilesAPI { return s.profilesService }

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := s.profiles().List(r.Context())
	if err != nil {
		s.writeProfileServiceError(w, r, err)
		return
	}
	s.writeSuccess(w, internalRuntime.ProfileList{Profiles: list.Profiles})
}

func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.profiles().Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeProfileServiceError(w, r, err)
		return
	}
	s.writeSuccess(w, v)
}

// --- error mapping ---------------------------------------------------------------

// writeProfileServiceError maps a profiles-service error to its wire shape
// (contracts/rest-api.md, contracts/refusals.md).
func (s *Server) writeProfileServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if s.writeProfilesError(w, r, err) {
		return
	}
	s.logger.Errorw("profiles operation failed", "error", err)
	s.writeError(w, r, http.StatusInternalServerError, "profiles operation failed")
}
