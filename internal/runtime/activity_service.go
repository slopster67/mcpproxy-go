package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// Default retention configuration
const (
	// DefaultRetentionMaxAge is the default max age for activity records (7 days)
	DefaultRetentionMaxAge = 7 * 24 * time.Hour
	// DefaultRetentionMaxRecords is the default max number of records (10000)
	DefaultRetentionMaxRecords = 10000
	// DefaultRetentionCheckInterval is the default interval between retention checks (1 hour)
	DefaultRetentionCheckInterval = 1 * time.Hour
	// DefaultRetentionMaxSizeBytes is the default total activity-log size cap (256MB)
	DefaultRetentionMaxSizeBytes int64 = 256 * 1024 * 1024
	// DefaultActivityMaxResponseSize is the default per-record response cap (64KB).
	// Aliased here because NewActivityService's first parameter shadows the
	// storage package inside that function body.
	DefaultActivityMaxResponseSize = storage.DefaultMaxResponseSize
)

// SessionClientResolver maps an MCP session id to the client that opened it
// (clientInfo.name / clientInfo.version from the initialize handshake).
// Returns empty strings when the session is unknown.
type SessionClientResolver func(sessionID string) (name, version string)

// SessionWorkSessionResolver maps an MCP session id to the WORK session it
// belongs to: one client, one project, across reconnects.
//
// It returns the id cached on the connection — deliberately not a fresh
// derivation, so every record from one connection agrees on its work session.
type SessionWorkSessionResolver func(sessionID string) string

// ActivityService subscribes to activity events and persists them to storage.
// It runs as a background goroutine and handles activity recording non-blocking.
type ActivityService struct {
	storage *storage.Manager
	logger  *zap.Logger

	// clientResolver stamps the MCP client onto each activity record at WRITE
	// time. Read-time joining against the sessions API is not viable: the core
	// retains only the 100 most recent sessions, while activity is retained for
	// 90 days — and an IDE that reconnects every few minutes burns through 100
	// sessions in about a day. Any name resolved by lookup therefore decays back
	// to a bare session id. Denormalizing it here makes it permanent, and it
	// costs nothing: the resolver reads the in-memory session store (O(1)) and
	// the session is by definition still open when its activity is emitted.
	//
	// nil until wired via SetSessionClientResolver; the records simply carry no
	// client name in that case.
	clientResolver SessionClientResolver

	// workSessionResolver returns the WORK session a record belongs to:
	// one client, one project, across reconnects. Stamped at write time for the
	// same reason the client name is — a value resolved later decays once the
	// record it points at is evicted.
	workSessionResolver SessionWorkSessionResolver

	// workSessionReaper drops idle work sessions so the tracker cannot grow
	// without bound. Wired alongside the resolver.
	workSessionReaper func(time.Duration) int

	// Channel for receiving events
	eventCh chan Event

	// Shutdown coordination: Runtime.Close must be able to
	// await every encrypted writer this service owns BEFORE the clean-shutdown
	// marker resolves and the DB closes. done signals the main event loop's
	// exit (the final flush-on-shutdown included); workersWG tracks the
	// background loops (retention, usage flush) and the per-event async
	// detection goroutines, all of which write to encrypted. startMu/started make
	// Stop return immediately when Start never ran (done would never close).
	// stopped is the terminal state: lifecycle.go launches Start via `go`,
	// so a fast shutdown can run Stop BEFORE the Start goroutine is scheduled — Stop marks stopped under
	// startMu and a later Start becomes a no-op instead of launching encrypted
	// writers after the shutdown-marker path began. Start's registration
	// (subscribe + every workersWG.Add) happens entirely under startMu, so a
	// Stop that loses the race blocks until registration is complete and its
	// Wait cannot miss a late worker.
	done      chan struct{}
	workersWG sync.WaitGroup
	startMu   sync.Mutex
	started   bool
	stopped   bool

	// Retention configuration
	maxAge        time.Duration
	maxRecords    int
	maxSizeBytes  int64 // total activity-log size cap in bytes (0 = disabled)
	checkInterval time.Duration

	// maxResponseSize bounds the response text persisted on a single activity
	// record (activity_max_response_size). Applied to every record type on the
	// write path, so one huge upstream payload cannot outweigh the whole log.
	maxResponseSize int

	// Usage aggregate: actor-owned rollup of tool-call activity.
	// Mutated only on this goroutine via Apply; published to readers as an
	// immutable snapshot. The flush cadence is fixed at startup.
	usage              *UsageStore
	usageFlushInterval time.Duration
}

