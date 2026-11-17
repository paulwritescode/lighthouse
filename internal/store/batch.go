package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxBatchOps = 500

// BatchOp is one operation in a batch. Op is save, update, delete or restore.
type BatchOp struct {
	Op         string         `json:"op"`
	ID         string         `json:"id,omitempty"`
	Save       *SaveInput     `json:"save,omitempty"`
	Patch      map[string]any `json:"patch,omitempty"`
	IfRevision *int64         `json:"if_revision,omitempty"`
	Merge      string         `json:"merge,omitempty"`
	Unset      []string       `json:"unset,omitempty"`
	Soft       *bool          `json:"soft,omitempty"`
}

// BatchOptions control a batch. Atomic (the default for callers) runs every
// operation in one transaction and applies nothing on any failure.
type BatchOptions struct {
	WriteOptions
	Atomic bool
}

// BatchItem is the outcome of one operation.
type BatchItem struct {
	Index    int       `json:"index"`
	Op       string    `json:"op"`
	Artifact *Artifact `json:"artifact,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// BatchResult lists every outcome and the batch ID that undoes them together.
type BatchResult struct {
	BatchID string      `json:"batch_id"`
	Items   []BatchItem `json:"items"`
}

// BatchError names the operation that made an atomic batch fail.
type BatchError struct {
	Index int
	Err   error
}

func (e *BatchError) Error() string { return fmt.Sprintf("operation %d failed: %v", e.Index, e.Err) }
func (e *BatchError) Unwrap() error { return e.Err }

func (s *Store) applyOp(ctx context.Context, tx *sql.Tx, i int, op BatchOp, o WriteOptions) (BatchItem, error) {
	item := BatchItem{Index: i, Op: op.Op}
	switch op.Op {
	case "save":
		if op.Save == nil {
			return item, errors.New("save needs a record")
		}
		res, err := s.saveTx(ctx, tx, *op.Save, o)
		if err != nil {
			return item, err
		}
		item.Artifact, item.Warnings = &res.Artifact, res.Warnings
	case "update":
		res, err := s.updateTx(ctx, tx, op.ID, op.Patch, UpdateOptions{WriteOptions: o, IfRevision: op.IfRevision, Merge: op.Merge, Unset: op.Unset})
		if err != nil {
			return item, err
		}
		item.Artifact, item.Warnings = &res.Artifact, res.Warnings
	case "delete":
		soft := true
		if op.Soft != nil {
			soft = *op.Soft
		}
		a, err := s.deleteTx(ctx, tx, op.ID, soft, o)
		if err != nil {
			return item, err
		}
		item.Artifact = &a
	case "restore":
		a, err := s.restoreTx(ctx, tx, op.ID, o)
		if err != nil {
			return item, err
		}
		item.Artifact = &a
	default:
		return item, fmt.Errorf("unknown operation %q", op.Op)
	}
	return item, nil
}

// Batch runs up to 500 operations as one unit tagged with a batch ID.
func (s *Store) Batch(ctx context.Context, ops []BatchOp, opts BatchOptions) (BatchResult, error) {
	if len(ops) == 0 {
		return BatchResult{}, errors.New("batch has no operations")
	}
	if len(ops) > maxBatchOps {
		return BatchResult{}, fmt.Errorf("batch has %d operations; the limit is %d", len(ops), maxBatchOps)
	}
	if opts.BatchID == "" {
		id, err := newID()
		if err != nil {
			return BatchResult{}, err
		}
		opts.BatchID = id
	}
	res := BatchResult{BatchID: opts.BatchID, Items: make([]BatchItem, 0, len(ops))}
	if opts.Atomic {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			for i, op := range ops {
				item, err := s.applyOp(ctx, tx, i, op, opts.WriteOptions)
				if err != nil {
					return &BatchError{Index: i, Err: err}
				}
				res.Items = append(res.Items, item)
			}
			return nil
		})
		if err != nil {
			return BatchResult{}, err
		}
		return res, nil
	}
	for i, op := range ops {
		var item BatchItem
		err := s.inTx(ctx, func(tx *sql.Tx) (err error) {
			item, err = s.applyOp(ctx, tx, i, op, opts.WriteOptions)
			return err
		})
		if err != nil {
			item = BatchItem{Index: i, Op: op.Op, Error: err.Error()}
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// UndoConflict is a record the undo left alone because it changed after the
// batch ran.
type UndoConflict struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// UndoResult reports what an undo reverted. Undoing is itself a batch.
type UndoResult struct {
	BatchID   string         `json:"batch_id"`
	Undone    string         `json:"undone"`
	Reverted  int            `json:"reverted"`
	Conflicts []UndoConflict `json:"conflicts"`
}

// historyKeepDays is how long pre-change snapshots are kept for undo.
const historyKeepDays = 7

// BatchUndo reverts a batch. For each record the batch touched it checks that
// the record is still at the revision the batch left it, then restores its
// state from before the batch (records the batch created are soft-deleted).
// Records edited since are reported as conflicts and not changed.
func (s *Store) BatchUndo(ctx context.Context, batchID string, o WriteOptions) (UndoResult, error) {
	if batchID == "" {
		return UndoResult{}, errors.New("batch_id is required")
	}
	undoID, err := newID()
	if err != nil {
		return UndoResult{}, err
	}
	o.BatchID = undoID
	res := UndoResult{BatchID: undoID, Undone: batchID, Conflicts: []UndoConflict{}}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,op,revision FROM changes WHERE batch_id=? ORDER BY seq`, batchID)
		if err != nil {
			return err
		}
		type touch struct {
			firstOp string
			lastRev int64
		}
		order := []string{}
		touched := map[string]*touch{}
		for rows.Next() {
			var id, op string
			var rev int64
			if err := rows.Scan(&id, &op, &rev); err != nil {
				rows.Close()
				return err
			}
			if t, ok := touched[id]; ok {
				t.lastRev = rev
			} else {
				touched[id] = &touch{firstOp: op, lastRev: rev}
				order = append(order, id)
			}
		}
		rows.Close()
		if len(order) == 0 {
			return errors.New("no changes were recorded for that batch")
		}
		for i := len(order) - 1; i >= 0; i-- {
			id := order[i]
			t := touched[id]
			current, err := getTx(ctx, tx, id)
			missing := errors.Is(err, ErrNotFound)
			if err != nil && !missing {
				return err
			}
			if !missing && current.Revision != t.lastRev {
				res.Conflicts = append(res.Conflicts, UndoConflict{ID: id, Reason: fmt.Sprintf("changed since the batch (revision %d, expected %d)", current.Revision, t.lastRev)})
				continue
			}
			if missing && t.firstOp != "purge" {
				res.Conflicts = append(res.Conflicts, UndoConflict{ID: id, Reason: "record no longer exists"})
				continue
			}
			if t.firstOp == "create" {
				if _, err := s.deleteTx(ctx, tx, id, true, o); err != nil {
					return err
				}
				res.Reverted++
				continue
			}
			var snapJSON string
			err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM record_history WHERE batch_id=? AND id=? ORDER BY revision ASC LIMIT 1`, batchID, id).Scan(&snapJSON)
			if errors.Is(err, sql.ErrNoRows) {
				res.Conflicts = append(res.Conflicts, UndoConflict{ID: id, Reason: "the pre-change snapshot has expired"})
				continue
			}
			if err != nil {
				return err
			}
			var snap Artifact
			if err := json.Unmarshal([]byte(snapJSON), &snap); err != nil {
				return err
			}
			if err := s.applySnapshot(ctx, tx, snap, missing, o); err != nil {
				return err
			}
			res.Reverted++
		}
		return nil
	})
	if err != nil {
		return UndoResult{}, err
	}
	return res, nil
}

// applySnapshot writes a saved state back as a new revision.
func (s *Store) applySnapshot(ctx context.Context, tx *sql.Tx, snap Artifact, missing bool, o WriteOptions) error {
	now := time.Now().UTC()
	actor := actorOr(o, "agent")
	metadata, err := json.Marshal(snap.Fields)
	if err != nil {
		return err
	}
	if snap.Fields == nil {
		metadata = []byte("{}")
	}
	revision := snap.Revision + 1
	if !missing {
		var cur int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM artifacts WHERE id=?`, snap.ID).Scan(&cur); err != nil {
			return err
		}
		revision = cur + 1
	}
	seq, err := recordChange(ctx, tx, snap.ID, snap.Kind, snap.Project, "undo", revision, actor, o.BatchID)
	if err != nil {
		return err
	}
	snap.Revision, snap.Seq, snap.UpdatedAt, snap.Actor, snap.BatchID, snap.DeletedAt = revision, seq, now, actor, o.BatchID, nil
	hash := hashContent(snap.Content)
	if missing {
		_, err = tx.ExecContext(ctx, `INSERT INTO artifacts(id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at,updated_at,revision,seq,actor,batch_id,schema_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			snap.ID, snap.Title, snap.Content, snap.Project, snap.Agent, snap.Kind, hash, string(metadata), encodeTags(snap.Tags), snap.CreatedAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), revision, seq, actor, nullable(o.BatchID), snap.SchemaVersion)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE artifacts SET title=?,content=?,project=?,agent=?,kind=?,content_hash=?,metadata_json=?,tags_json=?,updated_at=?,revision=?,seq=?,deleted_at=NULL,actor=?,batch_id=?,schema_version=? WHERE id=?`,
			snap.Title, snap.Content, snap.Project, snap.Agent, snap.Kind, hash, string(metadata), encodeTags(snap.Tags), now.Format(time.RFC3339Nano), revision, seq, actor, nullable(o.BatchID), snap.SchemaVersion, snap.ID)
	}
	if err != nil {
		return err
	}
	if err = ftsReplace(ctx, tx, snap); err != nil {
		return err
	}
	scope, err := bumpRevision(ctx, tx, snap.Project)
	if err != nil {
		return err
	}
	return logChange(ctx, tx, snap.Project, snap.ID, "artifact.undone", scope, "Undid a batch for "+snap.Title, actor, true)
}

