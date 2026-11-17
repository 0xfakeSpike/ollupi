//go:build unix

package sandbox

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// A command left running can put a link in place of a folder on a checked canonical path, so the agent's own files
// are opened folder by folder without following links: what is opened is what was checked.

var errLink = errors.New("the path leads through a link")

const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

func openDir(dir string, create bool) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: "/", Err: err}
	}
	at := "/"
	for name := range strings.SplitSeq(strings.Trim(filepath.Clean(dir), "/"), "/") {
		if name == "" {
			continue
		}
		at = filepath.Join(at, name)
		next, err := unix.Openat(fd, name, dirFlags, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(fd, name, 0o755); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, name, dirFlags, 0)
			}
		}
		// A system may report a link refused as a folder with ENOTDIR rather than ELOOP.
		var st unix.Stat_t
		if errors.Is(err, unix.ENOTDIR) && unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
			err = unix.ELOOP
		}
		unix.Close(fd)
		if err != nil {
			return -1, pathError(at, err)
		}
		fd = next
	}
	return fd, nil
}

func pathError(path string, err error) error {
	if errors.Is(err, unix.ELOOP) {
		return fmt.Errorf("sandbox: %s: %w", path, errLink)
	}
	return &fs.PathError{Op: "open", Path: path, Err: err}
}

func openRegular(path string) (*os.File, error) {
	dir, err := openDir(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dir)
	// O_NONBLOCK keeps a FIFO from holding the open until a writer comes; a regular file ignores it.
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, pathError(path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, fmt.Errorf("sandbox: %s is not a regular file", path)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}

// open applies the read rules to the path it opened, after following its links.
func (s *Sandbox) open(path string) (*os.File, error) {
	f, err := openRegular(path)
	if errors.Is(err, errLink) {
		if path, err = CanonicalPath(path, ""); err != nil {
			return nil, err
		}
		f, err = openRegular(path)
	}
	if err != nil {
		return nil, err
	}
	if err := s.CheckRead(path); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// A link in place of a folder above path is refused.
func writeFile(path string, data []byte, perm fs.FileMode) error {
	dir, err := openDir(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	base := filepath.Base(path)
	tmp := "." + base + "." + rand.Text()
	fd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return pathError(filepath.Join(filepath.Dir(path), tmp), err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(path), tmp))
	_, err = f.Write(data)
	if err = errors.Join(err, f.Chmod(perm), f.Close()); err == nil {
		// The rename replaces what is at path, a link included, rather than writing where a link leads.
		err = unix.Renameat(dir, tmp, dir, base)
	}
	if err != nil {
		return errors.Join(err, ignoreMissing(unix.Unlinkat(dir, tmp, 0)))
	}
	return nil
}

func ignoreMissing(err error) error {
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