// NewActivityService creates a new activity service.
func NewActivityService(storage *storage.Manager, logger *zap.Logger) *ActivityService {
	s := &ActivityService{
		storage:       storage,
		logger:        logger,
		eventCh:       make(chan Event, 100), // Buffer for non-blocking event delivery
		done:          make(chan struct{}),
		maxAge:        DefaultRetentionMaxAge,
		maxRecords:    DefaultRetentionMaxRecords,
		maxSizeBytes:  DefaultRetentionMaxSizeBytes,
		checkInterval: DefaultRetentionCheckInterval,

		maxResponseSize: DefaultActivityMaxResponseSize,
		usage:           newUsageStore(),
	}
	s.usageFlushInterval = DefaultUsagePersistInterval
	return s
}

// SetSessionClientResolver wires the session -> MCP client lookup. Safe to leave
// unset (records then carry no client name).
func (s *ActivityService) SetSessionClientResolver(r SessionClientResolver) {
	s.clientResolver = r
}

// SetWorkSessionResolver wires the session -> work-session lookup.
func (s *ActivityService) SetWorkSessionResolver(r SessionWorkSessionResolver) {
	s.workSessionResolver = r
}

// SetWorkSessionReaper wires the idle-work-session sweep into the retention loop.
func (s *ActivityService) SetWorkSessionReaper(f func(time.Duration) int) {
	s.workSessionReaper = f
}

// resolveWorkSession returns the work session a record belongs to, or "" when it
// cannot be attributed (an unattributed record beats one filed under a bucket
// that means nothing).
func (s *ActivityService) resolveWorkSession(sessionID string) string {
	if sessionID == "" || s.workSessionResolver == nil {
		return ""
	}
	return s.workSessionResolver(sessionID)
}

// withClientInfo stamps client_name / client_version onto an activity record's
// metadata, so the Activity Log can name the client that made the call long
// after the session record itself has been evicted.
//
// Returns the metadata map to assign; it allocates one only when there is
// something to add, so records for sessionless events stay exactly as they were.
func (s *ActivityService) withClientInfo(metadata map[string]interface{}, sessionID string) map[string]interface{} {
	if sessionID == "" || s.clientResolver == nil {
		return metadata
	}
	name, version := s.clientResolver(sessionID)
	if name == "" {
		return metadata // unknown session (e.g. already closed) — nothing to add
	}

	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["client_name"] = name
	if version != "" {
		metadata["client_version"] = version
	}
	return metadata
}

// SetRetentionConfig updates the retention configuration.
// maxAge: maximum age for records (0 = no age limit)
// maxRecords: maximum number of records (0 = no count limit)
// checkInterval: how often to run retention cleanup
func (s *ActivityService) SetRetentionConfig(maxAge time.Duration, maxRecords int, checkInterval time.Duration, maxSizeBytes int64) {
	if maxAge > 0 {
		s.maxAge = maxAge
	}
	if maxRecords > 0 {
		s.maxRecords = maxRecords
	}
	if checkInterval > 0 {
		s.checkInterval = checkInterval
	}
	// maxSizeBytes may be explicitly set to 0 to DISABLE the size cap, so a
	// negative sentinel (-1) means "leave unchanged"; >= 0 is applied verbatim.
	if maxSizeBytes >= 0 {
		s.maxSizeBytes = maxSizeBytes
	}
}

// SetMaxResponseSize sets the per-record response size cap in bytes.
// Values <= 0 are ignored, leaving the current cap in place; truncateForStorage
// falls back to storage.DefaultMaxResponseSize if it is ever unset.
func (s *ActivityService) SetMaxResponseSize(maxResponseSize int) {
	if maxResponseSize > 0 {
		s.maxResponseSize = maxResponseSize
	}
}

