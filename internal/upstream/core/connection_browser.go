package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/slopster67/mcpproxy-go/internal/oauth"
	"go.uber.org/zap"
)

// openBrowser attempts to open the OAuth URL in the default browser
func (c *Client) openBrowser(authURL string) error {
	if c.suppressBrowser {
		return fmt.Errorf("Sign in using the primary dashboard")
	}
	var cmd string
	var args []string

	switch runtime.GOOS {
	case osDarwin:
		cmd = "open"
		args = []string{authURL}
	case osLinux:
		// Always attempt xdg-open, but warn when no GUI/session indicators are found.
		if !c.hasGUIEnvironment() {
			c.logger.Warn("No GUI session detected - attempting to launch browser anyway. If nothing appears, copy/paste the URL manually.",
				zap.String("server", c.config.Name))
		}

		if _, err := exec.LookPath("xdg-open"); err != nil {
			return fmt.Errorf("xdg-open not found in PATH: %w", err)
		}

		cmd = "xdg-open"
		args = []string{authURL}
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	// The browser launch is an admitted section of
	// the instance's gate, so a retired instance opens no login page. The
	// code exchange and token write it would lead to are gated separately
	// (the OAuth HTTP client runs on the instance transport).
	release, err := c.Gate().Admit(context.Background())
	if err != nil {
		return fmt.Errorf("browser launch refused: %w", err)
	}
	defer release()

	return exec.Command(cmd, args...).Start()
}

// hasGUIEnvironment checks if a GUI environment is available on Linux
func (c *Client) hasGUIEnvironment() bool {
	// Check for common environment variables that indicate GUI
	envVars := []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE"}

	for _, envVar := range envVars {
		if value := os.Getenv(envVar); value != "" {
			return true
		}
	}

	return false
}

// shouldDeferOAuth avoids opening a browser during automatic reconnects.
func (c *Client) shouldDeferOAuth(ctx context.Context) bool {
	if c.isManualOAuthFlow(ctx) {
		return false
	}
	manager := oauth.GetTokenStoreManager()
	return manager != nil && !manager.HasRecentOAuthCompletionBound(c.config.Name, c.CredentialBinding())
}
