package configsvc

import (
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/commitview"
	"github.com/slopster67/mcpproxy-go/internal/config"
)

// ServerGen is the committed {generation, incarnation} of one server. It lives
// in the leaf package commitview so configsvc and storage never import each
// other.
type ServerGen = commitview.ServerGen

// ChangeKind classifies one server's change in a commit.
type ChangeKind int

const (
	// ChangeCreated: the name is absent from the base snapshot.
	ChangeCreated ChangeKind = iota + 1
	// ChangeUpdated: the entry exists in both and is not ServerEntryEqual.
	ChangeUpdated
	// ChangeRemoved: the name is absent from the new config.
	ChangeRemoved
	// ChangeReplaced: removed and re-created in one commit. Every
	// consumer treats it as Removed(Old) followed by Created(New).
	ChangeReplaced
)

// ServerChange is one server's change in a commit.
type ServerChange struct {
	Kind ChangeKind
	Old  ServerGen // zero when Created
	New  ServerGen // zero when Removed
	// Reconnect is true when the connection settings, Enabled
	// changed (config.ConnectionEquivalent is false); always true for
	// Created, Removed and Replaced.
	Reconnect bool
}

// PrepareInput is everything Prepare needs. Replaced names a server the
// commit removed and re-created: the final config alone cannot
// tell that from an unchanged or updated entry. Every replaced name must be
// present in both the base and the new config.
type PrepareInput struct {
	Config   *config.Config
	Source   string
	Type     UpdateType
	Replaced map[string]bool
	// OAuthCleared names the servers whose cached OAuth state this commit
	// clears because their OAuth configuration changed: their OAuthEpoch
	// moves to G, so an instance built for
	// the previous OAuth configuration holds a stale credential binding and
	// none of its OAuth writes can land after the clear.
	OAuthCleared map[string]bool
}

// PreparedSnapshot is a snapshot computed against a base version and not yet
// visible. Changes describes the commit from the same inputs, so the
// snapshot, the commit result and the stored rows cannot disagree.
type PreparedSnapshot struct {
	base       int64
	generation int64
	updateType UpdateType
	source     string
	snap       *Snapshot
	Changes    map[string]ServerChange
}

// Generation is the commit generation G this snapshot will publish as.
func (p *PreparedSnapshot) Generation() int64 { return p.generation }

// Snapshot is the snapshot that Publish will make current. Callers must treat
// it as read-only.
func (p *PreparedSnapshot) Snapshot() *Snapshot { return p.snap }

// computeGenerations applies the per-server generation rule:
//
//	replaced (checked first)        → {G, G, G}
//	absent in base (created)        → {G, G, G}
//	present, ServerEntryEqual       → base value (kept)
//	present, not equal              → {G, base.Incarnation, epoch}
//	absent in next                  → dropped
//
// where epoch is G when in.OAuthCleared names the server and
// base.OAuthEpoch otherwise. A base entry present in both configs must carry
// a complete committed stamp; one without is an error.
func computeGenerations(base *Snapshot, in PrepareInput, g int64) (map[string]ServerGen, map[string]ServerChange, error) {
	baseByName := map[string]*config.ServerConfig{}
	if base != nil && base.Config != nil {
		for _, sc := range base.Config.Servers {
			if sc != nil {
				baseByName[sc.Name] = sc
			}
		}
	}
	nextByName := map[string]*config.ServerConfig{}
	for _, sc := range in.Config.Servers {
		if sc == nil {
			continue
		}
		if _, dup := nextByName[sc.Name]; dup {
			return nil, nil, fmt.Errorf("configsvc: server %q is configured twice", sc.Name)
		}
		nextByName[sc.Name] = sc
	}
	for name := range in.Replaced {
		if baseByName[name] == nil || nextByName[name] == nil {
			return nil, nil, fmt.Errorf("configsvc: replaced server %q must be present in both the base and the new config", name)
		}
	}

	baseGen := func(name string) (ServerGen, bool) {
		if base == nil {
			return ServerGen{}, false
		}
		return base.ServerGeneration(name)
	}

	gens := make(map[string]ServerGen, len(nextByName))
	changes := map[string]ServerChange{}
	for name, next := range nextByName {
		prev := baseByName[name]
		switch {
		case in.Replaced[name]:
			old, _ := baseGen(name)
			gens[name] = ServerGen{Gen: g, Incarnation: g, OAuthEpoch: g}
			changes[name] = ServerChange{
				Kind: ChangeReplaced, Old: old, New: gens[name], Reconnect: true,
			}
		case prev == nil:
			gens[name] = ServerGen{Gen: g, Incarnation: g, OAuthEpoch: g}
			changes[name] = ServerChange{
				Kind: ChangeCreated, New: gens[name], Reconnect: true,
			}
		default:
			old, stamped := baseGen(name)
			equal := config.ServerEntryEqual(prev, next)
			if !stamped || old.Gen <= 0 || old.Incarnation <= 0 || old.OAuthEpoch <= 0 {
				return nil, nil, fmt.Errorf("server %q has no committed database identity", name)
			}
			epoch := old.OAuthEpoch
			if in.OAuthCleared[name] {
				epoch = g
			}
			if equal && epoch == old.OAuthEpoch {
				gens[name] = old
			} else {
				gens[name] = ServerGen{Gen: g, Incarnation: old.Incarnation, OAuthEpoch: epoch}
				changes[name] = ServerChange{Kind: ChangeUpdated, Old: old, New: gens[name], Reconnect: !config.ConnectionEquivalent(prev, next)}
			}
		}
	}
	for name := range baseByName {
		if nextByName[name] == nil {
			old, _ := baseGen(name)
			changes[name] = ServerChange{Kind: ChangeRemoved, Old: old, Reconnect: true}
		}
	}
	return gens, changes, nil
}
