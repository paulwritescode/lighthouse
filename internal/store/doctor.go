package store

import (
	"context"
	"fmt"
	"os"
)

// Check is one line of a health report. Status is ok, warn or fail.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Doctor inspects the database and reports problems with a suggested fix.
// path may be empty for an in-memory database.
func (s *Store) Doctor(ctx context.Context, path string) []Check {
	var out []Check
	add := func(name, status, format string, a ...any) {
		out = append(out, Check{Name: name, Status: status, Detail: fmt.Sprintf(format, a...)})
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		add("integrity", "fail", "SQLite reports %q %v. Restore the newest backup: lighthouse backup lists them.", integrity, err)
	} else {
		add("integrity", "ok", "the database file is consistent")
	}
	var version int
	_ = s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version)
	if version == schemaVersion {
		add("schema", "ok", "version %d", version)
	} else {
		add("schema", "fail", "version %d, this build expects %d", version, schemaVersion)
	}
	var live, indexed int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE deleted_at IS NULL`).Scan(&live)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts_fts`).Scan(&indexed)
	if live == indexed {
		add("search index", "ok", "%d records indexed", indexed)
	} else {
		add("search index", "warn", "%d live records but %d indexed; re-save or import a backup to repair", live, indexed)
	}
	head, _ := s.Head(ctx)
	var behind int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE seq=0`).Scan(&behind)
	if behind == 0 {
		add("change feed", "ok", "head %d", head)
	} else {
		add("change feed", "warn", "%d records have no sequence number", behind)
	}
	var dangling int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM links l WHERE NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.id=l.from_id) OR NOT EXISTS (SELECT 1 FROM artifacts a WHERE a.id=l.to_id)`).Scan(&dangling)
	if dangling == 0 {
		add("links", "ok", "no orphaned links")
	} else {
		add("links", "warn", "%d links point at purged records", dangling)
	}
	var tokens, history int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens WHERE revoked_at IS NULL`).Scan(&tokens)
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM record_history`).Scan(&history)
	add("api tokens", "ok", "%d active", tokens)
	add("undo history", "ok", "%d snapshots kept for %d days", history, historyKeepDays)
	if path != "" && path != ":memory:" {
		if fi, err := os.Stat(path + "-wal"); err == nil {
			if fi.Size() > 256<<20 {
				add("wal", "warn", "the write-ahead log is %d MiB; run lighthouse backup to checkpoint it", fi.Size()>>20)
			} else {
				add("wal", "ok", "%d KiB", fi.Size()>>10)
			}
		}
	}
	return out
}
