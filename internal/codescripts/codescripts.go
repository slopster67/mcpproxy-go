package codescripts

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// MaxNameLen is the longest permitted script name.
	MaxNameLen = 64

	// MaxSizeBytes bounds the daemon-side read of a script file. Inline code
	// has no such bound; stored scripts do, purely to bound the read.
	MaxSizeBytes = 256 * 1024

	// DirName is the scripts directory's name, inside the data directory.
	DirName = "scripts"

	// LanguageJavaScript / LanguageTypeScript are the languages a script
	// extension can derive, matching the code_execution `language` parameter.
	LanguageJavaScript = "javascript"
	LanguageTypeScript = "typescript"

	extJS = ".js"
	extTS = ".ts"
)

// Reasons a script file is present but unusable.
const (
	ReasonEmpty      = "empty"
	ReasonOversized  = "oversized"
	ReasonUnreadable = "unreadable"
	ReasonNonRegular = "non-regular"
)

// errNonRegular is the platform-independent signal that the path is not a
// regular file — a symlink, directory or device. Platform openers return it
// (Unix maps the kernel's no-follow rejection onto it).
var errNonRegular = errors.New("not a regular file")

type scopedOpener func(path string) (*os.File, error)

// scopedVerifier re-proves, on the descriptor a scopedOpener actually opened,
// that nothing was swapped between the probe and the open.
type scopedVerifier func(f *os.File, want string) error

type scopedCloser func()

var errIndexGenerationChanged = errors.New("codescripts: scripts directory changed between the index lookup and the open")

var errSpellingUnproven = errors.New("codescripts: the opened file's stored spelling could not be proven to match the requested name")

// InvalidNameError rejects a script name before any filesystem access.
type InvalidNameError struct {
	Name   string
	Reason string
}

func (e *InvalidNameError) Error() string {
	return fmt.Sprintf("invalid script name %q: %s (names are 1-%d characters of A-Z, a-z, 0-9, '-' or '_' — a name, never a path)",
		truncateForMessage(e.Name), e.Reason, MaxNameLen)
}

// NotFoundError reports a name with no script file behind it. Its text names
// only the caller's own requested name, never the directory or what else is
// stored, so a failed call cannot serve as an oracle for the directory's
// contents.
type NotFoundError struct {
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("stored script %q not found (an MCP client must already know the script name; there is no listing)", e.Name)
}

// AmbiguousError reports a name backed by both a .js and a .ts file. The text
// names the caller's own script only, never a host path.
type AmbiguousError struct {
	Name string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("stored script %q is ambiguous: both a %s and a %s file exist — ask an administrator to remove one",
		e.Name, extJS, extTS)
}

// InvalidError reports a script file that exists but cannot be executed. The
// text names the caller's own script and the reason only: no host path and no
// raw OS error.
type InvalidError struct {
	Name   string
	Reason string
}

func (e *InvalidError) Error() string {
	msg := fmt.Sprintf("stored script %q is %s", e.Name, e.Reason)
	switch e.Reason {
	case ReasonOversized:
		msg += fmt.Sprintf(": scripts are limited to %d bytes", MaxSizeBytes)
	case ReasonNonRegular:
		msg += ": only regular files are executed (symlinks, directories and devices are rejected)"
	}
	return msg
}

// LanguageMismatchError reports an explicit `language` that contradicts the
// script's extension (the extension is authoritative). The text withholds the
// script's actual extension.
type LanguageMismatchError struct {
	Name      string
	Requested string
}

func (e *LanguageMismatchError) Error() string {
	return fmt.Sprintf("stored script %q does not accept requested language %q — omit 'language' and let it be derived automatically",
		e.Name, e.Requested)
}

// ValidateName enforces the script-name token: 1-MaxNameLen characters of
// [A-Za-z0-9_-]. This is the confinement boundary and performs NO filesystem
// access — a valid name has no separators and no dots, so it cannot traverse.
func ValidateName(name string) error {
	if name == "" {
		return &InvalidNameError{Name: name, Reason: "name is empty"}
	}
	if len(name) > MaxNameLen {
		return &InvalidNameError{Name: name, Reason: fmt.Sprintf("name is %d characters long", len(name))}
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return &InvalidNameError{Name: name, Reason: fmt.Sprintf("character %q is not allowed", string(name[i]))}
		}
	}
	return nil
}

