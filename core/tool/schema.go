package tool

import (
	_ "embed"
	"encoding/json"
)

// JSON Schemas for the entity tools. These describe inputs to MCP clients
// (agents see them via tools/list); tool.Run still parses and validates inputs.
// The filter condition shape is repeated inline to keep each schema standalone.

//go:embed schema/describe.json
var schemaDescribe json.RawMessage

//go:embed schema/read.json
var schemaRead json.RawMessage

//go:embed schema/create.json
var schemaCreate json.RawMessage

//go:embed schema/update.json
var schemaUpdate json.RawMessage

//go:embed schema/delete.json
var schemaDelete json.RawMessage

//go:embed schema/execute.json
var schemaExecute json.RawMessage

//go:embed schema/aggregate.json
var schemaAggregate json.RawMessage

//go:embed schema/begin_transaction.json
var schemaBeginTransaction json.RawMessage

//go:embed schema/transaction_token.json
var transactionTokenSchema json.RawMessage
