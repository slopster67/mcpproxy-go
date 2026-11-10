package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/hash"
	"github.com/slopster67/mcpproxy-go/internal/runtime/stateview"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

func calculateToolApprovalHashWithOutputSchema(toolName, description, schemaJSON, outputSchemaJSON string, annotations *config.ToolAnnotations) string {
	// Every field is length-prefixed and an absent output schema has its own
	// marker, so no two different contracts share an encoding. Schemas are
	// JSON-normalized so a key-order-only difference is not a change; the
	// safety hints are their effective fingerprint, "" for the MCP defaults.
	h := sha256.New()
	writeApprovalHashField(h, toolApprovalHashTag)
	writeApprovalHashField(h, toolName)
	writeApprovalHashField(h, description)
	writeApprovalHashField(h, normalizeJSON(schemaJSON))
	if normalized := normalizeJSON(outputSchemaJSON); normalized != "" {
		writeApprovalHashField(h, "output:present")
		writeApprovalHashField(h, normalized)
	} else {
		writeApprovalHashField(h, "output:absent")
	}
	writeApprovalHashField(h, safetyHintsFingerprint(annotations))
	return hex.EncodeToString(h.Sum(nil))
}

// toolApprovalHashTag domain-separates the approval hash from the review
// fingerprint.
const toolApprovalHashTag = "mcpproxy-tool-approval-v5"

// writeApprovalHashField writes one length-prefixed field (8-byte big-endian
// length, then the bytes).
func writeApprovalHashField(w io.Writer, field string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(field)))
	_, _ = w.Write(n[:])
	_, _ = w.Write([]byte(field))
}

// safetyHintsFingerprint renders the EFFECTIVE values of the safety-relevant
// MCP tool annotation hints, or "" when they are all at their defaults
// (readOnlyHint=false, destructiveHint=true, idempotentHint=false,
// openWorldHint=true — the most conservative reading). These hints steer
// read/write routing (call_tool_read vs call_tool_write) and review tiers, so a
// flip is a contract change the approval gate must see.
func safetyHintsFingerprint(annotations *config.ToolAnnotations) string {
	readOnly, destructive, idempotent, openWorld := false, true, false, true
	if annotations != nil {
		if annotations.ReadOnlyHint != nil {
			readOnly = *annotations.ReadOnlyHint
		}
		if annotations.DestructiveHint != nil {
			destructive = *annotations.DestructiveHint
		}
		if annotations.IdempotentHint != nil {
			idempotent = *annotations.IdempotentHint
		}
		if annotations.OpenWorldHint != nil {
			openWorld = *annotations.OpenWorldHint
		}
	}
	if !readOnly && destructive && !idempotent && openWorld {
		return ""
	}
	return fmt.Sprintf("readOnly=%t,destructive=%t,idempotent=%t,openWorld=%t", readOnly, destructive, idempotent, openWorld)
}

// normalizeJSON parses a JSON string and re-serializes with sorted keys.
// Returns the original string if parsing fails (non-JSON content). Delegates to
// hash.NormalizeJSON so the approval hash and the upstream tool capture share a
// single canonical normalizer.
func normalizeJSON(s string) string {
	return hash.NormalizeJSON(s)
}

type TransitionReason string

const (
	ReasonHashMatch          TransitionReason = "hash_match"
	ReasonDescriptionRevert  TransitionReason = "description_revert"
	ReasonUserApprove        TransitionReason = "user_approve"
	ReasonAutoApprove        TransitionReason = "auto_approve"
	ReasonAutoApproveChanges TransitionReason = "auto_approve_changes"
	// ReasonScanApproved marks a NEW tool auto-approved because the server runs
	// in trust_mode: scan AND a synchronous, offline TPA scan of the tool
	// returned a green (clean) verdict. It never clears a changed (rug-pull)
	// record: a clean verdict is not proof a changed
	// contract is safe, so changed→approved needs an operator.
)

func assertToolApprovalInvariant(oldStatus, newStatus string, reason TransitionReason) error {
	if newStatus != storage.ToolApprovalStatusApproved {
		return nil
	}

	switch oldStatus {
	case storage.ToolApprovalStatusChanged:
		switch reason {
		case ReasonHashMatch, ReasonDescriptionRevert,
			ReasonUserApprove,
			ReasonAutoApproveChanges:
			return nil
		default:
			return fmt.Errorf("invariant violation: changed→approved with reason %q "+
				"(requires user action or description revert)", reason)
		}
	case storage.ToolApprovalStatusPending:
		switch reason {
		case ReasonUserApprove, ReasonAutoApprove, ReasonAutoApproveChanges:
			return nil
		default:
			return fmt.Errorf("invariant violation: pending→approved with reason %q "+
				"(requires user action)", reason)
		}
	}
	return nil
}

// enforceInvariant logs and returns the invariant error, or panics in test mode.
func (r *Runtime) enforceInvariant(serverName, toolName, oldStatus, newStatus string, reason TransitionReason) error {
	err := assertToolApprovalInvariant(oldStatus, newStatus, reason)
	if err == nil {
		return nil
	}
	r.logger.Error("Tool approval invariant violation",
		zap.String("server", serverName),
		zap.String("tool", toolName),
		zap.String("old_status", oldStatus),
		zap.String("new_status", newStatus),
		zap.String("reason", string(reason)),
		zap.Error(err))
	return err
}

