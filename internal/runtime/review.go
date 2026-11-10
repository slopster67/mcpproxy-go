package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/contracts"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

var ErrReviewServerNotFound = errors.New("review server not found")

// ReviewQueue is the composed, server-level review list shared by REST, CLI,
// MCP and native clients.
type ServerReview struct {
	Server ReviewServer `json:"server"`
	Tools  []ReviewTool `json:"tools"`
}

type ReviewServer struct {
	Name       string            `json:"name"`
	Transport  string            `json:"transport"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	WorkingDir string            `json:"working_dir,omitempty"`
	URL        string            `json:"url,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`

	TrustMode           string `json:"trust_mode"`
	DefinitionsCaptured bool   `json:"definitions_captured"`
	// LiveToolCount and LastCaptureAt are the fresh-capture proof: how many of
	// the stored records the upstream listed in its most recent capture, and
	// when that capture ran. They are separate from the stored-record
	// inventory (Tools), which retains operator decisions for absent tools.
	LiveToolCount int        `json:"live_tool_count,omitempty"`
	LastCaptureAt *time.Time `json:"last_capture_at,omitempty"`
	Generation    int64      `json:"generation,omitempty"`
	Incarnation   int64      `json:"incarnation,omitempty"`
}

type ReviewTool struct {
	Classification     string                  `json:"classification,omitempty"`
	ClassificationHash string                  `json:"classification_hash,omitempty"`
	Name               string                  `json:"name"`
	Description        string                  `json:"description"`
	InputSchema        json.RawMessage         `json:"input_schema"`
	OutputSchema       json.RawMessage         `json:"output_schema"`
	Annotations        *config.ToolAnnotations `json:"annotations"`
	Tier               contracts.Tier          `json:"tier"`
	ApprovalStatus     string                  `json:"approval_status"`
	Disabled           bool                    `json:"disabled"`
	// DefaultAllowed is the review screens' fail-closed default selection.
	DefaultAllowed bool                `json:"default_allowed"`
	Previous       *ReviewToolPrevious `json:"previous"`
	Diff           *ReviewToolDiff     `json:"diff,omitempty"`
	// CurrentHash is the review fingerprint of this tool, not the storage
	// CurrentHash: send it back in expected_hashes
	// to bind an approval to this review. Opaque; compare only with a later
	// review. The /tools/{tool}/diff payload's current_hash is a different
	// value (the storage hash).
	CurrentHash string `json:"current_hash,omitempty"`
}

type ReviewToolPrevious struct {
	Description  string                  `json:"description"`
	InputSchema  json.RawMessage         `json:"input_schema"`
	OutputSchema json.RawMessage         `json:"output_schema"`
	Annotations  *config.ToolAnnotations `json:"annotations"`
}

type ReviewToolDiff struct {
	Description  string `json:"description"`
	InputSchema  string `json:"input_schema"`
	OutputSchema string `json:"output_schema"`
	Annotations  string `json:"annotations"`
}

