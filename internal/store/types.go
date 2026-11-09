package store

import "time"

type Artifact struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Content     string         `json:"content"`
	Project     string         `json:"project,omitempty"`
	Agent       string         `json:"agent"`
	Kind        string         `json:"kind"`
	CreatedAt   time.Time      `json:"created_at"`
	ContentHash string         `json:"content_hash"`
	Fields      map[string]any `json:"fields,omitempty"`
}

type ChangeEvent struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	TargetID  string    `json:"target_id"`
	Action    string    `json:"action"`
	Revision  int64     `json:"revision"`
	Summary   string    `json:"summary"`
	Actor     string    `json:"actor"`
	Sensitive bool      `json:"sensitive"`
	CreatedAt time.Time `json:"created_at"`
}

type ActivityDay struct {
	Label string `json:"label"`
	Count int    `json:"count"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
}

type Memory struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Body      string    `json:"body"`
	Project   string    `json:"project,omitempty"`
	Kind      string    `json:"kind"`
	SourceID  string    `json:"source_id,omitempty"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ContextPack struct {
	SnapshotID string     `json:"snapshot_id"`
	Session    string     `json:"session,omitempty"`
	Project    string     `json:"project,omitempty"`
	Revision   int64      `json:"revision"`
	Context    string     `json:"context"`
	Artifacts  []Artifact `json:"artifacts"`
	Memories   []Memory   `json:"memories"`
}

type Counts struct {
	Artifacts     int64 `json:"artifacts"`
	Memories      int64 `json:"memories"`
	Projects      int64 `json:"projects"`
	AddedThisWeek int64 `json:"added_this_week"`
	Changes       int64 `json:"changes"`
	SecurityFlags int64 `json:"security_flags"`
}
