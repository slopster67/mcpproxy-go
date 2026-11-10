package storage

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/slopster67/mcpproxy-go/internal/database"
)

const (
	// DefaultToolCallMaxResponseSize bounds the marshalled response persisted
	// on a single per-server tool-call record, in bytes. It matches the
	// activity log's DefaultMaxResponseSize so the two record types that
	// mirror the same call are bounded the same way.
	DefaultToolCallMaxResponseSize = 64 * 1024

	// DefaultToolCallMaxRecordsPerServer bounds how many call records a single
	// server_<id>_tool_calls bucket retains. The store is a debugging history,
	// not an audit log — the activity log is the durable record — so it keeps
	// a recent window rather than everything.
	DefaultToolCallMaxRecordsPerServer = 1000

	// toolCallPreviewBytes is how much of an oversized payload survives as a
	// readable head on the stored record. It is an upper bound: previewBytes
	// shrinks it further so the placeholder itself always fits inside the
	// configured cap, which a fixed 2KB head would blow through on a cap of,
	// say, 1KB.
	toolCallPreviewBytes = 2048

	// serverIdentitiesBucket holds the ServerIdentity records that map a
	// server NAME to the SHA-256 id its tool-call bucket is keyed by.
	serverIdentitiesBucket = "server_identities"

	// codeExecutionServerID is the synthetic server id under which the
	// code_execution built-in records its parent call. It has a real bucket
	// but no entry in server_identities, so every sweep keyed on configured
	// servers must whitelist it or it deletes the parent of every recorded
	// script run.
	codeExecutionServerID = "code_execution"
)

// toolCallsBucketName is the single place the per-server bucket name is built.
func toolCallsBucketName(serverID string) string {
	return fmt.Sprintf("server_%s_tool_calls", serverID)
}

// SetToolCallLimits configures the per-record response cap (in bytes of
// marshalled JSON) and the per-server record count for the tool-call history.
//
// Non-positive values are ignored, leaving the current limit in place, so a
// config that omits the keys cannot make the history unbounded. This mirrors
// ActivityService.SetMaxResponseSize.
func (m *Manager) SetToolCallLimits(maxResponseBytes, maxRecordsPerServer int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if maxResponseBytes > 0 {
		m.toolCallMaxResponseBytes = maxResponseBytes
	}
	if maxRecordsPerServer > 0 {
		m.toolCallMaxRecords = maxRecordsPerServer
	}
}

// toolCallLimits reads the effective limits, falling back to the documented
// defaults for a Manager built before SetToolCallLimits ran.
func (m *Manager) toolCallLimits() (maxResponseBytes, maxRecords int) {
	maxResponseBytes = m.toolCallMaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = DefaultToolCallMaxResponseSize
	}
	maxRecords = m.toolCallMaxRecords
	if maxRecords <= 0 {
		maxRecords = DefaultToolCallMaxRecordsPerServer
	}
	return maxResponseBytes, maxRecords
}

// boundToolCallResponse returns a copy of record whose Response is bounded to
// maxBytes of marshalled JSON, or record itself when it already fits.
//
// The caller keeps its own record after RecordToolCall returns — mcp.go reuses
// it for token accounting and code_execution links children to the parent — so
// this never writes through the pointer it was given.
//
// Response stays a JSON OBJECT. contracts.ToolCallRecord.Response is a
// documented REST payload on four endpoints (GET /api/v1/tool-calls and
// friends) and httpapi.maskToolCallRecord walks it as structured JSON, so
// replacing it with a bare string would be a breaking API change.
func boundToolCallResponse(record *ToolCallRecord, maxBytes int) *ToolCallRecord {
	if record == nil {
		return record
	}

	bounded := record
	if args := boundToolCallArguments(record.Arguments, maxBytes); args != nil {
		copied := *record
		copied.Arguments = args
		copied.ArgumentsTruncated = true
		bounded = &copied
	}
	if record.Response == nil {
		return bounded
	}

	encoded, err := json.Marshal(record.Response)
	if err != nil {
		// An unmarshallable response would fail the record write below anyway;
		// leave it for that path to report rather than guessing at its size.
		return bounded
	}
	if len(encoded) <= maxBytes {
		return bounded
	}

	if bounded == record {
		copied := *record
		bounded = &copied
	}
	bounded.Response = boundedPlaceholder(string(encoded), maxBytes,
		"response omitted: exceeded tool_call_max_response_size. "+
			"The full response was delivered to the caller; only this stored copy is shortened.")
	bounded.ResponseTruncated = true
	bounded.ResponseBytes = int64(len(encoded))
	return bounded
}

