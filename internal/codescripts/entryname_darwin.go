//go:build darwin

package codescripts

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// On darwin, F_GETPATH reports the exact on-disk spelling of an already-open
// descriptor. openat already proves the opened entry is a child of the
// retained directory descriptor, so this adds a spelling proof of the opened
// descriptor's basename against the requested name, run after the generation
// recheck passes.
func init() {
	extraVerifyOpened = func(f *os.File, want string) error {
		stored, err := entryNameFromFd(f.Fd())
		if err != nil || stored != want {
			return errSpellingUnproven
		}
		return nil
	}
}

// entryNameFromFd reports the base name of the directory entry fd was opened
// from (F_GETPATH).
func entryNameFromFd(fd uintptr) (string, error) {
	var buf [1024]byte // MAXPATHLEN
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	n := bytes.IndexByte(buf[:], 0)
	if n < 0 {
		n = len(buf)
	}
	return filepath.Base(string(buf[:n])), nil
}
