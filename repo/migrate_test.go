package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The migrations and the row types are two accounts of one schema: a fresh
// database after every step has the tables, columns, keys, checks and
// indexes gorm would make from the rows.
func TestMigrationsMatchTheRows(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gormed := rawDB(t, filepath.Join(t.TempDir(), "gorm.sqlite"))
	if err := gormed.AutoMigrate(rowTypes...); err != nil {
		t.Fatal(err)
	}
	got, want := describe(t, db.gorm), describe(t, gormed)
	if len(want) < len(rowTypes) {
		t.Fatalf("the rows describe only %d lines", len(want))
	}
	for _, line := range diff(got, want) {
		t.Error(line)
	}
}

func TestADatabaseFromBeforeMigrationsKeepsItsRows(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "ollupi.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old := rawDB(t, path)
	if err := old.AutoMigrate(rowTypes...); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	if err := old.Create(&ThreadRow{ID: "kept", Title: "kept", CreatedAt: at, UpdatedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	closeRaw(t, old)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := userVersion(t, db.gorm); v != stepCount(t) {
		t.Fatalf("schema version %d, want %d", v, stepCount(t))
	}
	var n int64
	if err := db.gorm.Model(&ThreadRow{}).Where("id = ?", "kept").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("the thread from before: %d rows, %v", n, err)
	}
}

func TestADatabaseFromANewerOllupiIsRefused(t *testing.T) {
	home := t.TempDir()
	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.gorm.Exec(fmt.Sprintf("PRAGMA user_version = %d", stepCount(t)+1)).Error; err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(home); err == nil || !strings.Contains(err.Error(), "newer ollupi") {
		t.Fatalf("opening a newer database: %v", err)
	}
}

func rawDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open("file:"+path+"?_foreign_keys=on"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRaw(t, g) })
	return g
}

func closeRaw(t *testing.T, g *gorm.DB) {
	t.Helper()
	if conn, err := g.DB(); err == nil {
		conn.Close()
	}
}

func userVersion(t *testing.T, g *gorm.DB) int {
	t.Helper()
	var v int
	if err := g.Raw("PRAGMA user_version").Scan(&v).Error; err != nil {
		t.Fatal(err)
	}
	return v
}

func stepCount(t *testing.T) int {
	t.Helper()
	all, err := steps()
	if err != nil {
		t.Fatal(err)
	}
	return len(all)
}

var (
	space = regexp.MustCompile(`\s+`)
	check = regexp.MustCompile("CONSTRAINT `[^`]+` CHECK ")
)

// describe lists a database's schema as lines that do not depend on the
// order of columns or the layout of its SQL.
func describe(t *testing.T, g *gorm.DB) []string {
	t.Helper()
	var objects []struct{ Type, Name, SQL string }
	// The message search index is no row of gorm's; TestSearchFindsMessagesBySubstring covers it.
	if err := g.Raw("SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'items_search%'").Scan(&objects).Error; err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, o := range objects {
		if o.Type == "index" {
			lines = append(lines, "index "+space.ReplaceAllString(o.SQL, " "))
			continue
		}
		var columns []struct {
			Name, Type string
			NotNull    bool
			Dflt       *string `gorm:"column:dflt_value"`
			PK         int
		}
		if err := g.Raw(fmt.Sprintf("PRAGMA table_info(`%s`)", o.Name)).Scan(&columns).Error; err != nil {
			t.Fatal(err)
		}
		for _, c := range columns {
			dflt := "-"
			if c.Dflt != nil {
				dflt = *c.Dflt
			}
			lines = append(lines, fmt.Sprintf("column %s.%s %s notnull=%v default=%s pk=%d", o.Name, c.Name, c.Type, c.NotNull, dflt, c.PK))
		}
		var keys []struct {
			Table, From, To string
			OnDelete        string `gorm:"column:on_delete"`
		}
		if err := g.Raw(fmt.Sprintf("PRAGMA foreign_key_list(`%s`)", o.Name)).Scan(&keys).Error; err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("key %s.%s -> %s.%s on delete %s", o.Name, k.From, k.Table, k.To, k.OnDelete))
		}
		for _, at := range check.FindAllStringIndex(o.SQL, -1) {
			lines = append(lines, fmt.Sprintf("check %s %s", o.Name, space.ReplaceAllString(balanced(o.SQL[at[0]:]), " ")))
		}
	}
	slices.Sort(lines)
	return lines
}

// balanced is s up to the parenthesis that closes its first one.
func balanced(s string) string {
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return s
}

func diff(got, want []string) []string {
	var out []string
	for _, l := range got {
		if !slices.Contains(want, l) {
			out = append(out, "the migrations make, the rows do not: "+l)
		}
	}
	for _, l := range want {
		if !slices.Contains(got, l) {
			out = append(out, "the rows make, the migrations do not: "+l)
		}
	}
	return out
}
