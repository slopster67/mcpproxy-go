package httpapi

import (
	"fmt"
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

func (s *Server) approveTools(serverID string, req approveToolsRequest) (any, error) {
	if serverID == "" {
		return nil, &requestError{400, "Server is required"}
	}
	if req.ExpectedHashes == nil {
		return nil, &requestError{http.StatusBadRequest, "expected_hashes is required; load the tools before approving them"}
	}

	if req.ApproveAll {
		var (
			count int
			err   error
		)
		count, err = s.approveAllReviewed(serverID, req.ExpectedHashes, req.ExpectedIncarnation)
		if err != nil {
			return nil, err
		}
		response := map[string]interface{}{
			"approved": count,
			"message":  fmt.Sprintf("Approved %d tools for server %s", count, serverID),
		}
		if _, outcome, ok := s.toolApprovalOutcomeFor(serverID); ok {
			outcome.addTo(response)
			if held := outcome.StillPending + outcome.StillChanged; held > 0 {
				response["message"] = fmt.Sprintf("Approved %d tools for server %s; %d still need review", count, serverID, held)
			}
		} else {
			response["outcome_unavailable"] = true
			response["message"] = fmt.Sprintf("Approved %d tools for server %s, but the remaining tool state could not be read back; check it with GET /api/v1/review?server=%s", count, serverID, serverID)
		}
		return response, nil
	}

	if len(req.Tools) == 0 {
		return nil, &requestError{http.StatusBadRequest, "Either 'tools' array or 'approve_all: true' required"}
	}

	req.Tools = uniqueStrings(req.Tools)
	approveErr := s.approveToolsReviewed(serverID, req.Tools, req.ExpectedHashes, req.ExpectedIncarnation)
	if approveErr != nil {
		return nil, approveErr
	}

	// Report what actually applied. "approved" counts the requested tools
	// whose records now read approved; "not_approved" lists the rest.
	// When the records cannot be read back the outcome is unknown: report 0
	// applied with outcome_unavailable rather than echoing the request.
	approved := 0
	response := map[string]interface{}{"tools": req.Tools}
	records, outcome, ok := s.toolApprovalOutcomeFor(serverID)
	if !ok {
		response["approved"] = 0
		response["outcome_unavailable"] = true
		response["message"] = fmt.Sprintf("Approval of %d tools for server %s was submitted, but the resulting state could not be read back; check it with GET /api/v1/review?server=%s", len(req.Tools), serverID, serverID)
		return response, nil
	}
	byName := make(map[string]*storage.ToolApprovalRecord, len(records))
	for _, rec := range records {
		if rec != nil {
			byName[rec.ToolName] = rec
		}
	}
	var notApproved []string
	for _, name := range req.Tools {
		if rec := byName[name]; rec != nil && rec.Status == storage.ToolApprovalStatusApproved {
			approved++
		} else {
			notApproved = append(notApproved, name)
		}
	}
	if len(notApproved) > 0 {
		response["not_approved"] = notApproved
	}
	outcome.addTo(response)
	response["approved"] = approved
	response["message"] = fmt.Sprintf("Approved %d of %d requested tools for server %s", approved, len(req.Tools), serverID)
	return response, nil
}
