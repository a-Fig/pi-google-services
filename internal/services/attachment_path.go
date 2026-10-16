package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Where a downloaded attachment is allowed to land.
//
// This tool is a file-write primitive whose arguments are chosen by a model that
// reads untrusted email. Both halves of that matter: the filename comes straight
// from the sender, and the destination comes from a model the sender can talk to.
// So the sender cannot escape the chosen directory (safeFilename), the model
// cannot clobber an existing file (openExclusive), and neither can reach into a
// dot-directory such as ~/.ssh or ~/.config outside the temp directory.
//
// What is deliberately still allowed: writing a new file to an ordinary
// directory the model names. Confining that further is a policy decision for the
// agent's own permission rules, not something this connector can decide well.

// expandHome resolves a leading ~ the way a shell would. Models reliably pass
// "~/Downloads/", and without this it becomes a literal directory named "~"
// under whatever the daemon's working directory happens to be.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// hasHiddenComponent reports whether any element of a path is a dot-directory or
// dotfile. Those are where configuration and credentials live.
func hasHiddenComponent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if len(part) > 1 && strings.HasPrefix(part, ".") && part != ".." {
			return true
		}
	}
	return false
}

// resolveSavePath decides where an attachment lands. An empty savePath means the
// system temp directory; a savePath that is, or looks like, a directory gets the
// attachment's own filename appended; anything else is used as the file itself.
func resolveSavePath(savePath, filename string) (string, error) {
	if savePath == "" {
		return filepath.Join(os.TempDir(), safeFilename(filename)), nil
	}

	savePath = expandHome(savePath)
	looksLikeDir := len(savePath) > 0 && os.IsPathSeparator(savePath[len(savePath)-1])
	if !looksLikeDir {
		if info, err := os.Stat(savePath); err == nil && info.IsDir() {
			looksLikeDir = true
		}
	}

	dest := savePath
	if looksLikeDir {
		dest = filepath.Join(savePath, safeFilename(filename))
	}

	dest, err := filepath.Abs(filepath.Clean(dest))
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", savePath, err)
	}

	// The temp directory is exempt: it is the default, it is where scratch work
	// belongs, and nothing sensitive lives in a dot-path under it.
	if tmp, err := filepath.Abs(os.TempDir()); err != nil || !strings.HasPrefix(dest, tmp+string(os.PathSeparator)) {
		if hasHiddenComponent(dest) {
			return "", fmt.Errorf("refusing to write into a hidden path (%s); choose an ordinary directory", dest)
		}
	}
	return dest, nil
}

// safeFilename reduces a sender-controlled filename to a single, ordinary path
// element, so an attachment can never be written outside the chosen directory
// nor land as a dotfile.
func safeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = filepath.Base(strings.TrimSpace(name))

	// Control characters would otherwise survive into the filename and into the
	// attachment listing, where they can fake extra entries.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)

	name = strings.TrimLeft(name, ".")
	if name == "" || name == string(os.PathSeparator) {
		return "attachment"
	}
	return name
}

// openExclusive creates dest without ever truncating an existing file, adding
// "-1", "-2" and so on when the name is taken. Two senders both attaching
// invoice.pdf must not silently become one file, and an attachment must never
// overwrite something that was already there.
func openExclusive(dest string) (*os.File, string, error) {
	ext := filepath.Ext(dest)
	stem := strings.TrimSuffix(dest, ext)
	for attempt := 0; attempt < 100; attempt++ {
		candidate := dest
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, attempt, ext)
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("too many files already named like %s", filepath.Base(dest))
}
