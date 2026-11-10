package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// parseActivityFilters extracts activity filter parameters from the request query string.
func parseActivityFilters(r *http.Request) storage.ActivityFilter {
	filter := storage.DefaultActivityFilter()
	q := r.URL.Query()

	// Type filter (comma-separated multiple types)
	if typeStr := q.Get("type"); typeStr != "" {
		filter.Types = strings.Split(typeStr, ",")
	}

	// Server filter
	if server := q.Get("server"); server != "" {
		filter.Server = server
	}

	// Tool filter
	if tool := q.Get("tool"); tool != "" {
		filter.Tool = tool
	}

	// Session filters: work_session_id is a unit of user work (one client, one
	// project, across reconnects) and is what the UI's Session filter sends;
	// session_id is the raw transport session.
	if ws := q.Get("work_session_id"); ws != "" {
		filter.WorkSessionID = ws
	}
	if sessionID := q.Get("session_id"); sessionID != "" {
		filter.SessionID = sessionID
	}

	// Status filter
	if status := q.Get("status"); status != "" {
		filter.Status = status
	}

	// Time range filters
	if startTimeStr := q.Get("start_time"); startTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, startTimeStr); err == nil {
			filter.StartTime = t
		}
	}

	if endTimeStr := q.Get("end_time"); endTimeStr != "" {
		if t, err := time.Parse(time.RFC3339, endTimeStr); err == nil {
			filter.EndTime = t
		}
	}

	// Pagination
	if limitStr := q.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = limit
		}
	}

	if offsetStr := q.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil {
			filter.Offset = offset
		}
	}

	// Request ID filter
	if requestID := q.Get("request_id"); requestID != "" {
		filter.RequestID = requestID
	}

	// Parent ID filter: the sub-calls one code_execution issued. Navigation is
	// symmetric — parent→children is ?parent_id=<parent request_id>, and
	// child→parent is ?request_id=<child parent_id>.
	if parentID := q.Get("parent_id"); parentID != "" {
		filter.ParentID = parentID
	}

	// Scope attribution filters.
	filter.TokenName = q.Get("token")
	filter.Profile = q.Get("profile")
	filter.ClientID = q.Get("client")

	filter.Validate()
	return filter
}

func (s *Server) handleListActivity(w http.ResponseWriter, r *http.Request) {
	// /activity honours profile, client and token.
	if !rejectUnsupportedScopeFilters(w, r, "profile", "client", "token") {
		return
	}
	filter := parseActivityFilters(r)

	activities, total, err := s.controller.ListActivities(filter)
	if err != nil {
		s.logger.Errorw("Failed to list activities", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "Failed to list activities")
		return
	}

	excludePayloads := r.URL.Query().Get("exclude_payloads") == "true"
	contractActivities := make([]contracts.ActivityRecord, len(activities))
	for i, a := range activities {
		contractActivities[i] = storageToContractActivity(a)
		activityView(&contractActivities[i])
		if excludePayloads {
			contractActivities[i].Arguments = nil
			contractActivities[i].Response = ""
			contractActivities[i].ResponseTruncated = false
			contractActivities[i].Metadata = projectContextualMetadata(contractActivities[i].Metadata)
		}
	}

	response := contracts.ActivityListResponse{
		Activities: contractActivities,
		Total:      total,
		Limit:      filter.Limit,
		Offset:     filter.Offset,
	}

	s.writeSuccess(w, response)
}

func (s *Server) handleGetActivityDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, "Activity ID is required")
		return
	}

	activity, err := s.controller.GetActivity(id)
	if err != nil {
		s.logger.Errorw("Failed to get activity", "id", id, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "Failed to get activity")
		return
	}

	if activity == nil {
		s.writeError(w, r, http.StatusNotFound, "Activity not found")
		return
	}

	record := storageToContractActivity(activity)
	activityView(&record)

	s.writeSuccess(w, record)
}

// activityView drops MCPProxy's internal `_auth_*` arguments from a record's
// arguments. They are identity plumbing the proxy injects (lifted into the
// typed auth_type/agent_name fields by storageToContractActivity), never
// something the caller sent. Values are otherwise returned as stored.
func activityView(record *contracts.ActivityRecord) {
	record.Arguments = stripInternalArgsLocal(record.Arguments)
}

var contextualMetadataKeys = []string{"decision", "reason", "client_name", "client_version"}

// contextualIntentKeys are the keys kept inside `metadata.intent`. The rest of
// the intent object (scores, raw classifier output) is not rendered anywhere.
var contextualIntentKeys = []string{"reason", "operation_type"}

