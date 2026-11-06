package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
)

// The types of a chat stream's events. Each is an SSE message whose data is
// an Event; the last is the SSE event "end", an Event of type done, or, when
// the chat failed, the SSE event "error" with {"message"}.
const (
	EventStart      = "start"       // the conversation and model: Conversation, Title, Model
	EventText       = "text"        // a piece of the answer: Text
	EventThinking   = "thinking"    // a piece of the model's thinking: Text
	EventToolCall   = "tool_call"   // a tool called: ID, Name, Arguments
	EventToolResult = "tool_result" // its result, as the model got it: ID, Name, Content, Error, Cut
	EventAsk        = "ask"         // ask_user's question: ID, Question, Options
	EventUsage      = "usage"       // a model call's tokens: Usage, and Today, the user's today
	EventSummary    = "summary"     // the early turns were summarized, up to Folded
	EventProposal   = "proposal"    // a step joined the conversation's proposal: ID, Revision, Name (the tool), Text (its summary)
	EventDone       = "done"        // the end: Reason
)

// The reasons a chat ends with.
const (
	ReasonAnswer       = "answer"             // the model answered
	ReasonAsk          = "ask"                // ask_user waits for the person's choice
	ReasonToolCalls    = "max_tool_calls"     // limits.max_tool_calls was reached
	ReasonTokensPerDay = "max_tokens_per_day" // the user's tokens for the day are spent
	ReasonStopped      = "stopped"            // the person stopped it, or left
	ReasonError        = "error"              // a model call failed
)

// Event is an event of a chat's stream.
type Event struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	Content      string          `json:"content,omitempty"`
	Error        bool            `json:"error,omitempty"`
	Cut          bool            `json:"cut,omitempty"`
	Question     string          `json:"question,omitempty"`
	Options      []string        `json:"options,omitempty"`
	Usage        *model.Usage    `json:"usage,omitempty"`
	Today        int64           `json:"today,omitempty"`
	Conversation string          `json:"conversation,omitempty"`
	Title        string          `json:"title,omitempty"`
	Model        string          `json:"model,omitempty"`
	Folded       int             `json:"folded,omitempty"`
	Revision     int             `json:"revision,omitempty"`
	Reason       string          `json:"reason,omitempty"`
}

// The chat's own bounds.
var (
	// toolTimeout bounds a tool's call: past it the model is told it failed.
	toolTimeout = 30 * time.Second
	// pingInterval is how often a quiet stream gets a comment, so that
	// proxies keep it.
	pingInterval = 15 * time.Second
	// stallTimeout bounds the wait for a browser that does not take what
	// it is sent.
	stallTimeout = time.Minute
)

// errStopped is the cause a stopped chat's model call is cancelled with.
var errStopped = errors.New("the chat was stopped")

// chatBody is POST /api/chat's: one of a message, a retry of the last
// answer, or the choice of an ask_user question; and the page the person is
// on, and the workbench's language.
type chatBody struct {
	Conversation string       `json:"conversation"`
	Message      *string      `json:"message"`
	Retry        bool         `json:"retry"`
	Answer       *askAnswer   `json:"answer"`
	Page         *PageContext `json:"page"`
	Lang         string       `json:"lang"`
}

type askAnswer struct {
	ID     string `json:"id"`
	Choice string `json:"choice"`
}

// askArgs are ask_user's arguments.
type askArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func parseAsk(raw json.RawMessage) (askArgs, error) {
	var q askArgs
	if err := json.Unmarshal(raw, &q); err != nil || strings.TrimSpace(q.Question) == "" || len(q.Options) == 0 || len(q.Options) > maxAskOptions {
		return q, fmt.Errorf("ask_user takes a question and 1 to %d options", maxAskOptions)
	}
	return q, nil
}

// slot takes one of the chats that may run at once, as the settings bound
// them, and reports whether there was one.
func (a *Assistant) slot(s *config.Assistant) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running >= s.Limits.ConcurrentChats() {
		return false
	}
	a.running++
	return true
}

