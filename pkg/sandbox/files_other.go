//go:build !unix

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func (s *Sandbox) open(path string) (*os.File, error) {
	path, err := canonicalPath(path, "")
	if err != nil {
		return nil, err
	}
	if err := s.CheckRead(path); err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("sandbox: %s is not a regular file", path)
	}
	return os.Open(path)
}

func writeFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err := errors.Join(err, tmp.Chmod(perm), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
