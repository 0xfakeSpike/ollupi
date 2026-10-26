package repo

import (
	"encoding/json"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"gorm.io/gorm"
)

type MemoryReport struct {
	Model                llm.Model `json:"model"`
	EstimatedInputTokens int       `json:"estimatedInputTokens"`
	InputTokens          int64     `json:"inputTokens"`
	OutputTokens         int64     `json:"outputTokens"`
	UsageReported        bool      `json:"usageReported"`
}

type MemoryCursor struct {
	At  *time.Time
	ID  string `gorm:"not null;default:''"`
	Seq int    `gorm:"not null;default:0"`
}

func (c MemoryCursor) at() time.Time {
	if c.At == nil {
		return time.Time{}
	}
	return c.At.UTC()
}

type MemoryStateRow struct {
	ID          int           `gorm:"primaryKey;autoIncrement:false;check:memory_state_one,id = 1" json:"-"`
	Cursor      MemoryCursor  `gorm:"embedded;embeddedPrefix:cursor_" json:"-"`
	Attempts    int           `gorm:"not null;default:0" json:"attempts,omitempty"`
	NextAttempt *time.Time    `json:"nextAttempt,omitempty"`
	Skipped     int64         `gorm:"not null;default:0" json:"-"`
	LastSuccess *time.Time    `json:"lastSuccess,omitempty"`
	LastError   string        `gorm:"not null;default:''" json:"lastError,omitempty"`
	LastReport  *MemoryReport `gorm:"serializer:json;check:memory_state_report,last_report IS NULL OR json_valid(last_report)" json:"lastReport,omitempty"`
}

func (MemoryStateRow) TableName() string { return "memory_state" }

type MemoryStatus struct {
	GenerationEnabled bool  `json:"generationEnabled"`
	UseEnabled        bool  `json:"useEnabled"`
	Pending           int64 `json:"pending"`
	Failed            int64 `json:"failed"`
	Active            int64 `json:"active"`
	MemoryStateRow
}

const endedTurn = "status IN ('completed', 'failed', 'interrupted') AND completed_at IS NOT NULL"

func (d *DB) MemoryState() (MemoryStateRow, error) {
	var row MemoryStateRow
	return row, d.gorm.Where("id = 1").Take(&row).Error
}

func (d *DB) MemoryStatus() (MemoryStatus, error) {
	var s MemoryStatus
	if err := d.gorm.Where("id = 1").Take(&s.MemoryStateRow).Error; err != nil {
		return s, err
	}
	c := s.Cursor
	if err := d.gorm.Model(&TurnRow{}).Where(endedTurn+" AND (completed_at, id) > (?, ?)", c.at(), c.ID).Count(&s.Pending).Error; err != nil {
		return s, err
	}
	if err := d.gorm.Model(&MemoryRow{}).Where("status = ?", MemoryActive).Count(&s.Active).Error; err != nil {
		return s, err
	}
	s.Failed = s.Skipped
	return s, nil
}

func (d *DB) NextEndedTurn(c MemoryCursor) (TurnRow, error) {
	var row TurnRow
	err := d.gorm.Where(endedTurn+" AND (completed_at, id) > (?, ?)", c.at(), c.ID).Order("completed_at, id").Take(&row).Error
	return row, notFound(err)
}

func (d *DB) TurnItemsAfter(turnID string, seq int, types []ItemType, statuses []ItemStatus, limit int) ([]ItemRow, error) {
	var rows []ItemRow
	err := d.gorm.Where("turn_id = ? AND seq > ? AND type IN ? AND status IN ?", turnID, seq, types, statuses).
		Order("seq").Limit(limit).Find(&rows).Error
	return rows, err
}

func (d *DB) SetMemoryError(message string) error {
	return d.gorm.Model(&MemoryStateRow{}).Where("id = 1").Update("last_error", message).Error
}

func cursorColumns(c MemoryCursor) map[string]any {
	return map[string]any{"cursor_at": nullTime(c.At), "cursor_id": c.ID, "cursor_seq": c.Seq}
}

func (d *DB) AdvanceMemory(c MemoryCursor, now time.Time, report MemoryReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	cols := cursorColumns(c)
	cols["attempts"], cols["next_attempt"] = 0, nil
	cols["last_success"] = now.UTC()
	cols["last_error"] = gorm.Expr("CASE WHEN skipped > 0 THEN last_error ELSE '' END")
	cols["last_report"] = string(raw)
	return checkOne(d.gorm.Model(&MemoryStateRow{}).Where("id = 1").Updates(cols))
}

const maxMemoryAttempts = 10

func (d *DB) DeferMemory(skipTo MemoryCursor, entries int, message string, now time.Time) (skipped bool, err error) {
	err = d.Tx(func(tx *DB) error {
		s, err := tx.MemoryState()
		if err != nil {
			return err
		}
		cols := map[string]any{"last_error": message}
		if s.Attempts+1 >= maxMemoryAttempts {
			skipped = true
			for k, v := range cursorColumns(skipTo) {
				cols[k] = v
			}
			cols["attempts"], cols["next_attempt"] = 0, nil
			cols["skipped"] = s.Skipped + int64(entries)
		} else {
			cols["attempts"] = s.Attempts + 1
			cols["next_attempt"] = now.UTC().Add(time.Duration(min(60, 1<<min(s.Attempts, 6))) * time.Minute)
		}
		return checkOne(tx.gorm.Model(&MemoryStateRow{}).Where("id = 1").Updates(cols))
	})
	return skipped, err
}
