package secureenv

import (
	"os"
	"strings"
)

// EnvConfig controls inheritance from the daemon environment.
type EnvConfig struct {
	AllowedSystemVars []string `env:"MCPPROXY_ENVIRONMENT_ALLOWED_SYSTEM_VARS" json:"allowed_system_vars"`
}

func DefaultEnvConfig() *EnvConfig {
	allowedVars := []string{
		"PATH",   // Essential for finding executables
		"HOME",   // User directory path (Unix)
		"TMPDIR", // Temporary directory (Unix)
		"TEMP",   // Temporary directory
		"TMP",    // Temporary directory
		"SHELL",  // Default shell
		"TERM",   // Terminal type
		"LANG",   // Language settings
		"USER",   // Current user (Unix)
	}

	allowedVars = append(allowedVars,
		"XDG_CONFIG_HOME", // XDG config directory
		"XDG_DATA_HOME",   // XDG data directory
		"XDG_CACHE_HOME",  // XDG cache directory
		"XDG_RUNTIME_DIR", // XDG runtime directory
	)

	// Add locale-related variables
	localeVars := []string{
		"LC_ALL", "LC_CTYPE", "LC_NUMERIC", "LC_TIME", "LC_COLLATE",
		"LC_MONETARY", "LC_MESSAGES", "LC_PAPER", "LC_NAME", "LC_ADDRESS",
		"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION",
	}
	allowedVars = append(allowedVars, localeVars...)

	allowedVars = append(allowedVars, "NVM_DIR", "ASDF_DIR", "PYENV_ROOT", "VOLTA_HOME", "HOMEBREW_PREFIX")

	return &EnvConfig{

		AllowedSystemVars: allowedVars,
	}
}

// Manager builds the environment for one local upstream process.
type Manager struct {
	config     *EnvConfig
	serverVars map[string]string
}

func NewManager(config *EnvConfig, serverVars map[string]string) *Manager {
	if config == nil {
		config = DefaultEnvConfig()
	}
	return &Manager{config: config, serverVars: serverVars}
}

func (m *Manager) BuildSecureEnvironment() []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, overridden := m.serverVars[key]; !overridden && m.isKeyAllowed(key) {
			env = append(env, value)
		}
	}
	for key, value := range m.serverVars {
		env = append(env, key+"="+value)
	}
	return env
}

func (m *Manager) isKeyAllowed(key string) bool {
	for _, allowed := range m.config.AllowedSystemVars {
		if strings.HasSuffix(allowed, "*") {
			if strings.HasPrefix(key, strings.TrimSuffix(allowed, "*")) {
				return true
			}
		} else if strings.EqualFold(allowed, key) {
			return true
		}
	}
	return false
}
