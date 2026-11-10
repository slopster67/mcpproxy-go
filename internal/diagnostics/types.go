package diagnostics

import "time"

// Code is a stable error-code identifier. Immutable across releases once shipped.
type Code string

// Severity drives UI badge colour and CLI formatting.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// CatalogEntry is the authoritative description of one error code.
type CatalogEntry struct {
	Code        Code     `json:"code"`
	Severity    Severity `json:"severity"`
	UserMessage string   `json:"user_message"`
	// Retry declares whether an automatic reconnect can ever fix a failure
	// carrying this code. Omitted (zero value) means transient/retryable — see
	// RetryClass. Consumed by IsPermanent.
	Retry RetryClass `json:"retry,omitempty"`
}

// DiagnosticError is the runtime record attached to a server's stateview snapshot
// while the server has an active failure.
type DiagnosticError struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	Cause    string   `json:"cause,omitempty"`
	// Remediation is an optional runtime-aware, context-specific user message
	// that overrides the static CatalogEntry.UserMessage when present.
	// Empty when the generic catalog message is sufficient.
	Remediation string    `json:"remediation,omitempty"`
	ServerID    string    `json:"server_id"`
	DetectedAt  time.Time `json:"detected_at"`
}

// ClassifierHints lets callers nudge the classifier when context is known
// (e.g., "this error came from the stdio spawn path").
type ClassifierHints struct {
	Transport string
}

// RetryClass says whether an automatic retry of a failure carrying a given code
// can ever succeed without a human changing something first.
//
// The zero value means transient: a code is retryable until it is EXPLICITLY
// declared otherwise. An unclassified failure must never be mistaken for proof
// of permanence.
type RetryClass string

const (
	// RetryPermanent means the condition is a config/toolchain fact: the exact
	// same attempt will fail identically forever until a human edits something.
	// Retrying it burns CPU, disk and network and floods the log for no chance
	// of success.
	RetryPermanent RetryClass = "permanent"
)
