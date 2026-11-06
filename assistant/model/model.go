// Package model is the assistant's client of model APIs: for each kind of
// API (Anthropic's Messages, OpenAI's Chat Completions and Responses,
// Gemini's generateContent, Azure OpenAI, Ollama's chat, any server that
// speaks Chat Completions, and this fit's own llm.proxy) it builds the
// request from a system prompt, messages and tools, sends it on fib's
// asynchronous HTTP client, and parses the streamed answer, SSE or Ollama's
// NDJSON, as it arrives, into text, thinking and tool calls, with the tokens
// used. See docs/decisions/031-assistant.md.
//
// Nothing here blocks: a call is started, its events and its end are handed
// to callbacks on fib's goroutines, and it is bounded throughout — the
// connect, the first byte, the silence between pieces and the whole call by
// timeouts, a line, an event and the whole answer by sizes, the tool calls by
// number. A call is tried again only while none of its answer has been
// taken, and is abandoned when its caller cancels it, as when the browser
// that asked goes away. A key is never written anywhere but the request's
// own header: errors have it replaced, and Secret prints masked.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The kinds of API, as config.Assistant* names them.
const (
	KindAnthropic        = "anthropic"
	KindOpenAI           = "openai"
	KindOpenAIResponses  = "openai_responses"
	KindGemini           = "gemini"
	KindAzureOpenAI      = "azure_openai"
	KindOllama           = "ollama"
	KindOpenAICompatible = "openai_compatible"
	KindLLMProxy         = "llm_proxy"
)

// Secret is a key: it prints as "***", and marshals so, so that a provider
// logged or encoded by mistake does not give it away. string(s) is the key.
type Secret string

// String returns "***", or "" for no key.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "***"
}

// GoString returns what String does, for %#v.
func (s Secret) GoString() string { return `"` + s.String() + `"` }

// MarshalJSON writes what String does.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// Provider is a model API as a call needs it: where it is, how it is
// reached and with which key, and its bounds.
type Provider struct {
	// Name names it in errors.
	Name string
	// Kind is one of the Kind constants. KindLLMProxy is called as
	// KindOpenAICompatible is.
	Kind string
	// BaseURL is the API's base, without a trailing slash, as
	// https://api.openai.com/v1.
	BaseURL string
	// APIVersion is azure_openai's api-version.
	APIVersion string
	// Key is the API key, "" for none. It is sent only over https or to a
	// loopback host.
	Key Secret
	// Headers are sent with every call.
	Headers map[string]string
	// PinnedCert, when not nil, is the DER of the only certificate an https
	// BaseURL may present, in place of a CA's verification: llm.proxy's own,
	// on loopback.
	PinnedCert []byte
	// The call's bounds: a connect, TLS included; the wait for the first
	// byte of the answer's body; the silence between its pieces; and the
	// whole call. Zero takes the defaults below.
	ConnectTimeout, FirstByteTimeout, IdleTimeout, Timeout time.Duration
	// Retries is how often a call is tried again while none of its answer
	// has been taken; below zero, never.
	Retries int
}

// The defaults of a Provider's bounds.
const (
	DefaultConnectTimeout   = 10 * time.Second
	DefaultFirstByteTimeout = 2 * time.Minute
	DefaultIdleTimeout      = time.Minute
	DefaultTimeout          = 10 * time.Minute
)

