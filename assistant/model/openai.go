package model

import (
	"encoding/json"
	"net/url"
	"strconv"
)

// codec is one kind of API: its call's URL and body, and the parse of its
// streamed answer. A codec keeps the state of one answer's parse: each
// attempt has its own.
type codec interface {
	call(p *Provider, r *Request) (url string, body []byte)
	// parse takes one event of the answer: an SSE event's name and data,
	// or an NDJSON line with no name. data is valid only during the call.
	parse(a *answer, name string, data []byte) error
	// finish takes the end of the answer's body.
	finish(a *answer) error
	ndjson() bool
}

func newCodec(kind string) codec {
	switch kind {
	case KindAnthropic:
		return &anthropicCodec{}
	case KindGemini:
		return &geminiCodec{}
	case KindOllama:
		return &ollamaCodec{}
	case KindOpenAIResponses:
		return &responsesCodec{}
	}
	return &chatCodec{kind: kind}
}

// ---- Chat Completions: openai, azure_openai, openai_compatible, llm_proxy ----

type chatCodec struct {
	kind     string
	finished bool // a choice gave its finish_reason
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function functionDecl `json:"function"`
}

type functionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (c *chatCodec) ndjson() bool { return false }

func (c *chatCodec) call(p *Provider, r *Request) (string, []byte) {
	u := p.BaseURL + "/chat/completions"
	if c.kind == KindAzureOpenAI {
		u = p.BaseURL + "/openai/deployments/" + url.PathEscape(r.Model) + "/chat/completions?api-version=" + url.QueryEscape(p.APIVersion)
	}
	msgs := make([]chatMessage, 0, len(r.Messages)+1)
	if r.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: r.System})
	}
	for _, m := range r.Messages {
		switch m.Role {
		case RoleTool:
			msgs = append(msgs, chatMessage{Role: "tool", Content: m.Content, ToolCallID: m.ToolCallID})
		case RoleAssistant:
			cm := chatMessage{Role: "assistant", Content: m.Content}
			if m.Content == "" && len(m.ToolCalls) > 0 {
				cm.Content = nil
			}
			for _, tc := range m.ToolCalls {
				var t chatToolCall
				t.ID, t.Type = tc.ID, "function"
				t.Function.Name, t.Function.Arguments = tc.Name, toolArgs(tc)
				cm.ToolCalls = append(cm.ToolCalls, t)
			}
			msgs = append(msgs, cm)
		default:
			msgs = append(msgs, chatMessage{Role: "user", Content: m.Content})
		}
	}
	body := map[string]any{"messages": msgs, "stream": true, "stream_options": map[string]bool{"include_usage": true}}
	if c.kind != KindAzureOpenAI {
		body["model"] = r.Model
	}
	switch c.kind {
	case KindOpenAI, KindAzureOpenAI:
		body["max_completion_tokens"] = r.maxTokens()
		if r.thinking() {
			body["reasoning_effort"] = r.Thinking
		}
	default:
		body["max_tokens"] = r.maxTokens()
	}
	if r.Temperature != nil {
		body["temperature"] = *r.Temperature
	}
	if len(r.Tools) > 0 {
		tools := make([]chatTool, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = chatTool{Type: "function", Function: functionDecl{Name: t.Name, Description: t.Description, Parameters: t.Parameters}}
		}
		body["tools"] = tools
	}
	return u, mustJSON(body)
}

// toolArgs returns a call's arguments as the text the model wrote: a
// call marked Invalid has them as a JSON string.
func toolArgs(tc ToolCall) string {
	if tc.Invalid {
		var s string
		if json.Unmarshal(tc.Arguments, &s) == nil {
			return s
		}
	}
	if len(tc.Arguments) == 0 {
		return "{}"
	}
	return string(tc.Arguments)
}

// mustJSON marshals what cannot fail: strings, numbers, and JSON that
// Request.check has checked.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

type chatChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			Reasoning        string         `json:"reasoning"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (c *chatCodec) parse(a *answer, _ string, data []byte) error {
	if string(data) == "[DONE]" {
		a.done = true
		return a.endAll()
	}
	var ch chatChunk
	if err := json.Unmarshal(data, &ch); err != nil {
		return errBadEvent(err)
	}
	if len(ch.Error) > 0 && string(ch.Error) != "null" {
		return streamError(data)
	}
	if ch.Model != "" {
		a.res.Model = ch.Model
	}
	for _, choice := range ch.Choices {
		d := choice.Delta
		if err := a.thinking(d.ReasoningContent + d.Reasoning); err != nil {
			return err
		}
		if err := a.text(d.Content); err != nil {
			return err
		}
		for i, tc := range d.ToolCalls {
			key := strconv.Itoa(i)
			if tc.Index != nil {
				key = strconv.Itoa(*tc.Index)
			}
			pc, err := a.begin(key, tc.ID, tc.Function.Name)
			if err != nil {
				return err
			}
			pc.args.WriteString(tc.Function.Arguments)
		}
		if choice.FinishReason != "" {
			a.res.StopReason = choice.FinishReason
			c.finished = true
			if err := a.endAll(); err != nil {
				return err
			}
		}
	}
	if u := ch.Usage; u != nil {
		a.res.Usage.InputTokens, a.res.Usage.OutputTokens = u.PromptTokens, u.CompletionTokens
		if u.PromptTokensDetails != nil {
			a.res.Usage.CachedTokens = u.PromptTokensDetails.CachedTokens
		}
		if u.CompletionTokensDetails != nil {
			a.res.Usage.ThinkingTokens = u.CompletionTokensDetails.ReasoningTokens
		}
	}
	return nil
}

func (c *chatCodec) finish(a *answer) error {
	if !a.done && !c.finished {
		return ErrIncomplete
	}
	return a.endAll()
}
