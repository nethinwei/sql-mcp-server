package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

const systemPrompt = "You are a data analyst agent. The only way to access data is through the " +
	"provided tools; you have no other database access. Use the tools to answer the user's " +
	"question, then answer concisely. If a tool call is rejected, read the structured error " +
	"and either narrow the request or explain why the request is not allowed."

// startServer assembles the app in-process and connects an in-memory MCP
// session, so the pilot measures the tool contract rather than transport
// differences.
func startServer(ctx context.Context, dsn string) (*mcp.ClientSession, func(), error) {
	if err := os.Setenv("DATABASE_DSN", dsn); err != nil {
		return nil, nil, err
	}
	cfg, err := bootstrap.Load("eval/config.yaml")
	if err != nil {
		return nil, nil, err
	}
	app, err := bootstrap.Assemble(cfg)
	if err != nil {
		return nil, nil, err
	}
	srv := mcpserver.NewServer(app)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serveCtx, stopServe := context.WithCancel(ctx)
	go func() { _ = srv.Run(serveCtx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "eval-pilot", Version: "dev"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		stopServe()
		_ = app.Close()
		return nil, nil, err
	}
	cleanup := func() {
		_ = session.Close()
		stopServe()
		_ = app.Close()
	}
	return session, cleanup, nil
}

func chatToolsFromSession(ctx context.Context, session *mcp.ClientSession) ([]chatTool, error) {
	list, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	tools := make([]chatTool, 0, len(list.Tools))
	for _, t := range list.Tools {
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			return nil, err
		}
		tools = append(tools, chatTool{Type: "function", Function: chatToolFunction{
			Name: t.Name, Description: t.Description, Parameters: schema,
		}})
	}
	return tools, nil
}

// interactionStep is one turn of the ReAct loop, recorded verbatim so a
// failed task can be analyzed from the report alone: "assistant" steps carry
// the model's intermediate text, "tool" steps carry the full call arguments
// and the exact payload returned to the model.
type interactionStep struct {
	Role       string          `json:"role"` // "assistant" | "tool"
	Content    string          `json:"content,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Denied     bool            `json:"denied,omitempty"`
	DenialCode string          `json:"denialCode,omitempty"`
}

type transcript struct {
	Steps       []interactionStep `json:"steps"`
	FinalAnswer string            `json:"finalAnswer"`
	Prompt      int64             `json:"promptTokens"`
	Completion  int64             `json:"completionTokens"`
}

// toolSteps returns the executed tool calls in order.
func (t transcript) toolSteps() []interactionStep {
	steps := make([]interactionStep, 0, len(t.Steps))
	for _, step := range t.Steps {
		if step.Role == "tool" {
			steps = append(steps, step)
		}
	}
	return steps
}

// runConversation drives one task: model turns alternate with tool
// executions until the model produces a final answer or the call budget is
// exhausted.
func runConversation(
	ctx context.Context,
	client *modelClient,
	session *mcp.ClientSession,
	tools []chatTool,
	prompt string,
	maxToolCalls int,
) (transcript, error) {
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}
	var result transcript
	toolCalls := 0
	for toolCalls <= maxToolCalls {
		message, use, err := client.complete(ctx, messages, tools)
		if err != nil {
			return result, err
		}
		result.Prompt += use.PromptTokens
		result.Completion += use.CompletionTokens
		if len(message.ToolCalls) == 0 {
			result.FinalAnswer = message.Content
			return result, nil
		}
		if message.Content != "" {
			result.Steps = append(result.Steps, interactionStep{Role: "assistant", Content: message.Content})
		}
		messages = append(messages, message)
		for _, call := range message.ToolCalls {
			step, content := executeToolCall(ctx, session, call)
			result.Steps = append(result.Steps, step)
			toolCalls++
			messages = append(messages, chatMessage{
				Role: "tool", ToolCallID: call.ID, Content: content,
			})
		}
	}
	return result, nil
}

// executeToolCall runs one tool call and returns both the recorded step and
// the exact payload fed back to the model (the step stores the same payload).
func executeToolCall(ctx context.Context, session *mcp.ClientSession, call toolCall) (interactionStep, string) {
	step := interactionStep{
		Role: "tool", Tool: call.Function.Name,
		Arguments: rawOrQuoted(call.Function.Arguments),
	}
	finish := func(content string) (interactionStep, string) {
		step.Result = json.RawMessage(content)
		return step, content
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		step.Denied = true
		return finish(fmt.Sprintf(`{"error":"invalid tool arguments: %v"}`, err))
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: call.Function.Name, Arguments: args})
	if err != nil {
		step.Denied = true
		return finish(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	if res.IsError {
		step.Denied = true
		step.DenialCode = denialCode(res)
	}
	encoded, err := json.Marshal(res)
	if err != nil {
		return finish(fmt.Sprintf(`{"error":"marshal result: %v"}`, err))
	}
	return finish(string(encoded))
}

// rawOrQuoted embeds s as raw JSON when valid, else as a JSON string, so the
// report stays parseable even when the model emits malformed arguments.
func rawOrQuoted(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	quoted, _ := json.Marshal(s)
	return quoted
}

func denialCode(res *mcp.CallToolResult) string {
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return ""
	}
	var denial struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &denial); err != nil {
		return ""
	}
	return denial.Code
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
