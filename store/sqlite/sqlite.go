package sqlite

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
	"github.com/0xfakeSpike/ollupi/store"
	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	g       *gorm.DB
	feed    *store.Feed
	telling sync.Mutex
	epoch   atomic.Int64
	// base is the user's home: workspace roots are kept relative to it, so
	// the file means the same folders on another machine (paths.go).
	base string
}

var _ store.Store = (*Store)(nil)

// Open opens the store in home. Workspace roots are kept relative to the
// user's home, so the file carries them to another machine.
func Open(home string) (*Store, error) { return openAt(home, userHome()) }

// openAt opens the store in home relative to base, the home its workspace
// roots are read and written against.
func openAt(home, base string) (*Store, error) {
	path := filepath.Join(home, "data", "store.sqlite")
	g, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(g, migrations, "migrations"); err != nil {
		sqlitedb.Close(g)
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	if err := normalizeRoots(g, base); err != nil {
		sqlitedb.Close(g)
		return nil, fmt.Errorf("store: %s: %w", path, err)
	}
	return &Store{g: g, feed: store.NewFeed(), base: base}, nil
}

func (s *Store) Close() error { return sqlitedb.Close(s.g) }

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
	return s.g.WithContext(ctx).Transaction(func(g *gorm.DB) error { return fn(reader{g: g, base: s.base}) })
}

func (s *Store) Update(ctx context.Context, fn func(store.Tx) error) error {
	s.telling.Lock()
	defer s.telling.Unlock()
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
		t := &tx{reader: reader{g: g, base: s.base}, views: map[string]conversation.View{}, threads: map[string]*pending{}}
		if err := fn(t); err != nil {
			return err
		}
		var err error
		if events, err = t.events(); err != nil || len(events) == 0 {
			return err
		}
		seq, err := t.commitSeq()
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
