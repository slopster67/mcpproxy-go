package configsvc

import (
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
)

// Snapshot is an immutable point-in-time view of the configuration.
// It allows lock-free reads without blocking on disk I/O or mutations.
type Snapshot struct {
	Config *config.Config

	// Version is monotonically increasing. It is the commit
	// generation G of the commit that published this snapshot, and it is
	// seeded from storage at startup, so it also increases across restarts.
	Version   int64
	Timestamp time.Time
	// ServerGenerations holds each configured server's committed generation
	// restored from the database at startup and immutable once published.
	ServerGenerations map[string]ServerGen
}

// ServerGeneration returns the committed generation of one server.
func (s *Snapshot) ServerGeneration(name string) (ServerGen, bool) {
	if s == nil || s.ServerGenerations == nil {
		return ServerGen{}, false
	}
	g, ok := s.ServerGenerations[name]
	return g, ok
}

// Clone creates a deep copy of the configuration to ensure immutability.
func (s *Snapshot) Clone() *config.Config { return config.CopyConfig(s.Config) }

// GetServer returns a copy of the server configuration by name, or nil if not found.
func (s *Snapshot) GetServer(name string) *config.ServerConfig {
	if s.Config == nil {
		return nil
	}
	for _, server := range s.Config.Servers {
		if server != nil && server.Name == name {
			return config.CopyServerConfig(server)
		}
	}
	return nil
}

// ServerCount returns the number of servers in this snapshot.
func (s *Snapshot) ServerCount() int {
	if s.Config == nil || s.Config.Servers == nil {
		return 0
	}
	return len(s.Config.Servers)
}
