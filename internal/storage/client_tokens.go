package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

const ClientCredentialsBucket = "client_credentials"

var (
	ErrClientCredentialLimitReached   = errors.New("maximum number of clients reached")
	ErrStoredClientCredentialNotFound = errors.New("client credential not found")
)

// GetClientCredentialByHash retrieves a client token by its HMAC hash.
// Returns nil if not found.
func (m *Manager) GetClientCredentialByHash(hash string) (*auth.ClientCredential, error) {
	if hash == "" {
		return nil, fmt.Errorf("client token hash cannot be empty")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var token *auth.ClientCredential

	err := m.db.db.View(func(tx *database.Tx) error {
		tokenBucket := tx.Bucket([]byte(ClientCredentialsBucket))
		if tokenBucket == nil {
			return nil
		}

		data := tokenBucket.Get([]byte(hash))
		if data == nil {
			return nil
		}

		token = &auth.ClientCredential{}
		if err := json.Unmarshal(data, token); err != nil {
			return fmt.Errorf("failed to unmarshal client token: %w", err)
		}

		return nil
	})

	return token, err
}

// ListClientCredentials returns all stored client tokens.
//
// A record that fails to unmarshal is skipped, not fatal: one unparseable row
// must not turn every listing into an error. The skip is logged at WARN with
// the bucket key (an HMAC hash, not a credential) so an operator can find it.
func (m *Manager) ListClientCredentials() ([]auth.ClientCredential, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var tokens []auth.ClientCredential

	err := m.db.db.View(func(tx *database.Tx) error {
		tokenBucket := tx.Bucket([]byte(ClientCredentialsBucket))
		if tokenBucket == nil {
			return nil
		}

		return tokenBucket.ForEach(func(k, v []byte) error {
			var token auth.ClientCredential
			if err := json.Unmarshal(v, &token); err != nil {
				if m.logger != nil {
					m.logger.Warnw("skipping unparseable client token record",
						"bucket", ClientCredentialsBucket, "key", string(k), "error", err)
				}
				return nil
			}
			if token.Kind == auth.KindClient {
				tokens = append(tokens, token)
			}
			return nil
		})
	})

	return tokens, err
}

// UpdateClientCredentialLastUsed updates the LastUsedAt timestamp for the token
// stored under the given HMAC hash. The hash is the authoritative, unambiguous
// key: unlike a name it is unique across owners, so this cannot stamp another
// tenant's token.
func (m *Manager) UpdateClientCredentialLastUsed(hash string) error {
	if hash == "" {
		return fmt.Errorf("client token hash cannot be empty")
	}

	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.db.db.Update(func(tx *database.Tx) error {
		tokenBucket := tx.Bucket([]byte(ClientCredentialsBucket))
		if tokenBucket == nil {
			return ErrStoredClientCredentialNotFound
		}

		data := tokenBucket.Get([]byte(hash))
		if data == nil {
			return ErrStoredClientCredentialNotFound
		}

		var token auth.ClientCredential
		if err := json.Unmarshal(data, &token); err != nil {
			return fmt.Errorf("failed to unmarshal client token: %w", err)
		}

		token.LastUsedAt = &now

		updatedData, err := json.Marshal(token)
		if err != nil {
			return fmt.Errorf("failed to marshal client token: %w", err)
		}

		return tokenBucket.Put([]byte(hash), updatedData)
	})
}

func (m *Manager) ValidateClientToken(rawToken string, hmacKey []byte) (*auth.ClientCredential, error) {
	claimedKind, ok := auth.ValidateAnyTokenFormat(rawToken)
	if !ok {
		return nil, fmt.Errorf("invalid token format")
	}

	hash := auth.HashToken(rawToken, hmacKey)

	token, err := m.GetClientCredentialByHash(hash)
	if err != nil {
		return nil, fmt.Errorf("failed to look up token: %w", err)
	}
	if token == nil {
		return nil, fmt.Errorf("token not found")
	}

	// Fail-closed invariants, checked on every authentication (not only at
	// mint time): a violation never degrades to a wildcard client token.
	if err := auth.ValidateTokenInvariants(token, claimedKind); err != nil {
		if m.logger != nil {
			m.logger.Warnw("denying malformed credential record",
				"name", token.Name, "token_prefix", token.TokenPrefix)
		}
		return nil, err
	}

	return token, nil
}
