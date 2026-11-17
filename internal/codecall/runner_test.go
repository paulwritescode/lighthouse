package codecall

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/paulwritescode/lighthouse/internal/dock"
	"github.com/paulwritescode/lighthouse/internal/store"
)

func newRunner(t *testing.T, install bool) (Runner, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if install {
		if _, err := dock.Install(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	return Runner{Store: s, Actor: "agent:test"}, s
}

// run executes a script and returns every emitted JSON document.
func run(t *testing.T, r Runner, src string) ([]map[string]any, error) {
	t.Helper()
	var out bytes.Buffer
	err := r.Run(context.Background(), src, "", "job-search", &out)
	var docs []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			docs = append(docs, m)
		}
	}
	return docs, err
}

func mustRun(t *testing.T, r Runner, src string) []map[string]any {
	t.Helper()
	docs, err := run(t, r, src)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, src)
	}
	return docs
}

func TestLegacyCallsKeepTheirShape(t *testing.T) {
	r, _ := newRunner(t, false)
	docs := mustRun(t, r, `
saved = save(title="Result", content="Body text", fields={"score": 3})
emit(saved)
upd = update(saved["artifact"]["id"], fields={"score": 4, "title": "Result v2"})
emit(upd)
emit({"hits": search(query="Body")})
emit(describe())
mem = upsert_memory(key="k", body="a fact")
emit(mem)
emit({"audit": audit(limit=5)})
`)
	for _, key := range []string{"artifact", "revision", "snapshot_id", "context_patch"} {
		if _, ok := docs[0][key]; !ok {
			t.Fatalf("save response lost %q: %v", key, docs[0])
		}
	}
	a := docs[1]["artifact"].(map[string]any)
	if a["title"] != "Result v2" || a["fields"].(map[string]any)["score"] != float64(4) || a["revision"] != float64(2) {
		t.Fatalf("update response: %v", a)
	}
	if docs[3]["collection"] != "artifacts" || docs[3]["custom_fields"] == nil {
		t.Fatalf("describe lost its shape: %v", docs[3])
	}
}

func TestCompareAndSetBatchQueryAndUndoFromStarlark(t *testing.T) {
	r, _ := newRunner(t, false)
	docs := mustRun(t, r, `
made = save_many(items=[
  {"title": "A", "content": "c", "kind": "note", "fields": {"n": 1}},
  {"title": "B", "content": "c", "kind": "note", "fields": {"n": 2}},
])
ids = [i["artifact"]["id"] for i in made["items"]]
emit({"batch": made["batch_id"], "count": len(ids)})
q = query(kind="note", where={"fields.n": {"gte": 2}}, order=[["fields.n", "desc"]])
emit({"matched": [i["title"] for i in q["items"]]})
ok = update(ids[0], fields={"n": 10}, if_revision=1)
emit({"revision": ok["artifact"]["revision"]})
undo = batch_undo(made["batch_id"])
emit(undo)
`)
	if docs[0]["count"] != float64(2) || docs[1]["matched"].([]any)[0] != "B" {
		t.Fatalf("batch or query: %v %v", docs[0], docs[1])
	}
	if docs[2]["revision"] != float64(2) {
		t.Fatalf("update revision: %v", docs[2])
	}
	// The record was edited after the batch, so only B is reverted and A is a conflict.
	if undo := docs[3]; undo["reverted"] != float64(1) || len(undo["conflicts"].([]any)) != 1 {
		t.Fatalf("undo: %v", undo)
	}
	_, err := run(t, r, `
x = save(title="C", content="c")
update(x["artifact"]["id"], fields={"a": 1}, if_revision=7)
`)
	if err == nil || !strings.Contains(err.Error(), "revision_conflict") {
		t.Fatalf("a stale if_revision must fail with revision_conflict, got %v", err)
	}
}

func TestDockModuleRequiresInstall(t *testing.T) {
	r, _ := newRunner(t, false)
	_, err := run(t, r, `emit(dock.today())`)
	if err == nil || !strings.Contains(err.Error(), "dock_not_installed") {
		t.Fatalf("expected dock_not_installed, got %v", err)
	}
}

