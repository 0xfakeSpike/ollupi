package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MakeDir makes a folder at path, as a person does choosing where a
// workspace goes; its parent must be there. No folder a command is kept
// from (DenyRead) is made.
func (s *Sandbox) MakeDir(_ context.Context, path string) error {
	path, err := canonicalPath(path, "")
	if err != nil {
		return err
	}
	if err := s.CheckRead(path); err != nil {
		return err
	}
	return os.Mkdir(path, 0o755)
}

// Trash moves path to this machine's trash, where a person finds it and
// puts it back: the Trash of Finder on macOS, the freedesktop.org trash of
// the home elsewhere. Nothing a command is kept from (DenyRead) is moved.
func (s *Sandbox) Trash(_ context.Context, path string) error {
	path, err := canonicalPath(path, "")
	if err != nil {
		return err
	}
	if err := s.CheckRead(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	return trash(path, s.home)
}

// freedesktopTrash moves path into the trash of the home, as the
// freedesktop.org trash specification has it: the file under files/ and a
// .trashinfo of the same name under info/ saying where it was and when it
// went, which a file manager reads to put it back. A file on another disk
// than the home's is refused.
func freedesktopTrash(path, home string) error {
	dir := filepath.Join(home, ".local", "share", "Trash")
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		dir = filepath.Join(xdg, "Trash")
	}
	files, info := filepath.Join(dir, "files"), filepath.Join(dir, "info")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(info, 0o700); err != nil {
		return err
	}
	record := trashInfo(path, time.Now())
	base := filepath.Base(path)
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = base + "." + strconv.Itoa(n)
		}
		f, err := os.OpenFile(filepath.Join(info, name+".trashinfo"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = f.WriteString(record)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(path, filepath.Join(files, name))
		}
		if err != nil {
			os.Remove(filepath.Join(info, name+".trashinfo"))
			return fmt.Errorf("sandbox: move %s to the trash: %w", path, err)
		}
		return nil
	}
}

func trashInfo(path string, at time.Time) string {
	return "[Trash Info]\nPath=" + trashInfoPath(path) + "\nDeletionDate=" + at.Format("2006-01-02T15:04:05") + "\n"
}

// trashInfoPath is path as a .trashinfo records it, each name escaped.
func trashInfoPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
