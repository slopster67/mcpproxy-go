package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	internalRuntime "github.com/slopster67/mcpproxy-go/internal/runtime"
)

// Approval endpoints accept an optional
// "expected_incarnation", the incarnation of the server the client reviewed
// (GET /api/v1/servers[/{id}] and the review payload carry "incarnation").
// When the server was removed and re-added since, the write is refused with
// 409 "server_replaced: …" and nothing is written. When the field is absent,
// the runtime binds the approval to the incarnation current when the request
// started.

// incarnationApprovalController binds approval writes to an expected
// incarnation.
type incarnationApprovalController interface {
	BlockToolsAt(serverName string, toolNames []string, blockedBy string, expectedIncarnation int64) (int, error)
	BlockAllToolsAt(serverName string, blockedBy string, expectedIncarnation int64) (int, error)
	SetToolEnabledAt(serverName, toolName string, enabled bool, updatedBy string, expectedIncarnation int64) error
}

// reviewedIncarnationController carries both bindings on a review-bound tool
// approval: the expected incarnation and the reviewed definitions.
type reviewedIncarnationController interface {
	ApproveToolsReviewedAt(serverName string, toolNames []string, expected map[string]string, approvedBy string, expectedIncarnation int64) (int, error)
	ApproveAllToolsReviewedAt(serverName string, expected map[string]string, approvedBy string, expectedIncarnation int64) (int, error)
}

// writeApprovalWriteError maps an approval writer's error: 409
// server_replaced when the server was removed or replaced since the reviewed
// incarnation, 404 when it is not configured, 500 otherwise (with prefix).
func (s *Server) writeApprovalWriteError(w http.ResponseWriter, r *http.Request, err error, prefix string) {
	var replaced *commitview.ServerReplacedError
	switch {
	case errors.As(err, &replaced):
		s.writeError(w, r, http.StatusConflict, replaced.Error())
	case errors.Is(err, commitview.ErrServerReplaced):
		s.writeError(w, r, http.StatusConflict, "server_replaced: the server was removed or replaced since it was reviewed")
	case errors.Is(err, internalRuntime.ErrServerNotFound):
		s.writeError(w, r, http.StatusNotFound, err.Error())
	default:
		s.writeError(w, r, http.StatusInternalServerError, fmt.Sprintf("%s: %v", prefix, err))
	}
}

// serverIncarnationReader reports a server's committed incarnation.
type serverIncarnationReader interface {
	ServerIncarnation(serverName string) (int64, bool)
}

// serverIncarnationOf returns name's committed incarnation, or 0 when the
// controller cannot tell.
func (s *Server) serverIncarnationOf(name string) int64 {
	inc, _ := s.controller.ServerIncarnation(name)
	return inc
}

// incarnationReadAttempts bounds how often a listing re-reads rows whose
// server identity moved while they were read.
const incarnationReadAttempts = 3

// errIncarnationUnsettled is returned when a server kept being removed or
// replaced while its rows were read; the handler answers 409.
var errIncarnationUnsettled = errors.New("server_replaced: the server was removed or replaced while it was read; retry")

// readAtSettledIncarnation runs read between two reads of name's committed
// incarnation and returns the incarnation the rows belong to. The first read alone stamps rows with
// an incarnation no newer than their server, but when it saw the name ABSENT
// a same-name replacement landing before read returns the replacement's rows
// with no stamp at all, and the Web UI then sends no expected_incarnation, so
// a decision binds to whichever server exists when the operator clicks. The
// rows are therefore returned only when the identity did not move across the
// read; otherwise they are read again, and errIncarnationUnsettled is
// returned when it never settles.
func (s *Server) readAtSettledIncarnation(name string, read func() error) (int64, error) {
	for i := 0; i < incarnationReadAttempts; i++ {
		before := s.serverIncarnationOf(name)
		if err := read(); err != nil {
			return before, err
		}
		if s.serverIncarnationOf(name) == before {
			return before, nil
		}
	}
	return 0, errIncarnationUnsettled
}