// truncateForStorage caps the response text persisted on an activity record.
//
// This is the only thing bounding a single record's size: retention prunes the
// log by age, count and total bytes, but nothing else limits one record.
//
// The returned bool is OR-ed into ActivityRecord.ResponseTruncated for tool_call
// and internal tool calls, where the field means "cut on the way into the log, ResponseBytes
// still honest" — the same direction as a storage cut.
//
// It is deliberately NOT applied to internal_tool_call. There the flag carries the
// meaning "the agent received LESS than ResponseBytes", which two cost
// consumers read as "exclude this row from delivered traffic"
// (truncatedBuiltinOverstatesDelivery and bench/replaycorpus). A built-in cut only
// for storage was still delivered whole, so setting the flag would silently erase
// it from the usage timeline. The "...[truncated]" suffix still marks the text.
func (s *ActivityService) truncateForStorage(response string) (string, bool) {
	return storage.TruncateActivityResponse(response, s.maxResponseSize)
}

// Start begins listening for activity events and persisting them.
// It should be called as a goroutine: go svc.Start(ctx, runtime)
func (s *ActivityService) Start(ctx context.Context, rt *Runtime) {
	// Registration runs entirely under startMu. If Stop already ran (fast
	// shutdown beat this goroutine, which is launched via `go`), the service is terminally stopped: return
	// without subscribing or launching any encrypted-writing worker. Otherwise
	// mark started so Stop knows the done channel WILL close, and refuse a
	// second Start (the done/WaitGroup bookkeeping is single-shot). Holding
	// startMu through every workersWG.Add below means a concurrent Stop
	// blocks until registration is complete — its Wait cannot miss a worker.
	s.startMu.Lock()
	if s.stopped {
		s.startMu.Unlock()
		s.logger.Debug("Activity service Start called after Stop; not starting")
		return
	}
	if s.started {
		s.startMu.Unlock()
		s.logger.Warn("Activity service Start called twice; ignoring")
		return
	}
	// Subscribe to runtime events
	eventCh := rt.SubscribeEvents()
	s.started = true

	// Start retention loop in a separate goroutine. Tracked in workersWG: it
	// prunes activity records (encrypted writes), so Stop must await it.
	s.workersWG.Add(1)
	go func() {
		defer s.workersWG.Done()
		s.runRetentionLoop(ctx)
	}()

	// Load/rebuild the usage aggregate before processing events,
	// then start the periodic snapshot flush loop (tracked: it writes encrypted).
	s.initUsageFromStorage()
	s.workersWG.Add(1)
	go func() {
		defer s.workersWG.Done()
		s.runUsageFlushLoop(ctx)
	}()
	s.startMu.Unlock()

	defer rt.UnsubscribeEvents(eventCh)

	s.logger.Info("Activity service started")

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Activity service shutting down")
			// Flush-on-shutdown: persist the final usage snapshot.
			s.persistUsage()
			close(s.done)
			return
		case evt, ok := <-eventCh:
			if !ok {
				s.logger.Info("Activity service event channel closed")
				s.persistUsage()
				close(s.done)
				return
			}
			s.handleEvent(evt)
		}
	}
}

// runRetentionLoop periodically cleans up old activity records.
func (s *ActivityService) runRetentionLoop(ctx context.Context) {
	// Run initial cleanup
	s.runRetentionCleanup()

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Debug("Activity retention loop stopping")
			return
		case <-ticker.C:
			s.runRetentionCleanup()
			s.reapWorkSessions()
		}
	}
}

// workSessionReapAfter is how long a work session may sit idle before the
// tracker forgets it. Comfortably past the 30-minute idle window, so an entry is
// only dropped once it can no longer be continued.
const workSessionReapAfter = 4 * time.Hour

// reapWorkSessions stops the tracker growing without bound on a long-lived
// daemon: one map entry per distinct identity, forever, is a slow leak.
func (s *ActivityService) reapWorkSessions() {
	if s.workSessionReaper == nil {
		return
	}
	if n := s.workSessionReaper(workSessionReapAfter); n > 0 {
		s.logger.Debug("reaped idle work sessions", zap.Int("count", n))
	}
}

