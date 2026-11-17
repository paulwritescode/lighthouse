package codecall

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/paulwritescode/lighthouse/internal/dock"
	"github.com/paulwritescode/lighthouse/internal/store"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// jsonValue is a Starlark value that is converted to plain JSON data.
func jsonValue(v starlark.Value) any {
	if v == nil {
		return nil
	}
	return valueJSON(v)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func asInt64(v any) *int64 {
	switch n := v.(type) {
	case int64:
		return &n
	case float64:
		i := int64(n)
		return &i
	}
	return nil
}

// structuredError formats a store failure so an agent can read its code.
func structuredError(err error) error {
	var conflict *store.RevisionConflictError
	var verr *store.ValidationError
	var derr *dock.Error
	switch {
	case errors.As(err, &conflict):
		b, _ := json.Marshal(map[string]any{"code": "revision_conflict", "current": conflict.Current})
		return fmt.Errorf("revision_conflict: %s", b)
	case errors.As(err, &verr):
		b, _ := json.Marshal(map[string]any{"code": "validation_failed", "details": map[string]any{"errors": verr.Errors}})
		return fmt.Errorf("validation_failed: %s", b)
	case errors.As(err, &derr):
		return fmt.Errorf("%s", derr.Error())
	case errors.Is(err, store.ErrIDExists):
		return errors.New("id_exists: " + err.Error())
	case errors.Is(err, store.ErrDeleted):
		return errors.New("deleted: " + err.Error())
	case errors.Is(err, store.ErrInvalidQuery):
		return errors.New("invalid_query: " + err.Error())
	case errors.Is(err, store.ErrNotFound):
		return errors.New("not_found: " + err.Error())
	}
	return err
}

func (r Runner) writeOpts(actor, batch string) store.WriteOptions {
	if actor == "" {
		actor = r.Actor
	}
	if actor == "" {
		actor = "agent"
	}
	return store.WriteOptions{Actor: actor, BatchID: batch}
}

func (r Runner) save(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var title, content, project, agent, kind, id, actor, batch string
	var fields *starlark.Dict
	var tags *starlark.List
	agent, kind = "agent", "result"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "title", &title, "content", &content, "project?", &project, "agent?", &agent, "kind?", &kind, "fields?", &fields, "tags?", &tags, "id?", &id, "actor?", &actor, "batch_id?", &batch); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	custom, err := fieldsMap(fields)
	if err != nil {
		return nil, err
	}
	tagNames, err := starlarkTags(tags)
	if err != nil {
		return nil, err
	}
	res, err := r.Store.SaveArtifactWith(r.Context, store.SaveInput{ID: id, Title: title, Content: content, Project: project, Agent: agent, Kind: kind, Fields: custom, Tags: tagNames}, r.writeOpts(actor, batch))
	if err != nil {
		return nil, structuredError(err)
	}
	out := map[string]any{"artifact": res.Artifact}
	if len(res.Warnings) > 0 {
		out["warnings"] = res.Warnings
	}
	return r.writeResponse(out, project)
}

func (r Runner) update(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id, merge, actor, batch string
	var fields *starlark.Dict
	var unset *starlark.List
	var ifRev starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id, "fields?", &fields, "if_revision?", &ifRev, "merge?", &merge, "unset?", &unset, "actor?", &actor, "batch_id?", &batch); err != nil {
		return nil, err
	}
	patch, err := fieldsMap(fields)
	if err != nil {
		return nil, err
	}
	unsetPaths, err := starlarkTags(unset)
	if err != nil {
		return nil, err
	}
	opts := store.UpdateOptions{WriteOptions: r.writeOpts(actor, batch), IfRevision: asInt64(jsonValue(ifRev)), Merge: merge, Unset: unsetPaths}
	res, err := r.Store.UpdateArtifactWith(r.Context, id, patch, opts)
	if err != nil {
		return nil, structuredError(err)
	}
	out := map[string]any{"artifact": res.Artifact}
	if len(res.Warnings) > 0 {
		out["warnings"] = res.Warnings
	}
	if res.Unchanged {
		out["unchanged"] = true
	}
	return r.writeResponse(out, res.Artifact.Project)
}

func (r Runner) deleteArtifact(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id, actor, batch string
	soft := false
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id, "soft?", &soft, "actor?", &actor, "batch_id?", &batch); err != nil {
		return nil, err
	}
	a, err := r.Store.DeleteArtifactWith(r.Context, id, soft, r.writeOpts(actor, batch))
	if err != nil {
		return nil, structuredError(err)
	}
	key := "deleted_id"
	if soft {
		key = "soft_deleted_id"
	}
	return r.writeResponse(map[string]any{key: id}, a.Project)
}

