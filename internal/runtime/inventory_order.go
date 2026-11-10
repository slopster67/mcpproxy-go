package runtime

import (
	"errors"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/upstream/managed"
)

// Tool inventories are applied in capture order.
//
// Every upstream tools/list carries a managed.InventoryCapture taken by the
// leader of its flight before the upstream request. Each inventory
// write path — the sweep, single-server discovery (connect,
// tools/list_changed, refresh), the quarantined review capture, the lenient
// empty observation and the operator reconciliations that re-apply the
// last-good snapshot — admits its capture under the server's tool-approval
// lock, the same hold in which it then writes records, deletes, evicts,
// indexes, stores the last-good snapshot and publishes the StateView.
// Admission is the ordering point:
//
//   - a capture older than the newest admitted one (seq < applied) is stale
//     and writes nothing;
//   - a capture whose client was retired (replaced or removed) or reconnected
//     since the list started is not current and writes nothing; the caller
//     re-lists the current client;
//   - seq 0 (no managed client) is admitted only while nothing
//     ordered was admitted for the server;
//   - a lenient empty result is an observation: it advances applied and
//     writes nothing else;
//   - a reconciliation re-applies the retained snapshot only while its
//     capture is still the newest one with definitions and still current;
//     otherwise the approval reindex re-lists and the sweep fallback
//     skips the server.
//
// Lock order: captureSnapMu → approval lock → storage / index /
// supervisor state. The currency check reads two atomics of the client
// (CaptureIsCurrent) and takes no manager or client lock.

// inventoryMode says what kind of inventory write is being admitted.
type inventoryMode int

const (
	// inventoryDefinitions is a fresh capture that carries definitions: a
	// non-empty inventory, an authoritative empty one, or a quarantined
	// review capture. It advances applied and definitions.
	inventoryDefinitions inventoryMode = iota
	// inventoryObservation is a lenient empty result that keeps the retained
	// inventory. It advances applied only.
	inventoryObservation
	// inventoryReconcile re-applies the retained last-good snapshot. It
	// advances nothing.
	inventoryReconcile
)

// inventoryRef is the capture an inventory write applies, and its mode.
type inventoryRef struct {
	capture managed.InventoryCapture
	mode    inventoryMode
}

// inventoryVerdict is the outcome of admitInventoryHeld.
type inventoryVerdict int

const (
	inventoryAdmitted   inventoryVerdict = iota
	inventoryStale                       // a newer capture was already admitted: write nothing, do not re-list
	inventoryNotCurrent                  // the capture's client was retired or reconnected: write nothing, re-list
)

// errInventoryStale reports an inventory dropped because a newer capture was
// already applied for the server. Terminal: the newer capture stands.
var errInventoryStale = errors.New("tool inventory capture is older than one already applied")

// errInventoryNotCurrent reports an inventory dropped because the client
// that listed it was retired or reconnected since. The caller re-lists the
// server's current client (as for errDiscoveryStaleGeneration).
var errInventoryNotCurrent = errors.New("tool inventory capture came from a retired or reconnected client")

// admitInventoryHeld is the capture-order gate. The caller holds
// serverName's tool-approval lock and has passed the commit-generation check. An
// admitted fresh capture advances the server's applied (and, with
// definitions, its definitions) sequence before anything is written, so a
// later failure still fences older captures.
func (r *Runtime) admitInventoryHeld(serverName string, ref inventoryRef) inventoryVerdict {
	st := r.toolApprovalServerState(serverName)
	c := ref.capture
	if c.Client != nil && !c.Client.CaptureIsCurrent(c) {
		r.logger.Info("Tool inventory discarded: it was captured by a retired or reconnected client",
			zap.String("server", serverName), zap.Uint64("capture_seq", c.Seq))
		return inventoryNotCurrent
	}
	if ref.mode == inventoryReconcile {
		if c.Seq != st.definitions {
			r.logger.Info("Retained tool snapshot not re-applied: a newer capture superseded it",
				zap.String("server", serverName), zap.Uint64("snapshot_seq", c.Seq),
				zap.Uint64("definitions_seq", st.definitions))
			return inventoryNotCurrent
		}
		return inventoryAdmitted
	}
	if c.Seq == 0 {
		if st.applied != 0 {
			r.logger.Debug("Unordered tool inventory dropped: an ordered capture was already applied",
				zap.String("server", serverName), zap.Uint64("applied_seq", st.applied))
			return inventoryStale
		}
		return inventoryAdmitted
	}
	if c.Seq < st.applied {
		r.logger.Info("Tool inventory dropped: a newer capture was already applied",
			zap.String("server", serverName), zap.Uint64("capture_seq", c.Seq),
			zap.Uint64("applied_seq", st.applied))
		return inventoryStale
	}
	st.applied = c.Seq
	if ref.mode == inventoryDefinitions {
		st.definitions = c.Seq
	}
	return inventoryAdmitted
}

// inventoryVerdictErr maps a refusal onto the error the apply returns.
func inventoryVerdictErr(v inventoryVerdict) error {
	switch v {
	case inventoryStale:
		return errInventoryStale
	case inventoryNotCurrent:
		return errInventoryNotCurrent
	}
	return nil
}

// storeLastGoodLocked replaces serverName's last-good snapshot and capture
// stamp with tools from capture c, unless the stored snapshot came from a
// newer capture. The caller holds lastGoodToolsMu for writing.
func (r *Runtime) storeLastGoodLocked(serverName string, tools []*config.ToolMetadata, c managed.InventoryCapture, instanceGen int64) {
	if r.lastGoodCapture == nil {
		r.lastGoodCapture = make(map[string]managed.InventoryCapture)
	}
	if prev, ok := r.lastGoodCapture[serverName]; ok && c.Seq < prev.Seq {
		return
	}
	snapshot := make([]*config.ToolMetadata, len(tools))
	copy(snapshot, tools)
	r.lastGoodTools[serverName] = snapshot
	r.lastGoodCapture[serverName] = c
	// The snapshot is bound to the commit generation of the instance it was
	// listed from, so a local reindex hands it to the final generation check
	// and never reapplies a superseded instance's tools.
	if r.lastGoodGen == nil {
		r.lastGoodGen = make(map[string]int64)
	}
	r.lastGoodGen[serverName] = instanceGen
	r.noteCaptureLocked(serverName)
}

// lastGoodSnapshotWithCaptureGen is lastGoodSnapshotWithCapture that also
// returns the commit generation of the instance the snapshot was listed from
// read in the same hold.
func (r *Runtime) lastGoodSnapshotWithCaptureGen(serverName string) ([]*config.ToolMetadata, managed.InventoryCapture, uint64, int64) {
	r.lastGoodToolsMu.RLock()
	defer r.lastGoodToolsMu.RUnlock()
	seq := r.lastGoodPurgeSeq[serverName]
	snapshot := r.lastGoodTools[serverName]
	if len(snapshot) == 0 {
		return nil, managed.InventoryCapture{}, seq, 0
	}
	cp := make([]*config.ToolMetadata, len(snapshot))
	copy(cp, snapshot)
	return cp, r.lastGoodCapture[serverName], seq, r.lastGoodGen[serverName]
}

func (r *Runtime) inventoryBeforeApply(serverName string, c managed.InventoryCapture) {

}
