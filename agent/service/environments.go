package service

import (
	"errors"
	"fmt"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

var ErrEnvironmentInUse = errors.New("a conversation runs in the environment")

// This machine's environment comes first: the one a conversation that names none runs in.
func (sv *Service) Environments(user string) ([]db.EnvironmentRow, error) {
	return sv.cfg.DB.Environments(user)
}

func (sv *Service) AddEnvironment(user, name string, kind db.EnvironmentKind) (db.EnvironmentRow, error) {
	name, err := environmentName(name)
	if err != nil {
		return db.EnvironmentRow{}, err
	}
	switch {
	case kind == "":
		kind = db.EnvironmentMachine
	case kind == db.EnvironmentLocal:
		return db.EnvironmentRow{}, fmt.Errorf("service: %w: this machine is one environment and every user has it; name a cloud sandbox instead", ErrInvalidName)
	case kind != db.EnvironmentMachine:
		return db.EnvironmentRow{}, fmt.Errorf("service: %w: an environment is %q, not %q", ErrInvalidName, db.EnvironmentMachine, kind)
	}
	now := time.Now().UTC()
	row, err := sv.cfg.DB.AddEnvironment(
		db.EnvironmentRow{ID: uuid.NewString(), UserID: user, Name: name, Kind: kind, CreatedAt: now, UpdatedAt: now},
		func(existing db.EnvironmentRow) bool { return strings.EqualFold(existing.Name, name) })
	if err != nil {
		return db.EnvironmentRow{}, err
	}
	sv.ev.Emit(event.Event{Kind: event.EnvironmentChanged, Environment: &row})
	return row, nil
}

func (sv *Service) RenameEnvironment(user, id, name string) (db.EnvironmentRow, error) {
	name, err := environmentName(name)
	if err != nil {
		return db.EnvironmentRow{}, err
	}
	rows, err := sv.cfg.DB.Environments(user)
	if err != nil {
		return db.EnvironmentRow{}, err
	}
	for _, other := range rows {
		if other.ID != id && strings.EqualFold(other.Name, name) {
			return db.EnvironmentRow{}, fmt.Errorf("service: %w: you already have an environment called %q", ErrInvalidName, other.Name)
		}
	}
	row, err := sv.cfg.DB.RenameEnvironment(user, id, name, time.Now().UTC())
	if err != nil {
		return db.EnvironmentRow{}, fmt.Errorf("service: environment %s: %w", id, err)
	}
	sv.ev.Emit(event.Event{Kind: event.EnvironmentChanged, Environment: &row})
	return row, nil
}

// This machine stays, since the workspaces are folders in it, and so does any environment a conversation runs
// in: a conversation does not move while it exists.
func (sv *Service) DeleteEnvironment(user, id string) (db.EnvironmentRow, error) {
	row, err := sv.cfg.DB.EnvironmentOf(user, id)
	if err != nil {
		return db.EnvironmentRow{}, fmt.Errorf("service: environment %s: %w", id, err)
	}
	if row.Kind == db.EnvironmentLocal {
		return db.EnvironmentRow{}, fmt.Errorf("service: %w: %s is this machine, whose folders the workspaces are", ErrInvalidName, row.Name)
	}
	used, err := sv.cfg.DB.EnvironmentInUse(id)
	if err != nil {
		return db.EnvironmentRow{}, err
	}
	if used {
		return db.EnvironmentRow{}, fmt.Errorf("service: environment %s: %w", row.Name, ErrEnvironmentInUse)
	}
	if _, err := sv.cfg.DB.DeleteEnvironment(user, id); err != nil {
		return db.EnvironmentRow{}, err
	}
	sv.ev.Emit(event.Event{Kind: event.EnvironmentChanged, Environment: &row})
	return row, nil
}

func (sv *Service) environmentFor(user, id string) (db.EnvironmentRow, error) {
	if id == "" {
		env, err := sv.cfg.DB.LocalEnvironment(user)
		if errors.Is(err, db.ErrNotFound) {
			return db.EnvironmentRow{}, fmt.Errorf("service: the user %s has no environment on this machine", user)
		}
		return env, err
	}
	env, err := sv.cfg.DB.EnvironmentOf(user, id)
	if err != nil {
		return db.EnvironmentRow{}, fmt.Errorf("service: environment %s: %w", id, err)
	}
	return env, nil
}

func environmentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return "", fmt.Errorf("service: %w: an environment name must be 1 to 120 characters", ErrInvalidName)
	}
	return name, nil
}
