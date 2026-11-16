package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/google/uuid"
)

type Usage struct {
	InputTokens       int64 `gorm:"not null" json:"inputTokens"`
	CachedInputTokens int64 `gorm:"not null" json:"cachedInputTokens"`
	// CacheWriteInputTokens counts input tokens written to the provider's prompt cache; they are part of InputTokens.
	CacheWriteInputTokens int64 `gorm:"not null" json:"cacheWriteInputTokens"`
	OutputTokens          int64 `gorm:"not null" json:"outputTokens"`
}

func (u *Usage) Add(v Usage) {
	u.InputTokens += v.InputTokens
	u.CachedInputTokens += v.CachedInputTokens
	u.CacheWriteInputTokens += v.CacheWriteInputTokens
	u.OutputTokens += v.OutputTokens
}

type TurnRow struct {
	ID       string     `gorm:"primaryKey;index:turns_ended,priority:2" json:"id"`
	ThreadID string     `gorm:"not null;uniqueIndex:turns_thread_seq,priority:1;uniqueIndex:turns_one_running,where:status = 'inProgress';index:turns_open,where:NOT (status = 'completed' OR status = 'failed' OR status = 'interrupted')" json:"threadId"`
	Thread   *ThreadRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Seq      int        `gorm:"not null;uniqueIndex:turns_thread_seq,priority:2" json:"seq"`
	Status   TurnStatus `gorm:"not null" json:"status"`
	Model    llm.Model  `gorm:"not null;serializer:json;check:turns_model,json_valid(model)" json:"model"`
	// Agent is the role of agent that runs the turn, one of role.NameValues.
	Agent string `gorm:"not null;default:'full-main-agent'" json:"agent"`
	Usage Usage  `gorm:"embedded" json:"usage"`
	Error string `gorm:"not null;default:''" json:"error,omitempty"`
	// turns_cron_job finds the turns a scheduled task sent, newest last; the others stay out of it.
	CronJobID string `gorm:"not null;default:'';index:turns_cron_job,priority:1,where:cron_job_id <> ''" json:"cronJobId,omitempty"`
	// CreatedAt is when the turn was submitted, and StartedAt too: a turn that waits behind another runs once that
	// one ends, which the items of the thread tell.
	CreatedAt time.Time `gorm:"not null;autoCreateTime:false;index:turns_cron_job,priority:2" json:"createdAt"`
	// turns_ready orders the turns that wait for a slot; its condition is readyTurn, word for word.
	StartedAt time.Time `gorm:"not null;index:turns_ready,where:status = 'ready'" json:"startedAt"`
	// turns_ended orders the ended turns for memory extraction; its condition is endedTurn, word for word.
	CompletedAt *time.Time `gorm:"index:turns_ended,priority:1,where:(status = 'completed' OR status = 'failed' OR status = 'interrupted') AND completed_at IS NOT NULL" json:"completedAt,omitempty"`
}

func (TurnRow) TableName() string { return "turns" }

type TurnStatus string

// A thread runs one turn at a time: its current turn is its first that has not ended, and the later ones wait for it to
// end. A turn not ended is ready for a slot, in progress in one, or waiting on what its open calls await, holding none.
const (
	TurnReady              TurnStatus = "ready"
	TurnInProgress         TurnStatus = "inProgress"
	TurnWaitingOnApproval  TurnStatus = "waitingOnApproval"
	TurnWaitingOnUserInput TurnStatus = "waitingOnUserInput"
	TurnWaitingOnAgent     TurnStatus = "waitingOnAgent"
	TurnCompleted          TurnStatus = "completed"
	TurnInterrupted        TurnStatus = "interrupted"
	TurnFailed             TurnStatus = "failed"
)

var TurnStatusValues = []TurnStatus{TurnReady, TurnInProgress, TurnWaitingOnApproval, TurnWaitingOnUserInput,
	TurnWaitingOnAgent, TurnCompleted, TurnInterrupted, TurnFailed}

