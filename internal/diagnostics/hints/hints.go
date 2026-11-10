package hints

import (
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/diagnostics"
)

// For builds the classifier hints for a server's failure from its resolved
// transport.
func For(_ *config.Config, _ *config.ServerConfig, transport string) diagnostics.ClassifierHints {
	return diagnostics.ClassifierHints{Transport: diagnostics.CanonicalTransport(transport)}
}
