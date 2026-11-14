package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

// ErrEnvironmentInUse is an environment a conversation runs in, which cannot be removed under it.
var ErrEnvironmentInUse = errors.New("a conversation runs in the environment")

// Environments lists where the user's conversations can run, their environment on this machine first: the one a
// conversation that names none runs in.
func (o *Orchestrator) Environments(user string) ([]repo.EnvironmentRow, error) {
	return o.cfg.DB.Environments(user)
}

// AddEnvironment names another place a conversation can run: a cloud sandbox of the user's, which the host reaches
// through a sandbox.Machine. This machine is one environment already, and stays.
func (o *Orchestrator) AddEnvironment(user, name string, kind repo.EnvironmentKind) (repo.EnvironmentRow, error) {
	name, err := environmentName(name)
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	switch {
	case kind == "":
		kind = repo.EnvironmentMachine
	case kind == repo.EnvironmentLocal:
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: %w: this machine is one environment and every user has it; name a cloud sandbox instead", ErrInvalidName)
	case kind != repo.EnvironmentMachine:
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: %w: an environment is %q, not %q", ErrInvalidName, repo.EnvironmentMachine, kind)
	}
	now := time.Now().UTC()
	row, err := o.cfg.DB.AddEnvironment(
		repo.EnvironmentRow{ID: uuid.NewString(), UserID: user, Name: name, Kind: kind, CreatedAt: now, UpdatedAt: now},
		func(existing repo.EnvironmentRow) bool { return strings.EqualFold(existing.Name, name) })
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	o.emit(Event{Kind: EventEnvironmentChanged, Environment: &row})
	return row, nil
}

func (o *Orchestrator) RenameEnvironment(user, id, name string) (repo.EnvironmentRow, error) {
	name, err := environmentName(name)
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	rows, err := o.cfg.DB.Environments(user)
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	for _, other := range rows {
		if other.ID != id && strings.EqualFold(other.Name, name) {
			return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: %w: you already have an environment called %q", ErrInvalidName, other.Name)
		}
	}
	row, err := o.cfg.DB.RenameEnvironment(user, id, name, time.Now().UTC())
	if errors.Is(err, repo.ErrNotFound) {
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: environment %s: %w", id, repo.ErrNotFound)
	}
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	o.emit(Event{Kind: EventEnvironmentChanged, Environment: &row})
	return row, nil
}

// DeleteEnvironment removes a cloud sandbox from the list. This machine stays, since the workspaces are folders in it,
// and so does any environment a conversation runs in: a conversation does not move while it exists. It returns the
// environment it removed.
func (o *Orchestrator) DeleteEnvironment(user, id string) (repo.EnvironmentRow, error) {
	row, err := o.cfg.DB.EnvironmentOf(user, id)
	if errors.Is(err, repo.ErrNotFound) {
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: environment %s: %w", id, repo.ErrNotFound)
	}
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	if row.Kind == repo.EnvironmentLocal {
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: %w: %s is this machine, whose folders the workspaces are", ErrInvalidName, row.Name)
	}
	used, err := o.cfg.DB.EnvironmentInUse(id)
	if err != nil {
		return repo.EnvironmentRow{}, err
	}
	if used {
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: environment %s: %w", row.Name, ErrEnvironmentInUse)
	}
	if _, err := o.cfg.DB.DeleteEnvironment(user, id); err != nil {
		return repo.EnvironmentRow{}, err
	}
	o.emit(Event{Kind: EventEnvironmentChanged, Environment: &row})
	return row, nil
}

// environmentFor is the environment a conversation runs in: the one it names, or the user's on this machine.
func (o *Orchestrator) environmentFor(user, id string) (repo.EnvironmentRow, error) {
	if id == "" {
		env, err := o.cfg.DB.LocalEnvironment(user)
		if errors.Is(err, repo.ErrNotFound) {
			return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: the user %s has no environment on this machine", user)
		}
		return env, err
	}
	env, err := o.cfg.DB.EnvironmentOf(user, id)
	if errors.Is(err, repo.ErrNotFound) {
		return repo.EnvironmentRow{}, fmt.Errorf("orchestrator: environment %s: %w", id, repo.ErrNotFound)
	}
	return env, err
}

func environmentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return "", fmt.Errorf("orchestrator: %w: an environment name must be 1 to 120 characters", ErrInvalidName)
	}
	return name, nil
}