func (r *Runtime) refreshHeldContractSnapshot(serverName, toolName string, existing *storage.ToolApprovalRecord, tool *config.ToolMetadata, schemaJSON, outputSchemaJSON, currentHash string) {
	if existing == nil {
		return
	}
	// The live contract is the recorded one when the hashes match.
	if existing.CurrentHash == currentHash {
		captured := capturedToolAnnotations(tool.Annotations)
		// RAW hints: an unset hint and its spelled-out
		// default hash alike, but the review fingerprint tells them apart, so
		// the recorded snapshot must follow the raw value.
		if existing.CurrentAnnotations != nil &&
			reviewHintString(existing.CurrentAnnotations) == reviewHintString(captured) {
			return
		}
		existing.CurrentAnnotations = captured
		if r.storageManager == nil {
			return
		}
		if saveErr := r.saveToolApproval(existing); saveErr != nil {
			r.logger.Error("Failed to stamp the held contract snapshot",
				zap.String("server", serverName), zap.String("tool", toolName), zap.Error(saveErr))
		}
		return
	}
	contentMoved := existing.CurrentDescription != tool.Description ||
		existing.CurrentSchema != schemaJSON ||
		existing.CurrentOutputSchema != outputSchemaJSON ||
		!reflect.DeepEqual(existing.CurrentAnnotations, tool.Annotations)
	oldHash, oldDesc, oldSchema := existing.CurrentHash, existing.CurrentDescription, existing.CurrentSchema
	existing.CurrentHash = currentHash
	existing.CurrentDescription = tool.Description
	existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
	existing.CurrentSchema = schemaJSON
	existing.CurrentOutputSchema = outputSchemaJSON
	if r.storageManager == nil {
		return
	}
	if saveErr := r.saveToolApproval(existing); saveErr != nil {
		r.logger.Error("Failed to refresh the held contract snapshot",
			zap.String("server", serverName), zap.String("tool", toolName), zap.Error(saveErr))
		return
	}
	if !contentMoved {
		return
	}
	r.logger.Warn("Held tool contract changed again while awaiting confirmation",
		zap.String("server", serverName),
		zap.String("tool", toolName),
		zap.String("approved_hash", existing.ApprovedHash),
		zap.String("previous_current_hash", oldHash),
		zap.String("current_hash", currentHash))
	r.emitToolQuarantineEvent(serverName, toolName, "tool_description_changed",
		oldHash, currentHash, oldDesc, tool.Description, oldSchema, schemaJSON)
}

// ToolApprovalResult contains the result of checking tool approvals for a server.
type ToolApprovalResult struct {
	// BlockedTools is the set of tool names that should not be indexed (pending, changed, or disabled).
	BlockedTools map[string]bool
	// PendingCount is the number of newly discovered tools awaiting approval.
	PendingCount int
	// ChangedCount is the number of tools whose description/schema changed since approval.
	ChangedCount int
}

// toolQuarantineGate is the committed per-server policy for tool review.
type toolQuarantineGate struct{ autoApproveChanges bool }

func (g toolQuarantineGate) active() bool { return !g.autoApproveChanges }

// resolveToolQuarantineGate reads the canonical server under its approval lock.
func (r *Runtime) resolveToolQuarantineGate(serverName string) toolQuarantineGate {
	if r.storageManager != nil {
		if row, err := r.storageManager.GetServerRecord(serverName); err == nil {
			return toolQuarantineGate{autoApproveChanges: row.EffectiveTrustMode() == config.TrustModeAuto}
		}
	}
	for _, server := range r.Config().Servers {
		if server != nil && server.Name == serverName {
			return toolQuarantineGate{autoApproveChanges: server.EffectiveTrustMode() == config.TrustModeAuto}
		}
	}
	return toolQuarantineGate{}
}

// checkToolApprovalsHeld checks and updates the approval records of discovered
// tools (rug-pull detection included) for a caller that already holds
// serverName's approval lock (the discovery pass makes its final eligibility
// check, the approval decisions and its index writes in one hold).
func (r *Runtime) checkToolApprovalsHeld(serverName string, tools []*config.ToolMetadata) (*ToolApprovalResult, error) {
	return r.checkToolApprovalsResolved(serverName, tools)
}

