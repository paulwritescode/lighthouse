package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Errors returned by write operations. Callers match them with errors.Is or
// errors.As.
var (
	ErrIDExists = errors.New("a different record already uses this id")
	ErrDeleted  = errors.New("record is deleted; restore it first")
)

// RevisionConflictError reports a failed if_revision check and carries the
// current record so the caller can merge and retry.
type RevisionConflictError struct{ Current Artifact }

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("revision conflict: record %s is at revision %d", e.Current.ID, e.Current.Revision)
}

var callerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{7,63}$`)

// WriteOptions identify who made a write and which batch it belongs to.
type WriteOptions struct {
	Actor   string
	BatchID string
}

// SaveInput is a new record. ID is optional; when set it must be unique or
// identical to the existing record, which makes retries idempotent.
type SaveInput struct {
	ID        string
	Title     string
	Content   string
	Project   string
	Agent     string
	Kind      string
	Fields    map[string]any
	Tags      []string
	CreatedAt time.Time
}

// UpdateOptions extend WriteOptions with compare-and-set and merge control.
type UpdateOptions struct {
	WriteOptions
	IfRevision *int64
	Merge      string // "shallow" (default), "deep" or "replace"
	Unset      []string
}

// WriteResult is a committed record plus any schema warnings. Unchanged is
// true when the write matched the stored record and nothing was written.
type WriteResult struct {
	Artifact  Artifact `json:"artifact"`
	Warnings  []string `json:"warnings,omitempty"`
	Unchanged bool     `json:"unchanged,omitempty"`
}

func actorOr(o WriteOptions, fallback string) string {
	if o.Actor != "" {
		return o.Actor
	}
	return fallback
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func hashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func getTx(ctx context.Context, q queryRower, id string) (Artifact, error) {
	a, err := scanArtifact(q.QueryRowContext(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return a, err
}

func canonicalState(a Artifact) string {
	fields, tags := a.Fields, a.Tags
	if fields == nil {
		fields = map[string]any{}
	}
	if len(tags) == 0 {
		tags = []string{}
	}
	b, _ := json.Marshal([]any{a.Title, a.Content, a.Project, a.Agent, a.Kind, tags, normalizeJSON(fields)})
	return string(b)
}

func ftsReplace(ctx context.Context, tx *sql.Tx, a Artifact) error {
	metadata, err := json.Marshal(a.Fields)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artifacts_fts WHERE artifact_id=?`, a.ID); err != nil {
		return err
	}
	if a.DeletedAt != nil {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifacts_fts(artifact_id,title,content,project,agent) VALUES(?,?,?,?,?)`, a.ID, a.Title, a.Content+" "+string(metadata), a.Project, a.Agent)
	return err
}

func snapshotTx(ctx context.Context, tx *sql.Tx, a Artifact, batchID string) error {
	if batchID == "" {
		return nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO record_history(id,revision,snapshot_json,batch_id,at) VALUES(?,?,?,?,?)`, a.ID, a.Revision, string(b), batchID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// saveTx inserts one record inside an open transaction. Never query s.db
// while a transaction is open: the pool holds a single connection.
func (s *Store) saveTx(ctx context.Context, tx *sql.Tx, in SaveInput, o WriteOptions) (WriteResult, error) {
	project, tags, err := NormalizeTags(in.Project, in.Tags)
	if err != nil {
		return WriteResult{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return WriteResult{}, errors.New("title is required")
	}
	if strings.TrimSpace(in.Content) == "" {
		return WriteResult{}, errors.New("content is required")
	}
	if len(in.Content) > 2<<20 {
		return WriteResult{}, errors.New("content exceeds the 2 MiB limit")
	}
	agent, kind := in.Agent, in.Kind
	if agent == "" {
		agent = "unknown"
	}
	if kind == "" {
		kind = "result"
	}
	if err := validateFields(in.Fields); err != nil {
		return WriteResult{}, err
	}
	warnings, schemaVersion, err := checkFields(ctx, tx, kind, in.Fields)
	if err != nil {
		return WriteResult{}, err
	}
	now := time.Now().UTC()
	if !in.CreatedAt.IsZero() {
		now = in.CreatedAt.UTC()
	}
	id := in.ID
	if id != "" {
		if !callerIDPattern.MatchString(id) {
			return WriteResult{}, errors.New("id must be 8 to 64 lowercase letters, digits, hyphens or underscores")
		}
		existing, gerr := getTx(ctx, tx, id)
		if gerr == nil {
			candidate := Artifact{Title: title, Content: in.Content, Project: project, Agent: agent, Kind: kind, Tags: tags, Fields: in.Fields}
			if canonicalState(existing) == canonicalState(candidate) {
				return WriteResult{Artifact: existing, Warnings: warnings, Unchanged: true}, nil
			}
			return WriteResult{}, ErrIDExists
		}
		if !errors.Is(gerr, ErrNotFound) {
			return WriteResult{}, gerr
		}
	} else if id, err = newID(); err != nil {
		return WriteResult{}, err
	}
	encoded, err := json.Marshal(in.Fields)
	if err != nil {
		return WriteResult{}, err
	}
	actor := actorOr(o, "agent")
	seq, err := recordChange(ctx, tx, id, kind, project, "create", 1, actor, o.BatchID)
	if err != nil {
		return WriteResult{}, err
	}
	a := Artifact{ID: id, Title: title, Content: in.Content, Project: project, Agent: agent, Kind: kind, CreatedAt: now, UpdatedAt: now, ContentHash: hashContent(in.Content),
		Fields: in.Fields, Tags: tags, Revision: 1, Seq: seq, Actor: actor, BatchID: o.BatchID, SchemaVersion: schemaVersion}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at,updated_at,revision,seq,actor,batch_id,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Title, a.Content, a.Project, a.Agent, a.Kind, a.ContentHash, string(encoded), encodeTags(tags), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), 1, seq, actor, nullable(o.BatchID), schemaVersion)
	if err != nil {
		return WriteResult{}, fmt.Errorf("save artifact: %w", err)
	}
	if err = ftsReplace(ctx, tx, a); err != nil {
		return WriteResult{}, fmt.Errorf("index artifact: %w", err)
	}
	revision, err := bumpRevision(ctx, tx, project)
	if err != nil {
		return WriteResult{}, err
	}
	if err = logChange(ctx, tx, project, a.ID, "artifact.saved", revision, "Saved "+a.Title, actor, actor == "dashboard"); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Artifact: a, Warnings: warnings}, nil
}

