package catalog

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/sqlitedb"
	"gorm.io/gorm"
)

// SQL keeps the catalog in a database of its own, one file that every shard and router of a deployment opens. It
// decides each lease by the database's clock, as one served from elsewhere would by its server's.
type SQL struct {
	gorm *gorm.DB
}

var _ Catalog = (*SQL)(nil)

// now is the database's clock in Unix milliseconds.
const now = "CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)"

// The address is that of the process that holds the shard's lease, until ExpiresAt in Unix milliseconds by the
// database's clock.
type ShardRow struct {
	ID      string `gorm:"primaryKey"`
	Address string `gorm:"not null"`
	// A shard kept for one customer takes no new users, and one its host finds full stops taking them.
	Accepting bool `gorm:"not null"`
	// Load is by a measure all shards share; lower is lighter.
	Load      int64  `gorm:"not null;default:0"`
	Owner     string `gorm:"not null;default:''"`
	Epoch     int64  `gorm:"not null;default:0"`
	ExpiresAt int64  `gorm:"not null;default:0"`
}

func (ShardRow) TableName() string { return "shards" }

type PlacementRow struct {
	UserID  string `gorm:"primaryKey"`
	ShardID string `gorm:"not null;index"`
}

func (PlacementRow) TableName() string { return "placements" }

//go:embed migrations/*.sql
var migrationFiles embed.FS

func OpenSQL(path string) (*SQL, error) {
	g, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	if err := migrate(g); err != nil {
		err = fmt.Errorf("catalog: %s: %w", path, err)
		if cerr := sqlitedb.Close(g); cerr != nil {
			return nil, fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, err
	}
	return &SQL{gorm: g}, nil
}

// The first step is the schema gorm made before there were steps, so a catalog from then takes it as it is.
func migrate(g *gorm.DB) error {
	dir, _ := fs.Sub(migrationFiles, "migrations") // a valid path: it cannot fail
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

func (c *SQL) Close() error { return sqlitedb.Close(c.gorm) }

func (c *SQL) Acquire(ctx context.Context, shard, owner, address string, accepting bool, ttl time.Duration) (int64, bool, error) {
	var epoch int64
	err := c.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(`INSERT INTO shards(id, address, accepting, owner, epoch, expires_at) VALUES (?, ?, ?, ?, 1, `+now+` + ?)
			ON CONFLICT(id) DO UPDATE SET address = excluded.address, accepting = excluded.accepting, owner = excluded.owner,
				epoch = shards.epoch + 1, expires_at = excluded.expires_at
			WHERE shards.owner = '' OR shards.expires_at <= `+now, shard, address, accepting, owner, ttl.Milliseconds())
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		return tx.Model(&ShardRow{}).Where("id = ?", shard).Select("epoch").Scan(&epoch).Error
	})
	return epoch, epoch > 0, err
}

func (c *SQL) Renew(ctx context.Context, shard, owner string, epoch int64, ttl time.Duration, load int64) error {
	res := c.gorm.WithContext(ctx).Model(&ShardRow{}).Where("id = ? AND owner = ? AND epoch = ?", shard, owner, epoch).
		Updates(map[string]any{"expires_at": gorm.Expr(now+" + ?", ttl.Milliseconds()), "load": load})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (c *SQL) Release(ctx context.Context, shard, owner string, epoch int64) error {
	return c.gorm.WithContext(ctx).Model(&ShardRow{}).Where("id = ? AND owner = ? AND epoch = ?", shard, owner, epoch).
		Updates(map[string]any{"owner": "", "expires_at": 0}).Error
}

func (c *SQL) Locate(ctx context.Context, user string) (string, string, error) {
	var shard, address string
	err := c.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if shard, err = assign(tx, user); err != nil {
			return err
		}
		return tx.Model(&ShardRow{}).Where("id = ? AND owner <> '' AND expires_at > "+now, shard).
			Select("address").Scan(&address).Error
	})
	return shard, address, err
}

func (c *SQL) Place(ctx context.Context, user, shard string) error {
	res := c.gorm.WithContext(ctx).Exec(`INSERT INTO placements(user_id, shard_id) VALUES (?, ?) ON CONFLICT(user_id) DO NOTHING`, user, shard)
	if res.Error != nil {
		return res.Error
	}
	placed, err := c.ShardOf(ctx, user)
	if err != nil {
		return err
	}
	if placed != shard {
		return fmt.Errorf("catalog: user %s is already on shard %s", user, placed)
	}
	return nil
}

func (c *SQL) ShardOf(ctx context.Context, user string) (string, error) {
	return shardOf(c.gorm.WithContext(ctx), user)
}

// The lighter of two drawn at random, rather than the lightest of all, keeps a shard just added, or one whose load is
// not yet reported, from taking every new user until it catches up, and places nearly as evenly.
func assign(tx *gorm.DB, user string) (string, error) {
	if shard, err := shardOf(tx, user); err != nil || shard != "" {
		return shard, err
	}
	var open []candidate
	if err := tx.Raw(`SELECT s.id, s.load, count(p.user_id) AS users FROM shards s
		LEFT JOIN placements p ON p.shard_id = s.id WHERE s.accepting GROUP BY s.id`).Scan(&open).Error; err != nil {
		return "", err
	}
	if len(open) == 0 {
		return "", ErrNoShard
	}
	shard := lighterOfTwo(open).ID
	return shard, tx.Create(&PlacementRow{UserID: user, ShardID: shard}).Error
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
