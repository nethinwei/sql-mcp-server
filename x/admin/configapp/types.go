package configapp

// The results and inputs the configuration service shares with its
// adapters: the admin API binds its GraphQL types of the same names to them
// (gqlgen autobind), and the configuration MCP endpoint returns them as JSON.

import (
	"time"

	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/core/workspace"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
)

type CatalogCounts struct {
	All       int `json:"all"`
	New       int `json:"new"`
	Workspace int `json:"workspace"`
	Published int `json:"published"`
	Drift     int `json:"drift"`
}

// What has been scanned of a datasource.
type CatalogCoverage struct {
	Datasource string `json:"datasource"`
	// Schemas scanned.
	Scanned int `json:"scanned"`
	// Schemas listed; null when never listed.
	Listed *int `json:"listed,omitempty"`
	// Listed schemas never scanned.
	Unscanned []string `json:"unscanned"`
	// When the oldest scanned schema was scanned; null when none.
	OldestScan *time.Time `json:"oldestScan,omitempty"`
}

type CatalogFilter struct {
	// Datasources to search; empty: all.
	Datasources []string `json:"datasources,omitempty"`
	// Glob patterns (* and ?) on schema names.
	Schemas []string `json:"schemas,omitempty"`
	// Glob patterns on table names.
	Tables []string `json:"tables,omitempty"`
	// A part of the table name or its comment.
	Search *string  `json:"search,omitempty"`
	Status []string `json:"status,omitempty"`
	// Only configured tables whose columns and fields differ.
	Drift *bool `json:"drift,omitempty"`
}

type CatalogLinks struct {
	Outgoing []TableLink `json:"outgoing"`
	Incoming []TableLink `json:"incoming"`
}

type CatalogPage struct {
	// Tables matching the filter.
	Total  int          `json:"total"`
	Tables []CatalogRow `json:"tables"`
	// Tables by status, matching every condition but the status.
	Counts   *CatalogCounts    `json:"counts"`
	Coverage []CatalogCoverage `json:"coverage"`
}

type CatalogRow struct {
	// datasource.schema.table: the ID an entity named like the table would have.
	Ref         string `json:"ref"`
	Datasource  string `json:"datasource"`
	Schema      string `json:"schema"`
	Table       string `json:"table"`
	Description string `json:"description"`
	View        bool   `json:"view"`
	Columns     int    `json:"columns"`
	// Foreign keys of the table, and of other scanned tables referencing it.
	ForeignKeys  int `json:"foreignKeys"`
	ReferencedBy int `json:"referencedBy"`
	// Whether a key identifies a row.
	HasKey      bool   `json:"hasKey"`
	SideEffects bool   `json:"sideEffects"`
	Status      string `json:"status"`
	// The entity that reads the table.
	Entity *string `json:"entity,omitempty"`
	// Columns without a field, and fields without a column.
	Added   []string `json:"added"`
	Missing []string `json:"missing"`
	// When the table's schema was scanned.
	ScannedAt *time.Time `json:"scannedAt,omitempty"`
}

type McpToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// null: never expires.
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	// The datasources it reaches; null: every one.
	Datasources []string `json:"datasources,omitempty"`
	// Refused: created read only, which is no longer offered; generate a new one.
	Retired bool `json:"retired"`
}

type McpTokenInput struct {
	// Names the client, unique among the administrator's tokens.
	Name string `json:"name"`
	// Days until it expires; null or 0 never.
	ExpiresInDays *int `json:"expiresInDays,omitempty"`
	// Limits it to these datasources; null: every one.
	Datasources []string `json:"datasources,omitempty"`
}

type McpTokenSecret struct {
	// Shown only once.
	Token string    `json:"token"`
	Info  *McpToken `json:"info"`
}

type PendingPlan struct {
	// Changes when the report does: a new situation opens again.
	Key      string        `json:"key"`
	Template *PlanTemplate `json:"template"`
	Report   *PlanReport   `json:"report"`
	// Why the template no longer evaluates; its report is then empty.
	Error *string       `json:"error,omitempty"`
	State *PendingState `json:"state,omitempty"`
}

