package server

import (
	"context"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// Visibility reasons returned by the resolvers below.
// Callers use them to pick a response shape: scope failures are SILENT
// (an agent never learns a tool exists on a server it cannot access), the
// rest surface as locked/not-found with remediation.
const (
	visReasonNotIndexed           = "not_indexed"
	visReasonServerNotInScope     = "server_not_in_scope"
	visReasonToolPendingApproval  = "tool_pending_approval"
	visReasonToolChangedApproval  = "tool_changed_approval"
	visReasonToolNotCallable      = "tool_not_callable"
	visReasonToolNoApprovalRecord = "tool_no_approval_record"
	visReasonToolUnresolved       = "tool_unresolved"
	// visReasonToolPolicyExcluded is the defense-in-depth
	// reason: indexedToolVisible's own CompiledPolicy.Decide re-check found
	// the tool excluded, even though SearchToolsAdmitted already filtered it
	// out before the cut. Treated exactly like visReasonServerNotInScope by
	// the retrieve_tools loop (invisible, never "locked", never counted) —
	// forbids naming or describing a profile-hidden tool, and a
	// "locked" entry would do both.
	visReasonToolPolicyExcluded = "tool_policy_excluded"
)

func (p *MCPProxyServer) toolVisibleToSession(ctx context.Context, serverName, toolName string) (visible bool, reason string) {
	authCtx := auth.AuthContextFromContext(ctx)
	profileName, profileScope, profileIdx := p.resolveActiveProfileWithIndex(ctx)
	profileResolution := p.ResolveProfileV3(ctx, profileIdx)
	if profileResolution.Scope != nil {
		profileName = profileResolution.Name
		profileScope = profileResolution.Scope
	}

	// Scope is checked BEFORE index presence. An id whose server is outside the
	// caller's effective scope must answer the same reason whether or not a
	// hidden document happens to exist under that exact (server, tool) pair
	// — checking index presence first let the mere existence of a hidden
	// collision (e.g. a case-different "b:read" on a server outside scope,
	// alongside an authorized "B:read") swap the answer from not_indexed (no
	// suggestion attempted below) to server_not_in_scope, silently
	// suppressing the did-you-mean a token would otherwise get when the
	// hidden document didn't exist at all. The scope predicate itself only
	// reads the caller's own auth/profile state, never the index, so
	// reordering costs nothing for a genuinely visible id.
	if !p.serverInScope(authCtx, profileScope, serverName) {
		return false, visReasonServerNotInScope
	}
	if !p.toolIndexed(serverName, toolName) {
		return false, visReasonNotIndexed
	}
	if profileName != "" {
		if policy := profileIdx.PolicyFor(profileName); policy != nil {
			annotations, found := p.EffectiveAnnotations(serverName, toolName)
			intrinsic := profile.IntrinsicTier(annotations, found)
			if admitted, _, _ := policy.Decide(serverName, toolName, intrinsic); !admitted {
				return false, visReasonToolPolicyExcluded
			}
		}
	}
	if p.resolveExactToolIdentity(serverName, toolName).Unresolved() {
		return false, visReasonToolUnresolved
	}
	// describe_tool-only strict gates (contract steps 3–4) — ordered BEFORE
	// callability so a quarantined/pending id reports its real lock, not a
	// generic "disabled".
	if gateReason := p.describeGateReason(serverName, toolName); gateReason != "" {
		return false, gateReason
	}
	if !p.isExactToolCallable(serverName, toolName) {
		return false, visReasonToolNotCallable
	}
	return true, ""
}

func (p *MCPProxyServer) indexedToolVisible(authCtx *auth.AuthContext, profileScope *profile.ProfileScope, policy *profile.CompiledPolicy, serverName, toolName string) (visible bool, reason string) {
	// Profile scope + client token server scope —
	// applied BEFORE any classification so an agent never learns a tool
	// exists on a server it cannot access.
	if !p.serverInScope(authCtx, profileScope, serverName) {
		return false, visReasonServerNotInScope
	}

	// Callability: disabled/blocked tools are non-existent for discovery.
	if !p.isExactToolCallable(serverName, toolName) {
		return false, visReasonToolNotCallable
	}

	if policy != nil {
		annotations, found := p.EffectiveAnnotations(serverName, toolName)
		intrinsic := profile.IntrinsicTier(annotations, found)
		if admitted, _, _ := policy.Decide(serverName, toolName, intrinsic); !admitted {
			return false, visReasonToolPolicyExcluded
		}
	}

	return true, ""
}

func (p *MCPProxyServer) describeGateReason(serverName, toolName string) string {
	gate := p.evaluateExactToolGate(serverName, toolName)

	switch gate.lockStatus {
	case storage.ToolApprovalStatusPending:
		if isImplicitPendingApproval(gate.approval) {
			return visReasonToolNoApprovalRecord
		}
		return visReasonToolPendingApproval
	case storage.ToolApprovalStatusChanged:
		return visReasonToolChangedApproval
	}
	return ""
}

// normalizeServerTool strips the indexed "server:tool" prefix from toolName
// (result.Tool.Name keeps the prefix when ServerName is set) — exactly like
// isToolCallable, so approval/config lookups key consistently.
//
// : only the SERVER's own prefix is an indexing artifact. When
// serverName is already known and the first ":"-segment is something else,
// the colon belongs to the raw tool name ("ns:erase" on server "a") and is
// kept, so every gate keyed on this pair — tier classification, approval,
// config denial, callability — evaluates the exact identity that is
// dispatched instead of the suffix tool's.
func normalizeServerTool(serverName, toolName string) (string, string) {
	if prefix, rest, ok := strings.Cut(toolName, ":"); ok {
		switch {
		case serverName == "":
			serverName, toolName = prefix, rest
		case prefix == serverName:
			toolName = rest
		}
	}
	return serverName, toolName
}

// serverInScope is the scope step shared by both resolvers — the former
// serverDiscoverable closure (client token scope,, + profile
// scope,. Also used directly by the quarantined-tool discovery
// pass, so the three can never drift.
func (p *MCPProxyServer) serverInScope(authCtx *auth.AuthContext, profileScope *profile.ProfileScope, serverName string) bool {
	if authCtx != nil && !authCtx.IsAdmin() && (!authCtx.CanAccessServer(serverName) || !p.hostAllows(authCtx, serverName)) {
		return false
	}
	return profileScope.Allows(serverName)
}

func (p *MCPProxyServer) usageStatEligible(authCtx *auth.AuthContext, profileScope *profile.ProfileScope, policy *profile.CompiledPolicy, serverName, toolName string) bool {
	if !p.serverInScope(authCtx, profileScope, serverName) {
		return false
	}
	if p.lookupIndexedTool(serverName, toolName) == nil {
		return false
	}
	if !p.isExactToolCallable(serverName, toolName) {
		return false
	}
	if p.describeGateReason(serverName, toolName) != "" {
		return false
	}
	if policy != nil {
		annotations, found := p.EffectiveAnnotations(serverName, toolName)
		intrinsic := profile.IntrinsicTier(annotations, found)
		if admitted, _, _ := policy.Decide(serverName, toolName, intrinsic); !admitted {
			return false
		}
	}
	return true
}

// toolIndexed reports whether the tool is present in the shared search index
// (describe_tool visibility step 1 — ids resolve against the same corpus
// search ranks over).
func (p *MCPProxyServer) toolIndexed(serverName, toolName string) bool {
	return p.lookupIndexedTool(serverName, toolName) != nil
}

// lookupIndexedTool resolves a (server, RAW tool) pair to its indexed metadata
// — the same corpus retrieve_tools ranks over, so describe_tool definitions
// and search entries render from identical inputs. nil when absent. The
// indexed Name is the canonical "<server>:<raw>" id and is matched exactly
// : a bare-name alternate could only match when a raw name
// equals a sibling's canonical id (raw "a:erase" on server "a" → the "erase"
// doc), rendering one tool's schema under another's id.
func (p *MCPProxyServer) lookupIndexedTool(serverName, toolName string) *config.ToolMetadata {
	tools, err := p.index.GetToolsByServer(serverName)
	if err != nil {
		return nil
	}
	full := serverName + ":" + toolName
	for _, tool := range tools {
		if tool.Name == full {
			return tool
		}
	}
	return nil
}

// splitServerTool splits a "<server>:<tool>" id. Whitespace is never
// significant in a canonical id, so both segments are trimmed. ok=false when
// the id has no server prefix or either segment is blank.
func splitServerTool(id string) (serverName, toolName string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(id), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	serverName, toolName = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if serverName == "" || toolName == "" {
		return "", "", false
	}
	return serverName, toolName, true
}

// suggestCanonicalToolID resolves an id that differs from an indexed one only
// by letter case to its canonical form.
//
// : this stays the INDEX-backed resolver, byte-identical, for the
// retrieve surfaces. The direct surface has its own — suggestDirectToolID in
// mcp_describe_direct.go — because it must draw from the catalog and gate on the
// direct listing's rules (the operation-permission tier and withheld display-
// name collisions, neither of which exists here). The seam is therefore the
// call site in resolveDescribeDefinition rather than a branch inside this
// function: threading a surface flag through here would put direct-only rules
// inside the predicate three retrieve-path callers share. Case is NOT folded on any resolution
// path: server and tool names are exact keys in the approval, quarantine,
// profile and agent-scope stores, so accepting a miscased id would route a
// call around gates keyed on the exact name. The correction is therefore only
// ever suggested — and only when the corrected pair is visible to this
// session, so a suggestion can never confirm that an out-of-scope or
// quarantined tool exists.
func (p *MCPProxyServer) suggestCanonicalToolID(ctx context.Context, serverName, toolName string) (string, bool) {
	servers, err := p.index.GetAllIndexedServerNames()
	if err != nil {
		return "", false
	}
	for _, server := range servers {
		if !strings.EqualFold(server, serverName) {
			continue
		}
		tools, terr := p.index.GetToolsByServer(server)
		if terr != nil {
			continue
		}
		for _, tool := range tools {
			bare := config.RawToolName(tool)
			if !strings.EqualFold(bare, toolName) || (server == serverName && bare == toolName) {
				continue
			}
			if visible, _ := p.toolVisibleToSession(ctx, server, bare); visible {
				return server + ":" + bare, true
			}
		}
	}
	return "", false
}

// EffectiveAnnotations returns the (server, tool) pair's effective
// annotations for profile-policy enforcement (T013): the ONE
// annotation source for CompiledPolicy.Decide (via profile.IntrinsicTier),
// SearchToolsAdmitted's predicate, "view as" and the access explainer — the
// index itself stores no annotation field and gains none (no
// index-schema change, no reindex). It is a thin wrapper over
// resolveExactToolIdentity, the SAME identity seam every dispatch path
// already uses, so discovery and execution classify a
// tool from one source and can never disagree; it is also the documented
// hook for per-server annotation_overrides: only resolveExactToolIdentity
// needs to change, never this call site or its callers.
//
// found=false (the current snapshot does not list this exact server:tool
// name at all) tells the caller to fail closed: profile.IntrinsicTier(nil,
// false) is TierDestructive regardless of the returned annotations, which
// are nil in that case. found=true does not by itself certify that the
// annotations are current for the LIVE connection generation (a
// disconnected server can retain a previous connection's stamp) — dispatch
// enforces currency separately via the epoch-pinned certified() check
// (mcp.go); this seam only answers "what does the snapshot say", the same
// answer every other dispatch-adjacent reader of toolIdentity gets.
//
// This delegates to lookupExactToolAnnotations (mcp.go) rather than
// re-resolving: same identity, same seam, one implementation.
func (p *MCPProxyServer) EffectiveAnnotations(serverName, toolName string) (annotations *config.ToolAnnotations, found bool) {
	annotations, found = p.lookupExactToolAnnotations(serverName, toolName)
	if !found || p.storage == nil || profile.IntrinsicTier(annotations, true) != profile.TierUnannotated {
		return annotations, found
	}
	tier, err := p.storage.GetToolClassification(serverName, toolName)
	if err != nil || tier == "" {
		return annotations, found
	}
	effective := config.ToolAnnotations{}
	if annotations != nil {
		effective = *annotations
	}
	readOnly, destructive := tier == "read", tier == "destructive"
	effective.ReadOnlyHint = &readOnly
	effective.DestructiveHint = &destructive
	return &effective, true
}

// Host availability is independent of profile membership: one central profile
// can span hosts without exposing another host's local-only tools.
func (p *MCPProxyServer) hostAllows(caller *auth.AuthContext, name string) bool {
	if caller == nil || caller.IsAdmin() {
		return true
	}
	if cfg := p.currentConfig(); cfg != nil {
		for _, server := range cfg.Servers {
			if server == nil || server.Name != name {
				continue
			}
			host := server.ExecutionHost()
			return host == caller.HostID || caller.HostID != "" && host == "" && server.SharedWithPeers()
		}
	}
	return false
}