func (a *Assistant) endSlot() {
	a.mu.Lock()
	a.running--
	a.mu.Unlock()
}

// chatHandler answers POST /api/chat with the chat's events as SSE. The
// checks that need no store run here, on the worker; the rest on the pool,
// which then starts the stream and the loop and leaves.
func (a *Assistant) chatHandler(r *console.Request) {
	var body chatBody
	if !r.Decode(&body) {
		return
	}
	s := a.Settings()
	level := s.Level(r.User)
	modes := 0
	for _, on := range []bool{body.Message != nil, body.Retry, body.Answer != nil} {
		if on {
			modes++
		}
	}
	switch {
	case level == config.AssistantOff:
		_ = console.Error(r.Context, 403, "the assistant is off for "+r.User)
		return
	case modes != 1:
		_ = console.Error(r.Context, 400, "send one of message, retry and answer")
		return
	case body.Message != nil && (*body.Message == "" || len(*body.Message) > MaxMessageBytes || !utf8.ValidString(*body.Message)):
		_ = console.Error(r.Context, 400, fmt.Sprintf("the message is 1 to %d bytes of text", MaxMessageBytes))
		return
	case body.Conversation == "" && body.Message == nil:
		_ = console.Error(r.Context, 400, "a retry or an answer names its conversation")
		return
	}
	prov, mod := s.Default()
	if prov == "" || mod == "" {
		_ = console.Error(r.Context, 409, "no model is set: add a provider with a model on the Settings page")
		return
	}
	if !a.slot(s) {
		_ = console.Error(r.Context, 429, "the assistant is answering as many chats as it may at once; try again shortly")
		return
	}
	started := false
	r.GoThen(func() { started = a.begin(r, body, s, level, prov, mod) }, func() {
		if !started {
			a.endSlot()
		}
	})
}

// begin readies the conversation and starts the chat's stream, on the
// pool; it reports whether it did, answering the request otherwise.
func (a *Assistant) begin(r *console.Request, body chatBody, s *config.Assistant, level, prov, mod string) bool {
	p, err := a.Provider(prov)
	if err != nil {
		_ = console.Error(r.Context, 409, err.Error())
		return false
	}
	c := &chat{a: a, r: r, submit: r.Submit, user: r.User, level: level, s: s, p: p, model: mod, window: DefaultContextTokens,
		red: a.redactor(s), lang: body.Lang}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	useTools := true
	if cp := s.Provider(prov); cp != nil {
		for _, m := range cp.Models {
			if m.Name == mod {
				useTools = m.CallsTools()
				if m.Context > 0 {
					c.window = m.Context
				}
			}
		}
	}
	if useTools {
		c.tools = a.registry.For(level, s)
	}
	c.page = c.red.pageJSON(body.Page)
	if a.o.Catalog != nil {
		c.catalog = a.o.Catalog(r.User, level)
	}

	status, msg := a.prepare(c, body)
	if status != 0 {
		c.cancel()
		_ = console.Error(r.Context, status, msg)
		return false
	}
	r.Context.Retain()
	c.open()
	c.step()
	return true
}

