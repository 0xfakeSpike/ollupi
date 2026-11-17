//go:build !(sqlite_fts5 || fts5)

package sqlite

var _ = build_ollupi_with_tags_sqlite_fts5
