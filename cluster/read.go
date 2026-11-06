package cluster

import (
	"encoding/json"
	stdhttp "net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/lesismal/fit/app/console"
)

// MaxReads is how many reads of members through the manager run at once;
// one more is answered 429.
const MaxReads = 8

// Read is a GET of a member's Admin API the manager passes on to the
// workbench, for a member's page: the path through the member's console
// and through its Admin API, and the capability GET /node lists for it.
type Read struct {
	Console    string
	AdminAPI   string
	Capability string
}

// Reads are the member's reads GET /api/m/cluster/nodes/<name>/read/<what>
// passes on: read-only, each named, nothing else. I143's node switcher
// passes on any of the member's API at /api/n/<member>/ in their place,
// with the same node's session and bounds.
var Reads = map[string]Read{
	"node":     {Console: "/api/node", AdminAPI: "/node", Capability: "node"},
	"status":   {Console: "/api/m/admin/status", AdminAPI: "/status", Capability: capStatus},
	"versions": {Console: "/api/m/admin/versions", AdminAPI: "/versions", Capability: "config.versions"},
	"check":    {Console: "/api/m/admin/config/check", AdminAPI: "/config/check", Capability: capCheck},
}

// ReadNames returns the names of Reads, sorted.
func ReadNames() []string {
	out := make([]string, 0, len(Reads))
	for k := range Reads {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readFailure is a read's answer when the member could not be read: the
// problem, its message also as error, as every error of the console is.
type readFailure struct {
	Error   string   `json:"error"`
	Problem *Problem `json:"problem"`
}

// read answers GET /api/nodes/<name>/read/<what>: the member's answer to
// the Admin API's GET what names, as it answered it, through the node the
// poller reads it with, its session and its bounds; or 502 with the problem
// when the member could not be read.
func (m *Manager) read(r *console.Request, name, what string) {
	rd, ok := Reads[what]
	if !ok {
		_ = console.Error(r.Context, 404, "a member's reads are "+strings.Join(ReadNames(), ", "))
		return
	}
	if r.Request.Method != "GET" {
		r.Header().Set("Allow", "GET")
		_ = console.Error(r.Context, 405, "the method is not allowed")
		return
	}
	m.mu.Lock()
	n := m.nodes[name]
	var caps []string
	if n != nil {
		n.mu.Lock()
		if n.state.Node != nil {
			caps = n.state.Node.Capabilities
		}
		n.mu.Unlock()
	}
	m.mu.Unlock()
	if n == nil {
		_ = console.Error(r.Context, 404, errNotFound(name).Error())
		return
	}
	// A member that said what it serves is not asked for what it does not.
	if caps != nil && rd.Capability != "node" && !slices.Contains(caps, rd.Capability) {
		_ = r.JSON(502, readFailure{Error: "the member does not list " + rd.Capability + " in its capabilities",
			Problem: &Problem{Kind: KindUnsupported, Message: "the member does not list " + rd.Capability + " in its capabilities", At: m.now().UTC()}})
		return
	}
	select {
	case m.reads <- struct{}{}:
	default:
		_ = console.Error(r.Context, 429, "other reads of members are under way; try again")
		return
	}
	path := rd.AdminAPI
	if n.spec.Access == AccessConsole {
		path = rd.Console
	}
	r.Context.Retain()
	n.read(path, func(body json.RawMessage, prob *Problem) {
		<-m.reads
		defer r.Context.Release()
		if prob != nil {
			_ = r.JSON(502, readFailure{Error: prob.Message, Problem: prob})
			return
		}
		_ = console.Write(r.Context, stdhttp.StatusOK, "application/json", body)
	})
}

// read reads the member's path with the node's session, logging in first
// when there is none and again once when it ended, and calls done once with
// the answer, JSON within the transport's bound, or why there is none.
func (n *node) read(path string, done func(json.RawMessage, *Problem)) {
	var once sync.Once
	end := func(body json.RawMessage, prob *Problem) {
		once.Do(func() {
			if prob != nil {
				if prob.At.IsZero() {
					prob.At = n.m.now().UTC()
				}
				prob.Message = n.redact(prob.Message)
			}
			done(body, prob)
		})
	}
	p := &poller{n: n, start: n.m.now()}
	p.finishFn = func(prob *Problem) { end(nil, prob) }
	var body json.RawMessage
	get := func() { p.get(path, &body, func() { end(body, nil) }) }
	if n.spec.Access == AccessConsole && n.currentSession() == nil {
		n.login(func(prob *Problem) {
			if prob != nil {
				end(nil, prob)
				return
			}
			get()
		})
		return
	}
	get()
}
