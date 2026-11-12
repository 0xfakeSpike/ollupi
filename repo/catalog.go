package repo

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrNoShard says no shard can take the user: none has been published, or none takes new users.
var ErrNoShard = errors.New("storage: no shard takes this user")

// Catalog is what all shards share: which shard each user is on, and where each shard runs now. The turns and files of
// a user stay on their shard; this holds only enough to find it. It is its own database, which every router reaches.
type Catalog struct {
	gorm *gorm.DB
}

type ShardRow struct {
	ID string `gorm:"primaryKey"`
	// Address is where the process holding the shard takes connections.
	Address string `gorm:"not null"`
	// Accepting says new users may be placed here; a shard kept for one customer takes none.
	Accepting bool `gorm:"not null"`
}

func (ShardRow) TableName() string { return "shards" }

type PlacementRow struct {
	UserID  string `gorm:"primaryKey"`
	ShardID string `gorm:"not null;index"`
}

func (PlacementRow) TableName() string { return "placements" }

// OpenCatalog opens the catalog in the file at path, making it if need be.
func OpenCatalog(path string) (*Catalog, error) {
	g, err := openFile(path)
	if err != nil {
		return nil, err
	}
	if err := migrateCatalog(g); err != nil {
		err = fmt.Errorf("storage: %s: %w", path, err)
		if cerr := closeGorm(g); cerr != nil {
			return nil, fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, err
	}
	return &Catalog{gorm: g}, nil
}

func (c *Catalog) Close() error { return closeGorm(c.gorm) }

// Publish says where a shard runs now, as the process that took it does once it holds the lease.
func (c *Catalog) Publish(shard, address string, accepting bool) error {
	return c.gorm.Exec(`INSERT INTO shards(id, address, accepting) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET address = excluded.address, accepting = excluded.accepting`, shard, address, accepting).Error
}

// Address says where a shard runs.
func (c *Catalog) Address(shard string) (string, error) {
	var row ShardRow
	if err := c.gorm.Where("id = ?", shard).Take(&row).Error; err != nil {
		return "", notFound(err)
	}
	return row.Address, nil
}

// Place puts a user on a shard of the operator's choosing, as for a customer with a shard of their own. It must come
// before the user has anything: a user with work on a shard stays there, moving their rows is not done here.
func (c *Catalog) Place(user, shard string) error {
	res := c.gorm.Exec(`INSERT INTO placements(user_id, shard_id) VALUES (?, ?) ON CONFLICT(user_id) DO NOTHING`, user, shard)
	if res.Error != nil {
		return res.Error
	}
	placed, err := c.ShardOf(user)
	if err != nil {
		return err
	}
	if placed != shard {
		return fmt.Errorf("storage: user %s is already on shard %s", user, placed)
	}
	return nil
}

// ShardOf says which shard a user is on, placing a new user on the accepting shard with the fewest.
func (c *Catalog) ShardOf(user string) (string, error) {
	var shard string
	err := c.gorm.Transaction(func(tx *gorm.DB) error {
		var row PlacementRow
		err := tx.Where("user_id = ?", user).Take(&row).Error
		if err == nil {
			shard = row.ShardID
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var open []string
		if err := tx.Raw(`SELECT s.id FROM shards s LEFT JOIN placements p ON p.shard_id = s.id
			WHERE s.accepting GROUP BY s.id ORDER BY count(p.user_id), s.id LIMIT 1`).Scan(&open).Error; err != nil {
			return err
		}
		if len(open) == 0 {
			return ErrNoShard
		}
		shard = open[0]
		return tx.Create(&PlacementRow{UserID: user, ShardID: shard}).Error
	})
	return shard, err
}
