package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

type clientPresence struct {
	AllowCodeExecution bool   `json:"allow_code_execution"`
	AllowManaging      bool   `json:"allow_managing"`
	HostID             string `json:"host_id,omitempty"`
	ID                 string `json:"id"`
	DisplayName        string `json:"display_name"`
	Kind               string `json:"kind"`
	State              string `json:"state"`

	Connected bool `json:"connected"`

	LastSeen       *time.Time `json:"last_seen"`
	ActiveSessions int        `json:"active_sessions"`
	Calls24h       int        `json:"calls_24h"`

	Sessions []clientSession `json:"sessions,omitempty"`

	//
	// credential_state is what the client's connection carries (client |
	// admin_key | none | revoked | expired | unknown). The stat-only list never
	// reads a config: it reports the token store, else the last on-demand
	// observation (credential_checked_at says when), else unknown.
	CredentialState profile.CredentialState `json:"credential_state"`

	TokenName   string `json:"token_name,omitempty"`
	Profile     string `json:"profile,omitempty"`
	ProfileMode string `json:"profile_mode,omitempty"`
	// profile_source is pin (locked) or binding (switchable) and is empty when
	// credential_state is not client.
	ProfileSource  string `json:"profile_source,omitempty"`
	ProfileMissing bool   `json:"profile_missing,omitempty"`
	Blocked24h     int    `json:"blocked_24h"`

	Issuer *auth.CredentialIssuer `json:"issuer,omitempty"`

	ProfileState string `json:"profile_state,omitempty"`
}

