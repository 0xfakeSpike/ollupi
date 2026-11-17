package catalog

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/repo/sqlitedb"
)

func TestCatalogPlacesUsersOnShards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog", "catalog.sqlite")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Locate(t.Context(), "a"); !errors.Is(err, ErrNoShard) {
		t.Errorf("no shards yet: %v", err)
	}
	if shard, err := c.ShardOf(t.Context(), "a"); shard != "" || err != nil {
		t.Errorf("a, placed nowhere, is on %q: %v", shard, err)
	}
	for shard, accepting := range map[string]bool{"s1": true, "s2": true, "kept": false} {
		acquire(t, c, shard, "p-"+shard, accepting, 0)
	}
	placed := map[string]int{}
	for _, user := range []string{"a", "b", "c", "d"} {
		shard, address, err := c.Locate(t.Context(), user)
		if err != nil || address != "http://"+shard {
			t.Fatalf("%s on %s at %q: %v", user, shard, address, err)
		}
		placed[shard]++
		if again, _, err := c.Locate(t.Context(), user); err != nil || again != shard {
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
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if shard, err := reopened.ShardOf(t.Context(), "customer"); err != nil || shard != "kept" {
		t.Errorf("after reopening: %s, %v", shard, err)
	}
}

// One process holds a shard at a time; the address is its holder's, and each holder writes under a later epoch.
func TestOneProcessHoldsAShard(t *testing.T) {
	c := openCatalog(t, nil)
	first := acquire(t, c, "s1", "first", true, 0)
	if _, held, err := c.Acquire(t.Context(), "s1", "second", "http://second", true, time.Minute); err != nil || held {
		t.Fatalf("a second process took a held shard: %v, %v", held, err)
	}
	if err := c.Renew(t.Context(), "s1", "first", first, time.Minute, 7); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(t.Context(), "s1", "first", first); err != nil {
		t.Fatal(err)
	}
	if _, address, err := c.Locate(t.Context(), "a"); err != nil || address != "" {
		t.Errorf("the address of a shard no process holds: %q, %v", address, err)
	}
	second, held, err := c.Acquire(t.Context(), "s1", "second", "http://second", true, time.Minute)
	if err != nil || !held || second <= first {
		t.Fatalf("taking a released shard: epoch %d after %d, %v, %v", second, first, held, err)
	}
	if _, address, err := c.Locate(t.Context(), "a"); err != nil || address != "http://second" {
		t.Errorf("the address after a takeover: %q, %v", address, err)
	}
	if err := c.Renew(t.Context(), "s1", "first", first, time.Minute, 0); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("renewing a lease taken over: %v", err)
	}
	if _, held, err := c.Acquire(t.Context(), "s2", "third", "http://third", true, -time.Second); err != nil || !held {
		t.Fatal(held, err)
	}
	if _, held, err := c.Acquire(t.Context(), "s2", "fourth", "http://fourth", true, time.Minute); err != nil || !held {
		t.Errorf("a lease that ran out was not taken: %v, %v", held, err)
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
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if shard, err := c.ShardOf(t.Context(), "a"); err != nil || shard != "s1" {
		t.Errorf("a is on %q: %v", shard, err)
	}
	var version int
	if err := c.gorm.Raw("PRAGMA user_version").Scan(&version).Error; err != nil || version != 3 {
		t.Errorf("schema version %d: %v", version, err)
	}
}

func openCatalog(t *testing.T, shards map[string]int64) *Catalog {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	for shard, load := range shards {
		acquire(t, c, shard, "p-"+shard, true, load)
	}
	return c
}

func acquire(t *testing.T, c *Catalog, shard, owner string, accepting bool, load int64) int64 {
	t.Helper()
	epoch, held, err := c.Acquire(t.Context(), shard, owner, "http://"+shard, accepting, time.Minute)
	if err != nil || !held {
		t.Fatalf("taking %s: %v, %v", shard, held, err)
	}
	if err := c.Renew(t.Context(), shard, owner, epoch, time.Minute, load); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func assignMany(t *testing.T, c *Catalog, prefix string, n int) map[string]int {
	t.Helper()
	placed := map[string]int{}
	for i := range n {
		shard, _, err := c.Locate(t.Context(), fmt.Sprint(prefix, i))
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
	acquire(t, c, "new", "p-new", true, 0)
	// It is drawn with one shard in five, so takes about 20 of 100; all of them would mean it took every new user.
	placed := assignMany(t, c, "u", 100)
	if placed["new"] == 0 || placed["new"] >= 50 {
		t.Errorf("the new shard took %d of 100 new users: %v", placed["new"], placed)
	}
}

// Shards and routers open one catalog file each from a process of their own, and may do so at once.
func TestProcessesShareACatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog", "catalog.sqlite")
	// The file is there before they start: SQLite may refuse a second process that turns a new file to WAL at once.
	first, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlitedb.Close(first)
	const processes = 6
	catalogs := make([]*Catalog, processes)
	var wg sync.WaitGroup
	errs := make([]error, processes)
	for i := range processes {
		wg.Go(func() { catalogs[i], errs[i] = Open(path) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("process %d opening the catalog with the others: %v", i, err)
		}
		t.Cleanup(func() { catalogs[i].Close() })
	}
	for _, shard := range []string{"s1", "s2", "s3"} {
		acquire(t, catalogs[0], shard, "p-"+shard, true, 0)
	}
	shards := make([][]string, processes)
	for i, c := range catalogs {
		wg.Go(func() {
			for u := range 20 {
				shard, _, err := c.Locate(t.Context(), fmt.Sprint("user", u))
				if err != nil {
					errs[i] = err
					return
				}
				shards[i] = append(shards[i], shard)
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("process %d placing users with the others: %v", i, err)
		}
		if !slices.Equal(shards[i], shards[0]) {
			t.Errorf("process %d placed users on %v, process 0 on %v", i, shards[i], shards[0])
		}
	}
}