// projectContextualMetadata narrows metadata to the contextual whitelist,
// returning nil when nothing whitelisted is present so an all-dropped record
// serialises as an absent object rather than an empty one.
//
// Only string values are kept. A whitelisted key is not a promise about its
// value, and nothing stops a producer from putting a structured error under
// `reason` — copying by key alone would carry that whole nested payload through
// the one boundary callers are told payloads cannot cross.
//
// The result is always a fresh map: the input belongs to the storage layer (the
// controller may hand back live records) and must not be edited in place.
func projectContextualMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}

	projected := make(map[string]interface{}, len(contextualMetadataKeys)+1)
	for _, key := range contextualMetadataKeys {
		if value, ok := metadata[key].(string); ok {
			projected[key] = value
		}
	}

	if intent, ok := metadata["intent"].(map[string]interface{}); ok {
		projectedIntent := make(map[string]interface{}, len(contextualIntentKeys))
		for _, key := range contextualIntentKeys {
			if value, ok := intent[key].(string); ok {
				projectedIntent[key] = value
			}
		}
		if len(projectedIntent) > 0 {
			projected["intent"] = projectedIntent
		}
	}

	if len(projected) == 0 {
		return nil
	}
	return projected
}

// storageToContractActivity converts a storage ActivityRecord to a contracts ActivityRecord.
func storageToContractActivity(a *storage.ActivityRecord) contracts.ActivityRecord {

	return contracts.ActivityRecord{
		ID:                a.ID,
		Type:              contracts.ActivityType(a.Type),
		Source:            contracts.ActivitySource(a.Source),
		ServerName:        a.ServerName,
		ToolName:          a.ToolName,
		Arguments:         a.Arguments,
		Response:          a.Response,
		ResponseTruncated: a.ResponseTruncated,
		Status:            a.Status,
		ErrorMessage:      a.ErrorMessage,
		DurationMs:        a.DurationMs,
		Timestamp:         a.Timestamp,
		SessionID:         a.SessionID,
		WorkSessionID:     a.WorkSessionID,
		RequestID:         a.RequestID,
		ParentID:          a.ParentID,
		Metadata:          a.Metadata,
		AuthType:          authArgString(a.Arguments, "_auth_auth_type"),
		AgentName:         authArgString(a.Arguments, "_auth_agent_name"),
		Profile:           a.EffectiveProfile(),
		ProfileSource:     a.ProfileSource,
		ClientID:          a.ClientID,
		ClientName:        a.ClientName,
		ClientHostID:      a.ClientHostID, ExecutionHostID: a.ExecutionHostID,
		TokenName:   a.TokenName,
		BlockReason: a.BlockReason,
		// Call-error taxonomy

		// Sensitive data detection fields

	}
}

// authArgString reads one internal `_auth_*` identity key from a
// record's stored arguments. activityView strips those keys from the
// payload view, so the identity has to be lifted into typed fields here or the
// Web UI has nothing to filter on.
func authArgString(args map[string]interface{}, key string) string {
	if s, ok := args[key].(string); ok {
		return s
	}
	return ""
}

// =============================================================================
// GET /api/v1/activity/usage
// =============================================================================

const (
	usageDefaultTop    = 20
	usageDefaultSort   = "resp_bytes"
	usageDefaultWindow = "24h"
)

// usageParams holds the validated query parameters for the usage endpoint.
type usageParams struct {
	window string // "24h" | "7d" | "all"
	server string
	tool   string
	status string // "" | "success" | "error" | "blocked" | "rejected"
	top    int
	sort   string // "calls" | "resp_bytes" | "error_rate" | "p95"

	// Scope filters. When any is set the response is computed
	// from a window-bounded scan of the matching activity records (the
	// persisted aggregate is keyed only by server and tool).
	profile string
	client  string
	token   string
}

// scopeFiltered reports whether a profile/client/token filter is in effect.
func (p usageParams) scopeFiltered() bool {
	return p.profile != "" || p.client != "" || p.token != ""
}

