package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

func (s *Server) globalToolsData(ctx context.Context, serverFilter string) (*contracts.GlobalToolsResponse, error) {
	allServers, err := s.controller.ListServers()
	if err != nil {
		return nil, err
	}

	// Usage rollup is a single bounded pass over the activity log; a failure
	// here must not fail the whole page — usage columns just stay zero.
	usage, usageErr := s.controller.AggregateToolUsage(time.Now().Add(-globalToolsUsageWindow))
	if usageErr != nil {
		s.logger.Warnw("Global tools: usage aggregation failed, continuing without usage", "error", usageErr)
		usage = map[string]storage.ToolUsageStat{}
	}

	mgmtSvc := s.controller.GetManagementService()
	hasMgmt := mgmtSvc != nil
	getTools := func(name string) ([]map[string]interface{}, error) {
		if hasMgmt {
			return mgmtSvc.GetServerTools(ctx, name)
		}
		return s.controller.GetServerTools(name)
	}

	resp := contracts.GlobalToolsResponse{
		Tools: make([]contracts.Tool, 0, 256),
	}

	for _, srv := range allServers {
		name := srv.Name
		if name == "" || (serverFilter != "" && name != serverFilter) {
			continue
		}

		// the rows are returned only if the
		// server identity did not move across the read
		// (readAtSettledIncarnation); a server replaced throughout is
		// reported as a failed fetch rather than served under a stale stamp.
		var generic []map[string]interface{}
		serverInc, terr := s.readAtSettledIncarnation(name, func() error { var rerr error; generic, rerr = getTools(name); return rerr })
		cached := !srv.Connected
		if terr != nil || cached {
			if terr != nil {
				resp.Partial = true
				resp.FailedServers = append(resp.FailedServers, name)
			}
			var records []*storage.ToolApprovalRecord
			serverInc, terr = s.readAtSettledIncarnation(name, func() error {
				var err error
				records, err = s.controller.ListToolApprovals(name)
				return err
			})
			if terr != nil {
				continue
			}
			generic = make([]map[string]interface{}, 0, len(records))
			for _, record := range records {
				var input interface{}
				_ = json.Unmarshal([]byte(record.CurrentSchema), &input)
				generic = append(generic, map[string]interface{}{
					"name": record.ToolName, "server_name": name, "description": record.CurrentDescription,
					"inputSchema": input, "outputSchema": record.CurrentOutputSchema, "annotations": record.CurrentAnnotations,
				})
			}
			cached = true
		}

		typed := s.enrichServerTools(name, generic)
		if reader, ok := s.controller.(reviewReader); ok {
			review, rerr := reader.GetServerReview(ctx, name)
			attached, same := attachReviews(typed, name, serverInc, review)
			if rerr != nil || !same {
				resp.Partial = true
				resp.FailedServers = append(resp.FailedServers, name)
			} else {
				typed = attached
			}
		}
		for i := range typed {
			typed[i].ServerIncarnation = serverInc
			typed[i].Cached = cached
			if st, ok := usage[name+"\x00"+typed[i].Name]; ok {
				typed[i].Usage = st.Count
				if !st.LastUsed.IsZero() {
					lu := st.LastUsed
					typed[i].LastUsed = &lu
				}
			}
		}
		resp.Tools = append(resp.Tools, typed...)
	}

	updateGlobalToolsStats(&resp)
	return &resp, nil
}
func updateGlobalToolsStats(resp *contracts.GlobalToolsResponse) {
	resp.Stats = contracts.GlobalToolsStats{}

	for i := range resp.Tools {
		t := &resp.Tools[i]
		resp.Stats.Total++
		if t.Disabled {
			resp.Stats.Disabled++
		} else {
			resp.Stats.Enabled++
		}
		if t.ApprovalStatus == storage.ToolApprovalStatusPending || t.ApprovalStatus == storage.ToolApprovalStatusChanged {
			resp.Stats.PendingApproval++
		}
	}

}
