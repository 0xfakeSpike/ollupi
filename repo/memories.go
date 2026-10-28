package repo

import (
	"database/sql/driver"
	"time"

	"gorm.io/gorm/clause"
)

type MemorySource struct {
	Table      string `json:"table"`
	ID         string `json:"id"`
	OccurredAt string `json:"occurredAt,omitempty"`
}

// MemoryRow is one memory. Its scope says where it applies: global ones
// to every conversation, general ones to conversations outside any
// workspace, workspace ones to the conversations of WorkspaceID.
type MemoryRow struct {
	ID          string          `gorm:"primaryKey" json:"id"`
	Scope       MemoryScope     `gorm:"not null;uniqueIndex:memories_scope_topic,priority:1;index:memories_scope_status,priority:1;check:memories_scope_workspace,(scope = 'workspace') = (workspace_id <> '')" json:"scope"`
	WorkspaceID string          `gorm:"not null;default:'';uniqueIndex:memories_scope_topic,priority:2;index:memories_scope_status,priority:2" json:"workspaceId,omitempty"`
	Topic       string          `gorm:"not null;uniqueIndex:memories_scope_topic,priority:3" json:"topic"`
	Kind        string          `gorm:"not null" json:"kind"`
	Text        string          `gorm:"not null" json:"text"`
	Status      MemoryRowStatus `gorm:"not null;index:memories_scope_status,priority:3" json:"status"`
	UserEdited  bool            `gorm:"not null" json:"userEdited,omitempty"`
	Sources     []MemorySource  `gorm:"not null;serializer:json;check:memories_sources,json_valid(sources)" json:"sources"`
	CreatedAt   time.Time       `gorm:"not null;autoCreateTime:false" json:"createdAt"`
	UpdatedAt   time.Time       `gorm:"not null;autoUpdateTime:false" json:"updatedAt"`
}

func (MemoryRow) TableName() string { return "memories" }

type MemoryRowStatus string

const (
	MemoryActive    MemoryRowStatus = "active"
	MemoryRetired   MemoryRowStatus = "retired"
	MemoryForgotten MemoryRowStatus = "forgotten"
)

var MemoryRowStatusValues = []MemoryRowStatus{MemoryActive, MemoryRetired, MemoryForgotten}

func (s MemoryRowStatus) Value() (driver.Value, error) { return enumValue(s, MemoryRowStatusValues) }

type MemoryScope string

const (
	MemoryGlobal    MemoryScope = "global"
	MemoryGeneral   MemoryScope = "general"
	MemoryWorkspace MemoryScope = "workspace"
)

var MemoryScopeValues = []MemoryScope{MemoryGlobal, MemoryGeneral, MemoryWorkspace}

func (s MemoryScope) Value() (driver.Value, error) { return enumValue(s, MemoryScopeValues) }

// Memories are the memories of scope, of workspaceID for the workspace
// scope, and with global also the global ones.
func (d *DB) Memories(scope MemoryScope, workspaceID string, global bool) ([]MemoryRow, error) {
	var rows []MemoryRow
	err := d.gorm.Where("(scope = ? AND workspace_id = ?) OR (? AND scope = ?)", scope, workspaceID, global, MemoryGlobal).Order("updated_at DESC, id").Find(&rows).Error
	return rows, err
}

func (d *DB) Memory(id string) (MemoryRow, error) {
	var row MemoryRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

func (d *DB) SaveMemory(m MemoryRow) error {
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	if m.Sources == nil {
		m.Sources = []MemorySource{}
	}
	return d.gorm.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"scope", "workspace_id", "topic", "kind", "text", "status", "sources", "updated_at", "user_edited"}),
	}).Create(&m).Error
}
