package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
)

type ProfileChange struct {
	Kind            profile.ChangeKind
	Profile         string
	PreviousProfile string
	Diff            map[string]interface{}
}

// ChangeHint lets a service that performs ONE named operation (rename, delete
// with reassign, classify, set anonymous) say so, so the write yields exactly
// that operation's single record instead of the create+delete pair (and the
// cascaded switchable_to edits) a plain snapshot diff would show. A zero
// Kind means "derive the records from the diff".
type ChangeHint struct {
	Kind            profile.ChangeKind
	Profile         string
	PreviousProfile string
	Diff            map[string]interface{}
}

// diffProfiles derives the profile changes between two desired configs
// A hint overrides the derivation.
func diffProfiles(before, after *config.Config, hint ChangeHint) []ProfileChange {
	if hint.Kind != "" {
		return []ProfileChange{{Kind: hint.Kind, Profile: hint.Profile, PreviousProfile: hint.PreviousProfile, Diff: hint.Diff}}
	}
	var out []ProfileChange
	byID := func(c *config.Config) map[string]config.ProfileConfig {
		m := map[string]config.ProfileConfig{}
		if c != nil {
			for _, p := range c.Profiles {
				if _, dup := m[p.ID]; !dup {
					m[p.ID] = p
				}
			}
		}
		return m
	}
	oldByID, newByID := byID(before), byID(after)

	// Capacity from one side only: summing both lengths trips CodeQL's
	// allocation-overflow check, and append grows the slice as needed.
	names := make([]string, 0, len(oldByID))
	seen := map[string]bool{}
	for n := range oldByID {
		names, seen[n] = append(names, n), true
	}
	for n := range newByID {
		if !seen[n] {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		o, hadOld := oldByID[n]
		nw, hasNew := newByID[n]
		switch {
		case hadOld && !hasNew:
			out = append(out, ProfileChange{Kind: profile.ChangeDelete, Profile: n, PreviousProfile: n})
		case !hadOld && hasNew:
			out = append(out, ProfileChange{Kind: profile.ChangeCreate, Profile: n})
		default:
			if profileJSON(o) != profileJSON(nw) {
				out = append(out, ProfileChange{Kind: profile.ChangeUpdate, Profile: n, PreviousProfile: n, Diff: diffProfileFields(o, nw)})
			}
		}
	}

	return out
}

func profileJSON(p config.ProfileConfig) string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// diffProfileFields names the fields that differ between two versions of one
// profile: {from,to} for scalars, {added,removed} for lists. It never carries
// anything but profile policy (no secret-bearing field exists on a profile).
func diffProfileFields(o, n config.ProfileConfig) map[string]interface{} {
	d := map[string]interface{}{}
	scalar := func(field string, from, to interface{}) {
		if !reflect.DeepEqual(from, to) {
			d[field] = map[string]interface{}{"from": from, "to": to}
		}
	}
	list := func(field string, from, to []string) {
		added, removed := setDiff(from, to)
		if len(added) > 0 || len(removed) > 0 {
			d[field] = map[string]interface{}{"added": added, "removed": removed}
		}
	}
	scalar("name", o.Name, n.Name)
	scalar("selected_tools", o.SelectedTools, n.SelectedTools)
	list("servers", o.Servers, n.Servers)
	list("switchable_to", derefList(o.SwitchableTo), derefList(n.SwitchableTo))
	if (o.SwitchableTo == nil) != (n.SwitchableTo == nil) {
		d["switchable_to_set"] = map[string]interface{}{"from": o.SwitchableTo != nil, "to": n.SwitchableTo != nil}
	}
	return d
}

func derefList(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// setDiff returns the members added to and removed from a list (sorted).
func setDiff(from, to []string) (added, removed []string) {
	in := func(list []string) map[string]bool {
		m := make(map[string]bool, len(list))
		for _, v := range list {
			m[v] = true
		}
		return m
	}
	f, t := in(from), in(to)
	for v := range t {
		if !f[v] {
			added = append(added, v)
		}
	}
	for v := range f {
		if !t[v] {
			removed = append(removed, v)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	if added == nil {
		added = []string{}
	}
	if removed == nil {
		removed = []string{}
	}
	return added, removed
}

// writeProfileChanges writes one `profile_change` record per change.
func (r *Runtime) writeProfileChanges(ctx context.Context, actor Actor, changes []ProfileChange) {
	sm := r.StorageManager()
	if sm == nil {
		return
	}
	for _, c := range changes {
		writeChangeRecord(ctx, sm.SaveActivity, r.logger, time.Now(), actor, changeRecord{
			change: c.Kind, profile: c.Profile, previousProfile: c.PreviousProfile, diff: c.Diff,
		})
	}
}
