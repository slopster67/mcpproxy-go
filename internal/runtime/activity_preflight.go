package runtime

import (
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/preflight"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// Errors a preflight activity write can fail with. They are sentinels because
// the served preflight surface must answer 503 when the record could not be
// persisted (a 200 without its activity record would break the transparency
// guarantee), and it should be able to tell "the proxy is
// shutting down" from "the write failed".
var (
	// ErrActivityUnavailable means there is no activity store to write to.
	ErrActivityUnavailable = errors.New("activity service unavailable")
	// ErrActivityShuttingDown means the service already closed its write
	// barrier; the DB may be closing, so nothing new may be written.
	ErrActivityShuttingDown = errors.New("activity service is shutting down")
)

// PreflightToolOutcome is one per-tool line of a preflight activity record.
// Reason is empty for a ready tool.
type PreflightToolOutcome struct {
	ID     string
	Status string
	Reason string
}

// PreflightArguments is one in-band caller's request AS SENT, before trimming
// and dedup.
//
// It exists because ids_count is the count of UNIQUE ids — the definition the
// REST record already uses, and the one that makes the two records mean the same
// thing — which on its own would make "the agent asked for 12 ids, three of them
// duplicates" unrecoverable from the log. Nothing here is new data: the ids
// already appear per-tool, and the filter names are the closed enum
// describe_tool declares.
type PreflightArguments struct {
	// ToolIDs is the RAW array: request order, untrimmed, duplicates intact.
	// len(ToolIDs) is the raw requested count.
	ToolIDs []string
	// Filters names the annotation filters in effect, in declared order. Empty
	// when the call carried none.
	Filters []string
}

// PreflightActivity is one executed preflight, as the served surface hands it to
// the activity log. Everything here is either an enum value, a count or a tool
// ID — never a description, argument or hash (local activity log only,
// nothing that could leak to a telemetry surface).
type PreflightActivity struct {
	// RequestID correlates the record with the response's X-Request-Id and with
	// every tool call the same workflow makes afterwards.
	RequestID string
	SessionID string
	Source    storage.ActivitySource
	// Verdict is the set-level verdict (preflight.Verdict*).
	Verdict string
	// Status overrides the derived activity status. Leave empty to derive it
	// from Verdict via PreflightActivityStatus.
	Status string
	// Surface names the surface that ran the preflight, for surfaces the
	// Source alone does not identify (storage.PreflightSurfaceMCPCheck).
	// Empty for the REST endpoint.
	Surface string
	// Arguments is the request as the caller sent it. Set by the in-band
	// check surface; nil for the REST endpoint.
	Arguments *PreflightArguments
	// Timestamp defaults to time.Now() when zero.
	Timestamp time.Time
	Tools     []PreflightToolOutcome
}

func PreflightActivityStatus(verdict string) string {
	if verdict == preflight.VerdictReady {
		return storage.ActivityStatusSuccess
	}
	return storage.ActivityStatusBlocked
}

// RecordPreflight persists one preflight run SYNCHRONOUSLY and returns the write
// error.
//
// It deliberately bypasses the bounded async event channel every other activity
// path uses: that channel drops events for a subscriber that falls behind, and
// the record must be durable BEFORE the caller is answered. This uses one
// write on the caller's goroutine, joined to the shutdown barrier via
// enterWrite so it can never straddle a DB close.
func (s *ActivityService) RecordPreflight(rec PreflightActivity) error {
	if s == nil || s.storage == nil {
		return ErrActivityUnavailable
	}
	if !s.enterWrite() {
		return ErrActivityShuttingDown
	}
	defer s.workersWG.Done()

	timestamp := rec.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	status := rec.Status
	if status == "" {
		status = PreflightActivityStatus(rec.Verdict)
	}
	source := rec.Source
	if source == "" {
		source = storage.ActivitySourceAPI
	}

	record := &storage.ActivityRecord{
		Type:          storage.ActivityTypePreflight,
		Source:        source,
		Status:        status,
		Timestamp:     timestamp,
		SessionID:     rec.SessionID,
		RequestID:     rec.RequestID,
		WorkSessionID: s.resolveWorkSession(rec.SessionID),
		Metadata:      preflightMetadata(rec),
	}

	if err := s.storage.SaveActivity(record); err != nil {
		if s.logger != nil {
			s.logger.Error("Failed to save preflight activity record",
				zap.Error(err),
				zap.String("request_id", rec.RequestID),
				zap.String("verdict", rec.Verdict))
		}
		return fmt.Errorf("save preflight activity record: %w", err)
	}
	return nil
}

// RecordPreflight is the Runtime-level passthrough onto the activity service's
// synchronous preflight write, so a served surface holding only the runtime can
// record durably without reaching for the service directly.
func (r *Runtime) RecordPreflight(rec PreflightActivity) error {
	if r == nil || r.activityService == nil {
		return ErrActivityUnavailable
	}
	return r.activityService.RecordPreflight(rec)
}

// preflightMetadata builds the documented payload:
// {verdict, ids_count, reasons{code:count}, per_tool[{id,status,reason?}]}.
func preflightMetadata(rec PreflightActivity) map[string]interface{} {
	reasons := make(map[string]int)
	perTool := make([]map[string]interface{}, 0, len(rec.Tools))
	for _, tool := range rec.Tools {
		entry := map[string]interface{}{
			storage.PreflightPerToolKeyID:     tool.ID,
			storage.PreflightPerToolKeyStatus: tool.Status,
		}
		// A ready tool carries no reason at all — the field is omitted rather
		// than emitted empty, mirroring the wire DTO.
		if tool.Reason != "" {
			entry[storage.PreflightPerToolKeyReason] = tool.Reason
			reasons[tool.Reason]++
		}
		perTool = append(perTool, entry)
	}

	metadata := map[string]interface{}{
		storage.MetadataKeyPreflightVerdict:  rec.Verdict,
		storage.MetadataKeyPreflightIDsCount: len(rec.Tools),
		storage.MetadataKeyPreflightReasons:  reasons,
		storage.MetadataKeyPreflightPerTool:  perTool,
	}
	// Absent, not empty, for the REST surface: adding a key to every record it
	// has written since spec 098 would change a payload nothing asked to change.
	// The same rule governs the raw arguments below.
	if rec.Surface != "" {
		metadata[storage.MetadataKeyPreflightSurface] = rec.Surface
	}
	if rec.Arguments != nil {
		arguments := map[string]interface{}{
			// Present even when empty: "what was asked for" is the whole point
			// of the key, and an absent array would read as "not recorded".
			storage.PreflightArgumentsKeyToolIDs: rec.Arguments.ToolIDs,
		}
		if len(rec.Arguments.Filters) > 0 {
			arguments[storage.PreflightArgumentsKeyFilters] = rec.Arguments.Filters
		}
		metadata[storage.MetadataKeyPreflightArguments] = arguments
	}
	return metadata
}