// DeriveLanguage maps a script extension to a code_execution language and
// rejects an explicit language that contradicts it. An empty explicit language
// always agrees.
func DeriveLanguage(name, ext, explicitLanguage string) (string, error) {
	var derived string
	switch ext {
	case extJS:
		derived = LanguageJavaScript
	case extTS:
		derived = LanguageTypeScript
	default:
		return "", &InvalidError{Name: name, Reason: ReasonNonRegular}
	}
	if explicitLanguage != "" && explicitLanguage != derived {
		return "", &LanguageMismatchError{Name: name, Requested: explicitLanguage}
	}
	return derived, nil
}

// Resolve reads the stored script name from scriptsDir, deriving its language
// from the extension. The candidates are decided by a constant-cost path probe
// of the two possible file names rather than a directory listing, so a refusal
// costs the same whatever is stored and whatever name was asked for. Every
// refusal carries the caller's own name and the reason, never a host path or a
// raw OS error.
func Resolve(scriptsDir, name, explicitLanguage string) (source []byte, language string, err error) {
	if err := ValidateName(name); err != nil {
		return nil, "", err
	}

	notFound := func() error { return &NotFoundError{Name: name} }
	invalid := func(reason string) error { return &InvalidError{Name: name, Reason: reason} }

	// An empty scripts dir would make filepath.Join produce a bare relative
	// path resolved against the process CWD — never that. No authority means
	// no scripts.
	if scriptsDir == "" {
		return nil, "", notFound()
	}

	found, open, verifyUnchanged, closeSession, err := probeCandidates(scriptsDir, name)
	if closeSession != nil {
		defer closeSession()
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", notFound()
		}
		return nil, "", invalid(ReasonUnreadable)
	}

	switch len(found) {
	case 0:
		return nil, "", notFound()
	case 1:
	default:
		return nil, "", &AmbiguousError{Name: name}
	}

	path := found[0]
	lang, err := DeriveLanguage(name, filepath.Ext(path), explicitLanguage)
	if err != nil {
		return nil, "", err
	}

	f, err := open(path)
	if err != nil {
		switch {
		case errors.Is(err, errNonRegular):
			return nil, "", invalid(ReasonNonRegular)
		case errors.Is(err, fs.ErrNotExist):
			// Removed between the probe and the open.
			return nil, "", notFound()
		default:
			return nil, "", invalid(ReasonUnreadable)
		}
	}
	defer f.Close()

	if verifyUnchanged != nil {
		if verifyErr := verifyUnchanged(f, filepath.Base(path)); verifyErr != nil {
			if errors.Is(verifyErr, errIndexGenerationChanged) || errors.Is(verifyErr, errSpellingUnproven) {
				return nil, "", notFound()
			}
			return nil, "", invalid(ReasonUnreadable)
		}
	}

	// Re-verify on the open descriptor: this is the file that will actually be
	// read, whatever the path pointed at a moment ago.
	info, err := f.Stat()
	if err != nil {
		return nil, "", invalid(ReasonUnreadable)
	}
	if !info.Mode().IsRegular() {
		return nil, "", invalid(ReasonNonRegular)
	}

	// Bound the read itself rather than trusting the stat size: a file that
	// grows between stat and read would otherwise execute truncated content.
	// One extra byte is requested purely to detect the overflow.
	data, err := io.ReadAll(io.LimitReader(f, MaxSizeBytes+1))
	if err != nil {
		return nil, "", invalid(ReasonUnreadable)
	}
	if len(data) > MaxSizeBytes {
		return nil, "", invalid(ReasonOversized)
	}
	if len(data) == 0 {
		return nil, "", invalid(ReasonEmpty)
	}

	return data, lang, nil
}

func probeCandidates(scriptsDir, name string) ([]string, scopedOpener, scopedVerifier, scopedCloser, error) {
	storedExactly, open, verifyUnchanged, closeSession, err := storedSpellingsOf(scriptsDir)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	found := make([]string, 0, 2)
	for _, ext := range []string{extJS, extTS} {
		want := name + ext
		stored, err := storedExactly(want)
		if err != nil {
			if closeSession != nil {
				closeSession()
			}
			return nil, nil, nil, nil, err
		}
		if stored {
			found = append(found, filepath.Join(scriptsDir, want))
		}
	}
	return found, open, verifyUnchanged, closeSession, nil
}

// truncateForMessage bounds caller-supplied text echoed back in an error.
func truncateForMessage(s string) string {
	const limit = MaxNameLen + 16
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