func (s TurnStatus) Value() (driver.Value, error) { return enumValue(s, TurnStatusValues) }

func (s TurnStatus) Ended() bool {
	return s == TurnCompleted || s == TurnInterrupted || s == TurnFailed
}

// The conditions of the turns_open and turns_ready indexes, word for word; INDEXED BY makes a drift an error.
const (
	openTurn  = "NOT (status = 'completed' OR status = 'failed' OR status = 'interrupted')"
	readyTurn = "status = 'ready'"
	// firstOpen holds for a turn of the turns table that no earlier open turn of its thread is ahead of.
	firstOpen = "NOT EXISTS (SELECT 1 FROM turns ahead WHERE ahead.thread_id = turns.thread_id AND ahead.seq < turns.seq AND NOT (ahead.status = 'completed' OR ahead.status = 'failed' OR ahead.status = 'interrupted'))"
)

// ReadyThreads lists up to limit threads whose current turn waits for a slot, conversations before subagents
// and, within each, the turn submitted first. The users in except are left out: they hold all the slots they may.
func (d *DB) ReadyThreads(limit int, except []string) ([]string, error) {
	var ids []string
	q := d.gorm.Table("turns INDEXED BY turns_ready").Joins("JOIN threads ON threads.id = turns.thread_id").Where("turns." + readyTurn + " AND " + firstOpen)
	if len(except) > 0 {
		q = q.Where("threads.user_id NOT IN ?", except)
	}
	err := q.Order("threads.parent_thread_id IS NOT NULL, turns.started_at, turns.thread_id").
		Limit(limit).Pluck("turns.thread_id", &ids).Error
	return ids, err
}

// ThreadsWaitingOnUser lists the user's threads whose turn waits for an approval or an answer.
func (d *DB) ThreadsWaitingOnUser(user string) ([]string, error) {
	var ids []string
	err := d.gorm.Model(&TurnRow{}).Where("status IN ? AND "+ofUser, []TurnStatus{TurnWaitingOnApproval, TurnWaitingOnUserInput}, user).
		Order("thread_id").Pluck("thread_id", &ids).Error
	return ids, err
}

// StrandedThreads lists the threads a stopped process may have left without a way forward: a turn it was running. A
// subagent reports as its turn ends, in the same write.
func (d *DB) StrandedThreads() ([]string, error) {
	var ids []string
	err := d.gorm.Raw(`SELECT DISTINCT thread_id FROM turns WHERE status = 'inProgress'`).Scan(&ids).Error
	return ids, err
}

// Launch records that the ready turn took a slot, which only its thread's current turn may.
func (d *DB) Launch(turnID string) error {
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND "+readyTurn+" AND "+firstOpen, turnID).Update("status", TurnInProgress)
	if err := checkOne(res); err != nil {
		return fmt.Errorf("storage: launch turn %s: %w", turnID, err)
	}
	return nil
}

