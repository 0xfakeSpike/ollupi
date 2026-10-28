package repo

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/tools"
	"gorm.io/gorm/clause"
)

type ItemRow struct {
	ID                string             `gorm:"primaryKey" json:"id"`
	ThreadID          string             `gorm:"not null;uniqueIndex:items_thread_seq,priority:1" json:"-"`
	Thread            *ThreadRow         `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	TurnID            string             `gorm:"not null;index" json:"turnId"`
	Turn              *TurnRow           `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Seq               int                `gorm:"not null;uniqueIndex:items_thread_seq,priority:2" json:"seq"`
	Type              ItemType           `gorm:"not null" json:"type"`
	Status            ItemStatus         `gorm:"not null" json:"status"`
	Text              string             `gorm:"not null;default:''" json:"text,omitempty"`
	ResponseID        string             `gorm:"not null;default:''" json:"responseId,omitempty"`
	ContextStats      *ContextStats      `gorm:"serializer:json" json:"contextStats,omitempty"`
	Usage             *Usage             `gorm:"serializer:json" json:"usage,omitempty"`
	Memory            []MemoryReference  `gorm:"serializer:json" json:"memory,omitempty"`
	CallID            string             `gorm:"not null;default:''" json:"callId,omitempty"`
	Tool              string             `gorm:"not null;default:''" json:"tool,omitempty"`
	Arguments         string             `gorm:"not null;default:''" json:"arguments,omitempty"`
	Server            string             `gorm:"not null;default:''" json:"server,omitempty"`
	Annotations       *tools.Annotations `gorm:"serializer:json" json:"annotations,omitempty"`
	Approval          *Approval          `gorm:"serializer:json" json:"approval,omitempty"`
	PreparedExecution *tools.Prepared    `gorm:"serializer:json" json:"preparedExecution,omitempty"`
	Await             *tools.Await       `gorm:"serializer:json" json:"await,omitempty"`
	Result            *ToolResult        `gorm:"serializer:json" json:"result,omitempty"`
	Error             string             `gorm:"not null;default:''" json:"error,omitempty"`
	StartedAt         time.Time          `gorm:"not null" json:"startedAt"`
	CompletedAt       *time.Time         `json:"completedAt,omitempty"`
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
	ItemToolProgress      ItemType = "toolProgress"
	ItemError             ItemType = "error"
)

var ItemTypeValues = []ItemType{ItemUserMessage, ItemAgentMessage, ItemReasoning, ItemToolCall, ItemContextCompaction, ItemMemoryContext, ItemToolProgress, ItemError}

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

func NewUserMessage(id, turnID, text string, at time.Time) ItemRow {
	return ItemRow{ID: id, TurnID: turnID, Type: ItemUserMessage, Status: ItemCompleted, Text: text, StartedAt: at, CompletedAt: &at}
}

func (it ItemRow) IsCall() bool {
	return it.Type == ItemToolCall
}

func (it ItemRow) Awaits() bool {
	return it.Type == ItemToolCall && it.Status == ItemInProgress && it.Await != nil
}

func (it ItemRow) WaitsOnAgent() bool {
	return it.Awaits() && it.Await.By == tools.AwaitAgent
}

func (it ItemRow) IsPending() bool {
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

func (d *DB) NextItemSeq(threadID string) (int, error) {
	var last int
	err := d.gorm.Model(&ItemRow{}).Where("thread_id = ?", threadID).Select("COALESCE(MAX(seq), 0)").Scan(&last).Error
	return last + 1, err
}

func (d *DB) SaveItem(row ItemRow) error {
	if row.Await != nil && !slices.Contains(tools.AwaitByValues, row.Await.By) {
		return fmt.Errorf("item %s: awaits %q, which is not one of %q", row.ID, row.Await.By, tools.AwaitByValues)
	}
	row.StartedAt, row.CompletedAt = row.StartedAt.UTC(), nullTime(row.CompletedAt)
	res := d.gorm.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "text", "response_id", "context_stats", "usage", "memory",
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
		active, err := tx.ActiveTurn(threadID)
		if IsNotFound(err) || (err == nil && active.ID != item.TurnID) {
			return fmt.Errorf("item %s belongs to turn %s, which is not in progress", item.ID, item.TurnID)
		}
		if err != nil {
			return err
		}
		_, err = tx.Item(item.ID)
		isNew = IsNotFound(err)
		if err != nil && !isNew {
			return err
		}
		if err := tx.saveItemOf(threadID, item); err != nil {
			return err
		}
		stored, err = tx.Item(item.ID)
		return err
	})
	if err != nil {
		return ItemRow{}, false, fmt.Errorf("storage: record item: %w", err)
	}
	return stored, isNew, nil
}

func (d *DB) RecoverItems(threadID string) ([]ItemRow, error) {
	var closed []ItemRow
	err := d.Tx(func(tx *DB) error {
		v, err := tx.Thread(threadID)
		if err != nil {
			return err
		}
		active, ok := v.ActiveTurn()
		if !ok {
			return nil
		}
		now := time.Now().UTC()
		for _, it := range v.Items {
			if it.TurnID != active.ID || it.Status != ItemInProgress {
				continue
			}
			switch {
			case it.Type == ItemReasoning || it.Type == ItemAgentMessage || it.Type == ItemContextCompaction:
				it.Error = "the app stopped before this finished"
			case it.IsCall() && it.Await == nil:
				it.Error = "the app stopped while this call was running; it may or may not have taken effect, so check before repeating it"
			default:
				continue
			}
			it.Status, it.CompletedAt = ItemFailed, &now
			if err := tx.saveItemOf(threadID, it); err != nil {
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

func (d *DB) saveItemOf(threadID string, it ItemRow) error {
	if it.ID == "" || it.TurnID == "" || it.Type == "" || it.Status == "" {
		return fmt.Errorf("item needs an id, turn, type, and status")
	}
	prev, err := d.Item(it.ID)
	switch {
	case IsNotFound(err):
		if it.Seq, err = d.NextItemSeq(threadID); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if prev.ThreadID != threadID || prev.Type != it.Type || prev.TurnID != it.TurnID {
			return fmt.Errorf("item %s cannot change its thread, type, or turn", it.ID)
		}
		if prev.PreparedExecution != nil && (!reflect.DeepEqual(prev.PreparedExecution, it.PreparedExecution) || prev.Arguments != it.Arguments || prev.Tool != it.Tool || prev.Server != it.Server) {
			return fmt.Errorf("item %s cannot change its prepared execution or tool arguments", it.ID)
		}
		it.Seq = prev.Seq
	}
	it.ThreadID = threadID
	return d.SaveItem(it)
}
