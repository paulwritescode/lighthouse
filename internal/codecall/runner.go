// Package codecall provides a constrained Starlark interface to Lighthouse.
package codecall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/paulkinyatti/lighthouse/internal/store"
	"go.starlark.net/starlark"
)

const maxInput = 64 << 10
const maxOutput = 256 << 10

type Runner struct {
	Store   *store.Store
	Context context.Context
	Project string
	Session string
}

// Run executes a small Starlark program with database capabilities only. It
// has no filesystem, shell, environment, or network builtins.
func (r Runner) Run(ctx context.Context, src, session, project string, out io.Writer) error {
	if len(src) > maxInput {
		return errors.New("code input exceeds 64 KiB")
	}
	if r.Store == nil {
		return errors.New("database is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r.Context, r.Project, r.Session = ctx, project, session
	limited := &limitedWriter{w: out, remaining: maxOutput}
	thread := &starlark.Thread{Name: "lighthouse-code"}
	thread.Print = func(_ *starlark.Thread, msg string) { _, _ = io.WriteString(limited, msg+"\n") }
	thread.SetMaxExecutionSteps(100_000)
	globals := starlark.StringDict{
		"search":        starlark.NewBuiltin("search", r.search),
		"get":           starlark.NewBuiltin("get", r.get),
		"save":          starlark.NewBuiltin("save", r.save),
		"update":        starlark.NewBuiltin("update", r.update),
		"delete":        starlark.NewBuiltin("delete", r.deleteArtifact),
		"load_memory":   starlark.NewBuiltin("load_memory", r.loadMemory),
		"upsert_memory": starlark.NewBuiltin("upsert_memory", r.upsertMemory),
		"delete_memory": starlark.NewBuiltin("delete_memory", r.deleteMemory),
		"describe":      starlark.NewBuiltin("describe", r.describe),
		"audit":         starlark.NewBuiltin("audit", r.audit),
		"emit": starlark.NewBuiltin("emit", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var v starlark.Value
			if err := starlark.UnpackArgs("emit", args, kwargs, "value", &v); err != nil {
				return nil, err
			}
			b, err := json.Marshal(valueJSON(v))
			if err != nil {
				return nil, err
			}
			if _, err = limited.Write(append(b, '\n')); err != nil {
				return nil, err
			}
			return starlark.None, nil
		}),
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel("execution timed out")
		case <-done:
		}
	}()
	defer close(done)
	_, err := starlark.ExecFile(thread, "<code-call>", src, globals)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("code call exceeded the 3 second time limit")
		}
		return err
	}
	return limited.err
}

func (r Runner) search(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var q, project string
	limit := 10
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "query", &q, "project?", &project, "limit?", &limit); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	items, err := r.Store.Search(r.Context, q, project, limit)
	if err != nil {
		return nil, err
	}
	return toStarlark(items)
}
func (r Runner) get(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id); err != nil {
		return nil, err
	}
	a, err := r.Store.GetArtifact(r.Context, id)
	if err != nil {
		return nil, err
	}
	return toStarlark(a)
}
func (r Runner) save(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var title, content, project, agent, kind string
	var fields *starlark.Dict
	agent = "agent"
	kind = "result"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "title", &title, "content", &content, "project?", &project, "agent?", &agent, "kind?", &kind, "fields?", &fields); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	custom, err := fieldsMap(fields)
	if err != nil {
		return nil, err
	}
	a, err := r.Store.SaveArtifactFields(r.Context, title, content, project, agent, kind, custom)
	if err != nil {
		return nil, err
	}
	return r.writeResponse(map[string]any{"artifact": a}, project)
}

func fieldsMap(d *starlark.Dict) (map[string]any, error) {
	out := map[string]any{}
	if d == nil {
		return out, nil
	}
	for _, item := range d.Items() {
		k, ok := item[0].(starlark.String)
		if !ok {
			return nil, errors.New("field names must be strings")
		}
		out[string(k)] = valueJSON(item[1])
	}
	return out, nil
}

func (r Runner) update(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	var fields *starlark.Dict
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id, "fields", &fields); err != nil {
		return nil, err
	}
	patch, err := fieldsMap(fields)
	if err != nil {
		return nil, err
	}
	a, err := r.Store.UpdateArtifact(r.Context, id, patch, "agent")
	if err != nil {
		return nil, err
	}
	return r.writeResponse(map[string]any{"artifact": a}, a.Project)
}