type PendingState struct {
	Key string `json:"key"`
	// open, snoozed or ignored; a snooze that ended reads open.
	Status    string     `json:"status"`
	Owner     *string    `json:"owner,omitempty"`
	Reason    *string    `json:"reason,omitempty"`
	Until     *time.Time `json:"until,omitempty"`
	UpdatedBy string     `json:"updatedBy"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

type PendingStateInput struct {
	// The item's key, as PendingWork gives it.
	Key string `json:"key"`
	// open, snoozed or ignored.
	Status string  `json:"status"`
	Owner  *string `json:"owner,omitempty"`
	Reason *string `json:"reason,omitempty"`
	// When a snoozed item opens again.
	Until *time.Time `json:"until,omitempty"`
}

type PendingTable struct {
	// Changes when the differences do.
	Key     string        `json:"key"`
	Ref     string        `json:"ref"`
	Entity  string        `json:"entity"`
	Added   []string      `json:"added"`
	Missing []string      `json:"missing"`
	State   *PendingState `json:"state,omitempty"`
}

// PendingGoneTable is a configured table or view whose table is gone.
type PendingGoneTable struct {
	Key    string `json:"key"`
	Ref    string `json:"ref"`
	Entity string `json:"entity"`
	// Scanned: a scan of its schema missed it; otherwise only the serving
	// configuration found it missing.
	Scanned bool          `json:"scanned"`
	State   *PendingState `json:"state,omitempty"`
}

type PendingWork struct {
	// Recurring templates whose plan would change the configuration or has rules it cannot apply.
	Plans []PendingPlan `json:"plans"`
	// Tables whose fields differ from their columns.
	DriftTables      []PendingTable `json:"driftTables"`
	Drift            int            `json:"drift"`
	NewTables        int            `json:"newTables"`
	UnscannedSchemas int            `json:"unscannedSchemas"`
	// Datasources whose schemas were never listed: what they hold is unknown.
	UnlistedDatasources []string `json:"unlistedDatasources"`
	// Configured tables and views whose tables are gone.
	GoneTables []PendingGoneTable `json:"goneTables"`
}

type PlanChange struct {
	// import, createRole, grant, exclude, mask, syncAdd, syncRemove or removeTable.
	Kind      string   `json:"kind"`
	Principal *string  `json:"principal,omitempty"`
	Actions   []string `json:"actions"`
	Mask      *string  `json:"mask,omitempty"`
	// Fields a field change touches.
	Fields   int      `json:"fields"`
	Entities []string `json:"entities"`
}

type PlanException struct {
	// Identifies the exception within its plan: a draft is saved with each exception to confirm
	// confirmed by its ID.
	ID string `json:"id"`
	// renamed, noKey, sideEffects, relationshipDropped, qualified, unknownUser,
	// noFields, referenced, denied or review.
	Kind string `json:"kind"`
	// block (the proposal cannot be taken), confirm (taking it needs the
	// exception acknowledged) or info.
	Level string `json:"level"`
	// Not only informative: level is block or confirm.
	Warning bool `json:"warning"`
	// The table, when excluding it is a way out.
	Ref    *string `json:"ref,omitempty"`
	Entity *string `json:"entity,omitempty"`
	Detail *string `json:"detail,omitempty"`
	// The server's explanation, when it gives one: Detail translated.
	ReasonDetail *Reason `json:"reasonDetail,omitempty"`
}

type PlanObjects struct {
	Datasource string `json:"datasource"`
	Schema     string `json:"schema"`
	Tables     int    `json:"tables"`
	Imported   int    `json:"imported"`
	Changed    int    `json:"changed"`
}

type PlanReport struct {
	Matched    int                `json:"matched"`
	Excluded   int                `json:"excluded"`
	Objects    []PlanObjects      `json:"objects"`
	Changes    []PlanChange       `json:"changes"`
	Exceptions []PlanException    `json:"exceptions"`
	Unscanned  []UnscannedSchemas `json:"unscanned"`
	// When the tables in scope were scanned, oldest and newest.
	ScannedFrom *time.Time `json:"scannedFrom,omitempty"`
	ScannedTo   *time.Time `json:"scannedTo,omitempty"`
}

type PlanTemplate struct {
	Name string `json:"name"`
	Plan any    `json:"plan"`
	// Its new matches are pending after each scan.
	Recurring bool `json:"recurring"`
	// Bumped by every save.
	Version int `json:"version"`
	// Who saved this version.
	Author    string    `json:"author"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type PlanTemplateInput struct {
	// 1 to 64 characters.
	Name string `json:"name"`
	Plan any    `json:"plan"`
	// Report its new matches as pending.
	Recurring bool `json:"recurring"`
	// The version edited; null creates a template, failing when the name is
	// taken. Another administrator's save meanwhile fails with CONFLICT.
	ExpectedVersion *int `json:"expectedVersion,omitempty"`
}

type ScanBatch struct {
	ID         string `json:"id"`
	Datasource string `json:"datasource"`
	// running, done or canceled.
	State    string `json:"state"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Failed   int    `json:"failed"`
	Canceled int    `json:"canceled"`
	// Schemas waiting or scanning.
	Pending int `json:"pending"`
	// Items are nil in a list: ask for the batch (Service.ScanBatch), or
	// list with them (Service.ScanBatchesWithItems).
	Items      []ScanBatchItem `json:"items"`
	StartedBy  string          `json:"startedBy"`
	StartedAt  time.Time       `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
}

type ScanBatchInput struct {
	Datasource string   `json:"datasource"`
	Schemas    []string `json:"schemas"`
	// Names the request: sent again with the same key, it gets the batch it started.
	RequestKey *string `json:"requestKey,omitempty"`
}

type ScanBatchItem struct {
	Schema string `json:"schema"`
	// pending, running, done, failed or canceled.
	State string  `json:"state"`
	Error *string `json:"error,omitempty"`
}

type ScanJob struct {
	ID         string `json:"id"`
	Datasource string `json:"datasource"`
	State      string `json:"state"`
	// Why the scan failed or stopped.
	Error *string `json:"error,omitempty"`
}

type SchemaInfo struct {
	Name string `json:"name"`
	// When its tables were last scanned; null when never.
	ScannedAt *time.Time `json:"scannedAt,omitempty"`
	// How many tables the last scan found; null when never scanned.
	Tables *int `json:"tables,omitempty"`
}

type SchemaList struct {
	Datasource string `json:"datasource"`
	// Every user schema; when the database cannot list them, one named "" for its
	// default schema, which a scan without schemas reads.
	Schemas []SchemaInfo `json:"schemas"`
	// Where unqualified names resolve; null when unknown.
	DefaultSchema *string   `json:"defaultSchema,omitempty"`
	ListedAt      time.Time `json:"listedAt"`
	// Identifies the database the datasource reads (its driver and read
	// connection); results of another source are stale.
	Source string `json:"source"`
}

type ScopeCondition struct {
	// datasources, schemas, tables, search, status, drift or refs.
	Name string `json:"name"`
	// The condition as written.
	Value   string `json:"value"`
	Matched int    `json:"matched"`
}

type ScopeStats struct {
	Total int `json:"total"`
	// The first matching tables.
	Samples []CatalogRow `json:"samples"`
	// Every condition of the scope with how many tables it selects alone.
	Conditions []ScopeCondition `json:"conditions"`
}

// A foreign key between two scanned tables.
type TableLink struct {
	// The other table.
	Ref  string `json:"ref"`
	Name string `json:"name"`
	// Columns of the referencing table, and the referenced ones.
	Columns    []string `json:"columns"`
	RefColumns []string `json:"refColumns"`
	// The entity reading the other table, if any.
	Entity *string `json:"entity,omitempty"`
}

type UnscannedSchemas struct {
	Datasource string   `json:"datasource"`
	Schemas    []string `json:"schemas"`
}

type Validation struct {
	Ok          bool    `json:"ok"`
	Error       *string `json:"error,omitempty"`
	ContentHash *string `json:"contentHash,omitempty"`
	// Changes against the published revision that need a restart.
	RestartRequired []string `json:"restartRequired"`
	// Grants the routed datasource connections cannot run, and hard limits
	// the connections would not enforce (they do not block publishing unless
	// cost.requireNativeLimits is on, which makes them the Error): their
	// English text, and the same as Notices the console translates.
	Warnings []string `json:"warnings"`
	Notices  []Reason `json:"notices"`
}

// Reason is an explanation (see core/reason): the code the console
// translates, the values of its template's placeholders, the explanations
// some placeholders name, and its English text.
type Reason struct {
	Code   string       `json:"code"`
	Params any          `json:"params"`
	Parts  []ReasonPart `json:"parts"`
	Text   string       `json:"text"`
}

// ReasonPart is an explanation a placeholder of another names.
type ReasonPart struct {
	Name   string `json:"name"`
	Code   string `json:"code"`
	Params any    `json:"params"`
	Text   string `json:"text"`
}

// ReasonOf is r for the API; nil for no explanation.
func ReasonOf(r reason.Reason) *Reason {
	if r.Code == "" && r.Text == "" {
		return nil
	}
	out := &Reason{Code: string(r.Code), Params: reasonParams(r.Params), Parts: []ReasonPart{}, Text: r.Text}
	for _, name := range kit.SortedKeys(r.Parts) {
		p := r.Parts[name]
		out.Parts = append(out.Parts, ReasonPart{Name: name, Code: string(p.Code), Params: reasonParams(p.Params),
			Text: p.Text})
	}
	return out
}

func reasonParams(params map[string]string) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// notices are the explanations for the API, with their texts.
func notices(rs []reason.Reason) ([]Reason, []string) {
	out, texts := make([]Reason, 0, len(rs)), make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, *ReasonOf(r))
		texts = append(texts, r.Text)
	}
	return out, texts
}

