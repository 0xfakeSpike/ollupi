// Package blobs keeps files that outlive a process and are not part of a database. A store is a folder on this
// machine for now; one on an object store implements the same four calls.
package blobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Store holds whole objects under keys with slash-separated names, the way a bucket does.
type Store interface {
	// Put stores a new object; a key is written once.
	Put(ctx context.Context, key string, data []byte) error
	// Get reports a missing object with an error that is fs.ErrNotExist.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes an object; one already gone is no error.
	Delete(ctx context.Context, key string) error
	// DeletePrefix removes every object under the folder prefix names.
	DeletePrefix(ctx context.Context, prefix string) error
}

// Dir is a Store in a folder on this machine.
type Dir string

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
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

func (d Dir) Get(_ context.Context, key string) ([]byte, error) {
	path, err := d.path(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (d Dir) Delete(_ context.Context, key string) error {
	path, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d Dir) DeletePrefix(_ context.Context, prefix string) error {
	path, err := d.path(prefix)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}
