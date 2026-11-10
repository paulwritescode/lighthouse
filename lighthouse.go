// Package lighthouse exposes the local Lighthouse database to Go programs.
//
// Open a database path once, use Database methods with a context, and close it
// when finished. Applications can import this package directly or install the
// CLI separately with `go install github.com/paulwritescode/lighthouse/cmd/lighthouse@latest`.
package lighthouse

import (
	"context"
	"os"
	"path/filepath"

	"github.com/paulwritescode/lighthouse/internal/store"
)

// Artifact is one indexed Lighthouse document.
type Artifact = store.Artifact

// Memory is a durable, revisioned Lighthouse context item.
type Memory = store.Memory

// ContextPack contains the context and sources loaded for a project or session.
type ContextPack = store.ContextPack

// Counts summarizes stored results, memories, projects, and audit activity.
type Counts = store.Counts

// ChangeEvent is one audited data mutation.
type ChangeEvent = store.ChangeEvent

// ArtifactInput contains the indexed fields and optional per-record JSON fields.
type ArtifactInput struct {
	Title   string
	Content string
	Project string
	Agent   string
	Kind    string
	Fields  map[string]any
	Tags    []string
}

// MemoryInput contains a durable, project-scoped fact or preference.
type MemoryInput struct {
	Key      string
	Body     string
	Project  string
	Kind     string
	SourceID string
	Tags     []string
}

// Database is a handle to one local Lighthouse SQLite database.
type Database struct{ store *store.Store }

// DefaultDatabasePath returns the path used by the Lighthouse CLI, honoring
// LIGHTHOUSE_DB before the operating-system user configuration directory.
func DefaultDatabasePath() (string, error) {
	if path := os.Getenv("LIGHTHOUSE_DB"); path != "" {
		return path, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "lighthouse", "lighthouse.db"), nil
}

// OpenDefault opens the same database file used by the CLI by default.
func OpenDefault() (*Database, error) {
	path, err := DefaultDatabasePath()
	if err != nil {
		return nil, err
	}
	return Open(path)
}

// Open opens or creates a Lighthouse SQLite database at path and applies the
// current Lighthouse migrations. Use ":memory:" for an ephemeral database.
func Open(path string) (*Database, error) {
	s, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	return &Database{store: s}, nil
}

// Close releases the underlying SQLite connection.
func (db *Database) Close() error { return db.store.Close() }

// CreateArtifact creates an indexed result with optional JSON-compatible fields.
func (db *Database) CreateArtifact(ctx context.Context, input ArtifactInput) (Artifact, error) {
	return db.store.SaveArtifactTagged(ctx, input.Title, input.Content, input.Project, input.Agent, input.Kind, input.Fields, input.Tags, "library")
}

// GetArtifact returns an artifact by its stable ID.
func (db *Database) GetArtifact(ctx context.Context, id string) (Artifact, error) {
	return db.store.GetArtifact(ctx, id)
}

// SearchArtifacts finds artifacts by full text and optional project scope.
func (db *Database) SearchArtifacts(ctx context.Context, query, project string, limit int) ([]Artifact, error) {
	return db.store.Search(ctx, query, project, limit)
}

// ListArtifacts returns the most recently created artifacts in the project scope.
func (db *Database) ListArtifacts(ctx context.Context, project string, limit int) ([]Artifact, error) {
	return db.store.Recent(ctx, project, limit)
}

// UpdateArtifact applies indexed attributes and merges JSON fields by key.
func (db *Database) UpdateArtifact(ctx context.Context, id string, fields map[string]any) (Artifact, error) {
	return db.store.UpdateArtifact(ctx, id, fields, "library")
}

// DeleteArtifact removes an artifact and records an audit event.
func (db *Database) DeleteArtifact(ctx context.Context, id string) error {
	return db.store.DeleteArtifact(ctx, id, "library")
}

// CreateMemory creates or updates a memory item with the same project/key.
func (db *Database) CreateMemory(ctx context.Context, input MemoryInput) (Memory, error) {
	return db.store.SaveMemoryTagged(ctx, input.Key, input.Body, input.Project, input.Kind, input.SourceID, input.Tags, "library")
}

// ListMemory returns active memory items in the requested project scope.
func (db *Database) ListMemory(ctx context.Context, project string, limit int) ([]Memory, error) {
	return db.store.ListMemory(ctx, project, limit)
}

// GetMemory returns an active memory item by its stable ID.
func (db *Database) GetMemory(ctx context.Context, id string) (Memory, error) {
	return db.store.GetMemory(ctx, id)
}

// UpdateMemory replaces the selected memory item's key, body, scope, and metadata.
func (db *Database) UpdateMemory(ctx context.Context, id string, input MemoryInput) error {
	if input.Tags == nil {
		return db.store.UpdateMemory(ctx, id, input.Key, input.Body, input.Project, input.Kind, input.SourceID, "library")
	}
	return db.store.UpdateMemoryTagged(ctx, id, input.Key, input.Body, input.Project, input.Kind, input.SourceID, input.Tags, "library")
}

// DeleteMemory retracts a memory item and records an audit event.
func (db *Database) DeleteMemory(ctx context.Context, id string) error {
	return db.store.DeleteMemory(ctx, id, "library")
}

// LoadMemory creates a context snapshot for the session and project.
func (db *Database) LoadMemory(ctx context.Context, session, project string, limit int) (ContextPack, error) {
	return db.store.LoadContext(ctx, session, project, limit)
}

// Counts returns database record and change totals.
func (db *Database) Counts(ctx context.Context) (Counts, error) { return db.store.Counts(ctx) }

// Audit returns the latest recorded changes.
func (db *Database) Audit(ctx context.Context, limit int) ([]ChangeEvent, error) {
	return db.store.RecentChanges(ctx, limit)
}
