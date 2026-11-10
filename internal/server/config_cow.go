package server

import (
	"errors"
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/runtime"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// ServerExistsError reports an existing server name.
type ServerExistsError = runtime.ServerExistsError

// createServer commits the server to the database before publishing it.
func (s *Server) createServer(sc *config.ServerConfig) error {
	if h := s.createAfterConfigRead; h != nil {
		h(sc.Name)
	}
	_, err := s.runtime.Commit("create_server", func(tx *runtime.CommitTx) error {
		return tx.CreateServer(sc)
	})
	if err == nil {
		return nil
	}
	var exists *ServerExistsError
	if errors.As(err, &exists) {
		return exists
	}
	return fmt.Errorf("failed to save server: %w", err)
}

// removeServerFromConfig atomically deletes the server and its dependent records.
func (s *Server) removeServerFromConfig(name string) (storage.PurgeReport, error) {
	if s.runtime == nil {
		return storage.PurgeReport{}, fmt.Errorf("runtime not available")
	}
	// The informational-scan bookkeeping of a removed server is forgotten by
	// the purge observer (forgetInformationalScansOnPurge), which runs only
	// after the commit persisted: a removal that fails to persist leaves the
	// server configured and must keep its known mark and retry budget.
	res, err := s.runtime.Commit("remove_server", func(tx *runtime.CommitTx) error {
		return tx.RemoveServer(name)
	})
	if err != nil {
		return storage.PurgeReport{}, err
	}
	rep := storage.PurgeReport{Server: name}
	if res != nil {
		if r, ok := res.Purged[name]; ok {
			rep = r
		}
	}
	return rep, nil
}
