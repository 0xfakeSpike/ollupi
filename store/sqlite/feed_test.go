package sqlite

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestCommitsAreToldInTheOrderOfTheirSeq(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	const writers, writes = 4, 30
	for w := range writers {
		if err := s.Update(ctx, func(tx store.Tx) error { return tx.EnsureUser(fmt.Sprint("u", w)) }); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var told []int64
	sub := s.Feed().SubscribeAll(func(e store.Event) {
		time.Sleep(time.Duration(e.Seq%3) * time.Millisecond)
		mu.Lock()
		told = append(told, e.Seq)
		mu.Unlock()
	})
	defer sub.Close()
	var g sync.WaitGroup
	for w := range writers {
		g.Go(func() {
			user := fmt.Sprint("u", w)
			for range writes {
				err := s.Update(ctx, func(tx store.Tx) error { return tx.SetSettings(user, account.Settings{Memory: true}) })
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	g.Wait()
	if len(told) != writers*writes {
		t.Fatalf("told %d events, want %d", len(told), writers*writes)
	}
	for i := 1; i < len(told); i++ {
		if told[i] <= told[i-1] {
			t.Fatalf("event %d was told with seq %d after seq %d", i, told[i], told[i-1])
		}
	}
}