type clientSession struct {
	ID            string    `json:"id"`
	WorkSessionID string    `json:"work_session_id,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	LastActivity  time.Time `json:"last_activity"`
	Profile       string    `json:"profile,omitempty"`
	ProfileSource string    `json:"profile_source,omitempty"`
}

// clientSessionCap bounds the sessions a detail read returns (newest first).
const clientSessionCap = 20

type clientsResponse struct {
	Clients  []clientPresence `json:"clients"`
	Warnings []ClientWarning  `json:"warnings"`
}

func (s *Server) handleGetClients(w http.ResponseWriter, r *http.Request) {
	// profile and client are honoured here; token is not (a
	// client row is not addressed by token) and stays a 400.
	if !rejectUnsupportedScopeFilters(w, r, "profile", "client") {
		return
	}
	resp, err := s.clientsListResponse(r.Context(), r.URL.Query().Get("profile"), r.URL.Query().Get("client"))
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	s.writeSuccess(w, resp)
}

// clientsListResponse is the data of GET /clients: the rows (filtered by the
// current binding) and the warnings computed over the full
// set. It is shared by the route and the MCP `profiles` tool's list_clients.
func (s *Server) clientsListResponse(ctx context.Context, profileFilter, clientFilter string) (clientsResponse, error) {

	// An exact ?client= filter also shows that client's REVOKED custom row
	// (a deep link to a revoked worker reads "Revoked"). The
	// detail id does not inspect a client configuration file.
	rows, warnings, err := s.clientRows(ctx, false, clientFilter)
	if err != nil {
		return clientsResponse{}, err
	}
	rows = filterClientRows(rows, profileFilter, clientFilter)
	return clientsResponse{Clients: rows, Warnings: warnings}, nil
}

func (s *Server) handleGetClient(w http.ResponseWriter, r *http.Request) {
	if !rejectUnsupportedScopeFilters(w, r) {
		return
	}
	id := chi.URLParam(r, "client")
	rows, _, err := s.clientRows(r.Context(), true, id)
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	for _, row := range rows {
		if row.ID == id {
			s.writeSuccess(w, row)
			return
		}
	}
	s.writeError(w, r, http.StatusNotFound, "client not found")
}

// filterClientRows applies the list filters to the CURRENT binding:
// profile=X keeps rows with an ACTIVE client credential bound to X (a dangling
// pin included), profile=- those bound to All servers (an empty pin); a row
// with no active credential matches no profile. client= is an exact id match.
// An unknown value is not an error: no rows.
func filterClientRows(rows []clientPresence, profileFilter, clientFilter string) []clientPresence {
	if profileFilter == "" && clientFilter == "" {
		return rows
	}
	out := make([]clientPresence, 0, len(rows))
	for _, row := range rows {
		if clientFilter != "" && row.ID != clientFilter {
			continue
		}
		if profileFilter != "" {
			if row.CredentialState != profile.CredentialStateClient {
				continue
			}
			if profileFilter == "-" && row.Profile != "" || profileFilter != "-" && row.Profile != profileFilter {
				continue
			}
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) clientRows(ctx context.Context, withSessions bool, detailID string) ([]clientPresence, []internalRuntime.Warning, error) {
	rows := make([]clientPresence, 0)
	if s.clientsService == nil {
		return rows, []internalRuntime.Warning{}, nil
	}
	records, err := s.clientsService.Records()
	if err != nil {
		return nil, nil, err
	}
	sessions, total, err := s.controller.GetRecentSessions(storage.SessionFilter{Limit: 100})
	if err != nil {
		return nil, nil, err
	}
	if total > len(sessions) {
		sessions, _, err = s.controller.GetRecentSessions(storage.SessionFilter{Limit: total})
		if err != nil {
			return nil, nil, err
		}
	}
	cfg, _ := s.controller.GetConfig()
	stats := s.clientStats(ctx)
	for i := range records {
		rec := &records[i]
		if rec.Kind != auth.KindClient {
			continue
		}
		row := clientPresence{AllowCodeExecution: rec.AllowCodeExecution, AllowManaging: rec.AllowManaging, HostID: rec.HostID, ID: rec.ClientID, DisplayName: rec.DisplayName, Kind: "custom", TokenName: rec.Name, Profile: rec.ProfilePin, ProfileMode: rec.ProfileMode, CredentialState: s.clientsService.StateOf(rec), LastSeen: rec.LastUsedAt, Calls24h: stats.ByClient[rec.ClientID].Calls, Blocked24h: stats.ByClient[rec.ClientID].Blocked}
		if row.DisplayName == "" {
			row.DisplayName = row.ID
		}
		decorateLifecycle(&row, rec, cfg)
		row.ProfileMissing = rec.ProfilePin != "" && row.ProfileState == internalRuntime.ProfileStateDangling

		row.State = contracts.ClientPresenceConnectedNeverSeen
		if rec.LastUsedAt != nil {
			row.State = contracts.ClientPresenceConnectedSeen
		}
		if row.CredentialState == profile.CredentialStateClient {
			row.ProfileSource = string(profile.SourceBinding)
			if rec.ProfileMode == auth.ProfileModeLocked {
				row.ProfileSource = string(profile.SourcePin)
			}
		}
		for _, session := range sessions {
			if session.ClientID != rec.ClientID || (rec.ConnectedAt != nil && session.StartTime.Before(*rec.ConnectedAt)) {
				continue
			}
			if session.Status == "active" {
				row.ActiveSessions++
			}
			if withSessions && rec.ClientID == detailID && len(row.Sessions) < clientSessionCap {
				row.Sessions = append(row.Sessions, clientSession{ID: session.ID, WorkSessionID: session.WorkSessionID, StartedAt: session.StartTime, LastActivity: session.LastActivity, Profile: session.Profile, ProfileSource: session.ProfileSource})
			}
		}
		if rec.HostID != "" && !s.controller.GetPeerHub().Online(rec.HostID) {
			row.ActiveSessions = 0
		}
		row.Connected = row.ActiveSessions > 0
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].DisplayName) < strings.ToLower(rows[j].DisplayName)
	})
	return rows, s.clientsService.Warnings(), nil
}

// clientStats reads the 24 h rollup by client (administrator scope).
func (s *Server) clientStats(ctx context.Context) internalRuntime.ActivityStats24h {
	if p, ok := s.profiles().(interface {
		Stats24h(context.Context) internalRuntime.ActivityStats24h
	}); ok {
		return p.Stats24h(ctx)
	}
	return internalRuntime.ActivityStats24h{ByProfile: map[string]internalRuntime.Counter{}, ByClient: map[string]internalRuntime.Counter{}}
}

func decorateLifecycle(row *clientPresence, rec *auth.ClientCredential, cfg *config.Config) {
	row.Issuer = rec.Issuer
	row.ProfileState = internalRuntime.ProfileStateOf(rec.ProfilePin, cfg)
}

// recording it here) so they stay byte-for-byte identical.
