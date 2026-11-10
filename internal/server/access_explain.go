package server

import (
	"context"
	"strings"

	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/runtime"
)

// Access explanations use the same decisions as the tool inventory and report
// only concrete blockers. No passing-check or internal-source data is returned.

// splitExplainTool splits "server:tool" at the first ':'.
func splitExplainTool(tool string) (server, name string, ok bool) {
	i := strings.Index(tool, ":")
	if i <= 0 || i == len(tool)-1 {
		return "", "", false
	}
	return tool[:i], tool[i+1:], true
}

// Explain implements runtime.ProfileEvaluator.
func (p *MCPProxyServer) Explain(_ context.Context, subject profile.AccessSubject, tool string) (*runtime.AccessExplanation, error) {
	serverName, toolName, ok := splitExplainTool(tool)
	if !ok {
		return nil, profile.ErrExplainBuiltinTool
	}
	ev, err := p.NewAccessEvaluator(subject)
	if err != nil {
		return nil, err
	}
	verdict := ev.Evaluate(serverName, toolName)
	_, catalogued := p.EffectiveAnnotations(serverName, toolName)
	if !catalogued && verdict.Callable {
		verdict.Callable = false
		verdict.Issues = append(verdict.Issues, profile.AccessIssue{Code: "tool_missing", Message: "Tool is no longer available."})
	}
	out := &runtime.AccessExplanation{
		Subject: runtime.ExplainSubjectView{Kind: subject.Kind},
		Tool:    tool, Available: verdict.Callable, Issues: verdict.Issues,
	}
	if subject.Kind == profile.AccessSubjectClient {
		out.Subject.Name = subject.ClientID
	} else {
		out.Subject.Name = subject.Profile
	}
	return out, nil
}