// runRetentionCleanup performs the actual retention cleanup.
func (s *ActivityService) runRetentionCleanup() {
	s.logger.Debug("Running activity retention cleanup",
		zap.Duration("max_age", s.maxAge),
		zap.Int("max_records", s.maxRecords))

	// Prune by age
	if s.maxAge > 0 {
		deleted, err := s.storage.PruneOldActivities(s.maxAge)
		if err != nil {
			s.logger.Error("Failed to prune old activities", zap.Error(err))
		} else if deleted > 0 {
			s.logger.Info("Pruned old activity records",
				zap.Int("deleted", deleted),
				zap.Duration("max_age", s.maxAge))
		}
	}

	// Prune by count
	if s.maxRecords > 0 {
		deleted, err := s.storage.PruneExcessActivities(s.maxRecords, 0.9)
		if err != nil {
			s.logger.Error("Failed to prune excess activities", zap.Error(err))
		} else if deleted > 0 {
			s.logger.Info("Pruned excess activity records",
				zap.Int("deleted", deleted),
				zap.Int("max_records", s.maxRecords))
		}
	}

	// Prune by total size (runs after age+count; 0 disables). Bounds primary.db
	// growth from large per-record payloads that stay under the count/age caps.
	if s.maxSizeBytes > 0 {
		deleted, err := s.storage.PruneActivitiesToSize(s.maxSizeBytes)
		if err != nil {
			s.logger.Error("Failed to prune activities to size budget", zap.Error(err))
		} else if deleted > 0 {
			s.logger.Info("Pruned activity records to size budget",
				zap.Int("deleted", deleted),
				zap.Int64("max_size_mb", s.maxSizeBytes/(1024*1024)))
		}
	}
}

// Stop gracefully shuts down the activity service, waiting for every encrypted
// writer it owns to finish: the main event loop (including its final
// flush-on-shutdown of the usage snapshot), the retention and usage-flush
// loops, and any in-flight async detection goroutines (no activity write may
// land after Runtime.Close resolves the clean-shutdown
// marker or closes the DB).
//
// Callers must cancel the context passed to Start FIRST — the final usage
// flush runs on ctx.Done inside the event loop, and Stop waits for it, so the
// flush is captured before the marker resolves. Idempotent, and returns
// immediately when Start never ran.
//
// Stop is also terminal: it marks stopped under startMu, so a Start that has
// not yet registered (lifecycle.go starts the service via `go`) becomes a no-op instead of launching
// retention/usage/persist loops after the shutdown-marker path began. If
// Start is mid-registration, acquiring startMu here blocks until every
// workersWG.Add has happened, so the Wait below cannot miss a worker.
func (s *ActivityService) Stop() {
	s.startMu.Lock()
	s.stopped = true
	started := s.started
	s.startMu.Unlock()

	// Main loop exit (closes done AFTER the shutdown flush). All workersWG.Add
	// calls happen before done closes — the loop goroutines are registered at
	// the top of Start and detection goroutines are only spawned from the event
	// loop — so Wait below cannot race an Add.
	if started {
		<-s.done
	}

	s.workersWG.Wait()
}

// handleEvent processes an activity event and persists it to storage.
func (s *ActivityService) handleEvent(evt Event) {
	switch evt.Type {
	case EventTypeActivityToolCallCompleted:
		s.handleToolCallCompleted(evt)
	case EventTypeActivityPolicyDecision:
		s.handlePolicyDecision(evt)

	case EventTypeActivityToolCallStarted:
		// Started events are logged but not persisted - we wait for completion
		s.logger.Debug("Activity tool call started",
			zap.String("server_name", getStringPayload(evt.Payload, "server_name")),
			zap.String("tool_name", getStringPayload(evt.Payload, "tool_name")),
			zap.String("session_id", getStringPayload(evt.Payload, "session_id")),
			zap.String("request_id", getStringPayload(evt.Payload, "request_id")))
	// System lifecycle events
	case EventTypeActivitySystemStart:
		s.handleSystemStart(evt)
	case EventTypeActivitySystemStop:
		s.handleSystemStop(evt)
	case EventTypeActivityInternalToolCall:
		s.handleInternalToolCall(evt)
	case EventTypeActivityConfigChange:
		s.handleConfigChange(evt)
	// Tool-level quarantine events
	case EventTypeActivityToolQuarantineChange:
		s.handleToolQuarantineChange(evt)
	default:
		// Ignore other event types
	}
}

