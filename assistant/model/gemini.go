package model

import (
	"encoding/json"
	"net/url"
)

// ---- Gemini's generateContent ----

type geminiCodec struct{}

type geminiContent struct {
	Role  string           `json:"role"`
	Parts []map[string]any `json:"parts"`
}

func (c *geminiCodec) ndjson() bool { return false }

func (c *geminiCodec) call(p *Provider, r *Request) (string, []byte) {
	var contents []geminiContent
	add := func(role string, parts ...map[string]any) {
		if len(parts) == 0 {
			return
		}
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
			return
		}
		contents = append(contents, geminiContent{Role: role, Parts: parts})
	}
	for _, m := range r.Messages {
		switch m.Role {
		case RoleTool:
			add("user", map[string]any{"functionResponse": map[string]any{"name": m.ToolName, "response": toolResponse(m)}})
		case RoleAssistant:
			var parts []map[string]any
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				part := map[string]any{"functionCall": map[string]any{"name": tc.Name, "args": toolInput(tc)}}
				if tc.Signature != "" {
					part["thoughtSignature"] = tc.Signature
				}
				parts = append(parts, part)
			}
			add("model", parts...)
		default:
			if m.Content != "" {
				add("user", map[string]any{"text": m.Content})
			}
		}
	}
	if contents == nil {
		contents = []geminiContent{}
	}
	gen := map[string]any{"maxOutputTokens": r.maxTokens()}
	if r.Temperature != nil {
		gen["temperature"] = *r.Temperature
	}
	if r.thinking() {
		gen["thinkingConfig"] = map[string]any{"thinkingBudget": thinkingBudget(r.Thinking), "includeThoughts": true}
	}
	body := map[string]any{"contents": contents, "generationConfig": gen}
	if r.System != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": r.System}}}
	}
	if len(r.Tools) > 0 {
		decls := make([]map[string]any, len(r.Tools))
		for i, t := range r.Tools {
			decls[i] = map[string]any{"name": t.Name, "description": t.Description, "parametersJsonSchema": schemaOrEmpty(t.Parameters)}
		}
		body["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	return p.BaseURL + "/models/" + url.PathEscape(r.Model) + ":streamGenerateContent?alt=sse", mustJSON(body)
}

// toolResponse is a tool's result as Gemini takes it, an object: the result
// itself when it is one, else {"result": ...}, or {"error": ...} for a
// tool that failed.
func toolResponse(m Message) any {
	if m.IsError {
		return map[string]string{"error": m.Content}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(m.Content), &obj) == nil && obj != nil {
		return json.RawMessage(m.Content)
	}
	return map[string]string{"result": m.Content}
}

type geminiChunk struct {
	ModelVersion string `json:"modelVersion"`
	Candidates   []struct {
		Content struct {
			Parts []struct {
				Text             string `json:"text"`
				Thought          bool   `json:"thought"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					ID   string          `json:"id"`
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
	Error json.RawMessage `json:"error"`
}

func (c *geminiCodec) parse(a *answer, _ string, data []byte) error {
	var ch geminiChunk
	if err := json.Unmarshal(data, &ch); err != nil {
		return errBadEvent(err)
	}
	if len(ch.Error) > 0 {
		return streamError(data)
	}
	if ch.ModelVersion != "" {
		a.res.Model = ch.ModelVersion
	}
	for _, cand := range ch.Candidates {
		for _, part := range cand.Content.Parts {
			switch {
			case part.FunctionCall != nil:
				fc := part.FunctionCall
				if err := a.call(ToolCall{ID: fc.ID, Name: fc.Name, Arguments: fc.Args, Signature: part.ThoughtSignature}); err != nil {
					return err
				}
			case part.Thought:
				if err := a.thinking(part.Text); err != nil {
					return err
				}
			default:
				if err := a.text(part.Text); err != nil {
					return err
				}
			}
		}
		if cand.FinishReason != "" {
			a.res.StopReason = cand.FinishReason
			a.done = true
		}
	}
	if u := ch.UsageMetadata; u != nil {
		a.res.Usage = Usage{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount,
			ThinkingTokens: u.ThoughtsTokenCount, CachedTokens: u.CachedContentTokenCount}
	}
	return nil
}

func (c *geminiCodec) finish(a *answer) error {
	if !a.done {
		return ErrIncomplete
	}
	return nil
}

// ---- Ollama's /api/chat ----

type ollamaCodec struct{}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func (c *ollamaCodec) ndjson() bool { return true }

func (c *ollamaCodec) call(p *Provider, r *Request) (string, []byte) {
	msgs := make([]ollamaMessage, 0, len(r.Messages)+1)
	if r.System != "" {
		msgs = append(msgs, ollamaMessage{Role: "system", Content: r.System})
	}
	for _, m := range r.Messages {
		switch m.Role {
		case RoleTool:
			msgs = append(msgs, ollamaMessage{Role: "tool", Content: m.Content, ToolName: m.ToolName})
		case RoleAssistant:
			om := ollamaMessage{Role: "assistant", Content: m.Content}
			for _, tc := range m.ToolCalls {
				var t ollamaToolCall
				t.Function.Name, t.Function.Arguments = tc.Name, toolInput(tc)
				om.ToolCalls = append(om.ToolCalls, t)
			}
			msgs = append(msgs, om)
		default:
			msgs = append(msgs, ollamaMessage{Role: "user", Content: m.Content})
		}
	}
	opts := map[string]any{"num_predict": r.maxTokens()}
	if r.Temperature != nil {
		opts["temperature"] = *r.Temperature
	}
	body := map[string]any{"model": r.Model, "messages": msgs, "stream": true, "options": opts}
	if r.thinking() {
		body["think"] = true
	}
	if len(r.Tools) > 0 {
		tools := make([]chatTool, len(r.Tools))
		for i, t := range r.Tools {
			tools[i] = chatTool{Type: "function", Function: functionDecl{Name: t.Name, Description: t.Description, Parameters: schemaOrEmpty(t.Parameters)}}
		}
		body["tools"] = tools
	}
	return p.BaseURL + "/api/chat", mustJSON(body)
}

type ollamaLine struct {
	Model   string `json:"model"`
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

func (c *ollamaCodec) parse(a *answer, _ string, data []byte) error {
	var l ollamaLine
	if err := json.Unmarshal(data, &l); err != nil {
		return errBadEvent(err)
	}
	if l.Error != "" {
		return streamError(data)
	}
	if l.Model != "" {
		a.res.Model = l.Model
	}
	if err := a.thinking(l.Message.Thinking); err != nil {
		return err
	}
	if err := a.text(l.Message.Content); err != nil {
		return err
	}
	for _, tc := range l.Message.ToolCalls {
		if err := a.call(ToolCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments}); err != nil {
			return err
		}
	}
	if l.Done {
		a.done = true
		a.res.StopReason = l.DoneReason
		a.res.Usage.InputTokens, a.res.Usage.OutputTokens = l.PromptEvalCount, l.EvalCount
	}
	return nil
}

func (c *ollamaCodec) finish(a *answer) error {
	if !a.done {
		return ErrIncomplete
	}
	return nil
}
