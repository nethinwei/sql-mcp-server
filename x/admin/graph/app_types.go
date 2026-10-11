package graph

import "github.com/nethinwei/sql-mcp-server/x/admin/configapp"

// The configuration service's results and inputs, under the names of their
// GraphQL types (gqlgen binds those names in this package).
type (
	// The configuration read model is the workspace's items: a value as the
	// configuration says it, null when unset.
	// (Field is bound in gqlgen.yml: an alias of that name would also bind
	// the introspection type __Field.)
	Entity       = configapp.EntityItem
	Relationship = configapp.RelationshipItem
	EntityMCP    = configapp.EntityMCPItem
	Grant        = configapp.GrantItem
	Role         = configapp.RoleItem
	User         = configapp.UserItem

	CatalogCounts      = configapp.CatalogCounts
	FieldReference     = configapp.FieldReference
	CatalogCoverage    = configapp.CatalogCoverage
	CatalogFilter      = configapp.CatalogFilter
	CatalogLinks       = configapp.CatalogLinks
	CatalogPage        = configapp.CatalogPage
	CatalogRow         = configapp.CatalogRow
	McpToken           = configapp.McpToken
	McpTokenInput      = configapp.McpTokenInput
	McpTokenSecret     = configapp.McpTokenSecret
	PendingPlan        = configapp.PendingPlan
	PendingState       = configapp.PendingState
	PendingStateInput  = configapp.PendingStateInput
	PendingTable       = configapp.PendingTable
	PendingWork        = configapp.PendingWork
	PlanChange         = configapp.PlanChange
	PlanException      = configapp.PlanException
	PlanObjects        = configapp.PlanObjects
	PlanReport         = configapp.PlanReport
	PlanTemplate       = configapp.PlanTemplate
	PlanTemplateInput  = configapp.PlanTemplateInput
	ScanBatch          = configapp.ScanBatch
	ScanBatchInput     = configapp.ScanBatchInput
	ScanBatchItem      = configapp.ScanBatchItem
	ScanJob            = configapp.ScanJob
	SchemaInfo         = configapp.SchemaInfo
	SchemaList         = configapp.SchemaList
	ScopeCondition     = configapp.ScopeCondition
	ScopeStats         = configapp.ScopeStats
	Simulation         = configapp.Simulation
	TableLink          = configapp.TableLink
	UnscannedSchemas   = configapp.UnscannedSchemas
	Validation         = configapp.Validation
	Workspace          = configapp.Workspace
	WorkspaceConflict  = configapp.WorkspaceConflict
	WorkspaceNote      = configapp.WorkspaceNote
	WorkspaceRef       = configapp.WorkspaceRef
	WorkspaceLogEntry  = configapp.WorkspaceLogEntry
	CommitInput        = configapp.CommitInput
	AppliedPlan        = configapp.AppliedPlan
	WorkspaceEditInput = configapp.WorkspaceEditInput
	WorkspaceItemInput = configapp.WorkspaceItemInput
)
