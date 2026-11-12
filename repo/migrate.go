package repo

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"gorm.io/gorm"
)

// The shard's schema changes by the numbered steps in migrations/, as pkg/sqlitedb runs them. 0001_init.sql is the
// schema as it was when they began.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// shardSteps are the SQL of the shard's migrations in order.
func shardSteps() ([]string, error) {
	dir, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	return sqlitedb.Steps(dir)
}

func (d *DB) migrate() error {
	all, err := shardSteps()
	if err != nil {
		return err
	}
	version, err := sqlitedb.Version(d.gorm, len(all))
	if err != nil {
		return err
	}
	if version == 0 {
		// A database from before migrations: gorm made its tables, so it is
		// brought to the first step's schema as gorm always did it.
		var tables int64
		if err := d.gorm.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'workspaces'").Scan(&tables).Error; err != nil {
			return err
		}
		if tables > 0 {
			if err := d.gorm.Transaction(func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(rowTypes...); err != nil {
					return err
				}
				return tx.Exec("PRAGMA user_version = 1").Error
			}); err != nil {
				return fmt.Errorf("bring the database to schema version 1: %w", err)
			}
			version = 1
		}
	}
	return sqlitedb.Migrate(d.gorm, all, version)
}
