package router

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/0xfakeSpike/ollupi/pkg/sqlitedb"
)

func TestCatalogPlacesUsersOnShards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog", "catalog.sqlite")
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Assign(t.Context(), "a"); !errors.Is(err, ErrNoShard) {
		t.Errorf("no shards yet: %v", err)
	}
	if shard, err := c.ShardOf(t.Context(), "a"); shard != "" || err != nil {
		t.Errorf("a, placed nowhere, is on %q: %v", shard, err)
	}
	for shard, accepting := range map[string]bool{"s1": true, "s2": true, "kept": false} {
		if err := c.Publish(t.Context(), shard, "http://"+shard, accepting); err != nil {
			t.Fatal(err)
		}
	}
	placed := map[string]int{}
	for _, user := range []string{"a", "b", "c", "d"} {
		shard, err := c.Assign(t.Context(), user)
		if err != nil {
			t.Fatal(err)
		}
		placed[shard]++
		if again, err := c.Assign(t.Context(), user); err != nil || again != shard {
			t.Errorf("%s moved from %s to %s: %v", user, shard, again, err)
		}
		if again, err := c.ShardOf(t.Context(), user); err != nil || again != shard {
			t.Errorf("%s moved from %s to %s: %v", user, shard, again, err)
		}
	}
	if placed["s1"] != 2 || placed["s2"] != 2 {
		t.Errorf("placed %v", placed)
	}
	if err := c.Place(t.Context(), "customer", "kept"); err != nil {
		t.Fatal(err)
	}
	if shard, err := c.ShardOf(t.Context(), "customer"); err != nil || shard != "kept" {
		t.Errorf("the customer is on %s: %v", shard, err)
	}
	if err := c.Place(t.Context(), "a", "kept"); err == nil {
		t.Error("a user with a shard was moved")
	}

	if err := c.Publish(t.Context(), "s1", "http://standby", true); err != nil {
		t.Fatal(err)
	}
	if addr, err := c.Address(t.Context(), "s1"); err != nil || addr != "http://standby" {
		t.Errorf("the address after a takeover: %q, %v", addr, err)
	}
	if _, err := c.Address(t.Context(), "gone"); !errors.Is(err, ErrUnknownShard) {
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
	if shard, err := reopened.ShardOf(t.Context(), "customer"); err != nil || shard != "kept" {
		t.Errorf("after reopening: %s, %v", shard, err)
	}
}

// A catalog from before migrations, which gorm made, takes the first step as it is and keeps its rows.
func TestACatalogFromBeforeMigrationsKeepsItsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	g, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"CREATE TABLE `shards` (`id` text,`address` text NOT NULL,`accepting` numeric NOT NULL,PRIMARY KEY (`id`))",
		"CREATE TABLE `placements` (`user_id` text,`shard_id` text NOT NULL,PRIMARY KEY (`user_id`))",
		"CREATE INDEX `idx_placements_shard_id` ON `placements`(`shard_id`)",
		"INSERT INTO shards VALUES ('s1', 'http://s1', 1)",
		"INSERT INTO placements VALUES ('a', 's1')",
	} {
		if err := g.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := sqlitedb.Close(g); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if shard, err := c.ShardOf(t.Context(), "a"); err != nil || shard != "s1" {
		t.Errorf("a is on %q: %v", shard, err)
	}
	var version int
	if err := c.gorm.Raw("PRAGMA user_version").Scan(&version).Error; err != nil || version != 2 {
		t.Errorf("schema version %d: %v", version, err)
	}
}

func openCatalog(t *testing.T, shards map[string]int64) *Catalog {
	t.Helper()
	c, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	for shard, load := range shards {
		if err := c.Publish(t.Context(), shard, "http://"+shard, true); err != nil {
			t.Fatal(err)
		}
		if err := c.ReportLoad(t.Context(), shard, load); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func assignMany(t *testing.T, c *Catalog, prefix string, n int) map[string]int {
	t.Helper()
	placed := map[string]int{}
	for i := range n {
		shard, err := c.Assign(t.Context(), fmt.Sprint(prefix, i))
		if err != nil {
			t.Fatal(err)
		}
		placed[shard]++
	}
	return placed
}

// New users go to the lighter shards by the load they report, not by how many users they have.
func TestNewUsersAvoidABusyShard(t *testing.T) {
	c := openCatalog(t, map[string]int64{"s1": 10, "s2": 10, "busy": 1000})
	placed := assignMany(t, c, "u", 200)
	if placed["busy"] != 0 || placed["s1"]+placed["s2"] != 200 {
		t.Errorf("placed %v", placed)
	}
	if err := c.ReportLoad(t.Context(), "gone", 1); !errors.Is(err, ErrUnknownShard) {
		t.Errorf("the load of an unknown shard: %v", err)
	}
}

// A shard just added, with no users and as idle as the rest, takes its share of new users rather than all of them.
func TestAShardJustAddedTakesASharePerhapsLargerButNotAll(t *testing.T) {
	old := map[string]int64{}
	for i := range 9 {
		old[fmt.Sprint("old", i)] = 0
	}
	c := openCatalog(t, old)
	for shard := range old {
		for i := range 50 {
			if err := c.Place(t.Context(), fmt.Sprint(shard, "-user", i), shard); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := c.Publish(t.Context(), "new", "http://new", true); err != nil {
		t.Fatal(err)
	}
	// It is drawn with one shard in five, so takes about 20 of 100; all of them would mean it took every new user.
	placed := assignMany(t, c, "u", 100)
	if placed["new"] == 0 || placed["new"] >= 50 {
		t.Errorf("the new shard took %d of 100 new users: %v", placed["new"], placed)
	}
}