// enterWrite admits a encrypted write that runs on a caller's goroutine rather than
// on one of this service's own loops. It reports false once shutdown has begun.
//
// The stopped check and the workersWG.Add MUST happen under the same lock Stop
// takes, and in that order: Stop marks stopped under startMu and only then
// waits on workersWG, so a writer that got in first is registered before the
// wait starts, and one that arrives later sees stopped and never touches the
// DB. Checking and registering separately would leave exactly the window where
// Stop returns — and the DB closes — with a write in flight.
//
// Callers must defer workersWG.Done() when this returns true.
func (s *ActivityService) enterWrite() bool {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.stopped {
		return false
	}
	s.workersWG.Add(1)
	return true
}

// handleToolCallCompleted persists a tool call completion event.
func (s *ActivityService) handleToolCallCompleted(evt Event) {
	serverName := getStringPayload(evt.Payload, "server_name")
	toolName := getStringPayload(evt.Payload, "tool_name")
	sessionID := getStringPayload(evt.Payload, "session_id")
	requestID := getStringPayload(evt.Payload, "request_id")
	source := getStringPayload(evt.Payload, "source")
	status := getStringPayload(evt.Payload, "status")
	errorMsg := getStringPayload(evt.Payload, "error_message")
	arguments := getMapPayload(evt.Payload, "arguments")
	response := getStringPayload(evt.Payload, "response")
	responseTruncated := getBoolPayload(evt.Payload, "response_truncated")
	response, storageTruncated := s.truncateForStorage(response)
	responseTruncated = responseTruncated || storageTruncated
	durationMs := getInt64Payload(evt.Payload, "duration_ms")

	// Extract intent metadata if present
	toolVariant := getStringPayload(evt.Payload, "tool_variant")
	intent := getMapPayload(evt.Payload, "intent")
	// Extract content trust metadata if present
	contentTrust := getStringPayload(evt.Payload, "content_trust")
	profileSlug := getStringPayload(evt.Payload, "profile")

	// Correlation id of the parent code_execution call (empty for a top-level
	// dispatch). First-class on the record so ?parent_id= can filter on it.
	parentID := getStringPayload(evt.Payload, "parent_id")
	// Typed cause of a profile-refused code_execution sub-call; empty for
	// every other completion.
	blockReason := getStringPayload(evt.Payload, storage.MetadataKeyBlockReason)
	activitySource := storage.ActivitySource(source)

	// Build metadata with intent information if present
	var metadata map[string]interface{}
	if toolVariant != "" || intent != nil || contentTrust != "" || profileSlug != "" {
		metadata = make(map[string]interface{})
		if toolVariant != "" {
			metadata["tool_variant"] = toolVariant
		}
		if intent != nil {
			metadata["intent"] = intent
		}
		// Tag activity with content trust level based on openWorldHint
		if contentTrust != "" {
			metadata["content_trust"] = contentTrust
		}
		// Top-level profile slug (NOT nested under intent), so
		// operators can correlate activity to the profile it came from.
		if profileSlug != "" {
			metadata["profile"] = profileSlug
		}
	}
	// Name the MCP client on the record itself, so it survives session eviction.
	metadata = s.withClientInfo(metadata, sessionID)

	// Byte sizes measured pre-truncation by the emitter.
	requestBytes := int(getInt64Payload(evt.Payload, "request_bytes"))
	responseBytes := int(getInt64Payload(evt.Payload, "response_bytes"))

	record := &storage.ActivityRecord{
		Type:              storage.ActivityTypeToolCall,
		Source:            activitySource,
		ServerName:        serverName,
		ToolName:          toolName,
		Arguments:         arguments,
		Response:          response,
		ResponseTruncated: responseTruncated,
		Status:            status,
		ErrorMessage:      errorMsg,
		DurationMs:        durationMs,
		Timestamp:         evt.Timestamp,
		SessionID:         sessionID,
		WorkSessionID:     s.resolveWorkSession(sessionID),
		RequestID:         requestID,
		ParentID:          parentID,
		BlockReason:       blockReason,
		Metadata:          metadata,
		RequestBytes:      requestBytes,
		ResponseBytes:     responseBytes,
	}
	// The profile/client/token in effect when the call ran.
	applyAttribution(record, attributionFromPayload(evt.Payload))

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save activity record",
			zap.Error(err),
			zap.String("server_name", serverName),
			zap.String("tool_name", toolName))
	} else {
		s.logger.Debug("Activity record saved",
			zap.String("id", record.ID),
			zap.String("server_name", serverName),
			zap.String("tool_name", toolName),
			zap.String("status", status))

		// Fold the persisted call into the usage aggregate. Done
		// only on save success so the in-memory rollup stays consistent with a
		// cold-start rebuild that re-scans persisted records.
		if s.usage != nil {
			s.usage.Apply(record)
		}

	}
}

