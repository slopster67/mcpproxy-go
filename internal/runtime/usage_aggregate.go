package runtime

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// UsageAggregate is an in-memory rollup of tool-call activity, owned by the
// ActivityService goroutine and mutated incrementally via Apply. Readers never
// touch the live aggregate: the store publishes an immutable deep copy through
// an atomic pointer (copy-on-write), so reads are lock-free and never block.

// usageBucketWidth is the native time-bucket granularity for the timeline.
// The usage endpoint selects the requested window span over these buckets.
const usageBucketWidth = time.Hour

// usageMaxBuckets bounds timeline memory. 24*90 hourly buckets covers the
// default 90-day activity retention; older buckets are evicted oldest-first.
const usageMaxBuckets = 24 * 90

// latencyBucketBoundsMs are the inclusive upper bounds (in ms) of the fixed
// latency histogram buckets. A final overflow bucket captures anything slower
// than the last bound, so there are len(bounds)+1 buckets total.
//
// The low end is fine-grained on purpose. A percentile read off a histogram is
// the upper bound of the bucket it falls in, so the first bound is the floor of
// everything the table can print. Local MCP servers mostly answer in under
// 10ms, so the buckets need resolution there.
var latencyBucketBoundsMs = []int64{1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

func numLatencyBuckets() int { return len(latencyBucketBoundsMs) + 1 }

// latencyBucketIndex returns the histogram bucket for a duration in ms.
func latencyBucketIndex(durationMs int64) int {
	for i, bound := range latencyBucketBoundsMs {
		if durationMs <= bound {
			return i
		}
	}
	return len(latencyBucketBoundsMs) // overflow bucket
}

// toolKey builds the per-tool map key. A NUL separator cannot collide with ':'
// or other characters valid in server/tool names (mirrors storage.toolUsageKey).
func toolKey(server, tool string) string {
	return server + "\x00" + tool
}

// ToolUsage is a per-(server,tool) incremental rollup.
type ToolUsage struct {
	Server  string `json:"server"`
	Tool    string `json:"tool"`
	Calls   int64  `json:"calls"`
	Errors  int64  `json:"errors"`
	Blocked int64  `json:"blocked"`

	ReqBytesSum    int64     `json:"req_bytes_sum"`
	RespBytesSum   int64     `json:"resp_bytes_sum"`
	SizedReqCalls  int64     `json:"sized_req_calls"`  // calls with RequestBytes>0
	SizedRespCalls int64     `json:"sized_resp_calls"` // calls with ResponseBytes>0
	LatencyBuckets []int64   `json:"latency_buckets"`  // len == numLatencyBuckets()
	LastUsed       time.Time `json:"last_used"`
}

// AvgRespBytes returns the average response size over sized calls only
// (records with ResponseBytes>0). ok is false when there are no sized calls.
func (t *ToolUsage) AvgRespBytes() (avg int64, ok bool) {
	if t.SizedRespCalls == 0 {
		return 0, false
	}
	return t.RespBytesSum / t.SizedRespCalls, true
}

// AvgReqBytes returns the average request size over sized calls only
// (records with RequestBytes>0). ok is false when there are no sized calls.
func (t *ToolUsage) AvgReqBytes() (avg int64, ok bool) {
	if t.SizedReqCalls == 0 {
		return 0, false
	}
	return t.ReqBytesSum / t.SizedReqCalls, true
}

// ErrorRate returns Errors/Calls (0 when there are no calls).
func (t *ToolUsage) ErrorRate() float64 {
	if t.Calls == 0 {
		return 0
	}
	return float64(t.Errors) / float64(t.Calls)
}

// Percentile returns an approximate latency percentile (in ms) derived from the
// fixed latency histogram. p is in [0,1].
//
// ms is the upper BOUND of the bucket the percentile falls in, never a measured
// duration — the true value is somewhere at or below it. exceeds reports the one
// case where that reading inverts: the overflow bucket has no upper bound, so
// its ms is the last bound and the true value is ABOVE it. Callers must render
// the two differently ("≤ 5 ms" vs "> 10 s"); a bare number reads as a
// measurement.
//
// An empty histogram returns (0, false): no calls, nothing to bound.
func (t *ToolUsage) Percentile(p float64) (ms int64, exceeds bool) {
	total := int64(0)
	for _, c := range t.LatencyBuckets {
		total += c
	}
	if total == 0 {
		return 0, false
	}
	target := int64(float64(total) * p)
	if target < 1 {
		target = 1
	}
	last := latencyBucketBoundsMs[len(latencyBucketBoundsMs)-1]
	cum := int64(0)
	for i, c := range t.LatencyBuckets {
		cum += c
		if cum >= target {
			if i < len(latencyBucketBoundsMs) {
				return latencyBucketBoundsMs[i], false
			}
			return last, true
		}
	}
	return last, true
}

func (t *ToolUsage) clone() *ToolUsage {
	c := *t
	c.LatencyBuckets = make([]int64, len(t.LatencyBuckets))
	copy(c.LatencyBuckets, t.LatencyBuckets)
	return &c
}

// TimeBucket is a pre-bucketed slice of call volume over time for the timeline.
type TimeBucket struct {
	Start        time.Time `json:"start"`
	Calls        int64     `json:"calls"`
	Errors       int64     `json:"errors"`
	RespBytesSum int64     `json:"resp_bytes_sum"`
}

// UsageAggregate is the actor-owned rollup. Exported fields are JSON-serialized
// for persistence; unexported config fields are restored on construction.
type UsageAggregate struct {
	Tools     map[string]*ToolUsage `json:"tools"`
	Buckets   map[int64]*TimeBucket `json:"buckets"` // key = bucket start unix seconds
	UpdatedAt time.Time             `json:"updated_at"`

	// ClientCalls is a bounded hourly rolling counter keyed by the persisted
	// activity client_name. It avoids an activity-log scan on /clients while
	// retaining enough resolution to answer the 24-hour presence row.
	ClientCalls map[string]map[int64]int64 `json:"client_calls,omitempty"`
}

func newUsageAggregate() *UsageAggregate {
	return &UsageAggregate{
		Tools:       make(map[string]*ToolUsage),
		Buckets:     make(map[int64]*TimeBucket),
		ClientCalls: make(map[string]map[int64]int64),
	}
}

// tool returns the per-(server,tool) rollup, creating it on first use.
func (a *UsageAggregate) tool(server, toolName string) *ToolUsage {
	key := toolKey(server, toolName)
	tu := a.Tools[key]
	if tu == nil {
		tu = &ToolUsage{
			Server:         server,
			Tool:           toolName,
			LatencyBuckets: make([]int64, numLatencyBuckets()),
		}
		a.Tools[key] = tu
	}
	return tu
}

// Apply folds a single activity record into the aggregate. Apply is not safe
// for concurrent use; it is called only by the owning goroutine (see
// UsageStore).
//
// Two populations live in here and they are NOT the same:
//
//   - the per-tool rollup (ToolUsage) describes UPSTREAM TOOLS: calls, latency
//     percentiles, byte averages, blocked and shed attempts. Only records that
//     name an upstream tool belong in it.
//   - the timeline (TimeBucket) describes WHAT THE USER SAW. It must match the
//     glance row-for-row, or the bars under the list disagree with the list
//     above them — the single most confusing thing a dashboard can do.
//
// Which records reach the timeline is NOT decided here: it is
// storage.CountsAsCall, the one definition of "a call the user made", shared
// with the Activity Log's own counters so the two surfaces cannot drift apart.
// Apply only decides what each admitted record does
// to the PER-TOOL rollup, which is a narrower population — upstream tools only.
func (a *UsageAggregate) Apply(rec *storage.ActivityRecord) {
	if rec == nil || rec.ToolName == "" {
		return
	}

	a.applyToolRollup(rec)

	a.applyClientCalls(rec)

	if counted, isError := storage.CountsAsCall(rec); counted {
		a.countInTimeBucket(rec, isError)
	}
}

func (a *UsageAggregate) applyClientCalls(rec *storage.ActivityRecord) {
	if counted, _ := storage.CountsAsCall(rec); !counted || rec.Metadata == nil {
		return
	}
	raw, _ := rec.Metadata["client_name"].(string)
	client := strings.ToLower(strings.TrimSpace(raw))
	if client == "" {
		return
	}
	if a.ClientCalls == nil {
		a.ClientCalls = make(map[string]map[int64]int64)
	}
	if _, ok := a.ClientCalls[client]; !ok && len(a.ClientCalls) >= 32 {
		var oldest string
		var oldestBucket int64
		for key, buckets := range a.ClientCalls {
			for bucket := range buckets {
				if oldest == "" || bucket < oldestBucket {
					oldest, oldestBucket = key, bucket
				}
			}
		}
		if oldest != "" {
			delete(a.ClientCalls, oldest)
		}
	}
	bucket := rec.Timestamp.UTC().Truncate(time.Hour).Unix()
	if a.ClientCalls[client] == nil {
		a.ClientCalls[client] = make(map[int64]int64)
	}
	a.ClientCalls[client][bucket]++
}

// applyToolRollup folds a record into the per-(server,tool) rollup — the
// population behind the Calls-per-tool, token-sink and latency charts. Records
// that name no upstream tool (mcpproxy's own built-ins) contribute nothing:
// admitting them would invent tool rows no upstream owns and mix mcpproxy's own
// latency into upstream percentiles.
func (a *UsageAggregate) applyToolRollup(rec *storage.ActivityRecord) {
	switch {
	case rec.Type == storage.ActivityTypeToolCall && rec.Status == storage.ActivityStatusBlocked:
		// A policy gate refused the dispatch (today: a code_execution sub-call,
		// emitSubCallRefused). Same shape as a blocked policy_decision: the
		// upstream never ran it, so it takes the Blocked counter, not the
		// executed-call statistics.
		a.applyBlocked(rec)
		return
	case rec.Type == storage.ActivityTypeToolCall:
		// folded below
	case rec.Type == storage.ActivityTypePolicyDecision &&
		(rec.Status == storage.ActivityStatusBlocked):
		a.applyBlocked(rec)
		return
	default:
		// internal_tool_call and every non-call event: timeline-only or nothing.
		return
	}

	tu := a.tool(rec.ServerName, rec.ToolName)

	tu.Calls++
	switch rec.Status {
	case "error":
		tu.Errors++
	case "blocked":
		tu.Blocked++
	}
	if rec.ResponseBytes > 0 {
		tu.RespBytesSum += int64(rec.ResponseBytes)
		tu.SizedRespCalls++
	}
	if rec.RequestBytes > 0 {
		tu.ReqBytesSum += int64(rec.RequestBytes)
		tu.SizedReqCalls++
	}
	tu.LatencyBuckets[latencyBucketIndex(rec.DurationMs)]++
	if rec.Timestamp.After(tu.LastUsed) {
		tu.LastUsed = rec.Timestamp
	}
}

// applyBlocked folds a policy-blocked attempt into the per-tool Blocked counter.
// A blocked attempt never executed the tool, so it contributes no Calls,
// latency, or bytes to the PER-TOOL rollup — it only bumps Blocked and
// LastUsed, which keeps latency percentiles and byte averages free of attempts
// that never ran.
//
// It does enter the TIMELINE (storage.CountsAsCall admits it as a failed call).
// The timeline mirrors the glance, and in the glance a blocked attempt is a red
// row the user made and did not get: leaving it out made a run that policy
// refused wholesale render as a flat, empty histogram under a list full of
// failures.
func (a *UsageAggregate) applyBlocked(rec *storage.ActivityRecord) {
	tu := a.tool(rec.ServerName, rec.ToolName)
	tu.Blocked++
	if rec.Timestamp.After(tu.LastUsed) {
		tu.LastUsed = rec.Timestamp
	}
}

// countInTimeBucket adds one admitted call to its hour bucket. Admission and
// the failure classification both come from storage.CountsAsCall — the
// populations that reach the timeline disagree about what counts as a failure
// (a policy block is one, a built-in that answered "blocked" is one, an upstream
// tool_call is only one when its status is "error"), so the decision belongs to
// the shared contract, not here.
func (a *UsageAggregate) countInTimeBucket(rec *storage.ActivityRecord, isError bool) {
	start := rec.Timestamp.UTC().Truncate(usageBucketWidth)
	k := start.Unix()
	b := a.Buckets[k]
	if b == nil {
		b = &TimeBucket{Start: start}
		a.Buckets[k] = b
	}
	b.Calls++
	if isError {
		b.Errors++
	}
	if rec.ResponseBytes > 0 && !truncatedBuiltinOverstatesDelivery(rec) {
		b.RespBytesSum += int64(rec.ResponseBytes)
	}
	a.evictOldBuckets()
}

// evictOldBuckets keeps the timeline bounded to usageMaxBuckets, dropping the
// oldest buckets first.
func (a *UsageAggregate) evictOldBuckets() {
	if len(a.Buckets) <= usageMaxBuckets {
		return
	}
	keys := make([]int64, 0, len(a.Buckets))
	for k := range a.Buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys[:len(a.Buckets)-usageMaxBuckets] {
		delete(a.Buckets, k)
	}
}

// Timeline returns the time buckets in chronological order.
func (a *UsageAggregate) Timeline() []TimeBucket {
	out := make([]TimeBucket, 0, len(a.Buckets))
	for _, b := range a.Buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// clone returns a deep copy safe to publish to readers.
func (a *UsageAggregate) clone() *UsageAggregate {
	c := &UsageAggregate{
		Tools:       make(map[string]*ToolUsage, len(a.Tools)),
		Buckets:     make(map[int64]*TimeBucket, len(a.Buckets)),
		ClientCalls: make(map[string]map[int64]int64, len(a.ClientCalls)),
		UpdatedAt:   a.UpdatedAt,
	}
	for k, tu := range a.Tools {
		c.Tools[k] = tu.clone()
	}
	for k, b := range a.Buckets {
		bc := *b
		c.Buckets[k] = &bc
	}
	for client, buckets := range a.ClientCalls {
		copyBuckets := make(map[int64]int64, len(buckets))
		for bucket, calls := range buckets {
			copyBuckets[bucket] = calls
		}
		c.ClientCalls[client] = copyBuckets
	}
	return c
}

// UsageStore owns the working aggregate and publishes immutable snapshots via
// an atomic pointer (copy-on-write).
//
// The activity write path must stay O(1) and must not block.
// Apply therefore folds the record into the working aggregate under a short
// writer lock and only marks the published snapshot stale — it does NOT clone.
// The O(tools×buckets) clone is deferred to Snapshot (publish-on-read): the
// first reader after a burst of writes materializes one fresh snapshot; the
// owning activity goroutine never pays the clone cost on the hot path. Reads
// with no pending writes are lock-free (atomic load); the usage endpoint and the
// 30s persist flush are the only readers, so clones are rare relative to writes.
type UsageStore struct {
	mu      sync.Mutex // guards working; held for O(1) mutation, and (on read) for the clone
	working *UsageAggregate
	dirty   atomic.Bool // working has unpublished mutations since the last clone
	snap    atomic.Pointer[UsageAggregate]
}

func newUsageStore() *UsageStore {
	s := &UsageStore{working: newUsageAggregate()}
	// Publish an initial empty snapshot so Snapshot() is never nil and a
	// no-write read is lock-free from the start.
	s.mu.Lock()
	s.publishLocked()
	s.mu.Unlock()
	return s
}

// Apply folds a record into the working aggregate. O(1): it mutates under the
// writer lock and marks the snapshot stale, but never clones on the hot path.
func (s *UsageStore) Apply(rec *storage.ActivityRecord) {
	s.mu.Lock()
	s.working.Apply(rec)
	s.dirty.Store(true)
	s.mu.Unlock()
}

// Replace swaps in a freshly built aggregate (cold-start load or rebuild). The
// new snapshot is materialized lazily on the next read, like Apply.
func (s *UsageStore) Replace(agg *UsageAggregate) {
	s.mu.Lock()
	s.working = agg
	s.dirty.Store(true)
	s.mu.Unlock()
}

// publishLocked deep-copies the working aggregate, stamps freshness, stores it
// as the current immutable snapshot, and clears the dirty flag. Caller must
// hold s.mu.
func (s *UsageStore) publishLocked() {
	c := s.working.clone()
	c.UpdatedAt = time.Now()
	s.snap.Store(c)
	s.dirty.Store(false)
}

// Snapshot returns the latest immutable aggregate snapshot reflecting all writes
// applied so far. When writes have occurred since the last publish it
// materializes one fresh snapshot here (the clone runs off the activity hot
// path); otherwise it is a lock-free atomic load. The returned value must be
// treated as read-only.
func (s *UsageStore) Snapshot() *UsageAggregate {
	// Fast path: nothing written since the last publish — lock-free.
	if !s.dirty.Load() {
		if snap := s.snap.Load(); snap != nil {
			return snap
		}
	}
	// Stale: materialize a fresh snapshot. Double-check under the lock so
	// concurrent readers don't each re-clone.
	s.mu.Lock()
	if s.dirty.Load() {
		s.publishLocked()
	}
	snap := s.snap.Load()
	s.mu.Unlock()
	return snap
}

// truncatedBuiltinOverstatesDelivery reports whether a record's ResponseBytes
// describes MORE than the agent actually consumed, and so must not be added to
// delivered traffic.
//
// The direction differs by record type, and that is the whole point:
//
//   - an upstream tool_call is truncated on the way into the LOG while the agent
//     received the whole response, so the pre-truncation length is honest;
//   - an internal built-in — retrieve_tools above all — is the reverse: the log
//     keeps the FULL response and the agent consumed the CUT text, so the
//     pre-truncation length describes something larger than was delivered.
//
// Counting the second case inflates the usage timeline in the flattering
// direction, which is the one error a cost surface must not make. It became
// reachable only when internal calls began carrying byte counts at all (spec
// 103); before that they contributed zero and the question could not arise.
func truncatedBuiltinOverstatesDelivery(rec *storage.ActivityRecord) bool {
	return rec.ResponseTruncated && rec.Type == storage.ActivityTypeInternalToolCall
}
