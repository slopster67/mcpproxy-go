package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/blevesearch/bleve/v2"

	"github.com/slopster67/mcpproxy-go/internal/config"

	"go.uber.org/zap"
)

// profilesDirName is the subdirectory of index.bleve that holds per-profile
// indexes: <dataDir>/index.bleve/profiles/<id>/. Per-profile indexes are kept
// under their own namespace (rather than directly under index.bleve/<id>/) so
// they never collide with the shared index's internal entries — Bleve creates
// its own `store/` subdirectory and metadata files directly under index.bleve,
// and a sibling dir there could be mistaken for a profile during cleanup.
const profilesDirName = "profiles"

// Manager provides a unified interface for indexing operations.
//
// A root Manager (created via NewManager) owns the shared default index at
// <dataDir>/index.bleve plus a lazily-populated map of per-profile sub-Managers.
// Each sub-Manager, obtained via ForProfile, wraps its
// own index at <dataDir>/index.bleve/profiles/<id>/ and reuses the full Manager
// method surface. Sub-Managers do not themselves nest further (their profiles map
// is nil), so ForProfile is only meaningful on the root.
type Manager struct {
	bleveIndex *BleveIndex
	mu         sync.RWMutex
	logger     *zap.Logger

	dataDir  string              // root data dir; "" for sub-Managers
	profiles map[string]*Manager // id -> per-profile sub-Manager (root only)
}

// profilesBaseDir returns <dataDir>/index.bleve/profiles, the parent directory
// of all per-profile indexes.
func (m *Manager) profilesBaseDir() string {
	return filepath.Join(m.dataDir, "index.bleve", profilesDirName)
}

// profileIndexPath returns the on-disk index path for a profile id.
func (m *Manager) profileIndexPath(id string) string {
	return filepath.Join(m.profilesBaseDir(), id)
}

// NewManager creates a new root index manager backed by the shared default index.
func NewManager(dataDir string, logger *zap.Logger) (*Manager, error) {
	bleveIndex, err := NewBleveIndex(dataDir, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create Bleve index: %w", err)
	}

	return &Manager{
		bleveIndex: bleveIndex,
		logger:     logger,
		dataDir:    dataDir,
		profiles:   make(map[string]*Manager),
	}, nil
}

// NewMemoryManager indexes a fixed catalog using the same mapping and search
// implementation as upstream tools, without creating any files.
func NewMemoryManager(logger *zap.Logger) (*Manager, error) {
	idx, err := bleve.NewMemOnly(currentIndexMapping())
	if err != nil {
		return nil, err
	}
	return &Manager{bleveIndex: &BleveIndex{index: idx, logger: logger}, logger: logger}, nil
}

// Close closes the index manager and every per-profile index it opened.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	for id, sub := range m.profiles {
		if sub.bleveIndex != nil {
			if err := sub.bleveIndex.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(m.profiles, id)
	}

	if m.bleveIndex != nil {
		if err := m.bleveIndex.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BatchIndexTools indexes multiple tools efficiently
func (m *Manager) BatchIndexTools(tools []*config.ToolMetadata) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.bleveIndex.BatchIndex(tools)
}

// SearchTools searches for tools matching the query
func (m *Manager) SearchTools(query string, limit int) ([]*config.SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 20 // default limit
	}

	return m.bleveIndex.SearchTools(query, limit)
}

// SearchToolsScoped is SearchTools filtered to the servers inScope admits
// BEFORE the ranked cut. See BleveIndex.SearchToolsScoped.
func (m *Manager) SearchToolsScoped(query string, limit int, inScope func(serverName string) bool) ([]*config.SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 20 // default limit, as SearchTools
	}

	return m.bleveIndex.SearchToolsScoped(query, limit, inScope)
}

// SearchToolsAdmitted is SearchToolsScoped's hit-level counterpart: the
// predicate resolves per-hit Admission (Admit/RejectScope/
// RejectPolicy) over the canonical (server, tool) identity. See
// BleveIndex.SearchToolsAdmitted.
func (m *Manager) SearchToolsAdmitted(query string, limit int, admit func(Hit) Admission) ([]*config.SearchResult, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 20 // default limit, as SearchTools
	}

	return m.bleveIndex.SearchToolsAdmitted(query, limit, admit)
}

// DeleteTool removes a tool from the index
func (m *Manager) DeleteTool(serverName, toolName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.bleveIndex.DeleteTool(serverName, toolName)
}

// DeleteServerTools removes all tools from a specific server
func (m *Manager) DeleteServerTools(serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.bleveIndex.DeleteServerTools(serverName)
}

// GetAllIndexedServerNames returns all unique server names in the index.
func (m *Manager) GetAllIndexedServerNames() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.bleveIndex.GetAllIndexedServerNames()
}

