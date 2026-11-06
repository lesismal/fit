package config

import (
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The kinds of model API the assistant calls.
const (
	AssistantAnthropic        = "anthropic"
	AssistantOpenAI           = "openai"
	AssistantOpenAIResponses  = "openai_responses"
	AssistantGemini           = "gemini"
	AssistantAzureOpenAI      = "azure_openai"
	AssistantOllama           = "ollama"
	AssistantOpenAICompatible = "openai_compatible"
	AssistantLLMProxy         = "llm_proxy"
)

// AssistantKinds lists every provider kind, in the schema's order.
var AssistantKinds = []string{AssistantAnthropic, AssistantOpenAI, AssistantOpenAIResponses, AssistantGemini,
	AssistantAzureOpenAI, AssistantOllama, AssistantOpenAICompatible, AssistantLLMProxy}

// The levels of what a console user may have the assistant do, each
// including the ones before it.
const (
	AssistantOff     = "off"
	AssistantRead    = "read"
	AssistantEdit    = "edit"
	AssistantPropose = "propose"
	AssistantApply   = "apply"
)

// AssistantLevels lists the levels, lowest first.
var AssistantLevels = []string{AssistantOff, AssistantRead, AssistantEdit, AssistantPropose, AssistantApply}

// The assistant's defaults.
const (
	DefaultAssistantConnectTimeout     = 10 * time.Second
	DefaultAssistantFirstByteTimeout   = 2 * time.Minute
	DefaultAssistantIdleTimeout        = time.Minute
	DefaultAssistantTimeout            = 10 * time.Minute
	DefaultAssistantRetries            = 2
	DefaultAssistantMaxOutputTokens    = 4096
	DefaultAssistantMaxTurns           = 40
	DefaultAssistantMaxToolCalls       = 16
	DefaultAssistantMaxToolResultBytes = 32 << 10
	DefaultAssistantMaxConversations   = 50
	DefaultAssistantMaxConcurrentChats = 4
	DefaultAssistantMaxTokensPerDay    = 1_000_000
	DefaultAssistantMaxStreamBytes     = 8 << 20
	DefaultAssistantMaxAuditEntries    = 1000
	// MaxAssistantProviders, MaxAssistantModels and MaxAssistantHeaders
	// bound what the settings list.
	MaxAssistantProviders = 32
	MaxAssistantModels    = 256
	MaxAssistantHeaders   = 16
	MaxAssistantRedact    = 32
)

// Assistant is the workbench's assistant: the model APIs it calls, which
// model it uses, and its bounds. Its keys are not here: they are kept in
// the store, or read from the environment.
type Assistant struct {
	Providers    []AssistantProvider `json:"providers,omitempty" doc:"The model APIs the assistant may call, each by name. Their keys are not written here: the Settings page keeps them in admin.store, or api_key_env names a variable that holds one."`
	DefaultModel *AssistantModelRef  `json:"default_model,omitempty" doc:"The provider and model the chat uses; the first model of the first provider when left out."`
	Params       *AssistantParams    `json:"params,omitempty" doc:"What a model call asks for: its output, temperature and thinking."`
	Limits       *AssistantLimits    `json:"limits,omitempty" doc:"Bounds on the assistant's conversations, tool calls and model answers; the defaults when left out."`
	Data         *AssistantData      `json:"data,omitempty" doc:"What of fit's logs may be sent to a model, and more of what is redacted before anything is."`
	Users        []AssistantUser     `json:"users,omitempty" doc:"What each console user may have the assistant do, by user name; a user not listed has default_level."`
	DefaultLevel string              `json:"default_level,omitempty" enum:"off,read,edit,propose,apply" default:"\"read\"" doc:"The level of a console user that users does not list: off, read (the read tools only), edit (also drafts on the open page, never submitted), propose (also proposals) or apply (also applies them). read when left out."`

	At Value `json:"-" fit:"self"`
}

// AssistantProvider is a model API the assistant calls.
type AssistantProvider struct {
	Name             string            `json:"name" fit:"required" doc:"The provider's name, which default_model and the Settings page name it by: letters, digits, '_', '.' and '-'."`
	Kind             string            `json:"kind" fit:"required" enum:"anthropic,openai,openai_responses,gemini,azure_openai,ollama,openai_compatible,llm_proxy" doc:"The API it speaks: anthropic (Messages), openai (Chat Completions), openai_responses (Responses), gemini (generateContent), azure_openai (Chat Completions of a deployment), ollama (/api/chat), openai_compatible (Chat Completions at any base_url, as vLLM, llama.cpp, DeepSeek or a gateway), or llm_proxy, this fit's own llm.proxy, reached on its api address, so that the assistant uses the mesh's models."`
	BaseURL          string            `json:"base_url,omitempty" doc:"The API's base URL, as https://api.openai.com/v1; the vendor's when left out, and needed for openai_compatible and azure_openai. Not for llm_proxy. A key is sent only over https, or to a loopback host."`
	APIVersion       string            `json:"api_version,omitempty" doc:"The api-version of an azure_openai provider's calls, as 2024-10-21; needed for it, and for no other kind."`
	APIKeyEnv        string            `json:"api_key_env,omitempty" doc:"An environment variable holding the API key, read when a call is made; a key the Settings page keeps in the store comes first."`
	Headers          map[string]string `json:"headers,omitempty" doc:"Headers sent with every call, as an organization or a gateway's own; not the key's headers, Host or the body's framing."`
	Models           []AssistantModel  `json:"models,omitempty" doc:"The models offered from this provider; the Settings page reads the provider's own list to add them."`
	ConnectTimeout   Duration          `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long a connect, TLS included, may take. 10s when left out."`
	FirstByteTimeout Duration          `json:"first_byte_timeout,omitempty" default:"\"2m\"" doc:"How long a call may wait for its answer to begin. 2m when left out."`
	IdleTimeout      Duration          `json:"idle_timeout,omitempty" default:"\"1m\"" doc:"How long a streamed answer may go silent. 1m when left out."`
	Timeout          Duration          `json:"timeout,omitempty" default:"\"10m\"" doc:"The most one call may take, to the end of its answer. 10m when left out."`
	Retries          *int              `json:"retries,omitempty" min:"0" max:"5" default:"2" doc:"How often a call is tried again when it failed before any of its answer was taken: the connect failed, or the API answered 408, 429, 500, 502, 503, 504 or 529. 2 when left out; 0 tries once."`

	At Value `json:"-" fit:"self"`
}

// AssistantModel is a model a provider offers.
type AssistantModel struct {
	Name    string `json:"name" fit:"required" doc:"The model's ID, as the provider names it."`
	Context int    `json:"context,omitempty" min:"0" doc:"The model's context window, in tokens; 0 or left out when not known."`
	Tools   *bool  `json:"tools,omitempty" default:"true" doc:"Whether the model calls tools. On when left out."`

	At Value `json:"-" fit:"self"`
}

// AssistantModelRef names a provider's model.
type AssistantModelRef struct {
	Provider string `json:"provider" fit:"required" doc:"A provider's name."`
	Model    string `json:"model" fit:"required" doc:"The model's ID."`

	At Value `json:"-" fit:"self"`
}

// AssistantParams are what a model call asks for.
type AssistantParams struct {
	MaxOutputTokens int      `json:"max_output_tokens,omitempty" min:"0" default:"4096" doc:"The most tokens a model's answer may have. 4096 when left out or 0."`
	Temperature     *float64 `json:"temperature,omitempty" min:"0" max:"2" doc:"The sampling temperature; the model's own when left out."`
	Thinking        string   `json:"thinking,omitempty" enum:"off,low,medium,high" default:"\"off\"" doc:"How much a model that thinks before it answers may: off, low, medium or high, as its API takes it (an effort, or a budget of tokens). off when left out."`

	At Value `json:"-" fit:"self"`
}

// AssistantLimits bound the assistant.
type AssistantLimits struct {
	MaxTurns           int   `json:"max_turns,omitempty" min:"0" default:"40" doc:"The most turns a conversation keeps, a user's message and the answer to it each. 40 when left out or 0."`
	MaxToolCalls       int   `json:"max_tool_calls,omitempty" min:"0" default:"16" doc:"The most tools an answer may call before the assistant stops it. 16 when left out or 0."`
	MaxToolResultBytes int64 `json:"max_tool_result_bytes,omitempty" min:"0" default:"32768" doc:"The most bytes of a tool's result handed to the model; a longer one is cut, and says so. 32 KiB when left out or 0."`
	MaxConversations   int   `json:"max_conversations,omitempty" min:"0" default:"50" doc:"The most conversations kept for each user; one more removes the oldest. 50 when left out or 0."`
	MaxConcurrentChats int   `json:"max_concurrent_chats,omitempty" min:"0" default:"4" doc:"The most answers being made at once, across users; one more is refused until one ends. 4 when left out or 0."`
	MaxTokensPerDay    int64 `json:"max_tokens_per_day,omitempty" min:"0" default:"1000000" doc:"The most tokens, in and out, one user's calls may take in a day, as the APIs count them. 1000000 when left out or 0."`
	MaxStreamBytes     int64 `json:"max_stream_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes of one model answer as it streams in; a larger one is cut and fails. 8 MiB when left out or 0."`
	MaxAuditEntries    int   `json:"max_audit_entries,omitempty" min:"0" default:"1000" doc:"The most entries the assistant's audit log keeps; one more removes the oldest. 1000 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// AssistantData says what may be sent to a model.
type AssistantData struct {
	SendLogs      bool     `json:"send_logs,omitempty" doc:"Let the tools hand lines of fit's own log to the model, redacted. Off by default."`
	SendAccessLog bool     `json:"send_access_log,omitempty" doc:"Let the tools hand lines of the access log to the model, redacted. Off by default."`
	Redact        []string `json:"redact,omitempty" doc:"Regular expressions (Go's syntax) whose matches are replaced in everything the tools hand a model, beside the keys, hashes, tokens and private keys that always are."`

	At Value `json:"-" fit:"self"`
}

// AssistantUser is a console user's level.
type AssistantUser struct {
	Username string `json:"username" fit:"required" doc:"A console user's name."`
	Level    string `json:"level" fit:"required" enum:"off,read,edit,propose,apply" doc:"What the user may have the assistant do: off, read, edit, propose or apply, each including the ones before it."`

	At Value `json:"-" fit:"self"`
}

// The base URLs of the kinds that have a vendor's own.
var assistantBaseURLs = map[string]string{
	AssistantAnthropic:       "https://api.anthropic.com/v1",
	AssistantOpenAI:          "https://api.openai.com/v1",
	AssistantOpenAIResponses: "https://api.openai.com/v1",
	AssistantGemini:          "https://generativelanguage.googleapis.com/v1beta",
	AssistantOllama:          "http://127.0.0.1:11434",
}

// URL returns the provider's base URL without a trailing slash, the
// vendor's when left out; "" for llm_proxy, whose address is llm.proxy's.
func (p *AssistantProvider) URL() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	return assistantBaseURLs[p.Kind]
}

// Connect, FirstByte, Idle, Total and RetryCount return the provider's
// bounds, their defaults when left out.
func (p *AssistantProvider) Connect() time.Duration {
	return durOr(p.ConnectTimeout, DefaultAssistantConnectTimeout)
}
func (p *AssistantProvider) FirstByte() time.Duration {
	return durOr(p.FirstByteTimeout, DefaultAssistantFirstByteTimeout)
}
func (p *AssistantProvider) Idle() time.Duration {
	return durOr(p.IdleTimeout, DefaultAssistantIdleTimeout)
}
func (p *AssistantProvider) Total() time.Duration { return durOr(p.Timeout, DefaultAssistantTimeout) }
func (p *AssistantProvider) RetryCount() int {
	if p.Retries == nil {
		return DefaultAssistantRetries
	}
	return *p.Retries
}

func durOr(d Duration, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d.Std()
}

// CallsTools reports whether the model calls tools: so unless it says not.
func (m *AssistantModel) CallsTools() bool { return m.Tools == nil || *m.Tools }

// Provider returns the provider named name, or nil.
func (a *Assistant) Provider(name string) *AssistantProvider {
	for i := range a.Providers {
		if a.Providers[i].Name == name {
			return &a.Providers[i]
		}
	}
	return nil
}

// Default returns the provider and model the chat uses: default_model, or
// the first model of the first provider that lists one; "" when there is
// none.
func (a *Assistant) Default() (provider, model string) {
	if a.DefaultModel != nil {
		return a.DefaultModel.Provider, a.DefaultModel.Model
	}
	for _, p := range a.Providers {
		if len(p.Models) > 0 {
			return p.Name, p.Models[0].Name
		}
	}
	return "", ""
}

// Level returns what the console user may have the assistant do.
func (a *Assistant) Level(user string) string {
	for _, u := range a.Users {
		if u.Username == user {
			return u.Level
		}
	}
	if a.DefaultLevel == "" {
		return AssistantRead
	}
	return a.DefaultLevel
}

// MaxOutput, and the limits' accessors below, return a setting, its
// default when left out; they take a nil receiver.
func (p *AssistantParams) MaxOutput() int {
	if p == nil || p.MaxOutputTokens <= 0 {
		return DefaultAssistantMaxOutputTokens
	}
	return p.MaxOutputTokens
}

// ThinkingLevel returns the thinking asked for, "off" when left out.
func (p *AssistantParams) ThinkingLevel() string {
	if p == nil || p.Thinking == "" {
		return "off"
	}
	return p.Thinking
}

func (l *AssistantLimits) Turns() int {
	if l == nil {
		return DefaultAssistantMaxTurns
	}
	return int(orDefault(int64(l.MaxTurns), DefaultAssistantMaxTurns))
}
func (l *AssistantLimits) ToolCalls() int {
	if l == nil {
		return DefaultAssistantMaxToolCalls
	}
	return int(orDefault(int64(l.MaxToolCalls), DefaultAssistantMaxToolCalls))
}
func (l *AssistantLimits) ToolResultBytes() int64 {
	if l == nil {
		return DefaultAssistantMaxToolResultBytes
	}
	return orDefault(l.MaxToolResultBytes, DefaultAssistantMaxToolResultBytes)
}
func (l *AssistantLimits) Conversations() int {
	if l == nil {
		return DefaultAssistantMaxConversations
	}
	return int(orDefault(int64(l.MaxConversations), DefaultAssistantMaxConversations))
}
func (l *AssistantLimits) ConcurrentChats() int {
	if l == nil {
		return DefaultAssistantMaxConcurrentChats
	}
	return int(orDefault(int64(l.MaxConcurrentChats), DefaultAssistantMaxConcurrentChats))
}
func (l *AssistantLimits) TokensPerDay() int64 {
	if l == nil {
		return DefaultAssistantMaxTokensPerDay
	}
	return orDefault(l.MaxTokensPerDay, DefaultAssistantMaxTokensPerDay)
}
func (l *AssistantLimits) StreamBytes() int64 {
	if l == nil {
		return DefaultAssistantMaxStreamBytes
	}
	return orDefault(l.MaxStreamBytes, DefaultAssistantMaxStreamBytes)
}
func (l *AssistantLimits) AuditEntries() int {
	if l == nil {
		return DefaultAssistantMaxAuditEntries
	}
	return int(orDefault(int64(l.MaxAuditEntries), DefaultAssistantMaxAuditEntries))
}

// LoadAssistant parses and checks settings of the assistant kept apart from
// the config, as the store keeps those the Settings page saved, with the
// paths of its errors under admin.assistant. console is the config's
// admin.console, whose users the settings' users must be.
func LoadAssistant(data []byte, console *Console) (*Assistant, error) {
	root, err := parse(data)
	if err != nil {
		return nil, Errors{err.(*Error)}
	}
	var a Assistant
	var d decoder
	d.decode(root, "admin.assistant", reflect.ValueOf(&a).Elem(), nil)
	if len(d.errs) > 0 {
		return nil, d.errs
	}
	var errs Errors
	checkAssistant(&a, console, func(v Value, format string, args ...any) { errs = append(errs, v.Errorf(format, args...)) })
	if len(errs) > 0 {
		return nil, errs
	}
	return &a, nil
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// assistantHeadersRefused are the headers a provider's headers may not set:
// the keys' and the request's framing.
var assistantHeadersRefused = []string{"authorization", "x-api-key", "api-key", "x-goog-api-key", "host",
	"content-length", "content-type", "transfer-encoding", "connection", "anthropic-version"}

// checkAssistant checks a, admin.assistant, beside console, admin.console.
func checkAssistant(a *Assistant, console *Console, add func(Value, string, ...any)) {
	if a == nil {
		return
	}
	if console == nil {
		add(a.At, "the assistant lives in the console's workbench: set admin.console too")
	}
	if len(a.Providers) > MaxAssistantProviders {
		add(a.At.Field("providers"), "%d providers, more than %d", len(a.Providers), MaxAssistantProviders)
	}
	for i := range a.Providers {
		checkAssistantProvider(a, i, add)
	}
	if r := a.DefaultModel; r != nil && a.Provider(r.Provider) == nil {
		add(r.At.Field("provider"), "no provider is named %q", r.Provider)
	}
	if d := a.Data; d != nil {
		if len(d.Redact) > MaxAssistantRedact {
			add(d.At.Field("redact"), "%d patterns, more than %d", len(d.Redact), MaxAssistantRedact)
		}
		for i, re := range d.Redact {
			if _, err := regexp.Compile(re); err != nil || re == "" || len(re) > 256 {
				add(d.At.Field("redact").Index(i), "%q is not a regular expression of 1 to 256 bytes", re)
			}
		}
	}
	for i, u := range a.Users {
		at := a.At.Field("users").Index(i)
		switch {
		case slices.IndexFunc(a.Users, func(o AssistantUser) bool { return o.Username == u.Username }) < i:
			add(at.Field("username"), "%q is listed twice", u.Username)
		case console != nil && !slices.ContainsFunc(console.Users, func(c ConsoleUser) bool { return c.Username == u.Username }):
			add(at.Field("username"), "%q is not a console user", u.Username)
		}
	}
}

func checkAssistantProvider(a *Assistant, i int, add func(Value, string, ...any)) {
	p := &a.Providers[i]
	switch {
	case !namePattern.MatchString(p.Name):
		add(p.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", p.Name)
	case slices.IndexFunc(a.Providers, func(o AssistantProvider) bool { return o.Name == p.Name }) < i:
		add(p.At.Field("name"), "another provider is already named %q", p.Name)
	}
	switch {
	case p.Kind == AssistantLLMProxy && p.BaseURL != "":
		add(p.At.Field("base_url"), "an llm_proxy provider is reached on llm.proxy's api address: leave base_url out")
	case p.BaseURL == "" && (p.Kind == AssistantOpenAICompatible || p.Kind == AssistantAzureOpenAI):
		add(p.At.Field("base_url"), "a %s provider needs base_url", p.Kind)
	case p.BaseURL != "":
		if u, err := url.Parse(p.BaseURL); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			add(p.At.Field("base_url"), "%q is not an http or https URL without a query", p.BaseURL)
		}
	}
	if (p.APIVersion != "") != (p.Kind == AssistantAzureOpenAI) {
		add(p.At.Field("api_version"), "api_version is for azure_openai, which needs it")
	}
	if p.APIKeyEnv != "" && !envNamePattern.MatchString(p.APIKeyEnv) {
		add(p.At.Field("api_key_env"), "%q is not an environment variable's name", p.APIKeyEnv)
	}
	if len(p.Headers) > MaxAssistantHeaders {
		add(p.At.Field("headers"), "%d headers, more than %d", len(p.Headers), MaxAssistantHeaders)
	}
	for k, v := range p.Headers {
		switch {
		case !isToken(k):
			add(p.At.Field("headers").Field(k), "%q is not a header's name", k)
		case slices.Contains(assistantHeadersRefused, strings.ToLower(k)):
			add(p.At.Field("headers").Field(k), "%s is the call's own: the key is kept in the store or named by api_key_env", k)
		case strings.ContainsFunc(v, func(r rune) bool { return r < ' ' && r != '\t' || r == 0x7f }):
			add(p.At.Field("headers").Field(k), "the value holds a control character")
		}
	}
	if len(p.Models) > MaxAssistantModels {
		add(p.At.Field("models"), "%d models, more than %d", len(p.Models), MaxAssistantModels)
	}
	for j, m := range p.Models {
		at := p.At.Field("models").Index(j)
		switch {
		case m.Name == "" || len(m.Name) > 256 || strings.ContainsFunc(m.Name, func(r rune) bool { return r < ' ' || r == 0x7f }):
			add(at.Field("name"), "a model's ID is 1 to 256 printable bytes")
		case slices.IndexFunc(p.Models, func(o AssistantModel) bool { return o.Name == m.Name }) < j:
			add(at.Field("name"), "%q is listed twice", m.Name)
		}
	}
}
