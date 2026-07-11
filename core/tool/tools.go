package tool

// DefaultTools returns the entity and explicit transaction tool instances.
func DefaultTools() []Tool {
	return []Tool{
		DescribeTool{}, ReadTool{}, CreateTool{}, UpdateTool{},
		DeleteTool{}, ExecuteTool{}, AggregateTool{},
		BeginTransactionTool{}, CommitTransactionTool{}, RollbackTransactionTool{},
	}
}
