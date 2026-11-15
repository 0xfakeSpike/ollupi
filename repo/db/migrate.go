package db

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/0xfakeSpike/ollupi/repo/sqlitedb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The shard's schema changes by the numbered steps in migrations/, as repo/sqlitedb runs them. 0001_init.sql is the
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
		var tables int64
		if err := d.gorm.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'workspaces'").Scan(&tables).Error; err != nil {
			return err
		}
		if tables > 0 {
			// A database from before migrations, which gorm made from the rows of its day. The last of those days is
			// the first step: a database it made is at version 1. An older one lacks some of it, and the rows of today
			// would bring it to a later schema than the steps expect, so it is refused instead.
			if missing, err := missingFromFirstStep(d.gorm, all[0]); err != nil {
				return err
			} else if len(missing) > 0 {
				return fmt.Errorf("the database is from an ollupi before v0.8.0 and lacks %s; open it once with ollupi v0.8.1, which brings it to schema version 1", strings.Join(missing, ", "))
			}
			if err := d.gorm.Exec("PRAGMA user_version = 1").Error; err != nil {
				return fmt.Errorf("mark the database at schema version 1: %w", err)
			}
			version = 1
		}
	}
	return sqlitedb.Migrate(d.gorm, all, version)
}

// missingFromFirstStep names the tables and columns the first step makes that the database lacks.
func missingFromFirstStep(g *gorm.DB, first string) ([]string, error) {
	scratch, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	conn, err := scratch.DB()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Each connection to :memory: is a database of its own.
	conn.SetMaxOpenConns(1)
	if err := scratch.Exec(first).Error; err != nil {
		return nil, fmt.Errorf("make the first step's schema: %w", err)
	}
	want, err := columns(scratch)
	if err != nil {
		return nil, err
	}
	have, err := columns(g)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, c := range want {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	return missing, nil
}

// columns are the database's columns, as table.column.
func columns(g *gorm.DB) ([]string, error) {
	var out []string
	err := g.Raw("SELECT m.name || '.' || c.name FROM sqlite_master m JOIN pragma_table_info(m.name) c " +
		"WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name, c.cid").Scan(&out).Error
	return out, err
}