func (r Runner) restore(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id, actor, batch string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id, "actor?", &actor, "batch_id?", &batch); err != nil {
		return nil, err
	}
	a, err := r.Store.RestoreArtifact(r.Context, id, r.writeOpts(actor, batch))
	if err != nil {
		return nil, structuredError(err)
	}
	return r.writeResponse(map[string]any{"artifact": a}, a.Project)
}

func (r Runner) purge(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	olderThan := -1
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id?", &id, "older_than_days?", &olderThan); err != nil {
		return nil, err
	}
	switch {
	case id != "":
		a, err := r.Store.GetArtifact(r.Context, id)
		if err != nil {
			return nil, structuredError(err)
		}
		if a.DeletedAt == nil {
			return nil, errors.New("forbidden: only a soft-deleted record can be purged; delete it first")
		}
		if _, err = r.Store.DeleteArtifactWith(r.Context, id, false, r.writeOpts("", "")); err != nil {
			return nil, structuredError(err)
		}
		return toStarlark(map[string]any{"purged": 1})
	case olderThan >= 0:
		n, err := r.Store.PurgeDeleted(r.Context, olderThan, r.writeOpts("", ""))
		if err != nil {
			return nil, err
		}
		return toStarlark(map[string]any{"purged": n})
	}
	return nil, errors.New("purge needs id or older_than_days")
}

func parseOrder(v any) []store.OrderKey {
	list, _ := v.([]any)
	out := make([]store.OrderKey, 0, len(list))
	for _, item := range list {
		switch x := item.(type) {
		case map[string]any:
			p, _ := x["path"].(string)
			d, _ := x["dir"].(string)
			out = append(out, store.OrderKey{Path: p, Dir: d})
		case string:
			out = append(out, store.OrderKey{Path: x})
		case []any:
			if len(x) == 2 {
				p, _ := x[0].(string)
				d, _ := x[1].(string)
				out = append(out, store.OrderKey{Path: p, Dir: d})
			}
		}
	}
	return out
}

func (r Runner) query(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var kind, project, cursor string
	var where *starlark.Dict
	var order, sel starlark.Value
	limit := 50
	var includeDeleted, count bool
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "kind?", &kind, "project?", &project, "where?", &where, "order?", &order, "select?", &sel, "limit?", &limit, "cursor?", &cursor, "include_deleted?", &includeDeleted, "count?", &count); err != nil {
		return nil, err
	}
	w, err := fieldsMap(where)
	if err != nil {
		return nil, err
	}
	res, err := r.Store.Query(r.Context, store.QuerySpec{Kind: kind, Project: project, Where: w, Order: parseOrder(jsonValue(order)), Select: asStrings(jsonValue(sel)), Limit: limit, Cursor: cursor, IncludeDeleted: includeDeleted, Count: count})
	if err != nil {
		return nil, structuredError(err)
	}
	return toStarlark(res)
}

func saveInputFrom(m map[string]any) store.SaveInput {
	in := store.SaveInput{Fields: asMap(m["fields"]), Tags: asStrings(m["tags"])}
	in.ID, _ = m["id"].(string)
	in.Title, _ = m["title"].(string)
	in.Content, _ = m["content"].(string)
	in.Project, _ = m["project"].(string)
	in.Agent, _ = m["agent"].(string)
	in.Kind, _ = m["kind"].(string)
	if in.Agent == "" {
		in.Agent = "agent"
	}
	return in
}

func batchOpFrom(m map[string]any) store.BatchOp {
	op := store.BatchOp{Patch: asMap(m["fields"]), Merge: "", Unset: asStrings(m["unset"]), IfRevision: asInt64(m["if_revision"])}
	op.Op, _ = m["op"].(string)
	op.ID, _ = m["id"].(string)
	op.Merge, _ = m["merge"].(string)
	if soft, ok := m["soft"].(bool); ok {
		op.Soft = &soft
	}
	if op.Op == "save" {
		in := saveInputFrom(m)
		op.Save = &in
	}
	return op
}

func (r Runner) runBatch(ops []store.BatchOp, atomic bool, actor, batch string) (starlark.Value, error) {
	res, err := r.Store.Batch(r.Context, ops, store.BatchOptions{WriteOptions: r.writeOpts(actor, batch), Atomic: atomic})
	if err != nil {
		return nil, structuredError(err)
	}
	return toStarlark(res)
}

