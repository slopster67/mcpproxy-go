//go:build unix

package codescripts

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// A case-folding filesystem (the default APFS volume, vfat, an ext4 casefold
// directory, a bind mount from a case-insensitive host) finds `backdoor.JS`
// for `backdoor.js`, and Linux and the BSDs offer no single-entry call that
// reports how an entry is spelled on disk. The only exact answer is the
// directory listing, and a listing paid on a scoped caller's request would
// make the refusal's cost grow with the directory — and, paid only when a
// probe hits, would make "a case-variant exists" distinguishable from "no
// entry" by latency alone.
//
// So the scoped resolver answers from a stored-name INDEX: the exact
// spellings a scripts directory holds, maintained off the request path. The
// index is built when the server learns its scripts directory (Warm) and
// rebuilt by a single-flight goroutine whenever a request finds it behind the
// directory's GENERATION. No request ever lists: an exact hit is re-probed by
// the candidate's own no-follow stat and opened no-follow, so a removed or
// replaced file fails closed; a script added since the listing is refused
// until the rebuild lands, milliseconds later. Every request — hit, miss or case-variant, cold or
// warm, in a directory of ten thousand entries or none — costs the same
// bounded number of directory primitives and an O(1) set lookup. Darwin uses
// the same index; it additionally checks F_GETPATH on the opened descriptor
// (entryname_darwin.go).
//
// The index answers ONLY for the generation it was built against: a stale
// index is refused exactly as a never-built one is, fail closed, until its
// own rebuild lands (a call landing within milliseconds of a directory change
// is refused once — retry). The generation is checked once more after an
// exact-set hit's no-follow open: gen-before == index.gen == gen-after proves
// the file the open just read is the one the index vouched for.
//
// A matching generation is not enough on its own: a coarse filesystem
// timestamp (vfat: two seconds) can leave a directory's stamp unchanged
// across a rename that lands in the same tick as the stamp the index was
// listed against. An index may therefore authorize a hit only once it is
// SETTLED: its stamp predates the listing by at least generationSettleTime,
// so no write still landing on that stamp could have escaped it.
//
// Each index owns a context that eviction cancels; its async rebuild
// goroutine checks it between listing attempts and once more before
// installing a result, so a rebuild for an evicted or pruned index stops
// promptly and writes nothing. Warm's own synchronous rebuild always runs to
// completion.

const generationSettleTime = 2 * time.Second

// maxRebuildAttempts bounds how many times one rebuild re-lists when the
// directory's generation keeps moving under it. After the bound, whatever the
// last attempt installed stays as the index — the next request finds it stale
// against the directory's current generation and refuses fail-closed.
const maxRebuildAttempts = 3

// rebuildBackoff is the minimum gap between the end of one async rebuild and
// the start of the next for the same directory, so a directory changing on
// every request cannot keep a rebuild running back to back. During the
// backoff a request is answered fail-closed from whatever the index holds;
// only the new rebuild goroutine is withheld.
const rebuildBackoff = time.Second

// maxStoredNameIndexes bounds the index map. Warm keeps only the active
// directory's index (pruneOtherIndexesLocked), so this cap matters only for
// directories a scoped request touches without ever being Warmed for.
const maxStoredNameIndexes = 4

const maxConcurrentRebuilds = 2

// rebuildSlots is the process-wide semaphore bounding concurrent directory
// listings. An async rebuild that cannot acquire a slot is skipped, not
// queued: the index stays as stale as it was and the next request tries
// again. Warm blocks for a slot instead, since its caller waits on the
// result.
var rebuildSlots = make(chan struct{}, maxConcurrentRebuilds)

