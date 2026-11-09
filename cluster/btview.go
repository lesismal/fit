package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/lesismal/fit/app/console"
)

// The cluster's view of its members' BitTorrent roles (I206): each
// member's GET /bittorrent/summary (admin.CapBTSummary), read through
// Manager.Call as every read of a member is, at most max_concurrency
// members at once, each call bounded by the calls' timeout. The reads are
// made when a page or the assistant asks, not by the poller, and one read
// of every member serves every request for btViewTTL: a read under way is
// joined, never started twice. A member down, or one whose last poll says
// it lacks the summary (a fit older than I206), is not asked.

// capBTSummary is the capability of GET /bittorrent/summary.
const capBTSummary = "bittorrent.summary"

// btViewTTL is how long a read of the members' summaries serves; a
// variable for tests.
var btViewTTL = 2 * time.Second

// BTSummary is a member's GET /bittorrent/summary: its client role, nil
// when it runs none, and its tracker role's statistics, nil when it runs
// none.
type BTSummary struct {
	Client  *BTClient  `json:"client"`
	Tracker *BTTracker `json:"tracker"`
}

// BTClient is a member's client role, as its summary sums its torrents.
type BTClient struct {
	Running      bool           `json:"running"`
	Listen       string         `json:"listen,omitempty"`
	Torrents     int            `json:"torrents"`
	States       map[string]int `json:"states"`
	DownloadRate float64        `json:"download_rate"`
	UploadRate   float64        `json:"upload_rate"`
	Downloaded   int64          `json:"downloaded"`
	Uploaded     int64          `json:"uploaded"`
	Size         int64          `json:"size"`
	Completed    int64          `json:"completed"`
	Peers        int            `json:"peers"`
	Errors       int            `json:"errors"`
	Publish      []BTPublish    `json:"publish"`
}

// BTPublish is a publish entry of a member's client.
type BTPublish struct {
	Name     string `json:"name"`
	Torrents int    `json:"torrents"`
	Pending  int    `json:"pending"`
	Error    string `json:"error,omitempty"`
}

// BTTracker is a member's tracker role, as its statistics say.
type BTTracker struct {
	Torrents    int               `json:"torrents"`
	Peers       int               `json:"peers"`
	Announces   uint64            `json:"announces"`
	Scrapes     uint64            `json:"scrapes"`
	Refused     map[string]uint64 `json:"refused,omitempty"`
	Private     bool              `json:"private"`
	Allowed     int               `json:"allowed"`
	UDP         string            `json:"udp,omitempty"`
	MaxTorrents int               `json:"max_torrents"`
	MaxPeers    int               `json:"max_peers"`
}

// The bounds of what a member's summary may hold, past which it is cut.
const (
	maxBTStates  = 16
	maxBTPublish = 64
	maxBTRefused = 8
)

// clipped returns s cut to the bounds a manager keeps.
func (s *BTSummary) clipped() *BTSummary {
	if c := s.Client; c != nil {
		c.Listen = clip(c.Listen, 64)
		if len(c.States) > maxBTStates {
			c.States = nil
		}
		if c.States == nil {
			c.States = map[string]int{}
		}
		if len(c.Publish) > maxBTPublish {
			c.Publish = c.Publish[:maxBTPublish]
		}
		if c.Publish == nil {
			c.Publish = []BTPublish{}
		}
		for i := range c.Publish {
			c.Publish[i].Name, c.Publish[i].Error = clip(c.Publish[i].Name, MaxNameBytes*2), clip(c.Publish[i].Error, maxMessage)
		}
	}
	if t := s.Tracker; t != nil {
		t.UDP = clip(t.UDP, 64)
		if len(t.Refused) > maxBTRefused {
			t.Refused = nil
		}
	}
	return s
}

// BTMember is a member's BitTorrent roles as the view read them, or why
// they could not be read.
type BTMember struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Groups []string          `json:"groups"`
	// Status is the member's state as the poller has it.
	Status  string     `json:"status"`
	Summary *BTSummary `json:"summary,omitempty"`
	Error   *Problem   `json:"error,omitempty"`
	Read    time.Time  `json:"read,omitzero"`
}

// BTTotals sum the members' summaries.
type BTTotals struct {
	// Members counts the members, Read those whose summary was read,
	// Clients those that run the client role, Running those whose client
	// runs, Trackers those that run the tracker role.
	Members  int `json:"members"`
	Read     int `json:"read"`
	Clients  int `json:"clients"`
	Running  int `json:"running"`
	Trackers int `json:"trackers"`
	// The sums of the clients' summaries.
	Torrents     int            `json:"torrents"`
	States       map[string]int `json:"states"`
	DownloadRate float64        `json:"download_rate"`
	UploadRate   float64        `json:"upload_rate"`
	Downloaded   int64          `json:"downloaded"`
	Uploaded     int64          `json:"uploaded"`
	Peers        int            `json:"peers"`
	Errors       int            `json:"errors"`
	Published    int            `json:"published"`
	// The sums of the trackers' statistics.
	TrackerTorrents int    `json:"tracker_torrents"`
	TrackerPeers    int    `json:"tracker_peers"`
	Announces       uint64 `json:"announces"`
}

