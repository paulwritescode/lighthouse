package dock

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Error is a failure with a stable code that agents and the API can match.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func fail(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Limits for generative UI specs written by agents.
const (
	maxSpecBytes  = 64 << 10
	maxSections   = 8
	maxFields     = 60
	maxOptions    = 200
	maxNodeDepth  = 4
	maxOpenAsks   = 5
	maxPlanItems  = 120
	maxNotesBytes = 64 << 10
)

// inputTypes and displayTypes are the closed component catalog (version 1).
var inputTypes = set("text", "textarea", "number", "slider", "select", "radio", "chips", "multichips", "toggle", "date", "daterange",
	"time", "timerange", "datetime", "duration", "weekdays", "tags", "project", "task_picker", "slot_picker", "record_picker", "rating", "keyvalue")

var displayTypes = set("heading", "text", "divider", "callout", "stat", "progress", "badge", "list", "table", "timeline", "heatmap",
	"task_card", "event_card", "record_card", "diff")

func set(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, i := range items {
		out[i] = true
	}
	return out
}

var actionKinds = set("submit", "secondary", "destructive", "defer", "open")

// ValidateUISpec checks a generative screen against the component catalog and
// limits. Only catalog components are allowed, so a spec can never carry code.
func ValidateUISpec(spec map[string]any) error {
	raw, err := json.Marshal(spec)
	if err != nil {
		return fail("validation_failed", "spec must be JSON-compatible: %v", err)
	}
	if len(raw) > maxSpecBytes {
		return fail("validation_failed", "spec is %d bytes; the limit is %d", len(raw), maxSpecBytes)
	}
	if v, _ := spec["version"].(float64); v != 1 && spec["version"] != int64(1) && spec["version"] != 1 {
		return fail("validation_failed", "spec.version must be 1")
	}
	if title, _ := spec["title"].(string); strings.TrimSpace(title) == "" {
		return fail("validation_failed", "spec.title is required")
	}
	sections, _ := spec["sections"].([]any)
	if len(sections) == 0 || len(sections) > maxSections {
		return fail("validation_failed", "spec.sections needs 1 to %d sections", maxSections)
	}
	fields, ids := 0, map[string]bool{}
	for i, sec := range sections {
		sm, ok := sec.(map[string]any)
		if !ok {
			return fail("validation_failed", "section %d must be an object", i)
		}
		nodes, _ := sm["nodes"].([]any)
		for j, n := range nodes {
			if err := validateNode(n, 1, &fields, ids); err != nil {
				return fail("validation_failed", "section %d, node %d: %v", i, j, err)
			}
		}
	}
	if fields > maxFields {
		return fail("validation_failed", "spec has %d fields; the limit is %d", fields, maxFields)
	}
	actions, _ := spec["actions"].([]any)
	for i, a := range actions {
		am, ok := a.(map[string]any)
		if !ok {
			return fail("validation_failed", "action %d must be an object", i)
		}
		if kind, _ := am["kind"].(string); !actionKinds[kind] {
			return fail("validation_failed", "action %d has unknown kind %q", i, kind)
		}
		if label, _ := am["label"].(string); strings.TrimSpace(label) == "" {
			return fail("validation_failed", "action %d needs a label", i)
		}
		if open, ok := am["open"].(map[string]any); ok {
			if route, _ := open["route"].(string); !strings.HasPrefix(route, "/") || strings.Contains(route, "://") {
				return fail("validation_failed", "action %d may only open an in-app route", i)
			}
		}
	}
	return nil
}

func validateNode(n any, depth int, fields *int, ids map[string]bool) error {
	if depth > maxNodeDepth {
		return fmt.Errorf("nesting deeper than %d levels", maxNodeDepth)
	}
	nm, ok := n.(map[string]any)
	if !ok {
		return fmt.Errorf("a node must be an object")
	}
	t, _ := nm["type"].(string)
	switch {
	case inputTypes[t]:
		*fields++
		id, _ := nm["id"].(string)
		if id == "" || len(id) > 64 {
			return fmt.Errorf("field needs an id of up to 64 characters")
		}
		if ids[id] || strings.HasPrefix(id, "__") {
			return fmt.Errorf("field id %q is reused or reserved", id)
		}
		ids[id] = true
		if label, _ := nm["label"].(string); strings.TrimSpace(label) == "" {
			return fmt.Errorf("field %q needs a label", id)
		}
		if opts, ok := nm["options"].([]any); ok && len(opts) > maxOptions {
			return fmt.Errorf("field %q has %d options; the limit is %d", id, len(opts), maxOptions)
		}
		if from, ok := nm["options"].(map[string]any); ok {
			if err := validateOptionsSource(from); err != nil {
				return fmt.Errorf("field %q: %v", id, err)
			}
		}
	case displayTypes[t]:
	default:
		return fmt.Errorf("unknown component %q", t)
	}
	if children, ok := nm["children"].([]any); ok {
		for _, c := range children {
			if err := validateNode(c, depth+1, fields, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateOptionsSource allows only the sources Dock resolves itself. Agents
// cannot run free-form queries through a spec.
func validateOptionsSource(opts map[string]any) error {
	from, _ := opts["from"].(map[string]any)
	if from == nil {
		return fmt.Errorf("options must be a list or {from: {...}}")
	}
	for key := range from {
		switch key {
		case "static", "projects", "tags", "working_days":
		case "query":
			q, _ := from["query"].(map[string]any)
			kind, _ := q["kind"].(string)
			if kind != "dock_task" && kind != "dock_event" && kind != "dock_project" && kind != "dock_phase" {
				return fmt.Errorf("a query option source may only read Dock tasks, events, projects or phases")
			}
		case "scheduler":
		default:
			return fmt.Errorf("unknown options source %q", key)
		}
	}
	return nil
}

// Form builds a spec from a flat list of fields.
func Form(title string, fields []any, actions []any, subtitle, project string) map[string]any {
	if len(actions) == 0 {
		actions = []any{map[string]any{"id": "submit", "label": "Submit", "kind": "submit"}}
	}
	spec := map[string]any{
		"version":  1,
		"screen":   "custom_form",
		"title":    title,
		"sections": []any{map[string]any{"id": "main", "nodes": fields}},
		"actions":  actions,
	}
	if subtitle != "" {
		spec["subtitle"] = subtitle
	}
	if project != "" {
		spec["project"] = project
	}
	return spec
}
