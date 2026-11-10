package runtime

import (
	"encoding/json"
	"reflect"

	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// defaultEventBuffer sizes each internal activity-event subscription.
const defaultEventBuffer = 256

// SubscribeEvents registers a subscriber to the internal activity-event bus
// (the activity service records these events; they are not the dashboard
// change stream, see ChangeHub).
// Callers must not close the returned channel; use UnsubscribeEvents when finished.
func (r *Runtime) SubscribeEvents() chan Event {
	ch := make(chan Event, defaultEventBuffer)
	r.eventMu.Lock()
	if r.eventSubs == nil {
		r.eventSubs = make(map[chan Event]struct{})
	}
	r.eventSubs[ch] = struct{}{}
	r.eventMu.Unlock()
	return ch
}

// UnsubscribeEvents removes the subscriber and closes the channel.
func (r *Runtime) UnsubscribeEvents(ch chan Event) {
	r.eventMu.Lock()
	if _, ok := r.eventSubs[ch]; ok {
		delete(r.eventSubs, ch)
		close(ch)

	}
	r.eventMu.Unlock()
}

func (r *Runtime) publishEvent(evt Event) {
	r.eventMu.RLock()
	for ch := range r.eventSubs {
		select {
		case ch <- evt:
		default:
		}
	}
	r.eventMu.RUnlock()
}

// serversChanged marks the named servers changed on the dashboard change
// stream; no names means every server (a change not tied to one server).
func (r *Runtime) serversChanged(names ...string) {
	if len(names) == 0 {
		r.changes.AllServersDirty()
		return
	}
	r.changes.Dirty(EntityServers, names...)
}

// MarkServersChanged is serversChanged for collaborators outside the runtime
// (management service, OAuth refresh manager).
func (r *Runtime) MarkServersChanged(names ...string) { r.serversChanged(names...) }

// ProfileChanged marks a profile changed (created, edited, renamed or
// deleted). Client rows show their profile's name and state, so every client
// is refreshed too.
func (r *Runtime) ProfileChanged(id string) {
	r.changes.Dirty(EntityProfiles, id)
	r.allClientsChanged()
}

// allProfilesChanged refreshes every profile: their effective servers and
// tool counts derive from the servers and tools.
func (r *Runtime) allProfilesChanged() {
	cfg := r.Config()
	if cfg == nil {
		return
	}
	ids := make([]string, 0, len(cfg.Profiles))
	for i := range cfg.Profiles {
		ids = append(ids, cfg.Profiles[i].ID)
	}
	r.changes.Dirty(EntityProfiles, ids...)
}

func (r *Runtime) allClientsChanged() {
	if r.clientsService == nil {
		return
	}
	records, err := r.clientsService.Records()
	if err != nil {
		return
	}
	ids := make([]string, 0, len(records))
	for i := range records {
		ids = append(ids, records[i].ClientID)
	}
	r.changes.Dirty(EntityClients, ids...)
}

// toolsChanged reports that a server's tool rows changed (approval, enabled,
// classification, discovery): the server's counts and its tools (with their
// reviews) are derived from them.
func (r *Runtime) toolsChanged(serverName string) {
	r.serversChanged(serverName)
	r.changes.Invalidate(EntityTools, serverName)
	r.allProfilesChanged()
}

// Changes returns the dashboard change hub.
func (r *Runtime) Changes() *ChangeHub { return r.changes }

// EmitActivityToolCallStarted emits an event when a tool execution begins.
// source indicates how the call was triggered: "mcp" or "api"
func (r *Runtime) EmitActivityToolCallStarted(serverName, toolName, sessionID, requestID, source string, args map[string]any) {
	payload := map[string]any{
		"server_name": serverName,
		"tool_name":   toolName,
		"session_id":  sessionID,
		"request_id":  requestID,
		"source":      source,
		"arguments":   args,
	}
	r.publishEvent(newEvent(EventTypeActivityToolCallStarted, payload))
}

// EmitActivityToolCallCompletedAttributed emits a tool-call completion with the
// scope attribution (profile, client, token in effect for this call). A zero
// attr adds no attribution to the payload. blockReason is the
// profile.BlockReason of a profile-refused sub-call, "" for everything else.
func (r *Runtime) EmitActivityToolCallCompletedAttributed(serverName, toolName, sessionID, requestID, source, status, errorMsg string, durationMs int64, arguments map[string]interface{}, response string, responseTruncated bool, toolVariant string, intent map[string]interface{}, contentTrust, profile string, requestBytes, responseBytes int, parentID string, blockReason string, attr ActivityAttribution) {

	if cfg := r.Config(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.Name == serverName {
				attr.ExecutionHostID = server.ExecutionHost()
				break
			}
		}
	}
	payload := map[string]any{
		"server_name":        serverName,
		"tool_name":          toolName,
		"session_id":         sessionID,
		"request_id":         requestID,
		"source":             source,
		"status":             status,
		"error_message":      errorMsg,
		"duration_ms":        durationMs,
		"response":           response,
		"response_truncated": responseTruncated,
	}
	// Add arguments if provided
	if arguments != nil {
		payload["arguments"] = arguments
	}
	// Add intent metadata if provided
	if toolVariant != "" {
		payload["tool_variant"] = toolVariant
	}
	if intent != nil {
		payload["intent"] = intent
	}
	// Add content trust metadata if provided
	if contentTrust != "" {
		payload["content_trust"] = contentTrust
	}
	if profile != "" {
		payload["profile"] = profile
	}
	if requestBytes > 0 {
		payload["request_bytes"] = requestBytes
	}
	if responseBytes > 0 {
		payload["response_bytes"] = responseBytes
	}

	// Correlation id of the code_execution whose sandbox issued this sub-call;
	// only set when there is a parent.
	if parentID != "" {
		payload["parent_id"] = parentID
	}
	// The typed cause of a profile tool-policy refusal of a code_execution
	// sub-call; only set when non-empty.
	if blockReason != "" {
		payload[storage.MetadataKeyBlockReason] = blockReason
	}
	if a := attr.payload(); a != nil {
		payload[attributionPayloadKey] = a
	}

	r.publishEvent(newEvent(EventTypeActivityToolCallCompleted, payload))
}

