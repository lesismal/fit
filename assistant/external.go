package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// What outside agents reach of the assistant: the MCP server (package
// assistant/mcp, I130) calls the tools of the registry through CallTool,
// and reads the config, the schema and the versions through ReadResource,
// each redacted and bounded as for the model, and each in the audit log
// with its source. It runs nothing of the panel's: no model is called, and
// the page tools and ask_user, which need the open page and the person,
// are not served.

// SourceMCP is the source of what the MCP server did.
const SourceMCP = "mcp"

// AuditTool is an audit entry of an outside agent's: a tool it called, or a
// resource or a prompt it read.
const AuditTool = "tool"

// OutcomeOK is an outside agent's call that succeeded; one that failed is
// ReasonError's.
const OutcomeOK = "ok"

// MaxResourceBytes bounds a resource's text: a config past it is cut, and
// says so.
const MaxResourceBytes = 1 << 20

// ErrNoResource is a resource URI that names none.
var ErrNoResource = errors.New("no such resource")

// Served reports whether t is served to outside agents: a tool that runs in
// fit, on the pool. ask_user waits for the person in the panel, and the
// page tools (KindPage) act on a page that is open, neither of which an outside agent
// has.
func Served(t *Tool) bool { return t.Kind == KindPool && t.Group != GroupPage }

// ExternalTools returns the tools an outside caller of level may call under
// the settings in use, in the registry's order.
func (a *Assistant) ExternalTools(level string) []*Tool {
	var out []*Tool
	for _, t := range a.registry.For(level, a.Settings()) {
		if Served(t) {
			out = append(out, t)
		}
	}
	return out
}

// External is who calls from outside: the console user, the level the call
// has (its token's, within the user's), and where from.
type External struct {
	User  string
	Level string
	// Source is where from, as SourceMCP, and Token the name of the
	// caller's token, for the audit log.
	Source, Token string
	// Conversation is the user's conversation the caller's proposals are
	// kept in, "" for none yet; Title names one made for it.
	Conversation, Title string
	// Expired, when not nil, is set once the caller gave up on the call: a
	// propose tool then adds no step.
	Expired *atomic.Bool
}

// ExternalResult is a tool's result as an outside caller gets it: its text,
// redacted and cut to its bound, and whether it failed or was cut. A
// propose tool's names the proposal it added to, and the conversation it is
// kept in, which may be a new one.
type ExternalResult struct {
	Text         string
	Error, Cut   bool
	Proposal     string
	Revision     int
	Conversation string
	// Link is the workbench's link (#/cluster/...) of the rollout plan or
	// operation a cluster propose tool made (I153), which a person starts
	// there.
	Link string
}