// prepare finds or makes the conversation, adds what the person sent, and
// marks it under way; a status not 0 says why it cannot be.
func (a *Assistant) prepare(c *chat, body chatBody) (int, string) {
	a.chats.mu.Lock()
	defer a.chats.mu.Unlock()
	u := a.userLocked(c.user)
	c.u = u
	conv := u.conversation(body.Conversation)
	switch {
	case body.Conversation != "" && conv == nil:
		return 404, "no conversation " + body.Conversation
	case conv != nil && conv.busy != nil:
		return 409, "the conversation is answering already: stop it first"
	case body.Message != nil && conv != nil && turns(conv) >= c.s.Limits.Turns():
		return 409, fmt.Sprintf("the conversation has its %d turns: start a new one", c.s.Limits.Turns())
	case u.usedToday() >= c.s.Limits.TokensPerDay():
		return 429, fmt.Sprintf("%s has spent today's %d tokens", c.user, c.s.Limits.TokensPerDay())
	}
	now := nowMs()
	switch {
	case body.Message != nil:
		if conv == nil {
			conv = &Conversation{ID: newID(), Title: title(*body.Message), Created: now}
			u.convs = append(u.convs, conv)
		}
		closePendingLocked(conv, "(no choice was made: the person wrote the next message instead)")
		conv.Messages = append(conv.Messages, Msg{Message: model.Message{Role: model.RoleUser, Content: *body.Message}, At: now})
	case body.Retry:
		last := -1
		for i, m := range conv.Messages {
			if m.Role == model.RoleUser {
				last = i
			}
		}
		if last < 0 {
			return 409, "the conversation has no message to answer again"
		}
		conv.Messages = slices.Clip(conv.Messages[:last+1])
	default:
		tc := pendingAsk(conv)
		if tc == nil || tc.ID != body.Answer.ID {
			return 409, "the conversation is not waiting for that answer"
		}
		q, _ := parseAsk(tc.Arguments)
		if !slices.Contains(q.Options, body.Answer.Choice) {
			return 400, "the choice is not one of the question's options"
		}
		conv.Messages = append(conv.Messages, Msg{Message: model.Message{Role: model.RoleTool, Content: body.Answer.Choice,
			ToolCallID: tc.ID, ToolName: tc.Name}, At: now})
	}
	conv.Updated = now
	conv.busy = c
	c.conv = conv
	u.boundLocked(c.s.Limits.Conversations())
	return 0, ""
}

// turns counts the person's messages of conv.
func turns(conv *Conversation) int {
	n := 0
	for _, m := range conv.Messages {
		if m.Role == model.RoleUser {
			n++
		}
	}
	return n
}

// pendingAsk returns the ask_user call the conversation waits on, or nil.
func pendingAsk(conv *Conversation) *model.ToolCall {
	if conv == nil {
		return nil
	}
	missing := missingResults(conv.Messages)
	for i := range missing {
		if missing[i].Name == "ask_user" {
			return &missing[i]
		}
	}
	return nil
}

// missingResults returns the calls of the last answer with tools that have
// no result yet.
func missingResults(msgs []Msg) []model.ToolCall {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == model.RoleUser {
			return nil
		}
		if m.Role != model.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		var out []model.ToolCall
		for _, tc := range m.ToolCalls {
			answered := slices.ContainsFunc(msgs[i+1:], func(r Msg) bool { return r.Role == model.RoleTool && r.ToolCallID == tc.ID })
			if !answered {
				out = append(out, tc)
			}
		}
		return out
	}
	return nil
}

// closePendingLocked gives every call of the last answer without a result
// the result why; a.chats.mu is held.
func closePendingLocked(conv *Conversation, why string) {
	for _, tc := range missingResults(conv.Messages) {
		conv.Messages = append(conv.Messages, Msg{Message: model.Message{Role: model.RoleTool, Content: why, ToolCallID: tc.ID,
			ToolName: tc.Name, IsError: tc.Name != "ask_user"}, At: nowMs()})
	}
}

// chat is a chat under way: the loop between the model and the tools for
// one message of the person's, streamed to the browser that sent it.
type chat struct {
	a       *Assistant
	r       *console.Request
	submit  func(func()) bool // runs a tool on the console's pool
	user    string
	level   string
	s       *config.Assistant
	p       *model.Provider
	model   string
	window  int
	tools   []*Tool
	catalog []CatalogEntry
	red     *redactor
	page    []byte
	lang    string
	conv    *Conversation
	u       *userChats
	ctx     context.Context
	cancel  context.CancelFunc

	// summarized: the early turns were summarized once in this chat, or
	// failed to be; calls counts the tools called; limited says the limit
	// of calls was reached. Only the loop's one step at a time uses them.
	summarized atomic.Bool
	calls      atomic.Int32
	limited    atomic.Bool

	skill string // the skill the chat runs (I127)

	mu      sync.Mutex
	ended   bool
	call    *model.Call
	waiting bool
	ping    *time.Timer
	partial strings.Builder // the answer streaming in
	// audit is the tools called, for the chat's audit entry, and proposal
	// the proposal its propose tools left, with the members it changes.
	audit    []AuditCall
	proposal *AuditProposal
}