// PruneHistory removes undo snapshots older than historyKeepDays.
func (s *Store) PruneHistory(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -historyKeepDays).Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx, `DELETE FROM record_history WHERE at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- Links ----

// Link is a typed relation between two records.
type Link struct {
	From     string         `json:"from"`
	Rel      string         `json:"rel"`
	To       string         `json:"to"`
	Meta     map[string]any `json:"meta,omitempty"`
	At       time.Time      `json:"at"`
	Dangling bool           `json:"dangling,omitempty"`
}

// AddLink creates or replaces a relation. Both records must exist.
func (s *Store) AddLink(ctx context.Context, from, rel, to string, meta map[string]any) (Link, error) {
	rel = strings.ToLower(strings.TrimSpace(rel))
	if !relPattern.MatchString(rel) {
		return Link{}, errors.New("rel must be a lowercase slug such as source or depends_on")
	}
	if from == to {
		return Link{}, errors.New("a record cannot link to itself")
	}
	if err := validateFields(meta); err != nil {
		return Link{}, err
	}
	metaJSON, _ := json.Marshal(meta)
	if meta == nil {
		metaJSON = []byte("{}")
	}
	now := time.Now().UTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range []string{from, to} {
			if _, err := getTx(ctx, tx, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO links(from_id,rel,to_id,meta_json,at) VALUES(?,?,?,?,?) ON CONFLICT(from_id,rel,to_id) DO UPDATE SET meta_json=excluded.meta_json`, from, rel, to, string(metaJSON), now.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return Link{}, err
	}
	return Link{From: from, Rel: rel, To: to, Meta: meta, At: now}, nil
}

