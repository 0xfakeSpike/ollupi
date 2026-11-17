package service

import (
	"fmt"

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (sv *Service) Memories(user string, scope db.MemoryScope, workspaceID string) ([]db.MemoryRow, error) {
	m, err := sv.memory()
	if err != nil {
		return nil, err
	}
	return m.List(user, scope, workspaceID)
}

func (sv *Service) MemoryStatus(user string) (db.MemoryStatus, error) {
	m, err := sv.memory()
	if err != nil {
		return db.MemoryStatus{}, err
	}
	return m.Status(user)
}

func (sv *Service) EditMemory(user, id, text string) (db.MemoryRow, error) {
	m, err := sv.memory()
	if err != nil {
		return db.MemoryRow{}, err
	}
	return m.Edit(user, id, text)
}

func (sv *Service) MoveMemory(user, id string) (db.MemoryRow, error) {
	m, err := sv.memory()
	if err != nil {
		return db.MemoryRow{}, err
	}
	return m.Move(user, id)
}

func (sv *Service) ForgetMemory(user, id string) error {
	m, err := sv.memory()
	if err != nil {
		return err
	}
	return m.Forget(user, id)
}

func (sv *Service) memory() (*memory.Manager, error) {
	if sv.cfg.Memory == nil {
		return nil, fmt.Errorf("service: long-term memory is %w", ErrUnavailable)
	}
	return sv.cfg.Memory, nil
}
