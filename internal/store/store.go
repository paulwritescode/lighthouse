package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, stmt := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA synchronous=NORMAL"} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure database: %w", err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS artifacts (
 id TEXT PRIMARY KEY,
 title TEXT NOT NULL,
 content TEXT NOT NULL,
 project TEXT NOT NULL DEFAULT '',
 agent TEXT NOT NULL DEFAULT 'unknown',
 kind TEXT NOT NULL DEFAULT 'result',
 content_hash TEXT NOT NULL,
 metadata_json TEXT NOT NULL DEFAULT '{}',
 tags_json TEXT NOT NULL DEFAULT '[]',
 created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_artifacts_project_created ON artifacts(project, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_artifacts_created ON artifacts(created_at DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS artifacts_fts USING fts5(artifact_id UNINDEXED, title, content, project, agent);
CREATE TABLE IF NOT EXISTS memory_items (
 id TEXT PRIMARY KEY,
 memory_key TEXT NOT NULL,
 body TEXT NOT NULL,
 scope_key TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL DEFAULT 'fact',
 source_id TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1,
 status TEXT NOT NULL DEFAULT 'active',
 tags_json TEXT NOT NULL DEFAULT '[]',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(scope_key, memory_key)
);
CREATE INDEX IF NOT EXISTS idx_memory_scope_updated ON memory_items(scope_key, status, updated_at DESC);
CREATE TABLE IF NOT EXISTS scope_revisions (
 scope_key TEXT PRIMARY KEY,
 revision INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS context_snapshots (
 id TEXT PRIMARY KEY,
 session_key TEXT NOT NULL DEFAULT '',
 scope_key TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL,
 context_text TEXT NOT NULL,
 source_ids_json TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_session ON context_snapshots(session_key, created_at DESC);
CREATE TABLE IF NOT EXISTS embeddings (
 artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
 model TEXT NOT NULL,
 dimensions INTEGER NOT NULL,
 vector_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(artifact_id, model)
);
CREATE INDEX IF NOT EXISTS idx_embeddings_model_dimensions ON embeddings(model, dimensions);
CREATE TABLE IF NOT EXISTS change_events (
 id TEXT PRIMARY KEY,
 scope_key TEXT NOT NULL DEFAULT '',
 target_id TEXT NOT NULL,
 action TEXT NOT NULL,
 revision INTEGER NOT NULL,
 summary TEXT NOT NULL,
	created_at TEXT NOT NULL,
	actor TEXT NOT NULL DEFAULT 'agent',
	sensitive INTEGER NOT NULL DEFAULT 0
);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	for _, column := range []struct{ name, ddl string }{{"actor", "ALTER TABLE change_events ADD COLUMN actor TEXT NOT NULL DEFAULT 'agent'"}, {"sensitive", "ALTER TABLE change_events ADD COLUMN sensitive INTEGER NOT NULL DEFAULT 0"}} {
		if err := ensureColumn(ctx, s.db, "change_events", column.name, column.ddl); err != nil {
			return err
		}
	}
	for _, col := range []struct{ table, ddl string }{
		{"artifacts", "ALTER TABLE artifacts ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]'"},
		{"memory_items", "ALTER TABLE memory_items ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]'"},
	} {
		if err := ensureColumn(ctx, s.db, col.table, "tags_json", col.ddl); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeTags uses lowercase kebab-case keys consistently for agent and dashboard writes.
func NormalizeTags(primary string, secondary []string) (string, []string, error) {
	normalize := func(raw string) (string, error) {
		raw = strings.ToLower(strings.TrimSpace(raw))
		var out strings.Builder
		dash := false
		for _, r := range raw {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				if dash && out.Len() > 0 {
					out.WriteByte('-')
				}
				dash = false
				out.WriteRune(r)
			} else if r == ' ' || r == '_' || r == '-' || r == '.' || r == '/' {
				dash = true
			} else {
				return "", fmt.Errorf("tag %q: use letters, numbers, spaces, underscores, hyphens, dots, or slashes", raw)
			}
		}
		if out.Len() > 64 {
			return "", errors.New("tag exceeds 64 characters")
		}
		return out.String(), nil
	}
	primary, err := normalize(primary)
	if err != nil {
		return "", nil, err
	}
	if len(secondary) > 20 {
		return "", nil, errors.New("use at most 20 secondary tags")
	}
	if primary == "" && len(secondary) > 0 {
		return "", nil, errors.New("set a primary tag before adding secondary tags")
	}
	seen := map[string]bool{primary: true}
	tags := make([]string, 0, len(secondary))
	for _, raw := range secondary {
		tag, err := normalize(raw)
		if err != nil {
			return "", nil, err
		}
		if tag != "" && !seen[tag] {
			tags = append(tags, tag)
			seen[tag] = true
		}
	}
	return primary, tags, nil
}
func encodeTags(tags []string) string {
	b, _ := json.Marshal(tags)
	if tags == nil {
		return "[]"
	}
	return string(b)
}
func decodeTags(raw string) []string {
	var tags []string
	_ = json.Unmarshal([]byte(raw), &tags)
	return tags
}

func ensureColumn(ctx context.Context, db *sql.DB, table, name, ddl string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var col, typ string
		var def any
		if err := rows.Scan(&cid, &col, &typ, &notnull, &def, &pk); err != nil {
			return err
		}
		if col == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("migrate %s.%s: %w", table, name, err)
	}
	return nil
}

func (s *Store) SaveArtifact(ctx context.Context, title, content, project, agent, kind string) (Artifact, error) {
	return s.SaveArtifactFields(ctx, title, content, project, agent, kind, nil)
}

func (s *Store) SaveArtifactFields(ctx context.Context, title, content, project, agent, kind string, fields map[string]any, actors ...string) (Artifact, error) {
	return s.SaveArtifactTagged(ctx, title, content, project, agent, kind, fields, nil, actors...)
}

func (s *Store) SaveArtifactTagged(ctx context.Context, title, content, project, agent, kind string, fields map[string]any, tags []string, actors ...string) (Artifact, error) {
	var err error
	project, tags, err = NormalizeTags(project, tags)
	if err != nil {
		return Artifact{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return Artifact{}, errors.New("title is required")
	}
	if strings.TrimSpace(content) == "" {
		return Artifact{}, errors.New("content is required")
	}
	if len(content) > 2<<20 {
		return Artifact{}, errors.New("content exceeds the 2 MiB limit")
	}
	if agent == "" {
		agent = "unknown"
	}
	if kind == "" {
		kind = "result"
	}
	now := time.Now().UTC()
	id, err := newID()
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.Sum256([]byte(content))
	if err := validateFields(fields); err != nil {
		return Artifact{}, err
	}
	encodedFields, err := json.Marshal(fields)
	if err != nil {
		return Artifact{}, err
	}
	a := Artifact{ID: id, Title: title, Content: content, Project: project, Agent: agent, Kind: kind, CreatedAt: now, ContentHash: hex.EncodeToString(hash[:]), Fields: fields, Tags: tags}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Title, a.Content, a.Project, a.Agent, a.Kind, a.ContentHash, string(encodedFields), encodeTags(tags), now.Format(time.RFC3339Nano))
	if err != nil {
		return Artifact{}, fmt.Errorf("save artifact: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifacts_fts(artifact_id,title,content,project,agent) VALUES(?,?,?,?,?)`, a.ID, a.Title, a.Content+" "+string(encodedFields), a.Project, a.Agent)
	if err != nil {
		return Artifact{}, fmt.Errorf("index artifact: %w", err)
	}
	revision, err := bumpRevision(ctx, tx, project)
	if err != nil {
		return Artifact{}, err
	}
	actor := "agent"
	if len(actors) > 0 && actors[0] != "" {
		actor = actors[0]
	}
	if err = logChange(ctx, tx, project, a.ID, "artifact.saved", revision, "Saved "+a.Title, actor, actor == "dashboard"); err != nil {
		return Artifact{}, err
	}
	if err = tx.Commit(); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func (s *Store) ImportArtifacts(ctx context.Context, artifacts []Artifact) (int, error) {
	created := 0
	for _, a := range artifacts {
		if a.ID != "" {
			var found int
			err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts WHERE id=?`, a.ID).Scan(&found)
			if err != nil {
				return created, err
			}
			if found > 0 {
				continue
			}
		}
		if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Content) == "" {
			return created, errors.New("imported artifacts require a title and content")
		}
		if len(a.Content) > 2<<20 {
			return created, fmt.Errorf("imported artifact %q exceeds the 2 MiB content limit", a.Title)
		}
		if a.Agent == "" {
			a.Agent = "import"
		}
		if a.Kind == "" {
			a.Kind = "result"
		}
		if a.CreatedAt.IsZero() {
			a.CreatedAt = time.Now().UTC()
		}
		if err := validateFields(a.Fields); err != nil {
			return created, err
		}
		fields, _ := json.Marshal(a.Fields)
		if a.ID == "" {
			id, err := newID()
			if err != nil {
				return created, err
			}
			a.ID = id
		}
		hash := sha256.Sum256([]byte(a.Content))
		a.ContentHash = hex.EncodeToString(hash[:])
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return created, err
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifacts(id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Title, a.Content, a.Project, a.Agent, a.Kind, a.ContentHash, string(fields), encodeTags(a.Tags), a.CreatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			tx.Rollback()
			return created, err
		}
		var inserted int
		resultErr := tx.QueryRowContext(ctx, `SELECT changes()`).Scan(&inserted)
		if resultErr != nil {
			tx.Rollback()
			return created, resultErr
		}
		if inserted == 0 {
			tx.Rollback()
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artifacts_fts(artifact_id,title,content,project,agent) VALUES(?,?,?,?,?)`, a.ID, a.Title, a.Content+" "+string(fields), a.Project, a.Agent); err != nil {
			tx.Rollback()
			return created, err
		}
		rev, err := bumpRevision(ctx, tx, a.Project)
		if err != nil {
			tx.Rollback()
			return created, err
		}
		if err = logChange(ctx, tx, a.Project, a.ID, "artifact.imported", rev, "Imported "+a.Title, "import", true); err != nil {
			tx.Rollback()
			return created, err
		}
		if err = tx.Commit(); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func (s *Store) ImportMemories(ctx context.Context, memories []Memory) (int, error) {
	created := 0
	for _, m := range memories {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_items WHERE scope_key=? AND memory_key=? AND status='active'`, m.Project, m.Key).Scan(&count); err != nil {
			return created, err
		}
		if count > 0 {
			continue
		}
		if strings.TrimSpace(m.Key) == "" || strings.TrimSpace(m.Body) == "" {
			continue
		}
		if _, err := s.SaveMemoryTagged(ctx, m.Key, m.Body, m.Project, m.Kind, m.SourceID, m.Tags, "import"); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func (s *Store) BackupTo(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("backup path is required")
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("backup destination already exists")
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

func hasColumn(ctx context.Context, db *sql.DB, table, name string) bool {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var col, typ string
		var def any
		if rows.Scan(&cid, &col, &typ, &notnull, &def, &pk) == nil && col == name {
			return true
		}
	}
	return false
}

func ReadDatabase(ctx context.Context, path string) ([]Artifact, []Memory, error) {
	uri := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	var artifacts []Artifact
	artifactTagsColumn := "'[]'"
	if hasColumn(ctx, db, "artifacts", "tags_json") {
		artifactTagsColumn = "tags_json"
	}
	rows, err := db.QueryContext(ctx, `SELECT id,title,content,project,agent,kind,content_hash,metadata_json,`+artifactTagsColumn+`,created_at FROM artifacts ORDER BY created_at`)
	if err != nil {
		return nil, nil, fmt.Errorf("read imported database artifacts: %w", err)
	}
	for rows.Next() {
		var a Artifact
		var metadata, tagsJSON, created string
		if err = rows.Scan(&a.ID, &a.Title, &a.Content, &a.Project, &a.Agent, &a.Kind, &a.ContentHash, &metadata, &tagsJSON, &created); err != nil {
			rows.Close()
			return nil, nil, err
		}
		a.Fields = decodeFields(metadata)
		a.Tags = decodeTags(tagsJSON)
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		artifacts = append(artifacts, a)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	var memories []Memory
	memoryTagsColumn := "'[]'"
	if hasColumn(ctx, db, "memory_items", "tags_json") {
		memoryTagsColumn = "tags_json"
	}
	rows, err = db.QueryContext(ctx, `SELECT id,memory_key,body,scope_key,kind,source_id,revision,`+memoryTagsColumn+`,created_at,updated_at FROM memory_items WHERE status='active' ORDER BY created_at`)
	if err != nil {
		return nil, nil, fmt.Errorf("read imported database memory: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m Memory
		var c, u, tagsJSON string
		if err = rows.Scan(&m.ID, &m.Key, &m.Body, &m.Project, &m.Kind, &m.SourceID, &m.Revision, &tagsJSON, &c, &u); err != nil {
			return nil, nil, err
		}
		m.Tags = decodeTags(tagsJSON)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		memories = append(memories, m)
	}
	return artifacts, memories, rows.Err()
}

func (s *Store) GetArtifact(ctx context.Context, id string) (Artifact, error) {
	var a Artifact
	var created, metadata, tagsJSON string
	err := s.db.QueryRowContext(ctx, `SELECT id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at FROM artifacts WHERE id=?`, id).
		Scan(&a.ID, &a.Title, &a.Content, &a.Project, &a.Agent, &a.Kind, &a.ContentHash, &metadata, &tagsJSON, &created)
	if err != nil {
		return Artifact{}, err
	}
	a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		a.Fields = decodeFields(metadata)
		a.Tags = decodeTags(tagsJSON)
	}
	return a, err
}

func decodeFields(raw string) map[string]any {
	fields := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &fields)
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func validateFields(fields map[string]any) error {
	if len(fields) > 100 {
		return errors.New("document fields exceed the 100-field limit")
	}
	for key := range fields {
		if strings.TrimSpace(key) == "" || len(key) > 128 {
			return errors.New("field names must be 1–128 characters")
		}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("document fields must be JSON-compatible: %w", err)
	}
	if len(b) > 2<<20 {
		return errors.New("document fields exceed the 2 MiB limit")
	}
	return nil
}

// UpdateArtifact applies known document attributes and merges arbitrary fields
// into metadata_json. Unknown field names are preserved as document data.
func (s *Store) UpdateArtifact(ctx context.Context, id string, patch map[string]any, actor string) (Artifact, error) {
	a, err := s.GetArtifact(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	oldProject := a.Project
	if a.Fields == nil {
		a.Fields = map[string]any{}
	}
	var changed, sensitive bool
	for key, value := range patch {
		switch key {
		case "title":
			v, ok := value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return Artifact{}, errors.New("title must be a non-empty string")
			}
			a.Title = strings.TrimSpace(v)
			changed = true
		case "content":
			v, ok := value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return Artifact{}, errors.New("content must be a non-empty string")
			}
			if len(v) > 2<<20 {
				return Artifact{}, errors.New("content exceeds the 2 MiB limit")
			}
			a.Content = v
			changed = true
			sensitive = true
		case "project":
			v, ok := value.(string)
			if !ok {
				return Artifact{}, errors.New("project must be a string")
			}
			if v != a.Project {
				sensitive = true
			}
			a.Project = v
			changed = true
		case "tags":
			raw, ok := value.([]string)
			if !ok {
				if values, yes := value.([]any); yes {
					raw = make([]string, 0, len(values))
					for _, item := range values {
						tag, yes := item.(string)
						if !yes {
							return Artifact{}, errors.New("tags must contain only strings")
						}
						raw = append(raw, tag)
					}
				} else {
					return Artifact{}, errors.New("tags must be a list of strings")
				}
			}
			_, normalized, err := NormalizeTags(a.Project, raw)
			if err != nil {
				return Artifact{}, err
			}
			a.Tags = normalized
			changed = true
			sensitive = true
		case "agent":
			v, ok := value.(string)
			if !ok {
				return Artifact{}, errors.New("agent must be a string")
			}
			a.Agent = v
			changed = true
		case "kind":
			v, ok := value.(string)
			if !ok {
				return Artifact{}, errors.New("kind must be a string")
			}
			a.Kind = v
			changed = true
		default:
			if value == nil {
				delete(a.Fields, key)
			} else {
				a.Fields[key] = value
			}
			changed = true
			sensitive = true
		}
	}
	if !changed {
		return Artifact{}, errors.New("update contains no fields")
	}
	if err := validateFields(a.Fields); err != nil {
		return Artifact{}, err
	}
	a.Project, a.Tags, err = NormalizeTags(a.Project, a.Tags)
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.Sum256([]byte(a.Content))
	a.ContentHash = hex.EncodeToString(hash[:])
	metadata, err := json.Marshal(a.Fields)
	if err != nil {
		return Artifact{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Artifact{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE artifacts SET title=?,content=?,project=?,agent=?,kind=?,content_hash=?,metadata_json=?,tags_json=? WHERE id=?`, a.Title, a.Content, a.Project, a.Agent, a.Kind, a.ContentHash, string(metadata), encodeTags(a.Tags), id)
	if err != nil {
		return Artifact{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artifacts_fts WHERE artifact_id=?`, id); err != nil {
		return Artifact{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artifacts_fts(artifact_id,title,content,project,agent) VALUES(?,?,?,?,?)`, id, a.Title, a.Content+" "+string(metadata), a.Project, a.Agent); err != nil {
		return Artifact{}, err
	}
	if oldProject != a.Project {
		if _, err = bumpRevision(ctx, tx, oldProject); err != nil {
			return Artifact{}, err
		}
	}
	revision, err := bumpRevision(ctx, tx, a.Project)
	if err != nil {
		return Artifact{}, err
	}
	if actor == "" {
		actor = "agent"
	}
	if err = logChange(ctx, tx, a.Project, id, "artifact.updated", revision, "Updated "+a.Title, actor, sensitive); err != nil {
		return Artifact{}, err
	}
	if err = tx.Commit(); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func (s *Store) Search(ctx context.Context, query, project string, limit int) ([]Artifact, error) {
	return s.SearchSorted(ctx, query, project, limit, "newest")
}

func artifactOrder(sortKey, prefix string) string {
	switch sortKey {
	case "title":
		return prefix + "title COLLATE NOCASE ASC, " + prefix + "created_at DESC"
	case "tag":
		return prefix + "project COLLATE NOCASE ASC, " + prefix + "created_at DESC"
	default:
		return prefix + "created_at DESC"
	}
}

func (s *Store) SearchSorted(ctx context.Context, query, project string, limit int, sortKey string) ([]Artifact, error) {
	limit = clampLimit(limit, 50)
	if strings.TrimSpace(query) == "" {
		return s.listSorted(ctx, project, limit, sortKey)
	}
	phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.db.QueryContext(ctx, `
SELECT a.id,a.title,a.content,a.project,a.agent,a.kind,a.content_hash,a.metadata_json,a.tags_json,a.created_at
FROM artifacts_fts f JOIN artifacts a ON a.id=f.artifact_id
WHERE artifacts_fts MATCH ? AND (?='' OR a.project=? OR EXISTS (SELECT 1 FROM json_each(a.tags_json) WHERE value=?))
ORDER BY `+artifactOrder(sortKey, "a.")+` LIMIT ?`, phrase, project, project, project, limit)
	if err == nil {
		defer rows.Close()
		items, scanErr := scanArtifacts(rows)
		if scanErr == nil {
			return items, nil
		}
	}
	// Invalid FTS syntax or a query with punctuation still gets a safe literal fallback.
	like := literalLikePattern(query)
	return s.listLikeSorted(ctx, like, project, limit, sortKey)
}

// ArtifactIDs returns IDs matching the same project and full-text filters as
// Search. It intentionally omits document bodies for dashboard bulk actions.
func (s *Store) ArtifactIDs(ctx context.Context, query, project string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return s.artifactIDsLike(ctx, "", project)
	}
	phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := s.db.QueryContext(ctx, `SELECT a.id FROM artifacts_fts f JOIN artifacts a ON a.id=f.artifact_id WHERE artifacts_fts MATCH ? AND (?='' OR a.project=? OR EXISTS (SELECT 1 FROM json_each(a.tags_json) WHERE value=?)) ORDER BY a.created_at DESC`, phrase, project, project, project)
	if err == nil {
		ids, scanErr := scanIDs(rows)
		_ = rows.Close()
		if scanErr == nil {
			return ids, nil
		}
	}
	like := literalLikePattern(query)
	return s.artifactIDsLike(ctx, like, project)
}

func (s *Store) artifactIDsLike(ctx context.Context, query, project string) ([]string, error) {
	var rows *sql.Rows
	var err error
	if query == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT id FROM artifacts WHERE (?='' OR project=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY created_at DESC`, project, project, project)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT id FROM artifacts WHERE (title LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\' OR metadata_json LIKE ? ESCAPE '\') AND (?='' OR project=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY created_at DESC`, query, query, query, project, project, project)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDs(rows)
}

func literalLikePattern(query string) string {
	escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query)
	return "%" + escaped + "%"
}

func scanIDs(rows *sql.Rows) ([]string, error) {
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) list(ctx context.Context, query, project string, limit int) ([]Artifact, error) {
	return s.listSorted(ctx, project, limit, "newest")
}
func (s *Store) listSorted(ctx context.Context, project string, limit int, sortKey string) ([]Artifact, error) {
	sqlText := `SELECT id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at FROM artifacts WHERE (?='' OR project=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY ` + artifactOrder(sortKey, "") + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlText, project, project, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
}
func (s *Store) listLikeSorted(ctx context.Context, query, project string, limit int, sortKey string) ([]Artifact, error) {
	sqlText := `SELECT id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at FROM artifacts WHERE (title LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\' OR metadata_json LIKE ? ESCAPE '\') AND (?='' OR project=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY ` + artifactOrder(sortKey, "") + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlText, query, query, query, project, project, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

func (s *Store) Recent(ctx context.Context, project string, limit int) ([]Artifact, error) {
	return s.list(ctx, "", project, clampLimit(limit, 50))
}

func (s *Store) AllArtifacts(ctx context.Context) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at FROM artifacts ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var metadata, tagsJSON, created string
		if err := rows.Scan(&a.ID, &a.Title, &a.Content, &a.Project, &a.Agent, &a.Kind, &a.ContentHash, &metadata, &tagsJSON, &created); err != nil {
			return nil, err
		}
		a.Fields = decodeFields(metadata)
		a.Tags = decodeTags(tagsJSON)
		a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AllMemory(ctx context.Context) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,memory_key,body,scope_key,kind,source_id,revision,tags_json,created_at,updated_at FROM memory_items WHERE status='active' ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var c, u, tagsJSON string
		if err := rows.Scan(&m.ID, &m.Key, &m.Body, &m.Project, &m.Kind, &m.SourceID, &m.Revision, &tagsJSON, &c, &u); err != nil {
			return nil, err
		}
		m.Tags = decodeTags(tagsJSON)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanArtifacts(rows *sql.Rows) ([]Artifact, error) {
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var created, metadata, tagsJSON string
		if err := rows.Scan(&a.ID, &a.Title, &a.Content, &a.Project, &a.Agent, &a.Kind, &a.ContentHash, &metadata, &tagsJSON, &created); err != nil {
			return nil, err
		}
		var err error
		a.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		a.Fields = decodeFields(metadata)
		a.Tags = decodeTags(tagsJSON)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SaveMemory(ctx context.Context, key, body, project, kind, sourceID string, actors ...string) (Memory, error) {
	return s.SaveMemoryTagged(ctx, key, body, project, kind, sourceID, nil, actors...)
}

func (s *Store) SaveMemoryTagged(ctx context.Context, key, body, project, kind, sourceID string, tags []string, actors ...string) (Memory, error) {
	tagsProvided := tags != nil
	var err error
	project, tags, err = NormalizeTags(project, tags)
	if err != nil {
		return Memory{}, err
	}
	key = strings.TrimSpace(key)
	body = strings.TrimSpace(body)
	if key == "" || body == "" {
		return Memory{}, errors.New("memory key and body are required")
	}
	if len(body) > 32<<10 {
		return Memory{}, errors.New("memory body exceeds the 32 KiB limit")
	}
	if kind == "" {
		kind = "fact"
	}
	now := time.Now().UTC()
	id, err := newID()
	if err != nil {
		return Memory{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, err
	}
	defer tx.Rollback()
	var existsID string
	var oldRev int64
	err = tx.QueryRowContext(ctx, `SELECT id,revision FROM memory_items WHERE scope_key=? AND memory_key=?`, project, key).Scan(&existsID, &oldRev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Memory{}, err
	}
	if err == nil {
		if !tagsProvided {
			var stored string
			if err := tx.QueryRowContext(ctx, `SELECT tags_json FROM memory_items WHERE id=?`, existsID).Scan(&stored); err != nil {
				return Memory{}, err
			}
			tags = decodeTags(stored)
		}
		id = existsID
		oldRev++
	} else {
		oldRev = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_items(id,memory_key,body,scope_key,kind,source_id,revision,status,tags_json,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,'active',?,?,?) ON CONFLICT(scope_key,memory_key) DO UPDATE SET body=excluded.body,kind=excluded.kind,source_id=excluded.source_id,revision=excluded.revision,status='active',tags_json=excluded.tags_json,updated_at=excluded.updated_at`,
		id, key, body, project, kind, sourceID, oldRev, encodeTags(tags), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Memory{}, err
	}
	revision, err := bumpRevision(ctx, tx, project)
	if err != nil {
		return Memory{}, err
	}
	actor := "agent"
	if len(actors) > 0 && actors[0] != "" {
		actor = actors[0]
	}
	if err = logChange(ctx, tx, project, id, "memory.updated", revision, "Updated memory: "+key, actor, true); err != nil {
		return Memory{}, err
	}
	if err = tx.Commit(); err != nil {
		return Memory{}, err
	}
	return Memory{ID: id, Key: key, Body: body, Project: project, Kind: kind, SourceID: sourceID, Tags: tags, Revision: oldRev, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ListMemory(ctx context.Context, project string, limit int) ([]Memory, error) {
	return s.ListMemorySorted(ctx, project, limit, "newest")
}
func (s *Store) ListMemorySorted(ctx context.Context, project string, limit int, sortKey string) ([]Memory, error) {
	order := "updated_at DESC"
	if sortKey == "key" {
		order = "memory_key COLLATE NOCASE ASC, updated_at DESC"
	}
	if sortKey == "tag" {
		order = "scope_key COLLATE NOCASE ASC, updated_at DESC"
	}
	sqlText := `SELECT id,memory_key,body,scope_key,kind,source_id,revision,tags_json,created_at,updated_at FROM memory_items WHERE status='active' AND (?='' OR scope_key=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY ` + order + ` LIMIT ?`
	rows, err := s.db.QueryContext(ctx, sqlText, project, project, project, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		var c, u, tagsJSON string
		if err := rows.Scan(&m.ID, &m.Key, &m.Body, &m.Project, &m.Kind, &m.SourceID, &m.Revision, &tagsJSON, &c, &u); err != nil {
			return nil, err
		}
		m.Tags = decodeTags(tagsJSON)
		m.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMemory(ctx context.Context, id string) (Memory, error) {
	var m Memory
	var created, updated, tagsJSON string
	err := s.db.QueryRowContext(ctx, `SELECT id,memory_key,body,scope_key,kind,source_id,revision,tags_json,created_at,updated_at FROM memory_items WHERE id=? AND status='active'`, id).
		Scan(&m.ID, &m.Key, &m.Body, &m.Project, &m.Kind, &m.SourceID, &m.Revision, &tagsJSON, &created, &updated)
	if err != nil {
		return Memory{}, err
	}
	m.Tags = decodeTags(tagsJSON)
	m.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Memory{}, err
	}
	m.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return m, err
}

// MemoryIDs returns IDs for active memory items matching a project filter.
// An empty filter includes all scopes; a project filter includes global items.
func (s *Store) MemoryIDs(ctx context.Context, project string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM memory_items WHERE status='active' AND (?='' OR scope_key=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY updated_at DESC`, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIDs(rows)
}

func (s *Store) UpdateMemory(ctx context.Context, id, key, body, project, kind, sourceID, actor string) error {
	old, err := s.GetMemory(ctx, id)
	if err != nil {
		return err
	}
	return s.UpdateMemoryTagged(ctx, id, key, body, project, kind, sourceID, old.Tags, actor)
}

func (s *Store) UpdateMemoryTagged(ctx context.Context, id, key, body, project, kind, sourceID string, tags []string, actor string) error {
	var err error
	project, tags, err = NormalizeTags(project, tags)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	body = strings.TrimSpace(body)
	if key == "" || body == "" {
		return errors.New("memory key and body are required")
	}
	if len(body) > 32<<10 {
		return errors.New("memory body exceeds the 32 KiB limit")
	}
	if kind == "" {
		kind = "fact"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldProject string
	if err = tx.QueryRowContext(ctx, `SELECT scope_key FROM memory_items WHERE id=? AND status='active'`, id).Scan(&oldProject); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory_items SET memory_key=?,body=?,scope_key=?,kind=?,source_id=?,revision=revision+1,tags_json=?,updated_at=? WHERE id=?`, key, body, project, kind, sourceID, encodeTags(tags), time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	oldRev, err := bumpRevision(ctx, tx, oldProject)
	if err != nil {
		return err
	}
	if project != oldProject {
		if _, err = bumpRevision(ctx, tx, project); err != nil {
			return err
		}
	}
	if actor == "" {
		actor = "dashboard"
	}
	if err = logChange(ctx, tx, oldProject, id, "memory.updated", oldRev, "Updated memory: "+key, actor, true); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadContext(ctx context.Context, session, project string, limit int) (ContextPack, error) {
	limit = clampLimit(limit, 20)
	memories, err := s.ListMemory(ctx, project, limit)
	if err != nil {
		return ContextPack{}, err
	}
	artifacts, err := s.Recent(ctx, project, limit)
	if err != nil {
		return ContextPack{}, err
	}
	var revision int64
	err = s.db.QueryRowContext(ctx, `SELECT revision FROM scope_revisions WHERE scope_key=?`, project).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		revision = 0
		err = nil
	}
	if err != nil {
		return ContextPack{}, err
	}
	id, err := newID()
	if err != nil {
		return ContextPack{}, err
	}
	var b strings.Builder
	b.WriteString("# Lighthouse context\n")
	if project != "" {
		fmt.Fprintf(&b, "Project: %s\n", project)
	}
	fmt.Fprintf(&b, "Memory revision: %d\n\n", revision)
	if len(memories) > 0 {
		b.WriteString("## Durable memory\n")
		for _, m := range memories {
			fmt.Fprintf(&b, "- **%s** (%s, revision %d): %s", m.Key, m.Kind, m.Revision, m.Body)
			if m.SourceID != "" {
				fmt.Fprintf(&b, " [source: %s]", m.SourceID)
			}
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	if len(artifacts) > 0 {
		b.WriteString("## Recent agent results\n")
		for _, a := range artifacts {
			excerpt := a.Content
			if len(excerpt) > 1200 {
				excerpt = excerpt[:1200] + "…"
			}
			fmt.Fprintf(&b, "### %s\nSource: %s · %s · %s\n%s\n\n", a.Title, a.ID, a.Agent, a.CreatedAt.Format(time.RFC3339), excerpt)
		}
	}
	ids := make([]string, 0, len(artifacts)+len(memories))
	for _, a := range artifacts {
		ids = append(ids, a.ID)
	}
	for _, m := range memories {
		ids = append(ids, m.ID)
	}
	idsJSON, _ := json.Marshal(ids)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := b.String()
	_, err = s.db.ExecContext(ctx, `INSERT INTO context_snapshots(id,session_key,scope_key,revision,context_text,source_ids_json,created_at) VALUES(?,?,?,?,?,?,?)`, id, session, project, revision, body, string(idsJSON), now)
	if err != nil {
		return ContextPack{}, err
	}
	return ContextPack{SnapshotID: id, Session: session, Project: project, Revision: revision, Context: body, Artifacts: artifacts, Memories: memories}, nil
}

func (s *Store) PutEmbedding(ctx context.Context, artifactID, model string, vector []float32) error {
	if len(vector) < 2 || len(vector) > 8192 {
		return errors.New("embedding must have between 2 and 8192 dimensions")
	}
	if model == "" {
		return errors.New("embedding model is required")
	}
	if _, err := s.GetArtifact(ctx, artifactID); err != nil {
		return fmt.Errorf("find source artifact: %w", err)
	}
	encoded, err := json.Marshal(vector)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embeddings(artifact_id,model,dimensions,vector_json,created_at) VALUES(?,?,?,?,?) ON CONFLICT(artifact_id,model) DO UPDATE SET dimensions=excluded.dimensions,vector_json=excluded.vector_json,created_at=excluded.created_at`, artifactID, model, len(vector), string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) SearchVector(ctx context.Context, model, project string, vector []float32, limit int) ([]Artifact, error) {
	if len(vector) < 2 || len(vector) > 8192 {
		return nil, errors.New("query vector must have between 2 and 8192 dimensions")
	}
	q, err := json.Marshal(vector)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.title,a.content,a.project,a.agent,a.kind,a.content_hash,a.metadata_json,a.tags_json,a.created_at
FROM embeddings e JOIN artifacts a ON a.id=e.artifact_id
WHERE e.model=? AND e.dimensions=? AND (?='' OR a.project=? OR EXISTS (SELECT 1 FROM json_each(a.tags_json) WHERE value=?))
ORDER BY vec_distance_cos(e.vector_json, ?) ASC LIMIT ?`, model, len(vector), project, project, project, string(q), clampLimit(limit, 30))
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()
	return scanArtifacts(rows)
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	err := s.db.QueryRowContext(ctx, `SELECT
	(SELECT count(*) FROM artifacts),
	(SELECT count(*) FROM memory_items WHERE status='active'),
	(SELECT count(*) FROM (
        SELECT project AS name FROM artifacts WHERE project<>''
        UNION SELECT j.value FROM artifacts a, json_each(a.tags_json) j
        UNION SELECT scope_key FROM memory_items WHERE status='active' AND scope_key<>''
        UNION SELECT j.value FROM memory_items m, json_each(m.tags_json) j WHERE m.status='active'
    )),
	(SELECT count(*) FROM change_events WHERE created_at>=?),
	(SELECT count(*) FROM change_events),
	(SELECT count(*) FROM change_events WHERE sensitive=1)`, since).Scan(&c.Artifacts, &c.Memories, &c.Projects, &c.AddedThisWeek, &c.Changes, &c.SecurityFlags)
	return c, err
}

func (s *Store) ProjectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, SUM(results), SUM(memories) FROM (
		SELECT project AS name, count(*) AS results, 0 AS memories FROM artifacts WHERE project<>'' GROUP BY project
        UNION ALL
        SELECT j.value AS name, count(*) AS results, 0 AS memories FROM artifacts a, json_each(a.tags_json) j GROUP BY j.value
		UNION ALL
		SELECT scope_key AS name, 0 AS results, count(*) AS memories FROM memory_items WHERE status='active' AND scope_key<>'' GROUP BY scope_key
        UNION ALL
        SELECT j.value AS name, 0 AS results, count(*) AS memories FROM memory_items m, json_each(m.tags_json) j WHERE m.status='active' GROUP BY j.value
	) GROUP BY name ORDER BY SUM(results)+SUM(memories) DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list project summaries: %w", err)
	}
	defer rows.Close()
	var out []ProjectSummary
	var max int64
	for rows.Next() {
		var p ProjectSummary
		if err := rows.Scan(&p.Name, &p.Results, &p.Memories); err != nil {
			return nil, err
		}
		p.Total = p.Results + p.Memories
		if p.Total > max {
			max = p.Total
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].BarWidth = int(out[i].Total * 100 / max)
	}
	return out, nil
}

func (s *Store) RecentChanges(ctx context.Context, limit int) ([]ChangeEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,scope_key,target_id,action,revision,summary,actor,sensitive,created_at FROM change_events ORDER BY created_at DESC LIMIT ?`, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangeEvent
	for rows.Next() {
		var e ChangeEvent
		var sensitive int
		var created string
		if err := rows.Scan(&e.ID, &e.Scope, &e.TargetID, &e.Action, &e.Revision, &e.Summary, &e.Actor, &sensitive, &created); err != nil {
			return nil, err
		}
		e.Sensitive = sensitive != 0
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) WeeklyActivity(ctx context.Context) ([]ActivityDay, error) {
	counts := map[string]int{}
	rows, err := s.db.QueryContext(ctx, `SELECT substr(created_at,1,10),count(*) FROM change_events WHERE created_at>=? GROUP BY substr(created_at,1,10)`, time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			rows.Close()
			return nil, err
		}
		counts[day] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	max := 1
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	out := make([]ActivityDay, 7)
	today := time.Now().UTC()
	for i := 0; i < 7; i++ {
		d := today.AddDate(0, 0, i-6)
		label := d.Format("2006-01-02")
		n := counts[label]
		height := n * 54 / max
		if n > 0 && height < 3 {
			height = 3
		}
		out[i] = ActivityDay{Label: d.Format("Mon"), Count: n, X: 28 + i*74, Y: 68 - height}
	}
	return out, nil
}

func (s *Store) Describe(ctx context.Context, project string) (map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT metadata_json FROM artifacts WHERE (?='' OR project=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?)) ORDER BY created_at DESC LIMIT 500`, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fieldCounts := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		for key := range decodeFields(raw) {
			fieldCounts[key]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"collection": "artifacts", "project": project, "core_fields": []string{"id", "title", "content", "project", "agent", "kind", "created_at", "content_hash"}, "custom_fields": fieldCounts, "field_policy": "custom document fields are stored as JSON and can be added with save(fields=...) or update(id, fields=...)"}, nil
}

func bumpRevision(ctx context.Context, tx *sql.Tx, scope string) (int64, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO scope_revisions(scope_key,revision) VALUES(?,1) ON CONFLICT(scope_key) DO UPDATE SET revision=revision+1`, scope)
	if err != nil {
		return 0, err
	}
	var n int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM scope_revisions WHERE scope_key=?`, scope).Scan(&n)
	return n, err
}
func logChange(ctx context.Context, tx *sql.Tx, scope, target, action string, revision int64, summary, actor string, sensitive bool) error {
	id, err := newID()
	if err != nil {
		return err
	}
	if actor == "" {
		actor = "agent"
	}
	flag := 0
	if sensitive {
		flag = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO change_events(id,scope_key,target_id,action,revision,summary,created_at,actor,sensitive) VALUES(?,?,?,?,?,?,?,?,?)`, id, scope, target, action, revision, summary, time.Now().UTC().Format(time.RFC3339Nano), actor, flag)
	return err
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func clampLimit(n, max int) int {
	if n < 1 {
		return 10
	}
	if n > max {
		return max
	}
	return n
}