// open starts the stream: its header and first event, and the ping.
func (c *chat) open() {
	ctx := c.r.Context
	_ = ctx.SetWriteDeadline(time.Time{})
	h := ctx.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("X-Accel-Buffering", "no")
	ctx.WriteHeader(200)
	c.mu.Lock()
	c.writeLocked(sseData(Event{Type: EventStart, Conversation: c.conv.ID, Title: c.conv.Title, Model: c.model}))
	ctx.Flush()
	c.ping = time.AfterFunc(pingInterval, c.pingNow)
	c.mu.Unlock()
	ctx.OnCancel(func(error) { c.stop() })
}

func sseData(ev Event) []byte {
	data, _ := json.Marshal(ev)
	return append(append([]byte("data: "), data...), '\n', '\n')
}

func sseEvent(name string, v any) []byte {
	data, _ := json.Marshal(v)
	return append(append([]byte("event: "+name+"\ndata: "), data...), '\n', '\n')
}

func (c *chat) pingNow() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.writeLocked([]byte(": ping\n\n"))
	c.ping.Reset(pingInterval)
}

// emit sends ev to the browser.
func (c *chat) emit(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ended {
		c.writeLocked(sseData(ev))
	}
}

// writeLocked writes b without waiting; c.mu is held. A browser that does
// not take what it is sent has stallTimeout to, or its stream is cut, which
// stops the chat: what a chat writes is bounded by its limits, so it is not
// held back here.
func (c *chat) writeLocked(b []byte) {
	ctx := c.r.Context
	above, err := ctx.WriteNoWait(b)
	if err != nil {
		go c.stop()
		return
	}
	if above && !c.waiting {
		c.waiting = true
		_ = ctx.SetWriteDeadline(time.Now().Add(stallTimeout))
		if !ctx.OnWritable(c.writable) {
			c.waiting = false
			_ = ctx.SetWriteDeadline(time.Time{})
		}
	}
}

func (c *chat) writable(err error) {
	if err != nil {
		c.stop()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiting = false
	if !c.ended {
		_ = c.r.Context.SetWriteDeadline(time.Time{})
	}
}

func (c *chat) isEnded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ended
}

// setCall keeps the model call under way, cancelling it when the chat has
// ended.
func (c *chat) setCall(k *model.Call) {
	c.mu.Lock()
	ended := c.ended
	c.call = k
	c.mu.Unlock()
	if ended {
		k.Cancel(errStopped)
	}
}

// stop ends the chat as the person stopped it, or left: the model call is
// cancelled, and what the answer had come to is kept.
func (c *chat) stop() {
	c.mu.Lock()
	call := c.call
	c.mu.Unlock()
	c.finish(ReasonStopped, "")
	if call != nil {
		call.Cancel(errStopped)
	}
}

