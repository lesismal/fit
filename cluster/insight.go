package cluster

import (
	"math"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/app/console"
)

// What the manager keeps of each poll, beside the member's state (I150):
// a series of its counters, bounded per member, and the certificates its
// listeners serve. Both are kept on the member's node, so that a member
// removed, or changed so that it gets a node of its own, takes them with
// it, and a member added later under its name starts afresh. The series
// are read as rates between polls (requests, 4xx and 5xx a second; the
// configs applied and failed in between) and as gauges (connections open,
// bytes queued for clients), per member and summed per group; nothing is
// asked of a member that the poller does not read already.

// SeriesPoints is how many polls of each member the manager keeps: an hour
// at the default poll_interval of 10s.
const SeriesPoints = 360

// DefaultSeriesPoints is how many GET metrics answers when it is not told.
const DefaultSeriesPoints = 90

// MaxMemberCerts bounds the certificates kept of a member.
const MaxMemberCerts = 256

// gapPolls is how many poll intervals apart two polls may be and still
// make a rate: farther apart, the member was down or the manager stopped
// in between, and the point has none.
const gapPolls = 3

// sample is one poll's counters.
type sample struct {
	at      int64 // Unix milliseconds
	started int64 // the member's start, Unix milliseconds
	hasReq  bool
	req     uint64
	r4, r5  uint64
	open    int64
	pending int64
	ok      uint64 // reloads that succeeded
	failed  uint64 // reloads that failed
}

// insight is what the polls of a member read for the series and the
// inventory: the samples, a ring of SeriesPoints once full, and the
// certificates of the latest poll that read them.
type insight struct {
	mu      sync.Mutex
	samples []sample
	next    int // where the next sample goes, once full
	certs   []CertEntry
	certsAt time.Time
	last    *Counters
}

// Counters are a member's counters as its latest poll read them, which
// the series are made from: what the member's own GET /status and /metrics
// say.
type Counters struct {
	At          time.Time      `json:"at"`
	Requests    *RequestTotals `json:"requests,omitempty"`
	Connections Connections    `json:"connections"`
	Reloads     Reloads        `json:"reloads"`
}

// RequestTotals are the requests a member answered since it started, and
// by the class of their status, as its GET /status's requests say.
type RequestTotals struct {
	Total   uint64            `json:"total"`
	Classes map[string]uint64 `json:"classes"`
}

