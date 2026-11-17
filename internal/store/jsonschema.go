package store

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// FieldError is one schema violation with the path of the offending value.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError lists every violation found, not only the first.
type ValidationError struct {
	Kind   string       `json:"kind"`
	Errors []FieldError `json:"errors"`
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, f := range e.Errors {
		parts = append(parts, f.Path+": "+f.Message)
	}
	return fmt.Sprintf("%s fields failed validation: %s", e.Kind, strings.Join(parts, "; "))
}

const maxSchemaDepth = 24

// validateJSON checks value against a JSON Schema subset: type, enum, const,
// properties, required, additionalProperties, items, minItems, maxItems,
// minimum, maximum, minLength, maxLength, pattern, oneOf, anyOf, allOf and
// local $ref into $defs. Unknown keywords are ignored so newer schemas still
// load; they just are not enforced.
func validateJSON(schema map[string]any, value any) []FieldError {
	var errs []FieldError
	validateNode(schema, schema, value, "fields", 0, &errs)
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
	return errs
}

func validateNode(root, schema map[string]any, value any, path string, depth int, errs *[]FieldError) {
	if depth > maxSchemaDepth || len(*errs) >= 50 {
		return
	}
	if ref, ok := schema["$ref"].(string); ok {
		target, ok := resolveRef(root, ref)
		if !ok {
			*errs = append(*errs, FieldError{path, "unresolvable schema reference " + ref})
			return
		}
		validateNode(root, target, value, path, depth+1, errs)
		return
	}
	add := func(format string, a ...any) { *errs = append(*errs, FieldError{path, fmt.Sprintf(format, a...)}) }
	if t, ok := schema["type"]; ok && !typeMatches(t, value) {
		add("expected %s, got %s", typeLabel(t), jsonTypeName(value))
		return
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if jsonEqual(e, value) {
				found = true
				break
			}
		}
		if !found {
			add("must be one of %s", joinValues(enum))
		}
	}
	if c, ok := schema["const"]; ok && !jsonEqual(c, value) {
		add("must equal %v", c)
	}
	for _, key := range []string{"allOf"} {
		if subs, ok := schema[key].([]any); ok {
			for _, sub := range subs {
				if m, ok := sub.(map[string]any); ok {
					validateNode(root, m, value, path, depth+1, errs)
				}
			}
		}
	}
	if subs, ok := schema["anyOf"].([]any); ok && !anyValid(root, subs, value, depth) {
		add("does not match any allowed shape")
	}
	if subs, ok := schema["oneOf"].([]any); ok {
		if n := countValid(root, subs, value, depth); n != 1 {
			add("must match exactly one allowed shape, matched %d", n)
		}
	}
	switch v := value.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, present := v[name]; !present {
						*errs = append(*errs, FieldError{path + "." + name, "is required"})
					}
				}
			}
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub, ok := props[k].(map[string]any); ok {
				validateNode(root, sub, v[k], path+"."+k, depth+1, errs)
				continue
			}
			switch ap := schema["additionalProperties"].(type) {
			case bool:
				if !ap {
					*errs = append(*errs, FieldError{path + "." + k, "is not allowed"})
				}
			case map[string]any:
				validateNode(root, ap, v[k], path+"."+k, depth+1, errs)
			}
		}
	case []any:
		if n, ok := number(schema["minItems"]); ok && float64(len(v)) < n {
			add("needs at least %d items", int(n))
		}
		if n, ok := number(schema["maxItems"]); ok && float64(len(v)) > n {
			add("allows at most %d items", int(n))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range v {
				validateNode(root, items, item, fmt.Sprintf("%s[%d]", path, i), depth+1, errs)
			}
		}
	case string:
		if n, ok := number(schema["minLength"]); ok && float64(utf8.RuneCountInString(v)) < n {
			add("must be at least %d characters", int(n))
		}
		if n, ok := number(schema["maxLength"]); ok && float64(utf8.RuneCountInString(v)) > n {
			add("must be at most %d characters", int(n))
		}
		if p, ok := schema["pattern"].(string); ok && len(p) <= 200 {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(v) {
				add("does not match pattern %s", p)
			}
		}
	case float64:
		if n, ok := number(schema["minimum"]); ok && v < n {
			add("must be at least %v", n)
		}
		if n, ok := number(schema["maximum"]); ok && v > n {
			add("must be at most %v", n)
		}
	}
}

func anyValid(root map[string]any, subs []any, value any, depth int) bool {
	return countValid(root, subs, value, depth) > 0
}

func countValid(root map[string]any, subs []any, value any, depth int) int {
	n := 0
	for _, sub := range subs {
		m, ok := sub.(map[string]any)
		if !ok {
			continue
		}
		var errs []FieldError
		validateNode(root, m, value, "fields", depth+1, &errs)
		if len(errs) == 0 {
			n++
		}
	}
	return n
}

func resolveRef(root map[string]any, ref string) (map[string]any, bool) {
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, false
	}
	defs, _ := root["$defs"].(map[string]any)
	target, ok := defs[strings.TrimPrefix(ref, prefix)].(map[string]any)
	return target, ok
}

func typeMatches(t any, value any) bool {
	switch x := t.(type) {
	case string:
		return singleType(x, value)
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok && singleType(s, value) {
				return true
			}
		}
	}
	return false
}

func singleType(name string, value any) bool {
	switch name {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		return isNumber(value)
	case "integer":
		f, ok := toFloat(value)
		return ok && f == math.Trunc(f)
	}
	return true
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	if isNumber(v) {
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

func typeLabel(t any) string {
	switch x := t.(type) {
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, i := range x {
			parts = append(parts, fmt.Sprint(i))
		}
		return strings.Join(parts, " or ")
	}
	return fmt.Sprint(t)
}

func isNumber(v any) bool { _, ok := toFloat(v); return ok }

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}

func number(v any) (float64, bool) { return toFloat(v) }

func jsonEqual(a, b any) bool {
	if fa, ok := toFloat(a); ok {
		if fb, ok := toFloat(b); ok {
			return fa == fb
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b) && jsonTypeName(a) == jsonTypeName(b)
}

func joinValues(values []any) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprint(v))
	}
	return strings.Join(parts, ", ")
}