const planScript = `
src = save(title="Job search register", content="Roles and deadlines", project="job-search", kind="result",
           fields={"ranking": [{"employer": "A", "deadline_reported": "2026-11-30"}]})
sid = src["artifact"]["id"]
plan = dock.plan_begin([sid], title="Job search sprint", ask=False)
spec = {
  "title": "Job search sprint", "summary": "Apply before the deadline.",
  "source_refs": [{"artifact_id": sid}],
  "window": {"start": "2026-11-18", "end": "2026-12-08", "tz": "Africa/Nairobi"},
  "constraints": {"work_days": [1,2,3,4,5], "daily_minutes": 120, "buffer_pct": 15},
  "items": [
    {"key": "verify-roles", "title": "Verify which roles are open", "estimate_min": 45, "priority": "high", "rationale": "Two roles say verify live."},
    {"key": "tailor-cv", "title": "Tailor the CV", "estimate_min": 90, "depends_on": ["verify-roles"],
     "deadline": {"date": "2026-11-30", "hard": True, "source": {"artifact_id": sid, "anchor": "fields.ranking[0].deadline_reported"}},
     "rationale": "Applications close on 30 Nov."},
  ],
}
res = dock.plan_propose(plan["plan_id"], spec)
emit({"plan": plan["plan_id"], "res": res})
emit(dock.plan_status(plan["plan_id"]))
`

func TestPlanProposeCreatesOnlyProposals(t *testing.T) {
	r, s := newRunner(t, true)
	docs := mustRun(t, r, planScript)
	res := docs[0]["res"].(map[string]any)
	if len(res["item_ids"].([]any)) != 2 || res["version"] != float64(1) {
		t.Fatalf("plan_propose: %v", res)
	}
	st := docs[1]
	counts := st["counts"].(map[string]any)
	if st["status"] != "proposed" || counts["proposed"] != float64(2) || counts["todo"] != float64(0) {
		t.Fatalf("plan status: %v", st)
	}
	tasks, _ := s.Query(context.Background(), store.QuerySpec{Kind: "dock_task", Limit: 10})
	for _, it := range tasks.Items {
		f := it["fields"].(map[string]any)
		if f["status"] != "proposed" {
			t.Fatalf("an agent must only create proposals: %v", f["status"])
		}
		if it["actor"] != "agent:test" {
			t.Fatalf("writes must carry the actor, got %v", it["actor"])
		}
	}
	// The dependency points at the other task's id, and the deadline became a due date.
	var cv map[string]any
	for _, it := range tasks.Items {
		if it["title"] == "Tailor the CV" {
			cv = it["fields"].(map[string]any)
		}
	}
	if len(cv["depends_on"].([]any)) != 1 || cv["due"].(map[string]any)["date"] != "2026-11-30" {
		t.Fatalf("task fields: %v", cv)
	}
}

func TestPlanRejectsInventedDeadlinesCyclesAndBadSources(t *testing.T) {
	r, _ := newRunner(t, true)
	base := `
src = save(title="Src", content="c", project="job-search")
sid = src["artifact"]["id"]
plan = dock.plan_begin([sid], ask=False)
window = {"start": "2026-11-18", "end": "2026-12-08", "tz": "Africa/Nairobi"}
`
	for name, tc := range map[string]struct{ items, want string }{
		"hard deadline without a source":   {`[{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r", "deadline": {"date": "2026-11-30", "hard": True}}]`, "source_missing"},
		"hard deadline with a fake source": {`[{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r", "deadline": {"date": "2026-11-30", "hard": True, "source": {"artifact_id": "nope"}}}]`, "source_missing"},
		"dependency cycle":                 {`[{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r", "depends_on": ["b"]}, {"key": "b", "title": "B", "estimate_min": 30, "rationale": "r", "depends_on": ["a"]}]`, "cycle"},
		"unknown dependency":               {`[{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r", "depends_on": ["zzz"]}]`, "depends_on"},
		"estimate out of range":            {`[{"key": "a", "title": "A", "estimate_min": 2, "rationale": "r"}]`, "estimate_min"},
		"missing rationale":                {`[{"key": "a", "title": "A", "estimate_min": 30}]`, "rationale"},
		"soft deadline outside the window": {`[{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r", "deadline": {"date": "2027-05-01", "hard": False}}]`, "window"},
		"duplicate keys":                   {`[{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r"}, {"key": "a", "title": "B", "estimate_min": 30, "rationale": "r"}]`, "duplicate"},
	} {
		_, err := run(t, r, base+`dock.plan_propose(plan["plan_id"], {"title": "P", "summary": "s", "window": window, "items": `+tc.items+`})`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected an error mentioning %q, got %v", name, tc.want, err)
		}
	}
	// Dock records cannot be used as plan sources.
	_, err := run(t, r, `
t = dock.task_propose("x", "job-search")
dock.plan_begin([t["task_id"]], ask=False)`)
	if err == nil || !strings.Contains(err.Error(), "Dock record") {
		t.Fatalf("dock records must be refused as sources, got %v", err)
	}
	// Unknown fields in a spec are rejected rather than silently kept.
	_, err = run(t, r, base+`dock.plan_propose(plan["plan_id"], {"title": "P", "summary": "s", "window": window, "status": "active", "items": [{"key": "a", "title": "A", "estimate_min": 30, "rationale": "r"}]})`)
	if err == nil {
		t.Fatal("a spec may not carry extra top-level fields")
	}
}