// handlePolicyDecision persists a policy decision event.
func (s *ActivityService) handlePolicyDecision(evt Event) {
	serverName := getStringPayload(evt.Payload, "server_name")
	toolName := getStringPayload(evt.Payload, "tool_name")
	sessionID := getStringPayload(evt.Payload, "session_id")
	decision := getStringPayload(evt.Payload, "decision")
	reason := getStringPayload(evt.Payload, "reason")

	metadata := map[string]interface{}{
		"decision": decision,
		"reason":   reason,
	}
	blockReason := getStringPayload(evt.Payload, storage.MetadataKeyBlockReason)
	record := &storage.ActivityRecord{
		Type:          storage.ActivityTypePolicyDecision,
		ServerName:    serverName,
		ToolName:      toolName,
		Status:        decision,
		Metadata:      s.withClientInfo(metadata, sessionID),
		Timestamp:     evt.Timestamp,
		SessionID:     sessionID,
		RequestID:     getStringPayload(evt.Payload, "request_id"),
		WorkSessionID: s.resolveWorkSession(sessionID),
		BlockReason:   blockReason,
	}
	applyAttribution(record, attributionFromPayload(evt.Payload))

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save policy decision activity",
			zap.Error(err),
			zap.String("server_name", serverName),
			zap.String("decision", decision))
		return
	}

	// Fold blocked attempts into the usage aggregate. Apply
	// ignores non-blocked decisions, so passing every policy decision is safe.
	// Done only on save success so the in-memory rollup stays consistent with a
	// cold-start rebuild that re-scans persisted records.
	if s.usage != nil {
		s.usage.Apply(record)
	}
}

// handleSystemStart persists a system start event.
func (s *ActivityService) handleSystemStart(evt Event) {
	version := getStringPayload(evt.Payload, "version")
	listenAddress := getStringPayload(evt.Payload, "listen_address")
	startupDurationMs := getInt64Payload(evt.Payload, "startup_duration_ms")

	record := &storage.ActivityRecord{
		Type:   storage.ActivityTypeSystemStart,
		Source: storage.ActivitySourceAPI, // System events come from the API server
		Status: "success",
		Metadata: map[string]interface{}{
			"version":             version,
			"listen_address":      listenAddress,
			"startup_duration_ms": startupDurationMs,
		},
		Timestamp: evt.Timestamp,
	}

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save system start activity",
			zap.Error(err),
			zap.String("version", version))
	} else {
		s.logger.Info("System start activity recorded",
			zap.String("id", record.ID),
			zap.String("version", version),
			zap.Int64("startup_duration_ms", startupDurationMs))
	}
}

// handleSystemStop persists a system stop event.
func (s *ActivityService) handleSystemStop(evt Event) {
	reason := getStringPayload(evt.Payload, "reason")
	signal := getStringPayload(evt.Payload, "signal")
	uptimeSeconds := getInt64Payload(evt.Payload, "uptime_seconds")
	errorMsg := getStringPayload(evt.Payload, "error_message")

	status := "success"
	if errorMsg != "" {
		status = "error"
	}

	record := &storage.ActivityRecord{
		Type:         storage.ActivityTypeSystemStop,
		Source:       storage.ActivitySourceAPI,
		Status:       status,
		ErrorMessage: errorMsg,
		Metadata: map[string]interface{}{
			"reason":         reason,
			"signal":         signal,
			"uptime_seconds": uptimeSeconds,
		},
		Timestamp: evt.Timestamp,
	}

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save system stop activity",
			zap.Error(err),
			zap.String("reason", reason))
	} else {
		s.logger.Info("System stop activity recorded",
			zap.String("id", record.ID),
			zap.String("reason", reason),
			zap.Int64("uptime_seconds", uptimeSeconds))
	}
}

