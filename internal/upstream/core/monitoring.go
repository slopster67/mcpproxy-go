package core

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/slopster67/mcpproxy-go/internal/logs"
	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

const (
	// maxRecentStderrLines bounds the per-client ring buffer of recent
	// stderr output. Kept small because this is meant for the last few
	// lines before a failure — not a log archive.
	maxRecentStderrLines = 20
	// maxStderrLineLen truncates individual lines stored in the ring
	// buffer to protect memory against a misbehaving child that spews
	// huge single lines (e.g. a base64-encoded traceback).
	maxStderrLineLen = 512
)

// StartStderrMonitoring starts monitoring stderr output and logging it
func (c *Client) StartStderrMonitoring() {
	c.monitoringMu.Lock()
	defer c.monitoringMu.Unlock()

	if c.stderr == nil || c.transportType != transportStdio {
		return
	}

	// Capture the stderr reader as a local under monitoringMu. connectStdio
	// reassigns c.stderr on every (re)connect; passing the reader as a
	// goroutine arg keeps monitorStderr from reading the shared field, so a
	// later reconnect's write never races a lingering monitor's read.
	stderr := c.stderr

	// Create context for stderr monitoring. The monitor goroutine receives the
	// context, stderr reader, and its done channel as locals so an abandoned
	// (timed-out) goroutine never reads the shared fields a later Start may
	// overwrite.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.stderrMonitoringCancel = cancel
	c.stderrMonitoringDone = done

	go func() {
		defer close(done)
		c.monitorStderr(ctx, stderr)
	}()

	c.logger.Debug("Started stderr monitoring",
		zap.String("server", c.config.Name))
}

// StopStderrMonitoring stops stderr monitoring
func (c *Client) StopStderrMonitoring() {
	c.monitoringMu.Lock()
	defer c.monitoringMu.Unlock()

	if c.stderrMonitoringCancel == nil {
		return
	}

	c.stderrMonitoringCancel()
	done := c.stderrMonitoringDone
	c.stderrMonitoringCancel = nil
	c.stderrMonitoringDone = nil
	if done == nil {
		return
	}

	// Wait for the monitor goroutine directly under monitoringMu (no detached
	// waiter that could outlive the lock). On timeout the goroutine is abandoned;
	// it closes its own done channel and touches only its captured ctx, so it
	// cannot race a subsequent Start.
	select {
	case <-done:
		c.logger.Debug("Stopped stderr monitoring",
			zap.String("server", c.config.Name))
	case <-time.After(500 * time.Millisecond):
		c.logger.Warn("Stderr monitoring stop timed out after 500ms, forcing shutdown",
			zap.String("server", c.config.Name))
	}
}

// monitorStderr monitors stderr output and logs it to both main and server-specific logs.
// The stderr reader is passed as an argument (captured under monitoringMu by the
// caller) rather than read from c.stderr, so a concurrent connectStdio reassigning
// the shared field cannot race this goroutine's read.
func (c *Client) monitorStderr(ctx context.Context, stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			// The child's stderr lands in three places from here: main.log,
			// the per-server log served over REST, and the recent-stderr ring
			// buffer spliced into initialize errors. A child may print its own
			// credentials, so the line is scrubbed once here for all three.
			line = oauth.ScrubUpstreamText(line)

			// Log to main logger
			c.logger.Info("stderr output",
				zap.String("server", c.config.Name),
				zap.String("message", line))

			if c.upstreamLogger != nil {
				c.upstreamLogger.Info("stderr", zap.String("message", line), logs.ChildOutputField())
			}

			c.recordRecentStderr(line)
		}
	}

	// Check for scanner errors - this is crucial for detecting pipe issues
	if err := scanner.Err(); err != nil {
		// Distinguish between different error types
		if strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "closed pipe") {
			c.logger.Error("Stdin/stdout pipe closed - container may have died",
				zap.String("server", c.config.Name),
				zap.Error(err))
		} else {
			c.logger.Warn("Error reading stderr",
				zap.String("server", c.config.Name),
				zap.Error(err))
		}

		if c.upstreamLogger != nil {
			c.upstreamLogger.Warn("stderr read error", zap.Error(err))
		}
	} else {
		// If scanner ended without error, the pipe was likely closed gracefully
		c.logger.Info("Stderr stream ended",
			zap.String("server", c.config.Name))

		if c.upstreamLogger != nil {
			c.upstreamLogger.Info("stderr stream closed")
		}
	}
}

// recordRecentStderr appends a stderr line to the bounded ring buffer.
func (c *Client) recordRecentStderr(line string) {
	if line == "" {
		return
	}
	if len(line) > maxStderrLineLen {
		line = line[:maxStderrLineLen] + "…"
	}
	c.recentStderrMu.Lock()
	defer c.recentStderrMu.Unlock()
	c.recentStderr = append(c.recentStderr, line)
	if overflow := len(c.recentStderr) - maxRecentStderrLines; overflow > 0 {
		c.recentStderr = append([]string(nil), c.recentStderr[overflow:]...)
	}
}

// recentStderrSnapshot returns a copy of the recent stderr lines captured
// from the child process. Returns nil if nothing has been captured yet.
func (c *Client) recentStderrSnapshot() []string {
	c.recentStderrMu.Lock()
	defer c.recentStderrMu.Unlock()
	if len(c.recentStderr) == 0 {
		return nil
	}
	out := make([]string, len(c.recentStderr))
	copy(out, c.recentStderr)
	return out
}

func (c *Client) formatRecentStderr() string {
	lines := c.recentStderrSnapshot()
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	if hint := commandNotFoundHint(lines); hint != "" {
		b.WriteString(hint)
		b.WriteByte('\n')
	}
	for _, l := range collapseRepeatedLines(lines) {
		b.WriteString("  | ")
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// collapseRepeatedLines collapses runs of identical consecutive lines into a
// single "<line> (repeated N×)" entry. Non-repeated lines pass through verbatim.
func collapseRepeatedLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		if n := j - i; n > 1 {
			out = append(out, fmt.Sprintf("%s (repeated %d×)", lines[i], n))
		} else {
			out = append(out, lines[i])
		}
		i = j
	}
	return out
}

var (
	cmdNotFoundZshRe  = regexp.MustCompile(`command not found: (\S+)`)
	cmdNotFoundBashRe = regexp.MustCompile(`([^\s:]+): command not found`)
)

// extractMissingCommand returns the name of a binary the shell could not find
// in a stderr line, or "" if the line is not a "command not found" error.
func extractMissingCommand(line string) string {
	if m := cmdNotFoundZshRe.FindStringSubmatch(line); m != nil {
		return strings.Trim(m[1], `"'`)
	}
	if m := cmdNotFoundBashRe.FindStringSubmatch(line); m != nil {
		return strings.Trim(m[1], `"'`)
	}
	return ""
}

func commandNotFoundHint(lines []string) string {
	for _, l := range lines {
		cmd := extractMissingCommand(l)
		if cmd == "" {
			continue
		}

		return fmt.Sprintf("Command %q not found on the spawn PATH. Ensure it is installed and on PATH, then restart the affected servers.", cmd)
	}
	return ""
}