// BTView is the cluster's view of its members' BitTorrent roles: when it
// was read, each member's, their sums, and the distributions under way.
type BTView struct {
	Read    time.Time  `json:"read"`
	Members []BTMember `json:"members"`
	Totals  BTTotals   `json:"totals"`
	// Distributions counts the distributions kept by state.
	Distributions map[string]int `json:"distributions"`
}

// btViewState is the latest read of the members' summaries, and the
// requests waiting for the one under way; under its own mutex.
type btViewState struct {
	mu      sync.Mutex
	last    []BTMember
	at      time.Time
	busy    bool
	waiting []func([]BTMember, time.Time)
}

// BitTorrent reads every member's BitTorrent roles, or takes the read made
// within btViewTTL, and calls done once with the view, from a goroutine of
// the read's or the caller's; it must not block. BitTorrent itself never
// blocks. Any role reads the view, as any reads the members' states.
func (m *Manager) BitTorrent(done func(BTView)) {
	v := &m.btv
	v.mu.Lock()
	if !v.at.IsZero() && m.now().Sub(v.at) < btViewTTL {
		last, at := v.last, v.at
		v.mu.Unlock()
		done(m.btView(last, at))
		return
	}
	v.waiting = append(v.waiting, func(last []BTMember, at time.Time) { done(m.btView(last, at)) })
	if v.busy {
		v.mu.Unlock()
		return
	}
	v.busy = true
	v.mu.Unlock()
	go m.readBT()
}

// readBT reads every member's summary, then answers those waiting.
func (m *Manager) readBT() {
	m.mu.Lock()
	names := make([]string, 0, len(m.nodes))
	for name := range m.nodes {
		names = append(names, name)
	}
	m.mu.Unlock()
	sort.Strings(names)
	out := m.readSummaries(names)
	now := m.now().UTC()
	v := &m.btv
	v.mu.Lock()
	v.last, v.at, v.busy = out, now, false
	waiting := v.waiting
	v.waiting = nil
	v.mu.Unlock()
	for _, w := range waiting {
		w(out, now)
	}
}

// readSummaries reads the summaries of names, at most max_concurrency at
// once; it blocks until each has answered or failed, each bounded by the
// calls' timeout: a goroutine's of its own, never a worker's.
func (m *Manager) readSummaries(names []string) []BTMember {
	out := make([]BTMember, len(names))
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	var wg sync.WaitGroup
	for i, name := range names {
		out[i] = BTMember{Name: name}
		if prob := m.btReadable(name); prob != nil {
			out[i].Error = prob
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-m.stop:
			out[i].Error = &Problem{Kind: KindUnreachable, At: m.now().UTC(), Message: "the manager stopped"}
			continue
		}
		wg.Add(1)
		m.btSummary(name, func(s *BTSummary, prob *Problem) {
			out[i].Summary, out[i].Error = s, prob
			if prob == nil {
				out[i].Read = m.now().UTC()
			}
			<-sem
			wg.Done()
		})
	}
	wg.Wait()
	return out
}

