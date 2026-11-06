package model

import (
	"encoding/json"
)

// ---- OpenAI's Responses ----

type responsesCodec struct{}

func (c *responsesCodec) ndjson() bool { return false }

func (c *responsesCodec) call(p *Provider, r *Request) (string, []byte) {
	input := make([]map[string]any, 0, len(r.Messages))
	for _, m := range r.Messages {
		switch m.Role {
		case RoleTool:
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
		case RoleAssistant:
			if m.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": m.Content})
			}
			for _, tc := range m.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": toolArgs(tc)})
			}
		default:
			input = append(input, map[string]any{"role": "user", "content": m.Content})
		}
	}
	// store false: the conversation is fit's to keep, not the API's.
	body := map[string]any{"model": r.Model, "input": input, "stream": true, "store": false, "max_output_tokens": r.maxTokens()}
	if r.System != "" {
		body["instructions"] = r.System
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if r.thinking() {
		body["reasoning"] = map[string]string{"effort": r.Thinking, "summary": "auto"}
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": schemaOrEmpty(t.Parameters)}
		}
		body["tools"] = tools
	}
	return p.BaseURL + "/responses", mustJSON(body)
}

type responsesItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesBody struct {
	Model             string `json:"model"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type responsesEvent struct {
	Type     string         `json:"type"`
	Delta    string         `json:"delta"`
	ItemID   string         `json:"item_id"`
	Item     *responsesItem `json:"item"`
	Response *responsesBody `json:"response"`
}

func (c *responsesCodec) parse(a *answer, name string, data []byte) error {
	var e responsesEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return errBadEvent(err)
	}
	if e.Type == "" {
		e.Type = name
	}
	switch e.Type {
	case "response.created", "response.in_progress":
		if e.Response != nil && e.Response.Model != "" {
			a.res.Model = e.Response.Model
		}
	case "response.output_text.delta", "response.refusal.delta":
		return a.text(e.Delta)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return a.thinking(e.Delta)
	case "response.output_item.added":
		if it := e.Item; it != nil && it.Type == "function_call" {
			pc, err := a.begin(it.ID, it.CallID, it.Name)
			if err != nil {
				return err
			}
			pc.args.WriteString(it.Arguments)
		}
	case "response.function_call_arguments.delta":
		pc, err := a.begin(e.ItemID, "", "")
		if err != nil {
			return err
		}
		pc.args.WriteString(e.Delta)
	case "response.output_item.done":
		if it := e.Item; it != nil && it.Type == "function_call" {
			pc, err := a.begin(it.ID, it.CallID, it.Name)
			if err != nil {
				return err
			}
			if it.Arguments != "" {
				pc.args.Reset()
				pc.args.WriteString(it.Arguments)
			}
			return a.end(it.ID)
		}
	case "response.completed", "response.incomplete":
		a.done = true
		if r := e.Response; r != nil {
			a.res.StopReason = r.Status
			if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
				a.res.StopReason = r.IncompleteDetails.Reason
			}
			if r.Model != "" {
				a.res.Model = r.Model
			}
			if u := r.Usage; u != nil {
				a.res.Usage.InputTokens, a.res.Usage.OutputTokens = u.InputTokens, u.OutputTokens
				if u.InputTokensDetails != nil {
					a.res.Usage.CachedTokens = u.InputTokensDetails.CachedTokens
				}
				if u.OutputTokensDetails != nil {
					a.res.Usage.ThinkingTokens = u.OutputTokensDetails.ReasoningTokens
				}
			}
		}
		return a.endAll()
	case "response.failed":
		if e.Response != nil && len(e.Response.Error) > 0 {
			return streamError(mustJSON(map[string]json.RawMessage{"error": e.Response.Error}))
		}
		return streamError(data)
	case "error":
		return streamError(data)
	}
	return nil
}

func (c *responsesCodec) finish(a *answer) error {
	if !a.done {
		return ErrIncomplete
	}
	return nil
}