// RemoveLink deletes a relation and reports whether it existed.
func (s *Store) RemoveLink(ctx context.Context, from, rel, to string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM links WHERE from_id=? AND rel=? AND to_id=?`, from, rel, to)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Links lists relations touching a record. Direction is out, in or both.
func (s *Store) Links(ctx context.Context, id, rel, direction string) ([]Link, error) {
	var where []string
	var args []any
	switch direction {
	case "out":
		where, args = append(where, "l.from_id=?"), append(args, id)
	case "in":
		where, args = append(where, "l.to_id=?"), append(args, id)
	default:
		where, args = append(where, "(l.from_id=? OR l.to_id=?)"), append(args, id, id)
	}
	if rel != "" {
		where, args = append(where, "l.rel=?"), append(args, rel)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.from_id,l.rel,l.to_id,l.meta_json,l.at,
 (SELECT count(*) FROM artifacts a WHERE a.id=l.from_id AND a.deleted_at IS NULL),
 (SELECT count(*) FROM artifacts a WHERE a.id=l.to_id AND a.deleted_at IS NULL)
 FROM links l WHERE `+strings.Join(where, " AND ")+` ORDER BY l.at,l.rel LIMIT 1000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		var meta, at string
		var fromLive, toLive int
		if err := rows.Scan(&l.From, &l.Rel, &l.To, &meta, &at, &fromLive, &toLive); err != nil {
			return nil, err
		}
		l.Meta = decodeFields(meta)
		l.At, _ = time.Parse(time.RFC3339Nano, at)
		l.Dangling = fromLive == 0 || toLive == 0
		out = append(out, l)
	}
	return out, rows.Err()
}