// storedNames is the exact-spelling index of one scripts directory. names is
// replaced, never mutated, so a set handed out under the lock stays valid
// after it is released.
type storedNames struct {
	mu      sync.Mutex
	names   map[string]struct{} // nil until a build has landed, or when it failed
	err     error               // the last build's failure; nil when names is valid
	gen     dirGeneration       // the directory's stamp when names was listed
	settled bool                // gen predates the listing by more than any timestamp tick

	// building is the single-flight flag: at most one rebuild goroutine per
	// directory. landed is closed when that rebuild has finished, so Warm can
	// wait for it.
	building bool
	landed   chan struct{}

	// refreshAfter bounds how often an UNSETTLED index schedules a refresh:
	// at most once per generationSettleTime, whatever the request rate.
	refreshAfter time.Time

	// nextAttempt is the earliest time a new async rebuild may start (see
	// rebuildBackoff); zero means none has finished yet.
	nextAttempt time.Time

	// ctx is cancelled when the index is discarded (LRU eviction or Warm
	// pruning), stopping its async rebuild.
	ctx    context.Context
	cancel context.CancelFunc
}

// storedIndexes holds one *storedNames per cleaned scripts directory, capped
// at maxStoredNameIndexes with least-recently-used eviction.
var (
	storedIndexesMu  sync.Mutex
	storedIndexes    = map[string]*storedNames{}
	storedIndexesLRU []string // least-recently-used first; a touched key moves to the end
)

// storedNamesIndex returns the index of one cleaned scripts directory,
// creating an empty (never built) one on first use, and records the access
// for LRU eviction.
func storedNamesIndex(key string) *storedNames {
	storedIndexesMu.Lock()
	defer storedIndexesMu.Unlock()
	idx, ok := storedIndexes[key]
	if !ok {
		ctx, cancel := context.WithCancel(context.Background())
		idx = &storedNames{ctx: ctx, cancel: cancel}
		storedIndexes[key] = idx
	}
	touchIndexLocked(key)
	evictExcessLocked()
	return idx
}

// touchIndexLocked moves key to the most-recently-used end of the LRU order.
// storedIndexesMu must be held.
func touchIndexLocked(key string) {
	for i, k := range storedIndexesLRU {
		if k == key {
			storedIndexesLRU = append(storedIndexesLRU[:i], storedIndexesLRU[i+1:]...)
			break
		}
	}
	storedIndexesLRU = append(storedIndexesLRU, key)
}

// evictExcessLocked drops the least-recently-used indexes once the map holds
// more than maxStoredNameIndexes, cancelling each one's rebuild context
// first. storedIndexesMu must be held.
func evictExcessLocked() {
	for len(storedIndexesLRU) > maxStoredNameIndexes {
		oldest := storedIndexesLRU[0]
		storedIndexesLRU = storedIndexesLRU[1:]
		if idx, ok := storedIndexes[oldest]; ok {
			idx.cancel()
		}
		delete(storedIndexes, oldest)
	}
}

// pruneOtherIndexesLocked drops every index but keep, cancelling each dropped
// index's rebuild context first. storedIndexesMu must be held.
func pruneOtherIndexesLocked(keep string) {
	for k, idx := range storedIndexes {
		if k != keep {
			idx.cancel()
			delete(storedIndexes, k)
		}
	}
	kept := storedIndexesLRU[:0]
	for _, k := range storedIndexesLRU {
		if k == keep {
			kept = append(kept, k)
		}
	}
	storedIndexesLRU = kept
}

// dirGeneration is the stat tuple that moves whenever a directory's entry
// set can have changed: adding, removing or renaming an entry updates its
// mtime and ctime (ctime cannot be set from user space, so a restored mtime —
// tar, rsync -a — does not hide a change), a replaced directory has another
// inode, and size is the cheap extra. dev is the device the inode lives on:
// an inode number is unique only within a device, so without it a bind-mount
// swap to another filesystem whose directory collides on inode, size, mtime
// and ctime would read as the same generation.
type dirGeneration struct {
	modTime, changeTime time.Time
	size                int64
	ino                 uint64
	dev                 uint64
}

func (g dirGeneration) equal(o dirGeneration) bool {
	return g.modTime.Equal(o.modTime) && g.changeTime.Equal(o.changeTime) &&
		g.size == o.size && g.ino == o.ino && g.dev == o.dev
}

// latest is the later of the two timestamps.
func (g dirGeneration) latest() time.Time {
	if g.changeTime.After(g.modTime) {
		return g.changeTime
	}
	return g.modTime
}