func (r Runner) saveMany(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var items starlark.Value
	var batch, actor string
	atomic := true
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "items", &items, "batch_id?", &batch, "atomic?", &atomic, "actor?", &actor); err != nil {
		return nil, err
	}
	list, _ := jsonValue(items).([]any)
	ops := make([]store.BatchOp, 0, len(list))
	for _, item := range list {
		in := saveInputFrom(asMap(item))
		ops = append(ops, store.BatchOp{Op: "save", Save: &in})
	}
	return r.runBatch(ops, atomic, actor, batch)
}

func (r Runner) updateMany(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var items starlark.Value
	var batch, actor string
	atomic := true
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "updates", &items, "batch_id?", &batch, "atomic?", &atomic, "actor?", &actor); err != nil {
		return nil, err
	}
	list, _ := jsonValue(items).([]any)
	ops := make([]store.BatchOp, 0, len(list))
	for _, item := range list {
		m := asMap(item)
		m["op"] = "update"
		ops = append(ops, batchOpFrom(m))
	}
	return r.runBatch(ops, atomic, actor, batch)
}

func (r Runner) batch(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var items starlark.Value
	var batch, actor string
	atomic := true
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "ops", &items, "batch_id?", &batch, "atomic?", &atomic, "actor?", &actor); err != nil {
		return nil, err
	}
	list, _ := jsonValue(items).([]any)
	ops := make([]store.BatchOp, 0, len(list))
	for _, item := range list {
		ops = append(ops, batchOpFrom(asMap(item)))
	}
	return r.runBatch(ops, atomic, actor, batch)
}

func (r Runner) batchUndo(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id, actor string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "batch_id", &id, "actor?", &actor); err != nil {
		return nil, err
	}
	res, err := r.Store.BatchUndo(r.Context, id, r.writeOpts(actor, ""))
	if err != nil {
		return nil, structuredError(err)
	}
	return toStarlark(res)
}

func (r Runner) changes(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var since, limit = 0, 500
	var kinds, projects, actorsNot starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "since?", &since, "limit?", &limit, "kinds?", &kinds, "projects?", &projects, "actors_not?", &actorsNot); err != nil {
		return nil, err
	}
	page, err := r.Store.Changes(r.Context, store.ChangeQuery{Since: int64(since), Limit: limit, Kinds: asStrings(jsonValue(kinds)), Projects: asStrings(jsonValue(projects)), ActorsNot: asStrings(jsonValue(actorsNot))})
	if err != nil {
		return nil, err
	}
	return toStarlark(page)
}

func (r Runner) link(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var from, rel, to string
	var meta *starlark.Dict
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "from_id", &from, "rel", &rel, "to_id", &to, "meta?", &meta); err != nil {
		return nil, err
	}
	m, err := fieldsMap(meta)
	if err != nil {
		return nil, err
	}
	l, err := r.Store.AddLink(r.Context, from, rel, to, m)
	if err != nil {
		return nil, structuredError(err)
	}
	return toStarlark(l)
}

func (r Runner) unlink(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var from, rel, to string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "from_id", &from, "rel", &rel, "to_id", &to); err != nil {
		return nil, err
	}
	ok, err := r.Store.RemoveLink(r.Context, from, rel, to)
	if err != nil {
		return nil, err
	}
	return toStarlark(map[string]any{"removed": ok})
}

func (r Runner) links(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id, rel, direction string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id, "rel?", &rel, "direction?", &direction); err != nil {
		return nil, err
	}
	out, err := r.Store.Links(r.Context, id, rel, direction)
	if err != nil {
		return nil, err
	}
	return toStarlark(out)
}

func (r Runner) registerKind(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var kind, validation string
	var schema *starlark.Dict
	var indexes starlark.Value
	version := 1
	var exclude bool
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "kind", &kind, "schema?", &schema, "indexes?", &indexes, "version?", &version, "exclude_from_memory?", &exclude, "validation?", &validation); err != nil {
		return nil, err
	}
	sm, err := fieldsMap(schema)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	if list, ok := jsonValue(indexes).([]any); ok {
		for _, item := range list {
			switch x := item.(type) {
			case string:
				paths = append(paths, x)
			case map[string]any:
				if p, ok := x["path"].(string); ok {
					paths = append(paths, p)
				}
			}
		}
	}
	spec, err := r.Store.RegisterKind(r.Context, store.KindSpec{Kind: kind, Version: version, Schema: sm, Indexes: paths, ExcludeFromMemory: exclude, Validation: validation})
	if err != nil {
		return nil, err
	}
	return toStarlark(spec)
}