// Park records that the turn left its slot without ending: it waits on what its open calls await, or is ready again.
func (d *DB) Park(turnID string) (TurnStatus, error) {
	var status TurnStatus
	err := d.Tx(func(tx *DB) error {
		var err error
		status, err = tx.settleStatus(turnID, TurnInProgress)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("storage: park turn %s: %w", turnID, err)
	}
	return status, nil
}

// settleStatus sets a turn that is in status from to what its open calls await, or to ready when they await nothing.
func (d *DB) settleStatus(turnID string, from TurnStatus) (TurnStatus, error) {
	var open []ItemRow
	if err := d.gorm.Where("turn_id = ? AND "+openCalls, turnID).Order("seq").Find(&open).Error; err != nil {
		return "", err
	}
	status := waitingOn(open)
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND status = ?", turnID, from).Update("status", status)
	if err := checkOne(res); err != nil {
		return "", err
	}
	return status, nil
}

// waitingOn is the status of a turn out of its slot with these open calls: an approval or an answer to wait for, or
// with nothing awaited ready. A turn waits on its subagents only once it answered (AfterAnswer), since they come back
// as items, not as calls.
func waitingOn(open []ItemRow) TurnStatus {
	status := TurnReady
	for _, it := range open {
		switch {
		case it.WaitsForApproval():
			return TurnWaitingOnApproval
		case it.WaitsOnUser():
			status = TurnWaitingOnUserInput
		}
	}
	return status
}

// HasTurnBehind says whether a turn the scheduled task sent to the thread still waits behind another of its turns.
func (d *DB) HasTurnBehind(threadID, cronJobID string) (bool, error) {
	var n int64
	err := d.gorm.Model(&TurnRow{}).Where("thread_id = ? AND cron_job_id = ? AND "+openTurn+" AND NOT "+firstOpen, threadID, cronJobID).Count(&n).Error
	return n > 0, err
}

// nextTurnSeq is the number the thread's next turn takes.
func (d *DB) nextTurnSeq(threadID string) (int, error) {
	var total int64
	err := d.gorm.Model(&TurnRow{}).Where("thread_id = ?", threadID).Count(&total).Error
	return int(total) + 1, err
}

func (d *DB) createTurn(row TurnRow) error {
	row.CreatedAt, row.StartedAt, row.CompletedAt = row.CreatedAt.UTC(), row.StartedAt.UTC(), nullTime(row.CompletedAt)
	return d.gorm.Create(&row).Error
}

func (d *DB) updateTurn(row TurnRow) error {
	res := d.gorm.Model(&TurnRow{}).Where("id = ? AND thread_id = ?", row.ID, row.ThreadID).Updates(map[string]any{
		"status": row.Status, "input_tokens": row.Usage.InputTokens, "cached_input_tokens": row.Usage.CachedInputTokens,
		"cache_write_input_tokens": row.Usage.CacheWriteInputTokens, "output_tokens": row.Usage.OutputTokens, "error": row.Error, "completed_at": nullTime(row.CompletedAt),
	})
	if err := checkOne(res); err != nil {
		return fmt.Errorf("turn %s: %w", row.ID, err)
	}
	return nil
}

// ActiveTurn is the thread's current turn: its first that has not ended.
func (d *DB) ActiveTurn(threadID string) (TurnRow, error) {
	var row TurnRow
	return row, notFound(d.gorm.Table("turns INDEXED BY turns_open").Where("thread_id = ? AND "+openTurn, threadID).Order("seq").Take(&row).Error)
}

func (d *DB) Turn(id string) (TurnRow, error) {
	var row TurnRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

func (d *DB) EndedTurn(id string) (TurnRow, error) {
	var row TurnRow
	return row, notFound(d.gorm.Where("id = ? AND status IN ('completed', 'failed', 'interrupted')", id).Take(&row).Error)
}

// A Submission is how a message submitted to a thread was taken.
type Submission int

const (
	// Started is a message that started the thread's current turn.
	Started Submission = iota
	// Queued is a message that started a turn behind the current one.
	Queued
	// Joined is a message that joined the user's turn waiting behind the current one, which answers it together with
	// the messages before it.
	Joined
)

// SubmitTurn takes a message to the thread, with attachments: into turn, which it writes as the thread's next turn,
// or, when the thread's last turn is one of the user's waiting behind the current one, into that turn, which takes
// turn's model and role, since it has not run yet. A scheduled task's turn joins none, and none joins it: a task
// skips while its last run waits (HasTurnBehind). It returns the turn the message is in.
func (d *DB) SubmitTurn(threadID string, turn TurnRow, text string, attachments []Attachment) (ThreadRow, TurnRow, ItemRow, Submission, error) {
	var thread ThreadRow
	var message ItemRow
	how := Started
	err := d.Tx(func(tx *DB) error {
		var err error
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		if thread.ArchivedAt != nil {
			return ErrThreadArchived
		}
		var open []TurnRow
		if err := tx.gorm.Table("turns INDEXED BY turns_open").Where("thread_id = ? AND "+openTurn, threadID).Order("seq").Find(&open).Error; err != nil {
			return err
		}
		if len(open) > 0 {
			how = Queued
		}
		if last := len(open) - 1; last > 0 && open[last].CronJobID == "" && turn.CronJobID == "" {
			waiting := open[last]
			waiting.Model, waiting.Agent = turn.Model, turn.Agent
			res := tx.gorm.Model(&TurnRow{}).Where("id = ? AND status = ?", waiting.ID, TurnReady).Select("model", "agent").Updates(&TurnRow{Model: waiting.Model, Agent: waiting.Agent})
			if err := checkOne(res); err != nil {
				return fmt.Errorf("turn %s: %w", waiting.ID, err)
			}
			how, turn = Joined, waiting
			thread, message, err = tx.writeMessage(thread, turn, text, attachments)
			return err
		}
		thread, turn, message, err = tx.writeTurn(thread, turn, text, attachments)
		return err
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, 0, fmt.Errorf("storage: submit turn: %w", err)
	}
	return thread, turn, message, how, nil
}

// QueuedMessages counts the messages of the thread that wait behind its current turn.
func (d *DB) QueuedMessages(threadID string) (int64, error) {
	var n int64
	err := d.gorm.Table("turns").Joins("JOIN items ON items.turn_id = turns.id").
		Where("turns.thread_id = ? AND items.type = 'userMessage' AND NOT (turns.status = 'completed' OR turns.status = 'failed' OR turns.status = 'interrupted') AND NOT "+firstOpen, threadID).
		Count(&n).Error
	return n, err
}

// writeTurn writes the thread's next turn, ready, with text and attachments as its message.
func (d *DB) writeTurn(t ThreadRow, turn TurnRow, text string, attachments []Attachment) (ThreadRow, TurnRow, ItemRow, error) {
	seq, err := d.nextTurnSeq(t.ID)
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	turn.ThreadID, turn.Seq, turn.Status = t.ID, seq, TurnReady
	if err := d.createTurn(turn); err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	t, message, err := d.writeMessage(t, turn, text, attachments)
	return t, turn, message, err
}

// writeMessage writes a user message into the turn, which has not run yet, and touches the thread.
func (d *DB) writeMessage(t ThreadRow, turn TurnRow, text string, attachments []Attachment) (ThreadRow, ItemRow, error) {
	now := time.Now().UTC()
	message, _, err := d.saveItemOf(t.ID, NewUserMessage(uuid.NewString(), turn.ID, text, attachments, now))
	if err != nil {
		return ThreadRow{}, ItemRow{}, err
	}
	t.UpdatedAt = now
	if err := d.touchThread(t.ID, now); err != nil {
		return ThreadRow{}, ItemRow{}, err
	}
	return t, message, nil
}

func (d *DB) AddUsage(threadID, turnID string, u Usage) error {
	if u == (Usage{}) {
		return nil
	}
	return d.Tx(func(tx *DB) error {
		row, err := tx.ActiveTurn(threadID)
		if errors.Is(err, ErrNotFound) || (err == nil && row.ID != turnID) {
			return fmt.Errorf("storage: turn %s is not in progress", turnID)
		}
		if err != nil {
			return err
		}
		row.Usage.Add(u)
		return tx.updateTurn(row)
	})
}

// EndTurn ends the thread's current turn: its items not finished fail, and a subagent's report comes back to the turn
// that launched it, which it returns.
func (d *DB) EndTurn(threadID string, turn TurnRow) (ThreadRow, TurnRow, []ItemRow, *SubagentReport, error) {
	var thread ThreadRow
	var closed []ItemRow
	var report *SubagentReport
	err := d.Tx(func(tx *DB) error {
		var err error
		thread, turn, closed, report, err = tx.endTurn(threadID, turn)
		return err
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, fmt.Errorf("storage: end turn: %w", err)
	}
	return thread, turn, closed, report, nil
}

func (tx *DB) endTurn(threadID string, turn TurnRow) (ThreadRow, TurnRow, []ItemRow, *SubagentReport, error) {
	active, err := tx.ActiveTurn(threadID)
	if errors.Is(err, ErrNotFound) || (err == nil && active.ID != turn.ID) {
		return ThreadRow{}, TurnRow{}, nil, nil, fmt.Errorf("turn %s is not in progress", turn.ID)
	}
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, err
	}
	now := time.Now().UTC()
	turn.ThreadID, turn.Seq, turn.CompletedAt = threadID, active.Seq, &now
	if err := tx.updateTurn(turn); err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, err
	}
	var open, closed []ItemRow
	if err := tx.gorm.Where("thread_id = ? AND turn_id = ? AND status IN ('pending', 'inProgress')", threadID, turn.ID).Order("seq").Find(&open).Error; err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, err
	}
	for _, it := range open {
		it.Status = ItemFailed
		it.Error = fmt.Sprintf("the turn ended (%s) before this item finished", turn.Status)
		it.CompletedAt = &now
		if _, _, err := tx.saveItemOf(threadID, it); err != nil {
			return ThreadRow{}, TurnRow{}, nil, nil, err
		}
		closed = append(closed, it)
	}
	thread, err := tx.ThreadRow(threadID)
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, err
	}
	// The turn's end is news to whoever has not read the thread since.
	thread.UpdatedAt, thread.RepliedAt = now, &now
	if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", threadID).Updates(map[string]any{"updated_at": now, "replied_at": now})); err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, fmt.Errorf("thread %s: %w", threadID, err)
	}
	report, err := tx.reportToParent(thread, turn)
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, err
	}
	return thread, turn, closed, report, nil
}

