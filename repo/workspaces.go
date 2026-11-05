package repo

import (
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

type WorkspaceRow struct {
	ID         string             `gorm:"primaryKey" json:"id"`
	UserID     string             `gorm:"not null;default:'';uniqueIndex:workspaces_user_root,priority:1" json:"-"`
	User       *UserRow           `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	Name       string             `gorm:"not null;check:workspaces_name,length(trim(name)) > 0" json:"name"`
	RootPath   string             `gorm:"not null;uniqueIndex:workspaces_user_root,priority:2" json:"rootPath"`
	Settings   PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:workspaces_settings,json_valid(settings)" json:"settings"`
	CreatedAt  time.Time          `gorm:"not null;autoCreateTime:false" json:"createdAt"`
	UpdatedAt  time.Time          `gorm:"not null;autoUpdateTime:false;index:workspaces_archived_updated,priority:2,sort:desc" json:"updatedAt"`
	ArchivedAt *time.Time         `gorm:"index:workspaces_archived_updated,priority:1" json:"archivedAt,omitempty"`
}

func (WorkspaceRow) TableName() string { return "workspaces" }

func (d *DB) Workspaces(user string) ([]WorkspaceRow, error) {
	var rows []WorkspaceRow
	err := d.gorm.Where("user_id = ?", user).Order("updated_at DESC, id").Find(&rows).Error
	return rows, err
}

func (d *DB) saveWorkspace(w WorkspaceRow) error {
	w.CreatedAt, w.UpdatedAt, w.ArchivedAt = w.CreatedAt.UTC(), w.UpdatedAt.UTC(), nullTime(w.ArchivedAt)
	res := d.gorm.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "settings", "updated_at", "archived_at"}),
		Where:     where("workspaces.user_id = excluded.user_id AND workspaces.root_path = excluded.root_path AND workspaces.created_at = excluded.created_at"),
	}).Create(&w)
	if err := checkOne(res); err != nil {
		return fmt.Errorf("workspace %s: %w", w.ID, err)
	}
	return nil
}

func (d *DB) UpdateWorkspace(user, id string, change func(WorkspaceRow) (WorkspaceRow, error)) (WorkspaceRow, error) {
	var out WorkspaceRow
	err := d.Tx(func(tx *DB) error {
		row, err := tx.WorkspaceOf(user, id)
		if err != nil {
			return err
		}
		next, err := change(row)
		if err != nil {
			return err
		}
		out = next
		return tx.saveWorkspace(next)
	})
	return out, err
}

func (d *DB) AddWorkspace(w WorkspaceRow, same func(WorkspaceRow) bool, restore func(WorkspaceRow) (WorkspaceRow, bool)) (WorkspaceRow, error) {
	var out WorkspaceRow
	err := d.Tx(func(tx *DB) error {
		var rows []WorkspaceRow
		if err := tx.gorm.Where("user_id = ?", w.UserID).Find(&rows).Error; err != nil {
			return err
		}
		for _, existing := range rows {
			if !same(existing) {
				continue
			}
			out = existing
			if restored, changed := restore(existing); changed {
				out = restored
				return tx.saveWorkspace(restored)
			}
			return nil
		}
		out = w
		return tx.saveWorkspace(w)
	})
	return out, err
}

func (d *DB) Workspace(id string) (WorkspaceRow, error) {
	var row WorkspaceRow
	return row, notFound(d.gorm.Where("id = ?", id).Take(&row).Error)
}

// WorkspaceOf reads a workspace for the user who owns it; to anyone else it does not exist.
func (d *DB) WorkspaceOf(user, id string) (WorkspaceRow, error) {
	var row WorkspaceRow
	return row, notFound(d.gorm.Where("id = ? AND user_id = ?", id, user).Take(&row).Error)
}