func (r Runner) describe(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var project, kind string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project, "kind?", &kind); err != nil {
		return nil, err
	}
	if kind != "" {
		spec, err := r.Store.GetKind(r.Context, kind)
		if err != nil {
			return nil, err
		}
		if spec == nil {
			return toStarlark(map[string]any{"kind": kind, "registered": false})
		}
		counts, _ := r.Store.Query(r.Context, store.QuerySpec{Kind: kind, Count: true})
		return toStarlark(map[string]any{"kind": kind, "registered": true, "spec": spec, "count": counts.Count})
	}
	if project == "" {
		project = r.Project
	}
	v, err := r.Store.Describe(r.Context, project)
	if err != nil {
		return nil, err
	}
	if kinds, err := r.Store.ListKinds(r.Context); err == nil && len(kinds) > 0 {
		v["kinds"] = kinds
	}
	return toStarlark(v)
}

func (r Runner) loadMemory(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var project, session string
	var include starlark.Value
	limit := 10
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project, "session?", &session, "limit?", &limit, "include?", &include); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	if session == "" {
		session = r.Session
	}
	p, err := r.Store.LoadContext(r.Context, session, project, limit)
	if err != nil {
		return nil, err
	}
	if p.Artifacts == nil {
		p.Artifacts = []store.Artifact{}
	}
	if p.Memories == nil {
		p.Memories = []store.Memory{}
	}
	v, err := toStarlark(p)
	if err != nil {
		return nil, err
	}
	for _, name := range asStrings(jsonValue(include)) {
		if name != "dock" {
			return nil, fmt.Errorf("load_memory: unknown include %q (supported: dock)", name)
		}
		section, err := r.dockEnv().ContextSection(r.Context, project)
		if err != nil {
			return nil, structuredError(err)
		}
		sv, err := toStarlark(section)
		if err != nil {
			return nil, err
		}
		if d, ok := v.(*starlark.Dict); ok {
			_ = d.SetKey(starlark.String("dock"), sv)
		}
	}
	return v, nil
}

// ---- dock module ----

func (r Runner) dockEnv() dock.Env { return dock.Env{Store: r.Store, Actor: r.Actor} }

func dockResult(v any, err error) (starlark.Value, error) {
	if err != nil {
		return nil, structuredError(err)
	}
	return toStarlark(v)
}

