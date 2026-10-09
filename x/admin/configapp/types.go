package configapp

// The results and inputs the configuration service shares with its
// adapters: the admin API binds its GraphQL types of the same names to them
// (gqlgen autobind), and the configuration MCP endpoint returns them as JSON.

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"time"
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
	Search *string       `json:"search,omitempty"`
	Status []TableStatus `json:"status,omitempty"`
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
	HasKey      bool        `json:"hasKey"`
	SideEffects bool        `json:"sideEffects"`
	Status      TableStatus `json:"status"`
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
	// Reads only: no scans or drafts.
	ReadOnly bool `json:"readOnly"`
	// The datasources it reaches; null: every one.
	Datasources []string `json:"datasources,omitempty"`
}

type McpTokenInput struct {
	// Names the client, unique among the administrator's tokens.
	Name string `json:"name"`
	// Days until it expires; null or 0 never.
	ExpiresInDays *int `json:"expiresInDays,omitempty"`
	// Reads only: no scans or drafts.
	ReadOnly *bool `json:"readOnly,omitempty"`
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
	State    *PendingState `json:"state,omitempty"`
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
}

type PlanChange struct {
	// import, createRole, grant, exclude, mask, syncAdd or syncRemove.
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
	Pending    int             `json:"pending"`
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
	ID         string    `json:"id"`
	Datasource string    `json:"datasource"`
	State      ScanState `json:"state"`
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
	// Grants the routed datasource connections cannot run (they do not block publishing).
	Warnings []string `json:"warnings"`
}

type ScanState string

const (
	ScanStatePending  ScanState = "PENDING"
	ScanStateRunning  ScanState = "RUNNING"
	ScanStateDone     ScanState = "DONE"
	ScanStateFailed   ScanState = "FAILED"
	ScanStateCanceled ScanState = "CANCELED"
)

var AllScanState = []ScanState{
	ScanStatePending,
	ScanStateRunning,
	ScanStateDone,
	ScanStateFailed,
	ScanStateCanceled,
}

func (e ScanState) IsValid() bool {
	switch e {
	case ScanStatePending, ScanStateRunning, ScanStateDone, ScanStateFailed, ScanStateCanceled:
		return true
	}
	return false
}

func (e ScanState) String() string {
	return string(e)
}

func (e *ScanState) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = ScanState(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid ScanState", str)
	}
	return nil
}

func (e ScanState) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprint(w, strconv.Quote(e.String()))
}

func (e *ScanState) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	return e.UnmarshalGQL(s)
}

func (e ScanState) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	e.MarshalGQL(&buf)
	return buf.Bytes(), nil
}

type TableStatus string

const (
	// No entity reads the table.
	TableStatusNew TableStatus = "NEW"
	// An entity of the workspace reads it, not yet published.
	TableStatusWorkspace TableStatus = "WORKSPACE"
	// A published entity reads it.
	TableStatusPublished TableStatus = "PUBLISHED"
)

var AllTableStatus = []TableStatus{
	TableStatusNew,
	TableStatusWorkspace,
	TableStatusPublished,
}

func (e TableStatus) IsValid() bool {
	switch e {
	case TableStatusNew, TableStatusWorkspace, TableStatusPublished:
		return true
	}
	return false
}

func (e TableStatus) String() string {
	return string(e)
}

func (e *TableStatus) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = TableStatus(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid TableStatus", str)
	}
	return nil
}

func (e TableStatus) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprint(w, strconv.Quote(e.String()))
}

func (e *TableStatus) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	return e.UnmarshalGQL(s)
}

func (e TableStatus) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	e.MarshalGQL(&buf)
	return buf.Bytes(), nil
}
