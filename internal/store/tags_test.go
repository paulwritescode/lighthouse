package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTagsAcrossResultsAndMemory(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "lighthouse.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.SaveArtifactTagged(ctx, "Plan", "A plan", "Personal Portfolio", "test", "result", nil, []string{"Writing", "writing", "Research Notes"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "personal-portfolio" || len(a.Tags) != 2 || a.Tags[1] != "research-notes" {
		t.Fatalf("normalized artifact: %+v", a)
	}
	m, err := db.SaveMemoryTagged(ctx, "plan", "Remember this", "Personal Growth", "fact", "", []string{"Research Notes"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Project != "personal-growth" {
		t.Fatalf("normalized memory: %+v", m)
	}
	updated, err := db.SaveMemory(ctx, "plan", "Revised body", "personal-growth", "fact", "")
	if err != nil || len(updated.Tags) != 1 || updated.Tags[0] != "research-notes" {
		t.Fatalf("upsert lost existing tags: %+v, %v", updated, err)
	}
	results, err := db.Search(ctx, "", "research-notes", 50)
	if err != nil || len(results) != 1 || results[0].ID != a.ID {
		t.Fatalf("secondary-tag results: %+v, %v", results, err)
	}
	memories, err := db.ListMemory(ctx, "research-notes", 50)
	if err != nil || len(memories) != 1 || memories[0].ID != m.ID {
		t.Fatalf("secondary-tag memory: %+v, %v", memories, err)
	}
	groups, err := db.ProjectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, group := range groups {
		if group.Name == "research-notes" && group.Results == 1 && group.Memories == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("secondary tag group missing: %+v", groups)
	}
}

func TestSortAppliesBeforeDisplayLimit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sort.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.SaveArtifact(ctx, "Alpha", "first", "zeta", "test", "result"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveArtifact(ctx, "Zulu", "second", "alpha", "test", "result"); err != nil {
		t.Fatal(err)
	}
	results, err := db.SearchSorted(ctx, "", "", 1, "title")
	if err != nil || len(results) != 1 || results[0].Title != "Alpha" {
		t.Fatalf("result sort after limit: %+v, %v", results, err)
	}
	results, err = db.SearchSorted(ctx, "", "", 1, "tag")
	if err != nil || len(results) != 1 || results[0].Project != "alpha" {
		t.Fatalf("tag sort after limit: %+v, %v", results, err)
	}
	if _, err := db.SaveMemory(ctx, "alpha", "first", "zeta", "fact", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveMemory(ctx, "zulu", "second", "alpha", "fact", ""); err != nil {
		t.Fatal(err)
	}
	memories, err := db.ListMemorySorted(ctx, "", 1, "key")
	if err != nil || len(memories) != 1 || memories[0].Key != "alpha" {
		t.Fatalf("memory sort after limit: %+v, %v", memories, err)
	}
}
