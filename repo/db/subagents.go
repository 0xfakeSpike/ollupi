package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (d *DB) WriteSubagent(parentThreadID, itemID, title string, turn TurnRow, task string) (ThreadRow, error) {
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
		turn.ID, turn.Model, turn.CreatedAt, turn.StartedAt = uuid.NewString(), current.Model.Clone(), now, now
		child, _, _, err = tx.writeTurn(child, turn, task, nil)
		return err
	})
	if err != nil {
		return ThreadRow{}, fmt.Errorf("storage: write a subagent: %w", err)
	}
	return child, nil
}

func (d *DB) RunningSubagents(turnID string) ([]string, error) {
	var ids []string
	err := d.gorm.Raw(`SELECT c.id FROM threads c JOIN items i ON i.id = c.parent_item_id
		WHERE c.parent_thread_id = (SELECT thread_id FROM turns WHERE id = ?) AND i.turn_id = ?
			AND EXISTS (SELECT 1 FROM turns t WHERE t.thread_id = c.id AND NOT (t.status = 'completed' OR t.status = 'failed' OR t.status = 'interrupted'))
		ORDER BY c.created_at, c.id`, turnID, turnID).Scan(&ids).Error
	return ids, err
}

type SubagentReport struct {
	Thread ThreadRow
	Turn   TurnRow
	Item   ItemRow
}

func (d *DB) reportToParent(child ThreadRow, ended TurnRow) (*SubagentReport, error) {
	if child.ParentItemID == "" {
		return nil, nil
	}
	call, err := d.Item(string(child.ParentItemID))
	if err != nil {
		return nil, fmt.Errorf("the call that launched subagent %s: %w", child.ID, err)
	}
	parent, err := d.Turn(call.TurnID)
	if err != nil || parent.Status.Ended() {
		return nil, err
	}
	now := time.Now().UTC()
	report := ItemRow{ID: uuid.NewString(), TurnID: parent.ID, Type: ItemAgentReport, Status: ItemFailed, Tool: call.Tool, CallID: call.CallID, LaunchItemID: call.ID,
		Text: child.Title, StartedAt: now, CompletedAt: &now}
	switch ended.Status {
	case TurnCompleted:
		answer, err := d.FinalAnswer(child.ID, ended.ID)
		if errors.Is(err, ErrNotFound) {
			report.Error = "the subagent ended without a final answer"
			break
		}
		if err != nil {
			return nil, err
		}
		report.Result, _, _ = ResultOf(tools.Result{Output: answer}) // a text: it cannot fail
		report.Status = ItemCompleted
	case TurnInterrupted:
		report.Error = "the subagent was stopped: " + ended.Error
	default:
		report.Error = "the subagent failed: " + ended.Error
	}
	stored, _, err := d.saveItemOf(string(child.ParentThreadID), report)
	if err != nil {
		return nil, err
	}
	if parent.Status == TurnWaitingOnAgent {
		running, err := d.RunningSubagents(parent.ID)
		if err != nil {
			return nil, err
		}
		if len(running) == 0 {
			if err := checkOne(d.gorm.Model(&TurnRow{}).Where("id = ? AND status = ?", parent.ID, TurnWaitingOnAgent).Update("status", TurnReady)); err != nil {
				return nil, err
			}
			parent.Status = TurnReady
		}
	}
	thread, err := d.ThreadRow(string(child.ParentThreadID))
	if err != nil {
		return nil, err
	}
	return &SubagentReport{Thread: thread, Turn: parent, Item: stored}, nil
}

func (d *DB) FinalAnswer(threadID, turnID string) (string, error) {
	var row ItemRow
	err := notFound(d.gorm.Where("thread_id = ? AND turn_id = ? AND type = 'agentMessage' AND status = 'completed'", threadID, turnID).Order("seq DESC").Take(&row).Error)
	return row.Text, err
}
