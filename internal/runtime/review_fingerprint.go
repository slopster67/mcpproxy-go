package runtime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// The review fingerprint.
//
// A review reports, per tool, an opaque fingerprint of the definition it
// shows (wire name current_hash). A client sends the fingerprints back in
// expected_hashes, and the approval is refused as out of date when the stored
// definition no longer matches, so an operator can only approve what they
// saw. The fingerprint is derived from the stored approval record and never
// stored itself.
//
// It covers the server name (a binding for server A never matches server B),
// the raw tool name, the storage CurrentHash, the description, the input
// schema, the output schema and the four safety hints by their RAW value
// (unset, false, true), so a hint change is always visible to a review even
// where the rug-pull hash treats an unset hint and its spelled-out default
// alike. Each field is length-prefixed, so no two distinct field tuples share
// a fingerprint. Schemas are JSON-normalized, as the rug-pull hash does, so a
// key-order-only difference is not a change. Title, status, disabled,
// timestamps and scan evidence are not part of it. The rug-pull hash
// (calculateToolApprovalHash*) is separate.

// reviewFingerprintVersion prefixes every fingerprint. A fingerprint of
// another version never matches, which only makes the client reload.
const reviewFingerprintVersion = "rv2"

// reviewFingerprint returns the review fingerprint of rec: "rv2-" followed by
// 64 hex characters. It is non-empty for every record, including one with an
// empty CurrentHash (a toggle-synthesized record), whose stored definition
// still determines it.
func reviewFingerprint(rec *storage.ToolApprovalRecord) string {
	if rec == nil {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{
		reviewFingerprintVersion,
		rec.ServerName,
		rec.ToolName,
		rec.CurrentHash,
		rec.CurrentDescription,
		normalizeJSON(rec.CurrentSchema),
		normalizeJSON(rec.CurrentOutputSchema),
		reviewHintString(rec.CurrentAnnotations),
	} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write([]byte(part))
	}
	return reviewFingerprintVersion + "-" + hex.EncodeToString(h.Sum(nil))
}

// reviewHintString renders the four safety hints as
// "ro=<v>;de=<v>;id=<v>;ow=<v>" with v "-" (unset), "0" or "1".
func reviewHintString(a *config.ToolAnnotations) string {
	v := func(b *bool) string {
		switch {
		case b == nil:
			return "-"
		case *b:
			return "1"
		default:
			return "0"
		}
	}
	if a == nil {
		return "ro=-;de=-;id=-;ow=-"
	}
	return strings.Join([]string{
		"ro=" + v(a.ReadOnlyHint),
		"de=" + v(a.DestructiveHint),
		"id=" + v(a.IdempotentHint),
		"ow=" + v(a.OpenWorldHint),
	}, ";")
}
