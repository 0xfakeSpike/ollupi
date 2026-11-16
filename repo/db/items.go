package db

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/tools"
	"gorm.io/gorm/clause"
)

type ItemRow struct {
	ID string `gorm:"primaryKey" json:"id"`
	// items_thread_order holds a thread's items in conversationOrder. items_compactions and items_carried serve RunItems;
	// their conditions are compactedItem and carriedItem, word for word.
	ThreadID string     `gorm:"not null;uniqueIndex:items_thread_seq,priority:1;index:items_thread_order,priority:1;index:items_compactions,priority:1,where:type = 'contextCompaction' AND status = 'completed';index:items_carried,priority:1,where:type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))))" json:"-"`
	Thread   *ThreadRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	// items_turn finds a turn's items by type and status: its open tool calls, its messages.
	TurnID string   `gorm:"not null;index:items_turn,priority:1" json:"turnId"`
	Turn   *TurnRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	// TurnSeq is the seq of the item's turn, which places it in the conversation before Seq does.
	TurnSeq int `gorm:"not null;default:0;index:items_thread_order,priority:2;index:items_compactions,priority:2;index:items_carried,priority:2" json:"-"`
	// Seq numbers the thread's items as they were written.
	Seq          int                `gorm:"not null;uniqueIndex:items_thread_seq,priority:2;index:items_turn,priority:4;index:items_thread_order,priority:3;index:items_compactions,priority:3;index:items_carried,priority:3" json:"seq"`
	Type         ItemType           `gorm:"not null;index:items_turn,priority:2" json:"type"`
	Status       ItemStatus         `gorm:"not null;index:items_turn,priority:3" json:"status"`
	CallID       string             `gorm:"not null;default:''" json:"callId,omitempty"`
	Tool         string             `gorm:"not null;default:''" json:"tool,omitempty"`
	Server       string             `gorm:"not null;default:''" json:"server,omitempty"`
	ResponseID   string             `gorm:"not null;default:''" json:"responseId,omitempty"`
	Error        string             `gorm:"not null;default:''" json:"error,omitempty"`
	StartedAt    time.Time          `gorm:"not null" json:"startedAt"`
	CompletedAt  *time.Time         `json:"completedAt,omitempty"`
	Approval     *Approval          `gorm:"serializer:json" json:"approval,omitempty"`
	Await        *tools.Await       `gorm:"serializer:json" json:"await,omitempty"`
	Annotations  *tools.Annotations `gorm:"serializer:json" json:"annotations,omitempty"`
	Usage        *Usage             `gorm:"serializer:json" json:"usage,omitempty"`
	ContextStats *ContextStats      `gorm:"serializer:json" json:"contextStats,omitempty"`
	Memory       []MemoryReference  `gorm:"serializer:json" json:"memory,omitempty"`
	Attachments  []Attachment       `gorm:"serializer:json" json:"attachments,omitempty"`
	// The columns that can be large come last, so reading the ones above does not walk their overflow pages.
	Text              string          `gorm:"not null;default:''" json:"text,omitempty"`
	Arguments         string          `gorm:"not null;default:''" json:"arguments,omitempty"`
	PreparedExecution *tools.Prepared `gorm:"serializer:json" json:"preparedExecution,omitempty"`
	Result            *ToolResult     `gorm:"serializer:json" json:"result,omitempty"`
	// Native is the model provider's own record of the response this item belongs to, kept on the item that
	// carries the response's usage and replayed as is to the same kind of provider.
	Native []byte `json:"-"`
}

func (ItemRow) TableName() string { return "items" }

type ItemType string

const (
	ItemUserMessage       ItemType = "userMessage"
	ItemAgentMessage      ItemType = "agentMessage"
	ItemReasoning         ItemType = "reasoning"
	ItemToolCall          ItemType = "toolCall"
	ItemContextCompaction ItemType = "contextCompaction"
	ItemMemoryContext     ItemType = "memoryContext"
	ItemError             ItemType = "error"
	// ItemAgentReport is how a subagent ended, come back to the turn that launched it: its final answer as the result,
	// or why it has none. Text is the subagent's task, as its conversation is titled; CallID is the launching call's.
	ItemAgentReport ItemType = "agentReport"
)

var ItemTypeValues = []ItemType{ItemUserMessage, ItemAgentMessage, ItemReasoning, ItemToolCall, ItemContextCompaction, ItemMemoryContext, ItemError, ItemAgentReport}

