package oauth

import (
	"errors"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// OAuth state is bound to the committed credential binding of the upstream
// instance that owns it (storage/oauth_binding.go).

// ErrCredentialBindingStale reports that an OAuth write of an upstream
// instance was refused because a config commit removed, replaced or
// re-bound (OAuth config change) the server since the instance was built.
// Nothing was written; the instance that serves the server now runs its own
// flow.
var ErrCredentialBindingStale = errors.New("server was removed, replaced or reconfigured; OAuth result discarded")

// ClearClientIfBound clears the DCR registration of the exact record rec
// (its revision) under binding b: invalid_client handling acts only on the
// record it read, and only while the server's row still carries b.
func ClearClientIfBound(db storage.OAuthStore, serverKey, server string, b commitview.CredentialBinding, rec *storage.OAuthTokenRecord) (bool, error) {
	if db == nil || rec == nil {
		return false, nil
	}

	return db.ClearOAuthClientIfBound(serverKey, server, b, rec.Revision)
}
