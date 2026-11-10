package launcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// defaultDialPerAttempt is the per-dial timeout while polling. Kept short so
// each retry can fire quickly while the listener is coming up.
const defaultDialPerAttempt = 1 * time.Second

// defaultDialPollInterval is how long we sleep between failed dials. Short
// enough to feel snappy on a fast-starting server, long enough that we don't
// spin the CPU waiting on a slow one.
const defaultDialPollInterval = 200 * time.Millisecond

// WaitForURLWithDial is WaitForURL with the probe dials made through dial
// (nil: a plain net.Dialer). The upstream core passes its instance's gated
// dialer, so a retired instance stops probing.
func WaitForURLWithDial(ctx context.Context, rawURL string, timeout time.Duration, dial func(ctx context.Context, network, addr string) (net.Conn, error)) error {
	addr, err := addrFromURL(rawURL)
	if err != nil {
		return err
	}

	if timeout < 0 {
		timeout = 0
	}

	dialCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if dial == nil {
		dial = (&net.Dialer{Timeout: defaultDialPerAttempt}).DialContext
	}
	var lastErr error
	for {
		// Honor the deadline / cancel before each attempt so a cancel
		// during sleep is observed promptly.
		if err := dialCtx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if lastErr != nil {
					return fmt.Errorf("url %s not reachable in %s (last dial error: %w)", rawURL, timeout, lastErr)
				}
				return fmt.Errorf("url %s not reachable in %s", rawURL, timeout)
			}
			return err
		}

		attemptCtx, attemptCancel := context.WithTimeout(dialCtx, defaultDialPerAttempt)
		conn, err := dial(attemptCtx, "tcp", addr)
		attemptCancel()
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err

		select {
		case <-dialCtx.Done():
			// Loop back so the deadline path above formats the error
			// consistently.
			continue
		case <-time.After(defaultDialPollInterval):
		}
	}
}

// addrFromURL extracts host:port from rawURL. If the URL omits a port, a
// default is inferred from the scheme (http→80, https→443). Unsupported
// schemes return an explanatory error so misconfigurations surface early.
func addrFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url %q has no host component", rawURL)
	}

	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("url %q has empty host", rawURL)
	}

	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http", "ws":
			port = "80"
		case "https", "wss":
			port = "443"
		case "":
			return "", fmt.Errorf("url %q has no scheme — cannot infer port", rawURL)
		default:
			return "", fmt.Errorf("url %q has unsupported scheme %q for TCP probe", rawURL, u.Scheme)
		}
	}

	return net.JoinHostPort(host, port), nil
}