func (t ItemType) Value() (driver.Value, error) { return enumValue(t, ItemTypeValues) }

type ItemStatus string

const (
	ItemPending    ItemStatus = "pending"
	ItemInProgress ItemStatus = "inProgress"
	ItemCompleted  ItemStatus = "completed"
	ItemFailed     ItemStatus = "failed"
	ItemDeclined   ItemStatus = "declined"
)

var ItemStatusValues = []ItemStatus{ItemPending, ItemInProgress, ItemCompleted, ItemFailed, ItemDeclined}

func (s ItemStatus) Value() (driver.Value, error) { return enumValue(s, ItemStatusValues) }

type ContextStats struct {
	Model            string       `json:"model,omitempty"`
	BudgetTokens     int          `json:"budgetTokens"`
	CompactAtTokens  int          `json:"compactAtTokens"`
	SystemTokens     int          `json:"systemTokens"`
	MessagesTokens   int          `json:"messagesTokens"`
	MemoryTokens     int          `json:"memoryTokens,omitempty"`
	GlobalMemory     *MemoryStats `json:"globalMemory,omitempty"`
	LocalMemory      *MemoryStats `json:"localMemory,omitempty"`
	ToolsTokens      int          `json:"toolsTokens"`
	EstimatedTokens  int          `json:"estimatedTokens"`
	CalibratedTokens int          `json:"calibratedTokens,omitempty"`
}

type MemoryStats struct {
	Tokens int      `json:"tokens"`
	IDs    []string `json:"ids"`
}

type MemoryReference struct {
	MemoryNote
	Scope       MemoryScope `json:"scope"`
	WorkspaceID string      `json:"workspaceId,omitempty"`
}

type MemoryNote struct {
	ID    string `json:"id"`
	Topic string `json:"topic,omitempty"`
	Text  string `json:"text"`
}

func (e MemoryRow) Reference() MemoryReference {
	return MemoryReference{MemoryNote: MemoryNote{ID: e.ID, Topic: e.Topic, Text: e.Text}, Scope: e.Scope, WorkspaceID: e.WorkspaceID}
}

type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
)

var AttachmentKindValues = []AttachmentKind{AttachmentImage, AttachmentFile}

type Attachment struct {
	ID       string         `json:"id"`
	Kind     AttachmentKind `json:"kind"`
	Name     string         `json:"name"`
	MimeType string         `json:"mimeType"`
	Size     int64          `json:"size"`
	File     string         `json:"file"`
	Width    int            `json:"width,omitempty"`
	Height   int            `json:"height,omitempty"`
	Tokens   int            `json:"tokens"`
}

