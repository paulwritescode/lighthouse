package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidQuery is returned for malformed where documents, paths or cursors.
var ErrInvalidQuery = errors.New("invalid query")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, a...))
}

// OrderKey sorts by a core column or a custom field path ("fields.due.date").
type OrderKey struct {
	Path string `json:"path"`
	Dir  string `json:"dir"`
}

// QuerySpec describes one structured query. Where is a JSON document, see
// compileWhere for the language.
type QuerySpec struct {
	Kind           string         `json:"kind"`
	Project        string         `json:"project"`
	Where          map[string]any `json:"where"`
	Order          []OrderKey     `json:"order"`
	Select         []string       `json:"select"`
	Limit          int            `json:"limit"`
	Cursor         string         `json:"cursor"`
	IncludeDeleted bool           `json:"include_deleted"`
	Count          bool           `json:"count"`
}

// QueryResult is one page. NextCursor is empty on the last page. Total is nil
// when the match count is above 10,000.
type QueryResult struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"next_cursor"`
	Total      *int             `json:"total,omitempty"`
	Count      *int             `json:"count,omitempty"`
}

const (
	maxPredicates = 40
	maxWhereDepth = 4
	maxPageSize   = 1000
	totalCap      = 10000
)

var coreColumns = map[string]string{
	"id": "id", "kind": "kind", "project": "project", "agent": "agent", "actor": "actor", "batch_id": "batch_id", "title": "title",
	"created_at": "created_at", "updated_at": "updated_at", "revision": "revision", "seq": "seq", "deleted_at": "deleted_at", "schema_version": "schema_version",
}

type compiler struct {
	args  []any
	count int
}

// pathExpr maps a path to a SQL expression. isField reports a custom field.
func pathExpr(path string) (expr, field string, isField bool, err error) {
	if col, ok := coreColumns[path]; ok {
		return col, "", false, nil
	}
	if path == "tags" {
		return "", "", false, nil
	}
	if strings.HasPrefix(path, "fields.") {
		p := strings.TrimPrefix(path, "fields.")
		if !fieldPathPattern.MatchString(p) {
			return "", "", false, invalid("bad path %q", path)
		}
		return fieldExpr(p), p, true, nil
	}
	return "", "", false, invalid("unknown path %q: use a core column or fields.<name>", path)
}

func bindable(v any) (any, error) {
	switch x := v.(type) {
	case nil, string, float64, int, int64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return nil, invalid("values must be strings, numbers, booleans or null")
}

func likeEscape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(v)
}

// compileWhere turns the where document into SQL. A document with several keys
// means AND. Reserved keys: and, or, not. Any other key is a path whose value
// is a bare value (equality) or an object of operators: eq, ne, in, nin, gt,
// gte, lt, lte, between, exists, prefix, contains, has.
func (c *compiler) compileWhere(node map[string]any, depth int) (string, error) {
	if depth > maxWhereDepth {
		return "", invalid("where is nested deeper than %d levels", maxWhereDepth)
	}
	var parts []string
	for key, val := range node {
		var clause string
		var err error
		switch key {
		case "and", "or":
			list, ok := val.([]any)
			if !ok || len(list) == 0 {
				return "", invalid("%s needs a non-empty list", key)
			}
			subs := make([]string, 0, len(list))
			for _, item := range list {
				m, ok := item.(map[string]any)
				if !ok {
					return "", invalid("%s entries must be objects", key)
				}
				sub, err := c.compileWhere(m, depth+1)
				if err != nil {
					return "", err
				}
				if sub != "" {
					subs = append(subs, sub)
				}
			}
			if len(subs) > 0 {
				op := " AND "
				if key == "or" {
					op = " OR "
				}
				clause = "(" + strings.Join(subs, op) + ")"
			}
		case "not":
			m, ok := val.(map[string]any)
			if !ok {
				return "", invalid("not needs an object")
			}
			sub, err := c.compileWhere(m, depth+1)
			if err != nil {
				return "", err
			}
			if sub != "" {
				clause = "NOT (" + sub + ")"
			}
		default:
			clause, err = c.compilePredicate(key, val)
			if err != nil {
				return "", err
			}
		}
		if clause != "" {
			parts = append(parts, clause)
		}
	}
	return strings.Join(parts, " AND "), nil
}

func (c *compiler) compilePredicate(path string, val any) (string, error) {
	ops, isOps := val.(map[string]any)
	if !isOps {
		ops = map[string]any{"eq": val}
	}
	expr, field, isField, err := pathExpr(path)
	if err != nil {
		return "", err
	}
	var parts []string
	for op, operand := range ops {
		c.count++
		if c.count > maxPredicates {
			return "", invalid("more than %d predicates", maxPredicates)
		}
		if path == "tags" {
			if op != "has" && op != "in" {
				return "", invalid("tags supports has and in")
			}
			values := []any{operand}
			if list, ok := operand.([]any); ok {
				values = list
			}
			if len(values) == 0 {
				return "", invalid("tags needs at least one value")
			}
			for _, v := range values {
				c.args = append(c.args, v)
			}
			parts = append(parts, "EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value IN ("+placeholders(len(values))+"))")
			continue
		}
		switch op {
		case "eq", "ne":
			v, err := bindable(operand)
			if err != nil {
				return "", err
			}
			if v == nil {
				if op == "eq" {
					parts = append(parts, expr+" IS NULL")
				} else {
					parts = append(parts, expr+" IS NOT NULL")
				}
				continue
			}
			c.args = append(c.args, v)
			if op == "eq" {
				parts = append(parts, expr+" = ?")
			} else {
				parts = append(parts, "("+expr+" IS NULL OR "+expr+" <> ?)")
			}
		case "gt", "gte", "lt", "lte":
			v, err := bindable(operand)
			if err != nil || v == nil {
				return "", invalid("%s needs a string, number or boolean", op)
			}
			c.args = append(c.args, v)
			parts = append(parts, expr+map[string]string{"gt": " > ?", "gte": " >= ?", "lt": " < ?", "lte": " <= ?"}[op])
		case "between":
			pair, ok := operand.([]any)
			if !ok || len(pair) != 2 {
				return "", invalid("between needs [low, high]")
			}
			lo, err1 := bindable(pair[0])
			hi, err2 := bindable(pair[1])
			if err1 != nil || err2 != nil || lo == nil || hi == nil {
				return "", invalid("between needs two scalar values")
			}
			c.args = append(c.args, lo, hi)
			parts = append(parts, expr+" BETWEEN ? AND ?")
		case "in", "nin":
			list, ok := operand.([]any)
			if !ok || len(list) == 0 || len(list) > 500 {
				return "", invalid("%s needs a list of 1 to 500 values", op)
			}
			for _, item := range list {
				v, err := bindable(item)
				if err != nil || v == nil {
					return "", invalid("%s values must be non-null scalars", op)
				}
				c.args = append(c.args, v)
			}
			not := ""
			if op == "nin" {
				not = "NOT "
			}
			parts = append(parts, expr+" "+not+"IN ("+placeholders(len(list))+")")
		case "exists":
			want, ok := operand.(bool)
			if !ok {
				return "", invalid("exists needs true or false")
			}
			var test string
			if isField {
				test = "json_type(metadata_json,'$." + field + "') IS NOT NULL"
			} else {
				test = expr + " IS NOT NULL"
			}
			if !want {
				test = "NOT (" + test + ")"
			}
			parts = append(parts, test)
		case "prefix", "contains":
			sv, ok := operand.(string)
			if !ok {
				return "", invalid("%s needs a string", op)
			}
			pattern := likeEscape(sv) + "%"
			if op == "contains" {
				pattern = "%" + likeEscape(strings.ToLower(sv)) + "%"
				parts = append(parts, "LOWER("+expr+") LIKE ? ESCAPE '\\'")
			} else {
				parts = append(parts, expr+" LIKE ? ESCAPE '\\'")
			}
			c.args = append(c.args, pattern)
		case "has":
			if !isField {
				return "", invalid("has works on fields.<name> arrays and on tags")
			}
			v, err := bindable(operand)
			if err != nil || v == nil {
				return "", invalid("has needs a non-null scalar")
			}
			c.args = append(c.args, v)
			parts = append(parts, "EXISTS (SELECT 1 FROM json_each(metadata_json,'$."+field+"') WHERE value = ?)")
		default:
			return "", invalid("unknown operator %q", op)
		}
	}
	return strings.Join(parts, " AND "), nil
}

type cursorState struct {
	Offset int   `json:"o"`
	AsOf   int64 `json:"s"`
}

func decodeCursor(raw string) (cursorState, error) {
	var st cursorState
	if raw == "" {
		return st, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &st) != nil || st.Offset < 0 {
		return st, invalid("cursor is not valid")
	}
	return st, nil
}

func encodeCursor(st cursorState) string {
	b, _ := json.Marshal(st)
	return base64.RawURLEncoding.EncodeToString(b)
}

var kindGlob = regexp.MustCompile(`^[a-z0-9_*]{1,64}$`)

// Query runs a structured query. Pages are stable: the first call pins the
// change sequence, and later pages ignore records written after it. A record
// edited mid-iteration drops out of the remaining pages rather than appearing
// twice.
func (s *Store) Query(ctx context.Context, spec QuerySpec) (QueryResult, error) {
	limit := spec.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > maxPageSize {
		return QueryResult{}, invalid("limit must be from 1 to %d", maxPageSize)
	}
	cur, err := decodeCursor(spec.Cursor)
	if err != nil {
		return QueryResult{}, err
	}
	c := &compiler{}
	var where []string
	if !spec.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if spec.Kind != "" {
		if !kindGlob.MatchString(spec.Kind) {
			return QueryResult{}, invalid("kind must be a name or a glob such as dock_*")
		}
		clause, args := globClause("kind", []string{spec.Kind})
		if clause != "" {
			where = append(where, clause)
			c.args = append(c.args, args...)
		}
	}
	if spec.Project != "" {
		where = append(where, "(project=? OR EXISTS (SELECT 1 FROM json_each(tags_json) WHERE value=?))")
		c.args = append(c.args, spec.Project, spec.Project)
	}
	if len(spec.Where) > 0 {
		clause, err := c.compileWhere(spec.Where, 1)
		if err != nil {
			return QueryResult{}, err
		}
		if clause != "" {
			where = append(where, clause)
		}
	}
	asOf := cur.AsOf
	if asOf == 0 {
		if asOf, err = s.Head(ctx); err != nil {
			return QueryResult{}, err
		}
	}
	where = append(where, "seq <= ?")
	c.args = append(c.args, asOf)
	whereSQL := " WHERE " + strings.Join(where, " AND ")
	if spec.Count {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM artifacts"+whereSQL, c.args...).Scan(&n); err != nil {
			return QueryResult{}, err
		}
		return QueryResult{Items: []map[string]any{}, Count: &n}, nil
	}
	var orderParts []string
	for _, k := range spec.Order {
		expr, _, _, err := pathExpr(k.Path)
		if err != nil || expr == "" {
			return QueryResult{}, invalid("cannot order by %q", k.Path)
		}
		dir := strings.ToUpper(k.Dir)
		if dir == "" {
			dir = "ASC"
		}
		if dir != "ASC" && dir != "DESC" {
			return QueryResult{}, invalid("dir must be asc or desc")
		}
		orderParts = append(orderParts, expr+" "+dir)
	}
	if len(orderParts) == 0 {
		orderParts = append(orderParts, "updated_at DESC")
	}
	orderParts = append(orderParts, "id ASC")
	sqlText := `SELECT ` + artifactColumns + ` FROM artifacts` + whereSQL + ` ORDER BY ` + strings.Join(orderParts, ", ") + ` LIMIT ? OFFSET ?`
	args := append(append([]any{}, c.args...), limit+1, cur.Offset)
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return QueryResult{}, err
	}
	defer rows.Close()
	artifacts, err := scanArtifacts(rows)
	if err != nil {
		return QueryResult{}, err
	}
	res := QueryResult{Items: make([]map[string]any, 0, len(artifacts))}
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
		res.NextCursor = encodeCursor(cursorState{Offset: cur.Offset + limit, AsOf: asOf})
	}
	for _, a := range artifacts {
		res.Items = append(res.Items, project(a, spec.Select))
	}
	if cur.Offset == 0 {
		var n int
		countSQL := "SELECT count(*) FROM (SELECT 1 FROM artifacts" + whereSQL + fmt.Sprintf(" LIMIT %d)", totalCap+1)
		if err := s.db.QueryRowContext(ctx, countSQL, c.args...).Scan(&n); err == nil && n <= totalCap {
			res.Total = &n
		}
	}
	return res, nil
}

// project returns the record as a map, reduced to the selected paths.
func project(a Artifact, selected []string) map[string]any {
	b, _ := json.Marshal(a)
	var full map[string]any
	_ = json.Unmarshal(b, &full)
	if len(selected) == 0 {
		return full
	}
	out := map[string]any{}
	for _, path := range selected {
		parts := strings.Split(path, ".")
		var cur any = full
		ok := true
		for _, p := range parts {
			m, isMap := cur.(map[string]any)
			if !isMap {
				ok = false
				break
			}
			if cur, ok = m[p]; !ok {
				break
			}
		}
		if ok {
			out[path] = cur
		}
	}
	return out
}
