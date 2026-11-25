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
		if err := s.Update(ctx, func(tx store.Tx) error {
			return tx.EnsureUser(fmt.Sprint("u", w), account.NewEnvironment("This computer", "local", nil))
		}); err != nil {
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

func TestCommitsGoOnWhileAListenerIsSlow(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	for _, u := range []string{"slow", "other"} {
		if err := s.Update(ctx, func(tx store.Tx) error {
			return tx.EnsureUser(u, account.NewEnvironment("This computer", "local", nil))
		}); err != nil {
			t.Fatal(err)
		}
	}
	hearing, release := make(chan struct{}), make(chan struct{})
	sub := s.Feed().Subscribe("slow", func(store.Event) {
		close(hearing)
		<-release
	})
	defer sub.Close()
	go s.Update(ctx, func(tx store.Tx) error { return tx.SetSettings("slow", account.Settings{Model: "a"}) })
	<-hearing
	go s.Update(ctx, func(tx store.Tx) error { return tx.SetSettings("other", account.Settings{Model: "b"}) })
	committed := func() bool {
		var got account.Settings
		s.Read(ctx, func(r store.Reader) (err error) { got, err = r.Settings("other"); return err })
		return got.Model == "b"
	}
	deadline := time.Now().Add(5 * time.Second)
	for !committed() {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("a commit waited for a listener of an earlier one")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
}

func TestDeltasKeepTheirPlaceAfterEarlierCommits(t *testing.T) {
	s := open(t, t.TempDir())
	ctx := context.Background()
	if err := s.Update(ctx, func(tx store.Tx) error {
		return tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil))
	}); err != nil {
		t.Fatal(err)
	}
	hearing, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var heard []string
	sub := s.Feed().Subscribe("u", func(e store.Event) {
		if e.Owned != nil {
			close(hearing)
			<-release
		}
		mu.Lock()
		heard = append(heard, map[bool]string{true: "delta", false: "commit"}[e.Delta != nil])
		mu.Unlock()
	})
	defer sub.Close()
	committed := make(chan error, 1)
	go func() {
		committed <- s.Update(ctx, func(tx store.Tx) error { return tx.SetSettings("u", account.Settings{Model: "a"}) })
	}()
	<-hearing
	streamed := make(chan struct{})
	go func() {
		s.Feed().Publish([]store.Event{{User: "u", Thread: "th", Delta: &store.Delta{Text: "hi"}}}, func() { close(streamed) })
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	<-streamed
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 2 || heard[0] != "commit" || heard[1] != "delta" {
		t.Errorf("heard %v, want the commit before the delta published after it", heard)
	}
}