func (r Runner) dockModule() *starlarkstruct.Module {
	fn := func(name string, impl func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error)) *starlark.Builtin {
		return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			return impl(b, args, kwargs)
		})
	}
	env := r.dockEnv()
	return &starlarkstruct.Module{Name: "dock", Members: starlark.StringDict{
		"plan_begin": fn("dock.plan_begin", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var sources starlark.Value
			var project, title, urgency string
			var prefill *starlark.Dict
			ask := true
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "source_ids", &sources, "project?", &project, "title?", &title, "prefill?", &prefill, "urgency?", &urgency, "ask?", &ask); err != nil {
				return nil, err
			}
			pf, err := fieldsMap(prefill)
			if err != nil {
				return nil, err
			}
			return dockResult(env.PlanBegin(r.Context, dock.PlanBeginInput{SourceIDs: asStrings(jsonValue(sources)), Project: project, Title: title, Prefill: pf, Urgency: urgency, Ask: ask}))
		}),
		"plan_status": fn("dock.plan_status", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "plan_id", &id); err != nil {
				return nil, err
			}
			return dockResult(env.PlanStatus(r.Context, id))
		}),
		"plan_propose": fn("dock.plan_propose", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			var spec *starlark.Dict
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "plan_id", &id, "spec", &spec); err != nil {
				return nil, err
			}
			m, err := fieldsMap(spec)
			if err != nil {
				return nil, err
			}
			return dockResult(env.PlanPropose(r.Context, id, m))
		}),
		"form": fn("dock.form", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var title, subtitle, project string
			var fields, actions starlark.Value
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "title", &title, "fields", &fields, "actions?", &actions, "subtitle?", &subtitle, "project?", &project); err != nil {
				return nil, err
			}
			fl, _ := jsonValue(fields).([]any)
			al, _ := jsonValue(actions).([]any)
			spec := dock.Form(title, fl, al, subtitle, project)
			if err := dock.ValidateUISpec(spec); err != nil {
				return nil, structuredError(err)
			}
			return toStarlark(spec)
		}),
		"ask": fn("dock.ask", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var title, urgency, project string
			var spec, defaults *starlark.Dict
			var subjects, expires starlark.Value
			var auto bool
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "title", &title, "spec", &spec, "subject_ids?", &subjects, "urgency?", &urgency, "expires_in_min?", &expires, "defaults?", &defaults, "auto_apply?", &auto, "project?", &project); err != nil {
				return nil, err
			}
			sm, err := fieldsMap(spec)
			if err != nil {
				return nil, err
			}
			dm, err := fieldsMap(defaults)
			if err != nil {
				return nil, err
			}
			mins := 0
			if n := asInt64(jsonValue(expires)); n != nil {
				mins = int(*n)
			}
			if project == "" {
				project = r.Project
			}
			return dockResult(env.Ask(r.Context, title, sm, asStrings(jsonValue(subjects)), urgency, mins, dm, auto, project))
		}),
		"ask_status": fn("dock.ask_status", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "ask_id", &id); err != nil {
				return nil, err
			}
			return dockResult(env.AskStatus(r.Context, id))
		}),
		// ask_wait polls for an answer. A code call is limited to a few seconds,
		// so the wait is capped; call it again in a later turn for longer waits.
		"ask_wait": fn("dock.ask_wait", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			seconds := 60
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "ask_id", &id, "seconds?", &seconds); err != nil {
				return nil, err
			}
			if seconds > 2 {
				seconds = 2
			}
			deadline := time.Now().Add(time.Duration(seconds) * time.Second)
			for {
				st, err := env.AskStatus(r.Context, id)
				if err != nil {
					return nil, structuredError(err)
				}
				if status, _ := st["status"].(string); status != "pending" || time.Now().After(deadline) {
					return toStarlark(st)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}),
		"asks_answered": fn("dock.asks_answered", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var since, project string
			limit := 50
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "since?", &since, "project?", &project, "limit?", &limit); err != nil {
				return nil, err
			}
			return dockResult(env.AsksAnswered(r.Context, since, project, limit))
		}),
		"ask_cancel": fn("dock.ask_cancel", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id string
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "ask_id", &id); err != nil {
				return nil, err
			}
			return dockResult(env.AskCancel(r.Context, id))
		}),
		"task_propose": fn("dock.task_propose", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var title, project, notes, priority string
			var due, scheduled *starlark.Dict
			var tags, sources, depends, alerts starlark.Value
			estimate := 0
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "title", &title, "project", &project, "notes?", &notes, "due?", &due, "scheduled?", &scheduled, "estimate_min?", &estimate, "priority?", &priority, "tags?", &tags, "source_ids?", &sources, "alerts?", &alerts, "depends_on?", &depends); err != nil {
				return nil, err
			}
			dm, err := fieldsMap(due)
			if err != nil {
				return nil, err
			}
			sm, err := fieldsMap(scheduled)
			if err != nil {
				return nil, err
			}
			in := dock.TaskProposeInput{Title: title, Project: project, Notes: notes, EstimateMin: estimate, Priority: priority, Tags: asStrings(jsonValue(tags)), SourceIDs: asStrings(jsonValue(sources)), Alerts: jsonValue(alerts), DependsOn: asStrings(jsonValue(depends))}
			if due != nil {
				in.Due = dm
			}
			if scheduled != nil {
				in.Scheduled = sm
			}
			return dockResult(env.TaskPropose(r.Context, in))
		}),
		"today": fn("dock.today", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var project string
			days := 1
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project, "days?", &days); err != nil {
				return nil, err
			}
			if project == "" {
				project = r.Project
			}
			return dockResult(env.Today(r.Context, project, days))
		}),
		"tasks": fn("dock.tasks", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var project, from, to string
			var status starlark.Value
			limit := 100
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project, "status?", &status, "from_date?", &from, "to_date?", &to, "limit?", &limit); err != nil {
				return nil, err
			}
			if project == "" {
				project = r.Project
			}
			return dockResult(env.Tasks(r.Context, project, asStrings(jsonValue(status)), from, to, limit))
		}),
		"events": fn("dock.events", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var from, to, project string
			limit := 100
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "from_date", &from, "to_date", &to, "project?", &project, "limit?", &limit); err != nil {
				return nil, err
			}
			if project == "" {
				project = r.Project
			}
			return dockResult(env.Events(r.Context, from, to, project, limit))
		}),
		"tasks_summary": fn("dock.tasks_summary", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var project, since string
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project, "since?", &since); err != nil {
				return nil, err
			}
			if project == "" {
				project = r.Project
			}
			return dockResult(env.TasksSummary(r.Context, project, since))
		}),
		"note": fn("dock.note", func(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var id, text string
			if err := starlark.UnpackArgs(b.Name(), args, kwargs, "record_id", &id, "text", &text); err != nil {
				return nil, err
			}
			return dockResult(env.Note(r.Context, id, text))
		}),
	}}
}