// checkToolApprovalsResolved compares live definitions with the approved contract.
// A different hash needs review unless the server explicitly trusts all tools.
func (r *Runtime) checkToolApprovalsResolved(serverName string, tools []*config.ToolMetadata) (*ToolApprovalResult, error) {
	if r.storageManager == nil {
		return &ToolApprovalResult{BlockedTools: make(map[string]bool)}, nil
	}

	// Read the committed policy while holding the tool approval lock.
	gate := r.resolveToolQuarantineGate(serverName)
	autoApproveChanges := gate.autoApproveChanges
	enforceNewTools := gate.active()

	result := &ToolApprovalResult{
		BlockedTools: make(map[string]bool),
	}
	hashes := make(map[string]string, len(tools))

	for _, tool := range tools {
		// Approval identity is the exact upstream tool name.
		toolName := config.RawToolName(tool)

		// Serialize schema for hashing
		schemaJSON := tool.ParamsJSON
		if schemaJSON == "" {
			// Try to serialize from any parsed schema if available
			schemaJSON = "{}"
		}

		// Normalize JSON schemas before hashing and storage to ensure stable key ordering
		schemaJSON = normalizeJSON(schemaJSON)
		outputSchemaJSON := normalizeJSON(tool.OutputSchemaJSON)

		// Calculate current hash for the full approved contract, including output schema.
		currentHash := calculateToolApprovalHashWithOutputSchema(toolName, tool.Description, schemaJSON, outputSchemaJSON, tool.Annotations)
		hashes[toolName] = currentHash

		// Look up existing approval record
		existing, err := r.storageManager.GetToolApproval(serverName, toolName)

		if err != nil && !errors.Is(err, storage.ErrToolApprovalNotFound) {
			return nil, fmt.Errorf("read approval for %s:%s: %w", serverName, toolName, err)
		}

		if err != nil {
			autoApprove := false
			approvedBy := ""
			switch {
			case !enforceNewTools:
				autoApprove, approvedBy = true, "auto"
			case autoApproveChanges:
				autoApprove, approvedBy = true, "auto-approve-changes"
			}

			if autoApprove {
				now := time.Now().UTC()
				record := &storage.ToolApprovalRecord{
					ServerName:          serverName,
					ToolName:            toolName,
					CurrentHash:         currentHash,
					ApprovedHash:        currentHash,
					HashSchemaVersion:   storage.ToolApprovalHashSchemaVersion,
					Status:              storage.ToolApprovalStatusApproved,
					ApprovedBy:          approvedBy,
					ApprovedAt:          now,
					CurrentDescription:  tool.Description,
					CurrentAnnotations:  capturedToolAnnotations(tool.Annotations),
					CurrentSchema:       schemaJSON,
					CurrentOutputSchema: outputSchemaJSON,
				}

				if saveErr := r.saveToolApproval(record); saveErr != nil {
					r.logger.Error("Failed to save auto-approved tool record",
						zap.String("server", serverName),
						zap.String("tool", toolName),
						zap.String("approved_by", approvedBy),
						zap.Error(saveErr))
					continue
				}

				r.logger.Info("New tool discovered, auto-approved",
					zap.String("server", serverName),
					zap.String("tool", toolName),
					zap.String("approved_by", approvedBy))

				r.emitToolQuarantineEvent(serverName, toolName, "tool_auto_approved", "", currentHash,
					"", tool.Description, "", schemaJSON)

				continue
			}

			// Quarantine enabled, no baseline exemption — new tool requires user
			// review before use. This applies to trusted servers post-baseline
			// (and always to quarantined ones) to prevent injection attacks via
			// new tool additions on compromised servers.
			record := &storage.ToolApprovalRecord{
				ServerName:          serverName,
				ToolName:            toolName,
				CurrentHash:         currentHash,
				HashSchemaVersion:   storage.ToolApprovalHashSchemaVersion,
				Status:              storage.ToolApprovalStatusPending,
				CurrentDescription:  tool.Description,
				CurrentAnnotations:  capturedToolAnnotations(tool.Annotations),
				CurrentSchema:       schemaJSON,
				CurrentOutputSchema: outputSchemaJSON,
			}

			if saveErr := r.saveToolApproval(record); saveErr != nil {
				r.logger.Error("Failed to save tool approval record",
					zap.String("server", serverName),
					zap.String("tool", toolName),
					zap.Error(saveErr))
				continue
			}

			r.logger.Info("New tool discovered, pending approval",
				zap.String("server", serverName),
				zap.String("tool", toolName),
				zap.Bool("review_required", enforceNewTools))

			result.BlockedTools[toolName] = true
			result.PendingCount++

			r.emitToolQuarantineEvent(serverName, toolName, "tool_discovered", "", currentHash,
				"", tool.Description, "", schemaJSON)

			continue
		}

		if existing.Disabled {
			result.BlockedTools[toolName] = true
		}

		hashMatches := existing.ApprovedHash != "" && existing.ApprovedHash == currentHash
		if hashMatches {
			needsSave := false
			if existing.Status != storage.ToolApprovalStatusApproved {
				// Hash matches the approved contract but status is not approved
				// (the tool reverted). Restore to approved.
				if err := r.enforceInvariant(serverName, toolName, existing.Status, storage.ToolApprovalStatusApproved, ReasonHashMatch); err != nil {
					result.BlockedTools[toolName] = true
					result.ChangedCount++
					continue
				}
				existing.Status = storage.ToolApprovalStatusApproved
				existing.PreviousDescription = ""
				existing.PreviousAnnotations = nil
				existing.PreviousSchema = ""
				existing.PreviousOutputSchema = ""
				needsSave = true
				r.logger.Info("Tool restored to its approved definition",
					zap.String("server", serverName),
					zap.String("tool", toolName))
			}
			// Update current hash/description/schema in case they differ from storage
			if existing.CurrentHash != currentHash || existing.CurrentOutputSchema != outputSchemaJSON {
				existing.CurrentHash = currentHash
				existing.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
				existing.CurrentDescription = tool.Description
				existing.CurrentSchema = schemaJSON
				existing.CurrentOutputSchema = outputSchemaJSON
				needsSave = true
			}
			if !reflect.DeepEqual(existing.CurrentAnnotations, tool.Annotations) {
				existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
				needsSave = true
			}
			if needsSave {
				if saveErr := r.saveToolApproval(existing); saveErr != nil {
					r.logger.Debug("Failed to update tool approval record",
						zap.String("server", serverName),
						zap.String("tool", toolName),
						zap.Error(saveErr))
				}
			}
			continue
		}

		if existing.Status == storage.ToolApprovalStatusPending {
			// Update current info to the live snapshot.
			existing.CurrentHash = currentHash
			existing.CurrentDescription = tool.Description
			existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
			existing.CurrentSchema = schemaJSON
			existing.CurrentOutputSchema = outputSchemaJSON

			promote := false
			var promoteReason TransitionReason
			var promoteBy string
			if autoApproveChanges {
				promote, promoteReason, promoteBy = true, ReasonAutoApproveChanges, "auto-approve-changes"
			}

			if promote {
				if invErr := r.enforceInvariant(serverName, toolName, existing.Status, storage.ToolApprovalStatusApproved, promoteReason); invErr != nil {
					// Refuse to promote on an invariant violation — keep blocked.
					if saveErr := r.saveToolApproval(existing); saveErr != nil {
						r.logger.Debug("Failed to update pending tool approval",
							zap.String("server", serverName), zap.String("tool", toolName), zap.Error(saveErr))
					}
					if enforceNewTools {
						result.BlockedTools[toolName] = true
						result.PendingCount++
					}
					continue
				}
				existing.Status = storage.ToolApprovalStatusApproved
				existing.ApprovedHash = currentHash
				existing.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
				existing.ApprovedAt = time.Now().UTC()
				existing.ApprovedBy = promoteBy
				existing.PreviousDescription = ""
				existing.PreviousAnnotations = nil
				existing.PreviousSchema = ""
				existing.PreviousOutputSchema = ""
				if saveErr := r.saveToolApproval(existing); saveErr != nil {
					r.logger.Error("Failed to promote pending tool approval",
						zap.String("server", serverName), zap.String("tool", toolName),
						zap.String("approved_by", promoteBy), zap.Error(saveErr))
					continue
				}
				r.logger.Info("Pending tool promoted to approved",
					zap.String("server", serverName), zap.String("tool", toolName),
					zap.String("approved_by", promoteBy))
				r.emitToolQuarantineEvent(serverName, toolName, "tool_auto_approved", "", currentHash,
					"", tool.Description, "", schemaJSON)
				continue
			}

			// Stays pending — persist the updated current info.
			if saveErr := r.saveToolApproval(existing); saveErr != nil {
				r.logger.Debug("Failed to update pending tool approval",
					zap.String("server", serverName),
					zap.String("tool", toolName),
					zap.Error(saveErr))
			}

			if enforceNewTools {
				result.BlockedTools[toolName] = true
				result.PendingCount++
			}
			continue
		}

		// A changed tool is restored only if the WHOLE contract — description
		// AND input/output schemas — matches the APPROVED version recorded in
		// Previous*, not the current (changed) one.
		if existing.Status == storage.ToolApprovalStatusChanged {
			// Only restore if the tool reverted to the PREVIOUS (approved) contract
			if revertedToPreviousContract(existing, tool.Description, schemaJSON, outputSchemaJSON, tool.Annotations, currentHash) {
				if err := r.enforceInvariant(serverName, toolName, existing.Status, storage.ToolApprovalStatusApproved, ReasonDescriptionRevert); err != nil {
					result.BlockedTools[toolName] = true
					result.ChangedCount++
					continue
				}
				existing.Status = storage.ToolApprovalStatusApproved
				existing.ApprovedHash = currentHash
				existing.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
				existing.CurrentHash = currentHash
				existing.CurrentDescription = tool.Description
				existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
				existing.CurrentSchema = schemaJSON
				existing.CurrentOutputSchema = outputSchemaJSON
				existing.PreviousDescription = ""
				existing.PreviousAnnotations = nil
				existing.PreviousSchema = ""
				existing.PreviousOutputSchema = ""
				if saveErr := r.saveToolApproval(existing); saveErr == nil {
					r.logger.Info("Changed tool restored (reverted to previous contract)",
						zap.String("server", serverName),
						zap.String("tool", toolName))
				}
				continue
			}
			if autoApproveChanges {
				if invErr := r.enforceInvariant(serverName, toolName, existing.Status, storage.ToolApprovalStatusApproved, ReasonAutoApproveChanges); invErr != nil {
					result.BlockedTools[toolName] = true
					result.ChangedCount++
					continue
				}
				existing.Status = storage.ToolApprovalStatusApproved
				existing.ApprovedHash = currentHash
				existing.CurrentHash = currentHash
				existing.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
				existing.ApprovedAt = time.Now().UTC()
				existing.ApprovedBy = "auto-approve-changes"
				existing.CurrentDescription = tool.Description
				existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
				existing.CurrentSchema = schemaJSON
				existing.CurrentOutputSchema = outputSchemaJSON
				existing.PreviousDescription = ""
				existing.PreviousAnnotations = nil
				existing.PreviousSchema = ""
				existing.PreviousOutputSchema = ""
				if saveErr := r.saveToolApproval(existing); saveErr == nil {
					r.logger.Info("Changed tool auto-approved (trust_mode auto)",
						zap.String("server", serverName),
						zap.String("tool", toolName))
					r.emitToolQuarantineEvent(serverName, toolName, "tool_auto_approved", "", currentHash,
						"", tool.Description, "", schemaJSON)
				}
				continue
			}
			// The held contract moved on again (A → held B → held C): refresh
			// the Current* snapshot so review output and the ingested-contract
			// scan input describe what the server
			// serves now. ApprovedHash and the Previous* evidence of the
			// approved contract stay untouched, so the record stays changed
			// and a revert to the approved contract is still recognised.
			r.refreshHeldContractSnapshot(serverName, toolName, existing, tool, schemaJSON, outputSchemaJSON, currentHash)
			result.BlockedTools[toolName] = true
			result.ChangedCount++
			continue
		}

		if existing.ApprovedHash != "" && !hashMatches {
			hintsMatch := safetyHintsFingerprint(existing.CurrentAnnotations) == safetyHintsFingerprint(tool.Annotations)
			if autoApproveChanges {
				if invErr := r.enforceInvariant(serverName, toolName, existing.Status, storage.ToolApprovalStatusApproved, ReasonAutoApproveChanges); invErr != nil {
					result.BlockedTools[toolName] = true
					result.ChangedCount++
					continue
				}
				existing.Status = storage.ToolApprovalStatusApproved
				existing.ApprovedHash = currentHash
				existing.CurrentHash = currentHash
				existing.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
				existing.ApprovedAt = time.Now().UTC()
				existing.ApprovedBy = "auto-approve-changes"
				existing.CurrentDescription = tool.Description
				existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
				existing.CurrentSchema = schemaJSON
				existing.CurrentOutputSchema = outputSchemaJSON
				existing.PreviousDescription = ""
				existing.PreviousAnnotations = nil
				existing.PreviousSchema = ""
				existing.PreviousOutputSchema = ""
				if saveErr := r.saveToolApproval(existing); saveErr != nil {
					r.logger.Error("Failed to auto-approve changed tool",
						zap.String("server", serverName),
						zap.String("tool", toolName),
						zap.Error(saveErr))
					continue
				}
				r.logger.Info("Tool change auto-approved (trust_mode auto)",
					zap.String("server", serverName),
					zap.String("tool", toolName))
				r.emitToolQuarantineEvent(serverName, toolName, "tool_auto_approved", "", currentHash,
					"", tool.Description, "", schemaJSON)
				continue
			}

			// Unless trust_mode is auto, a genuine tool change (rug pull) is
			// always held for human review; a clean scan verdict does not
			// approve it, because paraphrased pulls scan clean.

			oldDesc := existing.CurrentDescription
			oldSchema := existing.CurrentSchema
			oldOutputSchema := existing.CurrentOutputSchema
			if existing.Status == storage.ToolApprovalStatusApproved {
				// Transitioning from approved to changed
				oldDesc = existing.CurrentDescription
				oldSchema = existing.CurrentSchema
				oldOutputSchema = existing.CurrentOutputSchema
			}

			existing.Status = storage.ToolApprovalStatusChanged
			existing.PreviousDescription = oldDesc
			existing.PreviousAnnotations = cloneToolAnnotations(existing.CurrentAnnotations)
			if existing.PreviousAnnotations == nil {
				// The approved contract's hints were hashed: nil means "all
				// defaults", which the revert check must still compare.
				existing.PreviousAnnotations = &config.ToolAnnotations{}
			}
			existing.PreviousSchema = oldSchema
			existing.PreviousOutputSchema = oldOutputSchema
			existing.CurrentHash = currentHash
			existing.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
			existing.CurrentDescription = tool.Description
			existing.CurrentAnnotations = cloneToolAnnotations(tool.Annotations)
			existing.CurrentSchema = schemaJSON
			existing.CurrentOutputSchema = outputSchemaJSON

			if saveErr := r.saveToolApproval(existing); saveErr != nil {
				r.logger.Error("Failed to update changed tool approval",
					zap.String("server", serverName),
					zap.String("tool", toolName),
					zap.Error(saveErr))
				continue
			}

			r.logger.Warn("Tool description/schema changed since approval (potential rug pull)",
				zap.String("server", serverName),
				zap.String("tool", toolName),
				zap.String("approved_hash", existing.ApprovedHash),
				zap.String("current_hash", currentHash),
				zap.Bool("annotation_hints_changed", !hintsMatch),
				zap.Bool("review_required", enforceNewTools))

			// Always block changed tools when quarantine is globally enabled,
			// even for trusted (non-quarantined) servers.
			// Rug pull detection is a critical security feature.
			result.BlockedTools[toolName] = true
			result.ChangedCount++

			// Emit activity event for description change
			r.emitToolQuarantineEvent(serverName, toolName, "tool_description_changed",
				existing.ApprovedHash, currentHash,
				oldDesc, tool.Description,
				oldSchema, schemaJSON)
		}
	}

	if len(result.BlockedTools) > 0 {
		r.logger.Info("Tool-level quarantine: tools blocked",
			zap.String("server", serverName),
			zap.Int("pending", result.PendingCount),
			zap.Int("changed", result.ChangedCount),
			zap.Int("total_blocked", len(result.BlockedTools)))
	}
	if r.reviewQueueChanged(serverName, result.BlockedTools, hashes) {
		r.toolsChanged(serverName)
	}

	return result, nil
}

