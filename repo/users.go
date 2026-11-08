package repo

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DefaultUser owns what a database held before it had users, and everything the gateway's one token reaches.
const DefaultUser = "default"

// UserRow is a user on this shard. Who a caller is gets settled before ollupi: a row only says the user exists here.
type UserRow struct {
	ID        string    `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime:false" json:"createdAt"`
}

func (UserRow) TableName() string { return "users" }

// EnsureUser makes the user with their settings, memory state and environment on this machine; a user who exists
// stays as they are.
func (d *DB) EnsureUser(id string) error {
	if id == "" {
		return errors.New("storage: a user needs an id")
	}
	return d.gorm.Transaction(func(tx *gorm.DB) error {
		if err := tx.Attrs(UserRow{CreatedAt: time.Now().UTC()}).FirstOrCreate(&UserRow{}, UserRow{ID: id}).Error; err != nil {
			return fmt.Errorf("create user %s: %w", id, err)
		}
		if err := tx.FirstOrCreate(&MemoryStateRow{}, MemoryStateRow{UserID: id}).Error; err != nil {
			return fmt.Errorf("create memory state of %s: %w", id, err)
		}
		if err := tx.Attrs(SettingsRow{RedactionKey: rand.Text()}).FirstOrCreate(&SettingsRow{}, SettingsRow{UserID: id}).Error; err != nil {
			return fmt.Errorf("create settings of %s: %w", id, err)
		}
		if err := tx.Attrs(EnvironmentRow{ID: uuid.NewString(), Name: LocalEnvironmentName, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).
			FirstOrCreate(&EnvironmentRow{}, EnvironmentRow{UserID: id, Kind: EnvironmentLocal}).Error; err != nil {
			return fmt.Errorf("create environment of %s: %w", id, err)
		}
		return nil
	})
}

// Users lists the users on this shard.
func (d *DB) Users() ([]string, error) {
	var ids []string
	return ids, d.gorm.Model(&UserRow{}).Order("id").Pluck("id", &ids).Error
}
