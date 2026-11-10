package server

import (
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/v2/mcp"
)

const (
	// activityErrorMessageLimit caps the recorded error text. Activity records
	// are persisted and streamed to the UI, so an upstream that answers with a
	// multi-megabyte error body must not be able to bloat the log.
	activityErrorMessageLimit = 512

	// activityErrorMessageEllipsis marks a message the cap above shortened.
	activityErrorMessageEllipsis = "…"

	// upstreamErrorResultMessage is recorded when the upstream flagged the
	// result as an error but supplied nothing readable to explain it. An empty
	// error_message would render as a blank row in the activity UI.
	upstreamErrorResultMessage = "upstream returned an error result (isError=true)"
)

// activityStatusForResult classifies a successfully dispatched tool result for
// the activity log, returning the (status, error_message) pair to record.
//
// It returns ("success", "") for anything that is not an MCP result flagged
// isError — including nil and non-CallToolResult values, which several call
// paths can produce — and ("error", <upstream text>) otherwise.
func activityStatusForResult(result interface{}) (status, errorMsg string) {
	ctr := asCallToolResult(result)
	if ctr == nil || !ctr.IsError {
		return "success", ""
	}
	return "error", upstreamErrorText(ctr)
}

// asCallToolResult normalises the interface{} that upstream.Manager.CallTool
// returns. Both the pointer and the value form are accepted; anything else
// (string, map, nil) yields nil.
func asCallToolResult(result interface{}) *mcp.CallToolResult {
	switch v := result.(type) {
	case *mcp.CallToolResult:
		return v
	case mcp.CallToolResult:
		return &v
	default:
		return nil
	}
}

// upstreamErrorText extracts a human-readable reason from an isError result:
// its text blocks first (that is where MCP servers put the message), then its
// structured content, then a fixed fallback.
func upstreamErrorText(ctr *mcp.CallToolResult) string {
	parts := make([]string, 0, len(ctr.Content))
	for _, c := range ctr.Content {
		var text string
		switch tc := c.(type) {
		case mcp.TextContent:
			text = tc.Text
		case *mcp.TextContent:
			if tc != nil {
				text = tc.Text
			}
		}
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}

	msg := strings.Join(parts, "\n")
	if msg == "" && ctr.StructuredContent != nil {
		if encoded, err := json.Marshal(ctr.StructuredContent); err == nil {
			msg = strings.TrimSpace(string(encoded))
		}
	}
	if msg == "" {
		return upstreamErrorResultMessage
	}
	if len(msg) > activityErrorMessageLimit {
		// safeTruncateBytes backs the cut up to a rune boundary, so the stored
		// message is always valid UTF-8.
		msg = msg[:safeTruncateBytes(msg, activityErrorMessageLimit)] + activityErrorMessageEllipsis
	}
	return msg
}