// EmitActivityPolicyDecisionAttributed emits a policy decision with the scope
// attribution. blockReason may be empty.
func (r *Runtime) EmitActivityPolicyDecisionAttributed(serverName, toolName, sessionID, requestID, decision, reason, blockReason string, attr ActivityAttribution) {

	if cfg := r.Config(); cfg != nil {
		for _, server := range cfg.Servers {
			if server != nil && server.Name == serverName {
				attr.ExecutionHostID = server.ExecutionHost()
				break
			}
		}
	}
	r.emitActivityPolicyDecision(serverName, toolName, sessionID, requestID, decision, reason, blockReason, attr)
}

func (r *Runtime) emitActivityPolicyDecision(serverName, toolName, sessionID, requestID, decision, reason, blockReason string, attr ActivityAttribution) {
	payload := map[string]any{
		"server_name": serverName,
		"tool_name":   toolName,
		"session_id":  sessionID,
		"request_id":  requestID,
		"decision":    decision,
		"reason":      reason,
	}
	if blockReason != "" {
		payload["block_reason"] = blockReason
	}
	if a := attr.payload(); a != nil {
		payload[attributionPayloadKey] = a
	}
	r.publishEvent(newEvent(EventTypeActivityPolicyDecision, payload))
}

// EmitActivitySystemStart emits an event when MCPProxy server starts.
func (r *Runtime) EmitActivitySystemStart(version, listenAddress string, startupDurationMs int64) {
	payload := map[string]any{
		"version":             version,
		"listen_address":      listenAddress,
		"startup_duration_ms": startupDurationMs,
	}
	r.publishEvent(newEvent(EventTypeActivitySystemStart, payload))
}

