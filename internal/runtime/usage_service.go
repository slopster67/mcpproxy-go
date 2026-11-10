package runtime

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// ActivityService orchestration for the usage aggregate.
//
// The ActivityService goroutine owns the aggregate: it folds each persisted
// tool_call into it (Apply, in handleEvent), serves immutable snapshots to
// readers, and persists the snapshot periodically + on shutdown. On a cold
// start it loads the last persisted snapshot, or rebuilds via a single
// full-scan when none exists.

// DefaultUsagePersistInterval is the default snapshot flush cadence.
const DefaultUsagePersistInterval = 30 * time.Second

// encodeUsageAggregate serializes the aggregate snapshot for persistence.
func encodeUsageAggregate(a *UsageAggregate) ([]byte, error) {
	return json.Marshal(a)
}

// decodeUsageAggregate restores a persisted snapshot, preserving constructed
// defaults for any maps absent from the blob.
func decodeUsageAggregate(data []byte) (*UsageAggregate, error) {
	agg := newUsageAggregate()
	if err := json.Unmarshal(data, agg); err != nil {
		return nil, err
	}
	if agg.Tools == nil {
		agg.Tools = map[string]*ToolUsage{}
	}
	if agg.Buckets == nil {
		agg.Buckets = map[int64]*TimeBucket{}
	}
	return agg, nil
}

// UsageSnapshot returns the latest immutable usage aggregate snapshot. Safe for
// concurrent readers; never blocks. Returns nil if usage
// tracking was never initialized.
func (s *ActivityService) UsageSnapshot() *UsageAggregate {
	if s.usage == nil {
		return nil
	}
	return s.usage.Snapshot()
}

// initUsageFromStorage loads the persisted snapshot, or rebuilds the aggregate
// via exactly one full-scan when no snapshot exists. Called once before the
// event loop starts.
func (s *ActivityService) initUsageFromStorage() {
	if s.usage == nil {
		s.usage = newUsageStore()
	}

	data, err := s.storage.LoadUsageSnapshot()
	if err != nil {
		s.logger.Warn("Failed to load usage snapshot; rebuilding from scan", zap.Error(err))
	} else if len(data) > 0 {
		if agg, derr := decodeUsageAggregate(data); derr != nil {
			s.logger.Warn("Failed to decode usage snapshot; rebuilding from scan", zap.Error(derr))
		} else {
			s.usage.Replace(agg)
			replayed := s.replayActivitiesSince(agg.UpdatedAt)
			s.logger.Info("Loaded usage aggregate from persisted snapshot",
				zap.Int("tools", len(agg.Tools)),
				zap.Time("snapshot_updated_at", agg.UpdatedAt),
				zap.Int("records_replayed", replayed))
			return
		}
	}

	// Cold start (or unreadable snapshot): rebuild with a single full-scan.
	agg := newUsageAggregate()
	scanned := 0
	if serr := s.storage.ScanAllActivities(func(rec *storage.ActivityRecord) {
		scanned++
		agg.Apply(rec)
	}); serr != nil {
		s.logger.Error("Failed to rebuild usage aggregate from scan", zap.Error(serr))
	}
	s.usage.Replace(agg)
	s.logger.Info("Rebuilt usage aggregate from activity scan",
		zap.Int("records_scanned", scanned),
		zap.Int("tools", len(agg.Tools)))
}

// replayActivitiesSince folds every activity record persisted AFTER the loaded
// snapshot was stamped into the aggregate, and reports how many it applied.
//
// The snapshot is flushed every 30s and on clean shutdown, so after a crash
// the records since the last flush exist only in the activity log; the
// aggregate is never rebuilt once a snapshot exists, so they must be replayed.
//
// Safe to run on every start because the write path persists a record BEFORE
// folding it in (ActivityService.handleToolCallCompleted), so "saved but not
// counted" is the only inconsistency possible, never "counted but not saved".
// The bound is strictly greater than the stamp, so nothing already in the
// snapshot can be applied a second time.
func (s *ActivityService) replayActivitiesSince(since time.Time) int {
	if since.IsZero() {
		// Snapshot from a build that did not stamp its aggregate: there is no
		// safe lower bound, and replaying from the beginning would double every
		// record it already contains. Keep the snapshot as loaded.
		s.logger.Warn("Persisted usage snapshot has no timestamp; skipping catch-up replay")
		return 0
	}
	replayed := 0
	if err := s.storage.ScanActivitiesAfter(since, func(rec *storage.ActivityRecord) {
		replayed++
		s.usage.Apply(rec)
	}); err != nil {
		s.logger.Warn("Failed to replay activity records into the usage aggregate",
			zap.Error(err), zap.Time("since", since))
	}
	return replayed
}

// persistUsage flushes the current snapshot to storage. No-op if usage tracking
// is uninitialized or no snapshot exists yet.
func (s *ActivityService) persistUsage() {
	if s.usage == nil {
		return
	}
	snap := s.usage.Snapshot()
	if snap == nil {
		return
	}
	data, err := encodeUsageAggregate(snap)
	if err != nil {
		s.logger.Warn("Failed to encode usage snapshot for persistence", zap.Error(err))
		return
	}
	if err := s.storage.SaveUsageSnapshot(data); err != nil {
		s.logger.Warn("Failed to persist usage snapshot", zap.Error(err))
	}
}

// runUsageFlushLoop periodically persists the usage snapshot until shutdown.
func (s *ActivityService) runUsageFlushLoop(ctx context.Context) {
	ticker := time.NewTicker(s.usageFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.persistUsage()
		}
	}
}
