package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

var (
	ErrClientCredentialActive   = errors.New("client credential is active; use rotation instead of mint")
	ErrClientCredentialNotFound = errors.New("client credential not found")
)

// findClientTokenRecordLocked resolves the kind=client token record named
// "client-"+clientID and returns its bucket key.
func findClientTokenRecordLocked(tx *database.Tx, clientID string) (hash []byte, token *auth.ClientCredential, err error) {
	tokenBucket := tx.Bucket([]byte(ClientCredentialsBucket))
	if tokenBucket == nil {
		return nil, nil, nil
	}
	name := auth.ClientTokenName(clientID)
	c := tokenBucket.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var t auth.ClientCredential
		if unmarshalErr := json.Unmarshal(v, &t); unmarshalErr != nil {
			continue // skip unparseable rows
		}
		if t.Name == name && t.Kind == auth.KindClient {
			hashCopy := append([]byte(nil), k...)
			return hashCopy, &t, nil
		}
	}
	return nil, nil, nil
}

type ClientMintOptions struct {
	AllowCodeExecution bool
	AllowManaging      bool
	HostID             string
	Mode               string
	Pin                string
	DisplayName        string
	Issuer             *auth.CredentialIssuer
}

// MintClientCredentialWith stores a new client credential for clientID with
// the given issuance options. It returns the committed record (the caller
// projects its view from it, never from a second store read).
func (m *Manager) MintClientCredentialWith(clientID, rawToken string, hmacKey []byte, opts ClientMintOptions) (*auth.ClientCredential, error) {
	mode, pin, displayName := opts.Mode, opts.Pin, opts.DisplayName
	if pin == "" {
		mode = auth.ProfileModeSwitchable
	}
	if !auth.ValidClientID(clientID) {
		return nil, fmt.Errorf("invalid client id %q", clientID)
	}
	if mode != auth.ProfileModeLocked && mode != auth.ProfileModeSwitchable {
		return nil, fmt.Errorf("invalid profile mode %q", mode)
	}

	hash := auth.HashToken(rawToken, hmacKey)
	now := time.Now().UTC()

	token := auth.ClientCredential{
		AllowCodeExecution: opts.AllowCodeExecution, AllowManaging: opts.AllowManaging,
		HostID:      opts.HostID,
		Name:        auth.ClientTokenName(clientID),
		Kind:        auth.KindClient,
		ClientID:    clientID,
		ProfileMode: mode,
		ProfilePin:  pin,
		CreatedAt:   now,
		ConnectedAt: &now,
		TokenHash:   hash,
		TokenPrefix: auth.TokenPrefix(rawToken),
		DisplayName: displayName,
		Issuer:      opts.Issuer,
	}
	if err := auth.ValidateTokenInvariants(&token, auth.KindClient); err != nil {
		return nil, fmt.Errorf("mint produced an invalid client credential record: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	err := m.db.db.Update(func(tx *database.Tx) error {
		tokenBucket, err := tx.CreateBucketIfNotExists([]byte(ClientCredentialsBucket))
		if err != nil {
			return fmt.Errorf("failed to create client credentials bucket: %w", err)
		}

		_, existing, err := findClientTokenRecordLocked(tx, clientID)
		if err != nil {
			return err
		}
		if existing != nil {
			return ErrClientCredentialActive
		}

		count := 0
		if err := tokenBucket.ForEach(func(_, data []byte) error {
			var rec auth.ClientCredential
			if json.Unmarshal(data, &rec) == nil && rec.Kind == auth.KindClient {
				count++
			}
			return nil
		}); err != nil {
			return err
		}
		if count >= auth.MaxTokens {
			return ErrClientCredentialLimitReached
		}

		data, err := json.Marshal(token)
		if err != nil {
			return fmt.Errorf("failed to marshal client credential: %w", err)
		}
		return tokenBucket.Put([]byte(hash), data)
	})
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// UpdateClientCredentialBinding rewrites the (profile pin, mode) of the ACTIVE
// kind=client credential named "client-"+clientID in one transaction and
// returns the record as it was and as it is now. The secret is untouched, so
// the client's config never changes (a binding is server-side state).
// The result must still satisfy ValidateTokenInvariants (a locked binding
// needs a non-empty pin); otherwise nothing is written.
func (m *Manager) UpdateClientCredentialBinding(clientID, pin, mode, host, displayName string, allowManaging *bool, allowCodeExecution *bool) (before, after *auth.ClientCredential, err error) {
	if !auth.ValidClientID(clientID) {
		return nil, nil, fmt.Errorf("invalid client id %q", clientID)
	}
	if pin == "" {
		mode = auth.ProfileModeSwitchable
	}

	if mode != auth.ProfileModeLocked && mode != auth.ProfileModeSwitchable {
		return nil, nil, fmt.Errorf("invalid profile mode %q", mode)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	err = m.db.db.Update(func(tx *database.Tx) error {
		tokenBucket := tx.Bucket([]byte(ClientCredentialsBucket))
		if tokenBucket == nil {
			return ErrClientCredentialNotFound
		}
		primaryHash, existing, err := findClientTokenRecordLocked(tx, clientID)
		if err != nil {
			return err
		}
		if existing == nil {
			return ErrClientCredentialNotFound
		}

		snapshot := *existing
		updated := *existing
		updated.HostID = host
		updated.DisplayName = displayName
		updated.ProfilePin = pin
		updated.ProfileMode = mode
		if allowManaging != nil {
			updated.AllowManaging = *allowManaging
		}
		if allowCodeExecution != nil {
			updated.AllowCodeExecution = *allowCodeExecution
		}
		if err := auth.ValidateTokenInvariants(&updated, auth.KindClient); err != nil {
			return fmt.Errorf("binding update produced an invalid client credential record: %w", err)
		}
		data, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("failed to marshal client credential: %w", err)
		}
		if err := tokenBucket.Put(primaryHash, data); err != nil {
			return fmt.Errorf("failed to store client binding: %w", err)
		}
		before, after = &snapshot, &updated
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// RotateClientCredential atomically replaces the client's only token.
func (m *Manager) RotateClientCredential(clientID, rawToken string, hmacKey []byte) (*auth.ClientCredential, error) {
	if !auth.ValidClientID(clientID) {
		return nil, fmt.Errorf("invalid client id %q", clientID)
	}
	if kind, ok := auth.ValidateAnyTokenFormat(rawToken); !ok || kind != auth.KindClient {
		return nil, fmt.Errorf("invalid client token format")
	}
	hash := auth.HashToken(rawToken, hmacKey)
	m.mu.Lock()
	defer m.mu.Unlock()
	var updated *auth.ClientCredential
	err := m.db.db.Update(func(tx *database.Tx) error {
		primaryHash, rec, err := findClientTokenRecordLocked(tx, clientID)
		if err != nil {
			return err
		}
		if rec == nil {
			return ErrClientCredentialNotFound
		}

		rec.TokenHash = hash
		rec.TokenPrefix = auth.TokenPrefix(rawToken)
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		bucket := tx.Bucket([]byte(ClientCredentialsBucket))
		if err := bucket.Delete(primaryHash); err != nil {
			return err
		}
		if err := bucket.Put([]byte(hash), data); err != nil {
			return err
		}
		updated = rec
		return nil
	})
	return updated, err
}

// DeleteClientCredential removes the saved client and its token atomically.
func (m *Manager) DeleteClientCredential(clientID string) (*auth.ClientCredential, error) {
	if !auth.ValidClientID(clientID) {
		return nil, fmt.Errorf("invalid client id %q", clientID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var deleted *auth.ClientCredential
	err := m.db.db.Update(func(tx *database.Tx) error {
		hash, rec, err := findClientTokenRecordLocked(tx, clientID)
		if err != nil {
			return err
		}
		if rec == nil {
			return ErrClientCredentialNotFound
		}
		if err := tx.Bucket([]byte(ClientCredentialsBucket)).Delete(hash); err != nil {
			return err
		}
		deleted = rec
		return nil
	})
	return deleted, err
}
