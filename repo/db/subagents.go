package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// QueueSubagent writes the conversation of a subagent that the call item of the parent thread launches, with its
// first turn queued: turn's input as its task and turn's agent as its role, on the model of the parent's current
// turn. The subagent's conversation is the parent's, in its workspace and environment, under title. A call that
// launched one already gets it back.
func (d *DB) QueueSubagent(parentThreadID, itemID, title string, turn TurnRow) (ThreadRow, error) {
	var child ThreadRow
	err := d.Tx(func(tx *DB) error {
		err := tx.gorm.Where("parent_item_id = ?", itemID).Take(&child).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		parent, err := tx.ThreadRow(parentThreadID)
		if err != nil {
			return err
		}
		current, err := tx.ActiveTurn(parentThreadID)
		if err != nil {
			return fmt.Errorf("the turn of thread %s that launches the subagent: %w", parentThreadID, err)
		}
		now := time.Now().UTC()
		child = ThreadRow{
			ID: uuid.NewString(), UserID: parent.UserID, Title: title,
			ParentThreadID: Ref(parent.ID), ParentItemID: Ref(itemID),
			WorkspaceID: parent.WorkspaceID, EnvironmentID: parent.EnvironmentID, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.CreateThread(child); err != nil {
			return err
		}
		turn.ID, turn.ThreadID, turn.Seq, turn.Status = uuid.NewString(), child.ID, 1, TurnQueued
		turn.Model, turn.CreatedAt, turn.StartedAt = current.Model.Clone(), now, now
		return tx.createTurn(turn)
	})
	if err != nil {
		return ThreadRow{}, fmt.Errorf("storage: queue a subagent: %w", err)
	}
	return child, nil
}
