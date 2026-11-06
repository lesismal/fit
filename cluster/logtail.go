package cluster

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"sync"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/logs"
)

// Logs tailed from several members at once (I150): each member's own log
// keeps its latest messages in a ring (I125), which its Admin API answers
// at GET /logs/recent, after a sequence number. The manager asks each
// member named for the messages after the last it saw, at once, through
// Manager.Call, and answers them merged by time, with a cursor that holds
// each member's last sequence number: a page that follows the tail sends
// it back and gets what came since. A member that restarted numbers its
// messages from 1 again, and answers from its start, which its own
// endpoint does for a sequence number past its last.

// capLogsRecent is the capability of GET /logs/recent (admin.CapLogsRecent).
const capLogsRecent = "logs.recent"

// The bounds of a tail.
const (
	// MaxTailNodes is how many members one tail reads.
	MaxTailNodes = 16
	// DefaultTailLimit is how many messages a tail asks each member for
	// when it is not told; MaxTailLimit the most. A member's ring holds
	// logs.RingSize.
	DefaultTailLimit = 200
	MaxTailLimit     = logs.RingSize
	// maxCursorBytes bounds a cursor sent back.
	maxCursorBytes = 4096
)

// TailQuery is a tail: the members, the filter each member's ring applies,
// and Cursor, what the tail before answered, or "" for the latest.
type TailQuery struct {
	Nodes    []string
	Logger   string
	Level    string
	Contains string
	Limit    int
	Cursor   string
}

// TailRecord is a member's message.
type TailRecord struct {
	Node string `json:"node"`
	logs.Record
}

// TailNode is how reading a member went: the messages it gave, the last
// sequence number its ring holds, or why it could not be read.
type TailNode struct {
	Name  string   `json:"name"`
	Count int      `json:"count"`
	Last  uint64   `json:"last"`
	Error *Problem `json:"error,omitempty"`
}

// TailBody is a tail's answer: the members' messages merged by time, the
// oldest first, the cursor to send for what comes next, and each member's
// outcome.
type TailBody struct {
	Records []TailRecord `json:"records"`
	Cursor  string       `json:"cursor"`
	Nodes   []TailNode   `json:"nodes"`
}

// tailCursor is a cursor: each member's last sequence number.
type tailCursor map[string]uint64