// extraVerifyOpened is an additional, platform-specific spelling proof run on
// the opened descriptor after the generation recheck passes. The default is a
// no-op; darwin installs an F_GETPATH check (entryname_darwin.go).
var extraVerifyOpened = func(*os.File, string) error { return nil }

// Warm builds the index for scriptsDir synchronously and drops every other
// directory's index.
func Warm(scriptsDir string) error {
	key := filepath.Clean(scriptsDir)
	idx := storedNamesIndex(key)
	storedIndexesMu.Lock()
	pruneOtherIndexesLocked(key)
	storedIndexesMu.Unlock()
	for {
		idx.mu.Lock()
		if !idx.building {
			idx.beginRebuildLocked()
			idx.mu.Unlock()
			break
		}
		landed := idx.landed
		idx.mu.Unlock()
		<-landed
	}
	// Warm's listing counts against the same process-wide rebuildSlots bound
	// as async rebuilds, but blocks for a slot rather than skipping, since
	// the caller trusts the error it returns. The slot is acquired outside
	// idx.mu, so a blocked acquire never holds up another goroutine that
	// needs idx.mu.
	rebuildSlots <- struct{}{}
	defer func() { <-rebuildSlots }()
	// Warm is the server's explicit request for a current index, not a
	// request-triggered rebuild, so it neither starts the backoff window nor
	// honours cancellation.
	idx.rebuild(key, false, false)
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return idx.err
}