func (r Runner) deleteArtifact(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id); err != nil {
		return nil, err
	}
	a, err := r.Store.GetArtifact(r.Context, id)
	if err != nil {
		return nil, err
	}
	if err = r.Store.DeleteArtifact(r.Context, id, "agent"); err != nil {
		return nil, err
	}
	return r.writeResponse(map[string]any{"deleted_id": id}, a.Project)
}

func (r Runner) deleteMemory(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var id, project string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "id", &id, "project?", &project); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	if err := r.Store.DeleteMemory(r.Context, id, "agent"); err != nil {
		return nil, err
	}
	return r.writeResponse(map[string]any{"deleted_memory_id": id}, project)
}

func (r Runner) describe(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var project string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	v, err := r.Store.Describe(r.Context, project)
	if err != nil {
		return nil, err
	}
	return toStarlark(v)
}

func (r Runner) audit(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	limit := 25
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "limit?", &limit); err != nil {
		return nil, err
	}
	v, err := r.Store.RecentChanges(r.Context, limit)
	if err != nil {
		return nil, err
	}
	return toStarlark(v)
}
func (r Runner) loadMemory(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var project, session string
	limit := 10
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project?", &project, "session?", &session, "limit?", &limit); err != nil {
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
	return toStarlark(p)
}
func (r Runner) upsertMemory(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key, body, project, kind, source string
	kind = "fact"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "key", &key, "body", &body, "project?", &project, "kind?", &kind, "source_id?", &source); err != nil {
		return nil, err
	}
	if project == "" {
		project = r.Project
	}
	m, err := r.Store.SaveMemory(r.Context, key, body, project, kind, source)
	if err != nil {
		return nil, err
	}
	// Return the committed write and a refreshed snapshot so the calling chat
	// can replace its stale memory in-band on this same code call.
	return r.writeResponse(map[string]any{"memory": m}, project)
}

func (r Runner) writeResponse(change map[string]any, project string) (starlark.Value, error) {
	pack, err := r.Store.LoadContext(r.Context, r.Session, project, 10)
	if err != nil {
		// The durable write has already committed. Return it with an explicit
		// refresh error so the caller won't mistake it for a failed write.
		change["refresh_error"] = err.Error()
		return toStarlark(change)
	}
	change["revision"] = pack.Revision
	change["snapshot_id"] = pack.SnapshotID
	change["context_patch"] = pack.Context
	return toStarlark(change)
}

func toStarlark(v any) (starlark.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var raw any
	if err = json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return fromJSON(raw), nil
}
func fromJSON(v any) starlark.Value {
	switch x := v.(type) {
	case nil:
		return starlark.None
	case bool:
		return starlark.Bool(x)
	case string:
		return starlark.String(x)
	case float64:
		return starlark.Float(x)
	case []any:
		l := make(starlark.Tuple, len(x))
		for i, a := range x {
			l[i] = fromJSON(a)
		}
		return l
	case map[string]any:
		d := starlark.NewDict(len(x))
		for k, a := range x {
			_ = d.SetKey(starlark.String(k), fromJSON(a))
		}
		return d
	}
	return starlark.String(fmt.Sprint(v))
}
func valueJSON(v starlark.Value) any {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(x)
	case starlark.String:
		return string(x)
	case starlark.Int:
		n, _ := x.Int64()
		return n
	case starlark.Float:
		return float64(x)
	case starlark.Tuple:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = valueJSON(v)
		}
		return a
	case *starlark.List:
		a := make([]any, x.Len())
		for i := range a {
			a[i] = valueJSON(x.Index(i))
		}
		return a
	case *starlark.Dict:
		m := map[string]any{}
		for _, item := range x.Items() {
			k, ok := item[0].(starlark.String)
			if ok {
				m[string(k)] = valueJSON(item[1])
			}
		}
		return m
	default:
		return x.String()
	}
}

type limitedWriter struct {
	w         io.Writer
	remaining int
	err       error
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if len(p) > l.remaining {
		l.err = errors.New("code call output exceeds 256 KiB")
		return 0, l.err
	}
	n, err := l.w.Write(p)
	l.remaining -= n
	if err != nil {
		l.err = err
	}
	return n, err
}
