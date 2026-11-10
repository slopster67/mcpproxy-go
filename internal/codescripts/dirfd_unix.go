//go:build unix

package codescripts

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// A scoped resolution is bound to ONE retained directory descriptor per
// request (storedSpellingsOf): the scripts directory is opened by path exactly
// once (openScopedDir); its generation is read from that descriptor
// (fstatDirGeneration); a candidate is probed relative to the same descriptor
// (fstatatEntry, AT_SYMLINK_NOFOLLOW); the winning candidate is opened
// relative to the same descriptor (openatEntry); and the post-open recheck
// reads the generation from it once more. No path is resolved twice, so a
// symlink, ancestor directory or bind mount retargeted between two steps of
// one request cannot make those steps observe different directories. The
// request's own cost stays O(1): one open, two fstats, one fstatat, one
// openat.
//
// The rebuild goroutine opens its own descriptor the same way and lists
// through it (listScopedDir), so the listing and the generation the index
// records for it come from the identical open.

// openScopedDir opens scriptsDir once. O_DIRECTORY refuses a non-directory at
// the path outright (a symlink resolving to a plain file would otherwise
// silently "open" as if it were an empty directory); O_CLOEXEC keeps the
// descriptor from leaking into a child process spawned while a request holds
// it.
func openScopedDir(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return fd, nil
}

// fstatDirGeneration reads a directory's generation stamp from an already-open
// descriptor, so it can be called again after the candidate open without
// re-resolving scriptsDir. It reads golang.org/x/sys/unix.Stat_t, whose field
// names (Dev, Ino, Mtim, Ctim, Size) are uniform across every Unix platform,
// unlike the standard syscall.Stat_t.
func fstatDirGeneration(fd int) (dirGeneration, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return dirGeneration{}, err
	}
	return dirGeneration{
		modTime:    time.Unix(st.Mtim.Unix()),
		changeTime: time.Unix(st.Ctim.Unix()),
		size:       st.Size,
		ino:        st.Ino,
		dev:        uint64(st.Dev),
	}, nil
}

// fstatatEntry probes name relative to dirfd without following a symlink: the
// candidate's own existence check, bound to the same descriptor the generation
// was read from.
func fstatatEntry(dirfd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	return nil
}

// openatEntry opens name relative to dirfd, refusing a symlink atomically
// (O_NOFOLLOW, mapped to errNonRegular as openScriptFile does) and never
// parking on a FIFO (O_NONBLOCK). This is the entry fstatatEntry probed,
// opened relative to the same descriptor — never a second, independent lookup
// of the name.
func openatEntry(dirfd int, name string) (*os.File, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
			return nil, errNonRegular
		}
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// listScopedDir lists dirfd's entries through a dup of the descriptor:
// closing the dup releases only the copy, leaving the caller's own dirfd — and
// its read position — untouched.
func listScopedDir(dirfd int, path string) ([]string, error) {
	dupFd, err := unix.Dup(dirfd)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dupFd), path)
	defer func() { _ = f.Close() }()
	return f.Readdirnames(-1)
}
