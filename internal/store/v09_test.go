package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustSave(t *testing.T, s *Store, title, project, kind string, fields map[string]any) Artifact {
	t.Helper()
	res, err := s.SaveArtifactWith(context.Background(), SaveInput{Title: title, Content: "body of " + title, Project: project, Kind: kind, Fields: fields}, WriteOptions{Actor: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Artifact
}

const v1Schema = `
CREATE TABLE artifacts (id TEXT PRIMARY KEY,title TEXT NOT NULL,content TEXT NOT NULL,project TEXT NOT NULL DEFAULT '',agent TEXT NOT NULL DEFAULT 'unknown',kind TEXT NOT NULL DEFAULT 'result',content_hash TEXT NOT NULL,metadata_json TEXT NOT NULL DEFAULT '{}',tags_json TEXT NOT NULL DEFAULT '[]',created_at TEXT NOT NULL);
CREATE VIRTUAL TABLE artifacts_fts USING fts5(artifact_id UNINDEXED, title, content, project, agent);
CREATE TABLE memory_items (id TEXT PRIMARY KEY,memory_key TEXT NOT NULL,body TEXT NOT NULL,scope_key TEXT NOT NULL DEFAULT '',kind TEXT NOT NULL DEFAULT 'fact',source_id TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1,status TEXT NOT NULL DEFAULT 'active',tags_json TEXT NOT NULL DEFAULT '[]',created_at TEXT NOT NULL,updated_at TEXT NOT NULL,UNIQUE(scope_key, memory_key));
CREATE TABLE scope_revisions (scope_key TEXT PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 0);
CREATE TABLE context_snapshots (id TEXT PRIMARY KEY,session_key TEXT NOT NULL DEFAULT '',scope_key TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL,context_text TEXT NOT NULL,source_ids_json TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE embeddings (artifact_id TEXT NOT NULL,model TEXT NOT NULL,dimensions INTEGER NOT NULL,vector_json TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(artifact_id, model));
CREATE TABLE change_events (id TEXT PRIMARY KEY,scope_key TEXT NOT NULL DEFAULT '',target_id TEXT NOT NULL,action TEXT NOT NULL,revision INTEGER NOT NULL,summary TEXT NOT NULL,created_at TEXT NOT NULL,actor TEXT NOT NULL DEFAULT 'agent',sensitive INTEGER NOT NULL DEFAULT 0);
`

func TestUpgradeFromV1KeepsDataAndWritesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lh.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(v1Schema); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"first", "second", "third"} {
		ts := time.Date(2026, 11, 1+i, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		if _, err = raw.Exec(`INSERT INTO artifacts(id,title,content,project,agent,kind,content_hash,metadata_json,tags_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			"aaaaaaaa"+strings.Repeat("0", 23)+string(rune('1'+i)), title, "c "+title, "proj", "agent", "lesson", "h", `{"n":1}`, "[]", ts); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	matches, _ := filepath.Glob(path + ".bak-v0-*")
	if len(matches) != 1 {
		t.Fatalf("expected one backup file, got %v", matches)
	}
	all, err := s.AllArtifacts(context.Background())
	if err != nil || len(all) != 3 {
		t.Fatalf("artifacts after upgrade: %d %v", len(all), err)
	}
	for _, a := range all {
		if a.Revision != 1 || a.Seq == 0 || a.UpdatedAt.IsZero() || a.Actor == "" {
			t.Fatalf("envelope not backfilled: %+v", a)
		}
	}
	if all[0].Seq >= all[1].Seq || all[1].Seq >= all[2].Seq {
		t.Fatalf("seq should follow creation order: %d %d %d", all[0].Seq, all[1].Seq, all[2].Seq)
	}
	page, err := s.Changes(context.Background(), ChangeQuery{})
	if err != nil || len(page.Items) != 3 || page.Head != all[2].Seq {
		t.Fatalf("change feed after upgrade: %+v %v", page, err)
	}
	var v int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	s.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	if m, _ := filepath.Glob(path + ".bak-*"); len(m) != 1 {
		t.Fatalf("reopening must not back up again: %v", m)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA user_version=99`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = Open(path); err == nil || !strings.Contains(err.Error(), "newer Lighthouse") {
		t.Fatalf("expected a newer-schema refusal, got %v", err)
	}
}

func TestRevisionConflictAndMergeModes(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	a := mustSave(t, s, "Task", "p", "dock_task", map[string]any{"status": "todo", "due": map[string]any{"date": "2026-11-20", "time": "09:00"}, "tags2": []any{"x"}})
	if a.Revision != 1 || a.Seq == 0 {
		t.Fatalf("new record envelope: %+v", a)
	}
	one := int64(1)
	res, err := s.UpdateArtifactWith(ctx, a.ID, map[string]any{"status": "doing"}, UpdateOptions{IfRevision: &one})
	if err != nil || res.Artifact.Revision != 2 {
		t.Fatalf("update with matching revision: %+v %v", res, err)
	}
	_, err = s.UpdateArtifactWith(ctx, a.ID, map[string]any{"status": "done"}, UpdateOptions{IfRevision: &one})
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current.Revision != 2 || conflict.Current.Fields["status"] != "doing" {
		t.Fatalf("expected a revision conflict carrying the current record, got %v", err)
	}
	// Shallow merge replaces the whole nested object.
	res, _ = s.UpdateArtifactWith(ctx, a.ID, map[string]any{"due": map[string]any{"date": "2026-11-21"}}, UpdateOptions{})
	if _, has := res.Artifact.Fields["due"].(map[string]any)["time"]; has {
		t.Fatal("shallow merge should replace the nested object")
	}
	// Deep merge keeps sibling keys.
	b := mustSave(t, s, "Deep", "p", "dock_task", map[string]any{"due": map[string]any{"date": "2026-11-20", "time": "09:00"}})
	res, _ = s.UpdateArtifactWith(ctx, b.ID, map[string]any{"due": map[string]any{"date": "2026-11-22"}}, UpdateOptions{Merge: "deep"})
	due := res.Artifact.Fields["due"].(map[string]any)
	if due["date"] != "2026-11-22" || due["time"] != "09:00" {
		t.Fatalf("deep merge lost a sibling: %v", due)
	}
	// Unset removes a nested path.
	res, _ = s.UpdateArtifactWith(ctx, b.ID, map[string]any{"status": "todo"}, UpdateOptions{Unset: []string{"due.time"}})
	if _, has := res.Artifact.Fields["due"].(map[string]any)["time"]; has {
		t.Fatal("unset should remove due.time")
	}
	// Replace swaps the whole document.
	res, _ = s.UpdateArtifactWith(ctx, b.ID, map[string]any{"only": "this"}, UpdateOptions{Merge: "replace"})
	if len(res.Artifact.Fields) != 1 || res.Artifact.Fields["only"] != "this" {
		t.Fatalf("replace should leave one field: %v", res.Artifact.Fields)
	}
	// A no-op update changes neither revision nor sequence.
	before, _ := s.GetArtifact(ctx, b.ID)
	res, err = s.UpdateArtifactWith(ctx, b.ID, map[string]any{"only": "this"}, UpdateOptions{})
	if err != nil || !res.Unchanged || res.Artifact.Revision != before.Revision || res.Artifact.Seq != before.Seq {
		t.Fatalf("no-op update must not write: %+v %v", res, err)
	}
	// The legacy API still updates titles.
	if _, err = s.UpdateArtifact(ctx, b.ID, map[string]any{"title": "Renamed"}, "agent"); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCompareAndSetHasOneWinner(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	a := mustSave(t, s, "Race", "p", "dock_task", map[string]any{"n": 0})
	rev := a.Revision
	results := make(chan error, 25)
	for i := 0; i < 25; i++ {
		go func(i int) {
			_, err := s.UpdateArtifactWith(ctx, a.ID, map[string]any{"n": i + 1}, UpdateOptions{IfRevision: &rev})
			results <- err
		}(i)
	}
	wins := 0
	for i := 0; i < 25; i++ {
		err := <-results
		var c *RevisionConflictError
		switch {
		case err == nil:
			wins++
		case errors.As(err, &c):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one update should win, got %d", wins)
	}
}

func TestCallerChosenIDIsIdempotent(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	in := SaveInput{ID: "0123456789abcdef0123456789abcdef", Title: "T", Content: "c", Project: "p", Kind: "dock_task", Fields: map[string]any{"a": 1}}
	first, err := s.SaveArtifactWith(ctx, in, WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SaveArtifactWith(ctx, in, WriteOptions{})
	if err != nil || !again.Unchanged || again.Artifact.Revision != first.Artifact.Revision {
		t.Fatalf("a retry of the same payload must return the original: %+v %v", again, err)
	}
	in.Title = "Different"
	if _, err = s.SaveArtifactWith(ctx, in, WriteOptions{}); !errors.Is(err, ErrIDExists) {
		t.Fatalf("a different payload for the same id must fail with ErrIDExists, got %v", err)
	}
	if _, err = s.SaveArtifactWith(ctx, SaveInput{ID: "Bad ID", Title: "T", Content: "c"}, WriteOptions{}); err == nil {
		t.Fatal("malformed ids must be rejected")
	}
}

func TestSoftDeleteRestorePurge(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	a := mustSave(t, s, "Searchable zebra", "p", "result", nil)
	if _, err := s.DeleteArtifactWith(ctx, a.ID, true, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, "zebra", "", 10); len(hits) != 0 {
		t.Fatal("soft-deleted records must leave search")
	}
	if all, _ := s.AllArtifacts(ctx); len(all) != 0 {
		t.Fatal("soft-deleted records must leave exports")
	}
	got, err := s.GetArtifact(ctx, a.ID)
	if err != nil || got.DeletedAt == nil {
		t.Fatalf("get should still return a soft-deleted record: %+v %v", got, err)
	}
	if _, err = s.UpdateArtifactWith(ctx, a.ID, map[string]any{"x": 1}, UpdateOptions{}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("updating a deleted record must fail, got %v", err)
	}
	restored, err := s.RestoreArtifact(ctx, a.ID, WriteOptions{})
	if err != nil || restored.DeletedAt != nil {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	if hits, _ := s.Search(ctx, "zebra", "", 10); len(hits) != 1 {
		t.Fatal("restored records must return to search")
	}
	if _, err = s.DeleteArtifactWith(ctx, a.ID, true, WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeDeleted(ctx, 0, WriteOptions{})
	if err != nil || n != 1 {
		t.Fatalf("purge removed %d: %v", n, err)
	}
	if _, err = s.GetArtifact(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a purged record is gone, got %v", err)
	}
}

func TestAtomicBatchRollsBackAndUndoRestores(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	keep := mustSave(t, s, "Existing", "p", "dock_task", map[string]any{"status": "todo"})
	_, err := s.Batch(ctx, []BatchOp{
		{Op: "save", Save: &SaveInput{Title: "A", Content: "c", Project: "p", Kind: "dock_task"}},
		{Op: "update", ID: "does-not-exist-000000000000", Patch: map[string]any{"x": 1}},
	}, BatchOptions{Atomic: true})
	var be *BatchError
	if !errors.As(err, &be) || be.Index != 1 {
		t.Fatalf("expected a failure at operation 1, got %v", err)
	}
	if res, _ := s.Query(ctx, QuerySpec{Kind: "dock_task"}); len(res.Items) != 1 {
		t.Fatalf("a failed atomic batch must apply nothing, found %d records", len(res.Items))
	}

	res, err := s.Batch(ctx, []BatchOp{
		{Op: "save", Save: &SaveInput{Title: "B1", Content: "c", Project: "p", Kind: "dock_task"}},
		{Op: "save", Save: &SaveInput{Title: "B2", Content: "c", Project: "p", Kind: "dock_task"}},
		{Op: "update", ID: keep.ID, Patch: map[string]any{"status": "done"}},
	}, BatchOptions{Atomic: true, WriteOptions: WriteOptions{Actor: "plan"}})
	if err != nil || len(res.Items) != 3 || res.BatchID == "" {
		t.Fatalf("batch: %+v %v", res, err)
	}
	undo, err := s.BatchUndo(ctx, res.BatchID, WriteOptions{Actor: "user"})
	if err != nil || undo.Reverted != 3 || len(undo.Conflicts) != 0 {
		t.Fatalf("undo: %+v %v", undo, err)
	}
	got, _ := s.GetArtifact(ctx, keep.ID)
	if got.Fields["status"] != "todo" {
		t.Fatalf("undo should restore the old status, got %v", got.Fields["status"])
	}
	if live, _ := s.Query(ctx, QuerySpec{Kind: "dock_task"}); len(live.Items) != 1 {
		t.Fatalf("created records should be gone after undo, found %d", len(live.Items))
	}

	// A record edited after the batch is reported, not overwritten.
	res, _ = s.Batch(ctx, []BatchOp{{Op: "update", ID: keep.ID, Patch: map[string]any{"status": "doing"}}}, BatchOptions{Atomic: true})
	if _, err = s.UpdateArtifactWith(ctx, keep.ID, map[string]any{"status": "blocked"}, UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	undo, err = s.BatchUndo(ctx, res.BatchID, WriteOptions{})
	if err != nil || undo.Reverted != 0 || len(undo.Conflicts) != 1 {
		t.Fatalf("a later edit must be a conflict: %+v %v", undo, err)
	}
	if got, _ = s.GetArtifact(ctx, keep.ID); got.Fields["status"] != "blocked" {
		t.Fatal("the later edit must survive")
	}
}

func TestQueryOperatorsOrderAndStablePaging(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	if _, err := s.RegisterKind(ctx, KindSpec{Kind: "dock_task", Indexes: []string{"fields.status", "fields.due.date"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		status := []string{"todo", "doing", "done"}[i%3]
		mustSave(t, s, "Task "+string(rune('A'+i%26)), "proj", "dock_task", map[string]any{
			"status": status, "due": map[string]any{"date": time.Date(2026, 11, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")},
			"tags2": []any{"alpha", "n" + string(rune('0'+i%3))}, "n": i})
	}
	mustSave(t, s, "Other kind", "proj", "result", nil)

	res, err := s.Query(ctx, QuerySpec{Kind: "dock_*", Where: map[string]any{"and": []any{
		map[string]any{"fields.status": map[string]any{"in": []any{"todo", "doing"}}},
		map[string]any{"fields.due.date": map[string]any{"gte": "2026-11-05", "lt": "2026-11-15"}},
	}}, Order: []OrderKey{{Path: "fields.due.date", Dir: "asc"}}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 7 || res.Total == nil || *res.Total != 7 {
		t.Fatalf("range and in query returned %d items (total %v)", len(res.Items), res.Total)
	}
	prev := ""
	for _, it := range res.Items {
		d := it["fields"].(map[string]any)["due"].(map[string]any)["date"].(string)
		if d < prev {
			t.Fatal("results must follow the requested order")
		}
		prev = d
	}
	if n, _ := s.Query(ctx, QuerySpec{Kind: "dock_task", Where: map[string]any{"fields.tags2": map[string]any{"has": "n1"}}, Count: true}); n.Count == nil || *n.Count != 10 {
		t.Fatalf("has on an array field: %v", n.Count)
	}
	if n, _ := s.Query(ctx, QuerySpec{Kind: "dock_task", Where: map[string]any{"not": map[string]any{"fields.status": "done"}}, Count: true}); *n.Count != 20 {
		t.Fatalf("not: %d", *n.Count)
	}
	if n, _ := s.Query(ctx, QuerySpec{Kind: "dock_task", Where: map[string]any{"title": map[string]any{"prefix": "Task A"}}, Count: true}); n.Count == nil || *n.Count < 1 {
		t.Fatal("prefix should match")
	}

	// Paging is stable even while records are written.
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		pr, err := s.Query(ctx, QuerySpec{Kind: "dock_task", Limit: 7, Cursor: cursor, Order: []OrderKey{{Path: "fields.n", Dir: "asc"}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range pr.Items {
			id := it["id"].(string)
			if seen[id] {
				t.Fatalf("record %s appeared twice", id)
			}
			seen[id] = true
		}
		mustSave(t, s, "Late arrival", "proj", "dock_task", map[string]any{"status": "todo", "n": -1})
		if pr.NextCursor == "" {
			break
		}
		cursor = pr.NextCursor
	}
	if len(seen) != 30 {
		t.Fatalf("paging saw %d of the 30 original records", len(seen))
	}

	for name, bad := range map[string]QuerySpec{
		"unknown path":   {Where: map[string]any{"bogus": 1}},
		"injection path": {Where: map[string]any{"fields.a'); DROP TABLE artifacts;--": 1}},
		"bad operator":   {Where: map[string]any{"fields.a": map[string]any{"matches": "x"}}},
		"bad limit":      {Limit: 5000},
		"bad cursor":     {Cursor: "%%%"},
	} {
		if _, err := s.Query(ctx, bad); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%s should be rejected, got %v", name, err)
		}
	}
	if _, err := s.GetArtifact(ctx, res.Items[0]["id"].(string)); err != nil {
		t.Fatalf("the artifacts table must be intact: %v", err)
	}

	// The declared index is used.
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT id FROM artifacts WHERE kind='dock_task' AND json_extract(metadata_json,'$.status')='todo'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	used := false
	for rows.Next() {
		var a, b, c int
		var detail string
		_ = rows.Scan(&a, &b, &c, &detail)
		used = used || strings.Contains(detail, "idx_kind_")
	}
	if !used {
		t.Fatal("the registered field index should be used for status lookups")
	}
}

func TestKindSchemaEnforcementWarningAndMemoryExclusion(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	schema := map[string]any{"type": "object", "required": []any{"status"}, "additionalProperties": true, "properties": map[string]any{
		"status":   map[string]any{"enum": []any{"todo", "done"}},
		"estimate": map[string]any{"type": "integer", "minimum": float64(1)},
	}}
	if _, err := s.RegisterKind(ctx, KindSpec{Kind: "strict_task", Schema: schema, ExcludeFromMemory: true}); err != nil {
		t.Fatal(err)
	}
	_, err := s.SaveArtifactWith(ctx, SaveInput{Title: "Bad", Content: "c", Kind: "strict_task", Fields: map[string]any{"status": "nope", "estimate": 0}}, WriteOptions{})
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Errors) != 2 {
		t.Fatalf("every violation should be listed, got %v", err)
	}
	ok, err := s.SaveArtifactWith(ctx, SaveInput{Title: "Good zebra", Content: "c", Kind: "strict_task", Fields: map[string]any{"status": "todo", "estimate": 30, "extra": true}}, WriteOptions{})
	if err != nil || ok.Artifact.SchemaVersion != 1 {
		t.Fatalf("valid record: %+v %v", ok, err)
	}
	if _, err = s.UpdateArtifactWith(ctx, ok.Artifact.ID, map[string]any{"status": "bogus"}, UpdateOptions{}); !errors.As(err, &verr) {
		t.Fatalf("updates are validated too, got %v", err)
	}
	if _, err = s.RegisterKind(ctx, KindSpec{Kind: "strict_task", Schema: schema, Validation: "warn", ExcludeFromMemory: true}); err != nil {
		t.Fatal(err)
	}
	w, err := s.SaveArtifactWith(ctx, SaveInput{Title: "Warned", Content: "c", Kind: "strict_task", Fields: map[string]any{"status": "bogus"}}, WriteOptions{})
	if err != nil || len(w.Warnings) != 1 {
		t.Fatalf("warn mode should save with a warning: %+v %v", w, err)
	}
	if hits, _ := s.Search(ctx, "zebra", "", 10); len(hits) != 0 {
		t.Fatal("excluded kinds must stay out of agent search")
	}
	if hits, _ := s.SearchSorted(ctx, "zebra", "", 10, "newest"); len(hits) != 1 {
		t.Fatal("the dashboard search must still find them")
	}
	pack, err := s.LoadContext(ctx, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pack.Artifacts {
		if a.Kind == "strict_task" {
			t.Fatal("excluded kinds must stay out of context packs")
		}
	}
	if _, err = s.RegisterKind(ctx, KindSpec{Kind: "Bad Name"}); err == nil {
		t.Fatal("kind names are validated")
	}
}

func TestChangeFeedIsMonotonicAndFilters(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	a := mustSave(t, s, "One", "p", "dock_task", nil)
	b := mustSave(t, s, "Two", "q", "result", nil)
	_, _ = s.UpdateArtifactWith(ctx, a.ID, map[string]any{"x": 1}, UpdateOptions{WriteOptions: WriteOptions{Actor: "dock"}})
	_, _ = s.DeleteArtifactWith(ctx, b.ID, true, WriteOptions{Actor: "dock"})
	page, err := s.Changes(ctx, ChangeQuery{})
	if err != nil || len(page.Items) != 4 {
		t.Fatalf("feed: %+v %v", page, err)
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i].Seq <= page.Items[i-1].Seq {
			t.Fatal("sequence numbers must increase")
		}
	}
	ops := []string{}
	for _, c := range page.Items {
		ops = append(ops, c.Op)
	}
	if strings.Join(ops, ",") != "create,create,update,delete" {
		t.Fatalf("ops: %v", ops)
	}
	after, _ := s.Changes(ctx, ChangeQuery{Since: page.Items[1].Seq, Kinds: []string{"dock_*"}})
	if len(after.Items) != 1 || after.Items[0].Op != "update" {
		t.Fatalf("since and kind filters: %+v", after)
	}
	none, _ := s.Changes(ctx, ChangeQuery{Since: page.Head, ActorsNot: []string{"dock"}})
	if len(none.Items) != 0 || none.NextSeq != page.Head {
		t.Fatalf("a caught-up client sees nothing new: %+v", none)
	}
	if future, _ := s.Changes(ctx, ChangeQuery{Since: page.Head + 100}); !future.Reset {
		t.Fatal("a position ahead of the feed must signal a reset")
	}
}

func TestLinksAndTokens(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	a := mustSave(t, s, "Task", "p", "dock_task", nil)
	b := mustSave(t, s, "Source", "p", "result", nil)
	if _, err := s.AddLink(ctx, a.ID, "source", b.ID, map[string]any{"quote": "deadline"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLink(ctx, a.ID, "source", a.ID, nil); err == nil {
		t.Fatal("self links are rejected")
	}
	back, _ := s.Links(ctx, b.ID, "source", "in")
	if len(back) != 1 || back[0].From != a.ID || back[0].Dangling {
		t.Fatalf("backlinks: %+v", back)
	}
	_, _ = s.DeleteArtifactWith(ctx, b.ID, true, WriteOptions{})
	if links, _ := s.Links(ctx, a.ID, "", "both"); !links[0].Dangling {
		t.Fatal("links to a deleted record are dangling")
	}
	_, _ = s.DeleteArtifactWith(ctx, b.ID, false, WriteOptions{})
	if links, _ := s.Links(ctx, a.ID, "", "both"); len(links) != 0 {
		t.Fatal("purging a record removes its links")
	}

	secret, tok, err := s.CreateToken(ctx, "dock", []string{"write:dock_*", "read:*"}, 0)
	if err != nil || !strings.HasPrefix(secret, "lh_") {
		t.Fatalf("create token: %v", err)
	}
	if got, err := s.VerifyToken(ctx, secret); err != nil || got.ID != tok.ID {
		t.Fatalf("verify: %v", err)
	}
	if _, err = s.VerifyToken(ctx, secret+"x"); err == nil {
		t.Fatal("a wrong secret must fail")
	}
	if !tok.Allows("write", "dock_task") || tok.Allows("write", "result") || !tok.Allows("read", "result") || tok.Allows("write", "dock") {
		t.Fatalf("scope checks are wrong: %+v", tok.Scopes)
	}
	if _, _, err = s.CreateToken(ctx, "dock", []string{"read:*"}, 0); err == nil {
		t.Fatal("duplicate names are rejected")
	}
	if _, _, err = s.CreateToken(ctx, "bad", []string{"admin"}, 0); err == nil {
		t.Fatal("bad scopes are rejected")
	}
	newSecret, _, err := s.RotateToken(ctx, "dock")
	if err != nil || newSecret == secret {
		t.Fatalf("rotate: %v", err)
	}
	if _, err = s.VerifyToken(ctx, secret); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("the old secret must stop working, got %v", err)
	}
	if _, err = s.VerifyToken(ctx, newSecret); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeToken(ctx, "dock"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyToken(ctx, newSecret); err == nil {
		t.Fatal("revoked tokens fail")
	}
}

func TestImportRecordsChangesAndBackupUsesNewestSchema(t *testing.T) {
	s, ctx := openMem(t), context.Background()
	n, err := s.ImportArtifacts(ctx, []Artifact{{Title: "Imported", Content: "c", Project: "p", CreatedAt: time.Now().Add(-time.Hour)}})
	if err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	page, _ := s.Changes(ctx, ChangeQuery{})
	if len(page.Items) != 1 || page.Items[0].Actor != "import" {
		t.Fatalf("imports must appear in the feed: %+v", page)
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err = s.BackupTo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dst); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	all, _ := c.AllArtifacts(ctx)
	if len(all) != 1 || all[0].Seq == 0 {
		t.Fatalf("backup should carry the envelope: %+v", all)
	}
}