// reviewQueueChanged records the server's blocked tools and reports whether
// they differ from the last pass, so repeated discovery of the same queue is
// silent.
func (r *Runtime) reviewQueueChanged(serverName string, blocked map[string]bool, hashes map[string]string) bool {
	names := make([]string, 0, len(blocked))
	for name := range blocked {
		names = append(names, name+"="+hashes[name])
	}
	sort.Strings(names)
	sig := strings.Join(names, "\n")
	previous, seen := r.reviewQueueSigs.Swap(serverName, sig)
	if !seen {
		return sig != ""
	}
	return previous.(string) != sig
}

func revertedToPreviousContract(existing *storage.ToolApprovalRecord, description, schemaJSON, outputSchemaJSON string, annotations *config.ToolAnnotations, currentHash string) bool {
	return existing.ApprovedHash != "" && existing.ApprovedHash == currentHash
}

func (r *Runtime) saveToolApproval(record *storage.ToolApprovalRecord) error {
	return r.storageManager.SaveToolApproval(record)
}

// cloneToolAnnotations preserves a point-in-time upstream snapshot. Annotation
// pointers are optional and callers may reuse them across discovery passes, so
// approval records must not retain aliases to mutable boolean values.
func cloneToolAnnotations(annotations *config.ToolAnnotations) *config.ToolAnnotations {
	if annotations == nil {
		return nil
	}
	clone := *annotations
	if annotations.ReadOnlyHint != nil {
		value := *annotations.ReadOnlyHint
		clone.ReadOnlyHint = &value
	}
	if annotations.DestructiveHint != nil {
		value := *annotations.DestructiveHint
		clone.DestructiveHint = &value
	}
	if annotations.IdempotentHint != nil {
		value := *annotations.IdempotentHint
		clone.IdempotentHint = &value
	}
	if annotations.OpenWorldHint != nil {
		value := *annotations.OpenWorldHint
		clone.OpenWorldHint = &value
	}
	return &clone
}