// finish ends the chat once: the conversation kept and let go, the slot
// given back, and only then the last event, so that a browser that sends
// the next message as soon as it reads it finds both free; then the
// request is let go.
func (c *chat) finish(reason, errMsg string) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	if c.ping != nil {
		c.ping.Stop()
	}
	partial := c.partial.String()
	c.partial.Reset()
	calls, prop := c.audit, c.proposal
	c.mu.Unlock()
	c.cancel()
	if len(calls) > 0 {
		outcome := reason
		if errMsg != "" {
			outcome = ReasonError
		}
		c.a.Audit(AuditEntry{User: c.user, Kind: AuditChat, Conversation: c.conv.ID, Skill: c.skill, Calls: calls, Proposal: prop,
			Outcome: outcome, Detail: errMsg})
	}

	a := c.a
	a.chats.mu.Lock()
	conv := c.conv
	now := nowMs()
	if partial != "" && reason != ReasonAnswer {
		conv.Messages = append(conv.Messages, Msg{Message: model.Message{Role: model.RoleAssistant, Content: partial}, At: now, Stopped: true})
	}
	if errMsg != "" {
		conv.Messages = append(conv.Messages, Msg{Message: model.Message{Role: model.RoleAssistant}, At: now, Error: errMsg})
	}
	if reason != ReasonAsk {
		closePendingLocked(conv, "error: not run: the answer was stopped")
	}
	conv.busy = nil
	conv.Updated = now
	trimLocked(conv)
	c.u.boundLocked(c.s.Limits.Conversations())
	a.chats.mu.Unlock()
	a.endSlot()
	a.saveChats(c.user)

	c.mu.Lock()
	if errMsg != "" {
		c.writeLocked(sseEvent("error", map[string]string{"message": errMsg}))
	} else {
		c.writeLocked(sseEvent("end", Event{Type: EventDone, Reason: reason, Conversation: conv.ID}))
	}
	c.mu.Unlock()
	c.r.Context.Release()
}

// addUsage counts a call's tokens to the user's day, and says so.
func (c *chat) addUsage(u model.Usage) {
	c.a.chats.mu.Lock()
	c.u.usedToday()
	c.u.tokens += int64(u.InputTokens + u.OutputTokens)
	today := c.u.tokens
	c.a.chats.mu.Unlock()
	c.emit(Event{Type: EventUsage, Usage: &u, Today: today})
}

// appendMsg adds m to the conversation.
func (c *chat) appendMsg(m Msg) {
	c.a.chats.mu.Lock()
	c.conv.Messages = append(c.conv.Messages, m)
	c.a.chats.mu.Unlock()
}

// request builds the next call, or, when the conversation outgrew its
// window and its early turns may be summarized, says up to where.
func (c *chat) request() (req *model.Request, fold int) {
	c.a.chats.mu.Lock()
	msgs := slices.Clone(c.conv.Messages)
	from, summary := c.conv.Folded, c.conv.Summary
	state := proposalState(c.conv.Proposal, c.conv.ProposalNote)
	c.a.chats.mu.Unlock()
	req = &model.Request{Model: c.model, System: systemPrompt(c.level, c.lang, c.page, c.catalog, summary, c.red.text(state)),
		MaxTokens: c.s.Params.MaxOutput(), Thinking: c.s.Params.ThinkingLevel()}
	if c.s.Params != nil {
		req.Temperature = c.s.Params.Temperature
	}
	if len(c.tools) > 0 {
		req.Tools = modelTools(c.tools)
	}
	toolsJSON, _ := json.Marshal(req.Tools)
	// The answer's share is its max_output_tokens, at most a quarter of the
	// window, so that a small window still holds the conversation.
	b := budget{window: c.window, output: min(req.MaxTokens, c.window/4), fixed: tokens(req.System) + tokens(string(toolsJSON))}
	req.Messages = history(msgs[from:])
	if fitHistory(req.Messages, b.room()) || c.summarized.Load() {
		return req, 0
	}
	if k := foldPoint(msgs, from, b.room()); k > from {
		return nil, k
	}
	return req, 0
}

// step makes the loop's next model call.
func (c *chat) step() {
	if c.isEnded() {
		return
	}
	c.a.chats.mu.Lock()
	spent := c.u.usedToday() >= c.s.Limits.TokensPerDay()
	c.a.chats.mu.Unlock()
	if spent {
		c.finish(ReasonTokensPerDay, "")
		return
	}
	req, fold := c.request()
	if fold > 0 {
		c.summarize(fold)
		return
	}
	c.mu.Lock()
	c.partial.Reset()
	c.mu.Unlock()
	c.setCall(c.a.client.Stream(c.p, req, c.s.Limits.StreamBytes(), c.onEvent, c.onDone))
}