// The states of a scan job (GraphQL ScanState), as scanjobs names them.
const (
	ScanStatePending  = "PENDING"
	ScanStateRunning  = "RUNNING"
	ScanStateDone     = "DONE"
	ScanStateFailed   = "FAILED"
	ScanStateCanceled = "CANCELED"
)

// How a scanned table stands to the configuration (GraphQL TableStatus).
const (
	// No entity reads the table.
	TableStatusNew = "NEW"
	// An entity of the workspace reads it, not yet published.
	TableStatusWorkspace = "WORKSPACE"
	// A published entity reads it.
	TableStatusPublished = "PUBLISHED"
)

// Workspace is an administrator's workspace as it stands: the one copy of
// their unpublished work, written by them (in any console) and their agents.
type Workspace struct {
	// The revision it builds on.
	Base      string              `json:"base"`
	Version   int                 `json:"version"`
	Conflicts []WorkspaceConflict `json:"conflicts"`
	// Where changes came from: plans agents applied, drafts taken in.
	Notes []WorkspaceNote `json:"notes"`
	// Drafts taken in or saved from it (not to offer it for review), and the
	// draft it holds as it is (saved from it, nothing changed since).
	Taken      []string   `json:"taken"`
	Checkpoint *string    `json:"checkpoint,omitempty"`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
	// The administrator, or agent:<token name>.
	UpdatedBy string `json:"updatedBy"`
	// Its items and whose it is, read as asked (see Sections and
	// Service.WorkspaceItems): a large workspace is encoded only when read.
	items    workspace.Snapshot
	username string
}