// CallTool runs the tool name for x with args, a JSON object, on the
// caller's goroutine, which is to be one of the console's pool: it reads
// the Admin API in process, and a propose tool checks the config. The
// level is checked against the settings in use now; a propose tool keeps
// its step in x's conversation, made when there is none.
func (a *Assistant) CallTool(ctx context.Context, x External, name string, args json.RawMessage) (res ExternalResult) {
	s := a.Settings()
	red := a.redactor(s)
	res.Conversation = x.Conversation
	var prop *AuditProposal
	defer func() {
		if p := recover(); p != nil {
			res = ExternalResult{Text: fmt.Sprintf("the tool failed: %v", p), Error: true, Conversation: res.Conversation}
		}
		e := AuditEntry{User: x.User, Source: x.Source, Token: x.Token, Kind: AuditTool, Proposal: prop, Outcome: OutcomeOK,
			Calls: []AuditCall{{Tool: name, Args: clip(red.text(string(args)), maxAuditArgs), Error: res.Error}}}
		if prop != nil {
			e.Conversation = res.Conversation
		}
		if res.Error {
			e.Outcome, e.Detail = ReasonError, res.Text
		}
		a.Audit(e)
	}()
	fail := func(msg string) ExternalResult {
		return ExternalResult{Text: red.text(msg), Error: true, Conversation: res.Conversation}
	}
	t := a.registry.Tool(name)
	if t == nil || !Served(t) {
		return fail("there is no tool " + name)
	}
	if ok, why := t.Allowed(x.Level, s); !ok {
		return fail("not run: the token's level is " + x.Level + ", and the tool " + why)
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if !isJSONObject(args) {
		return fail("the arguments are not a JSON object")
	}
	expired := x.Expired
	if expired == nil {
		expired = new(atomic.Bool)
	}
	c := &Call{Context: ctx, Args: args, User: x.User, Level: x.Level, Settings: s, a: a, expired: expired}
	// A cluster propose tool makes a rollout plan or an operation of the
	// manager's, not a step of a proposal: it needs no conversation.
	if t.Level == config.AssistantPropose && !clusterPropose(name) {
		id, err := a.ensureConversation(x.User, x.Conversation, x.Title)
		if err != nil {
			return fail(err.Error())
		}
		res.Conversation, c.Conversation = id, id
		c.proposed = func(p *Proposal, changed []string) {
			prop = auditOf(p, changed)
			res.Proposal, res.Revision = p.ID, p.Revision
		}
	}
	c.linked = func(link string) { res.Link = link }
	out, err := t.Run(c)
	if err != nil {
		return fail("error: " + err.Error())
	}
	limit := s.Limits.ToolResultBytes()
	if t.MaxBytes > 0 && t.MaxBytes < limit {
		limit = t.MaxBytes
	}
	res.Text, res.Cut = cutText(red.text(out), int(limit))
	return res
}

func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

// ensureConversation returns user's conversation id, made with title when
// there is none (it was never made, or was dropped past the bounds or
// deleted since), and refuses one answering in the panel now. It is kept
// as the most recent, so that the bounds drop others first.
func (a *Assistant) ensureConversation(user, id, title string) (string, error) {
	a.chats.mu.Lock()
	u := a.userLocked(user)
	conv := u.conversation(id)
	now := nowMs()
	switch {
	case conv == nil:
		if title == "" {
			title = "MCP"
		}
		conv = &Conversation{ID: newID(), Title: clip(title, 120), Created: now}
		u.convs = append(u.convs, conv)
	case conv.busy != nil:
		a.chats.mu.Unlock()
		return "", errors.New("the conversation the proposals are kept in is answering in the workbench's panel: try again once it is done")
	}
	conv.Updated = now
	u.boundLocked(a.Settings().Limits.Conversations())
	a.chats.mu.Unlock()
	a.saveChats(user)
	return conv.ID, nil
}

// AuditRead adds to the audit log what an outside caller read that is not a
// tool: a resource or a prompt (what), named arg; failed, when not "", why
// it failed.
func (a *Assistant) AuditRead(x External, what, arg, failed string) {
	e := AuditEntry{User: x.User, Source: x.Source, Token: x.Token, Kind: AuditTool, Outcome: OutcomeOK,
		Calls: []AuditCall{{Tool: what, Args: clip(arg, maxAuditArgs), Error: failed != ""}}}
	if failed != "" {
		e.Outcome, e.Detail = ReasonError, failed
	}
	a.Audit(e)
}

// Resource is a resource the assistant serves outside: a URI, its name,
// what it is, and its media type.
type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

// The resources: the config, as written and as it runs, the status, the
// kept versions, and the schema.
var resources = []Resource{
	{URI: "fit://config/base", Name: "config-base", Title: "The base config",
		Description: "fit's config as written and kept, JSON, with secrets redacted and the assistant's, the console's users and the Admin API's own settings left out.", MIMEType: "application/json"},
	{URI: "fit://config/effective", Name: "config-effective", Title: "The effective config",
		Description: "The config fit runs: the base config with what providers added, redacted as the base one is.", MIMEType: "application/json"},
	{URI: "fit://status", Name: "status", Title: "What fit runs now",
		Description: "The config version, the listeners bound, connections, upstreams in short, the roles running and the uptime.", MIMEType: "application/json"},
	{URI: "fit://versions", Name: "versions", Title: "The kept config versions",
		Description: "The config versions fit keeps, and the current one.", MIMEType: "application/json"},
	{URI: "fit://schema", Name: "schema", Title: "The config's schema, top level",
		Description: "The config's top-level members from its schema: what each is, its type and default. fit://schema/{path} describes one.", MIMEType: "application/json"},
}

// ResourceTemplate is a family of resources by a URI template.
type ResourceTemplate struct {
	URITemplate string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

var resourceTemplates = []ResourceTemplate{
	{URITemplate: "fit://schema/{path}", Name: "schema-member", Title: "A member of the config's schema",
		Description: "A member of the config, by its dotted path (as routes.handle or admin.console): what it is, its type, values, default and members.", MIMEType: "application/json"},
	{URITemplate: "fit://versions/{n}", Name: "version", Title: "A kept config version",
		Description: "The config kept as version n, JSON, redacted as the base config is.", MIMEType: "application/json"},
}

// Resources returns the resources the assistant serves outside.
func (a *Assistant) Resources() []Resource { return resources }

// ResourceTemplates returns the families of resources it serves.
func (a *Assistant) ResourceTemplates() []ResourceTemplate { return resourceTemplates }

// ReadResource returns the resource uri for x, redacted and cut to
// MaxResourceBytes, and its media type; ErrNoResource when uri names none.
// It reads the Admin API in process: on the console's pool. Every read is
// in the audit log.
func (a *Assistant) ReadResource(ctx context.Context, x External, uri string) (text, mime string, err error) {
	defer func() {
		failed := ""
		if err != nil {
			failed = err.Error()
		}
		a.AuditRead(x, "resources/read", uri, failed)
	}()
	if levelRank(x.Level) < levelRank(config.AssistantRead) {
		return "", "", errors.New("the token's level is " + x.Level + ": it reads nothing")
	}
	s := a.Settings()
	c := &Call{Context: ctx, User: x.User, Level: x.Level, Settings: s, a: a, expired: new(atomic.Bool)}
	run := func(tool func(*Call) (string, error), args any) (string, error) {
		c.Args, _ = json.Marshal(args)
		return tool(c)
	}
	switch {
	case uri == "fit://config/base":
		text, err = run(toolConfigGet, map[string]string{"source": "base"})
	case uri == "fit://config/effective":
		text, err = run(toolConfigGet, map[string]string{"source": "effective"})
	case uri == "fit://status":
		text, err = run(toolStatusGet, struct{}{})
	case uri == "fit://versions":
		text, err = run(toolVersionsDiff, struct{}{})
	case uri == "fit://schema":
		text, err = run(toolSchemaDescribe, struct{}{})
	case strings.HasPrefix(uri, "fit://schema/"):
		path := strings.TrimPrefix(uri, "fit://schema/")
		if path == "" || strings.ContainsAny(path, "/?#") {
			return "", "", ErrNoResource
		}
		text, err = run(toolSchemaDescribe, map[string]string{"path": path})
	case strings.HasPrefix(uri, "fit://versions/"):
		n, perr := strconv.ParseUint(strings.TrimPrefix(uri, "fit://versions/"), 10, 64)
		if perr != nil || n == 0 {
			return "", "", ErrNoResource
		}
		text, err = a.version(ctx, s, n)
	default:
		return "", "", ErrNoResource
	}
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == 404 {
			return "", "", fmt.Errorf("%w: %s", ErrNoResource, a.redactor(s).text(ae.Msg))
		}
		return "", "", errors.New(a.redactor(s).text(err.Error()))
	}
	text, _ = cutText(a.redactor(s).text(text), MaxResourceBytes)
	return text, "application/json", nil
}

// version returns the config kept as version n, redacted.
func (a *Assistant) version(ctx context.Context, s *config.Assistant, n uint64) (string, error) {
	data, err := a.apiJSON(ctx, "GET", "/versions/"+strconv.FormatUint(n, 10), "", nil)
	if err != nil {
		return "", err
	}
	out, err := a.redactor(s).redactJSON(data, hiddenMembers)
	if err != nil {
		return "", errors.New("the version is not JSON")
	}
	return string(out), nil
}

// Extend adds routes to the module the assistant is served as, beside its
// own: an MCP server's pages. It is called before the console serves.
func (a *Assistant) Extend(routes func(api *console.API)) {
	a.mu.Lock()
	a.extra = append(a.extra, routes)
	a.mu.Unlock()
}