// cacheKey is a stable identity for the params, used by the short-TTL cache.
//
// The encoding is LENGTH-PREFIXED so it stays injective when a term contains
// a separator.
func (p usageParams) cacheKey() string {
	var b strings.Builder
	write := func(part string) {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	for _, part := range []string{p.window, p.server, p.tool, p.status, p.sort, strconv.Itoa(p.top), p.profile, p.client, p.token} {
		write(part)
	}
	return b.String()
}

// windowStart returns the lower time bound for the window relative to now, plus
// whether a bound applies (false for "all").
func (p usageParams) windowStart(now time.Time) (time.Time, bool) {
	switch p.window {
	case "24h":
		return now.Add(-24 * time.Hour), true
	case "7d":
		return now.Add(-7 * 24 * time.Hour), true
	default: // "all"
		return time.Time{}, false
	}
}

// parseUsageParams validates the usage query string, returning a 400-style error
// message for any bad enum / non-int top.
func parseUsageParams(r *http.Request) (usageParams, error) {
	q := r.URL.Query()
	p := usageParams{
		window: usageDefaultWindow,
		server: q.Get("server"),
		tool:   q.Get("tool"),
		status: q.Get("status"),
		top:    usageDefaultTop,
		sort:   usageDefaultSort,
	}
	p.profile = q.Get("profile")
	p.client = q.Get("client")
	p.token = q.Get("token")

	if v := q.Get("window"); v != "" {
		switch v {
		case "24h", "7d", "all":
			p.window = v
		default:
			return p, fmt.Errorf("invalid window %q (expected 24h, 7d, or all)", v)
		}
	}

	if v := q.Get("sort"); v != "" {
		switch v {
		case "calls", "resp_bytes", "error_rate", "p95":
			p.sort = v
		default:
			return p, fmt.Errorf("invalid sort %q (expected calls, resp_bytes, error_rate, or p95)", v)
		}
	}

	if p.status != "" {
		switch p.status {

		case "success", "error", "blocked":
		default:
			return p, fmt.Errorf("invalid status %q (expected success, error, or blocked)", p.status)
		}
	}

	if v := q.Get("top"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, fmt.Errorf("invalid top %q (expected a positive integer)", v)
		}
		p.top = n
	}

	return p, nil
}

