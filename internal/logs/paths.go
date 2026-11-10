package logs

import (
	"os"
	"path/filepath"
	"strings"
)

// GetLogDir returns the log directory for the default data directory.
func GetLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mcpproxy", "logs"), nil
}

// EnsureLogDir creates the log directory if it doesn't exist
func EnsureLogDir(logDir string) error {
	return os.MkdirAll(logDir, 0755)
}

// GetLogFilePath returns the full path for a log file in the standard log directory
func GetLogFilePath(filename string) (string, error) {
	logDir, err := GetLogDir()
	if err != nil {
		return "", err
	}

	if err := EnsureLogDir(logDir); err != nil {
		return "", err
	}

	return filepath.Join(logDir, filename), nil
}

// GetLogFilePathWithDir returns the full path for a log file in a custom log directory
func GetLogFilePathWithDir(logDir, filename string) (string, error) {
	if logDir == "" {
		return GetLogFilePath(filename)
	}

	// Expand user home directory if needed
	if strings.HasPrefix(logDir, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		logDir = filepath.Join(homeDir, logDir[2:])
	}

	if err := EnsureLogDir(logDir); err != nil {
		return "", err
	}

	return filepath.Join(logDir, filename), nil
}
