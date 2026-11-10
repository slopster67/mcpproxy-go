package storage

import (
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/database"
)

const secretsBucket = "secrets"

func (b *Database) GetSecret(name string) (string, error) {
	var value string
	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucket))
		if bucket == nil {
			return fmt.Errorf("secret %q not found", name)
		}
		raw := bucket.Get([]byte(name))
		if raw == nil {
			return fmt.Errorf("secret %q not found", name)
		}
		value = string(raw)
		return nil
	})
	return value, err
}
func (b *Database) SetSecret(name, value string) error {
	if name == "" || value == "" {
		return fmt.Errorf("secret name and value are required")
	}
	return b.db.Update(func(tx *database.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(secretsBucket))
		if err != nil {
			return err
		}
		return bucket.Put([]byte(name), []byte(value))
	})
}
func (b *Database) DeleteSecret(name string) error {
	return b.db.Update(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucket))
		if bucket == nil {
			return nil
		}
		return bucket.Delete([]byte(name))
	})
}
func (b *Database) ListSecrets() ([]string, error) {
	names := []string{}
	err := b.db.View(func(tx *database.Tx) error {
		bucket := tx.Bucket([]byte(secretsBucket))
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(k, v []byte) error { names = append(names, string(k)); return nil })
	})
	return names, err
}
func (b *Database) AuthenticationKey() []byte { return b.db.AuthenticationKey() }