// storedSpellingsOf answers, for one scoped request, whether scriptsDir holds
// an entry spelled exactly `want`, and hands back how to open and re-verify
// the winning candidate — all bound to the single directory descriptor this
// call opens (see dirfd_unix.go). The index is validated once per request
// against that descriptor's own generation, and only an index hit is probed,
// so an absent name and a differently cased one cost the same. A directory
// that cannot be opened is an error the scoped resolver reports as
// unreadable.
//
// open opens the winning candidate relative to the same descriptor.
// verifyUnchanged reads the descriptor's generation once more after the open;
// gen-before == index.gen == gen-after proves the file just opened is the one
// the index vouched for. closeSession releases the descriptor and must be
// called exactly once.
func storedSpellingsOf(scriptsDir string) (storedExactly func(want string) (bool, error), open func(path string) (*os.File, error), verifyUnchanged func(f *os.File, want string) error, closeSession func(), err error) {
	key := filepath.Clean(scriptsDir)

	dirfd, err := openScopedDir(key)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	closeSession = func() { _ = unix.Close(dirfd) }

	gen, err := fstatDirGeneration(dirfd)
	if err != nil {
		closeSession()
		return nil, nil, nil, nil, err
	}

	names, lookupErr := storedNamesFor(key, gen)
	if lookupErr != nil {
		closeSession()
		return nil, nil, nil, nil, lookupErr
	}

	storedExactly = func(want string) (bool, error) {
		if _, ok := names[want]; !ok {
			return false, nil
		}
		if err := fstatatEntry(dirfd, want); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	open = func(path string) (*os.File, error) {
		return openatEntry(dirfd, filepath.Base(path))
	}
	verifyUnchanged = func(f *os.File, want string) error {
		cur, err := fstatDirGeneration(dirfd)
		if err != nil {
			return err
		}
		if !cur.equal(gen) {
			return errIndexGenerationChanged
		}
		return extraVerifyOpened(f, want)
	}
	return storedExactly, open, verifyUnchanged, closeSession, nil
}

// storedNamesFor returns the exact-name set of the directory as the index
// holds it for this request's freshly read generation, never listing on the
// caller's behalf. When gen does not equal the index's own generation (never
// built, behind, or a rebuild in flight) or the index is not yet settled, the
// request is answered fail-closed: nil names, no error. Because gen carries
// the directory's device and inode, an index built from directory A never
// authorizes a request whose descriptor opened directory B.
func storedNamesFor(key string, gen dirGeneration) (names map[string]struct{}, err error) {
	idx := storedNamesIndex(key)
	now := time.Now()

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// current is whether the index is built and answers for exactly this
	// generation: an out-of-date generation always reschedules, an
	// in-date-but-unsettled one reschedules at most once per window.
	current := (idx.names != nil || idx.err != nil) && idx.gen.equal(gen)

	switch {
	case !current:
		idx.scheduleRebuildLocked(key, now)
	case !idx.settled && !now.Before(idx.refreshAfter):
		idx.scheduleRebuildLocked(key, now)
	}

	if !current || !idx.settled {
		return nil, nil
	}
	return idx.names, idx.err
}

// scheduleRebuildLocked starts the directory's async rebuild goroutine unless
// one is already in flight, the backoff since the last one has not elapsed,
// or every rebuild slot is busy, and opens the next refresh window either
// way.
func (idx *storedNames) scheduleRebuildLocked(key string, now time.Time) {
	idx.refreshAfter = now.Add(generationSettleTime)
	if idx.building {
		return
	}
	if !idx.nextAttempt.IsZero() && now.Before(idx.nextAttempt) {
		return
	}
	select {
	case rebuildSlots <- struct{}{}:
	default:
		return
	}
	idx.beginRebuildLocked()
	go func() {
		defer func() { <-rebuildSlots }()
		idx.rebuild(key, true, true)
	}()
}

// beginRebuildLocked claims the single-flight slot.
func (idx *storedNames) beginRebuildLocked() {
	idx.building = true
	idx.landed = make(chan struct{})
}

// rebuild lists the directory and installs the result, holding no lock
// across the listing. A change during the listing is caught by
// listScopedDirOnce's before/after generation read and retried, up to
// maxRebuildAttempts; giving up leaves whatever the last attempt installed,
// which storedNamesFor then finds stale and refuses fail-closed.
//
// When cancellable and idx.ctx is done, the loop stops at the next checkpoint
// and installs nothing. Either way the single-flight slot is released and
// landed closed; when backoffAfter is set, the backoff window opens.
func (idx *storedNames) rebuild(key string, backoffAfter, cancellable bool) {
	for attempt := 1; ; attempt++ {
		if cancellable && idx.ctx.Err() != nil {
			idx.finishRebuild(backoffAfter)
			return
		}
		before, after, names, listErr := listScopedDirOnce(key)
		now := time.Now()
		if cancellable && idx.ctx.Err() != nil {
			idx.finishRebuild(backoffAfter)
			return
		}
		if listErr == nil && attempt < maxRebuildAttempts && !before.equal(after) {
			continue
		}
		gen := after
		if listErr != nil {
			gen = before
		}
		idx.mu.Lock()
		idx.names, idx.err, idx.gen = names, listErr, gen
		idx.settled = listErr == nil && now.Sub(gen.latest()) >= generationSettleTime
		idx.building = false
		if backoffAfter {
			idx.nextAttempt = time.Now().Add(rebuildBackoff)
		}
		close(idx.landed)
		idx.mu.Unlock()
		return
	}
}

// finishRebuild releases the single-flight slot and closes landed without
// installing anything.
func (idx *storedNames) finishRebuild(backoffAfter bool) {
	idx.mu.Lock()
	idx.building = false
	if backoffAfter {
		idx.nextAttempt = time.Now().Add(rebuildBackoff)
	}
	close(idx.landed)
	idx.mu.Unlock()
}

// listScopedDirOnce opens key once, reads its generation, lists its entries
// through the same descriptor, and reads the generation once more, so a
// change during the listing shows up as the two reads disagreeing.
func listScopedDirOnce(key string) (before, after dirGeneration, names map[string]struct{}, err error) {
	dirfd, err := openScopedDir(key)
	if err != nil {
		return dirGeneration{}, dirGeneration{}, nil, err
	}
	defer func() { _ = unix.Close(dirfd) }()

	before, err = fstatDirGeneration(dirfd)
	if err != nil {
		return dirGeneration{}, dirGeneration{}, nil, err
	}
	entryNames, err := listScopedDir(dirfd, key)
	if err != nil {
		return before, dirGeneration{}, nil, err
	}
	after, err = fstatDirGeneration(dirfd)
	if err != nil {
		return before, dirGeneration{}, nil, err
	}
	names = make(map[string]struct{}, len(entryNames))
	for _, n := range entryNames {
		names[n] = struct{}{}
	}
	return before, after, names, nil
}