// GetServerReview returns a caller-independent review view. Server command,
// URL, environment, header, and argument values are redacted unconditionally.
func (r *Runtime) GetServerReview(ctx context.Context, serverName string) (*ServerReview, error) {
	// The displayed configuration and the incarnation come from ONE
	// committed snapshot. Reading them separately lets a same-name
	// remove/re-add land in between, and the review would show the removed
	// server's command or URL with the replacement's incarnation, which the
	// client echoes back as expected_incarnation.
	server, gen, hasGen := r.committedServerView(serverName)
	if server == nil {
		return nil, fmt.Errorf("%w: %q", ErrReviewServerNotFound, serverName)
	}

	contractServer := contracts.Server{ServerConfig: config.ServerConfig{
		Name: server.Name, Protocol: server.Protocol, Command: server.Command,
		Args: append([]string(nil), server.Args...), WorkingDir: server.WorkingDir,
		URL: server.URL, Env: copyStringMap(server.Env), Headers: copyStringMap(server.Headers),
	}}
	// Redaction.Argv treats a whole `sh -c "<command line>"` element as one leaf,
	// so a low-entropy secret inside it survives. The review payload is display
	// only (never echoed back through a write door), so it can afford the
	// token-level command-string rule that the shared read doors must not take.
	// It runs on the RAW element, before the generic pass: that pass can cut a
	// quoted inline value (--password='a b') mid-quote, leaving unbalanced
	// quoting that the token splitter then cannot group.
	for i, arg := range contractServer.Args {
		// Every element, not only whitespace-bearing ones: `API_KEY=@x;npx` is a
		// whole command line with no whitespace in it.
		contractServer.Args[i] = oauth.LiveRedaction.CommandString(arg)
	}
	oauth.RedactServerSecretFields(&contractServer)
	reviewServer := ReviewServer{
		Name: contractServer.Name, Transport: reviewTransport(server.Protocol),
		Command: contractServer.Command, Args: contractServer.Args, WorkingDir: contractServer.WorkingDir,
		URL: contractServer.URL, Env: contractServer.Env, Headers: contractServer.Headers,
		TrustMode: string(server.EffectiveTrustMode()),
	}
	if hasGen {
		reviewServer.Generation, reviewServer.Incarnation = gen.Gen, gen.Incarnation
	}
	// Stored records and the live capture state are published by different
	// writers. A capture holds captureSnapMu for persist + prune + stamp, so
	// reading both under the read lock yields one generation, never a mix.
	r.captureSnapMu.RLock()
	records, err := r.storageManager.ListToolApprovals(serverName)
	if err != nil {
		r.captureSnapMu.RUnlock()
		return nil, fmt.Errorf("list tool reviews for %q: %w", serverName, err)
	}
	r.lastGoodToolsMu.RLock()
	if stamp, ok := r.lastCaptureAt[serverName]; ok {
		at := stamp
		reviewServer.LastCaptureAt = &at
	}
	liveNames := make(map[string]struct{}, len(r.lastGoodTools[serverName]))
	for _, t := range r.lastGoodTools[serverName] {
		liveNames[config.RawToolName(t)] = struct{}{}
	}
	r.lastGoodToolsMu.RUnlock()
	r.captureSnapMu.RUnlock()
	for _, rec := range records {
		if _, ok := liveNames[rec.ToolName]; ok {
			reviewServer.LiveToolCount++
		}
	}
	reviewServer.DefinitionsCaptured = len(records) > 0
	result := &ServerReview{Server: reviewServer, Tools: make([]ReviewTool, 0, len(records))}
	if !reviewServer.DefinitionsCaptured {
		return result, nil
	}
	for _, record := range records {
		tool := ReviewTool{
			Name: record.ToolName, Description: record.CurrentDescription,
			InputSchema: rawSchema(record.CurrentSchema), OutputSchema: rawSchema(record.CurrentOutputSchema),
			Annotations: cloneToolAnnotations(record.CurrentAnnotations), Tier: reviewTier(record.CurrentAnnotations),
			ApprovalStatus: record.Status, Disabled: record.Disabled,
		}
		if ac := auth.AuthContextFromContext(ctx); ac.CanManageMCPProxy() {
			tool.ClassificationHash = record.CurrentHash
		}
		if r.storageManager != nil {
			if tier, err := r.storageManager.GetToolClassificationAtHash(serverName, record.ToolName, record.CurrentHash); err == nil && tier != "" {
				tool.Classification = tier
				tool.Tier = contracts.Tier(tier)
			}
		}
		tool.DefaultAllowed = reviewDefaultAllowed(tool)
		tool.CurrentHash = reviewFingerprint(record)
		if record.PreviousDescription != "" || record.PreviousSchema != "" || record.PreviousOutputSchema != "" || record.PreviousAnnotations != nil {
			tool.Previous = &ReviewToolPrevious{
				Description: record.PreviousDescription, InputSchema: rawSchema(record.PreviousSchema),
				OutputSchema: rawSchema(record.PreviousOutputSchema), Annotations: cloneToolAnnotations(record.PreviousAnnotations),
			}
		}
		if tool.Previous != nil {
			tool.Diff = buildReviewDiff(*tool.Previous, tool)
		}
		result.Tools = append(result.Tools, tool)
	}
	return result, nil
}

// reviewDefaultAllowed is the default selection of the review screens: only
// an approved, unblocked tool starts allowed; everything else starts
// unchecked.
func reviewDefaultAllowed(tool ReviewTool) bool {
	if tool.Disabled {
		return false
	}
	return tool.ApprovalStatus == storage.ToolApprovalStatusApproved
}

func reviewTier(annotations *config.ToolAnnotations) contracts.Tier {
	if annotations == nil {
		return contracts.TierUnknown
	}
	return contracts.AnnotationTier(annotations)
}

func reviewTransport(protocol string) string {
	if strings.EqualFold(protocol, "stdio") || protocol == "" {
		return "stdio"
	}
	return "http"
}

func rawSchema(value string) json.RawMessage {
	if value == "" || !json.Valid([]byte(value)) {
		return nil
	}
	return json.RawMessage(value)
}

func copyStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	copy := make(map[string]string, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}
