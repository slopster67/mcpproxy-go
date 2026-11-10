package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/slopster67/mcpproxy-go/internal/database"
)

// Purge-set report keys that are not bucket names.
const (
	// PurgeKeyToolCallBuckets counts the per-identity tool-call buckets
	// dropped for the server.
	PurgeKeyToolCallBuckets = "tool_call_buckets"
	// PurgeKeyServerIdentities counts the server identities dropped.
	PurgeKeyServerIdentities = serverIdentitiesBucket
)

// PurgeReport lists what one server's purge deleted, per bucket.
type PurgeReport struct {
	Server  string         `json:"server"`
	Deleted map[string]int `json:"deleted"`
}

// Total returns the number of keys the purge deleted.
func (r PurgeReport) Total() int {
	n := 0
	for _, c := range r.Deleted {
		n += c
	}
	return n
}

func purgeServerTx(tx *database.Tx, name string) (PurgeReport, error) {
	rep := PurgeReport{Server: name, Deleted: map[string]int{}}
	note := func(bucket string, n int) {
		if n > 0 {
			rep.Deleted[bucket] += n
		}
	}

	if b := tx.Bucket([]byte(ServersBucket)); b != nil && b.Get([]byte(name)) != nil {
		if err := b.Delete([]byte(name)); err != nil {
			return rep, err
		}
		note(ServersBucket, 1)
	}

	if b := tx.Bucket(peerOAuthBucket); b != nil && b.Get([]byte(name)) != nil {
		if err := b.Delete([]byte(name)); err != nil {
			return rep, err
		}
		note(string(peerOAuthBucket), 1)
	}

	identities, buckets, err := purgeToolCallHistoryTx(tx, name)
	if err != nil {
		return rep, fmt.Errorf("tool-call history: %w", err)
	}
	note(PurgeKeyServerIdentities, identities)
	note(PurgeKeyToolCallBuckets, buckets)

	prefix := []byte(name + ":")
	n, err := deleteKeysTx(tx, ToolApprovalBucket, func(k, _ []byte) bool { return bytes.HasPrefix(k, prefix) })
	if err != nil {
		return rep, fmt.Errorf("%s: %w", ToolApprovalBucket, err)
	}
	note(ToolApprovalBucket, n)

	if b := tx.Bucket([]byte(OAuthTokenBucket)); b != nil {
		n, err = clearOAuthStateTx(b, name)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", OAuthTokenBucket, err)
		}
		note(OAuthTokenBucket, n)
	}

	n, err = deleteKeysTx(tx, OAuthCompletionBucket, func(_, v []byte) bool {
		var ev OAuthCompletionEvent
		return json.Unmarshal(v, &ev) == nil && ev.ServerName == name
	})
	if err != nil {
		return rep, fmt.Errorf("%s: %w", OAuthCompletionBucket, err)
	}
	note(OAuthCompletionBucket, n)

	return rep, nil
}

// deleteKeysTx deletes every key of bucket for which match is true. The keys
// are collected before any delete (database cursors are invalidated by mutation
// during iteration). A missing bucket deletes nothing.
func deleteKeysTx(tx *database.Tx, bucket string, match func(k, v []byte) bool) (int, error) {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return 0, nil
	}
	var keys [][]byte
	if err := b.ForEach(func(k, v []byte) error {
		if v != nil && match(k, v) {
			keys = append(keys, append([]byte(nil), k...))
		}
		return nil
	}); err != nil {
		return 0, err
	}
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return 0, err
		}
	}
	return len(keys), nil
}

// purgeToolCallHistoryTx drops every server identity whose name is name, the
// tool-call bucket of each, and the name-keyed bucket of a caller that
// recorded without an identity. It returns the identities and buckets
// removed.
func purgeToolCallHistoryTx(tx *database.Tx, name string) (identities, buckets int, err error) {
	ids := []string{name}
	var identityKeys [][]byte
	if b := tx.Bucket([]byte(serverIdentitiesBucket)); b != nil {
		if err := b.ForEach(func(k, v []byte) error {
			var identity ServerIdentity
			if json.Unmarshal(v, &identity) != nil {
				return nil // unreadable: cannot prove it is this server's
			}
			if identity.ServerName == name {
				ids = append(ids, identity.ID)
				identityKeys = append(identityKeys, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return 0, 0, err
		}
		for _, k := range identityKeys {
			if err := b.Delete(k); err != nil {
				return 0, 0, err
			}
		}
	}
	for _, id := range ids {
		dropped, err := deleteToolCallBucketTx(tx, id)
		if err != nil {
			return len(identityKeys), buckets, err
		}
		if dropped {
			buckets++
		}
	}
	return len(identityKeys), buckets, nil
}

// sortedPurgeNames returns the deduplicated, sorted names a batch purges.
func sortedPurgeNames(b *EntityCommit) []string {
	seen := make(map[string]bool, len(b.Removes)+len(b.Replaces))
	var out []string
	for _, list := range [][]string{b.Removes, b.Replaces} {
		for _, n := range list {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}
