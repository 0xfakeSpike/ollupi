package repo

import (
	"fmt"
	"time"
)

// EnvironmentKind says where a conversation's commands run: on this machine, whose folders the workspaces name, or on
// a cloud sandbox of the user's, which only the host knows how to reach.
type EnvironmentKind string

const (
	// EnvironmentLocal is this machine, the one the user's workspaces are folders of.
	EnvironmentLocal EnvironmentKind = "local"
	// EnvironmentMachine is a cloud sandbox of the user's, which the host reaches through a sandbox.Machine.
	EnvironmentMachine EnvironmentKind = "machine"
)

var EnvironmentKindValues = []EnvironmentKind{EnvironmentLocal, EnvironmentMachine}

// LocalEnvironmentName is what a user's environment on this machine is called until they rename it.
const LocalEnvironmentName = "This computer"

// EnvironmentRow is where a conversation runs: a Box the host makes, named by the user, chosen when the conversation
// starts. A user always has one of kind local, which cannot be removed: their workspaces are folders in it.
type EnvironmentRow struct {
	ID        string          `gorm:"primaryKey" json:"id"`
	UserID    string          `gorm:"not null;default:'';uniqueIndex:environments_user_name,priority:1" json:"-"`
	User      *UserRow        `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	Name      string          `gorm:"not null;uniqueIndex:environments_user_name,priority:2;check:environments_name,length(trim(name)) > 0" json:"name"`
	Kind      EnvironmentKind `gorm:"not null;check:environments_kind,kind in ('local','machine')" json:"kind"`
	CreatedAt time.Time       `gorm:"not null;autoCreateTime:false" json:"createdAt"`
	UpdatedAt time.Time       `gorm:"not null;autoUpdateTime:false" json:"updatedAt"`
}

func (EnvironmentRow) TableName() string { return "environments" }

// Environments lists the user's environments, the one they had first first: the default for a new conversation.
func (d *DB) Environments(user string) ([]EnvironmentRow, error) {
	var rows []EnvironmentRow
	err := d.gorm.Where("user_id = ?", user).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// EnvironmentOf reads an environment for the user who owns it; to anyone else it does not exist.
func (d *DB) EnvironmentOf(user, id string) (EnvironmentRow, error) {
	var row EnvironmentRow
	return row, notFound(d.gorm.Where("id = ? AND user_id = ?", id, user).Take(&row).Error)
}

// LocalEnvironment is the user's environment on this machine: their oldest one of kind local.
func (d *DB) LocalEnvironment(user string) (EnvironmentRow, error) {
	var row EnvironmentRow
	return row, notFound(d.gorm.Where("user_id = ? AND kind = ?", user, EnvironmentLocal).Order("created_at, id").Take(&row).Error)
}

// AddEnvironment makes the environment, unless the user already has one that taken calls the same, which it returns
// instead: adding a name twice is not a second environment.
func (d *DB) AddEnvironment(row EnvironmentRow, taken func(EnvironmentRow) bool) (EnvironmentRow, error) {
	var out EnvironmentRow
	err := d.Tx(func(tx *DB) error {
		var rows []EnvironmentRow
		if err := tx.gorm.Where("user_id = ?", row.UserID).Find(&rows).Error; err != nil {
			return err
		}
		for _, existing := range rows {
			if taken(existing) {
				out = existing
				return nil
			}
		}
		row.CreatedAt, row.UpdatedAt = row.CreatedAt.UTC(), row.UpdatedAt.UTC()
		if err := tx.gorm.Create(&row).Error; err != nil {
			return fmt.Errorf("environment %s: %w", row.Name, err)
		}
		out = row
		return nil
	})
	return out, err
}

// RenameEnvironment gives the environment another name; the environments a conversation can choose are its user's,
// and their names are the user's to tell apart.
func (d *DB) RenameEnvironment(user, id, name string, at time.Time) (EnvironmentRow, error) {
	var out EnvironmentRow
	err := d.Tx(func(tx *DB) error {
		row, err := tx.EnvironmentOf(user, id)
		if err != nil {
			return err
		}
		res := tx.gorm.Model(&EnvironmentRow{}).Where("id = ? AND user_id = ?", id, user).
			UpdateColumns(map[string]any{"name": name, "updated_at": at.UTC()})
		if err := checkOne(res); err != nil {
			return fmt.Errorf("environment %s: %w", id, err)
		}
		row.Name, row.UpdatedAt = name, at.UTC()
		out = row
		return nil
	})
	return out, err
}

// DeleteEnvironment removes an environment of kind machine. A conversation that runs in it keeps it alive: the
// threads foreign key refuses, and EnvironmentInUse lets the caller say so before it tries.
func (d *DB) DeleteEnvironment(user, id string) (EnvironmentRow, error) {
	var out EnvironmentRow
	err := d.Tx(func(tx *DB) error {
		row, err := tx.EnvironmentOf(user, id)
		if err != nil {
			return err
		}
		if err := checkOne(tx.gorm.Where("id = ? AND user_id = ?", id, user).Delete(&EnvironmentRow{})); err != nil {
			return fmt.Errorf("environment %s: %w", row.Name, err)
		}
		out = row
		return nil
	})
	return out, err
}

// EnvironmentInUse says whether a conversation runs in the environment.
func (d *DB) EnvironmentInUse(id string) (bool, error) {
	var n int64
	if err := d.gorm.Model(&ThreadRow{}).Where("environment_id = ?", id).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
