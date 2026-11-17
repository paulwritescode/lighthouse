package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// KindSpec describes a record kind: an optional JSON Schema for its fields,
// the field paths that get an index, and how it behaves in agent memory.
type KindSpec struct {
	Kind              string         `json:"kind"`
	Version           int            `json:"version"`
	Schema            map[string]any `json:"schema,omitempty"`
	Indexes           []string       `json:"indexes,omitempty"`
	ExcludeFromMemory bool           `json:"exclude_from_memory"`
	Validation        string         `json:"validation"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

var (
	kindNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	fieldPathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*){0,7}$`)
)

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// RegisterKind creates or replaces a kind and brings its indexes in line.
// Existing records are not rewritten; they keep the schema_version they were
// written under.
func (s *Store) RegisterKind(ctx context.Context, spec KindSpec) (KindSpec, error) {
	if !kindNamePattern.MatchString(spec.Kind) {
		return KindSpec{}, errors.New("kind must be lowercase letters, digits and underscores, starting with a letter")
	}
	if spec.Version < 1 {
		spec.Version = 1
	}
	switch spec.Validation {
	case "":
		spec.Validation = "enforce"
	case "enforce", "warn", "off":
	default:
		return KindSpec{}, errors.New("validation must be enforce, warn or off")
	}
	clean := make([]string, 0, len(spec.Indexes))
	seen := map[string]bool{}
	for _, p := range spec.Indexes {
		p = strings.TrimPrefix(strings.TrimSpace(p), "fields.")
		if !fieldPathPattern.MatchString(p) {
			return KindSpec{}, fmt.Errorf("invalid index path %q", p)
		}
		if !seen[p] {
			seen[p] = true
			clean = append(clean, p)
		}
	}
	sort.Strings(clean)
	spec.Indexes = clean
	if spec.Schema != nil {
		if _, err := json.Marshal(spec.Schema); err != nil {
			return KindSpec{}, fmt.Errorf("schema must be JSON-compatible: %w", err)
		}
	}
	schemaJSON, _ := json.Marshal(spec.Schema)
	indexJSON, _ := json.Marshal(spec.Indexes)
	spec.UpdatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KindSpec{}, err
	}
	defer tx.Rollback()
	var previous []string
	if old, err := kindInfo(ctx, tx, spec.Kind); err == nil && old != nil {
		previous = old.Indexes
	} else if err != nil {
		return KindSpec{}, err
	}
	exclude := 0
	if spec.ExcludeFromMemory {
		exclude = 1
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO kinds(kind,version,schema_json,indexes_json,exclude_from_memory,validation,updated_at) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(kind) DO UPDATE SET version=excluded.version,schema_json=excluded.schema_json,indexes_json=excluded.indexes_json,exclude_from_memory=excluded.exclude_from_memory,validation=excluded.validation,updated_at=excluded.updated_at`,
		spec.Kind, spec.Version, string(schemaJSON), string(indexJSON), exclude, spec.Validation, spec.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return KindSpec{}, err
	}
	for _, p := range previous {
		if !seen[p] {
			if _, err = tx.ExecContext(ctx, "DROP INDEX IF EXISTS "+indexName(spec.Kind, p)); err != nil {
				return KindSpec{}, err
			}
		}
	}
	for _, p := range spec.Indexes {
		stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON artifacts(kind, %s)", indexName(spec.Kind, p), fieldExpr(p))
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return KindSpec{}, fmt.Errorf("create index for %s: %w", p, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return KindSpec{}, err
	}
	return spec, nil
}

func indexName(kind, path string) string {
	sum := sha256.Sum256([]byte(kind + "|" + path))
	return "idx_kind_" + hex.EncodeToString(sum[:6])
}

// fieldExpr is the one spelling of a custom-field lookup. Queries and indexes
// must use exactly this text for SQLite to match them.
func fieldExpr(path string) string { return "json_extract(metadata_json,'$." + path + "')" }

func kindInfo(ctx context.Context, q queryRower, kind string) (*KindSpec, error) {
	var spec KindSpec
	var schemaJSON, indexJSON, updated string
	var exclude int
	err := q.QueryRowContext(ctx, `SELECT kind,version,schema_json,indexes_json,exclude_from_memory,validation,updated_at FROM kinds WHERE kind=?`, kind).
		Scan(&spec.Kind, &spec.Version, &schemaJSON, &indexJSON, &exclude, &spec.Validation, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(schemaJSON), &spec.Schema)
	_ = json.Unmarshal([]byte(indexJSON), &spec.Indexes)
	spec.ExcludeFromMemory = exclude == 1
	spec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return &spec, nil
}

// GetKind returns a registered kind, or nil when none is registered.
func (s *Store) GetKind(ctx context.Context, kind string) (*KindSpec, error) {
	return kindInfo(ctx, s.db, kind)
}

// ListKinds returns every registered kind with the number of live records.
func (s *Store) ListKinds(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT k.kind,k.version,k.schema_json,k.indexes_json,k.exclude_from_memory,k.validation,(SELECT count(*) FROM artifacts a WHERE a.kind=k.kind AND a.deleted_at IS NULL) FROM kinds k ORDER BY k.kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var kind, schemaJSON, indexJSON, validation string
		var version, exclude, count int
		if err := rows.Scan(&kind, &version, &schemaJSON, &indexJSON, &exclude, &validation, &count); err != nil {
			return nil, err
		}
		var schema any
		var indexes []string
		_ = json.Unmarshal([]byte(schemaJSON), &schema)
		_ = json.Unmarshal([]byte(indexJSON), &indexes)
		out = append(out, map[string]any{"kind": kind, "version": version, "schema": schema, "indexes": indexes, "exclude_from_memory": exclude == 1, "validation": validation, "count": count})
	}
	return out, rows.Err()
}

// excludedKinds lists kinds kept out of agent memory packs and default search.
func (s *Store) excludedKinds(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind FROM kinds WHERE exclude_from_memory=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// checkFields validates fields against the kind's schema. It returns warnings
// for kinds in "warn" mode and an error for kinds in "enforce" mode.
func checkFields(ctx context.Context, q queryRower, kind string, fields map[string]any) (warnings []string, schemaVersion int, err error) {
	spec, err := kindInfo(ctx, q, kind)
	if err != nil || spec == nil {
		return nil, 0, err
	}
	if spec.Validation == "off" || len(spec.Schema) == 0 {
		return nil, spec.Version, nil
	}
	var value any = fields
	if fields == nil {
		value = map[string]any{}
	}
	violations := validateJSON(spec.Schema, normalizeJSON(value))
	if len(violations) == 0 {
		return nil, spec.Version, nil
	}
	verr := &ValidationError{Kind: kind, Errors: violations}
	if spec.Validation == "warn" {
		return []string{verr.Error()}, spec.Version, nil
	}
	return nil, spec.Version, verr
}

// normalizeJSON round-trips a value so numbers are float64 and containers are
// map[string]any / []any, which is what the validator expects.
func normalizeJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

var relPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