func copyFields(f map[string]any) map[string]any {
	if f == nil {
		return map[string]any{}
	}
	out, _ := normalizeJSON(f).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

func deepMerge(dst, patch map[string]any) {
	for k, v := range patch {
		if v == nil {
			delete(dst, k)
			continue
		}
		pm, pok := v.(map[string]any)
		dm, dok := dst[k].(map[string]any)
		if pok && dok {
			deepMerge(dm, pm)
			continue
		}
		dst[k] = v
	}
}

func unsetPath(fields map[string]any, path string) {
	parts := strings.Split(path, ".")
	cur := fields
	for i, p := range parts {
		if i == len(parts)-1 {
			delete(cur, p)
			return
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

var coreKeys = map[string]bool{"title": true, "content": true, "project": true, "tags": true, "agent": true, "kind": true}

// updateTx applies a patch inside an open transaction.
func (s *Store) updateTx(ctx context.Context, tx *sql.Tx, id string, patch map[string]any, o UpdateOptions) (WriteResult, error) {
	a, err := getTx(ctx, tx, id)
	if err != nil {
		return WriteResult{}, err
	}
	if a.DeletedAt != nil {
		return WriteResult{}, ErrDeleted
	}
	if o.IfRevision != nil && *o.IfRevision != a.Revision {
		return WriteResult{}, &RevisionConflictError{Current: a}
	}
	if len(patch) == 0 && len(o.Unset) == 0 {
		return WriteResult{}, errors.New("update contains no fields")
	}
	mode := o.Merge
	if mode == "" {
		mode = "shallow"
	}
	if mode != "shallow" && mode != "deep" && mode != "replace" {
		return WriteResult{}, errors.New("merge must be shallow, deep or replace")
	}
	before := a
	before.Fields = copyFields(a.Fields)
	a.Fields = copyFields(a.Fields)
	oldProject := a.Project
	custom := map[string]any{}
	var sensitive bool
	for key, value := range patch {
		switch key {
		case "title":
			v, ok := value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return WriteResult{}, errors.New("title must be a non-empty string")
			}
			a.Title = strings.TrimSpace(v)
		case "content":
			v, ok := value.(string)
			if !ok || strings.TrimSpace(v) == "" {
				return WriteResult{}, errors.New("content must be a non-empty string")
			}
			if len(v) > 2<<20 {
				return WriteResult{}, errors.New("content exceeds the 2 MiB limit")
			}
			a.Content = v
			sensitive = true
		case "project":
			v, ok := value.(string)
			if !ok {
				return WriteResult{}, errors.New("project must be a string")
			}
			if v != a.Project {
				sensitive = true
			}
			a.Project = v
		case "tags":
			raw, ok := value.([]string)
			if !ok {
				values, yes := value.([]any)
				if !yes {
					return WriteResult{}, errors.New("tags must be a list of strings")
				}
				raw = make([]string, 0, len(values))
				for _, item := range values {
					tag, yes := item.(string)
					if !yes {
						return WriteResult{}, errors.New("tags must contain only strings")
					}
					raw = append(raw, tag)
				}
			}
			_, normalized, err := NormalizeTags(a.Project, raw)
			if err != nil {
				return WriteResult{}, err
			}
			a.Tags = normalized
			sensitive = true
		case "agent":
			v, ok := value.(string)
			if !ok {
				return WriteResult{}, errors.New("agent must be a string")
			}
			a.Agent = v
		case "kind":
			v, ok := value.(string)
			if !ok {
				return WriteResult{}, errors.New("kind must be a string")
			}
			a.Kind = v
		default:
			custom[key] = value
			sensitive = true
		}
	}
	switch mode {
	case "replace":
		if len(custom) > 0 || len(o.Unset) > 0 {
			a.Fields = map[string]any{}
			for k, v := range custom {
				if v != nil {
					a.Fields[k] = v
				}
			}
		}
	case "deep":
		deepMerge(a.Fields, custom)
	default:
		for k, v := range custom {
			if v == nil {
				delete(a.Fields, k)
			} else {
				a.Fields[k] = v
			}
		}
	}
	for _, path := range o.Unset {
		if !fieldPathPattern.MatchString(path) {
			return WriteResult{}, fmt.Errorf("invalid unset path %q", path)
		}
		unsetPath(a.Fields, path)
	}
	if len(a.Fields) == 0 {
		a.Fields = nil
	}
	if err := validateFields(a.Fields); err != nil {
		return WriteResult{}, err
	}
	a.Project, a.Tags, err = NormalizeTags(a.Project, a.Tags)
	if err != nil {
		return WriteResult{}, err
	}
	if canonicalState(before) == canonicalState(a) {
		return WriteResult{Artifact: before, Unchanged: true}, nil
	}
	warnings, schemaVersion, err := checkFields(ctx, tx, a.Kind, a.Fields)
	if err != nil {
		return WriteResult{}, err
	}
	if err = snapshotTx(ctx, tx, before, o.BatchID); err != nil {
		return WriteResult{}, err
	}
	now := time.Now().UTC()
	actor := actorOr(o.WriteOptions, "agent")
	a.Revision = before.Revision + 1
	a.UpdatedAt = now
	a.Actor = actor
	a.BatchID = o.BatchID
	a.SchemaVersion = schemaVersion
	a.ContentHash = hashContent(a.Content)
	if a.Seq, err = recordChange(ctx, tx, id, a.Kind, a.Project, "update", a.Revision, actor, o.BatchID); err != nil {
		return WriteResult{}, err
	}
	metadata, err := json.Marshal(a.Fields)
	if err != nil {
		return WriteResult{}, err
	}
	if a.Fields == nil {
		metadata = []byte("{}")
	}
	_, err = tx.ExecContext(ctx, `UPDATE artifacts SET title=?,content=?,project=?,agent=?,kind=?,content_hash=?,metadata_json=?,tags_json=?,updated_at=?,revision=?,seq=?,actor=?,batch_id=?,schema_version=? WHERE id=?`,
		a.Title, a.Content, a.Project, a.Agent, a.Kind, a.ContentHash, string(metadata), encodeTags(a.Tags), now.Format(time.RFC3339Nano), a.Revision, a.Seq, actor, nullable(o.BatchID), schemaVersion, id)
	if err != nil {
		return WriteResult{}, err
	}
	if err = ftsReplace(ctx, tx, a); err != nil {
		return WriteResult{}, err
	}
	if oldProject != a.Project {
		if _, err = bumpRevision(ctx, tx, oldProject); err != nil {
			return WriteResult{}, err
		}
	}
	revision, err := bumpRevision(ctx, tx, a.Project)
	if err != nil {
		return WriteResult{}, err
	}
	if err = logChange(ctx, tx, a.Project, id, "artifact.updated", revision, "Updated "+a.Title, actor, sensitive); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Artifact: a, Warnings: warnings}, nil
}

// deleteTx removes a record. Soft deletes keep the row (and its history) so it
// can be restored; hard deletes remove it for good.
func (s *Store) deleteTx(ctx context.Context, tx *sql.Tx, id string, soft bool, o WriteOptions) (Artifact, error) {
	a, err := getTx(ctx, tx, id)
	if err != nil {
		return Artifact{}, err
	}
	actor := actorOr(o, "agent")
	if soft && a.DeletedAt != nil {
		return a, nil
	}
	if err = snapshotTx(ctx, tx, a, o.BatchID); err != nil {
		return Artifact{}, err
	}
	revision := a.Revision + 1
	op := "purge"
	if soft {
		op = "delete"
	}
	seq, err := recordChange(ctx, tx, id, a.Kind, a.Project, op, revision, actor, o.BatchID)
	if err != nil {
		return Artifact{}, err
	}
	if soft {
		now := time.Now().UTC()
		a.DeletedAt, a.Revision, a.Seq, a.UpdatedAt, a.Actor, a.BatchID = &now, revision, seq, now, actor, o.BatchID
		_, err = tx.ExecContext(ctx, `UPDATE artifacts SET deleted_at=?,updated_at=?,revision=?,seq=?,actor=?,batch_id=? WHERE id=?`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), revision, seq, actor, nullable(o.BatchID), id)
		if err == nil {
			err = ftsReplace(ctx, tx, a)
		}
	} else {
		if _, err = tx.ExecContext(ctx, `DELETE FROM artifacts_fts WHERE artifact_id=?`, id); err == nil {
			if _, err = tx.ExecContext(ctx, `DELETE FROM links WHERE from_id=? OR to_id=?`, id, id); err == nil {
				_, err = tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id=?`, id)
			}
		}
	}
	if err != nil {
		return Artifact{}, err
	}
	scope, err := bumpRevision(ctx, tx, a.Project)
	if err != nil {
		return Artifact{}, err
	}
	if err = logChange(ctx, tx, a.Project, id, "artifact.deleted", scope, "Deleted "+a.Title, actor, true); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func (s *Store) restoreTx(ctx context.Context, tx *sql.Tx, id string, o WriteOptions) (Artifact, error) {
	a, err := getTx(ctx, tx, id)
	if err != nil {
		return Artifact{}, err
	}
	if a.DeletedAt == nil {
		return a, nil
	}
	if err = snapshotTx(ctx, tx, a, o.BatchID); err != nil {
		return Artifact{}, err
	}
	actor := actorOr(o, "agent")
	now := time.Now().UTC()
	a.DeletedAt, a.Revision, a.UpdatedAt, a.Actor, a.BatchID = nil, a.Revision+1, now, actor, o.BatchID
	if a.Seq, err = recordChange(ctx, tx, id, a.Kind, a.Project, "restore", a.Revision, actor, o.BatchID); err != nil {
		return Artifact{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artifacts SET deleted_at=NULL,updated_at=?,revision=?,seq=?,actor=?,batch_id=? WHERE id=?`, now.Format(time.RFC3339Nano), a.Revision, a.Seq, actor, nullable(o.BatchID), id); err != nil {
		return Artifact{}, err
	}
	if err = ftsReplace(ctx, tx, a); err != nil {
		return Artifact{}, err
	}
	scope, err := bumpRevision(ctx, tx, a.Project)
	if err != nil {
		return Artifact{}, err
	}
	if err = logChange(ctx, tx, a.Project, id, "artifact.restored", scope, "Restored "+a.Title, actor, true); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveArtifactWith saves one record with an optional caller-chosen ID and a
// write actor and batch.
func (s *Store) SaveArtifactWith(ctx context.Context, in SaveInput, o WriteOptions) (res WriteResult, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err = s.saveTx(ctx, tx, in, o)
		return err
	})
	return res, err
}

// UpdateArtifactWith applies a patch with compare-and-set and merge control.
func (s *Store) UpdateArtifactWith(ctx context.Context, id string, patch map[string]any, o UpdateOptions) (res WriteResult, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err = s.updateTx(ctx, tx, id, patch, o)
		return err
	})
	return res, err
}

// DeleteArtifactWith deletes softly or for good.
func (s *Store) DeleteArtifactWith(ctx context.Context, id string, soft bool, o WriteOptions) (a Artifact, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		a, err = s.deleteTx(ctx, tx, id, soft, o)
		return err
	})
	return a, err
}

// RestoreArtifact brings a soft-deleted record back.
func (s *Store) RestoreArtifact(ctx context.Context, id string, o WriteOptions) (a Artifact, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		a, err = s.restoreTx(ctx, tx, id, o)
		return err
	})
	return a, err
}

// PurgeDeleted permanently removes soft-deleted records older than the given
// number of days (0 purges all of them) and returns how many were removed.
func (s *Store) PurgeDeleted(ctx context.Context, olderThanDays int, o WriteOptions) (int, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -olderThanDays).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM artifacts WHERE deleted_at IS NOT NULL AND deleted_at<=?`, cutoff)
	if err != nil {
		return 0, err
	}
	ids, err := scanIDs(rows)
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err := s.DeleteArtifactWith(ctx, id, false, o); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// DeepEqualFields reports whether two field maps hold the same JSON.
func DeepEqualFields(a, b map[string]any) bool {
	return reflect.DeepEqual(normalizeJSON(a), normalizeJSON(b))
}