// capturedToolAnnotations marks a definition written by this runtime as
// captured even when its upstream omitted annotation hints. Historical stored
// nil remains meaningful to reviewTier as "unknown"; only a newly captured
// definition gets the non-nil empty record that means "unannotated".
func capturedToolAnnotations(annotations *config.ToolAnnotations) *config.ToolAnnotations {
	if annotations == nil {
		return &config.ToolAnnotations{}
	}
	return cloneToolAnnotations(annotations)
}

func (r *Runtime) serverEligibleForIndexing(serverName string) bool {
	for _, candidate := range r.Config().Servers {
		if candidate.Name == serverName {
			return candidate.Enabled
		}
	}
	return false
}

// applyServerDiffIfEligibleGuarded is applyServerDiffIfEligible for the
// inventory inv, with the under-lock stale check of
// applyDifferentialToolUpdateGuarded. An inventory the capture-order gate
// refuses writes nothing and reports false.
func (r *Runtime) applyServerDiffIfEligibleGuarded(ctx context.Context, serverName string, tools []*config.ToolMetadata, instanceGen int64, inv inventoryRef, stale func() bool) bool {
	if !r.serverEligibleForIndexing(serverName) {
		r.logger.Info("Skipping sweep index write for disabled server",
			zap.String("server", serverName))
		return false
	}
	if err := r.applyInventoryGuarded(ctx, serverName, tools, instanceGen, inv, stale, nil); err != nil {
		if errors.Is(err, errLocalSnapshotPurged) || errors.Is(err, errDiscoveryStaleGeneration) || errors.Is(err, errDiscoveryIneligible) || errors.Is(err, errInventoryStale) || errors.Is(err, errInventoryNotCurrent) {
			return false
		}
		r.logger.Error("Failed to apply differential update for server during sweep",
			zap.String("server", serverName),
			zap.Error(err))
		return false
	}
	return true
}