// summarize has the model summarize the turns up to fold, keeps the
// summary with the conversation, and goes on; a summary that fails leaves
// the turns to be cut instead.
func (c *chat) summarize(fold int) {
	c.summarized.Store(true)
	c.a.chats.mu.Lock()
	msgs := slices.Clone(c.conv.Messages[c.conv.Folded:fold])
	prev := c.conv.Summary
	c.a.chats.mu.Unlock()
	text := transcript(prev, msgs)
	if limit := c.window * 3 / 2; len(text) > limit {
		text, _ = cutText(text, limit)
	}
	req := &model.Request{Model: c.model, System: summaryPrompt, MaxTokens: 1024,
		Messages: []model.Message{{Role: model.RoleUser, Content: text}}}
	c.setCall(c.a.client.Stream(c.p, req, c.s.Limits.StreamBytes(), nil, func(res *model.Result, err error) {
		if c.isEnded() {
			return
		}
		if err == nil && strings.TrimSpace(res.Text) != "" {
			c.addUsage(res.Usage)
			c.a.chats.mu.Lock()
			c.conv.Summary, c.conv.Folded = strings.TrimSpace(res.Text), fold
			c.a.chats.mu.Unlock()
			c.emit(Event{Type: EventSummary, Folded: fold})
		}
		c.step()
	}))
}

func (c *chat) onEvent(e model.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	switch e.Type {
	case model.EventText:
		c.partial.WriteString(e.Text)
		c.writeLocked(sseData(Event{Type: EventText, Text: e.Text}))
	case model.EventThinking:
		c.writeLocked(sseData(Event{Type: EventThinking, Text: e.Text}))
	case model.EventToolCall:
		c.writeLocked(sseData(Event{Type: EventToolCall, ID: e.ToolCall.ID, Name: e.ToolCall.Name, Arguments: e.ToolCall.Arguments}))
	}
}

func (c *chat) onDone(res *model.Result, err error) {
	if c.isEnded() {
		return
	}
	if err != nil {
		c.finish(ReasonError, c.red.text(err.Error()))
		return
	}
	c.addUsage(res.Usage)
	c.mu.Lock()
	c.partial.Reset()
	c.mu.Unlock()
	c.appendMsg(Msg{Message: res.Message(), At: nowMs()})
	if len(res.ToolCalls) == 0 {
		c.finish(ReasonAnswer, "")
		return
	}
	c.runCalls(res.ToolCalls, 0, nil)
}