// handleInternalToolCall persists an internal tool call event.
func (s *ActivityService) handleInternalToolCall(evt Event) {
	internalToolName := getStringPayload(evt.Payload, "internal_tool_name")
	targetServer := getStringPayload(evt.Payload, "target_server")
	targetTool := getStringPayload(evt.Payload, "target_tool")
	toolVariant := getStringPayload(evt.Payload, "tool_variant")
	sessionID := getStringPayload(evt.Payload, "session_id")
	requestID := getStringPayload(evt.Payload, "request_id")
	status := getStringPayload(evt.Payload, "status")
	errorMsg := getStringPayload(evt.Payload, "error_message")
	durationMs := getInt64Payload(evt.Payload, "duration_ms")
	intent := getMapPayload(evt.Payload, "intent")
	arguments := getMapPayload(evt.Payload, "arguments")

	// Extract response - can be various types, convert to string
	var responseStr string
	if resp := evt.Payload["response"]; resp != nil {
		switch r := resp.(type) {
		case string:
			responseStr = r
		default:
			// Convert to JSON for other types
			if jsonBytes, err := json.Marshal(r); err == nil {
				responseStr = string(jsonBytes)
			}
		}
	}

	// Extract content trust metadata if present
	contentTrust := getStringPayload(evt.Payload, "content_trust")

	responseTruncated := getBoolPayload(evt.Payload, "response_truncated")
	// Truncate the stored text but deliberately do NOT touch responseTruncated.
	// On an internal record that flag has one specific meaning for cost:
	// "the agent received LESS than ResponseBytes, so exclude the row"
	// (truncatedBuiltinOverstatesDelivery, and bench/replaycorpus). A built-in
	// that was cut only on the way into the log was still delivered whole with
	// an honest ResponseBytes, so setting the flag here would drop it from
	// delivered traffic. The "...[truncated]" suffix still marks the stored text.
	responseStr, _ = s.truncateForStorage(responseStr)

	// Pre-truncation sizes, emitted for built-ins as well as upstream
	// dispatches. 0 means unknown rather than free.
	internalRequestBytes := int(getInt64Payload(evt.Payload, "request_bytes"))
	internalResponseBytes := int(getInt64Payload(evt.Payload, "response_bytes"))

	metadata := map[string]interface{}{
		"internal_tool_name": internalToolName,
	}
	if targetServer != "" {
		metadata["target_server"] = targetServer
	}
	if targetTool != "" {
		metadata["target_tool"] = targetTool
	}
	if toolVariant != "" {
		metadata["tool_variant"] = toolVariant
	}
	if intent != nil {
		metadata["intent"] = intent
	}
	// Tag activity with content trust level based on openWorldHint
	if contentTrust != "" {
		metadata["content_trust"] = contentTrust
	}
	// Name the MCP client on the record itself, so it survives session eviction.
	// retrieve_tools calls arrive here, and they are the bulk of session-bearing
	// activity — without this they would be the rows left showing a bare id.
	metadata = s.withClientInfo(metadata, sessionID)

	record := &storage.ActivityRecord{
		Type:              storage.ActivityTypeInternalToolCall,
		Source:            storage.ActivitySourceMCP,
		ToolName:          internalToolName,
		ServerName:        targetServer,
		Arguments:         arguments,
		Response:          responseStr,
		Status:            status,
		ErrorMessage:      errorMsg,
		DurationMs:        durationMs,
		Metadata:          metadata,
		Timestamp:         evt.Timestamp,
		SessionID:         sessionID,
		WorkSessionID:     s.resolveWorkSession(sessionID),
		RequestID:         requestID,
		ResponseTruncated: responseTruncated,
		RequestBytes:      internalRequestBytes,
		ResponseBytes:     internalResponseBytes,
	}
	applyAttribution(record, attributionFromPayload(evt.Payload))

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save internal tool call activity",
			zap.Error(err),
			zap.String("internal_tool_name", internalToolName))
	} else {
		s.logger.Debug("Internal tool call activity recorded",
			zap.String("id", record.ID),
			zap.String("internal_tool_name", internalToolName),
			zap.String("status", status))

		// Fold into the usage aggregate so the timeline matches the glance and
		// a cold-start rebuild (which re-scans persisted records through the
		// same Apply). Apply itself decides what is admissible: call_tool_* records
		// are dropped there, because the direct dispatch already emitted a
		// paired tool_call.
		if s.usage != nil {
			s.usage.Apply(record)
		}
	}
}

