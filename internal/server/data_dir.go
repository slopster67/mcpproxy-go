package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// ValidateDataDirectory checks that the data directory has secure permissions
// This is called before creating socket listeners to ensure security
func ValidateDataDirectory(dataDir string, logger *zap.Logger) error {
	if dataDir == "" {
		return fmt.Errorf("data directory not specified")
	}

	// Expand ~ if present
	if strings.HasPrefix(dataDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot expand home directory: %w", err)
		}
		dataDir = filepath.Join(home, dataDir[2:])
	}

	logger.Info("Validating data directory security", zap.String("path", dataDir))

	// Check if directory exists
	info, err := os.Stat(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			// Try to create with secure permissions
			logger.Info("Data directory does not exist, creating with secure permissions", zap.String("path", dataDir))
			if err := os.MkdirAll(dataDir, 0700); err != nil {
				return fmt.Errorf("cannot create data directory: %w", err)
			}
			return nil // Created with secure permissions
		}
		return fmt.Errorf("cannot access data directory: %w", err)
	}

	// Check it's a directory
	if !info.IsDir() {
		return fmt.Errorf("data path exists but is not a directory: %s", dataDir)
	}

	// Platform-specific permission checks
	return validateDataDirectoryPermissionsPlatform(dataDir, info, logger)
}

// PermissionError represents a permission-related error (exit code 5)
type PermissionError struct {
	Path string
	Err  error
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("permission error for %s: %v", e.Path, e.Err)
}

func (e *PermissionError) Unwrap() error {
	return e.Err
}