// EmitActivitySystemStop emits an event when MCPProxy server stops.
func (r *Runtime) EmitActivitySystemStop(reason, signal string, uptimeSeconds int64, errorMsg string) {
	payload := map[string]any{
		"reason":         reason,
		"signal":         signal,
		"uptime_seconds": uptimeSeconds,
		"error_message":  errorMsg,
	}
	r.publishEvent(newEvent(EventTypeActivitySystemStop, payload))
}

// EmitActivityInternalToolCallAttributed emits a built-in tool call with the
// scope attribution.
func (r *Runtime) EmitActivityInternalToolCallAttributed(internalToolName, targetServer, targetTool, toolVariant, sessionID, requestID, status, errorMsg string, durationMs int64, arguments map[string]interface{}, response interface{}, intent map[string]interface{}, contentTrust string, responseTruncated bool, attr ActivityAttribution) {
	payload := map[string]any{
		"internal_tool_name": internalToolName,
		"session_id":         sessionID,
		"request_id":         requestID,
		"status":             status,
		"error_message":      errorMsg,
		"duration_ms":        durationMs,
	}
	if targetServer != "" {
		payload["target_server"] = targetServer
	}
	if targetTool != "" {
		payload["target_tool"] = targetTool
	}
	if toolVariant != "" {
		payload["tool_variant"] = toolVariant
	}
	if arguments != nil {
		payload["arguments"] = arguments
	}
	if response != nil {
		payload["response"] = response
	}
	if intent != nil {
		payload["intent"] = intent
	}
	if contentTrust != "" {
		payload["content_trust"] = contentTrust
	}
	// Always set, never omitempty-style conditional: absent and false must not be
	// the same wire state here, because a consumer reading "no key" as "not
	// truncated" is exactly the silent understatement this flag prevents.
	payload["response_truncated"] = responseTruncated
	// Pre-truncation byte lengths, the same measure the upstream tool-call
	// path carries; 0 means unknown in the activity log, not free.
	//
	// `response` here is the FULL pre-truncation value (see the doc on
	// MCPProxyServer.emitActivityInternalToolCallTruncated), which is exactly
	// what response_bytes is defined to mean. When responseTruncated is set the
	// agent consumed LESS than this, and the flag travelling beside it is what
	// lets a consumer exclude the row rather than overstate mcpproxy's cost.
	if n := jsonByteLen(arguments); n > 0 {
		payload["request_bytes"] = n
	}
	if n := jsonByteLen(response); n > 0 {
		payload["response_bytes"] = n
	}
	if a := attr.payload(); a != nil {
		payload[attributionPayloadKey] = a
	}
	r.publishEvent(newEvent(EventTypeActivityInternalToolCall, payload))
}

// EmitActivityConfigChange emits an event when configuration changes.
// action is one of: server_added, server_removed, server_updated
// source indicates how the change was triggered: "mcp" or "api"
func (r *Runtime) EmitActivityConfigChange(action, affectedEntity, source string, changedFields []string, previousValues, newValues map[string]interface{}) {
	payload := map[string]any{
		"action":          action,
		"affected_entity": affectedEntity,
		"source":          source,
	}
	if len(changedFields) > 0 {
		payload["changed_fields"] = changedFields
	}
	if previousValues != nil {
		payload["previous_values"] = previousValues
	}
	if newValues != nil {
		payload["new_values"] = newValues
	}
	r.publishEvent(newEvent(EventTypeActivityConfigChange, payload))
}

// jsonByteLen returns the JSON-serialized byte length of v, or 0 when there is
// nothing to measure or it cannot be marshaled. A typed-nil pointer inside a
// non-nil interface encodes as "null"; that is 4 bytes of nothing, so it is
// reported as 0 rather than as a measured response.
func jsonByteLen(v interface{}) int {
	if v == nil {
		return 0
	}
	if rv := reflect.ValueOf(v); (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Map ||
		rv.Kind() == reflect.Slice || rv.Kind() == reflect.Interface) && rv.IsNil() {
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}
