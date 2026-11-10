package storage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/database"
)

const ProfilesBucket = "profiles"

type EntityState struct {
	Servers    []*ServerRecord
	Profiles   []config.ProfileConfig
	Generation int64
}

// LoadEntities reads one consistent database snapshot before the runtime starts.
func (m *Manager) LoadEntities() (*EntityState, error) {
	state := &EntityState{Servers: []*ServerRecord{}, Profiles: []config.ProfileConfig{}}
	err := m.db.db.View(func(tx *database.Tx) error {
		if meta := tx.Bucket([]byte(MetaBucket)); meta != nil {
			if raw := meta.Get([]byte(ConfigCommitGenerationKey)); raw != nil {
				n, err := strconv.ParseInt(string(raw), 10, 64)
				if err != nil || n < 0 {
					return fmt.Errorf("invalid entity commit generation")
				}
				state.Generation = n
			}
		}
		if b := tx.Bucket([]byte(ServersBucket)); b != nil {
			if err := b.ForEach(func(key, raw []byte) error {
				var server ServerRecord
				if err := json.Unmarshal(raw, &server); err != nil {
					return fmt.Errorf("read server %q: %w", key, err)
				}
				if server.Name != string(key) || server.Generation <= 0 || server.Incarnation <= 0 || server.OAuthEpoch <= 0 || server.Generation > state.Generation || server.Incarnation > state.Generation || server.OAuthEpoch > state.Generation {
					return fmt.Errorf("invalid stored server %q", key)
				}
				state.Servers = append(state.Servers, &server)
				return nil
			}); err != nil {
				return err
			}
		}
		if b := tx.Bucket([]byte(ProfilesBucket)); b != nil {
			return b.ForEach(func(key, raw []byte) error {
				var profile config.ProfileConfig
				if err := json.Unmarshal(raw, &profile); err != nil {
					return fmt.Errorf("read profile %q: %w", key, err)
				}
				if profile.ID != string(key) {
					return fmt.Errorf("invalid stored profile %q", key)
				}
				state.Profiles = append(state.Profiles, profile)
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(state.Servers, func(i, j int) bool { return state.Servers[i].Name < state.Servers[j].Name })
	sort.Slice(state.Profiles, func(i, j int) bool { return state.Profiles[i].Name < state.Profiles[j].Name })
	return state, nil
}

type ClientProfileMove struct{ From, To string }

func applyProfilesTx(tx *database.Tx, batch *EntityCommit) error {
	if len(batch.ProfileSaves) > 0 || len(batch.ProfileRemoves) > 0 {
		bucket, err := tx.CreateBucketIfNotExists([]byte(ProfilesBucket))
		if err != nil {
			return err
		}
		for _, id := range batch.ProfileRemoves {
			if err := bucket.Delete([]byte(id)); err != nil {
				return err
			}
		}
		for _, profile := range batch.ProfileSaves {
			if profile.ID == "" {
				return fmt.Errorf("profile ID is required")
			}
			raw, err := json.Marshal(profile)
			if err != nil {
				return err
			}
			if err := bucket.Put([]byte(profile.ID), raw); err != nil {
				return err
			}
		}
	}
	if move := batch.ClientProfileMove; move != nil {
		if move.From == "" || move.To == "" {
			return fmt.Errorf("client profile move needs a source and target")
		}
		profiles := tx.Bucket([]byte(ProfilesBucket))
		if profiles == nil || profiles.Get([]byte(move.To)) == nil {
			return fmt.Errorf("target profile does not exist")
		}
		if clients := tx.Bucket([]byte(ClientCredentialsBucket)); clients != nil {
			return clients.ForEach(func(key, raw []byte) error {
				var client auth.ClientCredential
				if err := json.Unmarshal(raw, &client); err != nil {
					return err
				}
				if client.ProfilePin != move.From {
					return nil
				}
				client.ProfilePin = move.To
				data, err := json.Marshal(client)
				if err != nil {
					return err
				}
				return clients.Put(key, data)
			})
		}
	}
	return nil
}
