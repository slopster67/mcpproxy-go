package oauth

import (
	"github.com/slopster67/mcpproxy-go/internal/contracts"
)

// RedactServerSecretFields masks the secret-bearing fields of a server object
// in place. It is the MCP management tools' read rule; the admin dashboard
// REST API returns server objects unmasked.
func RedactServerSecretFields(server *contracts.Server) {
	if server == nil {
		return
	}
	if len(server.Headers) > 0 {
		headers := make(map[string]string, len(server.Headers))
		for k, v := range server.Headers {
			headers[k] = LiveRedaction.HeaderValue(k, v)
		}
		server.Headers = headers
	}
	if len(server.Env) > 0 {
		env := make(map[string]string, len(server.Env))
		for k, v := range server.Env {
			env[k] = LiveRedaction.EnvValue(k, v)
		}
		server.Env = env
	}
	if server.URL != "" {
		server.URL = LiveRedaction.URLValue(server.URL)
	}
	// `command` and `working_dir` go through the same leaf rule the generic
	// walk applies, so every door renders them identically. The rule passes
	// ordinary values (`npx`, `/opt/tools`) through byte-identical and catches
	// a token pasted into either field.
	if server.Command != "" {
		server.Command = LiveRedaction.CommandString(server.Command)
	}
	if server.WorkingDir != "" {
		server.WorkingDir = LiveRedaction.Leaf("working_dir", server.WorkingDir)
	}
	if len(server.Args) > 0 {
		server.Args = LiveRedaction.Argv(server.Args)
	}
	if server.OAuth != nil {
		// Copy the struct rather than writing through the pointer: the
		// server object may share its OAuth block with the caller's config.
		masked := *server.OAuth
		masked.ClientID = LiveRedaction.Leaf("client_id", masked.ClientID)
		masked.ClientSecret = LiveRedaction.Leaf("client_secret", masked.ClientSecret)
		masked.RedirectURI = LiveRedaction.URLValue(masked.RedirectURI)
		masked.AuthorizationEndpoint = LiveRedaction.URLValue(masked.AuthorizationEndpoint)
		masked.TokenEndpoint = LiveRedaction.URLValue(masked.TokenEndpoint)
		masked.RegistrationEndpoint = LiveRedaction.URLValue(masked.RegistrationEndpoint)
		masked.AuthServerMetadataURL = LiveRedaction.URLValue(masked.AuthServerMetadataURL)
		if len(masked.ExtraParams) > 0 {
			params := make(map[string]string, len(masked.ExtraParams))
			for k, v := range masked.ExtraParams {
				params[k] = LiveRedaction.Leaf(k, v)
			}
			masked.ExtraParams = params
		}
		// A scope slot is free text an agent can paste a credential into.
		if len(masked.Scopes) > 0 {
			scopes := make([]string, len(masked.Scopes))
			for i, v := range masked.Scopes {
				scopes[i] = LiveRedaction.Leaf("scopes", v)
			}
			masked.Scopes = scopes
		}
		server.OAuth = &masked
	}

	if server.LastError != "" {
		server.LastError = ScrubUpstreamText(server.LastError)
	}
	// retry_stopped_reason is free-form UPSTREAM text, not a fixed catalog
	// string: diagnostics.PermanentFailureReason falls through to the raw
	// connect error (which can carry the child process's stderr and the
	// upstream URL with its query credentials) when the terminal code has no
	// catalog message. So it is scrubbed like LastError.
	if server.RetryStoppedReason != "" {
		server.RetryStoppedReason = MaskDetectedSecrets(RedactSensitiveData(server.RetryStoppedReason))
	}
	if server.Health != nil && server.Health.Detail != "" {
		health := *server.Health
		health.Detail = ScrubUpstreamText(health.Detail)
		server.Health = &health
	}
	// The diagnostic's Cause echoes the raw connect error, which carries the full upstream URL (query secrets and all); scrub it in parity
	// with LastError / Health.Detail.
	if server.Diagnostic != nil && server.Diagnostic.Cause != "" {
		diag := *server.Diagnostic
		diag.Cause = ScrubUpstreamText(diag.Cause)
		server.Diagnostic = &diag
	}
}
