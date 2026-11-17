package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// schemaVersion is stored in PRAGMA user_version. Version 2 adds the record
// envelope (revision, sequence, soft delete), the change feed, the kind
// registry, links, batch history and API tokens.
const schemaVersion = 2

// artifactColumns is the one column list used to read an artifact, in the
// order scanArtifact expects.
const artifactColumns = `id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at,updated_at,revision,seq,deleted_at,actor,batch_id,schema_version`

type rowScanner interface{ Scan(dest ...any) error }

func scanArtifact(r rowScanner) (Artifact, error) {
	var a Artifact
	var created, updated, metadata, tagsJSON string
	var deleted, batch sql.NullString
	if err := r.Scan(&a.ID, &a.Title, &a.Content, &a.Project, &a.Agent, &a.Kind, &a.ContentHash, &metadata, &tagsJSON, &created, &updated, &a.Revision, &a.Seq, &deleted, &a.Actor, &batch, &a.SchemaVersion); err != nil {
		return Artifact{}, err
	}
	var err error
	if a.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Artifact{}, err
	}
	if a.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		a.UpdatedAt = a.CreatedAt
	}
	if deleted.Valid && deleted.String != "" {
		if t, perr := time.Parse(time.RFC3339Nano, deleted.String); perr == nil {
			a.DeletedAt = &t
		}
	}
	a.BatchID = batch.String
	a.Fields = decodeFields(metadata)
	a.Tags = decodeTags(tagsJSON)
	return a, nil
}

// upgrade brings a database to schemaVersion. A file database that already
// holds data is copied to <path>.bak-v<old>-<timestamp> first, and a database
// written by a newer Lighthouse is refused rather than modified.
func (s *Store) upgrade(ctx context.Context, path string) error {
	var current int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return err
	}
	if current > schemaVersion {
		return fmt.Errorf("this database was written by a newer Lighthouse (schema %d, this build understands %d); update Lighthouse before opening it", current, schemaVersion)
	}
	if current == schemaVersion {
		return nil
	}
	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='artifacts'`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 && path != ":memory:" && path != "" {
		var rows int
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM artifacts)+(SELECT count(*) FROM memory_items)`).Scan(&rows); err == nil && rows > 0 {
			backup := fmt.Sprintf("%s.bak-v%d-%s", path, current, time.Now().UTC().Format("20060102T150405Z"))
			if err := s.BackupTo(ctx, backup); err != nil {
				return fmt.Errorf("back up database before upgrading: %w", err)
			}
			_ = os.Chmod(backup, 0o600)
		}
	}
	return s.migrateV2(ctx)
}

