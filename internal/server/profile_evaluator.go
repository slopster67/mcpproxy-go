package server

import (
	"context"
	"sort"
	"sync"

	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/runtime"
)

// MCPProxyServer is the runtime.ProfileEvaluator: everything
// the profiles service needs from the unexported profile index, the shared
// per-tool gate and the search index.
var _ runtime.ProfileEvaluator = (*MCPProxyServer)(nil)

// profileToolCountsCache memoizes the administrator ToolCounts of each profile
// for one published (profileIndex, ToolTierGeneration) pair (F35): a list of N
// profiles over T tools would otherwise evaluate N*T chains per request.
type profileToolCountsCache struct {
	mu     sync.Mutex
	idx    *profileIndex
	gen    uint64
	counts map[string]runtime.ToolCounts
}

func (c *profileToolCountsCache) get(idx *profileIndex, gen uint64, name string) (runtime.ToolCounts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx != idx || c.gen != gen {
		return runtime.ToolCounts{}, false
	}
	v, ok := c.counts[name]
	return v, ok
}

func (c *profileToolCountsCache) put(idx *profileIndex, gen uint64, name string, v runtime.ToolCounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx != idx || c.gen != gen {
		c.idx, c.gen, c.counts = idx, gen, map[string]runtime.ToolCounts{}
	}
	c.counts[name] = v
}

func (p *MCPProxyServer) toolTierGeneration() uint64 {
	if p.mainServer == nil || p.mainServer.runtime == nil {
		return 0
	}
	if sup := p.mainServer.runtime.Supervisor(); sup != nil {
		return sup.ToolTierGeneration()
	}
	return 0
}

// ToolCounts implements runtime.ProfileEvaluator: the visible tools of the
// named profile by the tier the profile gives them, plus the unannotated tools
// it hides, memoized per published snapshot.
func (p *MCPProxyServer) ToolCounts(ctx context.Context, name string) runtime.ToolCounts {
	idx := p.profileIndexCurrent(ctx)
	gen := p.toolTierGeneration()
	if c, ok := p.toolCounts.get(idx, gen, name); ok {
		return c
	}
	ev, err := p.NewAccessEvaluator(profile.AccessSubject{Kind: profile.AccessSubjectProfile, Profile: name})
	if err != nil {
		return runtime.ToolCounts{}
	}
	var counts runtime.ToolCounts
	for _, t := range p.profileEvaluationTools() {
		v := ev.Evaluate(t.server, t.tool)
		switch {
		case v.Visible:
			switch v.ProfileTier {
			case profile.TierRead:
				counts.Read++
			case profile.TierWrite:
				counts.Write++
			case profile.TierDestructive:
				counts.Destructive++
			case profile.TierUnannotated:
				counts.Unannotated++
			}
		}
	}
	p.toolCounts.put(idx, gen, name, counts)
	return counts
}

// EffectiveTools implements runtime.ProfileEvaluator: one row per catalog tool
// with the verdict of the ONE access chain (the same rendering GET
// /tools?profile= uses), so `callable` equals what a real call would do.
func (p *MCPProxyServer) EffectiveTools(_ context.Context, name string, opt runtime.EffectiveToolsOptions) (*runtime.EffectiveToolsResult, error) {
	subject := profile.AccessSubject{Kind: profile.AccessSubjectProfile, Profile: name}
	if opt.Client != "" {
		subject = profile.AccessSubject{Kind: profile.AccessSubjectClient, ClientID: opt.Client, Profile: name}

	}
	ev, err := p.NewAccessEvaluator(subject)
	if err != nil {
		return nil, err
	}
	tools := p.profileEvaluationTools()
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].server != tools[j].server {
			return tools[i].server < tools[j].server
		}
		return tools[i].tool < tools[j].tool
	})

	res := &runtime.EffectiveToolsResult{Profile: name, Tools: []runtime.EffectiveTool{}}
	byReason := map[string]int{}
	callable := 0
	for _, t := range tools {
		v := ev.Evaluate(t.server, t.tool)
		if v.Visible {
			res.Counts.Visible++
		} else {
			res.Counts.Hidden++
		}
		if v.Callable {
			callable++
		}
		if v.Reason != profile.AccessReasonNone {
			byReason[string(v.Reason)]++
		}
		if opt.Server != "" && t.server != opt.Server {
			continue
		}
		if opt.Reason != "" && string(v.Reason) != opt.Reason {
			continue
		}
		res.Tools = append(res.Tools, runtime.EffectiveTool{
			Server: t.server, Tool: t.tool,
			IntrinsicTier: t.tier.String(), ProfileTier: v.ProfileTier.String(),
			Access: runtime.ToolAccessView{Visible: v.Visible, Callable: v.Callable, Reason: string(v.Reason)},
		})
	}
	res.Counts.Callable = &callable
	res.Counts.ByReason = byReason
	return res, nil
}
