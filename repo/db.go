package repo

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"

	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	g, err := sqlitedb.Open(path)
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
		if cerr := sqlitedb.Close(g); cerr != nil {
			return nil, fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, err
	}
	return db, nil
}

func (d *DB) Close() error { return sqlitedb.Close(d.gorm) }

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
	&MemoryRow{}, &MemoryStateRow{}, &SettingsRow{}, &ShardLeaseRow{}, &BackgroundCallRow{},
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
