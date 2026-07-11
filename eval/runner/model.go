package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// modelClient speaks the OpenAI-compatible chat-completions protocol with
// tool calling. Only the fields the runner needs are modeled.
type modelClient struct {
	baseURL string
	apiKey  string
	model   string
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// complete performs one chat-completions round trip with basic retry on
// transient failures (429/5xx), which online endpoints emit routinely.
func (c *modelClient) complete(
	ctx context.Context,
	messages []chatMessage,
	tools []chatTool,
) (chatMessage, usage, error) {
	payload, err := json.Marshal(chatRequest{Model: c.model, Messages: messages, Tools: tools})
	if err != nil {
		return chatMessage{}, usage{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return chatMessage{}, usage{}, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		message, use, retryable, err := c.completeOnce(ctx, payload)
		if err == nil {
			return message, use, nil
		}
		lastErr = err
		if !retryable {
			break
		}
	}
	return chatMessage{}, usage{}, lastErr
}

func (c *modelClient) completeOnce(ctx context.Context, payload []byte) (chatMessage, usage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return chatMessage{}, usage{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return chatMessage{}, usage{}, true, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return chatMessage{}, usage{}, true, err
	}
	if res.StatusCode != http.StatusOK {
		retryable := res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
		return chatMessage{}, usage{}, retryable,
			fmt.Errorf("chat/completions status %d: %s", res.StatusCode, truncate(body, 512))
	}
	var decoded chatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return chatMessage{}, usage{}, false, fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return chatMessage{}, usage{}, false, fmt.Errorf("model error: %s", decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return chatMessage{}, usage{}, false, fmt.Errorf("no choices in response: %s", truncate(body, 512))
	}
	return decoded.Choices[0].Message, decoded.Usage, false, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
