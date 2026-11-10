package runtime

import (
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/storage"
	"github.com/slopster67/mcpproxy-go/internal/toolreview"
)

// reviewBindingMismatch compares candidates with expected. A candidate whose
// name is absent from expected, or whose expected value is empty, is
// unreviewed; one whose fingerprint differs is changed.
func reviewBindingMismatch(candidates []*storage.ToolApprovalRecord, expected map[string]string) (changed, unreviewed []string) {
	for _, rec := range candidates {
		want, ok := expected[rec.ToolName]
		switch {
		case !ok || want == "":
			unreviewed = append(unreviewed, rec.ToolName)
		case want != reviewFingerprint(rec):
			changed = append(changed, rec.ToolName)
		}
	}
	return changed, unreviewed
}

// staleReviewError logs and returns the out-of-date error for a refused
// bound approval, or nil when nothing is stale.
func (r *Runtime) staleReviewError(serverName string, changed, unreviewed, missingBlocks []string) error {
	stale := toolreview.NewStaleToolReviewError(serverName, changed, unreviewed, missingBlocks)
	if stale == nil {
		return nil
	}
	r.logger.Info("Review-bound approval refused: the review is out of date; nothing was approved",
		zap.String("server", serverName),
		zap.Strings("changed", stale.Changed),
		zap.Strings("unreviewed", stale.Unreviewed),
		zap.Strings("missing_blocks", stale.MissingBlocks))
	return stale
}

// ApproveToolsReviewedAt is ApproveToolsReviewed bound to expectedIncarnation,
// the server identity the client reviewed (0 binds to the
// incarnation current when the call starts). The two bindings are independent
// and both enforced under the approval lock: a replaced server answers
// ErrServerReplaced (server_replaced), changed definitions answer
// *toolreview.StaleToolReviewError (review_out_of_date); the incarnation is
// checked first because a replaced server's records are not the reviewed ones.
func (r *Runtime) ApproveToolsReviewedAt(serverName string, toolNames []string, expected map[string]string, approvedBy string, expectedIncarnation int64) (int, error) {
	if expected == nil {
		return 0, fmt.Errorf("expected tool hashes are required")
	}
	if r.storageManager == nil {
		return 0, nil
	}
	inc, err := r.CaptureApprovalIncarnation(serverName, expectedIncarnation)
	if err != nil {
		return 0, err
	}
	names := make([]string, 0, len(toolNames))
	seen := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	unlock := r.lockToolApprovals(serverName)
	if err := r.checkApprovalIncarnationHeld(serverName, inc); err != nil {
		unlock()
		return 0, err
	}
	candidates := make([]*storage.ToolApprovalRecord, 0, len(names))
	for _, name := range names {
		rec, err := r.storageManager.GetToolApproval(serverName, name)
		if err != nil {
			if errors.Is(err, storage.ErrToolApprovalNotFound) {
				continue
			}
			unlock()
			return 0, fmt.Errorf("read tool approval %s:%s: %w", serverName, name, err)
		}
		candidates = append(candidates, rec)
	}
	changed, unreviewed := reviewBindingMismatch(candidates, expected)
	if err := r.staleReviewError(serverName, changed, unreviewed, nil); err != nil {
		unlock()
		return 0, err
	}

	approved, err := r.approveToolsLocked(serverName, names, approvedBy, false)
	unlock()
	r.notifyToolsApproved(serverName, approved, approvedBy)
	return approved, err
}

// ApproveAllToolsReviewedAt is ApproveAllToolsReviewed bound to
// expectedIncarnation (see ApproveToolsReviewedAt).
func (r *Runtime) ApproveAllToolsReviewedAt(serverName string, expected map[string]string, approvedBy string, expectedIncarnation int64) (int, error) {
	if expected == nil {
		return 0, fmt.Errorf("expected tool hashes are required")
	}
	if r.storageManager == nil {
		return 0, nil
	}
	inc, err := r.CaptureApprovalIncarnation(serverName, expectedIncarnation)
	if err != nil {
		return 0, err
	}
	unlock := r.lockToolApprovals(serverName)
	if err := r.checkApprovalIncarnationHeld(serverName, inc); err != nil {
		unlock()
		return 0, err
	}
	records, err := r.storageManager.ListToolApprovals(serverName)
	if err != nil {
		unlock()
		return 0, err
	}
	var candidates []*storage.ToolApprovalRecord
	var names []string
	for _, rec := range records {
		if rec.Status == storage.ToolApprovalStatusPending || rec.Status == storage.ToolApprovalStatusChanged {
			candidates = append(candidates, rec)
			names = append(names, rec.ToolName)
		}
	}
	changed, unreviewed := reviewBindingMismatch(candidates, expected)
	if err := r.staleReviewError(serverName, changed, unreviewed, nil); err != nil {
		unlock()
		return 0, err
	}

	approved := 0
	if len(names) > 0 {
		approved, err = r.approveToolsLocked(serverName, names, approvedBy, false)
	}
	unlock()
	r.notifyToolsApproved(serverName, approved, approvedBy)
	return approved, err
}
