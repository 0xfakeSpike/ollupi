package router

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"

	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"gorm.io/gorm"
)

var (
	// ErrNoShard says no shard can take a new user: none has been published, or none takes new users.
	ErrNoShard = errors.New("router: no shard takes this user")
	// ErrUnknownShard says no shard of that name has been published.
	ErrUnknownShard = errors.New("router: no such shard")
)

// Catalog is what all shards share: which shard each user is on, and where each shard runs now. The turns and files of
// a user stay on their shard; this holds only enough to find it, in a SQLite database of its own, apart from the
// shards'. A deployment that keeps it elsewhere, such as in Postgres behind Redis, implements Directory and
// gateway.Placements over that instead.
type Catalog struct {
	gorm *gorm.DB
}

type ShardRow struct {
	ID string `gorm:"primaryKey"`
	// Address is where the process holding the shard takes connections.
	Address string `gorm:"not null"`
	// Accepting says new users may be placed here; a shard kept for one customer takes none, and one its host finds
	// full stops taking them.
	Accepting bool `gorm:"not null"`
	// Load is how busy the shard last said it is, by a measure all shards share; lower is lighter.
	Load int64 `gorm:"not null;default:0"`
}

func (ShardRow) TableName() string { return "shards" }

type PlacementRow struct {
	UserID  string `gorm:"primaryKey"`
	ShardID string `gorm:"not null;index"`
}

func (PlacementRow) TableName() string { return "placements" }

//go:embed migrations/*.sql
var migrationFiles embed.FS

// OpenCatalog opens the catalog in the file at path, making it if need be.
func OpenCatalog(path string) (*Catalog, error) {
	g, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	if err := migrate(g); err != nil {
		err = fmt.Errorf("router: %s: %w", path, err)
		if cerr := sqlitedb.Close(g); cerr != nil {
			return nil, fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, err
	}
	return &Catalog{gorm: g}, nil
}

// migrate takes the catalog's steps; the first is the schema gorm made before there were steps, so a catalog from
// then takes it as it is.
func migrate(g *gorm.DB) error {
	dir, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	all, err := sqlitedb.Steps(dir)
	if err != nil {
		return err
	}
	version, err := sqlitedb.Version(g, len(all))
	if err != nil {
		return err
	}
	return sqlitedb.Migrate(g, all, version)
}

func (c *Catalog) Close() error { return sqlitedb.Close(c.gorm) }

// Publish says where a shard runs now, as the process that took it does once it holds the lease.
func (c *Catalog) Publish(ctx context.Context, shard, address string, accepting bool) error {
	return c.gorm.WithContext(ctx).Exec(`INSERT INTO shards(id, address, accepting) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET address = excluded.address, accepting = excluded.accepting`, shard, address, accepting).Error
}

// ReportLoad records how busy a shard is now, as the shard's gateway says every so often (gateway.Options.Load), for
// Assign to place new users by.
func (c *Catalog) ReportLoad(ctx context.Context, shard string, load int64) error {
	res := c.gorm.WithContext(ctx).Model(&ShardRow{}).Where("id = ?", shard).Update("load", load)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrUnknownShard
	}
	return nil
}

// Address says where a shard runs.
func (c *Catalog) Address(ctx context.Context, shard string) (string, error) {
	var row ShardRow
	err := c.gorm.WithContext(ctx).Where("id = ?", shard).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrUnknownShard
	}
	return row.Address, err
}

// Place puts a user on a shard of the operator's choosing, as for a customer with a shard of their own. It must come
// before the user has anything: a user with work on a shard stays there, moving their rows is not done here.
func (c *Catalog) Place(ctx context.Context, user, shard string) error {
	res := c.gorm.WithContext(ctx).Exec(`INSERT INTO placements(user_id, shard_id) VALUES (?, ?) ON CONFLICT(user_id) DO NOTHING`, user, shard)
	if res.Error != nil {
		return res.Error
	}
	placed, err := c.ShardOf(ctx, user)
	if err != nil {
		return err
	}
	if placed != shard {
		return fmt.Errorf("router: user %s is already on shard %s", user, placed)
	}
	return nil
}

// ShardOf says which shard a user is on, or "" for one placed nowhere yet. It only looks, as a shard does before it
// serves a user.
func (c *Catalog) ShardOf(ctx context.Context, user string) (string, error) {
	return shardOf(c.gorm.WithContext(ctx), user)
}

// Assign says which shard a user is on, placing a new one on the lighter of two accepting shards drawn at random. Two
// drawn, rather than the lightest of all, keep a shard just added, or one whose load is not yet reported, from taking
// every new user until it catches up, and place nearly as evenly. Between equal loads, the one with fewer users wins.
func (c *Catalog) Assign(ctx context.Context, user string) (string, error) {
	var shard string
	err := c.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if shard, err = shardOf(tx, user); err != nil || shard != "" {
			return err
		}
		var open []candidate
		if err := tx.Raw(`SELECT s.id, s.load, count(p.user_id) AS users FROM shards s
			LEFT JOIN placements p ON p.shard_id = s.id WHERE s.accepting GROUP BY s.id`).Scan(&open).Error; err != nil {
			return err
		}
		if len(open) == 0 {
			return ErrNoShard
		}
		shard = lighterOfTwo(open).ID
		return tx.Create(&PlacementRow{UserID: user, ShardID: shard}).Error
	})
	return shard, err
}

type candidate struct {
	ID    string
	Load  int64
	Users int64
}

func lighterOfTwo(open []candidate) candidate {
	if len(open) == 1 {
		return open[0]
	}
	i := rand.IntN(len(open))
	j := rand.IntN(len(open) - 1)
	if j >= i {
		j++
	}
	a, b := open[i], open[j]
	if b.Load < a.Load || b.Load == a.Load && b.Users < a.Users {
		return b
	}
	return a
}

func shardOf(g *gorm.DB, user string) (string, error) {
	var row PlacementRow
	err := g.Where("user_id = ?", user).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return row.ShardID, err
}
