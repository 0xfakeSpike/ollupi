package repo

import (
	"embed"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// The schema changes by numbered steps, migrations/NNNN_<what>.sql, each
// run once, in order, in its own transaction. 0001_init.sql is the schema
// as it was when they began. A database records how many steps it has taken
// in PRAGMA user_version; one taken further than this ollupi knows, by a
// newer ollupi, is refused rather than used. Steps only go forward.
//
// Steps run with foreign keys off, so one can rebuild a table the others
// refer to without cascading deletes; before a step commits, the keys of
// every row are checked, and any that points nowhere fails the step.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// steps are the migrations' SQL in order; their numbers run from 1 without
// a gap.
func steps() ([]string, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	out := make([]string, 0, len(names))
	for i, name := range names {
		number, _, ok := strings.Cut(name, "_")
		if n, err := strconv.Atoi(number); !ok || err != nil || n != i+1 || len(number) != 4 || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("migration %s: want %04d_<what>.sql", name, i+1)
		}
		sql, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			return nil, err
		}
		out = append(out, string(sql))
	}
	return out, nil
}

func (d *DB) migrate() error {
	all, err := steps()
	if err != nil {
		return err
	}
	var version int
	if err := d.gorm.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(all) {
		return fmt.Errorf("the database is at schema version %d, from a newer ollupi; this one knows up to %d", version, len(all))
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
	if version == len(all) {
		return nil
	}
	if err := d.gorm.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		return fmt.Errorf("turn foreign keys off to migrate: %w", err)
	}
	for i := version; i < len(all); i++ {
		if err := d.gorm.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(all[i]).Error; err != nil {
				return err
			}
			var broken []struct{ Table string }
			if err := tx.Raw("PRAGMA foreign_key_check").Scan(&broken).Error; err != nil {
				return err
			}
			if len(broken) > 0 {
				return fmt.Errorf("%d rows of %s refer to rows that do not exist", len(broken), broken[0].Table)
			}
			return tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)).Error
		}); err != nil {
			return fmt.Errorf("migrate to schema version %d: %w", i+1, err)
		}
	}
	if err := d.gorm.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return fmt.Errorf("turn foreign keys back on after migrating: %w", err)
	}
	return nil
}
