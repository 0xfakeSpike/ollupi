package db

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
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
	// The columns that can be large come last.
	Attachments []Attachment `gorm:"serializer:json" json:"attachments,omitempty"`
	Input       string       `gorm:"not null;default:''" json:"input,omitempty"`
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
	// TurnWaitingOnBackground waits for background calls of its thread to end.
	TurnWaitingOnBackground TurnStatus = "waitingOnBackground"
	TurnCompleted           TurnStatus = "completed"
	TurnInterrupted         TurnStatus = "interrupted"
	TurnFailed              TurnStatus = "failed"
)

var TurnStatusValues = []TurnStatus{TurnReady, TurnInProgress, TurnWaitingOnApproval, TurnWaitingOnUserInput,
	TurnWaitingOnAgent, TurnWaitingOnBackground, TurnCompleted, TurnInterrupted, TurnFailed}

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

// StrandedThreads lists the threads a stopped process may have left without a way forward: a turn it was running, or
// a call awaiting background calls that ended as it stopped. A subagent reports as its turn ends, in the same write.
func (d *DB) StrandedThreads() ([]string, error) {
	var ids []string
	err := d.gorm.Raw(`SELECT DISTINCT thread_id FROM turns WHERE status IN ('inProgress', 'waitingOnBackground')`).Scan(&ids).Error
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

// waitingOn is the status of a turn out of its slot with these open calls: an approval or an answer to wait for
// comes first, since only a person can give it; then background calls; with nothing awaited, the turn is ready. A turn
// waits on its subagents only once it answered (AfterAnswer), since they come back as items, not as calls.
func waitingOn(open []ItemRow) TurnStatus {
	status := TurnReady
	for _, it := range open {
		switch {
		case it.WaitsForApproval():
			return TurnWaitingOnApproval
		case it.WaitsOnUser():
			status = TurnWaitingOnUserInput
		case it.waitsOnBackground() && status == TurnReady:
			status = TurnWaitingOnBackground
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

// SubmitTurn writes the thread's next turn with its message, and reports whether it waits behind a turn of the thread
// that has not ended: that one stays the thread's current turn until it ends.
func (d *DB) SubmitTurn(threadID string, turn TurnRow) (ThreadRow, TurnRow, ItemRow, bool, error) {
	var thread ThreadRow
	var message ItemRow
	var behind bool
	err := d.Tx(func(tx *DB) error {
		var err error
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		if thread.ArchivedAt != nil {
			return ErrThreadArchived
		}
		if turn.ID == "" || (strings.TrimSpace(turn.Input) == "" && len(turn.Attachments) == 0) {
			return fmt.Errorf("a new turn needs an id and an input")
		}
		var open int64
		if err := tx.gorm.Model(&TurnRow{}).Where("thread_id = ? AND "+openTurn, threadID).Count(&open).Error; err != nil {
			return err
		}
		behind = open > 0
		thread, turn, message, err = tx.writeTurn(thread, turn)
		return err
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, false, fmt.Errorf("storage: submit turn: %w", err)
	}
	return thread, turn, message, behind, nil
}

// writeTurn writes the thread's next turn, ready, with its input as its message.
func (d *DB) writeTurn(t ThreadRow, turn TurnRow) (ThreadRow, TurnRow, ItemRow, error) {
	seq, err := d.nextTurnSeq(t.ID)
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	turn.ThreadID, turn.Seq, turn.Status = t.ID, seq, TurnReady
	if err := d.createTurn(turn); err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	now := time.Now().UTC()
	message, _, err := d.saveItemOf(t.ID, NewUserMessage(uuid.NewString(), turn.ID, turn.Input, turn.Attachments, now))
	if err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	t.UpdatedAt = now
	if err := d.touchThread(t.ID, now); err != nil {
		return ThreadRow{}, TurnRow{}, ItemRow{}, err
	}
	return t, turn, message, nil
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
		active, err := tx.ActiveTurn(threadID)
		if errors.Is(err, ErrNotFound) || (err == nil && active.ID != turn.ID) {
			return fmt.Errorf("turn %s is not in progress", turn.ID)
		}
		if err != nil {
			return err
		}
		if !turn.Status.Ended() {
			return fmt.Errorf("turn %s must end with a final status", turn.ID)
		}
		now := time.Now().UTC()
		turn.ThreadID, turn.Seq, turn.CompletedAt = threadID, active.Seq, &now
		if err := tx.updateTurn(turn); err != nil {
			return err
		}
		var open []ItemRow
		if err := tx.gorm.Where("thread_id = ? AND turn_id = ? AND status IN ('pending', 'inProgress')", threadID, turn.ID).Order("seq").Find(&open).Error; err != nil {
			return err
		}
		for _, it := range open {
			it.Status = ItemFailed
			it.Error = fmt.Sprintf("the turn ended (%s) before this item finished", turn.Status)
			it.CompletedAt = &now
			if _, _, err := tx.saveItemOf(threadID, it); err != nil {
				return err
			}
			closed = append(closed, it)
		}
		if thread, err = tx.ThreadRow(threadID); err != nil {
			return err
		}
		// The turn's end is news to whoever has not read the thread since.
		thread.UpdatedAt, thread.RepliedAt = now, &now
		if err := checkOne(tx.gorm.Model(&ThreadRow{}).Where("id = ?", threadID).Updates(map[string]any{"updated_at": now, "replied_at": now})); err != nil {
			return fmt.Errorf("thread %s: %w", threadID, err)
		}
		report, err = tx.reportToParent(thread, turn)
		return err
	})
	if err != nil {
		return ThreadRow{}, TurnRow{}, nil, nil, fmt.Errorf("storage: end turn: %w", err)
	}
	return thread, turn, closed, report, nil
}

// AfterAnswer settles the turn in progress whose run gave its final answer. Input that came after the answer makes it
// ready, to answer that too; subagents it launched that still run make it wait on them, since a turn ends only once
// its subagents reported. Otherwise it returns TurnCompleted, for the caller to end the turn.
func (d *DB) AfterAnswer(turnID string) (TurnStatus, error) {
	next := TurnCompleted
	err := d.Tx(func(tx *DB) error {
		more, err := tx.InputAfterAnswer(turnID)
		if err != nil {
			return err
		}
		if more {
			next = TurnReady
		} else if running, err := tx.RunningSubagents(turnID); err != nil {
			return err
		} else if len(running) > 0 {
			next = TurnWaitingOnAgent
		}
		if next == TurnCompleted {
			return nil
		}
		return checkOne(tx.gorm.Model(&TurnRow{}).Where("id = ? AND status = ?", turnID, TurnInProgress).Update("status", next))
	})
	if err != nil {
		return "", fmt.Errorf("storage: settle the answer of turn %s: %w", turnID, err)
	}
	return next, nil
}

func (d *DB) FinalAnswer(threadID, turnID string) (string, error) {
	var row ItemRow
	err := notFound(d.gorm.Where("thread_id = ? AND turn_id = ? AND type = 'agentMessage' AND status = 'completed'", threadID, turnID).Order("seq DESC").Take(&row).Error)
	return row.Text, err
}

const (
	compactedItem = "type = 'contextCompaction' AND status = 'completed'"
	// carriedItem marks the items before the latest compaction whose effect outlasts it: user messages, which the
	// compacted history quotes; calls allowed for the conversation; and loaded tools and skills, which stay active.
	carriedItem = "type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))))"
)

// conversationOrder is the order of a thread's items: by their turns, and within a turn as they were written. A turn
// submitted behind another has its message written first, yet it follows all of that turn.
const conversationOrder = "turn_seq, seq"

// position is where an item stands in its thread's conversationOrder.
type position struct{ TurnSeq, Seq int }

// latestCompaction is the position of the thread's latest completed compaction up to the turn numbered upTo, or the
// zero position, before every item, when there is none.
func (d *DB) latestCompaction(threadID string, upTo int) (position, error) {
	var at []position
	err := d.gorm.Table("items INDEXED BY items_compactions").Where("thread_id = ? AND turn_seq <= ? AND "+compactedItem, threadID, upTo).
		Order("turn_seq DESC, seq DESC").Limit(1).Select("turn_seq, seq").Scan(&at).Error
	if err != nil || len(at) == 0 {
		return position{}, err
	}
	return at[0], nil
}

// RunItems reads, in order, the items a run of the turn numbered upTo works from: of that turn and those before it,
// every item from the latest completed compaction on, and before it only the carried ones, without their native
// records. The turns after it, which wait for it, are not its conversation yet. A run that needs more of the earlier
// items must widen carriedItem and its index. INDEXED BY makes a condition that drifted from its index an error.
func (d *DB) RunItems(threadID string, upTo int) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.Tx(func(tx *DB) error {
		from, err := tx.latestCompaction(threadID, upTo)
		if err != nil {
			return err
		}
		if err := tx.gorm.Table("items INDEXED BY items_carried").Omit("native").Where("thread_id = ? AND (turn_seq, seq) < (?, ?) AND ("+carriedItem+")", threadID, from.TurnSeq, from.Seq).Order(conversationOrder).Find(&rows).Error; err != nil {
			return err
		}
		var recent []ItemRow
		if err := tx.gorm.Table("items INDEXED BY items_thread_order").Where("thread_id = ? AND (turn_seq, seq) >= (?, ?) AND turn_seq <= ?", threadID, from.TurnSeq, from.Seq, upTo).Order(conversationOrder).Find(&recent).Error; err != nil {
			return err
		}
		rows = append(rows, recent...)
		return nil
	})
	return rows, err
}

func (d *DB) TurnItems(turnID string) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.gorm.Where("turn_id = ?", turnID).Order("seq").Find(&rows).Error
	return rows, err
}

func (d *DB) InputAfterAnswer(turnID string) (bool, error) {
	var n int64
	err := d.gorm.Model(&ItemRow{}).Where("turn_id = ? AND type IN ('userMessage', 'backgroundResult', 'agentReport') AND seq > (SELECT COALESCE(MAX(seq), 0) FROM items WHERE turn_id = ? AND type = 'agentMessage' AND status = 'completed')", turnID, turnID).Count(&n).Error
	return n > 0, err
}