// reindexServerToolsAfterApprovalChange reconciles the search index with a
// just-mutated set of tool-approval records for one server.
// Approval mutations (ApproveTools / BlockTools / SetToolEnabled and their bulk
// variants) only write encrypted records and emit events; indexing otherwise
// happens solely in applyDifferentialToolUpdate, reached from discovery (boot
// sweep, periodic sweep, connect, tools/list_changed). Without this an approved
// tool is callable immediately (call-time gates read encrypted live) but stays
// invisible to retrieve_tools/describe_tool until the next sweep, and a
// blocked/disabled tool lingers in the index just as long.
//
// Callers invoke this in a goroutine so request handlers return fast.
func (r *Runtime) reindexServerToolsAfterApprovalChange(serverName string) {
	if r.indexManager == nil || r.storageManager == nil {
		return
	}

	if !r.serverEligibleForIndexing(serverName) {
		return
	}

	// Prefer the last-good snapshot so the reindex is a pure local diff with no
	// network round-trip. It is populated by the full sweep and by
	// DiscoverAndIndexToolsForServer. Selection and application happen under one
	// captureSnapMu hold so a concurrent capture cannot be overwritten by a
	// stale snapshot (which would look like a revert and clear a changed hold).
	if r.reindexFromLastGoodSnapshot(serverName) {
		return
	}

	// No snapshot captured yet — fall back to a full single-server
	// rediscover, which indexes and populates the snapshot for next time.
	// Use the authoritative path so a server now reporting zero tools has
	// stale entries cleared rather than resurfaced.
	if err := r.RefreshServerTools(r.AppContext(), serverName); err != nil {
		r.logger.Debug("Approval-driven rediscovery failed",
			zap.String("server", serverName), zap.Error(err))
	}
}

// reindexFromLastGoodSnapshot re-runs the differential update from the
// last-good snapshot under captureSnapMu. It reports false when no snapshot
// exists yet, or when the snapshot no longer
// describes the live connection — its client was replaced, retired or
// reconnected, or a newer capture with definitions was applied — so the caller
// re-lists the server instead of re-applying older definitions.
func (r *Runtime) reindexFromLastGoodSnapshot(serverName string) bool {
	r.captureSnapMu.Lock()
	defer r.captureSnapMu.Unlock()

	snapshot, inv, seq, snapshotGen := r.lastGoodSnapshotWithCaptureGen(serverName)
	if len(snapshot) == 0 {
		return false
	}

	if !r.serverEligibleForIndexing(serverName) {
		return true
	}

	// Re-run the differential update against the current approval records.
	// Rug-pull safety is preserved: if the tool mutated after approval,
	// checkToolApprovalsHeld re-flags it changed and it stays blocked.
	// The snapshot is bound to the commit generation of the instance it was
	// listed from. After an endpoint change it is rejected by the final
	// check, and the caller re-lists the current instance instead of
	// reapplying the previous endpoint's tools.
	stale := func() bool { return r.lastGoodPurgedSince(serverName, seq) }
	err := r.applyInventoryGuarded(r.AppContext(), serverName, snapshot, snapshotGen, inventoryRef{capture: inv, mode: inventoryReconcile}, stale, nil)
	switch {
	case errors.Is(err, errDiscoveryStaleGeneration):
		r.logger.Info("Last-good snapshot belongs to a superseded instance generation; re-listing the server",
			zap.String("server", serverName), zap.Int64("snapshot_generation", snapshotGen))
		return false
	case errors.Is(err, errInventoryNotCurrent), errors.Is(err, errInventoryStale):
		r.logger.Info("Approval reindex re-lists the server: its last-good snapshot no longer describes the live connection",
			zap.String("server", serverName))
		return false
	case err != nil && !errors.Is(err, errLocalSnapshotPurged) && !errors.Is(err, errDiscoveryIneligible):
		r.logger.Warn("Failed to reindex tools after approval change",
			zap.String("server", serverName), zap.Error(err))
	}
	return true
}

// approveToolsLocked approves the named tools. The caller holds the server's
// tool-approval lock. Each record is re-read here, under the lock, so the
// decision is made on the current record, never on a listing taken earlier.
// With pendingOnly (server-approval baseline trust) only records still
// "pending" are promoted: a record that turned "changed" (rug-pull) is left
// for explicit review, and approved records are not rewritten. Missing
// records are skipped. Returns the number of records written approved.
func (r *Runtime) approveToolsLocked(serverName string, toolNames []string, approvedBy string, pendingOnly bool) (int, error) {
	approved := 0
	for _, toolName := range toolNames {
		record, err := r.storageManager.GetToolApproval(serverName, toolName)
		if err != nil {
			r.logger.Warn("Tool approval record not found for approval",
				zap.String("server", serverName),
				zap.String("tool", toolName),
				zap.Error(err))
			continue
		}
		if pendingOnly && record.Status != storage.ToolApprovalStatusPending {
			continue
		}

		if err := r.enforceInvariant(serverName, toolName, record.Status, storage.ToolApprovalStatusApproved, ReasonUserApprove); err != nil {
			return approved, err
		}

		record.Status = storage.ToolApprovalStatusApproved
		record.ApproveCurrentHash()
		record.ApprovedAt = time.Now().UTC()
		record.ApprovedBy = approvedBy
		record.PreviousDescription = ""
		record.PreviousAnnotations = nil
		record.PreviousSchema = ""
		record.PreviousOutputSchema = ""

		if err := r.saveToolApproval(record); err != nil {
			return approved, err
		}
		approved++

		r.logger.Info("Tool approved",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.String("approved_by", approvedBy))

		// Emit activity event
		r.emitToolQuarantineEvent(serverName, toolName, "tool_approved",
			"", record.ApprovedHash, "", record.CurrentDescription, "", record.CurrentSchema)
	}
	return approved, nil
}