// CertEntry is a certificate a member's listener serves, as the latest
// poll that read the member found it.
type CertEntry struct {
	Node      string    `json:"node"`
	Listener  string    `json:"listener"`
	Names     []string  `json:"names"`
	Issuer    string    `json:"issuer"`
	Serial    string    `json:"serial"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	// DaysLeft is how many whole days are left before it expires, less
	// than zero once it has.
	DaysLeft int `json:"days_left"`
	// Status is its member's state now, and Read when the poll read it.
	Status string    `json:"status"`
	Read   time.Time `json:"read"`
}

// observe keeps what a poll that read the member found; one without
// /status (a member that does not offer it) keeps nothing.
func (n *node) observe(now time.Time, p *poller) {
	st := p.status
	if st == nil {
		return
	}
	s := sample{at: now.UnixMilli(), started: p.answer.Started.UnixMilli(), open: st.Connections.Open,
		pending: st.Connections.PendingBytes, ok: st.Reloads.Succeeded, failed: st.Reloads.Failed}
	last := &Counters{At: now.UTC(), Connections: st.Connections, Reloads: st.Reloads}
	if rq := st.Requests; rq != nil {
		s.hasReq, s.req, s.r4, s.r5 = true, rq.Total, rq.Classes["4xx"], rq.Classes["5xx"]
		last.Requests = &RequestTotals{Total: rq.Total, Classes: map[string]uint64{}}
		for k, v := range rq.Classes {
			if len(last.Requests.Classes) < 8 {
				last.Requests.Classes[k] = v
			}
		}
	}
	certs := make([]CertEntry, 0, min(len(st.Certificates), MaxMemberCerts))
	for _, c := range st.Certificates {
		if len(certs) == MaxMemberCerts {
			break
		}
		certs = append(certs, CertEntry{Listener: clip(c.Listener, 128), Names: nonNil(clipList(c.Names, 16, 253)), Issuer: clip(c.Issuer, 256),
			Serial: clip(c.Serial, 64), NotBefore: c.NotBefore, NotAfter: c.NotAfter, Read: now.UTC()})
	}
	in := &n.ins
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.samples == nil {
		// Its whole ring at once, so that appending never grows it past.
		in.samples = make([]sample, 0, SeriesPoints)
	}
	if len(in.samples) < SeriesPoints {
		in.samples = append(in.samples, s)
	} else {
		in.samples[in.next] = s
		in.next = (in.next + 1) % SeriesPoints
	}
	in.certs, in.certsAt, in.last = certs, now, last
}

// ordered returns the samples kept, oldest first; in.mu is held.
func (in *insight) ordered() []sample {
	if len(in.samples) < SeriesPoints {
		return slices.Clone(in.samples)
	}
	return append(slices.Clone(in.samples[in.next:]), in.samples[:in.next]...)
}

// Value is a number of a series, or none: JSON null.
type Value struct {
	V  float64
	OK bool
}

// MarshalJSON writes the number, or null.
func (v Value) MarshalJSON() ([]byte, error) {
	if !v.OK {
		return []byte("null"), nil
	}
	return strconv.AppendFloat(nil, v.V, 'f', -1, 64), nil
}

// UnmarshalJSON reads a number, or null.
func (v *Value) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*v = Value{}
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return err
	}
	*v = Value{V: f, OK: true}
	return nil
}

func some(v float64) Value { return Value{V: math.Round(v*1000) / 1000, OK: true} }

// Series are a member's points, or a group's, as columns: T the time of
// each point, in Unix milliseconds, and the values at it, null where there
// is none. Requests, Errors4xx and Errors5xx are a second, over the time
// since the poll before; Reloads and ReloadsFailed the configs applied, and
// those that failed, since the poll before; Connections and PendingBytes
// as the poll read them. A group's Members counts the members summed at
// each point.
type Series struct {
	T             []int64 `json:"t"`
	Requests      []Value `json:"requests"`
	Errors4xx     []Value `json:"errors_4xx"`
	Errors5xx     []Value `json:"errors_5xx"`
	Connections   []Value `json:"connections"`
	PendingBytes  []Value `json:"pending_bytes"`
	Reloads       []Value `json:"reloads"`
	ReloadsFailed []Value `json:"reloads_failed"`
	Members       []int   `json:"members,omitempty"`
}

// columns returns the series' value columns, in a fixed order.
func (s *Series) columns() []*[]Value {
	return []*[]Value{&s.Requests, &s.Errors4xx, &s.Errors5xx, &s.Connections, &s.PendingBytes, &s.Reloads, &s.ReloadsFailed}
}

func (s *Series) add(t int64, vals [7]Value) {
	s.T = append(s.T, t)
	for i, c := range s.columns() {
		*c = append(*c, vals[i])
	}
}

func newSeries() Series {
	s := Series{T: []int64{}}
	for _, c := range s.columns() {
		*c = []Value{}
	}
	return s
}

// points makes the series of samples, the last n of them: each point's
// rates from the sample before, when that is the same run of the member
// (its start unchanged, its counters not gone back) and at most gapPolls
// intervals before.
func points(samples []sample, n int, interval time.Duration) Series {
	out := newSeries()
	from := max(len(samples)-n, 0)
	gap := int64(gapPolls) * interval.Milliseconds()
	for i := from; i < len(samples); i++ {
		s := samples[i]
		var vals [7]Value
		vals[3], vals[4] = some(float64(s.open)), some(float64(s.pending))
		if i > 0 {
			p := samples[i-1]
			dt := float64(s.at-p.at) / 1000
			if dt > 0 && s.at-p.at <= gap && s.started == p.started {
				if s.hasReq && p.hasReq && s.req >= p.req && s.r4 >= p.r4 && s.r5 >= p.r5 {
					vals[0], vals[1], vals[2] = some(float64(s.req-p.req)/dt), some(float64(s.r4-p.r4)/dt), some(float64(s.r5-p.r5)/dt)
				}
				if s.ok >= p.ok && s.failed >= p.failed {
					vals[5], vals[6] = some(float64(s.ok-p.ok)), some(float64(s.failed-p.failed))
				}
			}
		}
		out.add(s.at, vals)
	}
	return out
}

// sumGroup sums its members' series into buckets of interval: each
// member's latest point in a bucket counts, and a value is the sum of the
// members that have one there, none when no member has.
func sumGroup(members []Series, interval time.Duration) Series {
	w := max(interval.Milliseconds(), 1)
	type bucket struct {
		vals    [7]Value
		members int
	}
	buckets := map[int64]*bucket{}
	for _, s := range members {
		seen := map[int64]int{} // a member's latest point in each bucket
		for i, t := range s.T {
			seen[t/w] = i
		}
		for b, i := range seen {
			bk := buckets[b]
			if bk == nil {
				bk = &bucket{}
				buckets[b] = bk
			}
			bk.members++
			for c, col := range s.columns() {
				if v := (*col)[i]; v.OK {
					bk.vals[c] = some(bk.vals[c].V + v.V)
				}
			}
		}
	}
	keys := make([]int64, 0, len(buckets))
	for b := range buckets {
		keys = append(keys, b)
	}
	slices.Sort(keys)
	out := newSeries()
	out.Members = []int{}
	for _, b := range keys {
		out.add(b*w, buckets[b].vals)
		out.Members = append(out.Members, buckets[b].members)
	}
	return out
}

// NodeSeries is a member's series, its state and its latest counters.
type NodeSeries struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Groups   []string  `json:"groups"`
	Counters *Counters `json:"counters,omitempty"`
	Series   Series    `json:"series"`
}

// GroupSeries is a group's series: its members' summed.
type GroupSeries struct {
	Name   string   `json:"name"`
	Nodes  []string `json:"nodes"`
	Series Series   `json:"series"`
}

// MetricsBody is GET metrics's answer.
type MetricsBody struct {
	IntervalMs int64         `json:"interval_ms"`
	PointsMax  int           `json:"points_max"`
	Now        time.Time     `json:"now"`
	Nodes      []NodeSeries  `json:"nodes"`
	Groups     []GroupSeries `json:"groups"`
}

// MetricsQuery says which series Metrics returns: those of Nodes and of
// Groups, by name, every member's and every group's when both are empty;
// the last Points of each, DefaultSeriesPoints when 0.
type MetricsQuery struct {
	Nodes  []string
	Groups []string
	Points int
}

// Metrics returns the members' and groups' series q asks for. A name there
// is no member or group of is the error.
func (m *Manager) Metrics(q MetricsQuery) (MetricsBody, error) {
	n := q.Points
	if n <= 0 {
		n = DefaultSeriesPoints
	}
	n = min(n, SeriesPoints)
	all := len(q.Nodes) == 0 && len(q.Groups) == 0
	m.mu.Lock()
	for _, name := range q.Nodes {
		if index(&m.doc, name) < 0 {
			m.mu.Unlock()
			return MetricsBody{}, errNotFound(name)
		}
	}
	for _, g := range q.Groups {
		if groupIndex(&m.doc, g) < 0 {
			m.mu.Unlock()
			return MetricsBody{}, errNoGroup(g)
		}
	}
	type pick struct {
		name   string
		node   *node
		status string
		groups []string
	}
	var picks []pick
	for i := range m.doc.Members {
		mem := &m.doc.Members[i]
		p := pick{name: mem.Name, node: m.nodes[mem.Name], status: StatusError, groups: nonNil(m.groupsOf(mem))}
		if p.node != nil {
			p.node.mu.Lock()
			p.status = p.node.state.Status
			p.node.mu.Unlock()
		}
		picks = append(picks, p)
	}
	var groups []GroupView
	for i := range m.doc.Groups {
		g := &m.doc.Groups[i]
		if all || slices.Contains(q.Groups, g.Name) {
			groups = append(groups, m.viewLocked(g))
		}
	}
	m.mu.Unlock()
	sort.Slice(picks, func(i, j int) bool { return picks[i].name < picks[j].name })
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })

	body := MetricsBody{IntervalMs: m.interval.Milliseconds(), PointsMax: SeriesPoints, Now: m.now().UTC(), Nodes: []NodeSeries{}, Groups: []GroupSeries{}}
	byName := map[string]Series{}
	needed := map[string]bool{}
	for _, g := range groups {
		for _, name := range g.Nodes {
			needed[name] = true
		}
	}
	for _, p := range picks {
		shown := all || slices.Contains(q.Nodes, p.name)
		if !shown && !needed[p.name] {
			continue
		}
		ns := NodeSeries{Name: p.name, Status: p.status, Groups: p.groups, Series: newSeries()}
		if p.node != nil {
			in := &p.node.ins
			in.mu.Lock()
			samples := in.ordered()
			if in.last != nil {
				c := *in.last
				ns.Counters = &c
			}
			in.mu.Unlock()
			ns.Series = points(samples, n, m.interval)
		}
		byName[p.name] = ns.Series
		if shown {
			body.Nodes = append(body.Nodes, ns)
		}
	}
	for _, g := range groups {
		var members []Series
		for _, name := range g.Nodes {
			if s, ok := byName[name]; ok {
				members = append(members, s)
			}
		}
		body.Groups = append(body.Groups, GroupSeries{Name: g.Name, Nodes: nonNil(g.Nodes), Series: sumGroup(members, m.interval)})
	}
	return body, nil
}

// CertsBody is GET certificates's answer: every member's certificates,
// the one that expires first first; and each member, how many it serves
// and when they were read, for those that serve none or were never read.
type CertsBody struct {
	Now          time.Time   `json:"now"`
	Certificates []CertEntry `json:"certificates"`
	Nodes        []CertNode  `json:"nodes"`
}

// CertNode is a member in the inventory.
type CertNode struct {
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Count  int       `json:"count"`
	Read   time.Time `json:"read,omitzero"`
}

// Certificates returns the certificate inventory: every member's
// certificates as its latest poll that read them found them, sorted by
// expiry, the soonest first.
func (m *Manager) Certificates() CertsBody {
	now := m.now()
	body := CertsBody{Now: now.UTC(), Certificates: []CertEntry{}, Nodes: []CertNode{}}
	m.mu.Lock()
	for i := range m.doc.Members {
		name := m.doc.Members[i].Name
		cn := CertNode{Name: name, Status: StatusError}
		if n := m.nodes[name]; n != nil {
			n.mu.Lock()
			cn.Status = n.state.Status
			n.mu.Unlock()
			n.ins.mu.Lock()
			cn.Count = len(n.ins.certs)
			if !n.ins.certsAt.IsZero() {
				cn.Read = n.ins.certsAt.UTC()
			}
			for _, c := range n.ins.certs {
				c.Node, c.Status, c.Names = name, cn.Status, slices.Clone(c.Names)
				c.DaysLeft = int(math.Floor(c.NotAfter.Sub(now).Hours() / 24))
				body.Certificates = append(body.Certificates, c)
			}
			n.ins.mu.Unlock()
		}
		body.Nodes = append(body.Nodes, cn)
	}
	m.mu.Unlock()
	sort.SliceStable(body.Certificates, func(i, j int) bool {
		a, b := &body.Certificates[i], &body.Certificates[j]
		if !a.NotAfter.Equal(b.NotAfter) {
			return a.NotAfter.Before(b.NotAfter)
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.Listener < b.Listener
	})
	sort.Slice(body.Nodes, func(i, j int) bool { return body.Nodes[i].Name < body.Nodes[j].Name })
	return body
}

// ---- The API ----

// insightRoutes adds GET metrics, certificates and logs (logtail.go).
// The series and the inventory are what the poller reads, as the members'
// states are: any role reads them, as it reads every state (decision 033,
// item 122). A member's logs are its own read, which needs a role on it.
func (m *Manager) insightRoutes(api *router) {
	api.Handle("GET", "/api/metrics", m.metricsRoute)
	api.Handle("GET", "/api/certificates", func(r *console.Request) { _ = r.JSON(200, m.Certificates()) })
	api.Checked("GET", "/api/logs", m.logsRoute)
}

// metricsQuery reads a MetricsQuery from a request's query: node and group,
// repeated or separated by commas, and points.
func metricsQuery(v url.Values) (MetricsQuery, error) {
	q := MetricsQuery{Nodes: listParam(v, "node"), Groups: listParam(v, "group")}
	if p := v.Get("points"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > SeriesPoints {
			return q, fieldErr("points", "points is 1 to %d", SeriesPoints)
		}
		q.Points = n
	}
	return q, nil
}

// listParam returns the values of a query's name, each split at commas,
// without empty ones or repeats.
func listParam(v url.Values, name string) []string {
	var out []string
	for _, x := range v[name] {
		for p := range strings.SplitSeq(x, ",") {
			if p = strings.TrimSpace(p); p != "" && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func (m *Manager) metricsRoute(r *console.Request) {
	q, err := metricsQuery(r.Request.URL.Query())
	if err == nil {
		var body MetricsBody
		if body, err = m.Metrics(q); err == nil {
			_ = r.JSON(200, body)
			return
		}
	}
	m.answer(r, 0, nil, err)
}
