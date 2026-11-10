package runtime

import (
	"sync"
)

// toolApprovalServerLock is one server's tool-approval lock plus the capture
// order state it guards.
//
// applied is the highest capture sequence that entered an inventory apply for
// the server (records, deletes, eviction, index, last-good snapshot, StateView
// publish). Every admitted capture advances it, including one whose apply
// later fails and a lenient-empty observation, so it is a fence: an older
// capture that reaches the apply afterwards writes nothing.
//
// definitions is the sequence of the newest admitted capture that carried
// definitions (a non-empty inventory, an authoritative empty one, or a
// quarantined review capture). The retained last-good snapshot may be
// re-applied by operator reconciliation (the approval reindex, the sweep
// fallback) only while its capture is that one.
//
// Both fields are guarded by mu. Sequences are process-global (managed.
// InventoryCapture), so a removed and re-added server needs no reset: every
// capture of the new instance is newer.
type toolApprovalServerLock struct {
	mu          sync.Mutex
	applied     uint64
	definitions uint64
}

// toolApprovalServerState returns serverName's lock state, creating it.
func (r *Runtime) toolApprovalServerState(serverName string) *toolApprovalServerLock {
	v, _ := r.toolApprovalLocks.LoadOrStore(serverName, &toolApprovalServerLock{})
	return v.(*toolApprovalServerLock)
}

// lockToolApprovals acquires the per-server tool-approval lock and returns its
// unlock function.
func (r *Runtime) lockToolApprovals(serverName string) func() {
	st := r.toolApprovalServerState(serverName)
	st.mu.Lock()
	return st.mu.Unlock
}
