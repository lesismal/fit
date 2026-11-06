package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
)

// KindPage is a page tool (I154): its call runs in the open page, not in
// fit. The chat sends it to the panel over its stream as the event
// page_call, the panel hands it to the page, and the page's answer comes
// back with POST /api/chat/page as the call's result, redacted, bounded and
// audited as any other; the chat waits for it within pageTimeout, and no
// longer than the browser stays. Run, when not nil, checks the call in fit
// first, on the console's pool, and returns the arguments the page gets. A
// page tool is offered only while the page context declares an editor, and
// never over MCP, which has no open page.
const KindPage = "page"

// The page tools' names.
const (
	ToolPageGet    = "page_get"
	ToolPageEdit   = "page_edit"
	ToolPageRevert = "page_revert"
)

// The page tools' bounds.
const (
	// MaxDraftBytes bounds a page_edit call's arguments: a draft over it
	// is refused, and the model told so.
	MaxDraftBytes = 256 << 10
	// maxEditableMembers bounds the members a page declares it edits.
	maxEditableMembers = 64
)

// pageTimeout bounds the wait for the page's answer to a page tool's call.
var pageTimeout = 30 * time.Second

// EventPageCall is a page tool's call the panel hands the open page: ID,
// Name, and Arguments as fit checked them. The page answers it with
// POST /api/chat/page.
const EventPageCall = "page_call"

// Editable is what a page declares it edits, as PageContext.Editable
// carries it: the top-level members of the config its form and text tabs
// hold, the text formats it has, the tab shown, and whether it holds
// unsaved changes and the assistant's draft.
type Editable struct {
	Members        []string `json:"members"`
	Formats        []string `json:"formats,omitempty"`
	Tab            string   `json:"tab,omitempty"`
	Unsaved        bool     `json:"unsaved,omitempty"`
	AssistantDraft bool     `json:"assistant_draft,omitempty"`
}

// editableOf returns what page p edits, or nil when it edits nothing.
func editableOf(p *PageContext) *Editable {
	if p == nil || len(p.Editable) == 0 {
		return nil
	}
	var e Editable
	if json.Unmarshal(p.Editable, &e) != nil {
		return nil
	}
	var ms []string
	for _, m := range e.Members {
		if m != "" && m != "admin" && !slices.Contains(ms, m) && len(ms) < maxEditableMembers {
			ms = append(ms, m)
		}
	}
	if len(ms) == 0 {
		return nil
	}
	e.Members = ms
	return &e
}

// withPage returns tools without the page tools when page p edits nothing.
func withPage(tools []*Tool, p *PageContext) []*Tool {
	if editableOf(p) != nil {
		return tools
	}
	return slices.DeleteFunc(slices.Clone(tools), func(t *Tool) bool { return t.Kind == KindPage })
}

