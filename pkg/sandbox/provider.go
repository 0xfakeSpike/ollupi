package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// A Provider is one kind of sandbox a deployment offers: this machine, or a
// cloud's. Each environment a user has is one sandbox of a provider; the
// provider makes it the first time it is opened and finds it again after.
// This machine plugs in as a provider like any cloud does (Local).
type Provider interface {
	// Name is what a person calls the provider's sandboxes; the first
	// environment of a user is named it.
	Name() string
	// Check says whether config is one this provider can make a sandbox of:
	// what a user chose for an environment (a template, a region). Empty is
	// the provider's default.
	Check(config json.RawMessage) error
	// Open gives the box of an environment, making its sandbox when the
	// environment has none yet and resuming the one it has otherwise. The
	// box is the caller's until it closes it.
	Open(ctx context.Context, env Env) (Box, error)
	// Remove gets rid of an environment's sandbox for good, once its
	// environment is deleted. A sandbox that is gone already is no error.
	Remove(ctx context.Context, env Env) error
}

// Env is one environment as its provider sees it.
type Env struct {
	User string
	ID   string
	// Config is what the user chose for it, which Check accepted.
	Config json.RawMessage
	// State is what the provider kept of the sandbox last time (Keep), as a
	// cloud sandbox's id: nil before the sandbox is made.
	State json.RawMessage
	// Keep records state for the next Open, before the box is handed out:
	// a sandbox made is found again after a restart.
	Keep func(state json.RawMessage) error
}

// Boxes finds the box of one of a user's environments; an empty
// environment is the user's first.
type Boxes func(ctx context.Context, user, environment string) (Box, error)

// Fixed is Boxes that gives b for every user and environment, for a tool
// used on its own.
func Fixed(b Box) Boxes {
	return func(context.Context, string, string) (Box, error) { return b, nil }
}

// LocalName is what the sandboxes of this machine are called.
const LocalName = "This computer"

// Local is this machine as a provider. Every environment of it is a folder
// of this machine, Dir/<user>/<environment>, where a command runs when the
// conversation has no project; commands run with the limits of Config,
// whose Roots it sets. Its environments keep no state.
type Local struct {
	Dir    string
	Config Config
}

func (Local) Name() string { return LocalName }

func (Local) Check(config json.RawMessage) error {
	if c := bytes.TrimSpace(config); len(c) > 0 && !bytes.Equal(c, []byte("{}")) && !bytes.Equal(c, []byte("null")) {
		return errors.New("sandbox: this machine takes no settings")
	}
	return nil
}

func (l Local) Open(_ context.Context, env Env) (Box, error) {
	root, err := l.root(env)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox: the folder of environment %s: %w", env.ID, err)
	}
	c := l.Config
	c.Roots = []string{root}
	return New(c)
}

// Remove leaves the environment's folder: what is in it is the user's.
func (l Local) Remove(context.Context, Env) error { return nil }

func (l Local) root(env Env) (string, error) {
	if l.Dir == "" {
		return "", errors.New("sandbox: this machine has no folder for its environments")
	}
	for _, part := range []string{env.User, env.ID} {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part {
			return "", fmt.Errorf("sandbox: %q cannot name a folder", part)
		}
	}
	return filepath.Join(l.Dir, env.User, env.ID), nil
}
