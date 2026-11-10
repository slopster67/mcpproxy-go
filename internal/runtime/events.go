package runtime

import "time"

// EventType is an internal activity event; the activity service turns each
// into a stored record.
type EventType string

const (
	// Activity logging events (RFC-003)
	// EventTypeActivityToolCallStarted is emitted when a tool execution begins.
	EventTypeActivityToolCallStarted EventType = "activity.tool_call.started"
	// EventTypeActivityToolCallCompleted is emitted when a tool execution finishes.
	EventTypeActivityToolCallCompleted EventType = "activity.tool_call.completed"

	EventTypeActivityPolicyDecision EventType = "activity.policy_decision"

	// Activity log events
	// EventTypeActivitySystemStart is emitted when MCPProxy server starts.
	EventTypeActivitySystemStart EventType = "activity.system.start"
	// EventTypeActivitySystemStop is emitted when MCPProxy server stops.
	EventTypeActivitySystemStop EventType = "activity.system.stop"
	// EventTypeActivityInternalToolCall is emitted when an internal tool (retrieve_tools, call_tool_*, etc.) completes.
	EventTypeActivityInternalToolCall EventType = "activity.internal_tool_call.completed"
	// EventTypeActivityConfigChange is emitted when configuration changes (server add/remove/update).
	EventTypeActivityConfigChange EventType = "activity.config_change"

	// Tool-level quarantine events
	// EventTypeActivityToolQuarantineChange is emitted when a tool's quarantine status changes.
	EventTypeActivityToolQuarantineChange EventType = "activity.tool_quarantine_change"
)

// Event is a typed notification published by the runtime event bus.
type Event struct {
	Type      EventType      `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

func newEvent(eventType EventType, payload map[string]any) Event {
	return Event{
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}
}
