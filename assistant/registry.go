package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
)

// The kinds of tool, by where a call of one runs.
const (
	// KindPool is a tool run on the console's pool, in fit: every read tool.
	KindPool = ""
	// KindAsk is ask_user: its call is shown in the panel as a question with
	// buttons, and the conversation waits for the choice, which comes back
	// as the call's result with the user's next request.
	KindAsk = "ask"
)

// The tools' groups, as the Functions page shows them.
const (
	GroupConfig  = "config"
	GroupRuntime = "runtime"
	GroupRoles   = "roles"
	GroupLogs    = "logs"
	GroupPage    = "page"
	GroupChange  = "change"
	GroupConvert = "convert"
	GroupAgent   = "agent"
)

// Tool is a function the model may call: its name, what it does, the JSON
// Schema of its arguments, the level a user needs for it, and the bound of
// its result. Tools are registered once, before the assistant serves.
type Tool struct {
	// Name is how the model calls it: lower case letters, digits and '_'.
	Name string
	// Group sorts it on the Functions page.
	Group string
	// Level is the least level a user needs for it: config.AssistantRead,
	// AssistantEdit or AssistantPropose.
	Level string
	// Description tells the model what it does and when to call it.
	Description string
	// Parameters is the JSON Schema of its arguments, an object.
	Parameters json.RawMessage
	// MaxBytes, when not 0, bounds its result below the settings'
	// max_tool_result_bytes.
	MaxBytes int64
	// Kind says where a call runs: KindPool, or KindAsk.
	Kind string
	// Enabled, when not nil, says whether the settings let it run, and why
	// not: log_tail needs data.send_logs. A tool not enabled is not offered.
	Enabled func(s *config.Assistant) (ok bool, why string)
	// Run runs a call of a KindPool tool, on the console's pool, and returns
	// its result for the model: text or JSON, redacted and bounded after.
	// An error is handed back to the model as the call's failure.
	Run func(c *Call) (string, error)
}

// Call is a call of a tool being run.
type Call struct {
	// Context ends when the chat does: the browser left, or it was stopped.
	Context context.Context
	// Args are the call's arguments, a JSON object.
	Args json.RawMessage
	// User is the console user the chat is for, and Level theirs.
	User, Level string
	// Settings are the assistant's settings the chat runs with.
	Settings *config.Assistant
	// Conversation is the ID of the chat's conversation.
	Conversation string
	// Emit sends an event of the chat's stream to the panel, as a proposal
	// would that a tool adds to.
	Emit func(Event)
	// Skill is the skill the chat runs, when one (I127).
	Skill string

	a *Assistant
	// expired is set once the call's result was given up on, past its
	// timeout: a propose tool then adds no step.
	expired *atomic.Bool
	// proposed hears of the proposal a propose tool changed, and the
	// members it changes, for the chat's audit entry.
	proposed func(p *Proposal, changed []string)
}

// Decode decodes the call's arguments into v, a struct, and says what is
// wrong with them.
func (c *Call) Decode(v any) error {
	if len(c.Args) == 0 {
		return nil
	}
	if err := json.Unmarshal(c.Args, v); err != nil {
		return fmt.Errorf("the arguments are not what the tool takes: %v", err)
	}
	return nil
}

var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Registry is the tools the assistant offers.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

// Add registers t, refusing a name taken or not valid, a level not one of
// read, edit and propose, parameters that are not a JSON object, and a
// pool tool without Run.
func (r *Registry) Add(t *Tool) error {
	switch {
	case !toolName.MatchString(t.Name):
		return fmt.Errorf("assistant: %q is not a tool's name", t.Name)
	case !slices.Contains([]string{config.AssistantRead, config.AssistantEdit, config.AssistantPropose}, t.Level):
		return fmt.Errorf("assistant: tool %s: %q is not a level a tool may need", t.Name, t.Level)
	case !isObject(t.Parameters):
		return fmt.Errorf("assistant: tool %s: its parameters are not a JSON Schema object", t.Name)
	case t.Kind == KindPool && t.Run == nil:
		return fmt.Errorf("assistant: tool %s has nothing to run", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools[t.Name] != nil {
		return fmt.Errorf("assistant: tool %s is registered twice", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

func isObject(raw json.RawMessage) bool {
	var m map[string]any
	return json.Unmarshal(raw, &m) == nil && m["type"] == "object"
}

// Tool returns the tool named name, or nil.
func (r *Registry) Tool(name string) *Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Tools returns every tool, by group and name.
func (r *Registry) Tools() []*Tool {
	r.mu.RLock()
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return groupRank(out[i].Group) < groupRank(out[j].Group)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

var groupOrder = []string{GroupConfig, GroupRuntime, GroupRoles, GroupLogs, GroupPage, GroupChange, GroupConvert, GroupAgent}

func groupRank(g string) int {
	if i := slices.Index(groupOrder, g); i >= 0 {
		return i
	}
	return len(groupOrder)
}

// levelRank orders the levels, off lowest.
func levelRank(level string) int { return slices.Index(config.AssistantLevels, level) }

// Allowed reports whether a user of level may call t under settings s, and
// why not.
func (t *Tool) Allowed(level string, s *config.Assistant) (bool, string) {
	if levelRank(level) < levelRank(t.Level) {
		return false, "needs the level " + t.Level
	}
	if t.Enabled != nil {
		return t.Enabled(s)
	}
	return true, ""
}

// For returns the tools a user of level may call under settings s, as
// the model is offered them.
func (r *Registry) For(level string, s *config.Assistant) []*Tool {
	var out []*Tool
	for _, t := range r.Tools() {
		if ok, _ := t.Allowed(level, s); ok {
			out = append(out, t)
		}
	}
	return out
}

// modelTools returns tools as a model call lists them.
func modelTools(tools []*Tool) []model.Tool {
	out := make([]model.Tool, len(tools))
	for i, t := range tools {
		out[i] = model.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters}
	}
	return out
}
