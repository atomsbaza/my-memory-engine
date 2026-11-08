// Decode targets for the frozen tool schemas.
//
// The schemas themselves live in tools.frozen.json (their descriptions, enums,
// defaults and required lists are the baseline's); these structs only carry the
// arguments into the handlers. Required fields are the non-pointer fields
// without omitempty, matching the baseline's "required" lists — the SDK
// validates against the frozen schema before a handler runs.
package mcp

// ContextArgs is memory_context's arguments: budgeted retrieval by task.
type ContextArgs struct {
	Query   string `json:"query,omitempty"`
	Project string `json:"project,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Budget  int    `json:"budget,omitempty"`
	NoTouch bool   `json:"no_touch,omitempty"`
}

// SearchArgs is memory_search's arguments: query is required.
type SearchArgs struct {
	Query           string `json:"query"`
	Layer           string `json:"layer,omitempty"`
	Scope           string `json:"scope,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	IncludeInactive bool   `json:"include_inactive,omitempty"`
	Scores          bool   `json:"scores,omitempty"`
}

// ShowArgs is memory_show's arguments: id is required.
type ShowArgs struct {
	ID string `json:"id"`
}

// CiteArgs is memory_cite's arguments: id is required.
type CiteArgs struct {
	ID string `json:"id"`
}

// AddArgs is memory_add's arguments: text is required (the body).
type AddArgs struct {
	Text        string   `json:"text"`
	Layer       string   `json:"layer,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Title       string   `json:"title,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	Decay       float64  `json:"decay,omitempty"`
	HalfLife    string   `json:"half_life,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Expires     string   `json:"expires,omitempty"`
	Supersedes  string   `json:"supersedes,omitempty"`
	Supersede   bool     `json:"supersede,omitempty"`
	Source      string   `json:"source,omitempty"`
	Sensitivity string   `json:"sensitivity,omitempty"`
	Force       bool     `json:"force,omitempty"`
}

// ListArgs is memory_list's arguments.
type ListArgs struct {
	Layer string `json:"layer,omitempty"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// VerifyArgs is memory_verify's arguments: no parameters.
type VerifyArgs struct{}

// StatsArgs is memory_stats's arguments: no parameters.
type StatsArgs struct{}

// SyncArgs is memory_sync's arguments.
type SyncArgs struct {
	Message string `json:"message,omitempty"`
	NoPush  bool   `json:"no_push,omitempty"`
}

// TraceArgs is memory_trace's arguments: query is required.
type TraceArgs struct {
	Query string `json:"query"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// ArchiveArgs is memory_archive's arguments. Threshold is a pointer so an
// explicit 0 stays distinguishable from an absent one (the baseline applies its
// 0.15 default only when the argument is missing).
type ArchiveArgs struct {
	Threshold *float64 `json:"threshold,omitempty"`
	DryRun    bool     `json:"dry_run,omitempty"`
}

// PromoteArgs is memory_promote's arguments.
type PromoteArgs struct {
	MinUses int  `json:"min_uses,omitempty"`
	Force   bool `json:"force,omitempty"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// CompactArgs is memory_compact's arguments.
type CompactArgs struct {
	Days     int  `json:"days,omitempty"`
	MinGroup int  `json:"min_group,omitempty"`
	DryRun   bool `json:"dry_run,omitempty"`
}
