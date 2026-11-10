package httpapi

import (
	"context"
	"encoding/json"

	"github.com/slopster67/mcpproxy-go/internal/contracts"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

type reviewReader interface {
	GetServerReview(ctx context.Context, serverName string) (*internalRuntime.ServerReview, error)
}

// attachReviews adds each awaiting tool's review to its row in tools (one
// server's rows, read at incarnation). A tool awaiting review that is not in
// the live listing gets a row built from its review. It reports false when the
// server moved between the two reads, so the caller fails the server's fetch
// rather than pairing rows and reviews of different incarnations.
func attachReviews(tools []contracts.Tool, server string, incarnation int64, review *internalRuntime.ServerReview) ([]contracts.Tool, bool) {
	if review == nil {
		return tools, true
	}
	if incarnation != 0 && review.Server.Incarnation != 0 && incarnation != review.Server.Incarnation {
		return tools, false
	}
	index := make(map[string]int, len(tools))
	for i := range tools {
		index[tools[i].Name] = i
	}
	for i := range review.Tools {
		tool := review.Tools[i]
		if tool.ApprovalStatus != storage.ToolApprovalStatusPending && tool.ApprovalStatus != storage.ToolApprovalStatusChanged {
			continue
		}
		if at, ok := index[tool.Name]; ok {
			tools[at].Review = &tool
			continue
		}
		row := contracts.Tool{
			ID: server + ":" + tool.Name, Name: tool.Name, ServerName: server, Description: tool.Description,
			Tier: tool.Tier, ApprovalStatus: tool.ApprovalStatus, Disabled: tool.Disabled,
			Classification: tool.Classification, ClassificationHash: tool.ClassificationHash,
			ServerIncarnation: incarnation, Cached: true, Review: &tool,
		}
		_ = json.Unmarshal(tool.InputSchema, &row.Schema)
		_ = json.Unmarshal(tool.OutputSchema, &row.OutputSchema)
		if tool.Annotations != nil {
			if data, err := json.Marshal(tool.Annotations); err == nil {
				_ = json.Unmarshal(data, &row.Annotations)
			}
		}
		tools = append(tools, row)
	}
	return tools, true
}
