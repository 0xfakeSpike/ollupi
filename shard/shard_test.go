package shard

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/catalog"
	"github.com/0xfakeSpike/ollupi/repo/db"
)

// A standby waits while another process holds the shard, takes it once that one stops, and the one it took the shard
// from can no longer write.
func TestAStandbyTakesTheShardAndFencesTheOneBefore(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	home := t.TempDir()
	cat, err := catalog.Open(filepath.Join(home, "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	open := func() *db.DB {
		store, err := db.Open(home)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	}
	member := func(address string) *Member {
		return &Member{ID: "s1", Address: address, Accepting: true, Directory: cat, TTL: 300 * time.Millisecond}
	}
	first, firstStore := member("http://first"), open()
	serving, stop := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Run(ctx, firstStore, func(context.Context) error {
			close(serving)
			<-stop
			return nil
		})
	}()
	<-serving
	if err := first.Admit(ctx, "u"); err == nil {
		t.Error("a user placed nowhere was admitted")
	}
	if shard, address, err := cat.Locate(ctx, "u"); err != nil || shard != "s1" || address != "http://first" {
		t.Fatalf("u on %s at %q: %v", shard, address, err)
	}
	if err := first.Admit(ctx, "u"); err != nil {
		t.Errorf("the shard's own user: %v", err)
	}

	second, secondStore := member("http://second"), open()
	took := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.Run(ctx, secondStore, func(context.Context) error {
			close(took)
			return nil
		})
	}()
	select {
	case <-took:
		t.Fatal("the standby took a held shard")
	case <-time.After(time.Second):
	}
	close(stop)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	<-took
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if err := firstStore.EnsureUser("late"); !errors.Is(err, db.ErrShardLost) {
		t.Errorf("a write of the process the shard was taken from: %v", err)
	}
}
