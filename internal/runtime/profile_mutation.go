package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

var ErrConfigUnavailable = errors.New("configuration unavailable")

type TokenRewrite struct{ From, To string }

func (t TokenRewrite) active() bool { return t.From != "" && t.To != "" }

// mutateProfiles commits profile edits and client reassignment together.
func (r *Runtime) mutateProfiles(ctx context.Context, actor Actor, mutate func(*config.Config) (ChangeHint, error), tokens TokenRewrite) error {
	unlock := r.LockBindingWrites()
	defer unlock()
	var before, desired *config.Config
	var hint ChangeHint
	_, err := r.Commit("profile_edit", func(tx *CommitTx) error {
		before = cloneCommitConfig(tx.Base())
		desired = cloneCommitConfig(tx.Base())
		var err error
		hint, err = mutate(desired)
		if err != nil {
			return err
		}
		if errs := desired.ValidateDetailed(); len(errs) > 0 {
			return fmt.Errorf("invalid profile: %s", errs[0].Error())
		}
		tx.next.Profiles = desired.Profiles
		if tokens.active() {
			tx.clientProfileMove = &storage.ClientProfileMove{From: tokens.From, To: tokens.To}
		}
		tx.afterCommit(func(*CommitResult) { r.reconcileProfileIndexes(); r.syncOnlinePeers() })
		return nil
	})
	if err != nil {
		return err
	}
	r.writeProfileChanges(ctx, actor, diffProfiles(before, desired, hint))
	return nil
}
