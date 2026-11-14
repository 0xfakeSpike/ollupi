// Package sqlitedb opens a SQLite file for one process and brings its schema forward.
//
// The schema changes by numbered steps, NNNN_<what>.sql, each run once, in order, in its own transaction. A database
// records how many steps it has taken in PRAGMA user_version; one taken further than the program knows, by a newer
// version of it, is refused rather than used. Steps only go forward.
//
// Steps run with foreign keys off, so one can rebuild a table the others refer to without cascading deletes; before a
// step commits, the keys of every row are checked, and any that points nowhere fails the step.
package sqlitedb

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open opens the database file at path on its one connection, making its folder if need be.
func Open(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create directory of %s: %w", path, err)
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on",
	}).String()
	g, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	conn, err := g.DB()
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1)
	return g, nil
}

func Close(g *gorm.DB) error {
	conn, err := g.DB()
	if err != nil {
		return err
	}
	return conn.Close()
}

// Steps are the SQL of the migrations at the root of fsys in order; their numbers run from 1 without a gap.
func Steps(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
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
		sql, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		out = append(out, string(sql))
	}
	return out, nil
}

// Version is how many steps the database has taken, of the known ones.
func Version(g *gorm.DB, known int) (int, error) {
	var version int
	if err := g.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if version > known {
		return 0, fmt.Errorf("the database is at schema version %d, from a newer version of this program; this one knows up to %d", version, known)
	}
	return version, nil
}

// Migrate takes the steps after version.
func Migrate(g *gorm.DB, steps []string, version int) error {
	if version == len(steps) {
		return nil
	}
	if err := g.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		return fmt.Errorf("turn foreign keys off to migrate: %w", err)
	}
	for i := version; i < len(steps); i++ {
		if err := g.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(steps[i]).Error; err != nil {
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
	if err := g.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return fmt.Errorf("turn foreign keys back on after migrating: %w", err)
	}
	return nil
}
