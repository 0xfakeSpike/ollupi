package repo

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type DB struct {
	gorm *gorm.DB
	// owner is the lease Fence holds this handle to, shared by every handle made from it; nil before Fence.
	owner *atomic.Pointer[string]
}

// WithContext is the database for one request: its queries end when ctx does.
func (d *DB) WithContext(ctx context.Context) *DB {
	return &DB{gorm: d.gorm.WithContext(ctx), owner: d.owner}
}

func Open(homeDir string) (*DB, error) {
	path := filepath.Join(homeDir, "data", "ollupi.sqlite")
	g, err := openFile(path)
	if err != nil {
		return nil, err
	}
	db := &DB{gorm: g, owner: new(atomic.Pointer[string])}
	err = db.fenceWrites()
	if err == nil {
		err = db.applySchema()
	}
	if err != nil {
		err = fmt.Errorf("storage: %s: %w", path, err)
		if cerr := closeGorm(g); cerr != nil {
			return nil, fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, err
	}
	return db, nil
}

func closeGorm(g *gorm.DB) error {
	conn, err := g.DB()
	if err != nil {
		return err
	}
	return conn.Close()
}

// openFile opens a database file on its one connection, making its folder if need be.
func openFile(path string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create directory of %s: %w", path, err)
	}

	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on",
	}).String()
	g, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),

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

func (d *DB) Close() error { return closeGorm(d.gorm) }

func (d *DB) applySchema() error {
	if err := d.migrate(); err != nil {
		return err
	}
	return d.EnsureUser(DefaultUser)
}

func (d *DB) Tx(fn func(*DB) error) error {
	return d.gorm.Transaction(func(tx *gorm.DB) error {
		return fn(&DB{gorm: tx, owner: d.owner})
	})
}

func nullTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func checkOne(res *gorm.DB) error {
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("storage: wrote %d rows, want 1 (an immutable column changed?)", res.RowsAffected)
	}
	return nil
}

var rowTypes = []any{
	&UserRow{}, &EnvironmentRow{}, &WorkspaceRow{}, &ThreadRow{}, &TurnRow{}, &ItemRow{}, &CronJobRow{},
	&MemoryRow{}, &MemoryStateRow{}, &SettingsRow{}, &ShardLeaseRow{},
}

var ErrNotFound = errors.New("storage: not found")

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func where(sql string) clause.Where {
	return clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: sql}}}
}

func enumValue[T ~string](v T, values []T) (driver.Value, error) {
	if !slices.Contains(values, v) {
		return nil, fmt.Errorf("storage: %T %q is not one of %q", v, v, values)
	}
	return string(v), nil
}

type Ref string

func (r Ref) Value() (driver.Value, error) {
	if r == "" {
		return nil, nil
	}
	return string(r), nil
}

func (r *Ref) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*r = ""
	case string:
		*r = Ref(x)
	case []byte:
		*r = Ref(x)
	default:
		return fmt.Errorf("storage: cannot read %T as an id", v)
	}
	return nil
}