// WorkspaceItems are items of a workspace: all of them (Complete), or those
// changed since a version (an item removed has a nil Value).
type WorkspaceItems struct {
	Complete bool            `json:"complete"`
	Items    []WorkspaceItem `json:"items"`
}

// WorkspaceItem is an item of a workspace.
type WorkspaceItem struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// WorkspaceRef names an item: entity (by ID), role or user (by name), or
// settings.
type WorkspaceRef struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// WorkspaceConflict is a value the workspace and another side changed
// differently; the workspace keeps its value until resolved.
type WorkspaceConflict struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// Where in the item, e.g. fields[email].mask; empty: the whole item.
	Path string `json:"path"`
	// The values (null when removed: MineGone, TheirsGone).
	Mine       any  `json:"mine,omitempty"`
	MineGone   bool `json:"mineGone"`
	Theirs     any  `json:"theirs,omitempty"`
	TheirsGone bool `json:"theirsGone"`
	// published (a newer revision), draft (one taken in) or workspace (a
	// write made meanwhile, by another console or an agent).
	Source string  `json:"source"`
	FromID *string `json:"fromId,omitempty"`
	FromBy *string `json:"fromBy,omitempty"`
}

// WorkspaceNote says where changes came from.
type WorkspaceNote struct {
	// agent or draft.
	Source string `json:"source"`
	By     string `json:"by"`
	Text   string `json:"text"`
}

// WorkspaceLogEntry is a write of a workspace.
type WorkspaceLogEntry struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	// edit, apply, update, reset, merge, resolve, checkpoint or commit.
	Op    string         `json:"op"`
	Items []WorkspaceRef `json:"items"`
	Note  *string        `json:"note,omitempty"`
}

// WorkspaceEditInput writes items of the workspace.
type WorkspaceEditInput struct {
	Items []WorkspaceItemInput `json:"items"`
}

// WorkspaceItemInput writes one item: its value (null removes it) and the
// value its writer read (null: absent). An item written meanwhile merges.
// RenamedFrom is the key of the item of the same kind (an entity or a role)
// it was renamed or moved from, which it replaces; references follow it.
type WorkspaceItemInput struct {
	Kind        string  `json:"kind"`
	Key         string  `json:"key"`
	Value       any     `json:"value,omitempty"`
	Previous    any     `json:"previous,omitempty"`
	RenamedFrom *string `json:"renamedFrom,omitempty"`
}

// CommitInput commits the workspace at the version reviewed.
type CommitInput struct {
	Version           int     `json:"version"`
	ExpectedPublished string  `json:"expectedPublished"`
	Comment           *string `json:"comment,omitempty"`
	RestartRequired   *bool   `json:"restartRequired,omitempty"`
}