func orDur(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// The roles of messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one message of a conversation: the user's words, the model's
// answer with the tools it called, or a tool's result.
type Message struct {
	// Role is RoleUser, RoleAssistant or RoleTool.
	Role string `json:"role"`
	// Content is the text: the user's, the answer's, or the tool's result.
	Content string `json:"content,omitempty"`
	// Thinking and ThinkingSignature are an assistant message's thinking
	// as Anthropic gave it, which goes back with the tool calls it led to.
	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`
	// ToolCalls are the tools an assistant message called.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and ToolName say which call a tool message answers, and
	// IsError that the tool failed.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ToolCall is a call of a tool by the model.
type ToolCall struct {
	// ID is the API's ID for the call, or one made here when it gives none.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is a JSON object; "{}" for none. When the model wrote
	// something that is not JSON, Arguments holds it as a JSON string and
	// Invalid is set, for the caller to hand back as an error.
	Arguments json.RawMessage `json:"arguments"`
	Invalid   bool            `json:"invalid,omitempty"`
	// Signature is Gemini's thought signature of the call, which goes back
	// with it.
	Signature string `json:"signature,omitempty"`
}

// Tool is a tool the model may call: its name, what it does, and a JSON
// Schema of its arguments, an object.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// The levels of thinking, as config.AssistantParams.Thinking.
const (
	ThinkingOff    = "off"
	ThinkingLow    = "low"
	ThinkingMedium = "medium"
	ThinkingHigh   = "high"
)

// thinkingBudget is the budget of tokens the APIs that take one (Anthropic,
// Gemini) get for a level.
func thinkingBudget(level string) int {
	switch level {
	case ThinkingLow:
		return 2048
	case ThinkingMedium:
		return 8192
	case ThinkingHigh:
		return 24576
	}
	return 0
}

// Request is one call: the model, the system prompt, the conversation and
// the tools, and what the answer may be.
type Request struct {
	Model    string
	System   string
	Messages []Message
	Tools    []Tool
	// MaxTokens bounds the answer; 4096 when 0.
	MaxTokens int
	// Temperature, when not nil, is the sampling temperature.
	Temperature *float64
	// Thinking is one of the Thinking levels; off when "".
	Thinking string
}

func (r *Request) maxTokens() int {
	if r.MaxTokens <= 0 {
		return 4096
	}
	return r.MaxTokens
}

func (r *Request) thinking() bool { return r.Thinking != "" && r.Thinking != ThinkingOff }

// The types of events.
const (
	EventText     = "text"
	EventThinking = "thinking"
	EventToolCall = "tool_call"
)

// Event is a piece of the answer as it streams: text, thinking, or a tool
// call once its arguments have all arrived.
type Event struct {
	Type     string
	Text     string
	ToolCall *ToolCall
}

// Usage is what a call took, as the API counts it.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// ThinkingTokens is the part of OutputTokens spent thinking, where the
	// API says; CachedTokens the part of InputTokens read from its cache.
	ThinkingTokens int `json:"thinking_tokens,omitempty"`
	CachedTokens   int `json:"cached_tokens,omitempty"`
}

// Result is a whole answer.
type Result struct {
	// Model is the model the API says answered.
	Model             string     `json:"model,omitempty"`
	Text              string     `json:"text"`
	Thinking          string     `json:"thinking,omitempty"`
	ThinkingSignature string     `json:"thinking_signature,omitempty"`
	ToolCalls         []ToolCall `json:"tool_calls,omitempty"`
	Usage             Usage      `json:"usage"`
	// StopReason is why the answer ended, in the API's words: stop,
	// end_turn, tool_calls, length and the like.
	StopReason string `json:"stop_reason,omitempty"`
	// Attempts is how many times the call was sent.
	Attempts int `json:"attempts"`
}

// Message returns the result as the assistant message that goes back in
// the conversation.
func (r *Result) Message() Message {
	return Message{Role: RoleAssistant, Content: r.Text, Thinking: r.Thinking, ThinkingSignature: r.ThinkingSignature, ToolCalls: r.ToolCalls}
}

// ModelInfo is a model of a provider's list.
type ModelInfo struct {
	ID string `json:"id"`
	// Context is its context window in tokens, where the list says.
	Context int `json:"context,omitempty"`
}

// Errors of a call.
var (
	// ErrKeyInClear is a call that would send a key over http to a host
	// that is not loopback.
	ErrKeyInClear = errors.New("the key would cross the network in the clear: use an https base_url, or a loopback host")
	// ErrFirstByte, ErrIdle and ErrTimeout are the call's timeouts.
	ErrFirstByte = errors.New("the model API did not begin its answer in time")
	ErrIdle      = errors.New("the model API's answer went silent")
	ErrTimeout   = errors.New("the model API's answer took longer than the call may")
	// ErrTooLarge is an answer past a bound: the whole of it, a line or
	// an event, or the number of tool calls.
	ErrTooLarge = errors.New("the model API's answer is larger than the assistant takes")
	// ErrIncomplete is an answer that ended before it said it was done.
	ErrIncomplete = errors.New("the model API's answer ended before it was complete")
	// ErrClosed is a call on a closed client.
	ErrClosed = errors.New("the model client is closed")
)

// APIError is an answer of the API that says the call failed.
type APIError struct {
	Provider string
	Status   int
	// Message is the API's own, its key replaced.
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}

// retryable reports whether an answer of status may be tried again: a
// timeout, a rate limit or an overloaded server.
func retryable(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// redact replaces key in s.
func redact(s string, key Secret) string {
	if len(key) < 4 {
		return s
	}
	return strings.ReplaceAll(s, string(key), "***")
}
