package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Dir string

func keyed(key string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return &fs.PathError{Op: pe.Op, Path: key, Err: pe.Err}
	}
	return err
}

func (d Dir) path(key string) (string, error) {
	path := filepath.FromSlash(key)
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf("blobs: key %q leaves the store", key)
	}
	return filepath.Join(string(d), path), nil
}

func (d Dir) Put(_ context.Context, key string, data []byte) error {
	path, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return keyed(key, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return keyed(key, err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return errors.Join(keyed(key, err), keyed(key, os.Remove(path)))
	}
	return nil
}

func (d Dir) Get(_ context.Context, key string) ([]byte, error) {
	path, err := d.path(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	return data, keyed(key, err)
}

func (d Dir) Delete(_ context.Context, key string) error {
	path, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return keyed(key, err)
	}
	return nil
}

func (d Dir) DeletePrefix(_ context.Context, prefix string) error {
	path, err := d.path(prefix)
	if err != nil {
		return err
	}
	return keyed(prefix, os.RemoveAll(path))
}
