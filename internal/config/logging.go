package config

import (
	"os"
	"path/filepath"
)

// EffectiveLogConfig resolves an omitted directory relative to the active data directory.
func (c *Config) EffectiveLogConfig() *LogConfig {
	var logging LogConfig
	if c.Logging != nil {
		logging = *c.Logging
	} else {
		logging = *DefaultConfig().Logging
	}
	if logging.LogDir == "" {
		dataDir := c.DataDir
		if dataDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				dataDir = filepath.Join(home, DefaultDataDir)
			}
		}
		logging.LogDir = filepath.Join(dataDir, "logs")
	}
	return &logging
}