func TestReProposalSupersedesAndRepeatIsIdempotent(t *testing.T) {
	r, s := newRunner(t, true)
	script := func(title string) string {
		return `
plans = query(kind="dock_plan")["items"]
src = save(title="Src", content="c", project="job-search")
pid = plans[0]["id"] if plans else dock.plan_begin([src["artifact"]["id"]], ask=False)["plan_id"]
spec = {"title": "P", "summary": "s", "window": {"start": "2026-11-18", "end": "2026-12-08", "tz": "UTC"},
        "items": [{"key": "a", "title": "` + title + `", "estimate_min": 30, "rationale": "r"}]}
emit(dock.plan_propose(pid, spec))
`
	}
	first := mustRun(t, r, script("First"))[0]
	repeat := mustRun(t, r, script("First"))[0]
	if repeat["version"] != first["version"] || !strings.Contains(strings.Join(toStrings(repeat["warnings"]), " "), "unchanged") {
		t.Fatalf("an identical proposal must be a no-op: %v", repeat)
	}
	second := mustRun(t, r, script("Second"))[0]
	if second["version"] != float64(2) {
		t.Fatalf("a changed plan becomes version 2: %v", second)
	}
	old, _ := s.Query(context.Background(), store.QuerySpec{Kind: "dock_task", Where: map[string]any{"fields.status": "cancelled"}, Count: true})
	if old.Count == nil || *old.Count != 1 {
		t.Fatalf("version 1 proposals must be superseded, got %v", old.Count)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestAskFlowAndAgentLimits(t *testing.T) {
	r, s := newRunner(t, true)
	docs := mustRun(t, r, `
spec = dock.form("Weekly focus", fields=[
  {"id": "focus", "type": "chips", "label": "Focus", "options": [{"value": "apps", "label": "Applications"}]},
  {"id": "hours", "type": "slider", "label": "Hours", "default": 10},
], project="job-search")
ask = dock.ask("Weekly focus", spec, urgency="blocking", expires_in_min=60)
emit(ask)
emit(dock.ask_status(ask["ask_id"]))
emit({"waited": dock.ask_wait(ask["ask_id"], seconds=1)["status"]})
`)
	askID := docs[0]["ask_id"].(string)
	if docs[1]["status"] != "pending" || docs[2]["waited"] != "pending" {
		t.Fatalf("a fresh ask is pending: %v %v", docs[1], docs[2])
	}
	// Dock answers the question.
	if _, err := s.UpdateArtifactWith(context.Background(), askID, map[string]any{"status": "answered", "answers": map[string]any{"focus": "apps", "hours": 12}, "answered_at": "2026-11-17T10:00:00Z"}, store.UpdateOptions{WriteOptions: store.WriteOptions{Actor: "dock"}}); err != nil {
		t.Fatal(err)
	}
	got := mustRun(t, r, `
emit(dock.ask_status("`+askID+`"))
emit({"answered": dock.asks_answered(since="2026-11-17T00:00:00Z")})
emit({"none": dock.asks_answered(since="2026-12-01T00:00:00Z")})
`)
	if got[0]["status"] != "answered" || got[0]["answers"].(map[string]any)["focus"] != "apps" {
		t.Fatalf("ask_status after an answer: %v", got[0])
	}
	if len(got[1]["answered"].([]any)) != 1 || len(got[2]["none"].([]any)) != 0 {
		t.Fatalf("asks_answered filtering: %v %v", got[1], got[2])
	}
	for name, src := range map[string]string{
		"unknown component": `dock.ask("x", {"version": 1, "title": "t", "sections": [{"id": "s", "nodes": [{"type": "script", "id": "a", "label": "a"}]}], "actions": []})`,
		"remote route":      `dock.ask("x", {"version": 1, "title": "t", "sections": [{"id": "s", "nodes": [{"type": "text", "id": "a", "label": "a"}]}], "actions": [{"id": "o", "label": "Go", "kind": "open", "open": {"route": "https://evil.example"}}]})`,
		"free query":        `dock.ask("x", {"version": 1, "title": "t", "sections": [{"id": "s", "nodes": [{"type": "select", "id": "a", "label": "a", "options": {"from": {"query": {"kind": "result"}}}}]}], "actions": []})`,
		"reserved id":       `dock.ask("x", {"version": 1, "title": "t", "sections": [{"id": "s", "nodes": [{"type": "text", "id": "__action", "label": "a"}]}], "actions": []})`,
	} {
		if _, err := run(t, r, src); err == nil || !strings.Contains(err.Error(), "validation_failed") {
			t.Fatalf("%s must be refused, got %v", name, err)
		}
	}
	// No more than five open asks per agent and project.
	var err error
	for i := 0; i < 6 && err == nil; i++ {
		_, err = run(t, r, `dock.ask("q`+string(rune('a'+i))+`", dock.form("q", fields=[{"id": "a", "type": "text", "label": "a"}]), project="job-search")`)
	}
	if err == nil || !strings.Contains(err.Error(), "too_many_open_asks") {
		t.Fatalf("expected too_many_open_asks, got %v", err)
	}
}

func TestAgentsCannotActivateOrDeleteDockWork(t *testing.T) {
	r, s := newRunner(t, true)
	docs := mustRun(t, r, `
t = dock.task_propose("Send follow-up", "job-search", due={"date": "2026-12-05", "time": "10:00"}, estimate_min=15)
emit(t)
emit(dock.note(t["task_id"], "Waiting on a reply."))
`)
	id := docs[0]["task_id"].(string)
	a, _ := s.GetArtifact(context.Background(), id)
	if a.Fields["status"] != "proposed" || len(a.Fields["activity"].([]any)) != 1 {
		t.Fatalf("task_propose and note: %v", a.Fields)
	}
	if _, err := run(t, r, `dock.task_propose("", "p")`); err == nil {
		t.Fatal("empty titles are refused")
	}
	if _, err := run(t, r, `dock.task_propose("T", "p", due={"date": "next week"})`); err == nil {
		t.Fatal("malformed dates are refused")
	}
	if _, err := run(t, r, `dock.note("`+docs[0]["task_id"].(string)+`", "")`); err == nil {
		t.Fatal("empty notes are refused")
	}
	// The registered schema blocks malformed Dock records even through plain save().
	_, err := run(t, r, `save(title="x", content="c", kind="dock_task", fields={"status": "everything-is-fine"})`)
	if err == nil || !strings.Contains(err.Error(), "validation_failed") {
		t.Fatalf("the dock_task schema must be enforced, got %v", err)
	}
}

func TestContextIncludeDockAndMemoryExclusion(t *testing.T) {
	r, _ := newRunner(t, true)
	mustRun(t, r, `
save(title="Kept result", content="Normal memory", project="job-search")
dock.task_propose("Proposed task zebra", "job-search", due={"date": "2099-01-01"})
`)
	docs := mustRun(t, r, `
plain = load_memory(project="job-search")
emit({"has_dock": "dock" in plain, "artifacts": [a["title"] for a in plain["artifacts"]]})
rich = load_memory(project="job-search", include=["dock"])
emit({"dock": rich["dock"]})
emit({"hits": [h["title"] for h in search(query="zebra", project="job-search")]})
`)
	if docs[0]["has_dock"] == true {
		t.Fatal("the context pack must not change unless include is passed")
	}
	for _, title := range docs[0]["artifacts"].([]any) {
		if strings.Contains(title.(string), "zebra") {
			t.Fatal("Dock records must stay out of the default context pack")
		}
	}
	d := docs[1]["dock"].(map[string]any)
	if d["installed"] != true || d["pending_asks"] == nil {
		t.Fatalf("dock context section: %v", d)
	}
	if len(docs[2]["hits"].([]any)) != 0 {
		t.Fatal("agent search must not return Dock records")
	}
	_, err := run(t, r, `load_memory(include=["nope"])`)
	if err == nil {
		t.Fatal("unknown include names are refused")
	}
}

func TestSoftDeleteRestoreLinksChangesFromStarlark(t *testing.T) {
	r, _ := newRunner(t, false)
	docs := mustRun(t, r, `
a = save(title="Alpha", content="c")["artifact"]
b = save(title="Beta", content="c")["artifact"]
link(a["id"], "source", b["id"], meta={"quote": "q"})
emit({"links": len(links(b["id"], direction="in"))})
delete(a["id"], soft=True)
emit({"hidden": len(search(query="Alpha"))})
restore(a["id"])
emit({"shown": len(search(query="Alpha"))})
c = changes(since=0)
emit({"ops": [i["op"] for i in c["items"]], "head": c["head"]})
register_kind("note_v", schema={"type": "object", "required": ["n"]}, indexes=["fields.n"])
emit(describe(kind="note_v"))
`)
	if docs[0]["links"] != float64(1) || docs[1]["hidden"] != float64(0) || docs[2]["shown"] != float64(1) {
		t.Fatalf("links and soft delete: %v %v %v", docs[0], docs[1], docs[2])
	}
	ops := docs[3]["ops"].([]any)
	if len(ops) != 4 || ops[2] != "delete" || ops[3] != "restore" {
		t.Fatalf("change feed ops: %v", ops)
	}
	if docs[4]["registered"] != true {
		t.Fatalf("describe(kind=...): %v", docs[4])
	}
}