// tool returns the tool name the chat offers, or nil.
func (c *chat) tool(name string) *Tool {
	for _, t := range c.tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// runCalls runs the calls of an answer from i on, one at a time, and then
// goes on: the next step, the question asked, or the end at a limit.
func (c *chat) runCalls(calls []model.ToolCall, i int, ask *model.ToolCall) {
	for ; i < len(calls); i++ {
		if c.isEnded() {
			return
		}
		tc := calls[i]
		t := c.tool(tc.Name)
		switch {
		case t == nil:
			c.result(nil, tc, "error: there is no tool "+tc.Name+" you may call", true)
		case tc.Invalid:
			c.result(t, tc, "error: the arguments are not JSON: "+clip(string(tc.Arguments), 200), true)
		case int(c.calls.Load()) >= c.s.Limits.ToolCalls():
			c.limited.Store(true)
			c.result(t, tc, fmt.Sprintf("error: not run: the answer reached its limit of %d tool calls", c.s.Limits.ToolCalls()), true)
		case !c.allowedNow(t):
			// The user's level, checked on every call against the
			// settings in use now, which a save may have changed.
			s := c.a.Settings()
			_, why := t.Allowed(s.Level(c.user), s)
			c.result(t, tc, "error: not run: "+c.user+"'s level is "+s.Level(c.user)+" now, and the tool "+why, true)
		case t.Kind == KindAsk:
			c.calls.Add(1)
			if _, err := parseAsk(tc.Arguments); err != nil || ask != nil {
				if err == nil {
					err = errors.New("one question at a time")
				}
				c.result(t, tc, "error: "+err.Error(), true)
				continue
			}
			ask = &calls[i]
		default:
			c.calls.Add(1)
			next := i + 1
			c.runPool(t, tc, func() { c.runCalls(calls, next, ask) })
			return
		}
	}
	switch {
	case c.limited.Load():
		if ask != nil {
			c.result(c.tool(ask.Name), *ask, "error: not asked: the answer reached its limit of tool calls", true)
		}
		c.finish(ReasonToolCalls, "")
	case ask != nil:
		q, _ := parseAsk(ask.Arguments)
		opts := make([]string, len(q.Options))
		for i, o := range q.Options {
			opts[i] = c.red.text(o)
		}
		c.emit(Event{Type: EventAsk, ID: ask.ID, Question: c.red.text(q.Question), Options: opts})
		c.finish(ReasonAsk, "")
	default:
		c.step()
	}
}

// allowedNow reports whether the user may call t under the settings in use
// now.
func (c *chat) allowedNow(t *Tool) bool {
	s := c.a.Settings()
	ok, _ := t.Allowed(s.Level(c.user), s)
	return ok
}

// setProposal keeps what a propose tool left of the conversation's
// proposal, for the chat's audit entry.
func (c *chat) setProposal(p *Proposal, changed []string) {
	c.mu.Lock()
	c.proposal = auditOf(p, changed)
	c.mu.Unlock()
}

// runPool runs a call of t on the console's pool, within toolTimeout, and
// then next.
func (c *chat) runPool(t *Tool, tc model.ToolCall, next func()) {
	var done atomic.Bool
	call := &Call{Context: c.ctx, Args: tc.Arguments, User: c.user, Level: c.level, Settings: c.s, Conversation: c.conv.ID,
		Emit: c.emit, Skill: c.skill, a: c.a, expired: &done, proposed: c.setProposal}
	var timer atomic.Pointer[time.Timer]
	deliver := func(out string, err error) {
		if !done.CompareAndSwap(false, true) {
			return
		}
		if tm := timer.Load(); tm != nil {
			tm.Stop()
		}
		if err != nil {
			c.result(t, tc, "error: "+err.Error(), true)
		} else {
			c.result(t, tc, out, false)
		}
		next()
	}
	timer.Store(time.AfterFunc(toolTimeout, func() {
		deliver("", fmt.Errorf("the tool took longer than %s", toolTimeout))
	}))
	ok := c.submit(func() {
		defer func() {
			if p := recover(); p != nil {
				deliver("", fmt.Errorf("the tool failed: %v", p))
			}
		}()
		out, err := t.Run(call)
		deliver(out, err)
	})
	if !ok {
		deliver("", errors.New("the console is busy; try again shortly"))
	}
}

// result keeps a call's result, redacted and bounded, as the model gets it,
// and shows it to the browser.
func (c *chat) result(t *Tool, tc model.ToolCall, out string, isErr bool) {
	if c.isEnded() {
		return
	}
	limit := c.s.Limits.ToolResultBytes()
	if t != nil && t.MaxBytes > 0 && t.MaxBytes < limit {
		limit = t.MaxBytes
	}
	out, cut := cutText(c.red.text(out), int(limit))
	c.mu.Lock()
	c.audit = append(c.audit, auditCall(c.red, tc, isErr))
	c.mu.Unlock()
	c.appendMsg(Msg{Message: model.Message{Role: model.RoleTool, Content: out, ToolCallID: tc.ID, ToolName: tc.Name, IsError: isErr},
		At: nowMs(), Cut: cut})
	c.emit(Event{Type: EventToolResult, ID: tc.ID, Name: tc.Name, Content: out, Error: isErr, Cut: cut})
}