// boundToolCallArguments caps the stored arguments the same way, returning nil
// when they already fit.
//
// A tool called with a megabyte of input persists that megabyte too — the
// echo-shaped case makes the record twice as big as the response cap allows.
// Unlike the activity log this store feeds no detector (sensitive-data
// scanning runs on the activity path, which keeps its own copy), so shortening
// here narrows nothing.
func boundToolCallArguments(args map[string]interface{}, maxBytes int) map[string]interface{} {
	if len(args) == 0 {
		return nil
	}
	encoded, err := json.Marshal(args)
	if err != nil || len(encoded) <= maxBytes {
		return nil
	}
	return boundedPlaceholder(string(encoded), maxBytes,
		"arguments omitted: exceeded tool_call_max_response_size. "+
			"The full arguments were sent to the upstream server; only this stored copy is shortened.")
}

func boundedPlaceholder(encoded string, maxBytes int, note string) map[string]interface{} {
	build := func(preview string, withNote bool) map[string]interface{} {
		p := map[string]interface{}{
			"truncated":      true,
			"original_bytes": len(encoded),
		}
		if preview != "" {
			p["preview"] = preview + truncationMarker
		}
		if withNote {
			p["note"] = note
		}
		return p
	}

	fits := func(p map[string]interface{}) bool {
		encodedPlaceholder, err := json.Marshal(p)
		return err == nil && len(encodedPlaceholder) <= maxBytes
	}

	raw := truncateRuneSafe(encoded, toolCallPreviewBytes)
	for {
		if p := build(raw, true); fits(p) {
			return p
		}
		if raw == "" {
			break
		}
		raw = truncateRuneSafe(raw, len(raw)/2)
	}

	if p := build("", true); fits(p) {
		return p
	}
	return build("", false)
}

// truncationMarker is appended to a shortened preview so a reader can see the
// text was cut rather than being a short payload.
const truncationMarker = "...[truncated]"

// truncateRuneSafe cuts s to at most maxBytes without splitting a rune. It adds
// no marker: callers that want one append it, so the marker's own length is
// visible to the size arithmetic instead of hiding inside this function.
func truncateRuneSafe(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func pruneToolCallBucket(bucket *database.Bucket, maxRecords int) error {
	cursor := bucket.Cursor()

	kept := 0
	k, _ := cursor.Last()
	for k != nil && kept < maxRecords {
		kept++
		k, _ = cursor.Prev()
	}
	if k == nil {
		return nil
	}

	// Everything from here to the front is excess. Collect first, then delete:
	// mutating through the cursor while stepping it is not safe in database.
	var doomed [][]byte
	for ; k != nil; k, _ = cursor.Prev() {
		doomed = append(doomed, append([]byte(nil), k...))
	}
	for _, key := range doomed {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// deleteToolCallBucketTx drops a server's whole call history inside tx.
// Reports whether a bucket was actually removed.
func deleteToolCallBucketTx(tx *database.Tx, serverID string) (bool, error) {
	name := []byte(toolCallsBucketName(serverID))
	if tx.Bucket(name) == nil {
		return false, nil
	}
	if err := tx.DeleteBucket(name); err != nil {
		return false, err
	}
	return true, nil
}