// GetToolsByServer retrieves all tools from a specific server
func (m *Manager) GetToolsByServer(serverName string) ([]*config.ToolMetadata, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.bleveIndex.GetToolsByServer(serverName)
}

// ForProfile returns the index Manager scoped to the named profile, backed by a
// physically separate index at <dataDir>/index.bleve/profiles/<id>/. The
// per-profile index is opened (or created) lazily on first use and cached, so
// repeated calls for the same id return the same handle. An empty id returns
// the root (shared default) Manager. The id must be a valid, filesystem-safe
// profile id (see config.IsValidProfileID) — invalid ids are rejected to
// prevent path traversal. Only valid on a root Manager.
func (m *Manager) ForProfile(id string) (*Manager, error) {
	if id == "" {
		return m, nil
	}
	if m.profiles == nil {
		return nil, fmt.Errorf("ForProfile is only valid on the root index manager")
	}
	if !config.IsValidProfileID(id) {
		return nil, fmt.Errorf("invalid profile id %q: not filesystem-safe", id)
	}

	// Fast path: already opened.
	m.mu.RLock()
	if sub, ok := m.profiles[id]; ok {
		m.mu.RUnlock()
		return sub, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-check under the write lock in case of a concurrent opener.
	if sub, ok := m.profiles[id]; ok {
		return sub, nil
	}

	indexPath := m.profileIndexPath(id)
	bleveIndex, err := newBleveIndexAt(indexPath, m.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to open profile index %q: %w", id, err)
	}

	sub := &Manager{
		bleveIndex: bleveIndex,
		logger:     m.logger,
		// Sub-Managers carry no dataDir/profiles map: they do not nest further.
	}
	m.profiles[id] = sub
	m.logger.Info("Opened per-profile index", zap.String("profile", id), zap.String("path", indexPath))
	return sub, nil
}

// ClearAll removes every document from this index, leaving it empty.
func (m *Manager) ClearAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.bleveIndex.DeleteAll()
}

// RebuildProfileFromShared (re)builds the named profile's index to contain
// exactly the tools of the given servers, sourced from the shared default index.
// The profile index is cleared first, so the operation is idempotent and only
// ever touches the named profile — other profiles are left untouched (reload
// isolation). Servers with no indexed tools contribute nothing; an empty server
// list yields an empty profile index.
func (m *Manager) RebuildProfileFromShared(id string, servers []string) error {
	pm, err := m.ForProfile(id)
	if err != nil {
		return err
	}

	if err := pm.ClearAll(); err != nil {
		return fmt.Errorf("failed to clear profile index %q: %w", id, err)
	}

	var tools []*config.ToolMetadata
	for _, srv := range servers {
		srvTools, err := m.GetToolsByServer(srv)
		if err != nil {
			return fmt.Errorf("failed to read shared index for server %q: %w", srv, err)
		}
		tools = append(tools, srvTools...)
	}

	if len(tools) == 0 {
		return nil
	}
	return pm.BatchIndexTools(tools)
}

// DropProfile closes the named profile's index (if open) and removes its on-disk
// directory. It is a no-op if the profile was never opened and its directory does
// not exist. The empty (default) id cannot be dropped.
func (m *Manager) DropProfile(id string) error {
	if id == "" {
		return fmt.Errorf("cannot drop the shared default index")
	}
	if m.dataDir == "" {
		return fmt.Errorf("DropProfile is only valid on the root index manager")
	}
	if !config.IsValidProfileID(id) {
		return fmt.Errorf("invalid profile id %q: not filesystem-safe", id)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if sub, ok := m.profiles[id]; ok {
		if sub.bleveIndex != nil {
			if err := sub.bleveIndex.Close(); err != nil {
				m.logger.Warn("Failed to close profile index before drop",
					zap.String("profile", id), zap.Error(err))
			}
		}
		delete(m.profiles, id)
	}

	dir := m.profileIndexPath(id)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("failed to remove profile index dir %q: %w", dir, err)
	}
	m.logger.Info("Dropped per-profile index", zap.String("profile", id), zap.String("path", dir))
	return nil
}

// ExistingProfileDirs returns the ids of every per-profile index directory
// present on disk under <dataDir>/index.bleve/profiles/, regardless of whether
// it is currently open. Only valid, filesystem-safe id directories are
// returned. Used to reclaim profile indexes whose profile no longer exists.
func (m *Manager) ExistingProfileDirs() ([]string, error) {
	if m.dataDir == "" {
		return nil, nil
	}
	base := m.profilesBaseDir()
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read index dir %q: %w", base, err)
	}

	var ids []string
	for _, e := range entries {
		if e.IsDir() && config.IsValidProfileID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}
