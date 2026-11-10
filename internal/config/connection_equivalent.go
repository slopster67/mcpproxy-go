package config

import "reflect"

// ConnectionEquivalent reports whether two snapshots of the same server carry
// identical CONNECTION settings — everything that decides how mcpproxy dials or
// spawns the upstream.
//
// It is the supervisor's "did the user change something?" predicate. A
// permanently failed server is parked until its connection settings change,
// so a missed field here is a user who fixes their `args` and never sees the
// server come back.
//
// Deliberately EXCLUDED: Name (identity, keyed elsewhere), Created, and Updated.
// Updated is rewritten by unrelated config writes (quarantine bookkeeping, tool
// hashes), so including it would reconnect every server on the next tick.
// Also excluded are settings that do not affect the connection itself — trust
// mode, tool allow/deny lists, concurrency limits, output formatting — which are
// applied in place without a redial.
func ConnectionEquivalent(a, b *ServerConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ExecutionHost() == b.ExecutionHost() && a.URL == b.URL &&
		a.Protocol == b.Protocol &&
		a.Command == b.Command &&
		a.WorkingDir == b.WorkingDir &&
		a.Enabled == b.Enabled &&
		a.LauncherWaitTimeout == b.LauncherWaitTimeout &&
		equalStringSlice(a.Args, b.Args) &&
		equalStringMap(a.Env, b.Env) &&
		equalStringMap(a.Headers, b.Headers) &&
		reflect.DeepEqual(a.OAuth, b.OAuth)
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || va != vb {
			return false
		}
	}
	return true
}
