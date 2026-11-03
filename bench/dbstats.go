package main

import (
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"github.com/0xfakeSpike/ollupi/repo"
	"gorm.io/gorm"
)

// dbStats counts the statements the agent's database runs and the time they take, reads apart from writes, and keeps
// the connection pool, whose single connection every statement waits for.
type dbStats struct {
	mu                  sync.Mutex
	reads, writes, rows int64
	readTime, writeTime time.Duration
	pool                *sql.DB
	gorm                *gorm.DB
	startStats          sql.DBStats
}

// watchDB hooks into the database repo.Open returned. The bench reaches the gorm handle repo keeps unexported, so
// that measuring the database needs nothing in repo that only the bench would use.
func watchDB(db *repo.DB) *dbStats {
	field := reflect.ValueOf(db).Elem().FieldByName("gorm")
	g := *(**gorm.DB)(unsafe.Pointer(field.UnsafeAddr()))
	pool, err := g.DB()
	if err != nil {
		log.Fatal(err)
	}
	s := &dbStats{pool: pool, gorm: g}
	begin := func(tx *gorm.DB) { tx.InstanceSet("bench:at", time.Now()) }
	end := func(write bool) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			at, ok := tx.InstanceGet("bench:at")
			if !ok {
				return
			}
			took := time.Since(at.(time.Time))
			s.mu.Lock()
			if write {
				s.writes++
				s.writeTime += took
				s.rows += tx.RowsAffected
			} else {
				s.reads++
				s.readTime += took
			}
			s.mu.Unlock()
		}
	}
	type hook interface {
		Register(string, func(*gorm.DB)) error
	}
	cb := g.Callback()
	for _, h := range []struct {
		before, after hook
		write         bool
	}{
		{cb.Create().Before("gorm:create"), cb.Create().After("gorm:create"), true},
		{cb.Update().Before("gorm:update"), cb.Update().After("gorm:update"), true},
		{cb.Delete().Before("gorm:delete"), cb.Delete().After("gorm:delete"), true},
		{cb.Raw().Before("gorm:raw"), cb.Raw().After("gorm:raw"), true},
		{cb.Query().Before("gorm:query"), cb.Query().After("gorm:query"), false},
		{cb.Row().Before("gorm:row"), cb.Row().After("gorm:row"), false},
	} {
		if err := h.before.Register("bench:begin", begin); err != nil {
			log.Fatal(err)
		}
		if err := h.after.Register("bench:end", end(h.write)); err != nil {
			log.Fatal(err)
		}
	}
	return s
}

// start marks where the measured run begins.
func (s *dbStats) start() {
	s.mu.Lock()
	s.reads, s.writes, s.rows, s.readTime, s.writeTime = 0, 0, 0, 0, 0
	s.mu.Unlock()
	s.startStats = s.pool.Stats()
}

// report is what the run cost the database for each of its turns: statements, rows written, time in statements, time
// waiting for the connection, and the items each turn left.
func (s *dbStats) report(turns int) string {
	st := s.pool.Stats()
	var items, turnRows int64
	s.gorm.Raw("SELECT count(*) FROM items").Scan(&items)
	s.gorm.Raw("SELECT count(*) FROM turns").Scan(&turnRows)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := float64(turns)
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) / n }
	return fmt.Sprintf("db_reads_per_turn=%.1f db_writes_per_turn=%.1f db_rows_written_per_turn=%.1f db_read_ms_per_turn=%.2f "+
		"db_write_ms_per_turn=%.2f conn_waits_per_turn=%.1f conn_wait_ms_per_turn=%.2f items_per_turn=%.1f",
		float64(s.reads)/n, float64(s.writes)/n, float64(s.rows)/n, ms(s.readTime), ms(s.writeTime),
		float64(st.WaitCount-s.startStats.WaitCount)/n, ms(st.WaitDuration-s.startStats.WaitDuration),
		float64(items)/float64(turnRows))
}
