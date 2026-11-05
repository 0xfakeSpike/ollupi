package repo

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCatalogPlacesUsersOnShards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog", "catalog.sqlite")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ShardOf("a"); !errors.Is(err, ErrNoShard) {
		t.Errorf("no shards yet: %v", err)
	}
	for shard, accepting := range map[string]bool{"s1": true, "s2": true, "kept": false} {
		if err := c.Publish(shard, "http://"+shard, accepting); err != nil {
			t.Fatal(err)
		}
	}
	placed := map[string]int{}
	for _, user := range []string{"a", "b", "c", "d"} {
		shard, err := c.ShardOf(user)
		if err != nil {
			t.Fatal(err)
		}
		placed[shard]++
		if again, err := c.ShardOf(user); err != nil || again != shard {
			t.Errorf("%s moved from %s to %s: %v", user, shard, again, err)
		}
	}
	if placed["s1"] != 2 || placed["s2"] != 2 {
		t.Errorf("placed %v", placed)
	}
	if err := c.Place("customer", "kept"); err != nil {
		t.Fatal(err)
	}
	if shard, err := c.ShardOf("customer"); err != nil || shard != "kept" {
		t.Errorf("the customer is on %s: %v", shard, err)
	}
	if err := c.Place("a", "kept"); err == nil {
		t.Error("a user with a shard was moved")
	}

	if err := c.Publish("s1", "http://standby", true); err != nil {
		t.Fatal(err)
	}
	if addr, err := c.Address("s1"); err != nil || addr != "http://standby" {
		t.Errorf("the address after a takeover: %q, %v", addr, err)
	}
	if _, err := c.Address("gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown shard: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if shard, err := reopened.ShardOf("customer"); err != nil || shard != "kept" {
		t.Errorf("after reopening: %s, %v", shard, err)
	}
}