func (s *Server) handleActivityUsage(w http.ResponseWriter, r *http.Request) {
	// Usage honours profile, client and token. client_name is advisory and
	// not offered here.
	if !rejectUnsupportedScopeFilters(w, r, "profile", "client", "token") {
		return
	}
	if !s.rejectClientNameParam(w, r) {
		return
	}
	params, err := parseUsageParams(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	ttl := s.usageCacheTTL()
	key := params.cacheKey()
	if cached := s.getUsageCache(key, ttl); cached != nil {
		s.writeSuccess(w, cached)
		return
	}

	var resp *contracts.UsageAggregateResponse
	if params.scopeFiltered() {
		resp = s.scopedUsageResponse(params, time.Now().UTC())
	} else {
		snap := s.controller.UsageSnapshot()

		resp = buildUsageResponse(snap, params, time.Now().UTC())
	}
	s.putUsageCache(key, resp, ttl)
	s.writeSuccess(w, resp)
}

func (s *Server) scopedUsageResponse(p usageParams, now time.Time) *contracts.UsageAggregateResponse {
	filter := storage.ActivityFilter{
		// Every record Apply may admit, including the call_tool_* mirrors it
		// drops itself: the population a cold-start rebuild scans.
		ExcludeCallToolSuccess: false,
		Profile:                p.profile,
		ClientID:               p.client,
		TokenName:              p.token,
	}
	if start, bounded := p.windowStart(now); bounded {
		filter.StartTime = start
	}
	agg := internalRuntime.NewUsageAggregate()
	// The stream holds a read transaction open until drained, so it always runs
	// to completion.
	for rec := range s.controller.StreamActivities(filter) {
		agg.Apply(rec)
	}
	agg.UpdatedAt = now
	return buildUsageResponse(agg, p, now)
}

// usageCacheTTL reads the configured read-cache freshness bound,
// falling back to the default when config is unavailable. Read per request so
// the value hot-reloads with config.
func (s *Server) usageCacheTTL() time.Duration {
	def := time.Duration(config.DefaultObservabilityConfig().UsageCacheTTL)
	cfg := s.controller.GetCurrentConfig()
	if cfg == nil || cfg.Observability == nil {
		return def
	}
	if d := time.Duration(cfg.Observability.UsageCacheTTL); d > 0 {
		return d
	}
	return def
}

// buildUsageResponse projects the usage snapshot into the API contract, applying
// window/filter/sort/top-N. It performs no I/O and never scans the activity
// log: the actor-owned snapshot is the incremental-precompute path.
func buildUsageResponse(snap *internalRuntime.UsageAggregate, p usageParams, now time.Time) *contracts.UsageAggregateResponse {
	resp := &contracts.UsageAggregateResponse{
		Window:      p.window,
		GeneratedAt: now,

		Tools:    make([]contracts.UsageToolStat, 0),
		Timeline: make([]contracts.UsageTimeBucket, 0),
	}

	if snap == nil {
		return resp
	}
	if !snap.UpdatedAt.IsZero() {
		if age := now.Sub(snap.UpdatedAt); age > 0 {
			resp.FreshnessMs = age.Milliseconds()
		}
	}

	start, bounded := p.windowStart(now)

	// Per-tool rollup: filter by membership, project to contract rows.
	rows := make([]contracts.UsageToolStat, 0, len(snap.Tools))
	for _, tu := range snap.Tools {
		if p.server != "" && tu.Server != p.server {
			continue
		}
		if p.tool != "" && tu.Tool != p.tool {
			continue
		}
		if !usageMatchesStatus(tu, p.status) {
			continue
		}
		if bounded && tu.LastUsed.Before(start) {
			continue // tool idle for the whole window
		}
		rows = append(rows, usageToolStat(tu))
	}

	sortUsageRows(rows, p.sort)

	// Top-N + 'other' fold.
	if len(rows) > p.top {
		other := &contracts.UsageOtherBucket{}
		for _, row := range rows[p.top:] {
			other.ToolsFolded++
			other.Calls += row.Calls
			other.TotalRespBytes += row.TotalRespBytes
		}
		resp.Other = other
		rows = rows[:p.top]
	}
	resp.Tools = rows

	// Timeline: global buckets trimmed to the window span. Its sum is also the
	// window's headline count — computed here, server-side, from the same bars
	// the response carries, so the tiles and the histogram beneath them agree
	// and so the Activity Log can print the same number.
	for _, b := range snap.Timeline() {
		if bounded && b.Start.Before(start) {
			continue
		}
		resp.Timeline = append(resp.Timeline, contracts.UsageTimeBucket{
			Start:          b.Start,
			Calls:          b.Calls,
			Errors:         b.Errors,
			TotalRespBytes: b.RespBytesSum,
		})
		resp.TotalCalls += b.Calls
		resp.TotalErrors += b.Errors
	}

	return resp
}

// usageMatchesStatus reports whether a tool has any activity of the requested
// status. Status filters operate as membership filters on the cumulative
// per-tool rollup (the aggregate does not retain per-status byte breakdowns).
func usageMatchesStatus(tu *internalRuntime.ToolUsage, status string) bool {
	switch status {
	case "":
		return true
	case "error":
		return tu.Errors > 0
	case "blocked":
		return tu.Blocked > 0
	case "success":
		return tu.Calls-tu.Errors > 0
	default:
		return true
	}
}

// usageToolStat projects a runtime ToolUsage into the API contract row.
func usageToolStat(tu *internalRuntime.ToolUsage) contracts.UsageToolStat {
	p50, p50Exceeds := tu.Percentile(0.50)
	p95, p95Exceeds := tu.Percentile(0.95)
	row := contracts.UsageToolStat{
		Server:    tu.Server,
		Tool:      tu.Tool,
		Calls:     tu.Calls,
		Errors:    tu.Errors,
		ErrorRate: tu.ErrorRate(),
		Blocked:   tu.Blocked,

		TotalRespBytes: tu.RespBytesSum,
		TotalReqBytes:  tu.ReqBytesSum,
		SizedCalls:     tu.SizedRespCalls,
		P50Ms:          p50,
		P50Exceeds:     p50Exceeds,
		P95Ms:          p95,
		P95Exceeds:     p95Exceeds,
		LastUsed:       tu.LastUsed,
	}
	if avg, ok := tu.AvgRespBytes(); ok {
		row.AvgRespBytes = &avg
	}
	if avg, ok := tu.AvgReqBytes(); ok {
		row.AvgReqBytes = &avg
	}
	return row
}

// sortUsageRows orders rows descending by the requested key, breaking ties by
// server:tool for a deterministic response.
func sortUsageRows(rows []contracts.UsageToolStat, key string) {
	less := func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch key {
		case "calls":
			if a.Calls != b.Calls {
				return a.Calls > b.Calls
			}
		case "error_rate":
			if a.ErrorRate != b.ErrorRate {
				return a.ErrorRate > b.ErrorRate
			}
		case "p95":
			if a.P95Ms != b.P95Ms {
				return a.P95Ms > b.P95Ms
			}
			// Both sit on the last histogram bound, but one of them is only
			// BOUNDED there and the other ran PAST it. "Sort by p95 latency"
			// exists to surface the slowest tools, and top-N truncation means
			// losing that tie-break can drop the genuinely slow one off the
			// chart in favour of a tool that merely touched the ceiling.
			if a.P95Exceeds != b.P95Exceeds {
				return a.P95Exceeds
			}
		default: // resp_bytes
			if a.TotalRespBytes != b.TotalRespBytes {
				return a.TotalRespBytes > b.TotalRespBytes
			}
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Tool < b.Tool
	}
	sort.Slice(rows, less)
}

// stripInternalArgsLocal removes internal `_auth_*` argument keys.
func stripInternalArgsLocal(args map[string]interface{}) map[string]interface{} {
	has := false
	for k := range args {
		if len(k) >= 6 && k[:6] == "_auth_" {
			has = true
			break
		}
	}
	if !has {
		return args
	}
	out := make(map[string]interface{}, len(args))
	for k, v := range args {
		if len(k) >= 6 && k[:6] == "_auth_" {
			continue
		}
		out[k] = v
	}
	return out
}
