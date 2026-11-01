//go:build !(sqlite_fts5 || fts5)

package repo

// Message search needs SQLite's FTS5, which the driver compiles in only under a build tag:
// build and test ollupi with -tags sqlite_fts5.
var _ = build_ollupi_with_tags_sqlite_fts5