// notifyToolsApproved publishes the approval to SSE subscribers and reindexes.
// Called WITHOUT the tool-approval lock held.
func (r *Runtime) notifyToolsApproved(serverName string, approved int, approvedBy string) {
	if approved > 0 {
		r.toolsChanged(serverName)
		// Index the newly approved tools now instead of waiting for
		// the next discovery sweep. Async so the caller returns fast. Also covers
		// ApproveAllTools and approveBaselineToolsForServer, which delegate here.
		go r.reindexServerToolsAfterApprovalChange(serverName)
	}
}

func (r *Runtime) setToolEnabledNoEmit(serverName, toolName string, enabled bool, updatedBy string, inc int64) (bool, error) {
	if r.storageManager == nil {
		return false, nil
	}
	defer r.lockToolApprovals(serverName)()
	if err := r.checkApprovalIncarnationHeld(serverName, inc); err != nil {
		return false, err
	}

	record, err := r.storageManager.GetToolApproval(serverName, toolName)
	switch {
	case err == nil:
		// existing record — keep its Status, just flip Disabled below.
	case errors.Is(err, storage.ErrToolApprovalNotFound):
		// First time we've seen this tool: synthesize the record the
		// discovery producer would have filed for it under this server's
		// gate (see the function comment).
		record = r.newToggleSynthesizedRecord(serverName, toolName, updatedBy, inc)
	default:
		// Real read error — refuse to write. See comment above for rationale.
		return false, fmt.Errorf("read tool approval %s:%s: %w", serverName, toolName, err)
	}

	if record.Disabled == !enabled {
		// Already in the desired state — no write, no audit event, no SSE.
		// Matches the prior bulk-path pre-check; lifted here so single-toggle
		// also avoids no-op encrypted writes.
		return false, nil
	}

	record.Disabled = !enabled

	if err := r.saveToolApproval(record); err != nil {
		return false, err
	}

	action := "tool_enabled"
	if !enabled {
		action = "tool_disabled"
	}

	r.emitToolQuarantineEvent(serverName, toolName, action,
		record.ApprovedHash, record.CurrentHash,
		"", record.CurrentDescription,
		"", record.CurrentSchema)

	return true, nil
}

// newToggleSynthesizedRecord builds the record setToolEnabledNoEmit files for
// a record-less tool (see its function comment): pending under an active
// tool-level quarantine gate, approved and baselined to the snapshot's current
// contract under a lifted one. The caller sets Disabled.
func (r *Runtime) newToggleSynthesizedRecord(serverName, toolName, updatedBy string, inc int64) *storage.ToolApprovalRecord {
	record := &storage.ToolApprovalRecord{
		ServerName: serverName,
		ToolName:   toolName,
	}
	// The snapshot's current contract, when the StateView lists the raw name.
	if tool := r.snapshotToolMetadata(serverName, toolName, inc); tool != nil {
		schemaJSON := tool.ParamsJSON
		if schemaJSON == "" {
			schemaJSON = "{}"
		}
		schemaJSON = normalizeJSON(schemaJSON)
		outputSchemaJSON := normalizeJSON(tool.OutputSchemaJSON)
		record.CurrentHash = calculateToolApprovalHashWithOutputSchema(toolName, tool.Description, schemaJSON, outputSchemaJSON, tool.Annotations)
		record.HashSchemaVersion = storage.ToolApprovalHashSchemaVersion
		record.CurrentDescription = tool.Description
		record.CurrentSchema = schemaJSON
		record.CurrentOutputSchema = outputSchemaJSON
		record.CurrentAnnotations = capturedToolAnnotations(tool.Annotations)
	}

	if r.resolveToolQuarantineGate(serverName).active() {
		record.Status = storage.ToolApprovalStatusPending
		r.logger.Info("Tool toggled before any approval record existed; filed pending under an active quarantine gate until approved by name",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.String("updated_by", updatedBy))
		return record
	}
	record.Status = storage.ToolApprovalStatusApproved
	record.ApproveCurrentHash()
	record.ApprovedAt = time.Now().UTC()
	record.ApprovedBy = updatedBy
	return record
}

// viewToolsAt returns the StateView's tools for serverName when the view was
// reconciled from incarnation inc, and nil otherwise. The StateView lags the commit: after a remove and
// same-name re-add it can still hold the removed server's tools under the
// removed incarnation, and an approval writer bound to the replacement must
// never persist those definitions as the replacement's records. inc == 0 is
// an unbound capture and accepts the view.
func (r *Runtime) viewToolsAt(serverName string, inc int64) []stateview.ToolInfo {
	if r.supervisor == nil {
		return nil
	}
	snapshot := r.supervisor.StateView().Snapshot()
	if snapshot == nil {
		return nil
	}
	status, ok := snapshot.Servers[serverName]
	if !ok || status == nil {
		return nil
	}
	if inc != 0 && status.Incarnation != inc {
		r.logger.Debug("StateView lags the bound incarnation; not reading its tools for an approval write",
			zap.String("server", serverName),
			zap.Int64("view_incarnation", status.Incarnation),
			zap.Int64("bound_incarnation", inc))
		return nil
	}
	return status.Tools
}

// snapshotToolMetadata returns the StateView's current contract for a raw
// tool name on a server — the same fields the discovery producer hashes —
// or nil when the snapshot does not list the exact name. The StateView holds
// raw names verbatim, so only the exact name matches.
// inc is the incarnation the writer is bound to: a view reconciled from any
// other incarnation is not consulted (see viewToolsAt).
func (r *Runtime) snapshotToolMetadata(serverName, toolName string, inc int64) *config.ToolMetadata {
	viewTools := r.viewToolsAt(serverName, inc)
	for i := range viewTools {
		info := viewTools[i]
		if info.Name != toolName {
			continue
		}
		paramsJSON := ""
		if info.InputSchema != nil {
			if raw, err := json.Marshal(info.InputSchema); err == nil {
				paramsJSON = string(raw)
			}
		}
		return &config.ToolMetadata{
			ServerName:       serverName,
			Name:             info.Name,
			RawName:          info.Name,
			Description:      info.Description,
			ParamsJSON:       paramsJSON,
			OutputSchemaJSON: info.OutputSchemaJSON,
			Annotations:      info.Annotations,
		}
	}
	return nil
}

