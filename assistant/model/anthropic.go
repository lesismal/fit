package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// errBadEvent is an event of the answer that does not parse.
func errBadEvent(err error) error {
	return fmt.Errorf("the model API sent an event that is not the JSON expected: %w", err)
}

// streamError is an error the API sent in the middle of its answer.
func streamError(data []byte) error {
	return &APIError{Status: 200, Message: apiMessage(data)}
}

// AnthropicVersion is the anthropic-version the calls send.
const AnthropicVersion = "2023-06-01"

// ---- Anthropic's Messages ----

type anthropicCodec struct {
	// blocks are the kinds of the content blocks begun, by index.
	blocks map[int]string
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (c *anthropicCodec) ndjson() bool { return false }

// call merges the messages of one role in a row into one, as the API
// wants the roles to alternate: tool results go as the user's.
func (c *anthropicCodec) call(p *Provider, r *Request) (string, []byte) {
	var msgs []anthropicMessage
	add := func(role string, blocks ...map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, anthropicMessage{Role: role, Content: blocks})
	}
	for _, m := range r.Messages {
		switch m.Role {
		case RoleTool:
			b := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}
			if m.IsError {
				b["is_error"] = true
			}
			add("user", b)
		case RoleAssistant:
			var blocks []map[string]any
			if m.Thinking != "" && m.ThinkingSignature != "" {
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": m.Thinking, "signature": m.ThinkingSignature})
			}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": toolInput(tc)})
			}
			add("assistant", blocks...)
		default:
			if m.Content != "" {
				add("user", map[string]any{"type": "text", "text": m.Content})
			}
		}
	}
	if msgs == nil {
		msgs = []anthropicMessage{}
	}
	body := map[string]any{"model": r.Model, "max_tokens": r.maxTokens(), "messages": msgs, "stream": true}
	if r.System != "" {
		body["system"] = r.System
	}
	if r.thinking() {
		// The budget is spent within max_tokens, which must exceed it; the
		// API takes no temperature with thinking.
		b := thinkingBudget(r.Thinking)
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": b}
		body["max_tokens"] = r.maxTokens() + b
	} else if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if len(r.Tools) > 0 {
		tools := make([]anthropicTool, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = anthropicTool{Name: t.Name, Description: t.Description, InputSchema: schemaOrEmpty(t.Parameters)}
		}
		body["tools"] = tools
	}
	return p.BaseURL + "/messages", mustJSON(body)
}

// toolInput returns a call's arguments as a JSON object: those of a call
// marked Invalid as {}, since the APIs that take an object refuse a string.
func toolInput(tc ToolCall) json.RawMessage {
	if tc.Invalid || len(tc.Arguments) == 0 {
		return json.RawMessage("{}")
	}
	return tc.Arguments
}

func schemaOrEmpty(s json.RawMessage) json.RawMessage {
	if len(s) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return s
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	CacheReadTokens int `json:"cache_read_input_tokens"`
	CacheCreation   int `json:"cache_creation_input_tokens"`
}

func (c *anthropicCodec) parse(a *answer, _ string, data []byte) error {
	var e anthropicEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return errBadEvent(err)
	}
	key := strconv.Itoa(e.Index)
	switch e.Type {
	case "message_start":
		if m := e.Message; m != nil {
			a.res.Model = m.Model
			if u := m.Usage; u != nil {
				a.res.Usage.InputTokens = u.InputTokens + u.CacheReadTokens + u.CacheCreation
				a.res.Usage.CachedTokens = u.CacheReadTokens
				a.res.Usage.OutputTokens = u.OutputTokens
			}
		}
	case "content_block_start":
		b := e.ContentBlock
		if b == nil {
			return errBadEvent(errors.New("content_block_start without its block"))
		}
		if c.blocks == nil {
			c.blocks = map[int]string{}
		}
		c.blocks[e.Index] = b.Type
		switch b.Type {
		case "tool_use":
			if _, err := a.begin(key, b.ID, b.Name); err != nil {
				return err
			}
		case "text":
			return a.text(b.Text)
		}
	case "content_block_delta":
		d := e.Delta
		if d == nil {
			return errBadEvent(errors.New("content_block_delta without its delta"))
		}
		switch d.Type {
		case "text_delta":
			return a.text(d.Text)
		case "thinking_delta":
			return a.thinking(d.Thinking)
		case "signature_delta":
			a.res.ThinkingSignature += d.Signature
		case "input_json_delta":
			pc, err := a.begin(key, "", "")
			if err != nil {
				return err
			}
			pc.args.WriteString(d.PartialJSON)
		}
	case "content_block_stop":
		if c.blocks[e.Index] == "tool_use" {
			return a.end(key)
		}
	case "message_delta":
		if e.Delta != nil && e.Delta.StopReason != "" {
			a.res.StopReason = e.Delta.StopReason
		}
		if e.Usage != nil {
			a.res.Usage.OutputTokens = e.Usage.OutputTokens
		}
	case "message_stop":
		a.done = true
		return a.endAll()
	case "error":
		return streamError(data)
	}
	return nil
}

func (c *anthropicCodec) finish(a *answer) error {
	if !a.done {
		return ErrIncomplete
	}
	return nil
}
