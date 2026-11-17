// Package sqlite keeps a shard's store in one SQLite database.
package sqlite

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync/atomic"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"github.com/0xfakeSpike/ollupi/store"
	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	g    *gorm.DB
	feed *store.Feed
	// epoch is the holder's, 0 while the process does not hold the shard through a lease.
	epoch atomic.Int64
}

var _ store.Store = (*Store)(nil)

func Open(home string) (*Store, error) {
	path := filepath.Join(home, "data", "store.sqlite")
	g, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	if err := migrate(g); err != nil {
		sqlitedb.Close(g)
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	return &Store{g: g, feed: store.NewFeed()}, nil
}

func migrate(g *gorm.DB) error {
	dir, _ := fs.Sub(migrations, "migrations") // a valid path: it cannot fail
	steps, err := sqlitedb.Steps(dir)
	if err != nil {
		return err
	}
	version, err := sqlitedb.Version(g, len(steps))
	if err != nil {
		return err
	}
	return sqlitedb.Migrate(g, steps, version)
}

func (s *Store) Close() error { return sqlitedb.Close(s.g) }

// Gorm is the store's database handle, for measuring what the store does with it, as bench does; writing through it
// goes around the epoch check and the feed.
func (s *Store) Gorm() *gorm.DB { return s.g }

func (s *Store) Feed() *store.Feed { return s.feed }

func (s *Store) Fence(epoch int64) error {
	res := s.g.Exec(`INSERT INTO shard_epoch(id, epoch) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET epoch = excluded.epoch WHERE shard_epoch.epoch <= excluded.epoch`, epoch)
	if res.Error != nil {
		return fmt.Errorf("store: fence the shard: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return store.ErrShardLost
	}
	s.epoch.Store(epoch)
	return nil
}

func (s *Store) Read(ctx context.Context, fn func(store.Reader) error) error {
	return s.g.WithContext(ctx).Transaction(func(g *gorm.DB) error { return fn(reader{g: g}) })
}

// SQLite's one writer keeps the epoch from changing between the check and the writes after it.
func (s *Store) Update(ctx context.Context, fn func(store.Tx) error) error {
	var events []store.Event
	err := s.g.WithContext(ctx).Transaction(func(g *gorm.DB) error {
		if mine := s.epoch.Load(); mine != 0 {
			var current int64
			if err := g.Raw("SELECT epoch FROM shard_epoch WHERE id = 1").Scan(&current).Error; err != nil {
				return fmt.Errorf("store: check the shard's epoch: %w", err)
			}
			if current != mine {
				return store.ErrShardLost
			}
		}
		t := &tx{reader: reader{g: g}, views: map[string]conversation.View{}, threads: map[string]*pending{}}
		if err := fn(t); err != nil {
			return err
		}
		events = t.events()
		if len(events) == 0 {
			return nil
		}
		seq, err := t.number()
		if err != nil {
			return err
		}
		for i := range events {
			events[i].Seq = seq
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, e := range events {
		s.feed.Publish(e)
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return store.ErrNotFound
	}
	return err
}
