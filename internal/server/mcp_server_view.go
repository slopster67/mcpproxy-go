package server

import (
	"github.com/slopster67/mcpproxy-go/internal/oauth"
)

func scrubUpstreamText(s string) string { return oauth.ScrubUpstreamText(s) }