// AfterAnswer settles the turn in progress whose run gave its final answer, and says what became of it by the status
// of the turn it returns. Input that came after the answer makes it ready, to answer that too; subagents it launched
// that still run make it wait on them, since a turn ends only once its subagents reported. Otherwise it ends
// completed, as EndTurn ends a turn, in the same write as the look: a message cannot come to the turn between them
// and be left unanswered.
func (d *DB) AfterAnswer(threadID string, turn TurnRow) (ThreadRow, TurnRow, []ItemRow, *SubagentReport, error) {
	var thread ThreadRow
	var closed []ItemRow
	var report *SubagentReport
	err := d.Tx(func(tx *DB) error {
		more, err := tx.InputAfterAnswer(turn.ID)
		if err != nil {
			return err
		}
		running, err := tx.RunningSubagents(turn.ID)
		if err != nil {
			return err
		}
		switch {
		case more:
			turn.Status = TurnReady
		case len(running) > 0:
			turn.Status = TurnWaitingOnAgent
		default:
			turn.Status = TurnCompleted
			thread, turn, closed, report, err = tx.endTurn(threadID, turn)
			return err
		}
		return checkOne(tx.gorm.Model(&TurnRow{}).Where("id = ? AND status = ?", turn.ID, TurnInProgress).Update("status", turn.Status))
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, fmt.Errorf("storage: settle the answer of turn %s: %w", turn.ID, err)
	}
	return thread, turn, closed, report, nil
}

func (d *DB) TurnItems(turnID string) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.gorm.Where("turn_id = ?", turnID).Order("seq").Find(&rows).Error
	return rows, err
}

func (d *DB) InputAfterAnswer(turnID string) (bool, error) {
	var n int64
	err := d.gorm.Model(&ItemRow{}).Where("turn_id = ? AND type IN ('userMessage', 'agentReport') AND seq > (SELECT COALESCE(MAX(seq), 0) FROM items WHERE turn_id = ? AND type = 'agentMessage' AND status = 'completed')", turnID, turnID).Count(&n).Error
	return n > 0, err
}