// btReadable says why the member name is not asked for its summary: it is
// down, or its last poll says it offers none; nil when it is asked.
func (m *Manager) btReadable(name string) *Problem {
	m.mu.Lock()
	n := m.nodes[name]
	m.mu.Unlock()
	if n == nil {
		return &Problem{Kind: KindUnreachable, At: m.now().UTC(), Message: "the member was removed"}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st.Status == StatusDown {
		p := &Problem{Kind: KindUnreachable, At: m.now().UTC(), Message: "the member is down, as its last poll found it"}
		if st.Error != nil {
			p.Kind, p.Message = st.Error.Kind, "the member is down: "+st.Error.Message
		}
		return p
	}
	if st.Node != nil && !slices.Contains(st.Node.Capabilities, capBTSummary) {
		return &Problem{Kind: KindUnsupported, At: m.now().UTC(),
			Message: "the member does not list " + capBTSummary + " in its capabilities: it is a fit older than the cluster's BitTorrent view"}
	}
	return nil
}

// btSummary reads the member name's summary; done is called once, from a
// fib worker, a timer or the caller's goroutine.
func (m *Manager) btSummary(name string, done func(*BTSummary, *Problem)) {
	var s BTSummary
	m.call(name, "GET", "/bittorrent/summary", nil, nil, &s, func(_ *Reply, prob *Problem) {
		if prob != nil {
			if prob.At.IsZero() {
				prob.At = m.now().UTC()
			}
			if prob.Status == 404 {
				prob.Kind = KindUnsupported
				prob.Message = "the member serves no GET /bittorrent/summary: it is a fit older than the cluster's BitTorrent view"
			}
			done(nil, prob)
			return
		}
		done(s.clipped(), nil)
	})
}

// btView makes the view of members read at at, with the members' states
// and groups as they are now, and the distributions' counts.
func (m *Manager) btView(read []BTMember, at time.Time) BTView {
	states := map[string]State{}
	for _, s := range m.States() {
		states[s.Name] = s
	}
	view := BTView{Read: at, Members: make([]BTMember, 0, len(read)), Totals: BTTotals{States: map[string]int{}}, Distributions: m.distCounts()}
	for _, b := range read {
		st, ok := states[b.Name]
		if !ok {
			continue // removed since
		}
		b.Status, b.Labels, b.Groups = st.Status, st.Labels, nonNil(st.Groups)
		view.Members = append(view.Members, b)
		t := &view.Totals
		t.Members++
		s := b.Summary
		if s == nil {
			continue
		}
		t.Read++
		addSummary(t, s)
	}
	return view
}

// btRoutes adds GET bittorrent, the view, beside the distributions'
// endpoints (distribute.go).
func (m *Manager) btRoutes(api *router) {
	api.Handle("GET", "/api/bittorrent", func(r *console.Request) {
		r.Context.Retain()
		m.BitTorrent(func(v BTView) {
			defer r.Context.Release()
			_ = r.JSON(200, v)
		})
	})
	m.distRoutes(api)
}

// btAdminAPI adds the view and the distributions to the manager's Admin
// API, at prefix: GET bittorrent, GET and POST distributions, GET
// distributions/{id} and POST distributions/{id}/cancel, acting as
// AdminAPIUser.
func (m *Manager) btAdminAPI(mux *stdhttp.ServeMux, p string) {
	mux.HandleFunc("GET "+p+"/bittorrent", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		ch := make(chan BTView, 1)
		m.BitTorrent(func(v BTView) { ch <- v })
		apiJSON(w, 200, <-ch)
	})
	mux.HandleFunc("GET "+p+"/distributions", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) { apiJSON(w, 200, m.distsBody()) })
	mux.HandleFunc("POST "+p+"/distributions", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req DistRequest
		if !apiDecode(w, r, &req) {
			return
		}
		d, err := m.StartDist(AdminAPIUser, &req)
		if err != nil {
			apiError(w, err)
			return
		}
		apiJSON(w, 202, d)
	})
	mux.HandleFunc("GET "+p+"/distributions/{id}", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		d, ok := m.DistByID(r.PathValue("id"))
		if !ok {
			apiError(w, errNoDist(r.PathValue("id")))
			return
		}
		apiJSON(w, 200, d)
	})
	mux.HandleFunc("POST "+p+"/distributions/{id}/cancel", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		d, err := m.CancelDist(AdminAPIUser, r.PathValue("id"))
		if err != nil {
			apiError(w, err)
			return
		}
		apiJSON(w, 200, d)
	})
}

// assistantDists bounds the distributions cluster_bittorrent lists.
const assistantDists = 10

// BitTorrent answers cluster_bittorrent (I206): the members' BitTorrent
// roles as the view reads them, of nodes, a group's members or every
// member, with their sums, and the latest distributions with each
// target's progress. It waits for the view until ctx ends: the console
// pool's.
func (a *AssistantAPI) BitTorrent(ctx context.Context, user, group string, nodes []string) ([]byte, error) {
	names, err := a.targets(user, group, nodes)
	if err != nil {
		return nil, err
	}
	ch := make(chan BTView, 1)
	a.m.BitTorrent(func(v BTView) { ch <- v })
	var v BTView
	select {
	case v = <-ch:
	case <-ctx.Done():
		return nil, fmt.Errorf("the members' BitTorrent roles were not read in time: %w", ctx.Err())
	}
	keep := v.Members[:0]
	for _, b := range v.Members {
		if slices.Contains(names, b.Name) {
			keep = append(keep, b)
		}
	}
	read := sumView(keep, v.Read)
	read.Distributions = v.Distributions
	dists := a.m.Dists()
	if len(dists) > assistantDists {
		dists = dists[:assistantDists]
	}
	return json.Marshal(map[string]any{"view": read, "distributions": dists,
		"note": "Distributions are started on the Cluster → BitTorrent page by a person; the assistant only reads them."})
}

// sumView sums members read at at again, as the view does, without the
// distributions' counts.
func sumView(members []BTMember, at time.Time) BTView {
	v := BTView{Read: at, Members: members, Totals: BTTotals{States: map[string]int{}}}
	for _, b := range members {
		t := &v.Totals
		t.Members++
		s := b.Summary
		if s == nil {
			continue
		}
		t.Read++
		addSummary(t, s)
	}
	return v
}

// addSummary adds s to t.
func addSummary(t *BTTotals, s *BTSummary) {
	if c := s.Client; c != nil {
		t.Clients++
		if c.Running {
			t.Running++
		}
		t.Torrents += c.Torrents
		for k, n := range c.States {
			t.States[k] += n
		}
		t.DownloadRate += c.DownloadRate
		t.UploadRate += c.UploadRate
		t.Downloaded += c.Downloaded
		t.Uploaded += c.Uploaded
		t.Peers += c.Peers
		t.Errors += c.Errors
		for _, p := range c.Publish {
			t.Published += p.Torrents
		}
	}
	if tr := s.Tracker; tr != nil {
		t.Trackers++
		t.TrackerTorrents += tr.Torrents
		t.TrackerPeers += tr.Peers
		t.Announces += tr.Announces
	}
}