func (s *Store) migrateV2(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, col := range []struct{ name, ddl string }{
		{"updated_at", "ALTER TABLE artifacts ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''"},
		{"revision", "ALTER TABLE artifacts ADD COLUMN revision INTEGER NOT NULL DEFAULT 1"},
		{"seq", "ALTER TABLE artifacts ADD COLUMN seq INTEGER NOT NULL DEFAULT 0"},
		{"deleted_at", "ALTER TABLE artifacts ADD COLUMN deleted_at TEXT"},
		{"actor", "ALTER TABLE artifacts ADD COLUMN actor TEXT NOT NULL DEFAULT ''"},
		{"batch_id", "ALTER TABLE artifacts ADD COLUMN batch_id TEXT"},
		{"schema_version", "ALTER TABLE artifacts ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumnTx(ctx, tx, "artifacts", col.name, col.ddl); err != nil {
			return err
		}
	}
	const tables = `
CREATE TABLE IF NOT EXISTS changes (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL,
 kind TEXT NOT NULL,
 project TEXT NOT NULL DEFAULT '',
 op TEXT NOT NULL,
 revision INTEGER NOT NULL,
 actor TEXT NOT NULL DEFAULT '',
 batch_id TEXT,
 at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_changes_batch ON changes(batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_changes_id ON changes(id, seq);
CREATE TABLE IF NOT EXISTS record_history (
 id TEXT NOT NULL,
 revision INTEGER NOT NULL,
 snapshot_json TEXT NOT NULL,
 batch_id TEXT NOT NULL,
 at TEXT NOT NULL,
 PRIMARY KEY(id, revision, batch_id)
);
CREATE INDEX IF NOT EXISTS idx_history_batch ON record_history(batch_id);
CREATE TABLE IF NOT EXISTS kinds (
 kind TEXT PRIMARY KEY,
 version INTEGER NOT NULL DEFAULT 1,
 schema_json TEXT NOT NULL DEFAULT '{}',
 indexes_json TEXT NOT NULL DEFAULT '[]',
 exclude_from_memory INTEGER NOT NULL DEFAULT 0,
 validation TEXT NOT NULL DEFAULT 'enforce',
 updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS links (
 from_id TEXT NOT NULL,
 rel TEXT NOT NULL,
 to_id TEXT NOT NULL,
 meta_json TEXT NOT NULL DEFAULT '{}',
 at TEXT NOT NULL,
 PRIMARY KEY(from_id, rel, to_id)
);
CREATE INDEX IF NOT EXISTS idx_links_to ON links(to_id, rel);
CREATE TABLE IF NOT EXISTS api_tokens (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL UNIQUE,
 hash TEXT NOT NULL,
 salt TEXT NOT NULL,
 scopes_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 expires_at TEXT,
 last_used_at TEXT,
 revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS idempotency (
 key TEXT PRIMARY KEY,
 request_hash TEXT NOT NULL,
 response_json TEXT NOT NULL,
 at TEXT NOT NULL
);`
	if _, err := tx.ExecContext(ctx, tables); err != nil {
		return fmt.Errorf("create v0.9 tables: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET updated_at=created_at WHERE updated_at=''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET actor=agent WHERE actor=''`); err != nil {
		return err
	}
	var logged int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM changes`).Scan(&logged); err != nil {
		return err
	}
	if logged == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO changes(id,kind,project,op,revision,actor,batch_id,at) SELECT id,kind,project,'create',1,actor,NULL,created_at FROM artifacts ORDER BY created_at,id`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET seq=(SELECT c.seq FROM changes c WHERE c.id=artifacts.id AND c.op='create' LIMIT 1)`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureColumnTx(ctx context.Context, tx *sql.Tx, table, name, ddl string) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &column, &kind, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if column == name {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = tx.ExecContext(ctx, ddl)
	return err
}

// recordChange appends one row to the change feed and returns its sequence.
func recordChange(ctx context.Context, tx *sql.Tx, id, kind, project, op string, revision int64, actor, batchID string) (int64, error) {
	var batch any
	if batchID != "" {
		batch = batchID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO changes(id,kind,project,op,revision,actor,batch_id,at) VALUES(?,?,?,?,?,?,?,?)`, id, kind, project, op, revision, actor, batch, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Change is one entry in the change feed. It carries no record content.
type Change struct {
	Seq      int64     `json:"seq"`
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Project  string    `json:"project,omitempty"`
	Op       string    `json:"op"`
	Revision int64     `json:"revision"`
	Actor    string    `json:"actor,omitempty"`
	BatchID  string    `json:"batch_id,omitempty"`
	At       time.Time `json:"at"`
}

// ChangeQuery selects changes after Since. Kinds accepts "*" wildcards.
type ChangeQuery struct {
	Since     int64
	Limit     int
	Kinds     []string
	Projects  []string
	ActorsNot []string
}

// ChangePage is one page of the change feed. Reset is true when Since is
// ahead of the feed, which means the client holds state from another database
// and must resynchronise from scratch.
type ChangePage struct {
	Items   []Change `json:"items"`
	NextSeq int64    `json:"next_seq"`
	Head    int64    `json:"head"`
	Reset   bool     `json:"reset"`
}

// Head returns the newest change sequence number, or 0 for an empty feed.
func (s *Store) Head(ctx context.Context) (int64, error) {
	var head sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM changes`).Scan(&head); err != nil {
		return 0, err
	}
	return head.Int64, nil
}

// Changes lists feed entries with seq greater than q.Since, oldest first.
func (s *Store) Changes(ctx context.Context, q ChangeQuery) (ChangePage, error) {
	head, err := s.Head(ctx)
	if err != nil {
		return ChangePage{}, err
	}
	page := ChangePage{Head: head, NextSeq: q.Since, Items: []Change{}}
	if q.Since > head {
		page.Reset = true
		page.NextSeq = head
		return page, nil
	}
	limit := q.Limit
	if limit < 1 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	where := []string{"seq>?"}
	args := []any{q.Since}
	if clause, vals := globClause("kind", q.Kinds); clause != "" {
		where, args = append(where, clause), append(args, vals...)
	}
	if len(q.Projects) > 0 {
		where = append(where, "project IN ("+placeholders(len(q.Projects))+")")
		for _, p := range q.Projects {
			args = append(args, p)
		}
	}
	if len(q.ActorsNot) > 0 {
		where = append(where, "actor NOT IN ("+placeholders(len(q.ActorsNot))+")")
		for _, a := range q.ActorsNot {
			args = append(args, a)
		}
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT seq,id,kind,project,op,revision,actor,batch_id,at FROM changes WHERE `+strings.Join(where, " AND ")+` ORDER BY seq LIMIT ?`, args...)
	if err != nil {
		return ChangePage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Change
		var batch sql.NullString
		var at string
		if err := rows.Scan(&c.Seq, &c.ID, &c.Kind, &c.Project, &c.Op, &c.Revision, &c.Actor, &batch, &at); err != nil {
			return ChangePage{}, err
		}
		c.BatchID = batch.String
		c.At, _ = time.Parse(time.RFC3339Nano, at)
		page.Items = append(page.Items, c)
		page.NextSeq = c.Seq
	}
	if err := rows.Err(); err != nil {
		return ChangePage{}, err
	}
	if len(page.Items) == 0 {
		page.NextSeq = q.Since
	}
	return page, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// globClause turns kind patterns such as "dock_*" into an OR of LIKE tests.
func globClause(column string, patterns []string) (string, []any) {
	var parts []string
	var args []any
	for _, p := range patterns {
		if p == "" || p == "*" {
			return "", nil
		}
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(p)
		parts = append(parts, column+` LIKE ? ESCAPE '\'`)
		args = append(args, strings.ReplaceAll(escaped, "*", "%"))
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

// ErrNotFound reports a missing record.
var ErrNotFound = errors.New("record not found")