type Approval struct {
	ID          string     `json:"id"`
	RequestedAt time.Time  `json:"requestedAt"`
	Reason      string     `json:"reason,omitempty"`
	Rule        string     `json:"rule,omitempty"`
	Content     string     `json:"content,omitempty"`
	Suggested   []string   `json:"suggestedRules,omitempty"`
	Decision    Decision   `json:"decision,omitempty"`
	DecidedBy   string     `json:"decidedBy,omitempty"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
}

type Decision string

const (
	DecisionAccept           Decision = "accept"
	DecisionAcceptForSession Decision = "acceptForSession"
	DecisionAcceptAlways     Decision = "acceptAlways"
	DecisionDecline          Decision = "decline"
)

var DecisionValues = []Decision{DecisionAccept, DecisionAcceptForSession, DecisionAcceptAlways, DecisionDecline}

func (a Approval) AvailableDecisions() []Decision {
	if a.Rule != "" {
		return []Decision{DecisionAccept, DecisionDecline}
	}
	if len(a.Suggested) == 0 {
		return []Decision{DecisionAccept, DecisionAcceptForSession, DecisionDecline}
	}
	return slices.Clone(DecisionValues)
}

type ToolResult struct {
	Output    json.RawMessage         `json:"output,omitempty"`
	Artifacts []tools.ContextArtifact `json:"artifacts,omitempty"`
}

func NewUserMessage(id, turnID, text string, attachments []Attachment, at time.Time) ItemRow {
	return ItemRow{ID: id, TurnID: turnID, Type: ItemUserMessage, Status: ItemCompleted, Text: text, Attachments: attachments, StartedAt: at, CompletedAt: &at}
}

func (d *DB) HasImages(threadID string) (bool, error) {
	var n int64
	err := d.Tx(func(tx *DB) error {
		from, err := tx.latestCompaction(threadID, math.MaxInt32)
		if err != nil {
			return err
		}
		return tx.gorm.Model(&ItemRow{}).Where(`thread_id = ? AND type = 'userMessage' AND (turn_seq, seq) > (?, ?)
			AND EXISTS (SELECT 1 FROM json_each(items.attachments) WHERE json_extract(json_each.value, '$.kind') = 'image')`, threadID, from.TurnSeq, from.Seq).Count(&n).Error
	})
	return n > 0, err
}

func (d *DB) Attachment(user, threadID, id string) (Attachment, error) {
	var rows []ItemRow
	err := d.gorm.Where("thread_id = ? AND "+ofUser+" AND type = 'userMessage' AND EXISTS (SELECT 1 FROM json_each(items.attachments) WHERE json_extract(json_each.value, '$.id') = ?)", threadID, user, id).Find(&rows).Error
	if err != nil {
		return Attachment{}, err
	}
	for _, it := range rows {
		for _, a := range it.Attachments {
			if a.ID == id {
				return a, nil
			}
		}
	}
	return Attachment{}, ErrNotFound
}

func (it ItemRow) IsCall() bool {
	return it.Type == ItemToolCall
}

func (it ItemRow) Awaits() bool {
	return it.Type == ItemToolCall && it.Status == ItemInProgress && it.Await != nil
}

func (it ItemRow) WaitsOnUser() bool {
	return it.WaitsForApproval() || (it.Awaits() && it.Await.By == tools.AwaitUser)
}

func (it ItemRow) WaitsForApproval() bool {
	return it.Type == ItemToolCall && it.Status == ItemPending && it.Approval != nil && it.Approval.Decision == ""
}

func (it ItemRow) LiveResult() (tools.Result, error) {
	if it.Result == nil {
		return tools.Result{}, fmt.Errorf("item %s has no tool result", it.ID)
	}
	o := it.Result
	r := tools.Result{Tool: it.Tool, Artifacts: o.Artifacts}
	if len(o.Output) > 0 {
		dec := json.NewDecoder(bytes.NewReader(o.Output))
		dec.UseNumber()
		if err := dec.Decode(&r.Output); err != nil {
			return tools.Result{}, fmt.Errorf("decode tool output of item %s: %w", it.ID, err)
		}
	}
	if it.Error != "" {
		r.Err = errors.New(it.Error)
	}
	return r, nil
}

func ResultOf(result tools.Result) (*ToolResult, string, error) {
	out := &ToolResult{Artifacts: result.Artifacts}
	if result.Output != nil {
		raw, err := json.Marshal(result.Output)
		if err != nil {
			return nil, "", fmt.Errorf("encode tool output: %w", err)
		}
		out.Output = raw
	}
	if result.Err == nil {
		return out, "", nil
	}
	return out, result.Err.Error(), nil
}

func (d *DB) Item(id string) (ItemRow, error) {
	var row ItemRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

func (d *DB) nextItemSeq(threadID string) (int, error) {
	var last int
	err := d.gorm.Model(&ItemRow{}).Where("thread_id = ?", threadID).Select("COALESCE(MAX(seq), 0)").Scan(&last).Error
	return last + 1, err
}

func (d *DB) saveItem(row ItemRow) error {
	if row.Await != nil && !slices.Contains(tools.AwaitByValues, row.Await.By) {
		return fmt.Errorf("item %s: awaits %q, which is not one of %q", row.ID, row.Await.By, tools.AwaitByValues)
	}
	row.StartedAt, row.CompletedAt = row.StartedAt.UTC(), nullTime(row.CompletedAt)
	res := d.gorm.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "text", "response_id", "context_stats", "usage", "native", "memory",
			"call_id", "tool", "arguments", "server", "annotations", "approval", "prepared_execution", "await", "result", "error", "completed_at"}),
		Where: where(`items.thread_id = excluded.thread_id AND items.turn_id = excluded.turn_id
			AND items.seq = excluded.seq AND items.type = excluded.type`),
	}).Create(&row)
	if err := checkOne(res); err != nil {
		return fmt.Errorf("item %s: %w", row.ID, err)
	}
	return nil
}

func (d *DB) RecordItem(threadID string, item ItemRow) (ItemRow, bool, error) {
	var stored ItemRow
	var isNew bool
	err := d.Tx(func(tx *DB) error {
		active, err := tx.turnOf(threadID, item)
		if err != nil {
			return err
		}
		stored, isNew, err = tx.recordIn(threadID, active, item)
		return err
	})
	if err != nil {
		return ItemRow{}, false, fmt.Errorf("storage: record item: %w", err)
	}
	return stored, isNew, nil
}

// AnswerRequest records the user's answer to a request, as RecordItem does, if the turn still waits on the user for
// it; otherwise it returns ErrNoSuchRequest. What the caller read may be stale: a message may have declined the request,
// or another answer taken it.
func (d *DB) AnswerRequest(threadID string, answer ItemRow) (ItemRow, error) {
	var stored ItemRow
	err := d.Tx(func(tx *DB) error {
		active, err := tx.turnOf(threadID, answer)
		if errors.Is(err, errNotCurrent) {
			return ErrNoSuchRequest
		}
		if err != nil {
			return err
		}
		if active.Status != TurnWaitingOnApproval && active.Status != TurnWaitingOnUserInput {
			return ErrNoSuchRequest
		}
		prev, err := tx.Item(answer.ID)
		if err != nil {
			return err
		}
		if !prev.WaitsOnUser() {
			return ErrNoSuchRequest
		}
		stored, _, err = tx.recordIn(threadID, active, answer)
		return err
	})
	if err != nil {
		return ItemRow{}, fmt.Errorf("storage: answer request: %w", err)
	}
	return stored, nil
}

var errNotCurrent = errors.New("not the thread's current turn")

// turnOf is the thread's current turn, which the item must belong to.
func (d *DB) turnOf(threadID string, item ItemRow) (TurnRow, error) {
	active, err := d.ActiveTurn(threadID)
	if errors.Is(err, ErrNotFound) || (err == nil && active.ID != item.TurnID) {
		return TurnRow{}, fmt.Errorf("item %s belongs to turn %s, which is %w", item.ID, item.TurnID, errNotCurrent)
	}
	return active, err
}

func (d *DB) recordIn(threadID string, active TurnRow, item ItemRow) (ItemRow, bool, error) {
	stored, isNew, err := d.saveItemOf(threadID, item)
	if err != nil || item.Type != ItemToolCall {
		return stored, isNew, err
	}
	// A call answered or approved may be the last one its waiting turn awaited.
	switch active.Status {
	case TurnWaitingOnApproval, TurnWaitingOnUserInput:
		_, err = d.settleStatus(active.ID, active.Status)
	}
	return stored, isNew, err
}

// Steer writes a user message into the current turn. A turn that waits on the user takes the message in place of what
// it waits for, and is ready again, so its next run reads the message: the calls of the turn that are still to run,
// the one awaiting approval or an answer among them, are declined. A turn that waits on its subagents is ready again:
// they go on, and report to it as they end. It returns the declined calls as closed.
func (d *DB) Steer(threadID string, message ItemRow) (ItemRow, []ItemRow, error) {
	var stored ItemRow
	var closed []ItemRow
	err := d.Tx(func(tx *DB) error {
		active, err := tx.ActiveTurn(threadID)
		if errors.Is(err, ErrNotFound) || (err == nil && active.ID != message.TurnID) {
			return fmt.Errorf("message %s belongs to turn %s, which is not in progress", message.ID, message.TurnID)
		}
		if err != nil {
			return err
		}
		if message.Type != ItemUserMessage {
			return fmt.Errorf("item %s is not a user message", message.ID)
		}
		if stored, _, err = tx.saveItemOf(threadID, message); err != nil {
			return err
		}
		switch active.Status {
		case TurnWaitingOnAgent:
			return checkOne(tx.gorm.Model(&TurnRow{}).Where("id = ? AND status = ?", active.ID, TurnWaitingOnAgent).Update("status", TurnReady))
		case TurnWaitingOnApproval, TurnWaitingOnUserInput:
		default:
			return nil
		}
		var open []ItemRow
		if err := tx.gorm.Where("turn_id = ? AND "+openCalls, active.ID).Order("seq").Find(&open).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, it := range open {
			switch {
			case it.WaitsForApproval():
				it.Error = "the user sent a message instead of approving this call, so it did not run"
			case it.WaitsOnUser():
				it.Error = "the user sent a message instead of answering"
			case it.Status == ItemPending:
				it.Error = "skipped: the user sent a message before this call ran"
			default:
				continue
			}
			it.Status, it.CompletedAt = ItemDeclined, &now
			if it, _, err = tx.saveItemOf(threadID, it); err != nil {
				return err
			}
			closed = append(closed, it)
		}
		_, err = tx.settleStatus(active.ID, active.Status)
		return err
	})
	if err != nil {
		return ItemRow{}, nil, fmt.Errorf("storage: steer: %w", err)
	}
	return stored, closed, nil
}

func (d *DB) RecoverItems(threadID string) ([]ItemRow, error) {
	var closed []ItemRow
	err := d.Tx(func(tx *DB) error {
		active, err := tx.ActiveTurn(threadID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var running []ItemRow
		if err := tx.gorm.Where("turn_id = ? AND status = ?", active.ID, ItemInProgress).Order("seq").Find(&running).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, it := range running {
			switch {
			case it.Type == ItemReasoning || it.Type == ItemAgentMessage || it.Type == ItemContextCompaction:
				it.Error = "the app stopped before this finished"
			case it.IsCall() && it.Await == nil:
				it.Error = "the app stopped while this call was running; it may or may not have taken effect, so check before repeating it"
			default:
				continue
			}
			it.Status, it.CompletedAt = ItemFailed, &now
			if _, _, err := tx.saveItemOf(threadID, it); err != nil {
				return err
			}
			closed = append(closed, it)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("storage: recover thread %s: %w", threadID, err)
	}
	return closed, nil
}

func (it ItemRow) ended() bool {
	return it.Status == ItemCompleted || it.Status == ItemFailed || it.Status == ItemDeclined
}

// saveItemOf writes the item and returns it as stored, saying whether it is new; it reads only the item as it was,
// which may not change its thread, type or turn, nor a prepared execution once it has one, nor end and start again.
func (d *DB) saveItemOf(threadID string, it ItemRow) (ItemRow, bool, error) {
	if it.ID == "" || it.TurnID == "" || it.Type == "" || it.Status == "" {
		return ItemRow{}, false, fmt.Errorf("item needs an id, turn, type, and status")
	}
	prev, err := d.Item(it.ID)
	isNew := errors.Is(err, ErrNotFound)
	switch {
	case isNew:
		if it.Seq, err = d.nextItemSeq(threadID); err != nil {
			return ItemRow{}, false, err
		}
		var turnSeq []int
		if err := d.gorm.Model(&TurnRow{}).Where("id = ? AND thread_id = ?", it.TurnID, threadID).Pluck("seq", &turnSeq).Error; err != nil {
			return ItemRow{}, false, err
		}
		if len(turnSeq) == 0 {
			return ItemRow{}, false, fmt.Errorf("item %s belongs to turn %s, which thread %s does not have", it.ID, it.TurnID, threadID)
		}
		it.TurnSeq = turnSeq[0]
	case err != nil:
		return ItemRow{}, false, err
	default:
		if prev.ThreadID != threadID || prev.Type != it.Type || prev.TurnID != it.TurnID {
			return ItemRow{}, false, fmt.Errorf("item %s cannot change its thread, type, or turn", it.ID)
		}
		if prev.PreparedExecution != nil && (!reflect.DeepEqual(prev.PreparedExecution, it.PreparedExecution) || prev.Arguments != it.Arguments || prev.Tool != it.Tool || prev.Server != it.Server) {
			return ItemRow{}, false, fmt.Errorf("item %s cannot change its prepared execution or tool arguments", it.ID)
		}
		// An item that ended stays ended: a copy read before it did cannot open it again.
		if prev.ended() && !it.ended() {
			return ItemRow{}, false, fmt.Errorf("item %s ended %s and cannot be %s again", it.ID, prev.Status, it.Status)
		}
		it.Seq, it.TurnSeq = prev.Seq, prev.TurnSeq
	}
	it.ThreadID = threadID
	it.StartedAt, it.CompletedAt = it.StartedAt.UTC(), nullTime(it.CompletedAt)
	if err := d.saveItem(it); err != nil {
		return ItemRow{}, false, err
	}
	return it, isNew, nil
}
