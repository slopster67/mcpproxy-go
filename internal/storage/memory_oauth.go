package storage

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slopster67/mcpproxy-go/internal/commitview"
)

// MemoryOAuth holds one server's credentials only in RAM. Mutations are queued
// in order until acknowledged centrally, including after an acknowledgement is
// lost. The provider's freshly rotated refresh token survives a network outage
// while this process lives.
type MemoryOAuth struct {
	mu        sync.Mutex
	name, key string
	snapshot  OAuthSnapshot
	record    *OAuthTokenRecord
	pending   []OAuthUpdate
	retired   bool
	sequence  uint64
	publish   func(OAuthUpdate) (uint64, error)
}

func NewMemoryOAuth(name string, snapshot OAuthSnapshot, publish func(OAuthUpdate) (uint64, error)) *MemoryOAuth {
	m := &MemoryOAuth{name: name, snapshot: snapshot, record: copyOAuth(snapshot.Record), publish: publish}
	m.snapshot.Record = nil
	if m.record != nil {
		m.key = m.record.ServerName
		m.sequence = m.record.Revision
	}
	return m
}
func copyOAuth(record *OAuthTokenRecord) *OAuthTokenRecord {
	if record == nil {
		return nil
	}
	out := *record
	out.Scopes = append([]string(nil), record.Scopes...)
	return &out
}
func (m *MemoryOAuth) Retire() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retired = true
	m.record = nil
	m.pending = nil
}
func (m *MemoryOAuth) Lease() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.retired {
		return ""
	}
	return m.snapshot.Lease
}
func (m *MemoryOAuth) Revision() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot.Revision
}
func (m *MemoryOAuth) matches(b commitview.CredentialBinding) bool {
	return !m.retired && m.snapshot.Binding == b
}
func (m *MemoryOAuth) Flush() error { m.mu.Lock(); defer m.mu.Unlock(); return m.flushLocked() }
func (m *MemoryOAuth) flushLocked() error {
	if m.publish == nil {
		return nil
	}
	for len(m.pending) > 0 && !m.retired {
		update := m.pending[0]
		update.Revision = m.snapshot.Revision
		revision, err := m.publish(update)
		if err != nil {
			if errors.Is(err, commitview.ErrStaleGeneration) {
				m.retired = true
				m.record = nil
				m.pending = nil
			}
			return err
		}
		m.snapshot.Revision = revision
		m.pending = m.pending[1:]
	}
	return nil
}
func (m *MemoryOAuth) queueLocked(record *OAuthTokenRecord) error {
	m.sequence++
	if record != nil {
		record.Revision = m.sequence
		record.Updated = time.Now()
		record.OAuthEpoch = m.snapshot.Binding.OAuthEpoch
	}
	m.record = copyOAuth(record)
	m.pending = append(m.pending, OAuthUpdate{Binding: m.snapshot.Binding, Lease: m.snapshot.Lease, ID: uuid.NewString(), Key: m.key, Record: copyOAuth(record)})
	// Network failures keep the new credential and its pending write in RAM.
	// A rejected lease is different: it must never become usable locally.
	if err := m.flushLocked(); errors.Is(err, commitview.ErrStaleGeneration) {
		return err
	}
	return nil
}
func (m *MemoryOAuth) GetOAuthTokenBound(key string, b commitview.CredentialBinding) (*OAuthTokenRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.matches(b) || m.record == nil || m.record.ServerName != key {
		return nil, fmt.Errorf("oauth token not found")
	}
	return copyOAuth(m.record), nil
}
func (m *MemoryOAuth) GetOAuthClientCredentialsBound(key string, b commitview.CredentialBinding) (string, string, int, string, uint64, error) {
	rec, err := m.GetOAuthTokenBound(key, b)
	if err != nil {
		return "", "", 0, "", 0, nil
	}
	return rec.ClientID, rec.ClientSecret, rec.CallbackPort, rec.RedirectURI, rec.Revision, nil
}
func (m *MemoryOAuth) UpdateOAuthTokenIfBound(key, name string, b commitview.CredentialBinding, mutate func(*OAuthTokenRecord) error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name != m.name || !m.matches(b) {
		return false, nil
	}
	rec := copyOAuth(m.record)
	if rec == nil || rec.ServerName != key {
		rec = &OAuthTokenRecord{ServerName: key, DisplayName: name, Created: time.Now()}
	}
	if err := mutate(rec); err != nil {
		if errors.Is(err, ErrSkipOAuthTokenUpdate) {
			return false, nil
		}
		return false, err
	}
	m.key = key
	rec.ServerName = key
	rec.DisplayName = name
	err := m.queueLocked(rec)
	return err == nil, err
}
func (m *MemoryOAuth) SaveOAuthClientIfBound(key, name string, b commitview.CredentialBinding, mode OAuthClientSaveMode, id, secret string, port int, uri string) (bool, error) {
	return m.UpdateOAuthTokenIfBound(key, name, b, func(rec *OAuthTokenRecord) error {
		if mode == OAuthClientSaveIfUnsetOrSame && rec.ClientID != "" && rec.ClientID != id {
			return ErrSkipOAuthTokenUpdate
		}
		if mode == OAuthClientSaveIfSameOrEmpty && rec.ClientID != id && (rec.ClientID != "" || rec.RefreshToken != "") {
			return ErrSkipOAuthTokenUpdate
		}
		rec.ClientID = id
		rec.ClientSecret = secret
		rec.CallbackPort = port
		rec.RedirectURI = uri
		return nil
	})
}
func (m *MemoryOAuth) clear(key, name string, b commitview.CredentialBinding, accept func(*OAuthTokenRecord) bool, remove bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name != m.name || !m.matches(b) || m.record == nil || m.key != key || !accept(m.record) {
		return false, nil
	}
	rec := copyOAuth(m.record)
	if remove {
		rec = nil
	} else {
		rec.ClientID = ""
		rec.ClientSecret = ""
		rec.CallbackPort = 0
		rec.RedirectURI = ""
	}
	err := m.queueLocked(rec)
	return err == nil, err
}
func (m *MemoryOAuth) DeleteOAuthTokenIfBound(key, name string, b commitview.CredentialBinding, rev uint64) (bool, error) {
	return m.clear(key, name, b, func(r *OAuthTokenRecord) bool { return r.Revision == rev }, true)
}
func (m *MemoryOAuth) ClearOAuthClientIfBound(key, name string, b commitview.CredentialBinding, rev uint64) (bool, error) {
	return m.clear(key, name, b, func(r *OAuthTokenRecord) bool { return r.Revision == rev }, false)
}
func (m *MemoryOAuth) ClearOAuthClientIfBoundAndClientID(key, name string, b commitview.CredentialBinding, id string) (bool, error) {
	return m.clear(key, name, b, func(r *OAuthTokenRecord) bool { return id != "" && r.ClientID == id }, false)
}
func (m *MemoryOAuth) SaveOAuthCompletionEventIfBound(event *OAuthCompletionEvent, b commitview.CredentialBinding) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return event.ServerName == m.name && m.matches(b), nil
}
func (m *MemoryOAuth) Token() *OAuthTokenRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copyOAuth(m.record)
}
