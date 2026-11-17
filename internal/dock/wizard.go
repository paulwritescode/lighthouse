package dock

import (
	"fmt"
	"strings"
)

func opt(value, label string) map[string]any { return map[string]any{"value": value, "label": label} }

func opts(pairs ...string) []any {
	out := make([]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, opt(pairs[i], pairs[i+1]))
	}
	return out
}

// WizardSpec is the "plan from this memory" questionnaire (screen 5.4 in the
// Dock plan). prefill may carry window_weeks, work_days, daily_minutes and
// deadlines, a list of {label, date, source_ref}, found in the source records.
func WizardSpec(title string, sourceTitles []string, prefill map[string]any, project string) map[string]any {
	deadlines := []any{}
	if list, ok := prefill["deadlines"].([]any); ok {
		for i, d := range list {
			dm, _ := d.(map[string]any)
			label, _ := dm["label"].(string)
			date, _ := dm["date"].(string)
			if label == "" || date == "" {
				continue
			}
			deadlines = append(deadlines, opt(fmt.Sprint(i), label+" · "+date))
		}
	}
	defaults := defaultsFrom(prefill)
	weeks := "3"
	if w, ok := prefill["window_weeks"]; ok {
		weeks = fmt.Sprint(int(num(w, 3)))
	}
	sections := []any{
		map[string]any{"id": "when", "title": "How long", "nodes": []any{
			map[string]any{"type": "text", "id": "plan_name", "label": "Plan name", "default": title},
			map[string]any{"type": "chips", "id": "window_weeks", "label": "Length", "default": weeks, "options": opts("1", "1 week", "2", "2 weeks", "3", "3 weeks", "4", "4 weeks", "deadline", "Until the last deadline", "custom", "Custom")},
			map[string]any{"type": "daterange", "id": "window", "label": "Dates", "visible_if": map[string]any{"eq": []any{"$answers.window_weeks", "custom"}}},
			map[string]any{"type": "chips", "id": "start", "label": "Start", "default": "today", "options": opts("today", "Today", "tomorrow", "Tomorrow", "pick", "Pick a date")},
			map[string]any{"type": "date", "id": "start_date", "label": "Start date", "visible_if": map[string]any{"eq": []any{"$answers.start", "pick"}}},
		}},
		map[string]any{"id": "time", "title": "How much time", "nodes": []any{
			map[string]any{"type": "weekdays", "id": "work_days", "label": "Days to work", "default": defaults["work_days"]},
			map[string]any{"type": "slider", "id": "daily_minutes", "label": "Time per day", "default": defaults["daily_minutes"], "props": map[string]any{"min": 30, "max": 480, "step": 15, "unit": "min"}},
			map[string]any{"type": "chips", "id": "time_of_day", "label": "Best time", "default": "morning", "options": opts("morning", "Morning", "afternoon", "Afternoon", "evening", "Evening", "any", "Any")},
			map[string]any{"type": "chips", "id": "intensity", "label": "Pace", "default": defaults["intensity"], "options": opts("light", "Light", "steady", "Steady", "sprint", "Sprint")},
		}},
	}
	if len(deadlines) > 0 {
		all := make([]any, len(deadlines))
		for i, d := range deadlines {
			all[i] = d.(map[string]any)["value"]
		}
		sections = append(sections, map[string]any{"id": "deadlines", "title": "Deadlines I found", "nodes": []any{
			map[string]any{"type": "multichips", "id": "deadlines", "label": "Plan around these", "default": all, "options": deadlines,
				"help": "Dates come from the source records. Untick any that no longer apply."},
		}})
	}
	sections = append(sections, map[string]any{"id": "reminders", "title": "Reminders", "nodes": []any{
		map[string]any{"type": "chips", "id": "reminders", "label": "Style", "default": defaults["reminders"], "options": opts("gentle", "Gentle", "standard", "Standard", "pushy", "Pushy")},
		map[string]any{"type": "multichips", "id": "channels", "label": "Alert me by", "default": []any{"os"}, "options": opts("os", "Mac notification", "inapp", "In Dock", "google_popup", "Google Calendar popup")},
		map[string]any{"type": "chips", "id": "mirror", "label": "Mirror to Google", "default": defaults["mirror"], "options": opts("off", "Off", "calendar", "Calendar", "tasks", "Tasks (date only)")},
		map[string]any{"type": "project", "id": "project", "label": "Project", "default": project},
		map[string]any{"type": "textarea", "id": "notes", "label": "Notes for the planner (optional)", "props": map[string]any{"rows": 3, "max": 1000}},
	}})
	spec := map[string]any{
		"version": 1, "screen": "plan_wizard", "title": "Plan: " + strings.TrimPrefix(title, "Plan: "),
		"subtitle": "From: " + strings.Join(sourceTitles, ", "),
		"sections": sections,
		"actions": []any{
			map[string]any{"id": "draft", "label": "Save draft", "kind": "secondary", "sets": map[string]any{"draft": true}},
			map[string]any{"id": "generate", "label": "Generate plan", "kind": "submit"},
		},
	}
	if project != "" {
		spec["project"] = project
	}
	return spec
}
