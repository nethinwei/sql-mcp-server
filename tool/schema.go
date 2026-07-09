package tool

import "encoding/json"

// JSON Schemas for the seven DML tools. These describe inputs to MCP clients
// (agents see them via tools/list); tool.Run still parses and validates inputs.
// The filter condition shape is repeated inline to keep each schema standalone.

// schemaDescribe describes describe_entities input.
var schemaDescribe = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string","description":"Entity name; omit to list all entities"}}}`)

// schemaRead describes read_records input.
var schemaRead = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string"},"fields":{"type":"array","items":{"type":"string"}},"filter":{"type":"array","items":{"type":"object","properties":{"field":{"type":"string"},"op":{"type":"string","enum":["eq","ne","gt","gte","lt","lte","in","not_in","like","is_null","is_not_null"]},"value":{"description":"Value (array for in/not_in; omitted for is_null/is_not_null)"}},"required":["field","op"]}},"limit":{"type":"integer","minimum":1},"offset":{"type":"integer","minimum":0},"cursor":{"type":"object","description":"Keyset pagination: last row's primary-key column values to resume after"}},"required":["entity"]}`)

// schemaCreate describes create_record input.
var schemaCreate = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string"},"values":{"type":"object","description":"Column name to value map"}},"required":["entity","values"]}`)

// schemaUpdate describes update_record input.
var schemaUpdate = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string"},"filter":{"type":"array","items":{"type":"object","properties":{"field":{"type":"string"},"op":{"type":"string","enum":["eq","ne","gt","gte","lt","lte","in","not_in","like","is_null","is_not_null"]},"value":{"description":"Value (array for in/not_in; omitted for is_null/is_not_null)"}},"required":["field","op"]}},"set":{"type":"object","description":"Column name to new value map"}},"required":["entity","filter","set"]}`)

// schemaDelete describes delete_record input.
var schemaDelete = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string"},"filter":{"type":"array","items":{"type":"object","properties":{"field":{"type":"string"},"op":{"type":"string","enum":["eq","ne","gt","gte","lt","lte","in","not_in","like","is_null","is_not_null"]},"value":{"description":"Value (array for in/not_in; omitted for is_null/is_not_null)"}},"required":["field","op"]}}},"required":["entity","filter"]}`)

// schemaExecute describes execute_entity input.
var schemaExecute = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string"},"args":{"type":"object","description":"Procedure argument name to value map"}},"required":["entity"]}`)

// schemaAggregate describes aggregate_records input.
var schemaAggregate = json.RawMessage(`{"type":"object","properties":{"entity":{"type":"string"},"groupBy":{"type":"array","items":{"type":"string"}},"aggregates":{"type":"array","items":{"type":"object","properties":{"func":{"type":"string","enum":["count","sum","avg","min","max"]},"field":{"type":"string","description":"Omit for count(*)"}},"required":["func"]}},"filter":{"type":"array","items":{"type":"object","properties":{"field":{"type":"string"},"op":{"type":"string","enum":["eq","ne","gt","gte","lt","lte","in","not_in","like","is_null","is_not_null"]},"value":{"description":"Value (array for in/not_in; omitted for is_null/is_not_null)"}},"required":["field","op"]}}},"required":["entity","aggregates"]}`)
