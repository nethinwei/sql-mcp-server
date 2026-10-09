package graph

import "github.com/nethinwei/sql-mcp-server/x/admin/configapp"

// The configuration service's results and inputs, under the names of their
// GraphQL types (gqlgen binds those names in this package).
type (
	CatalogCounts     = configapp.CatalogCounts
	CatalogCoverage   = configapp.CatalogCoverage
	CatalogFilter     = configapp.CatalogFilter
	CatalogLinks      = configapp.CatalogLinks
	CatalogPage       = configapp.CatalogPage
	CatalogRow        = configapp.CatalogRow
	McpToken          = configapp.McpToken
	McpTokenInput     = configapp.McpTokenInput
	McpTokenSecret    = configapp.McpTokenSecret
	PendingPlan       = configapp.PendingPlan
	PendingState      = configapp.PendingState
	PendingStateInput = configapp.PendingStateInput
	PendingTable      = configapp.PendingTable
	PendingWork       = configapp.PendingWork
	PlanChange        = configapp.PlanChange
	PlanException     = configapp.PlanException
	PlanObjects       = configapp.PlanObjects
	PlanReport        = configapp.PlanReport
	PlanTemplate      = configapp.PlanTemplate
	PlanTemplateInput = configapp.PlanTemplateInput
	ScanBatch         = configapp.ScanBatch
	ScanBatchInput    = configapp.ScanBatchInput
	ScanBatchItem     = configapp.ScanBatchItem
	ScanJob           = configapp.ScanJob
	ScanState         = configapp.ScanState
	SchemaInfo        = configapp.SchemaInfo
	SchemaList        = configapp.SchemaList
	ScopeCondition    = configapp.ScopeCondition
	ScopeStats        = configapp.ScopeStats
	Simulation        = configapp.Simulation
	TableLink         = configapp.TableLink
	TableStatus       = configapp.TableStatus
	UnscannedSchemas  = configapp.UnscannedSchemas
	Validation        = configapp.Validation
)

// The values of the bound enums.
const (
	ScanStatePending     = configapp.ScanStatePending
	ScanStateRunning     = configapp.ScanStateRunning
	ScanStateDone        = configapp.ScanStateDone
	ScanStateFailed      = configapp.ScanStateFailed
	ScanStateCanceled    = configapp.ScanStateCanceled
	TableStatusNew       = configapp.TableStatusNew
	TableStatusWorkspace = configapp.TableStatusWorkspace
	TableStatusPublished = configapp.TableStatusPublished
)
