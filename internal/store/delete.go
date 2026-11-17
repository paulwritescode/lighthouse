package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeleteArtifact permanently removes a record. Use DeleteArtifactWith for a
// soft delete that can be restored.
func (s *Store) DeleteArtifact(ctx context.Context, id string, actors ...string) error {
	o := WriteOptions{}
	if len(actors) > 0 {
		o.Actor = actors[0]
	}
	_, err := s.DeleteArtifactWith(ctx, id, false, o)
	if errors.Is(err, ErrNotFound) {
		return sql.ErrNoRows
	}
	return err
}
func (s *Store) DeleteMemory(ctx context.Context, id string, actors ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, key string
	err = tx.QueryRowContext(ctx, `SELECT scope_key,memory_key FROM memory_items WHERE id=? AND status='active'`, id).Scan(&project, &key)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory_items SET status='retracted',updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	revision, err := bumpRevision(ctx, tx, project)
	if err != nil {
		return err
	}
	actor := "agent"
	if len(actors) > 0 && actors[0] != "" {
		actor = actors[0]
	}
	if err = logChange(ctx, tx, project, id, "memory.deleted", revision, "Deleted memory: "+key, actor, true); err != nil {
		return err
	}
	return tx.Commit()
}
