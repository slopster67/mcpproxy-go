package httpapi

import "github.com/slopster67/mcpproxy-go/internal/runtime"

// Named aliases of the runtime view types, so the swag annotations of the
// profile routes can name them without the import alias (swag resolves a local
// type name; it does not see the `internalRuntime` alias).

// ClientWarning is one Clients-surface warning (code, severity, action).
type ClientWarning = runtime.Warning