func (c tailCursor) encode() string {
	data, _ := json.Marshal(c) // a map of numbers: cannot fail
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(s string) (tailCursor, error) {
	c := tailCursor{}
	if s == "" {
		return c, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(s) > maxCursorBytes || json.Unmarshal(data, &c) != nil {
		return nil, fieldErr("cursor", "the cursor is not one a tail answered")
	}
	return c, nil
}

// check checks q, filling in its defaults, and returns its cursor.
func (q *TailQuery) check(m *Manager) (tailCursor, error) {
	switch {
	case len(q.Nodes) == 0:
		return nil, fieldErr("node", "name the members whose logs to tail")
	case len(q.Nodes) > MaxTailNodes:
		return nil, fieldErr("node", "a tail reads %d members at most", MaxTailNodes)
	case q.Level != "" && q.Level != "debug" && q.Level != "info" && q.Level != "warn" && q.Level != "error":
		return nil, fieldErr("level", "level is debug, info, warn or error")
	case len(q.Contains) > 256 || len(q.Logger) > 64:
		return nil, fieldErr("contains", "contains is 256 bytes at most, and logger 64")
	case q.Limit < 0 || q.Limit > MaxTailLimit:
		return nil, fieldErr("limit", "limit is 1 to %d", MaxTailLimit)
	}
	if q.Limit == 0 {
		q.Limit = DefaultTailLimit
	}
	m.mu.Lock()
	for _, n := range q.Nodes {
		if m.nodes[n] == nil {
			m.mu.Unlock()
			return nil, errNotFound(n)
		}
	}
	m.mu.Unlock()
	return decodeCursor(q.Cursor)
}

// recentAnswer is a member's GET /logs/recent.
type recentAnswer struct {
	Records []logs.Record `json:"records"`
	Last    uint64        `json:"last"`
}

// TailLogs reads the latest messages of q's members, after its cursor,
// and calls done once with them merged, from a fib worker, a timer or the
// caller's goroutine; it must not block. TailLogs itself never blocks. It
// checks nothing of the user's roles: its callers do.
func (m *Manager) TailLogs(q TailQuery, done func(TailBody, error)) {
	cur, err := q.check(m)
	if err != nil {
		done(TailBody{}, err)
		return
	}
	nodes := slices.Clone(q.Nodes)
	var mu sync.Mutex
	outs := make([]TailNode, len(nodes))
	got := make([][]logs.Record, len(nodes))
	left := len(nodes)
	finish := func() {
		body := TailBody{Records: []TailRecord{}, Nodes: outs}
		next := tailCursor{}
		for i, name := range nodes {
			next[name] = cur[name]
			if outs[i].Error == nil {
				next[name] = outs[i].Last
			}
			for _, r := range got[i] {
				body.Records = append(body.Records, TailRecord{Node: name, Record: r})
			}
		}
		sort.SliceStable(body.Records, func(i, j int) bool {
			a, b := &body.Records[i], &body.Records[j]
			if !a.Time.Equal(b.Time) {
				return a.Time.Before(b.Time)
			}
			if a.Node != b.Node {
				return a.Node < b.Node
			}
			return a.Seq < b.Seq
		})
		body.Cursor = next.encode()
		done(body, nil)
	}
	end := func(i int, recs []logs.Record, last uint64, prob *Problem) {
		mu.Lock()
		outs[i] = TailNode{Name: nodes[i], Count: len(recs), Last: last, Error: prob}
		got[i] = recs
		left--
		over := left == 0
		mu.Unlock()
		if over {
			finish()
		}
	}
	query := url.Values{}
	for k, v := range map[string]string{"logger": q.Logger, "level": q.Level, "contains": q.Contains} {
		if v != "" {
			query.Set(k, v)
		}
	}
	query.Set("limit", strconv.Itoa(q.Limit))
	for i, name := range nodes {
		if caps := m.polledCaps(name); caps != nil && !slices.Contains(caps, capLogsRecent) {
			end(i, nil, cur[name], &Problem{Kind: KindUnsupported, At: m.now().UTC(),
				Message: "the member does not list " + capLogsRecent + " in its capabilities: it is a fit older than the cluster's logs"})
			continue
		}
		qi := url.Values{}
		for k, v := range query {
			qi[k] = v
		}
		if after := cur[name]; after > 0 {
			qi.Set("after", strconv.FormatUint(after, 10))
		}
		err := m.Call(name, "GET", "/logs/recent?"+qi.Encode(), nil, nil, func(r *Reply, prob *Problem) {
			if prob != nil {
				end(i, nil, cur[name], prob)
				return
			}
			if r.Status != 200 {
				end(i, nil, cur[name], &Problem{Kind: KindStatus, Status: r.Status, At: m.now().UTC(),
					Message: clip(fmt.Sprintf("GET /logs/recent answered %d: %s", r.Status, errorOf(r.Body)), maxMessage)})
				return
			}
			var a recentAnswer
			if err := json.Unmarshal(r.Body, &a); err != nil {
				end(i, nil, cur[name], &Problem{Kind: KindBadAnswer, Status: r.Status, At: m.now().UTC(), Message: "the member's /logs/recent is not the JSON expected"})
				return
			}
			if len(a.Records) > MaxTailLimit {
				a.Records = a.Records[len(a.Records)-MaxTailLimit:]
			}
			end(i, a.Records, a.Last, nil)
		})
		if err != nil {
			end(i, nil, cur[name], &Problem{Kind: KindUnreachable, At: m.now().UTC(), Message: err.Error()})
		}
	}
}

// polledCaps returns what the member name's latest poll said it offers,
// nil before one read it.
func (m *Manager) polledCaps(name string) []string {
	m.mu.Lock()
	n := m.nodes[name]
	m.mu.Unlock()
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state.Node == nil {
		return nil
	}
	return n.state.Node.Capabilities
}

// errorOf returns the error a JSON answer holds, or its start.
func errorOf(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return clip(string(body), 128)
}

// tailQuery reads a TailQuery from a request's query: node (repeated or
// separated by commas), logger, level, contains, limit and cursor.
func tailQuery(v url.Values) (TailQuery, error) {
	q := TailQuery{Nodes: listParam(v, "node"), Logger: v.Get("logger"), Level: v.Get("level"), Contains: v.Get("contains"), Cursor: v.Get("cursor")}
	if l := v.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			return q, fieldErr("limit", "limit is 1 to %d", MaxTailLimit)
		}
		q.Limit = n
	}
	return q, nil
}

// logsRoute answers GET /api/logs: the tail of the members named, which
// the user needs a role on each of; one of the reads of members (MaxReads)
// while it runs.
func (m *Manager) logsRoute(r *console.Request) {
	q, err := tailQuery(r.Request.URL.Query())
	if err != nil {
		m.answer(r, 0, nil, err)
		return
	}
	if !m.allow(r, Need{Role: RoleViewer, Nodes: q.Nodes}) || !m.takeRead(r) {
		return
	}
	r.Context.Retain()
	m.TailLogs(q, func(body TailBody, err error) {
		<-m.reads
		defer r.Context.Release()
		if err != nil {
			m.answer(r, 0, nil, err)
			return
		}
		_ = r.JSON(200, body)
	})
}
