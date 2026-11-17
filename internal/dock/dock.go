package dock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/paulwritescode/lighthouse/internal/store"
)

// Env is what every module call needs: the store and who is calling.
type Env struct {
	Store *store.Store
	Actor string // for example "agent:kiro"; written to every record
}

func (e Env) opts(batch string) store.WriteOptions {
	actor := e.Actor
	if actor == "" {
		actor = "agent"
	}
	return store.WriteOptions{Actor: actor, BatchID: batch}
}

func (e Env) require(ctx context.Context) error {
	if !Installed(ctx, e.Store) {
		return fail("dock_not_installed", "the Dock kinds are not registered; run `lighthouse kind install dock`")
	}
	return nil
}

var (
	dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timeRE = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	keyRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

func validDate(s string) bool {
	if !dateRE.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// ---- Plan spec ----

// SourceRef points at a Lighthouse record, optionally at a field or quote.
type SourceRef struct {
	ArtifactID string `json:"artifact_id"`
	Anchor     string `json:"anchor,omitempty"`
	Quote      string `json:"quote,omitempty"`
}

// Deadline is a date an item must meet. A hard deadline must cite its source.
type Deadline struct {
	Date   string     `json:"date"`
	Time   string     `json:"time,omitempty"`
	Hard   bool       `json:"hard"`
	Source *SourceRef `json:"source,omitempty"`
}

// Item is one task a plan proposes. Dates are hints; Dock does the scheduling.
type Item struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	Notes       string         `json:"notes,omitempty"`
	EstimateMin int            `json:"estimate_min"`
	Priority    string         `json:"priority,omitempty"`
	Phase       string         `json:"phase,omitempty"`
	DependsOn   []string       `json:"depends_on,omitempty"`
	Earliest    string         `json:"earliest,omitempty"`
	Deadline    *Deadline      `json:"deadline,omitempty"`
	Preferred   map[string]any `json:"preferred,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Alerts      any            `json:"alerts,omitempty"`
	Recurrence  string         `json:"recurrence,omitempty"`
	Rationale   string         `json:"rationale"`
	SourceRefs  []SourceRef    `json:"source_refs,omitempty"`
	Confidence  *float64       `json:"confidence,omitempty"`
}

// PlanSpec is the document an agent (or Dock's own AI) produces.
type PlanSpec struct {
	Title      string      `json:"title"`
	Summary    string      `json:"summary"`
	SourceRefs []SourceRef `json:"source_refs,omitempty"`
	Window     struct {
		Start string `json:"start"`
		End   string `json:"end"`
		TZ    string `json:"tz"`
	} `json:"window"`
	Constraints map[string]any   `json:"constraints,omitempty"`
	Phases      []map[string]any `json:"phases,omitempty"`
	Items       []Item           `json:"items"`
}

func (e Env) sourceExists(ctx context.Context, id string) error {
	a, err := e.Store.GetArtifact(ctx, id)
	if err != nil || a.DeletedAt != nil {
		return fail("source_missing", "source record %q does not exist", id)
	}
	if strings.HasPrefix(a.Kind, "dock_") {
		return fail("validation_failed", "source record %q is a Dock record; sources must be memory or results", id)
	}
	return nil
}

func (e Env) validatePlan(ctx context.Context, spec *PlanSpec) ([]string, error) {
	var warnings []string
	if strings.TrimSpace(spec.Title) == "" {
		return nil, fail("validation_failed", "title is required")
	}
	if !validDate(spec.Window.Start) || !validDate(spec.Window.End) {
		return nil, fail("validation_failed", "window.start and window.end must be YYYY-MM-DD dates")
	}
	start, _ := time.Parse("2006-01-02", spec.Window.Start)
	end, _ := time.Parse("2006-01-02", spec.Window.End)
	if end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return nil, fail("validation_failed", "window must end after it starts and span at most a year")
	}
	if spec.Window.TZ == "" {
		return nil, fail("validation_failed", "window.tz is required, for example Africa/Nairobi")
	}
	if _, err := time.LoadLocation(spec.Window.TZ); err != nil {
		return nil, fail("validation_failed", "window.tz %q is not a time zone", spec.Window.TZ)
	}
	if len(spec.Items) == 0 || len(spec.Items) > maxPlanItems {
		return nil, fail("validation_failed", "a plan needs 1 to %d items", maxPlanItems)
	}
	checked := map[string]bool{}
	checkSource := func(r SourceRef) error {
		if r.ArtifactID == "" {
			return fail("source_missing", "a source_ref needs an artifact_id")
		}
		if checked[r.ArtifactID] {
			return nil
		}
		if err := e.sourceExists(ctx, r.ArtifactID); err != nil {
			return err
		}
		checked[r.ArtifactID] = true
		return nil
	}
	for _, r := range spec.SourceRefs {
		if err := checkSource(r); err != nil {
			return nil, err
		}
	}
	keys := map[string]int{}
	totalMin := 0
	for i := range spec.Items {
		it := &spec.Items[i]
		where := fmt.Sprintf("item %d (%s)", i, it.Key)
		if !keyRE.MatchString(it.Key) {
			return nil, fail("validation_failed", "%s: key must be lowercase kebab-case, up to 64 characters", where)
		}
		if _, dup := keys[it.Key]; dup {
			return nil, fail("validation_failed", "%s: duplicate key", where)
		}
		keys[it.Key] = i
		if t := strings.TrimSpace(it.Title); t == "" || len(t) > 120 {
			return nil, fail("validation_failed", "%s: title must be 1 to 120 characters", where)
		}
		if len(it.Notes) > maxNotesBytes {
			return nil, fail("validation_failed", "%s: notes are too long", where)
		}
		if it.EstimateMin < 5 || it.EstimateMin > 480 {
			return nil, fail("validation_failed", "%s: estimate_min must be 5 to 480", where)
		}
		switch it.Priority {
		case "":
			it.Priority = "normal"
		case "low", "normal", "high", "urgent":
		default:
			return nil, fail("validation_failed", "%s: priority must be low, normal, high or urgent", where)
		}
		if strings.TrimSpace(it.Rationale) == "" || len(it.Rationale) > 600 {
			return nil, fail("validation_failed", "%s: a rationale of up to 600 characters is required", where)
		}
		for _, d := range []string{it.Earliest} {
			if d != "" && (!validDate(d)) {
				return nil, fail("validation_failed", "%s: earliest must be YYYY-MM-DD", where)
			}
		}
		if it.Earliest != "" && (it.Earliest < spec.Window.Start || it.Earliest > spec.Window.End) {
			return nil, fail("validation_failed", "%s: earliest lies outside the window", where)
		}
		if dl := it.Deadline; dl != nil {
			if !validDate(dl.Date) || (dl.Time != "" && !timeRE.MatchString(dl.Time)) {
				return nil, fail("validation_failed", "%s: deadline needs a YYYY-MM-DD date and an optional HH:mm time", where)
			}
			if dl.Hard {
				if dl.Source == nil {
					return nil, fail("source_missing", "%s: a hard deadline must cite the record it came from (deadline.source)", where)
				}
				if err := checkSource(*dl.Source); err != nil {
					return nil, err
				}
			} else if dl.Date < spec.Window.Start || dl.Date > spec.Window.End {
				return nil, fail("validation_failed", "%s: a soft deadline must lie inside the window", where)
			}
		}
		if d, ok := it.Preferred["date"].(string); ok && !validDate(d) {
			return nil, fail("validation_failed", "%s: preferred.date must be YYYY-MM-DD", where)
		}
		for _, r := range it.SourceRefs {
			if err := checkSource(r); err != nil {
				return nil, err
			}
		}
		totalMin += it.EstimateMin
	}
	for _, it := range spec.Items {
		for _, dep := range it.DependsOn {
			if _, ok := keys[dep]; !ok || dep == it.Key {
				return nil, fail("validation_failed", "item %s: depends_on %q is not another item in this plan", it.Key, dep)
			}
		}
	}
	if cyclic(spec.Items, keys) {
		return nil, fail("validation_failed", "depends_on contains a cycle")
	}
	if daily, ok := spec.Constraints["daily_minutes"].(float64); ok && daily > 0 {
		days := workingDays(start, end, spec.Constraints["work_days"])
		buffer, _ := spec.Constraints["buffer_pct"].(float64)
		capacity := float64(days) * daily * (1 - buffer/100)
		if float64(totalMin) > capacity {
			warnings = append(warnings, fmt.Sprintf("over_capacity: %d minutes planned, about %.0f available in the window", totalMin, capacity))
		}
	}
	return warnings, nil
}

func cyclic(items []Item, index map[string]int) bool {
	state := make([]int, len(items))
	var visit func(i int) bool
	visit = func(i int) bool {
		if state[i] == 1 {
			return true
		}
		if state[i] == 2 {
			return false
		}
		state[i] = 1
		for _, dep := range items[i].DependsOn {
			if visit(index[dep]) {
				return true
			}
		}
		state[i] = 2
		return false
	}
	for i := range items {
		if visit(i) {
			return true
		}
	}
	return false
}

func workingDays(start, end time.Time, raw any) int {
	allowed := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	if list, ok := raw.([]any); ok && len(list) > 0 {
		allowed = map[int]bool{}
		for _, v := range list {
			if f, ok := v.(float64); ok {
				allowed[int(f)] = true
			}
		}
	}
	n := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if allowed[int(d.Weekday())] {
			n++
		}
	}
	return n
}

// ---- Plans ----

// PlanBeginInput opens a plan for one or more source records.
type PlanBeginInput struct {
	SourceIDs []string
	Project   string
	Title     string
	Prefill   map[string]any
	Urgency   string
	Ask       bool
}

// PlanBegin creates a plan in needs_input, and the questions to answer when
// Ask is true.
func (e Env) PlanBegin(ctx context.Context, in PlanBeginInput) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	if len(in.SourceIDs) == 0 {
		return nil, fail("validation_failed", "plan_begin needs at least one source id")
	}
	hashes := map[string]any{}
	titles := []string{}
	project := in.Project
	for _, id := range in.SourceIDs {
		if err := e.sourceExists(ctx, id); err != nil {
			return nil, err
		}
		a, _ := e.Store.GetArtifact(ctx, id)
		hashes[id] = a.ContentHash
		titles = append(titles, a.Title)
		if project == "" {
			project = a.Project
		}
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Plan: " + titles[0]
	}
	urgency := in.Urgency
	if urgency == "" {
		urgency = "blocking"
	}
	if urgency != "blocking" && urgency != "normal" && urgency != "low" {
		return nil, fail("validation_failed", "urgency must be blocking, normal or low")
	}
	planID, err := store.NewID()
	if err != nil {
		return nil, err
	}
	askID := ""
	ops := []store.BatchOp{}
	planFields := map[string]any{"status": "needs_input", "source_ids": strSlice(in.SourceIDs), "source_hashes": hashes, "version": 1, "summary": "", "fit_state": "agent_hint", "item_ids": []any{}}
	if in.Ask {
		if err := e.checkOpenAsks(ctx, project); err != nil {
			return nil, err
		}
		if askID, err = store.NewID(); err != nil {
			return nil, err
		}
		planFields["ask_id"] = askID
		spec := WizardSpec(title, titles, in.Prefill, project)
		if err := ValidateUISpec(spec); err != nil {
			return nil, err
		}
		ops = append(ops, store.BatchOp{Op: "save", Save: &store.SaveInput{ID: askID, Title: title, Content: "Questions needed to plan: " + strings.Join(titles, ", "), Project: project, Agent: e.agentName(), Kind: KindAsk, Fields: map[string]any{
			"status": "pending", "requested_by": map[string]any{"kind": "agent", "id": e.agentName()}, "urgency": urgency,
			"subject_ids": strSlice(in.SourceIDs), "spec": spec, "defaults": defaultsFrom(in.Prefill), "auto_apply": false, "answers": nil,
			"on_answer": map[string]any{"kind": "plan", "plan_id": planID},
		}}})
	}
	ops = append([]store.BatchOp{{Op: "save", Save: &store.SaveInput{ID: planID, Title: title, Content: "Plan from " + strings.Join(titles, ", "), Project: project, Agent: e.agentName(), Kind: KindPlan, Fields: planFields}}}, ops...)
	res, err := e.Store.Batch(ctx, ops, store.BatchOptions{WriteOptions: e.opts(""), Atomic: true})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"plan_id": planID, "ask_id": nil, "batch_id": res.BatchID}
	if askID != "" {
		out["ask_id"] = askID
	}
	return out, nil
}

func (e Env) agentName() string {
	if strings.HasPrefix(e.Actor, "agent:") {
		return strings.TrimPrefix(e.Actor, "agent:")
	}
	return "agent"
}

func strSlice(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func defaultsFrom(prefill map[string]any) map[string]any {
	out := map[string]any{"work_days": []any{1.0, 2.0, 3.0, 4.0, 5.0}, "daily_minutes": 120.0, "intensity": "steady", "reminders": "standard", "mirror": "off"}
	for k, v := range prefill {
		out[k] = v
	}
	return out
}

func (e Env) checkOpenAsks(ctx context.Context, project string) error {
	res, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindAsk, Project: project, Count: true, Where: map[string]any{"and": []any{
		map[string]any{"fields.status": "pending"},
		map[string]any{"fields.requested_by.id": e.agentName()},
	}}})
	if err != nil {
		return err
	}
	if res.Count != nil && *res.Count >= maxOpenAsks {
		return fail("too_many_open_asks", "%d questions from this agent are already waiting in project %q; wait for answers or cancel some", *res.Count, project)
	}
	return nil
}

// PlanStatus reports where a plan stands, including the wizard's answers.
func (e Env) PlanStatus(ctx context.Context, planID string) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	plan, err := e.Store.GetArtifact(ctx, planID)
	if err != nil || plan.Kind != KindPlan {
		return nil, fail("not_found", "no plan with id %q", planID)
	}
	out := map[string]any{"plan_id": plan.ID, "status": plan.Fields["status"], "version": plan.Fields["version"], "summary": plan.Fields["summary"], "metrics": plan.Fields["metrics"], "health": plan.Fields["health"], "fit_state": plan.Fields["fit_state"], "ask_status": nil, "answers": nil}
	if askID, _ := plan.Fields["ask_id"].(string); askID != "" {
		if ask, err := e.Store.GetArtifact(ctx, askID); err == nil {
			out["ask_status"], out["answers"] = ask.Fields["status"], ask.Fields["answers"]
		}
	}
	counts := map[string]any{}
	for _, st := range taskStatuses {
		r, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindTask, Count: true, Where: map[string]any{"fields.plan_id": planID, "fields.status": st}})
		if err != nil {
			return nil, err
		}
		counts[st] = *r.Count
	}
	out["counts"] = counts
	return out, nil
}

// PlanPropose validates a PlanSpec and writes its items as proposed tasks in
// one atomic batch. It never activates anything.
func (e Env) PlanPropose(ctx context.Context, planID string, raw map[string]any) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	plan, err := e.Store.GetArtifact(ctx, planID)
	if err != nil || plan.Kind != KindPlan || plan.DeletedAt != nil {
		return nil, fail("not_found", "no plan with id %q", planID)
	}
	status, _ := plan.Fields["status"].(string)
	if status == "active" || status == "completed" || status == "archived" {
		return nil, fail("forbidden_for_agents", "plan %s is already %s; ask the user to re-plan from Dock", planID, status)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fail("validation_failed", "plan spec must be JSON-compatible")
	}
	var spec PlanSpec
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fail("validation_failed", "plan spec is malformed: %v", err)
	}
	warnings, err := e.validatePlan(ctx, &spec)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	specHash := hex.EncodeToString(sum[:])
	version := int(num(plan.Fields["version"], 1))
	if prev, _ := plan.Fields["spec_hash"].(string); prev == specHash {
		return map[string]any{"plan_id": planID, "version": version, "item_ids": plan.Fields["item_ids"], "warnings": append(warnings, "unchanged: this exact plan was already proposed")}, nil
	}
	if prev, _ := plan.Fields["spec_hash"].(string); prev != "" {
		version++
	}
	ids := make(map[string]string, len(spec.Items))
	itemIDs := make([]any, 0, len(spec.Items))
	for _, it := range spec.Items {
		id, err := store.NewID()
		if err != nil {
			return nil, err
		}
		ids[it.Key] = id
		itemIDs = append(itemIDs, id)
	}
	ops := []store.BatchOp{}
	// A new version supersedes proposals nobody accepted yet.
	old, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindTask, Limit: 500, Where: map[string]any{"fields.plan_id": planID, "fields.status": "proposed"}})
	if err != nil {
		return nil, err
	}
	for _, it := range old.Items {
		ops = append(ops, store.BatchOp{Op: "update", ID: it["id"].(string), Patch: map[string]any{"status": "cancelled", "superseded_by_version": version}})
	}
	total := 0
	for _, it := range spec.Items {
		total += it.EstimateMin
		ops = append(ops, store.BatchOp{Op: "save", Save: &store.SaveInput{ID: ids[it.Key], Title: strings.TrimSpace(it.Title), Content: firstNonEmpty(it.Notes, it.Rationale), Project: plan.Project, Agent: e.agentName(), Kind: KindTask, Fields: e.taskFields(it, ids, planID, spec)}})
	}
	if spec.Constraints == nil {
		spec.Constraints = map[string]any{}
	}
	planPatch := map[string]any{"status": "proposed", "version": version, "summary": spec.Summary, "window": map[string]any{"start": spec.Window.Start, "end": spec.Window.End, "tz": spec.Window.TZ},
		"constraints": spec.Constraints, "item_ids": itemIDs, "spec_hash": specHash, "fit_state": "agent_hint", "metrics": map[string]any{"items": len(spec.Items), "minutes": total}}
	ops = append(ops, store.BatchOp{Op: "update", ID: planID, Patch: planPatch})
	res, err := e.Store.Batch(ctx, ops, store.BatchOptions{WriteOptions: e.opts(""), Atomic: true})
	if err != nil {
		return nil, err
	}
	return map[string]any{"plan_id": planID, "version": version, "item_ids": itemIDs, "warnings": warnings, "batch_id": res.BatchID}, nil
}

func (e Env) taskFields(it Item, ids map[string]string, planID string, spec PlanSpec) map[string]any {
	f := map[string]any{
		"type": "task", "status": "proposed", "priority": it.Priority, "estimate_min": it.EstimateMin, "tags": strSlice(append([]string{}, it.Tags...)),
		"plan_id": planID, "mirror": "auto", "subtasks": []any{}, "alerts": []any{},
		"reschedule": map[string]any{"count": 0, "last_at": nil, "last_reason": nil, "history": []any{}},
		"source":     map[string]any{"kind": "plan", "plan_id": planID, "item_key": it.Key, "rationale": it.Rationale, "artifact_ids": artifactIDs(spec, it)},
	}
	deps := make([]any, 0, len(it.DependsOn))
	for _, d := range it.DependsOn {
		deps = append(deps, ids[d])
	}
	f["depends_on"] = deps
	if it.Deadline != nil {
		due := map[string]any{"date": it.Deadline.Date, "time": nil, "tz": spec.Window.TZ}
		if it.Deadline.Time != "" {
			due["time"] = it.Deadline.Time
		}
		f["due"] = due
		f["deadline"] = map[string]any{"hard": it.Deadline.Hard}
	}
	if it.Earliest != "" {
		f["earliest"] = it.Earliest
	}
	if len(it.Preferred) > 0 {
		f["preferred"] = it.Preferred
	}
	if it.Phase != "" {
		f["phase_key"] = it.Phase
	}
	if it.Recurrence != "" {
		f["recurrence"] = it.Recurrence
	}
	if it.Alerts != nil {
		f["alert_spec"] = it.Alerts
	}
	if it.Confidence != nil {
		f["confidence"] = *it.Confidence
	}
	return f
}

func artifactIDs(spec PlanSpec, it Item) []any {
	seen := map[string]bool{}
	out := []any{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, r := range spec.SourceRefs {
		add(r.ArtifactID)
	}
	for _, r := range it.SourceRefs {
		add(r.ArtifactID)
	}
	if it.Deadline != nil && it.Deadline.Source != nil {
		add(it.Deadline.Source.ArtifactID)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return "-"
}

func num(v any, fallback float64) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return fallback
}

// ---- Asks ----

// Ask stores a validated generative screen for the user to answer in Dock.
func (e Env) Ask(ctx context.Context, title string, spec map[string]any, subjectIDs []string, urgency string, expiresInMin int, defaults map[string]any, autoApply bool, project string) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, fail("validation_failed", "an ask needs a title")
	}
	if err := ValidateUISpec(spec); err != nil {
		return nil, err
	}
	if urgency == "" {
		urgency = "normal"
	}
	if urgency != "blocking" && urgency != "normal" && urgency != "low" {
		return nil, fail("validation_failed", "urgency must be blocking, normal or low")
	}
	for _, id := range subjectIDs {
		if _, err := e.Store.GetArtifact(ctx, id); err != nil {
			return nil, fail("source_missing", "subject record %q does not exist", id)
		}
	}
	if err := e.checkOpenAsks(ctx, project); err != nil {
		return nil, err
	}
	var expires any
	if expiresInMin > 0 {
		expires = time.Now().UTC().Add(time.Duration(expiresInMin) * time.Minute).Format(time.RFC3339)
	}
	res, err := e.Store.SaveArtifactWith(ctx, store.SaveInput{Title: title, Content: "Question for the user: " + title, Project: project, Agent: e.agentName(), Kind: KindAsk, Fields: map[string]any{
		"status": "pending", "requested_by": map[string]any{"kind": "agent", "id": e.agentName()}, "urgency": urgency, "subject_ids": strSlice(subjectIDs),
		"spec": spec, "defaults": defaults, "auto_apply": autoApply, "expires_at": expires, "answers": nil, "on_answer": map[string]any{"kind": "agent_poll"},
	}}, e.opts(""))
	if err != nil {
		return nil, err
	}
	return map[string]any{"ask_id": res.Artifact.ID}, nil
}

func (e Env) loadAsk(ctx context.Context, id string) (store.Artifact, error) {
	a, err := e.Store.GetArtifact(ctx, id)
	if err != nil || a.Kind != KindAsk || a.DeletedAt != nil {
		return store.Artifact{}, fail("not_found", "no question with id %q", id)
	}
	return a, nil
}

// AskStatus returns a question's status and answers.
func (e Env) AskStatus(ctx context.Context, id string) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	a, err := e.loadAsk(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ask_id": a.ID, "status": a.Fields["status"], "answers": a.Fields["answers"], "answered_at": a.Fields["answered_at"]}, nil
}

// AsksAnswered lists questions answered after a time, newest last.
func (e Env) AsksAnswered(ctx context.Context, since, project string, limit int) ([]map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	where := map[string]any{"fields.status": "answered"}
	if since != "" {
		where["fields.answered_at"] = map[string]any{"gt": since}
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	res, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindAsk, Project: project, Where: where, Limit: limit, Order: []store.OrderKey{{Path: "fields.answered_at", Dir: "asc"}}})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(res.Items))
	for _, it := range res.Items {
		f, _ := it["fields"].(map[string]any)
		out = append(out, map[string]any{"ask_id": it["id"], "title": it["title"], "answers": f["answers"], "answered_at": f["answered_at"], "subject_ids": f["subject_ids"]})
	}
	return out, nil
}

// AskCancel withdraws a pending question.
func (e Env) AskCancel(ctx context.Context, id string) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	a, err := e.loadAsk(ctx, id)
	if err != nil {
		return nil, err
	}
	if st, _ := a.Fields["status"].(string); st != "pending" {
		return nil, fail("validation_failed", "only a pending question can be cancelled; this one is %s", st)
	}
	if _, err := e.Store.UpdateArtifactWith(ctx, id, map[string]any{"status": "cancelled"}, store.UpdateOptions{WriteOptions: e.opts("")}); err != nil {
		return nil, err
	}
	return map[string]any{"ask_id": id, "status": "cancelled"}, nil
}

// ---- Single proposals and notes ----

// TaskProposeInput is a task an agent suggests. It is always created proposed.
type TaskProposeInput struct {
	Title       string
	Project     string
	Notes       string
	Due         map[string]any
	Scheduled   map[string]any
	EstimateMin int
	Priority    string
	Tags        []string
	SourceIDs   []string
	Alerts      any
	DependsOn   []string
}

// TaskPropose creates one proposed task.
func (e Env) TaskPropose(ctx context.Context, in TaskProposeInput) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Title) == "" || len(in.Title) > 120 {
		return nil, fail("validation_failed", "title must be 1 to 120 characters")
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	fields := map[string]any{"type": "task", "status": "proposed", "priority": in.Priority, "tags": strSlice(in.Tags), "subtasks": []any{}, "alerts": []any{}, "mirror": "auto",
		"depends_on": strSlice(in.DependsOn), "reschedule": map[string]any{"count": 0, "last_at": nil, "last_reason": nil, "history": []any{}},
		"source": map[string]any{"kind": "agent", "agent": e.agentName(), "artifact_ids": strSlice(in.SourceIDs)}}
	if in.Due != nil {
		if d, _ := in.Due["date"].(string); !validDate(d) {
			return nil, fail("validation_failed", "due.date must be YYYY-MM-DD")
		}
		if t, _ := in.Due["time"].(string); t != "" && !timeRE.MatchString(t) {
			return nil, fail("validation_failed", "due.time must be HH:mm")
		}
		fields["due"] = in.Due
	}
	if in.Scheduled != nil {
		fields["scheduled"] = in.Scheduled
	}
	if in.EstimateMin > 0 {
		fields["estimate_min"] = in.EstimateMin
	}
	if in.Alerts != nil {
		fields["alert_spec"] = in.Alerts
	}
	for _, id := range in.SourceIDs {
		if err := e.sourceExists(ctx, id); err != nil {
			return nil, err
		}
	}
	res, err := e.Store.SaveArtifactWith(ctx, store.SaveInput{Title: strings.TrimSpace(in.Title), Content: firstNonEmpty(in.Notes, in.Title), Project: in.Project, Agent: e.agentName(), Kind: KindTask, Fields: fields}, e.opts(""))
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": res.Artifact.ID}, nil
}

// Note appends a comment to a Dock record's activity without changing state.
func (e Env) Note(ctx context.Context, recordID, text string) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	if t := strings.TrimSpace(text); t == "" || len(t) > 2000 {
		return nil, fail("validation_failed", "a note must be 1 to 2000 characters")
	}
	a, err := e.Store.GetArtifact(ctx, recordID)
	if err != nil || !strings.HasPrefix(a.Kind, "dock_") {
		return nil, fail("not_found", "notes can only be added to Dock records")
	}
	activity, _ := a.Fields["activity"].([]any)
	activity = append(activity, map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "actor": e.opts("").Actor, "text": strings.TrimSpace(text)})
	if len(activity) > 200 {
		activity = activity[len(activity)-200:]
	}
	rev := a.Revision
	if _, err := e.Store.UpdateArtifactWith(ctx, recordID, map[string]any{"activity": activity}, store.UpdateOptions{WriteOptions: e.opts(""), IfRevision: &rev}); err != nil {
		return nil, err
	}
	return map[string]any{"record_id": recordID, "notes": len(activity)}, nil
}

// ---- Reads ----

func summary(it map[string]any) map[string]any {
	f, _ := it["fields"].(map[string]any)
	return map[string]any{"id": it["id"], "title": it["title"], "project": it["project"], "status": f["status"], "priority": f["priority"], "due": f["due"], "scheduled": f["scheduled"], "when": f["when"], "plan_id": f["plan_id"]}
}

// Tasks lists task summaries filtered by project, status and due date range.
func (e Env) Tasks(ctx context.Context, project string, statuses []string, from, to string, limit int) ([]map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	and := []any{}
	if len(statuses) > 0 {
		and = append(and, map[string]any{"fields.status": map[string]any{"in": strSlice(statuses)}})
	}
	if from != "" {
		and = append(and, map[string]any{"fields.due.date": map[string]any{"gte": from}})
	}
	if to != "" {
		and = append(and, map[string]any{"fields.due.date": map[string]any{"lte": to}})
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	spec := store.QuerySpec{Kind: KindTask, Project: project, Limit: limit, Order: []store.OrderKey{{Path: "fields.due.date", Dir: "asc"}}}
	if len(and) > 0 {
		spec.Where = map[string]any{"and": and}
	}
	res, err := e.Store.Query(ctx, spec)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, summary(it))
	}
	return out, nil
}

// Events lists event summaries that start in a date range (UTC days).
func (e Env) Events(ctx context.Context, from, to, project string, limit int) ([]map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	if !validDate(from) || !validDate(to) {
		return nil, fail("validation_failed", "from_date and to_date must be YYYY-MM-DD")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	res, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindEvent, Project: project, Limit: limit, Order: []store.OrderKey{{Path: "fields.when.start_at", Dir: "asc"}}, Where: map[string]any{"or": []any{
		map[string]any{"and": []any{map[string]any{"fields.when.start_at": map[string]any{"gte": from + "T00:00:00Z"}}, map[string]any{"fields.when.start_at": map[string]any{"lt": to + "T23:59:59.999Z"}}}},
		map[string]any{"and": []any{map[string]any{"fields.when.start_date": map[string]any{"gte": from}}, map[string]any{"fields.when.start_date": map[string]any{"lte": to}}}},
	}}})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, summary(it))
	}
	return out, nil
}

func (e Env) count(ctx context.Context, project string, where map[string]any) int {
	r, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindTask, Project: project, Count: true, Where: where})
	if err != nil || r.Count == nil {
		return 0
	}
	return *r.Count
}

// Today summarises the agenda for the next days, overdue work, open questions
// and active plans. It is the same data the context pack includes.
func (e Env) Today(ctx context.Context, project string, days int) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	if days < 1 || days > 31 {
		days = 1
	}
	today := time.Now().Format("2006-01-02")
	last := time.Now().AddDate(0, 0, days-1).Format("2006-01-02")
	agenda, err := e.Tasks(ctx, project, []string{"todo", "doing", "reschedule"}, today, last, 100)
	if err != nil {
		return nil, err
	}
	overdue := e.count(ctx, project, map[string]any{"and": []any{map[string]any{"fields.status": map[string]any{"in": []any{"todo", "doing", "reschedule"}}}, map[string]any{"fields.due.date": map[string]any{"lt": today}}}})
	asks, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindAsk, Project: project, Limit: 20, Where: map[string]any{"fields.status": "pending"}})
	if err != nil {
		return nil, err
	}
	pending := []map[string]any{}
	for _, a := range asks.Items {
		f, _ := a["fields"].(map[string]any)
		pending = append(pending, map[string]any{"id": a["id"], "title": a["title"], "urgency": f["urgency"]})
	}
	plans, err := e.Store.Query(ctx, store.QuerySpec{Kind: KindPlan, Project: project, Limit: 20, Where: map[string]any{"fields.status": map[string]any{"in": []any{"proposed", "active"}}}})
	if err != nil {
		return nil, err
	}
	active := []map[string]any{}
	for _, p := range plans.Items {
		f, _ := p["fields"].(map[string]any)
		id, _ := p["id"].(string)
		total := e.count(ctx, "", map[string]any{"fields.plan_id": id, "fields.status": map[string]any{"nin": []any{"cancelled"}}})
		done := e.count(ctx, "", map[string]any{"fields.plan_id": id, "fields.status": "done"})
		active = append(active, map[string]any{"id": id, "title": p["title"], "status": f["status"], "health": f["health"], "progress": fmt.Sprintf("%d/%d", done, total)})
	}
	return map[string]any{"date": today, "agenda": agenda, "overdue": overdue, "pending_asks": pending, "active_plans": active}, nil
}

// TasksSummary counts tasks by status, plus overdue and rescheduled counts.
func (e Env) TasksSummary(ctx context.Context, project, since string) (map[string]any, error) {
	if err := e.require(ctx); err != nil {
		return nil, err
	}
	byStatus := map[string]any{}
	open := 0
	for _, st := range taskStatuses {
		n := e.count(ctx, project, map[string]any{"fields.status": st})
		byStatus[st] = n
		if st == "todo" || st == "doing" || st == "reschedule" {
			open += n
		}
	}
	today := time.Now().Format("2006-01-02")
	doneWhere := map[string]any{"fields.status": "done"}
	if since != "" {
		doneWhere["fields.completed_at"] = map[string]any{"gte": since}
	}
	return map[string]any{
		"by_status": byStatus, "open": open, "done": e.count(ctx, project, doneWhere),
		"overdue":     e.count(ctx, project, map[string]any{"and": []any{map[string]any{"fields.status": map[string]any{"in": []any{"todo", "doing", "reschedule"}}}, map[string]any{"fields.due.date": map[string]any{"lt": today}}}}),
		"rescheduled": e.count(ctx, project, map[string]any{"fields.reschedule.count": map[string]any{"gt": 0}}),
	}, nil
}

// ContextSection builds the compact Dock block for load_memory(include=["dock"]).
func (e Env) ContextSection(ctx context.Context, project string) (map[string]any, error) {
	if !Installed(ctx, e.Store) {
		return map[string]any{"installed": false}, nil
	}
	t, err := e.Today(ctx, project, 1)
	if err != nil {
		return nil, err
	}
	agenda, _ := t["agenda"].([]map[string]any)
	if len(agenda) > 8 {
		agenda = agenda[:8]
	}
	short := make([]map[string]any, 0, len(agenda))
	for _, a := range agenda {
		short = append(short, map[string]any{"id": a["id"], "title": a["title"], "due": a["due"], "status": a["status"]})
	}
	return map[string]any{"installed": true, "today": short, "overdue": t["overdue"], "pending_asks": t["pending_asks"], "active_plans": t["active_plans"]}, nil
}