// pageTools returns the page tools of the plan's registry.
func pageTools() []*Tool {
	note := " It changes only what the open page holds, as an unsaved draft marked as yours: it never submits or saves, and the config, its version and its ETag stay as they are until the person clicks Save and apply on the page."
	return []*Tool{
		{Name: ToolPageGet, Group: GroupPage, Level: config.AssistantRead, Kind: KindPage,
			Description: "Read what the open page holds now: the members of the config it edits as its draft has them, unsaved edits and your drafts included, the tab shown, a text that does not parse, and the last save's problems. Secrets are redacted.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: ToolPageEdit, Group: GroupPage, Level: config.AssistantEdit, Kind: KindPage,
			Description: "Draft a change into the open page, for the members it edits (page_context's editable.members) only: top-level members set whole (members; null removes one), operations at JSON pointers into its form (ops: add, replace or remove; /routes/- appends), or the page's part of the config as text in one of its tabs (text, with format conf, yaml or json). Copy [redacted] values back where they were; a secret, admin or a member the page does not edit cannot be drafted: propose that instead when you may." + note,
			Parameters: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","description":"what the draft does, in a few words, shown on the page"},` +
				`"members":{"type":"object","description":"top-level members the page edits, set to these values; null removes one"},` +
				`"ops":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["add","replace","remove"]},"path":{"type":"string","description":"a JSON pointer, as /routes/0/handle or /upstreams/-"},"value":{"description":"the value of an add or a replace"}},"required":["op","path"]}},` +
				`"text":{"type":"string","description":"the page's members as config text, written into its tab of format"},"format":{"type":"string","enum":["conf","yaml","json"]}},"required":["summary"]}`),
			Run: checkPageEdit},
		{Name: ToolPageRevert, Group: GroupPage, Level: config.AssistantEdit, Kind: KindPage,
			Description: "Remove your drafts from the open page, as the person asks: the page returns to what it held before your first draft. Refused when the person has edited the page since; they then revert or discard on the page." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`)},
	}
}

// pageEditArgs are page_edit's arguments.
type pageEditArgs struct {
	Summary string          `json:"summary"`
	Members json.RawMessage `json:"members"`
	Ops     []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"ops"`
	Text   string `json:"text"`
	Format string `json:"format"`
}

// checkPageEdit checks a page_edit call before the page gets it: what it
// changes is on the page and not in admin, it writes no secret, and its
// text parses. The page checks it again against its draft, puts back the
// redacted values it copies, and runs its own check of the text.
func checkPageEdit(c *Call) (string, error) {
	var args pageEditArgs
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	ed := editableOf(c.Page)
	if ed == nil {
		return "", errors.New("no open page edits the config: " + elsewhere(c.Level))
	}
	if strings.TrimSpace(args.Summary) == "" {
		return "", errors.New("say in summary what the draft does")
	}
	hasText, hasEdits := args.Text != "", len(args.Members) > 0 || len(args.Ops) > 0
	switch {
	case hasText == hasEdits:
		return "", errors.New("give either members and ops, or text with its format")
	case hasText:
		return string(c.Args), checkDraftText(ed, c.Level, args.Text, args.Format)
	}
	if len(args.Members) > 0 {
		tree, err := decodeTree(args.Members)
		members, ok := tree.(*jobject)
		if err != nil || !ok {
			return "", errors.New("members is not a JSON object")
		}
		for _, k := range members.keys {
			if err := onPage(ed, c.Level, k); err != nil {
				return "", err
			}
			if err := guardDraft([]string{k}, members.vals[k], secretName(k)); err != nil {
				return "", err
			}
		}
	}
	for n, op := range args.Ops {
		tokens, err := parsePointer(op.Path)
		switch {
		case err != nil:
			return "", fmt.Errorf("ops[%d]: %v", n, err)
		case len(tokens) == 0:
			return "", fmt.Errorf("ops[%d]: the path names the whole config; set top-level members with members", n)
		case op.Op != opAdd && op.Op != opReplace && op.Op != opRemove:
			return "", fmt.Errorf("ops[%d]: op is add, replace or remove, not %q", n, op.Op)
		}
		if err := onPage(ed, c.Level, tokens[0]); err != nil {
			return "", fmt.Errorf("ops[%d]: %v", n, err)
		}
		if slices.ContainsFunc(tokens, secretName) || secretPath(tokens) {
			return "", fmt.Errorf("ops[%d]: %s is a secret field, which the assistant never writes: the person sets it on the page", n, op.Path)
		}
		if op.Op == opRemove {
			continue
		}
		tree, err := decodeTree(op.Value)
		if len(op.Value) == 0 || err != nil {
			return "", fmt.Errorf("ops[%d]: %s needs a value", n, op.Op)
		}
		if err := guardDraft(tokens, tree, false); err != nil {
			return "", fmt.Errorf("ops[%d]: %v", n, err)
		}
	}
	return string(c.Args), nil
}

// checkDraftText checks a draft written as text: it parses in its format,
// sets only members the page edits, and writes no secret.
func checkDraftText(ed *Editable, level, text, format string) error {
	name := ""
	switch format {
	case "conf":
		name = "fit"
	case "yaml":
		name = "yaml"
	case "json":
	default:
		return fmt.Errorf("format is conf, yaml or json, not %q", format)
	}
	if len(ed.Formats) > 0 && !slices.Contains(ed.Formats, format) {
		return fmt.Errorf("the page has no %s tab: it has %s", format, strings.Join(ed.Formats, ", "))
	}
	res, err := adapter.Adapt(name, []byte(text))
	if err != nil {
		// The converters stop at the first error.
		var b strings.Builder
		fmt.Fprintf(&b, "the text does not parse as %s:", format)
		for _, e := range config.AsErrors(err) {
			b.WriteString("\n- ")
			if e.Line > 0 {
				fmt.Fprintf(&b, "line %d, column %d: ", e.Line, e.Col)
			}
			b.WriteString(e.Msg)
		}
		return errors.New(b.String())
	}
	tree, err := decodeTree(res.JSON)
	obj, ok := tree.(*jobject)
	if err != nil || !ok {
		return fmt.Errorf("the text does not parse as %s into the config's members", format)
	}
	for _, k := range obj.keys {
		if err := onPage(ed, level, k); err != nil {
			return err
		}
		if err := guardDraft([]string{k}, obj.vals[k], secretName(k)); err != nil {
			return err
		}
	}
	return nil
}

// onPage refuses a member the page does not edit, and admin.
func onPage(ed *Editable, level, member string) error {
	if err := forbidden([]string{member}); err != nil {
		return err
	}
	if !slices.Contains(ed.Members, member) {
		return fmt.Errorf("the open page edits %s, not %s: %s", strings.Join(ed.Members, ", "), member, elsewhere(level))
	}
	return nil
}

// elsewhere says what becomes of a change no open page can hold, at level.
func elsewhere(level string) string {
	if levelRank(level) >= levelRank(config.AssistantPropose) {
		return "a change no open page can hold is proposed instead, with config_propose"
	}
	return "describe the change, and the page that edits it, for the person to make there"
}

// guardDraft refuses a value at tokens that writes a secret: a string
// under a secret-named member must be a redacted value page_get showed,
// which the page puts back as it holds it.
func guardDraft(tokens []string, v any, secret bool) error {
	switch t := v.(type) {
	case *jobject:
		for _, k := range t.keys {
			sub := append(tokens[:len(tokens):len(tokens)], k)
			if err := guardDraft(sub, t.vals[k], secret || secretPath(sub)); err != nil {
				return err
			}
		}
	case []any:
		for i, x := range t {
			if err := guardDraft(append(tokens[:len(tokens):len(tokens)], strconv.Itoa(i)), x, secret); err != nil {
				return err
			}
		}
	case string:
		if secret && !isRedacted(t) {
			return fmt.Errorf("%s holds a secret, which the assistant never writes: copy it as page_get shows it ([redacted]) or leave it out, and the person sets it on the page", pointerOf(tokens))
		}
	case nil:
	default:
		if secret {
			return fmt.Errorf("%s holds a secret, which the assistant never writes: the person sets it on the page", pointerOf(tokens))
		}
	}
	return nil
}

// pageWait is a page tool's call the chat waits on the page for.
type pageWait struct {
	id      string
	deliver func(out string, err error)
}

// runPage runs a call of t, a page tool: checked in fit by its Run, on the
// console's pool, then sent to the page, whose answer is its result, all
// within pageTimeout; and then next.
func (c *chat) runPage(t *Tool, tc model.ToolCall, next func()) {
	if n := len(tc.Arguments); t.Name == ToolPageEdit && n > MaxDraftBytes {
		c.result(t, tc, fmt.Sprintf("error: not drafted: the draft is %d bytes, over the page's bound of %d; draft a smaller change, or a part of it at a time", n, MaxDraftBytes), true)
		next()
		return
	}
	var done atomic.Bool
	var timer *time.Timer
	deliver := func(out string, err error) {
		if !done.CompareAndSwap(false, true) {
			return
		}
		c.mu.Lock()
		if c.wait != nil && c.wait.id == tc.ID {
			c.wait = nil
		}
		if timer != nil {
			timer.Stop()
		}
		c.mu.Unlock()
		if err != nil {
			c.result(t, tc, "error: "+err.Error(), true)
		} else {
			c.result(t, tc, out, false)
		}
		next()
	}
	c.mu.Lock()
	timer = time.AfterFunc(pageTimeout, func() {
		deliver("", fmt.Errorf("the page did not answer within %s: it may have been left or closed; read it with page_get before you draft again", pageTimeout))
	})
	c.mu.Unlock()
	send := func(args json.RawMessage) {
		c.mu.Lock()
		if c.ended || done.Load() {
			c.mu.Unlock()
			return
		}
		c.wait = &pageWait{id: tc.ID, deliver: deliver}
		c.writeLocked(sseData(Event{Type: EventPageCall, ID: tc.ID, Name: tc.Name, Arguments: args}))
		c.mu.Unlock()
	}
	if t.Run == nil {
		send(tc.Arguments)
		return
	}
	call := &Call{Context: c.ctx, Args: tc.Arguments, User: c.user, Level: c.level, Settings: c.s, Conversation: c.conv.ID,
		Emit: c.emit, Skill: c.skillName(), Page: c.pageCtx, a: c.a}
	ok := c.submit(func() {
		defer func() {
			if p := recover(); p != nil {
				deliver("", fmt.Errorf("the tool failed: %v", p))
			}
		}()
		args, err := t.Run(call)
		if err != nil {
			deliver("", errors.New("not drafted: "+err.Error()))
			return
		}
		send(json.RawMessage(args))
	})
	if !ok {
		deliver("", errors.New("the console is busy; try again shortly"))
	}
}

// endWait gives up the page call the chat waits on, as it ends.
func (c *chat) endWait() {
	c.mu.Lock()
	w := c.wait
	c.wait = nil
	c.mu.Unlock()
	if w != nil {
		w.deliver("", errors.New("not answered: the chat ended"))
	}
}

// takeWait returns the page call id the chat waits on, no longer waited
// on, or nil.
func (c *chat) takeWait(id string) *pageWait {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.wait
	if w == nil || w.id != id || c.ended {
		return nil
	}
	c.wait = nil
	return w
}

// pageBody is POST /api/chat/page's: the page's answer to a page tool's
// call of a conversation, its result (JSON) or its error.
type pageBody struct {
	Conversation string          `json:"conversation"`
	ID           string          `json:"id"`
	Result       json.RawMessage `json:"result"`
	Error        string          `json:"error"`
}

// pageHandler answers POST /api/chat/page: the open page's answer to a
// page tool's call goes to the chat waiting on it, as the call's result,
// redacted: by structure when it is JSON, and as text. Only the user whose
// chat it is answers it.
func (a *Assistant) pageHandler(r *console.Request) {
	var body pageBody
	if !r.Decode(&body) {
		return
	}
	if body.ID == "" || (len(body.Result) == 0) == (body.Error == "") {
		_ = console.Error(r.Context, 400, "send the call's id, and its result or its error")
		return
	}
	a.chats.mu.Lock()
	var c *chat
	if u := a.chats.users[r.User]; u != nil {
		if conv := u.conversation(body.Conversation); conv != nil {
			c = conv.busy
		}
	}
	a.chats.mu.Unlock()
	var w *pageWait
	if c != nil {
		w = c.takeWait(body.ID)
	}
	if w == nil {
		_ = console.Error(r.Context, 409, "the conversation is not waiting for that call")
		return
	}
	_ = r.JSON(200, map[string]bool{"ok": true})
	run := func() {
		if body.Error != "" {
			w.deliver("", errors.New(c.red.text(body.Error)))
			return
		}
		out, err := c.red.redactJSON(body.Result, hiddenMembers)
		if err != nil {
			out = []byte(c.red.text(string(body.Result)))
		}
		w.deliver(string(out), nil)
	}
	if !r.Submit(run) {
		w.deliver("", errors.New("the console is busy; the page's answer was not taken"))
	}
}

// pagePrompt is what the system prompt says of the page tools while
// page_edit is offered; pageGetPrompt while only page_get is.
const pagePrompt = `

Page drafts: the page the person is on edits the members of the config that page_context's editable.members lists, in its form and in its .conf, .yaml and .json tabs. When the person asks for a change those members hold, draft it there with page_edit: members set whole, ops at JSON pointers, or the page's part as text in one tab. The page writes it into its form or editor as an unsaved draft marked as yours; you never submit or save it, and nothing changes in fit until the person reviews it and clicks Save and apply on the page. Read the page first with page_get, change only what was asked, copy [redacted] values back where they were, and then say in a few words what you drafted and that it waits for the person's Save and apply. page_revert removes your drafts when the person asks. A change the page cannot hold (another member, a secret, admin, or a role's own settings) is not drafted: `

const pageGetPrompt = `

page_get reads what the open page holds now, the person's unsaved edits included. You cannot draft on the page: describe a change, or show its config text, for the person to make there.`

// writePage writes what the system prompt says of the page tools offered.
func writePage(b *strings.Builder, p promptParts) {
	switch {
	case p.pageEdit:
		b.WriteString(pagePrompt)
		if levelRank(p.level) >= levelRank(config.AssistantPropose) {
			b.WriteString("propose it with the propose tools instead.")
		} else {
			b.WriteString("describe it, and the page that edits it, for the person to make there.")
		}
	case p.pageGet:
		b.WriteString(pageGetPrompt)
	}
}

// hasTool reports whether tools has the tool name.
func hasTool(tools []*Tool, name string) bool {
	return slices.ContainsFunc(tools, func(t *Tool) bool { return t.Name == name })
}