// SetToolEnabledAt is SetToolEnabled bound to expectedIncarnation (0 binds to
// the incarnation current when the call starts).
func (r *Runtime) SetToolEnabledAt(serverName, toolName string, enabled bool, updatedBy string, expectedIncarnation int64) error {
	if r.storageManager == nil {
		return nil
	}
	inc, err := r.CaptureApprovalIncarnation(serverName, expectedIncarnation)
	if err != nil {
		return err
	}
	changed, err := r.setToolEnabledNoEmit(serverName, toolName, enabled, updatedBy, inc)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	r.toolsChanged(serverName)

	// Reconcile the index with the new visibility state: a disabled
	// tool is removed, a re-enabled tool re-indexed.
	go r.reindexServerToolsAfterApprovalChange(serverName)

	return nil
}

// BlockToolsAt is BlockTools bound to expectedIncarnation (0 binds to the
// incarnation current when the call starts).
func (r *Runtime) BlockToolsAt(serverName string, toolNames []string, blockedBy string, expectedIncarnation int64) (int, error) {
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
	blocked, err := r.blockToolsLocked(serverName, toolNames, blockedBy)
	unlock()
	r.notifyToolsBlocked(serverName, blocked, blockedBy)
	return blocked, err
}

// blockToolsLocked is BlockTools' record loop. The caller holds the server's
// tool-approval lock; each record is re-read under it.
func (r *Runtime) blockToolsLocked(serverName string, toolNames []string, blockedBy string) (int, error) {
	blocked := 0
	for _, toolName := range toolNames {
		record, err := r.storageManager.GetToolApproval(serverName, toolName)
		if err != nil {
			r.logger.Warn("Tool approval record not found for block",
				zap.String("server", serverName),
				zap.String("tool", toolName),
				zap.Error(err))
			continue
		}

		// The approve half of a block is a user action, so the pending/changed
		// → approved transition is permitted by the quarantine invariant.
		if err := r.enforceInvariant(serverName, toolName, record.Status, storage.ToolApprovalStatusApproved, ReasonUserApprove); err != nil {
			return blocked, err
		}

		// Approve + disable in a single write — all-or-nothing.
		record.Status = storage.ToolApprovalStatusApproved
		record.ApproveCurrentHash()
		record.ApprovedAt = time.Now().UTC()
		record.ApprovedBy = blockedBy
		record.PreviousDescription = ""
		record.PreviousAnnotations = nil
		record.PreviousSchema = ""
		record.PreviousOutputSchema = ""
		record.Disabled = true

		if err := r.saveToolApproval(record); err != nil {
			return blocked, err
		}
		blocked++

		r.logger.Info("Tool blocked (approved + disabled)",
			zap.String("server", serverName),
			zap.String("tool", toolName),
			zap.String("blocked_by", blockedBy))

		// Single per-tool audit event describing the block as one action.
		r.emitToolQuarantineEvent(serverName, toolName, "tool_blocked",
			"", record.ApprovedHash, "", record.CurrentDescription, "", record.CurrentSchema)
	}
	return blocked, nil
}

// notifyToolsBlocked publishes a block to SSE subscribers and reindexes.
// Called WITHOUT the tool-approval lock held.
func (r *Runtime) notifyToolsBlocked(serverName string, blocked int, blockedBy string) {
	// One SSE emit per call (not per tool) so an open Servers/overview page
	// refreshes its quarantine badge — mirrors ApproveTools.
	if blocked > 0 {
		r.toolsChanged(serverName)
		// Evict the now-disabled tools from the index promptly.
		// Covers BlockAllTools, which delegates here.
		go r.reindexServerToolsAfterApprovalChange(serverName)
	}
}

// BlockAllToolsAt is BlockAllTools bound to expectedIncarnation (0 binds to
// the incarnation current when the call starts).
func (r *Runtime) BlockAllToolsAt(serverName string, blockedBy string, expectedIncarnation int64) (int, error) {
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
	var toolNames []string
	for _, record := range records {
		if record.Status == storage.ToolApprovalStatusPending || record.Status == storage.ToolApprovalStatusChanged {
			toolNames = append(toolNames, record.ToolName)
		}
	}
	blocked := 0
	if len(toolNames) > 0 {
		blocked, err = r.blockToolsLocked(serverName, toolNames, blockedBy)
	}
	unlock()
	r.notifyToolsBlocked(serverName, blocked, blockedBy)
	return blocked, err
}

// emitToolQuarantineEvent emits an activity event for tool quarantine changes.
func (r *Runtime) emitToolQuarantineEvent(serverName, toolName, action, oldHash, newHash, oldDesc, newDesc, oldSchema, newSchema string) {
	metadata := map[string]interface{}{
		"action":    action,
		"tool_name": toolName,
	}
	if oldHash != "" {
		metadata["old_hash"] = oldHash
	}
	if newHash != "" {
		metadata["new_hash"] = newHash
	}
	// Truncate descriptions at 64KB for storage
	const maxDescLen = 64 * 1024
	if oldDesc != "" {
		if len(oldDesc) > maxDescLen {
			oldDesc = oldDesc[:maxDescLen]
		}
		metadata["old_description"] = oldDesc
	}
	if newDesc != "" {
		if len(newDesc) > maxDescLen {
			newDesc = newDesc[:maxDescLen]
		}
		metadata["new_description"] = newDesc
	}
	if oldSchema != "" {
		if len(oldSchema) > maxDescLen {
			oldSchema = oldSchema[:maxDescLen]
		}
		metadata["old_schema"] = oldSchema
	}
	if newSchema != "" {
		if len(newSchema) > maxDescLen {
			newSchema = newSchema[:maxDescLen]
		}
		metadata["new_schema"] = newSchema
	}

	// Marshal metadata to JSON string for the event payload
	metadataJSON, _ := json.Marshal(metadata)

	payload := map[string]any{
		"server_name": serverName,
		"tool_name":   toolName,
		"action":      action,
		"metadata":    string(metadataJSON),
	}
	r.publishEvent(newEvent(EventTypeActivityToolQuarantineChange, payload))
}
