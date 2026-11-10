package runtime

import (
	"errors"
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/runtime/configsvc"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// Approval writers bind to the incarnation they reviewed.
//
// An approval is a decision about one server identity. The records it writes
// are keyed by the server name, and a name can be removed and re-added — with
// different tools behind it — between the moment an operator (or the scanner)
// reviewed it and the moment the write lands. Every approval writer therefore:
//
//  1. captures the server's incarnation when it starts (or takes the caller's
//     expected_incarnation, which the Web UI echoes from the payload it
//     rendered);
//  2. takes the server's approval lock (L1), the lock the removal commit
//     purges the server's records under; and
//  3. re-checks, under that lock, that the stored row still carries that
//     incarnation, and writes nothing (ErrServerReplaced) when it does not.
//
// The incarnation, not the generation: an unrelated edit of the server
// (a header) keeps the incarnation, so it never rejects an
// approval; only a remove followed by a create does.

// ServerIncarnation returns name's committed incarnation from the live
// snapshot. ok is false when the server is not configured.
func (r *Runtime) ServerIncarnation(name string) (int64, bool) {
	g, ok := r.configSvc.Current().ServerGeneration(name)
	if !ok {
		return 0, false
	}
	return g.Incarnation, true
}

// committedServerView returns name's configuration and its committed
// generation from ONE configuration snapshot, so a view that displays the
// configuration and offers the incarnation for expected_incarnation never
// pairs one server's command or URL with another's incarnation. server is
// nil when name is not configured.
func (r *Runtime) committedServerView(name string) (server *config.ServerConfig, gen configsvc.ServerGen, hasGen bool) {
	snap := r.configSvc.Current()
	server = snap.GetServer(name)
	if server != nil {
		gen, hasGen = snap.ServerGeneration(name)
	}
	return server, gen, hasGen
}

// serverReplacedError builds the ErrServerReplaced error for name. Its text is
// the REST 409 contract (contracts/surfaces.md):
// "server_replaced: server '<name>' was removed or replaced since it was reviewed".
func serverReplacedError(name string) error {
	return &commitview.ServerReplacedError{Name: name}
}

// CaptureApprovalIncarnation is step 1 of an approval writer: it returns
// expected when the caller supplied one (> 0), otherwise the server's current
// incarnation. A server that is not configured fails with ErrServerNotFound
// .
func (r *Runtime) CaptureApprovalIncarnation(name string, expected int64) (int64, error) {
	inc := expected
	if inc <= 0 {
		cur, ok := r.ServerIncarnation(name)
		if !ok {
			return 0, &ServerNotFoundError{Name: name}
		}
		inc = cur
	}

	return inc, nil
}

// checkApprovalIncarnationHeld is step 3: called with name's approval lock
// held, it fails with ErrServerReplaced unless the stored row exists and
// carries incarnation inc. inc == 0 is an unbound capture (a database no
// commit has written) and is not checked. A row that cannot be read fails
// closed.
func (r *Runtime) checkApprovalIncarnationHeld(name string, inc int64) error {
	if inc == 0 {
		return nil
	}
	row, err := r.storageManager.GetServerRecord(name)
	switch {
	case errors.Is(err, storage.ErrUpstreamNotFound):
		return serverReplacedError(name)
	case err != nil:
		return fmt.Errorf("read server %s for the approval check: %w", name, err)
	case row == nil:
		return serverReplacedError(name)
	case row.Incarnation != inc:
		return serverReplacedError(name)
	}
	return nil
}

// WithServerApprovalLockAt runs fn while holding name's approval lock, after
// checking (under that lock) that the stored row still carries incarnation
// inc. On a mismatch fn does not run and the error is ErrServerReplaced.
// Writers outside the runtime that touch name-keyed approval state — the
// scanner's baseline and blocks — use it. fn must not
// call back into a runtime method that takes the same lock, nor Commit.
func (r *Runtime) WithServerApprovalLockAt(name string, inc int64, fn func() error) error {
	defer r.lockToolApprovals(name)()
	if err := r.checkApprovalIncarnationHeld(name, inc); err != nil {
		return err
	}
	return fn()
}