// handleConfigChange persists a config change event.
func (s *ActivityService) handleConfigChange(evt Event) {
	action := getStringPayload(evt.Payload, "action")
	affectedEntity := getStringPayload(evt.Payload, "affected_entity")
	source := getStringPayload(evt.Payload, "source")

	var activitySource storage.ActivitySource
	switch source {
	case "mcp":
		activitySource = storage.ActivitySourceMCP
	default:
		activitySource = storage.ActivitySourceAPI
	}

	metadata := map[string]interface{}{
		"action":          action,
		"affected_entity": affectedEntity,
	}
	if changedFields := getSlicePayload(evt.Payload, "changed_fields"); len(changedFields) > 0 {
		metadata["changed_fields"] = changedFields
	}
	if prevValues := getMapPayload(evt.Payload, "previous_values"); prevValues != nil {
		metadata["previous_values"] = prevValues
	}
	if newValues := getMapPayload(evt.Payload, "new_values"); newValues != nil {
		metadata["new_values"] = newValues
	}
	// A removal records what its purge deleted.
	if purged := getMapPayload(evt.Payload, "purged"); purged != nil {
		metadata["purged"] = purged
	}

	record := &storage.ActivityRecord{
		Type:       storage.ActivityTypeConfigChange,
		Source:     activitySource,
		ServerName: affectedEntity,
		Status:     "success",
		Metadata:   metadata,
		Timestamp:  evt.Timestamp,
	}

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save config change activity",
			zap.Error(err),
			zap.String("action", action),
			zap.String("affected_entity", affectedEntity))
	} else {
		s.logger.Info("Config change activity recorded",
			zap.String("id", record.ID),
			zap.String("action", action),
			zap.String("affected_entity", affectedEntity))
	}
}

// Helper functions to extract payload values safely

func getStringPayload(payload map[string]any, key string) string {
	if v, ok := payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getBoolPayload(payload map[string]any, key string) bool {
	if v, ok := payload[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func getInt64Payload(payload map[string]any, key string) int64 {
	if v, ok := payload[key]; ok {
		switch n := v.(type) {
		case int64:
			return n
		case int:
			return int64(n)
		case float64:
			return int64(n)
		}
	}
	return 0
}

func getMapPayload(payload map[string]any, key string) map[string]interface{} {
	if v, ok := payload[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
		// Also handle map[string]any which is an alias
		if m, ok := v.(map[string]any); ok {
			return m
		}
	}
	return nil
}

func getSlicePayload(payload map[string]any, key string) []string {
	if v, ok := payload[key]; ok {
		if s, ok := v.([]string); ok {
			return s
		}
		// Also handle []interface{} and convert to []string
		if arr, ok := v.([]interface{}); ok {
			result := make([]string, 0, len(arr))
			for _, item := range arr {
				if str, ok := item.(string); ok {
					result = append(result, str)
				}
			}
			return result
		}
		if arr, ok := v.([]any); ok {
			result := make([]string, 0, len(arr))
			for _, item := range arr {
				if str, ok := item.(string); ok {
					result = append(result, str)
				}
			}
			return result
		}
	}
	return nil
}

// handleToolQuarantineChange persists a tool quarantine state change event.
func (s *ActivityService) handleToolQuarantineChange(evt Event) {
	serverName := getStringPayload(evt.Payload, "server_name")
	toolName := getStringPayload(evt.Payload, "tool_name")
	action := getStringPayload(evt.Payload, "action")
	metadataStr := getStringPayload(evt.Payload, "metadata")

	// Parse metadata from JSON string
	var metadata map[string]interface{}
	if metadataStr != "" {
		if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
			s.logger.Debug("Failed to parse tool quarantine metadata",
				zap.Error(err))
			metadata = map[string]interface{}{
				"action":    action,
				"tool_name": toolName,
			}
		}
	} else {
		metadata = map[string]interface{}{
			"action":    action,
			"tool_name": toolName,
		}
	}

	record := &storage.ActivityRecord{
		Type:       storage.ActivityTypeToolQuarantineChange,
		ServerName: serverName,
		ToolName:   toolName,
		Status:     action,
		Metadata:   metadata,
		Timestamp:  evt.Timestamp,
	}

	if err := s.storage.SaveActivity(record); err != nil {
		s.logger.Error("Failed to save tool quarantine activity",
			zap.Error(err),
			zap.String("server_name", serverName),
			zap.String("tool_name", toolName),
			zap.String("action", action))
	}
}
