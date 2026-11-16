package storage

import (
	"crypto/subtle"
	"encoding/json"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/database"
)

type refreshFamily struct {
	Token   string `json:"token"`
	Expires int64  `json:"expires"`
}

func (m *Manager) RotateRefreshToken(family, previous, next string, expires int64) (bool, error) {
	valid := false
	err := m.GetDB().Update(func(tx *database.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("oauth_refresh_families"))
		if err != nil {
			return err
		}
		key := []byte(family)
		var current refreshFamily
		raw := b.Get(key)
		if previous != "" {
			if len(raw) == 0 || json.Unmarshal(raw, &current) != nil || current.Expires <= time.Now().Unix() {
				return nil
			}
			if subtle.ConstantTimeCompare([]byte(current.Token), []byte(previous)) != 1 {
				return b.Delete(key)
			}
		} else if len(raw) != 0 {
			return nil
		}
		data, err := json.Marshal(refreshFamily{Token: next, Expires: expires})
		if err != nil {
			return err
		}
		if err := b.PutUntil(key, data, expires); err != nil {
			return err
		}
		valid = true
		return nil
	})
	return valid, err
}

func (m *Manager) RefreshFamilyActive(family string) bool {
	active := false
	err := m.GetDB().View(func(tx *database.Tx) error {
		b := tx.Bucket([]byte("oauth_refresh_families"))
		if b == nil {
			return nil
		}
		var current refreshFamily
		active = json.Unmarshal(b.Get([]byte(family)), &current) == nil && current.Expires > time.Now().Unix()
		return nil
	})
	return err == nil && active
}
