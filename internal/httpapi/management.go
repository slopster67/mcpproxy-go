package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	rt "github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// ManagementToolSpec describes a transport adapter, not a second implementation
// of the operation. Inputs use the same Go request types as the dashboard.
type ManagementToolSpec struct {
	Name, Description string
	Input             any
	Operations        []string
}
type ServerManagementRequest struct {
	Operation string                `json:"operation"`
	Name      string                `json:"name,omitempty"`
	Server    *AddServerRequest     `json:"server,omitempty"`
	Enabled   *bool                 `json:"enabled,omitempty"`
	Lines     int                   `json:"lines,omitempty"`
	Convert   *ConvertSecretRequest `json:"convert,omitempty"`
}
type ClientManagementRequest struct {
	Operation string               `json:"operation"`
	ID        string               `json:"id,omitempty"`
	Client    *CreateClientRequest `json:"client,omitempty"`
	Binding   *PatchClientRequest  `json:"binding,omitempty"`
}
type PeerManagementRequest struct {
	Operation string `json:"operation"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
}
type SecretManagementRequest struct {
	Operation string `json:"operation"`
	Name      string `json:"name,omitempty"`
	Value     string `json:"value,omitempty"`
}
type ToolManagementRequest struct {
	Operation           string            `json:"operation"`
	Server              string            `json:"server,omitempty"`
	Tool                string            `json:"tool,omitempty"`
	Tools               []string          `json:"tools,omitempty"`
	ApproveAll          bool              `json:"approve_all,omitempty"`
	ExpectedHashes      map[string]string `json:"expected_hashes,omitempty"`
	ExpectedHash        string            `json:"expected_hash,omitempty"`
	ExpectedIncarnation int64             `json:"expected_incarnation,omitempty"`
	Enabled             *bool             `json:"enabled,omitempty"`
	Tier                string            `json:"tier,omitempty"`
}

type ActivityManagementRequest struct {
	SessionID     string    `json:"session_id,omitempty"`
	RequestID     string    `json:"request_id,omitempty"`
	WorkSessionID string    `json:"work_session_id,omitempty"`
	Types         []string  `json:"types,omitempty"`
	Operation     string    `json:"operation"`
	ID            string    `json:"id,omitempty"`
	Server        string    `json:"server,omitempty"`
	Tool          string    `json:"tool,omitempty"`
	Client        string    `json:"client,omitempty"`
	Profile       string    `json:"profile,omitempty"`
	Status        string    `json:"status,omitempty"`
	Window        string    `json:"window,omitempty"`
	StartTime     time.Time `json:"start_time,omitempty"`
	EndTime       time.Time `json:"end_time,omitempty"`
	Limit         int       `json:"limit,omitempty"`
	Offset        int       `json:"offset,omitempty"`
}

func ManagementToolSpecs() []ManagementToolSpec {
	return []ManagementToolSpec{
		{"manage_servers", "Manage server configuration, icons (data URI), execution hosts, connections, OAuth and logs. create/patch take a server object; patch merges env/headers and null removes a key. OAuth overrides use server.oauth; null resets an OAuth field to automatic. oauth_settings reads the discovered OAuth values. sign_in returns the authorization URL.", ServerManagementRequest{}, []string{"list", "get", "create", "patch", "delete", "set_enabled", "restart", "sign_in", "sign_out", "oauth_settings", "logs", "convert_secret"}},
		{"manage_clients", "Manage clients and their single access token. create/rotate return the newly applied token. allow_managing grants full MCPProxy administration, independently of the upstream profile. update takes a binding object with profile (empty = All servers).", ClientManagementRequest{}, []string{"list", "get", "create", "update", "rotate", "delete"}},
		{"manage_peers", "Manage execution peers. create/rotate return the connection token. A peer with servers or clients must be emptied before deletion.", PeerManagementRequest{}, []string{"list", "create", "rotate", "delete"}},
		{"manage_secrets", "List secret references and their users, or set/delete a secret. Values are never included in activity records.", SecretManagementRequest{}, []string{"list", "set", "delete"}},
		{"manage_tools", "Inspect tools and review definitions, approve the definitions you reviewed, enable/disable tools, and classify unannotated tools. Approval requires expected_hashes and the expected_incarnation from the review. classify requires expected_hash; tier is read, write, destructive or empty to clear.", ToolManagementRequest{}, []string{"list", "review", "approve", "set_enabled", "classify"}},
		{"manage_activity", "Read activity, call details and usage. list supports server/tool/client/profile/status/time filters and pagination. usage window is 24h, 7d or all.", ActivityManagementRequest{}, []string{"list", "get", "sessions", "usage"}},
	}
}
func managementDecode(raw json.RawMessage, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func managementOK(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]bool{"success": true}, nil
}
func managementRequired(value, name string) error {
	if value == "" {
		return &requestError{400, name + " is required"}
	}
	return nil
}
func managementActor(ctx context.Context) rt.Actor {
	return rt.ActorFromContext(ctx, profile.SurfaceMCP)
}

// ManagementOperation is called only after the MCP transport refreshes the
// client's permission from storage. REST uses its own browser authentication.
func (s *Server) ManagementOperation(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if !auth.AuthContextFromContext(ctx).CanManageMCPProxy() {
		return nil, &requestError{403, "MCPProxy management permission is required"}
	}
	switch name {
	case "manage_servers":
		var q ServerManagementRequest
		if err := managementDecode(raw, &q); err != nil {
			return nil, err
		}
		if q.Operation != "list" && q.Operation != "create" {
			if err := managementRequired(q.Name, "name"); err != nil {
				return nil, err
			}
		}
		svc := s.controller.GetManagementService()
		switch q.Operation {
		case "list":
			servers, err := s.controller.ListServers()
			if err != nil {
				return nil, err
			}
			for i := range servers {
				oauth.RedactServerSecretFields(&servers[i])
			}
			return map[string]any{"servers": servers}, nil
		case "get":
			return s.serverObject(q.Name, true)
		case "oauth_settings":
			return s.controller.GetServerOAuthSettings(ctx, q.Name)
		case "create":
			if q.Server == nil {
				return nil, &requestError{400, "server is required"}
			}
			return s.createServer(ctx, *q.Server, true)
		case "patch":
			if q.Server == nil {
				return nil, &requestError{400, "server is required"}
			}
			return s.patchServer(ctx, q.Name, *q.Server, true)
		case "delete":
			return managementOK(s.controller.RemoveServer(ctx, q.Name))
		case "set_enabled":
			if q.Enabled == nil {
				return nil, &requestError{400, "enabled is required"}
			}
			return managementOK(s.controller.EnableServer(q.Name, *q.Enabled))
		case "restart":
			return managementOK(s.controller.RestartServer(q.Name))
		case "logs":
			if q.Lines <= 0 {
				q.Lines = 100
			}
			if q.Lines > 1000 {
				q.Lines = 1000
			}
			return s.controller.GetServerLogs(q.Name, q.Lines)
		case "convert_secret":
			if q.Convert == nil {
				return nil, &requestError{400, "convert is required"}
			}
			return s.convertConfigToSecret(ctx, q.Name, *q.Convert)
		case "sign_in", "sign_out":
			if svc == nil {
				return nil, rt.ErrEvaluatorUnavailable
			}
			if q.Operation == "sign_in" {
				return svc.TriggerOAuthLoginQuick(ctx, q.Name)
			}
			return managementOK(svc.TriggerOAuthLogout(ctx, q.Name))
		}
	case "manage_clients":
		var q ClientManagementRequest
		if err := managementDecode(raw, &q); err != nil {
			return nil, err
		}
		svc := s.clientsService
		if svc == nil {
			return nil, rt.ErrEvaluatorUnavailable
		}
		switch q.Operation {
		case "list":
			return s.ClientsListData(ctx, "", "")
		case "get":
			return s.ClientObjectData(ctx, q.ID)
		case "create":
			if q.Client == nil {
				return nil, &requestError{400, "client is required"}
			}
			return s.createClient(ctx, managementActor(ctx), *q.Client)
		case "update":
			if q.Binding == nil {
				return nil, &requestError{400, "binding is required"}
			}
			return s.updateClient(ctx, managementActor(ctx), q.ID, *q.Binding)
		case "rotate":
			return s.rotateClient(ctx, managementActor(ctx), q.ID)
		case "delete":
			return managementOK(svc.Delete(ctx, managementActor(ctx), q.ID))
		}
	case "manage_peers":
		var q PeerManagementRequest
		if err := managementDecode(raw, &q); err != nil {
			return nil, err
		}
		hub := s.controller.GetPeerHub()
		switch q.Operation {
		case "list":
			return hub.List()
		case "create":
			p, token, err := hub.Create(q.Name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"peer": p, "token": token}, nil
		case "rotate":
			token, err := hub.Rotate(q.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"token": token}, nil
		case "delete":
			return managementOK(s.deletePeer(q.ID))
		}
	case "manage_secrets":
		var q SecretManagementRequest
		if err := managementDecode(raw, &q); err != nil {
			return nil, err
		}
		switch q.Operation {
		case "list":
			return s.inspectGlobalSecrets(ctx, s.controller.GetCurrentConfig())
		case "set":
			return s.setSecret(ctx, SetSecretRequest{Name: q.Name, Value: q.Value})
		case "delete":
			return s.deleteSecret(ctx, q.Name, "")
		}
	case "manage_tools":
		var q ToolManagementRequest
		if err := managementDecode(raw, &q); err != nil {
			return nil, err
		}
		switch q.Operation {
		case "list":
			return s.globalToolsData(ctx, q.Server)
		case "review":
			reader, ok := s.controller.(reviewReader)
			if !ok {
				return nil, rt.ErrEvaluatorUnavailable
			}
			// Tools awaiting review, across servers, carry their review in list.
			if err := managementRequired(q.Server, "server"); err != nil {
				return nil, err
			}
			return reader.GetServerReview(ctx, q.Server)
		case "approve":
			return s.approveTools(q.Server, approveToolsRequest{Tools: q.Tools, ApproveAll: q.ApproveAll, ExpectedHashes: q.ExpectedHashes, ExpectedIncarnation: q.ExpectedIncarnation})
		case "set_enabled":
			if q.Enabled == nil {
				return nil, &requestError{400, "enabled is required"}
			}
			return managementOK(s.controller.SetToolEnabledAt(q.Server, q.Tool, *q.Enabled, "mcp", q.ExpectedIncarnation))
		case "classify":
			if q.Tier != "" && q.Tier != "read" && q.Tier != "write" && q.Tier != "destructive" {
				return nil, &requestError{400, "choose read, write or destructive"}
			}
			return managementOK(s.controller.SetToolClassificationAt(q.Server, q.Tool, q.Tier, q.ExpectedHash, q.ExpectedIncarnation))
		}
	case "manage_activity":
		var q ActivityManagementRequest
		if err := managementDecode(raw, &q); err != nil {
			return nil, err
		}
		switch q.Operation {
		case "list":
			if q.Limit <= 0 {
				q.Limit = 50
			}
			if q.Limit > 100 {
				q.Limit = 100
			}
			filter := q.activityFilter()
			filter.Limit = q.Limit
			rows, total, err := s.controller.ListActivities(filter)
			if err != nil {
				return nil, err
			}
			projected := make([]contracts.ActivityRecord, 0, len(rows))
			for _, row := range rows {
				record := storageToContractActivity(row)
				activityView(&record)
				projected = append(projected, record)
			}
			return contracts.ActivityListResponse{Activities: projected, Total: total, Limit: q.Limit, Offset: q.Offset}, nil
		case "get":
			row, err := s.controller.GetActivity(q.ID)
			if err != nil {
				return nil, err
			}
			if row == nil {
				return nil, &requestError{404, "Activity not found"}
			}
			record := storageToContractActivity(row)
			activityView(&record)
			return record, nil
		case "sessions":
			if q.Limit <= 0 || q.Limit > 100 {
				q.Limit = 100
			}
			rows, total, err := s.controller.GetRecentSessions(storage.SessionFilter{Limit: q.Limit, Status: q.Status, Profile: q.Profile, ClientID: q.Client})
			if err != nil {
				return nil, err
			}
			return map[string]any{"sessions": rows, "total": total}, nil
		case "usage":
			if q.Window == "" {
				q.Window = "24h"
			}
			if q.Window != "24h" && q.Window != "7d" && q.Window != "all" {
				return nil, &requestError{400, "window must be 24h, 7d or all"}
			}
			params := usageParams{window: q.Window, server: q.Server, tool: q.Tool, status: q.Status, profile: q.Profile, client: q.Client, top: 20, sort: "calls"}
			if params.scopeFiltered() {
				return s.scopedUsageResponse(params, time.Now().UTC()), nil
			}
			return buildUsageResponse(s.controller.UsageSnapshot(), params, time.Now().UTC()), nil
		}
	}
	return nil, &requestError{400, fmt.Sprintf("Unknown operation for %s", name)}
}

func (q ActivityManagementRequest) activityFilter() storage.ActivityFilter {
	return storage.ActivityFilter{Types: q.Types, Server: q.Server, Tool: q.Tool, ClientID: q.Client, Profile: q.Profile, Status: q.Status, StartTime: q.StartTime, EndTime: q.EndTime, Limit: q.Limit, Offset: q.Offset, SessionID: q.SessionID, RequestID: q.RequestID, WorkSessionID: q.WorkSessionID}
}
