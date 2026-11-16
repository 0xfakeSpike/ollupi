package memory

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

var ErrInvalidChange = errors.New("invalid memory change")

func (m *Manager) List(user string, scope db.MemoryScope, workspaceID string) ([]db.MemoryRow, error) {
	if err := checkPlace(scope, workspaceID); err != nil {
		return nil, err
	}
	entries, err := m.db.Memories(user, scope, workspaceID, false)
	if entries == nil {
		entries = []db.MemoryRow{}
	}
	return entries, err
}

func checkPlace(scope db.MemoryScope, workspaceID string) error {
	if !slices.Contains(db.MemoryScopeValues, scope) {
		return fmt.Errorf("%w: the scope must be one of %v", ErrInvalidChange, db.MemoryScopeValues)
	}
	if (scope == db.MemoryWorkspace) != (workspaceID != "") {
		return fmt.Errorf("%w: give a workspaceId with the workspace scope, and only with it", ErrInvalidChange)
	}
	return nil
}

func (m *Manager) Move(user, id string) (db.MemoryRow, error) {
	var out db.MemoryRow
	err := m.db.Tx(func(tx *db.DB) error {
		e, err := tx.Memory(user, id)
		if err != nil {
			return err
		}
		if e.Status == db.MemoryForgotten {
			return fmt.Errorf("%w: a forgotten memory cannot be moved", ErrInvalidChange)
		}
		if e.Scope == db.MemoryGlobal {
			e.Scope, e.WorkspaceID, e.OriginWorkspaceID = localScope(e.OriginWorkspaceID), e.OriginWorkspaceID, ""
		} else {
			e.Scope, e.WorkspaceID, e.OriginWorkspaceID = db.MemoryGlobal, "", e.WorkspaceID
		}
		there, err := tx.Memories(user, e.Scope, e.WorkspaceID, false)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(there, func(o db.MemoryRow) bool { return o.Topic == e.Topic }) {
			return fmt.Errorf("%w: a memory with the topic %q is already there; forget or edit that one instead", ErrInvalidChange, e.Topic)
		}
		e.UpdatedAt = time.Now().UTC()
		out = e
		return tx.SaveMemory(e)
	})
	return out, err
}

func (m *Manager) Status(user string) (db.MemoryStatus, error) {
	s, err := m.db.MemoryStatus(user)
	s.GenerationEnabled, s.UseEnabled = m.opts.Generate, m.opts.Use
	return s, err
}

func (m *Manager) Edit(user, id, text string) (db.MemoryRow, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > maxTextRunes {
		return db.MemoryRow{}, fmt.Errorf("%w: memory text must contain 1 to %d characters", ErrInvalidChange, maxTextRunes)
	}
	var out db.MemoryRow
	err := m.db.Tx(func(tx *db.DB) error {
		e, err := tx.Memory(user, id)
		if err != nil {
			return err
		}
		if e.Status == db.MemoryForgotten {
			return fmt.Errorf("%w: a forgotten memory cannot be edited", ErrInvalidChange)
		}
		e.Text, e.UserEdited, e.Status = text, true, db.MemoryActive
		e.UpdatedAt = time.Now().UTC()
		out = e
		return tx.SaveMemory(e)
	})
	return out, err
}

func (m *Manager) Forget(user, id string) error {
	return m.db.Tx(func(tx *db.DB) error {
		e, err := tx.Memory(user, id)
		if err != nil {
			return err
		}
		if e.Status == db.MemoryForgotten {
			return nil
		}
		e.Status, e.Text = db.MemoryForgotten, ""
		e.UpdatedAt = time.Now().UTC()
		return tx.SaveMemory(e)
	})
}
