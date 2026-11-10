package storage

import (
	stderrors "errors"
	"fmt"
	"strconv"

	"github.com/slopster67/mcpproxy-go/internal/database"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
)

// Entity commits persist canonical server/profile records and dependent state atomically.

const (
	// ConfigCommitGenerationKey (meta bucket) holds the generation G of the
	// last config commit, as a decimal string.
	ConfigCommitGenerationKey = "config_commit_generation"
)

// ErrInvariant marks a commit batch that violates a commit invariant. It
// indicates a bug in the caller, never a user error.
var ErrInvariant = stderrors.New("config commit invariant violated")

// ServerSave is one row write. Stamp is the ServerGen that the commit's
// Prepare computed for this name. Storage writes it verbatim and never derives
// a stamp from the batch generation or from the row it replaces.
type ServerSave struct {
	Config *config.ServerConfig
	Stamp  commitview.ServerGen
}

// EntityCommit contains the entity and dependent-state changes in one transaction.
type EntityCommit struct {
	ProfileSaves      []config.ProfileConfig
	ProfileRemoves    []string
	ClientProfileMove *ClientProfileMove
	// Generation is G; it is written to meta.config_commit_generation only.
	Generation int64
	// Saves are the servers the commit creates or changes.
	Saves []ServerSave
	// Removes are names whose row and whole purge set (purge.go) are
	// deleted in this transaction.
	Removes []string
	// Replaces are names removed and re-created in this commit: the old row and its whole purge set are deleted before the new
	// row is saved.
	Replaces []string

	// OAuthClears are names whose cached OAuth state (tokens and DCR client
	// credentials, the ClearOAuthState key set) is deleted in this
	// transaction because their OAuth configuration changed: the new OAuth config and the removal of the old
	// credentials commit or roll back together.
	OAuthClears []string
}

// ApplyEntityCommit writes one commit's batch in a single database
// Update transaction: the purges of Removes and Replaces first, then the OAuth
// clears, then the saves with their stamps, then the meta keys. Any invariant
// violation or write error aborts the whole transaction and nothing changes.
// It returns one PurgeReport per removed or replaced name.
func (m *Manager) ApplyEntityCommit(b *EntityCommit) (map[string]PurgeReport, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: nil batch", ErrInvariant)
	}
	if b.Generation <= 0 {
		return nil, fmt.Errorf("%w: generation %d", ErrInvariant, b.Generation)
	}
	deleted := make(map[string]bool, len(b.Removes)+len(b.Replaces))
	replaced := make(map[string]bool, len(b.Replaces))
	for _, n := range b.Replaces {
		replaced[n] = true
		deleted[n] = true
	}
	for _, n := range b.Removes {
		if replaced[n] {
			return nil, fmt.Errorf("%w: %q is both removed and replaced", ErrInvariant, n)
		}
		deleted[n] = true
	}
	saved := make(map[string]bool, len(b.Saves))
	for _, s := range b.Saves {
		if s.Config == nil || s.Config.Name == "" {
			return nil, fmt.Errorf("%w: save without a server name", ErrInvariant)
		}
		name := s.Config.Name
		if saved[name] {
			return nil, fmt.Errorf("%w: %q is saved twice", ErrInvariant, name)
		}
		saved[name] = true
		if deleted[name] && !replaced[name] {
			return nil, fmt.Errorf("%w: %q is both removed and saved", ErrInvariant, name)
		}
		if s.Stamp.Gen <= 0 || s.Stamp.Incarnation <= 0 || s.Stamp.OAuthEpoch <= 0 {
			return nil, fmt.Errorf("%w: %q has a zero stamp %+v", ErrInvariant, name, s.Stamp)
		}
		if s.Stamp.Gen > b.Generation || s.Stamp.Incarnation > b.Generation || s.Stamp.OAuthEpoch > b.Generation {
			return nil, fmt.Errorf("%w: %q stamp %+v is above the commit generation %d", ErrInvariant, name, s.Stamp, b.Generation)
		}
	}
	for n := range replaced {
		if !saved[n] {
			return nil, fmt.Errorf("%w: replaced %q has no new row", ErrInvariant, n)
		}
	}
	for _, n := range b.OAuthClears {
		if !saved[n] {
			return nil, fmt.Errorf("%w: OAuth clear for %q without a row save", ErrInvariant, n)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var reports map[string]PurgeReport
	err := m.db.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(ServersBucket))
		if bucket == nil {
			return fmt.Errorf("servers bucket not found")
		}
		// A removed or replaced name loses its whole purge set
		// in this transaction, so a removal never commits without its purge
		// and a re-add (in this commit or later) inherits nothing.
		reports = make(map[string]PurgeReport, len(deleted))
		for _, name := range sortedPurgeNames(b) {
			rep, err := purgeServerTx(tx, name)
			if err != nil {
				return fmt.Errorf("purge %s: %w", name, err)
			}
			reports[name] = rep
		}
		// Before the saves, so any later failure in this transaction rolls
		// the clear back together with the rows.
		if len(b.OAuthClears) > 0 {
			if tokens := tx.Bucket([]byte(OAuthTokenBucket)); tokens != nil {
				for _, name := range b.OAuthClears {
					if _, err := clearOAuthStateTx(tokens, name); err != nil {
						return fmt.Errorf("clear OAuth state for %s: %w", name, err)
					}
				}
			}
		}
		for _, s := range b.Saves {
			record := serverRecordFromConfig(s.Config)
			record.Generation = s.Stamp.Gen
			record.Incarnation = s.Stamp.Incarnation
			record.OAuthEpoch = s.Stamp.OAuthEpoch

			data, err := record.MarshalBinary()
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(record.Name), data); err != nil {
				return err
			}
		}
		if err := applyProfilesTx(tx, b); err != nil {
			return err
		}
		meta, err := tx.CreateBucketIfNotExists([]byte(MetaBucket))
		if err != nil {
			return err
		}
		if err := meta.Put([]byte(ConfigCommitGenerationKey), []byte(strconv.FormatInt(b.Generation, 10))); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return reports, nil
}

// GetServerRecord returns a canonical server and its committed generation stamps.
func (m *Manager) GetServerRecord(name string) (*ServerRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.db.GetServer(name)
}
