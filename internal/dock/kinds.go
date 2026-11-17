// Package dock implements the optional `dock` module: the record kinds that
// the Dock desktop app stores in Lighthouse, and the safe, proposal-only
// helpers that let agents create plans and questions for it.
package dock

import (
	"context"

	"github.com/paulwritescode/lighthouse/internal/store"
)

// Kind names used by Dock.
const (
	KindTask    = "dock_task"
	KindEvent   = "dock_event"
	KindPlan    = "dock_plan"
	KindAsk     = "dock_ask"
	KindProject = "dock_project"
)

type m = map[string]any

func str(enum ...string) m {
	out := m{"type": "string"}
	if len(enum) > 0 {
		list := make([]any, len(enum))
		for i, e := range enum {
			list[i] = e
		}
		out["enum"] = list
	}
	return out
}

func obj(required []string, props m) m {
	out := m{"type": "object", "properties": props}
	if len(required) > 0 {
		list := make([]any, len(required))
		for i, r := range required {
			list[i] = r
		}
		out["required"] = list
	}
	return out
}

func nullable(t string) m { return m{"type": []any{t, "null"}} }

var (
	date      = m{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	boolean   = m{"type": "boolean"}
	integer   = m{"type": "integer"}
	stringArr = m{"type": "array", "items": m{"type": "string"}}
	anyObject = m{"type": "object"}
	anyArray  = m{"type": "array"}
)

var taskStatuses = []string{"proposed", "todo", "doing", "reschedule", "done", "cancelled"}

// Kinds returns the Dock kinds with their schemas and indexes. Schemas
// validate the record's fields and leave unknown fields alone, so newer Dock
// versions can add data without breaking older Lighthouse builds.
func Kinds() []store.KindSpec {
	base := func(kind string, schema m, indexes ...string) store.KindSpec {
		return store.KindSpec{Kind: kind, Version: 1, Schema: schema, Indexes: indexes, ExcludeFromMemory: true, Validation: "enforce"}
	}
	return []store.KindSpec{
		base(KindTask, obj([]string{"status"}, m{
			"type":          str("task", "reminder"),
			"status":        str(taskStatuses...),
			"priority":      str("low", "normal", "high", "urgent"),
			"due":           obj([]string{"date"}, m{"date": date, "time": nullable("string"), "tz": nullable("string")}),
			"scheduled":     anyObject,
			"estimate_min":  nullable("integer"),
			"actual_min":    nullable("integer"),
			"phase_id":      nullable("string"),
			"tags":          stringArr,
			"subtasks":      anyArray,
			"depends_on":    stringArr,
			"alerts":        anyArray,
			"series_id":     nullable("string"),
			"source":        anyObject,
			"reschedule":    anyObject,
			"plan_id":       nullable("string"),
			"mirror":        str("auto", "none", "tasks", "calendar"),
			"next_alert_at": nullable("string"),
		}), "fields.status", "fields.due.date", "fields.next_alert_at", "fields.plan_id", "fields.priority", "fields.series_id", "fields.phase_id", "fields.source.plan_id"),
		base(KindEvent, obj([]string{"when"}, m{
			"when":          anyObject,
			"status":        str("confirmed", "tentative", "cancelled"),
			"event_type":    str("default", "focusTime", "outOfOffice", "workingLocation", "birthday", "fromGmail"),
			"attendees":     anyArray,
			"alerts":        anyArray,
			"series_id":     nullable("string"),
			"calendar":      anyObject,
			"next_alert_at": nullable("string"),
		}), "fields.when.start_at", "fields.when.start_date", "fields.series_id", "fields.next_alert_at", "fields.calendar.calendar_id"),
		base("dock_series", obj([]string{"entity", "rrule"}, m{"entity": str("task", "event"), "rrule": m{"type": "string"}, "paused": boolean, "materialized_until": nullable("string")}), "fields.materialized_until", "fields.paused"),
		base(KindPlan, obj([]string{"status"}, m{
			"status":        str("needs_input", "generating", "proposed", "active", "completed", "archived", "failed"),
			"source_ids":    stringArr,
			"source_hashes": anyObject,
			"window":        anyObject,
			"constraints":   anyObject,
			"ask_id":        nullable("string"),
			"version":       integer,
			"summary":       m{"type": "string"},
			"metrics":       anyObject,
			"health":        nullable("string"),
			"fit_state":     str("agent_hint", "fitted", "needs_refit"),
			"item_ids":      stringArr,
			"spec_hash":     m{"type": "string"},
		}), "fields.status"),
		base(KindAsk, obj([]string{"status", "spec"}, m{
			"status":       str("pending", "answered", "expired", "cancelled", "superseded"),
			"requested_by": anyObject,
			"urgency":      str("blocking", "normal", "low"),
			"subject_ids":  stringArr,
			"spec":         anyObject,
			"defaults":     anyObject,
			"auto_apply":   boolean,
			"expires_at":   nullable("string"),
			"answers":      m{"type": []any{"object", "null"}},
			"answered_at":  nullable("string"),
			"on_answer":    anyObject,
		}), "fields.status", "fields.urgency", "fields.expires_at", "fields.answered_at"),
		base("dock_rule", obj([]string{"enabled"}, m{"enabled": boolean, "origin": str("builtin", "user", "ai"), "trigger": anyObject, "then": anyArray}), "fields.enabled"),
		base("dock_rule_run", obj([]string{"status"}, m{"status": str("running", "waiting", "done", "failed", "skipped"), "resume_at": nullable("string")}), "fields.status", "fields.resume_at"),
		base("dock_link", obj([]string{"local_id", "google_id"}, m{"local_id": m{"type": "string"}, "google_id": m{"type": "string"}, "state": str("clean", "dirty_local", "dirty_remote", "conflict", "error", "deleted_remote", "deleted_local")}), "fields.google_id", "fields.local_id", "fields.state"),
		base("dock_sync", obj([]string{"account_id", "calendar_id"}, m{"account_id": m{"type": "string"}, "calendar_id": m{"type": "string"}}), "fields.calendar_id"),
		base("dock_fire", obj([]string{"owner_id", "occurrence_key"}, m{"owner_id": m{"type": "string"}, "occurrence_key": m{"type": "string"}, "outcome": m{"type": "string"}}), "fields.occurrence_key", "fields.due_at"),
		base("dock_run", obj(nil, m{"purpose": m{"type": "string"}, "provider": m{"type": "string"}, "status": m{"type": "string"}})),
		base(KindProject, obj(nil, m{"key": m{"type": "string"}, "archived": boolean})),
		base("dock_phase", obj(nil, m{"start_date": nullable("string"), "end_date": nullable("string")})),
		base("dock_template", obj([]string{"entity"}, m{"entity": str("task", "event_prep")})),
		base("dock_settings", obj([]string{"scope"}, m{"scope": m{"type": "string"}}), "fields.scope"),
		base("dock_update", obj(nil, m{"body": m{"type": "string"}})),
	}
}

// Install registers every Dock kind. It is safe to run repeatedly.
func Install(ctx context.Context, s *store.Store) ([]store.KindSpec, error) {
	var out []store.KindSpec
	for _, spec := range Kinds() {
		got, err := s.RegisterKind(ctx, spec)
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	return out, nil
}

// Installed reports whether the core Dock kinds are registered.
func Installed(ctx context.Context, s *store.Store) bool {
	for _, kind := range []string{KindTask, KindPlan, KindAsk} {
		if spec, err := s.GetKind(ctx, kind); err != nil || spec == nil {
			return false
		}
	}
	return true
}
