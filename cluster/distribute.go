package cluster

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app/console"
)

// Distributions (I206): a file or a directory one member publishes
// (bittorrent.client.publish, I202) spread to a group of members over
// BitTorrent. The source's publish entry must be private (BEP 27), so that
// the torrent is never announced on the DHT, exchanged with peers or sent
// to a tracker it does not name; the manager reads the entry's torrent
// from the source, its .torrent file through the source's Admin API, and
// tells each target to add it (POST /bittorrent/torrents) into a category
// of its own config (its directory) or its download_dir, with the source
// and the other targets as direct peers, the addresses their clients take
// peers on. It then follows each target's progress until every one has the
// torrent whole, failed, or the distribution is cancelled, and at the end
// removes the torrent from the targets it added it on, keeping their files
// (with keep_seeding, those that have it whole keep seeding it); a target
// that failed or was cancelled before it had it whole has its partial
// files removed with it.
//
// A distribution is a resource of its own beside operations, kept in
// memory as they are (the last MaxKeptDists, with each target's outcome)
// and in the audit log when it ends: it changes no config, so it holds no
// member against rollouts and operations; a member is the target of one
// distribution at a time. Its goroutine waits only for its calls to
// members, each bounded by the calls' timeout, never on a fib worker.

// The states of a distribution, Dist.State.
const (
	DistPreparing = "preparing"
	DistRunning   = "running"
	DistDone      = "done"
	DistFailed    = "failed"
	DistCancelled = "cancelled"
)

// The statuses of a distribution's target, DistTarget.Status.
const (
	TargetPending     = "pending"
	TargetAdding      = "adding"
	TargetDownloading = "downloading"
	TargetDone        = "done"
	TargetFailed      = "failed"
	TargetCancelled   = "cancelled"
)

// The kinds of Problem only distributions report.
const (
	// KindNoClient is a member that runs no bittorrent.client, or whose
	// client does not run.
	KindNoClient = "no_client"
	// KindStalled is a target whose download made no progress for the
	// distribution's stall_timeout.
	KindStalled = "stalled"
	// KindNotPrivate is a source's torrent that is not private.
	KindNotPrivate = "not_private"
)

// The bounds of distributions.
const (
	// MaxDistTargets is how many members one distribution spreads to.
	MaxDistTargets = 64
	// MaxRunningDists is how many distributions run at once; one more is
	// answered 429.
	MaxRunningDists = 2
	// MaxKeptDists is how many distributions the manager keeps, the
	// oldest ended dropped first.
	MaxKeptDists = 16
	// MaxDistBytes bounds the size of what a distribution spreads.
	MaxDistBytes = 1 << 40
	// DefaultDistStall is how long a target's download may make no
	// progress before it fails; MinDistStall and MaxDistStall bound
	// stall_timeout_ms.
	DefaultDistStall = 10 * time.Minute
	MinDistStall     = time.Second
	MaxDistStall     = 24 * time.Hour
	// maxPeerHost bounds a peer_hosts value.
	maxPeerHost = 255
	// maxDistDescription bounds a distribution's description.
	maxDistDescription = 256
)

// distPoll is how often a distribution reads its targets' progress; a
// variable for tests.
var distPoll = 2 * time.Second

// DistRequest is a distribution as POST /api/distributions takes it.
type DistRequest struct {
	// Source is the member that publishes what is spread, Publish its
	// publish entry, and Torrent the entry's torrent: its ID, its info
	// hash, or its file's path (mode per_file); left empty for an entry
	// of one torrent (mode whole_dir).
	Source  string `json:"source"`
	Publish string `json:"publish"`
	Torrent string `json:"torrent,omitempty"`
	// The targets: the group's members, Members, or Members of the group,
	// as an operation's; the source is never one of them.
	Group   string   `json:"group,omitempty"`
	Members []string `json:"members,omitempty"`
	// Category is the targets' category the torrent is added in, which
	// decides its directory; download_dir when empty.
	Category string `json:"category,omitempty"`
	// KeepSeeding leaves the torrent on the targets that have it whole
	// when the distribution ends, seeding by their limits.
	KeepSeeding bool `json:"keep_seeding,omitempty"`
	// PeerHosts are the addresses other members reach a member's client
	// at, by the member's name: a host, whose port is its client's, or
	// host:port. A member not named here is reached at the address its
	// client listens on, or, when that is every address, at its URL's
	// host.
	PeerHosts map[string]string `json:"peer_hosts,omitempty"`
	// StallTimeoutMs is how long a target's download may make no progress
	// before it fails; DefaultDistStall when 0.
	StallTimeoutMs int64  `json:"stall_timeout_ms,omitempty"`
	Description    string `json:"description,omitempty"`
}

// DistTorrent is the torrent a distribution spreads.
type DistTorrent struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Path       string `json:"path,omitempty"`
	InfoHash   string `json:"info_hash,omitempty"`
	InfoHashV2 string `json:"info_hash_v2,omitempty"`
	Size       int64  `json:"size"`
	Files      int    `json:"files"`
	Trackers   int    `json:"trackers"`
}

// DistTarget is a distribution's target and how it goes.
type DistTarget struct {
	Node   string `json:"node"`
	Status string `json:"status"`
	// Peer is the address the other members are given for its client,
	// "" when none is known.
	Peer string `json:"peer,omitempty"`
	// Adopted is a target that had the torrent before: it is followed,
	// and never removed from it.
	Adopted bool `json:"adopted,omitempty"`
	// What its client reported at the last read: the torrent's state,
	// progress (0 to 1), the bytes it has of those it wants, its download
	// rate in bytes a second and its peers.
	State        string   `json:"state,omitempty"`
	Progress     float64  `json:"progress"`
	Completed    int64    `json:"completed"`
	Wanted       int64    `json:"wanted"`
	DownloadRate float64  `json:"download_rate"`
	Peers        int      `json:"peers"`
	Error        *Problem `json:"error,omitempty"`
	// Removed says the torrent was removed from it when the distribution
	// ended, with its files when it did not have it whole.
	Removed bool      `json:"removed,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	TookMs  int64     `json:"took_ms,omitempty"`

	// lastAt is when Completed last grew, for the stall timeout.
	lastAt time.Time
}

// DistCounts counts a distribution's targets by status.
type DistCounts struct {
	Pending   int `json:"pending"`
	Active    int `json:"active"`
	Done      int `json:"done"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// Dist is a distribution.
type Dist struct {
	ID          string       `json:"id"`
	User        string       `json:"user"`
	Description string       `json:"description,omitempty"`
	Source      string       `json:"source"`
	Publish     string       `json:"publish"`
	Torrent     *DistTorrent `json:"torrent,omitempty"`
	// SourcePeer is the address the targets are given for the source.
	SourcePeer     string `json:"source_peer,omitempty"`
	Group          string `json:"group,omitempty"`
	Category       string `json:"category,omitempty"`
	KeepSeeding    bool   `json:"keep_seeding,omitempty"`
	StallTimeoutMs int64  `json:"stall_timeout_ms"`
	// State is DistPreparing, DistRunning, DistDone, DistFailed or
	// DistCancelled, and Reason why it failed or who cancelled it; Error
	// the problem that failed it.
	State    string       `json:"state"`
	Reason   string       `json:"reason,omitempty"`
	Error    *Problem     `json:"error,omitempty"`
	Created  time.Time    `json:"created"`
	Ended    time.Time    `json:"ended,omitzero"`
	Finished bool         `json:"finished"`
	Counts   DistCounts   `json:"counts"`
	Targets  []DistTarget `json:"targets"`

	// The request's, kept for the run: the torrent asked for and the
	// members' peer hosts.
	want      string
	peerHosts map[string]string
	// cancel is closed by Cancel; cancelledBy who did.
	cancel      chan struct{}
	cancelledBy string
}

// distState is the distributions the manager keeps, under m.mu.
type distState struct {
	list    []*Dist           // oldest first
	busy    map[string]string // a target's name → the ID of its distribution
	running int
	wg      sync.WaitGroup
}

// errNoDist is a distribution there is none of: 404.
type errNoDist string

func (e errNoDist) Error() string { return "no distribution " + strconv.Quote(string(e)) + " is kept" }

// check checks req, but for what needs the manager's state.
func (req *DistRequest) check() error {
	switch {
	case req.Source == "":
		return fieldErr("source", "name the member that publishes what is spread")
	case req.Publish == "" || len(req.Publish) > MaxNameBytes || !printable(req.Publish) || strings.ContainsAny(req.Publish, "/?#%"):
		return fieldErr("publish", "name the source's publish entry: at most %d characters, no '/'", MaxNameBytes)
	case len(req.Torrent) > 1024 || !printable(req.Torrent):
		return fieldErr("torrent", "the torrent is its ID, its info hash or its file's path, at most 1024 characters")
	case len(req.Category) > MaxNameBytes || !printable(req.Category):
		return fieldErr("category", "the category is a name of at most %d characters", MaxNameBytes)
	case len(req.Description) > maxDistDescription || !printable(req.Description):
		return fieldErr("description", "the description is at most %d characters", maxDistDescription)
	case req.StallTimeoutMs != 0 && (req.StallTimeoutMs < MinDistStall.Milliseconds() || req.StallTimeoutMs > MaxDistStall.Milliseconds()):
		return fieldErr("stall_timeout_ms", "stall_timeout_ms is %d to %d", MinDistStall.Milliseconds(), MaxDistStall.Milliseconds())
	case len(req.PeerHosts) > MaxDistTargets+1:
		return fieldErr("peer_hosts", "peer_hosts names %d members at most", MaxDistTargets+1)
	}
	for name, h := range req.PeerHosts {
		if h == "" || len(h) > maxPeerHost || !printable(h) || strings.ContainsAny(h, " /?#@") {
			return fieldErr("peer_hosts", "%q: a host, or host:port, of at most %d characters", name, maxPeerHost)
		}
		if host, port, err := net.SplitHostPort(h); err == nil {
			if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || host == "" {
				return fieldErr("peer_hosts", "%q: %q is not host:port", name, h)
			}
		}
	}
	return nil
}

// StartDist starts the distribution req as user, and returns it as it
// starts: preparing, every target pending. user needs operator on the
// source and on every target.
func (m *Manager) StartDist(user string, req *DistRequest) (Dist, error) {
	if err := req.check(); err != nil {
		return Dist{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.closed:
		return Dist{}, errClosed
	case m.readOnly != "":
		return Dist{}, errReadOnly(m.readOnly)
	}
	if index(&m.doc, req.Source) < 0 {
		return Dist{}, errNotFound(req.Source)
	}
	names, err := m.targetsLocked(&OpRequest{Group: req.Group, Members: req.Members})
	if err != nil {
		return Dist{}, err
	}
	names = slices.DeleteFunc(names, func(n string) bool { return n == req.Source })
	switch {
	case len(names) == 0:
		return Dist{}, fieldErr("members", "the source is the only member named: name the members to spread to")
	case len(names) > MaxDistTargets:
		return Dist{}, fieldErr("members", "%d members, more than the %d a distribution spreads to", len(names), MaxDistTargets)
	}
	for name := range req.PeerHosts {
		if name != req.Source && !slices.Contains(names, name) {
			return Dist{}, fieldErr("peer_hosts", "%q is neither the source nor a target", name)
		}
	}
	if err := m.allowLocked(user, Need{Role: RoleOperator, Nodes: append([]string{req.Source}, names...)}); err != nil {
		return Dist{}, err
	}
	if m.dist.busy == nil {
		m.dist.busy = map[string]string{}
	}
	var taken []string
	for _, name := range names {
		if id := m.dist.busy[name]; id != "" {
			taken = append(taken, name+" ("+id+")")
		}
	}
	if len(taken) > 0 {
		return Dist{}, errConflict("a distribution spreads to " + strings.Join(taken, ", ") + " already: wait for it to end, or cancel it")
	}
	if m.dist.running >= MaxRunningDists {
		return Dist{}, errBusy(fmt.Sprintf("%d distributions run already; try again once one ends", MaxRunningDists))
	}
	stall := req.StallTimeoutMs
	if stall == 0 {
		stall = DefaultDistStall.Milliseconds()
	}
	d := &Dist{ID: newOpID(), User: user, Description: req.Description, Source: req.Source, Publish: req.Publish, Group: req.Group,
		Category: req.Category, KeepSeeding: req.KeepSeeding, StallTimeoutMs: stall, State: DistPreparing, Created: m.now().UTC(),
		Targets: make([]DistTarget, len(names)), want: req.Torrent, peerHosts: req.PeerHosts, cancel: make(chan struct{})}
	for i, name := range names {
		d.Targets[i] = DistTarget{Node: name, Status: TargetPending}
		m.dist.busy[name] = d.ID
	}
	d.countLocked()
	m.dist.running++
	m.dist.list = append(m.dist.list, d)
	m.trimDistsLocked()
	m.dist.wg.Add(1)
	out := d.copy()
	go m.runDist(d)
	m.logf("cluster: %q started the distribution %s of %s's %s to %s", user, d.ID, d.Source, d.Publish, strings.Join(names, ", "))
	return out, nil
}

// countLocked counts d's targets. m.mu is held.
func (d *Dist) countLocked() {
	c := DistCounts{}
	for _, t := range d.Targets {
		switch t.Status {
		case TargetPending:
			c.Pending++
		case TargetAdding, TargetDownloading:
			c.Active++
		case TargetDone:
			c.Done++
		case TargetCancelled:
			c.Cancelled++
		default:
			c.Failed++
		}
	}
	d.Counts = c
}

// copy returns a copy of d that shares nothing that changes with it.
func (d *Dist) copy() Dist {
	out := *d
	out.Targets = slices.Clone(d.Targets)
	if d.Torrent != nil {
		t := *d.Torrent
		out.Torrent = &t
	}
	return out
}

// trimDistsLocked drops the oldest ended distributions past MaxKeptDists.
// m.mu is held.
func (m *Manager) trimDistsLocked() {
	for len(m.dist.list) > MaxKeptDists {
		i := slices.IndexFunc(m.dist.list, func(d *Dist) bool { return d.Finished })
		if i < 0 {
			return
		}
		m.dist.list = slices.Delete(m.dist.list, i, i+1)
	}
}

// Dists returns the distributions kept, the latest first.
func (m *Manager) Dists() []Dist {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Dist, 0, len(m.dist.list))
	for i := len(m.dist.list) - 1; i >= 0; i-- {
		out = append(out, m.dist.list[i].copy())
	}
	return out
}

// DistByID returns the distribution id, while it is kept.
func (m *Manager) DistByID(id string) (Dist, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.distLocked(id); d != nil {
		return d.copy(), true
	}
	return Dist{}, false
}

func (m *Manager) distLocked(id string) *Dist {
	for _, d := range m.dist.list {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// distCounts counts the distributions kept by state.
func (m *Manager) distCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	for _, d := range m.dist.list {
		out[d.State]++
	}
	return out
}

// CancelDist cancels the distribution id as user, who needs operator on
// its source and targets: its targets still downloading stop, and the
// torrent is removed from them with what they downloaded.
func (m *Manager) CancelDist(user, id string) (Dist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.distLocked(id)
	if d == nil {
		return Dist{}, errNoDist(id)
	}
	nodes := []string{d.Source}
	for _, t := range d.Targets {
		nodes = append(nodes, t.Node)
	}
	if err := m.allowLocked(user, Need{Role: RoleOperator, Nodes: nodes}); err != nil {
		return Dist{}, err
	}
	if d.Finished {
		return Dist{}, errConflict("the distribution " + id + " has ended already")
	}
	if d.cancelledBy == "" {
		d.cancelledBy = user
		close(d.cancel)
		m.logf("cluster: %q cancelled the distribution %s", user, id)
	}
	return d.copy(), nil
}

// update runs fn on d under m.mu, and counts its targets again.
func (m *Manager) update(d *Dist, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn()
	d.countLocked()
}

// await is Manager.call, waited for: the distribution's goroutine's.
func (m *Manager) await(name, method, path string, body []byte, v any) (*Reply, *Problem) {
	type res struct {
		r *Reply
		p *Problem
	}
	ch := make(chan res, 1)
	m.call(name, method, path, nil, body, v, func(r *Reply, p *Problem) {
		if p != nil && p.At.IsZero() {
			p.At = m.now().UTC()
		}
		ch <- res{r, p}
	})
	x := <-ch
	return x.r, x.p
}

// cancelled reports whether d was cancelled or the manager stops.
func (m *Manager) cancelled(d *Dist) bool {
	select {
	case <-d.cancel:
		return true
	case <-m.stop:
		return true
	default:
		return false
	}
}

// publishAnswer is a member's GET /bittorrent/publish/{name}.
type publishAnswer struct {
	Name     string `json:"name"`
	Torrents []struct {
		ID         string `json:"id"`
		Path       string `json:"path"`
		Name       string `json:"name"`
		InfoHash   string `json:"info_hash"`
		InfoHashV2 string `json:"info_hash_v2"`
		State      string `json:"state"`
		Error      string `json:"error"`
	} `json:"torrents"`
	Error string `json:"error"`
}

// distProblem is a Problem of the distribution's own.
func (m *Manager) distProblem(kind, format string, args ...any) *Problem {
	return &Problem{Kind: kind, At: m.now().UTC(), Message: clip(fmt.Sprintf(format, args...), maxMessage)}
}

// prepare reads the source: its client, its publish entry's torrent and
// the torrent's .torrent file, which must be private. It returns the file
// and the torrent's ID, or why the distribution cannot go on.
func (m *Manager) prepare(d *Dist) ([]byte, string, string, *Problem) {
	s, prob := m.awaitSummary(d.Source)
	if prob != nil {
		return nil, "", "", prob
	}
	if s.Client == nil || !s.Client.Running {
		return nil, "", "", m.distProblem(KindNoClient, "the source runs no bittorrent.client")
	}
	peer, err := m.peerAddr(d.Source, s.Client.Listen, d.peerHosts[d.Source])
	if err != nil {
		return nil, "", "", m.distProblem(KindNoClient, "the source's client cannot be reached by the targets: %v", err)
	}
	var pa publishAnswer
	path := "/bittorrent/publish/" + url.PathEscape(d.Publish)
	if _, prob := m.await(d.Source, "GET", path, nil, &pa); prob != nil {
		if prob.Status == 404 {
			prob.Message = "the source has no publish entry " + strconv.Quote(d.Publish)
		}
		return nil, "", "", prob
	}
	if pa.Error != "" && len(pa.Torrents) == 0 {
		return nil, "", "", m.distProblem(KindRefused, "the source's publish entry %q publishes nothing: %s", d.Publish, pa.Error)
	}
	idx := -1
	want := strings.ToLower(strings.TrimSpace(d.want))
	for i, t := range pa.Torrents {
		if want == "" && len(pa.Torrents) == 1 || want != "" && (want == t.ID || want == t.InfoHash || want == t.InfoHashV2 ||
			d.want == t.Path || d.want == t.Name) {
			idx = i
			break
		}
	}
	switch {
	case idx < 0 && want == "":
		return nil, "", "", m.distProblem(KindRefused, "the source's publish entry %q has %d torrents: name one by its ID or its file's path", d.Publish, len(pa.Torrents))
	case idx < 0:
		return nil, "", "", m.distProblem(KindRefused, "the source's publish entry %q has no torrent %q", d.Publish, d.want)
	}
	pt := pa.Torrents[idx]
	if pt.State != "seeding" {
		why := pt.State
		if pt.Error != "" {
			why += ": " + pt.Error
		}
		return nil, "", "", m.distProblem(KindRefused, "the source does not seed the torrent %s (%s)", pt.ID, clip(why, 256))
	}
	r, prob := m.await(d.Source, "GET", path+"/torrents/"+url.PathEscape(pt.ID), nil, nil)
	if prob != nil {
		return nil, "", "", prob
	}
	mi, err := metainfo.Parse(r.Body)
	if err != nil {
		return nil, "", "", m.distProblem(KindBadAnswer, "the source's .torrent file: %v", err)
	}
	if !mi.Info.Private {
		return nil, "", "", m.distProblem(KindNotPrivate, "the publish entry %q is not private: set private on it, so that the torrent is never announced to the DHT or exchanged with peers", d.Publish)
	}
	size := int64(0)
	files := mi.Info.FileList()
	for _, f := range files {
		size += f.Length
	}
	if size > MaxDistBytes {
		return nil, "", "", m.distProblem(KindRefused, "the torrent is %d bytes, more than the %d a distribution spreads", size, int64(MaxDistBytes))
	}
	trackers := 0
	for _, tier := range mi.AnnounceList {
		trackers += len(tier)
	}
	if trackers == 0 && mi.Announce != "" {
		trackers = 1
	}
	m.update(d, func() {
		d.SourcePeer = peer
		d.Torrent = &DistTorrent{ID: pt.ID, Name: clip(mi.Info.Name, 256), Path: clip(pt.Path, 1024), InfoHash: pt.InfoHash, InfoHashV2: pt.InfoHashV2,
			Size: size, Files: len(files), Trackers: trackers}
	})
	return r.Body, pt.ID, peer, nil
}

// awaitSummary reads the member name's BitTorrent summary, waited for.
func (m *Manager) awaitSummary(name string) (*BTSummary, *Problem) {
	type res struct {
		s *BTSummary
		p *Problem
	}
	ch := make(chan res, 1)
	m.btSummary(name, func(s *BTSummary, p *Problem) { ch <- res{s, p} })
	x := <-ch
	return x.s, x.p
}

// peerAddr returns the address other members reach the member name's
// client at: host, a host or host:port, when given; else listen, the
// address its client listens on, unless that is every address, whose port
// goes with its URL's host.
func (m *Manager) peerAddr(name, listen, host string) (string, error) {
	ap, err := netip.ParseAddrPort(listen)
	if listen != "" && err != nil {
		return "", fmt.Errorf("its client's address %q is not an address", listen)
	}
	if host != "" {
		if _, _, err := net.SplitHostPort(host); err == nil {
			return host, nil
		}
		if !ap.IsValid() {
			return "", fmt.Errorf("its client's port is not known")
		}
		return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(int(ap.Port()))), nil
	}
	if !ap.IsValid() {
		return "", fmt.Errorf("its client's address is not known")
	}
	if !ap.Addr().IsUnspecified() {
		return ap.String(), nil
	}
	m.mu.Lock()
	n := m.nodes[name]
	m.mu.Unlock()
	if n == nil {
		return "", errNotFound(name)
	}
	u, err := url.Parse(n.spec.URL)
	if n.spec.URL == "" || err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("its client listens on every address and the manager knows no host of it (a linked member): give it in peer_hosts")
	}
	return net.JoinHostPort(u.Hostname(), strconv.Itoa(int(ap.Port()))), nil
}

// fail ends d before any target started, as failed with prob.
func (m *Manager) failDist(d *Dist, prob *Problem) {
	m.update(d, func() {
		d.Error = prob
		d.Reason = prob.Message
		for i := range d.Targets {
			if d.Targets[i].Status == TargetPending {
				d.Targets[i].Status, d.Targets[i].Error = TargetFailed, &Problem{Kind: prob.Kind, At: prob.At, Message: "not started: " + prob.Message}
			}
		}
	})
}

// runDist runs d: it prepares, adds the torrent on the targets, follows
// them, cleans up and records d in the audit log. It is a goroutine of its
// own: it waits only for its calls to members.
func (m *Manager) runDist(d *Dist) {
	defer m.dist.wg.Done()
	defer m.endDist(d)
	raw, id, srcPeer, prob := m.prepare(d)
	if m.cancelled(d) {
		return
	}
	if prob != nil {
		m.failDist(d, prob)
		return
	}
	// The targets' clients, and the addresses they take peers on.
	m.eachTarget(d, func(i int, t DistTarget) {
		s, prob := m.awaitSummary(t.Node)
		switch {
		case prob != nil:
		case s.Client == nil || !s.Client.Running:
			prob = m.distProblem(KindNoClient, "the member runs no bittorrent.client")
		}
		peer := ""
		if prob == nil {
			peer, _ = m.peerAddr(t.Node, s.Client.Listen, d.peerHosts[t.Node])
		}
		m.update(d, func() {
			x := &d.Targets[i]
			x.Peer = peer
			if prob != nil {
				x.Status, x.Error, x.Ended = TargetFailed, prob, m.now().UTC()
			}
		})
	})
	if m.cancelled(d) {
		return
	}
	m.update(d, func() { d.State = DistRunning })
	// Each target adds the torrent, with the source and the other targets
	// as its peers.
	m.mu.Lock()
	var peers []string
	for _, t := range d.Targets {
		if t.Status == TargetPending && t.Peer != "" {
			peers = append(peers, t.Peer)
		}
	}
	m.mu.Unlock()
	m.eachTarget(d, func(i int, t DistTarget) {
		if t.Status != TargetPending || m.cancelled(d) {
			return
		}
		m.update(d, func() { d.Targets[i].Status, d.Targets[i].Started = TargetAdding, m.now().UTC() })
		them := []string{srcPeer}
		for _, p := range peers {
			if p != t.Peer {
				them = append(them, p)
			}
		}
		body, _ := json.Marshal(map[string]any{"torrent": raw, "category": d.Category, "peers": them})
		_, prob := m.await(t.Node, "POST", "/bittorrent/torrents", body, nil)
		adopted := false
		if prob != nil && prob.Status == 409 {
			// It has the torrent already: followed, never removed.
			prob, adopted = nil, true
		}
		m.update(d, func() {
			x := &d.Targets[i]
			x.lastAt = m.now()
			if prob != nil {
				x.Status, x.Error, x.Ended = TargetFailed, prob, m.now().UTC()
				return
			}
			x.Status, x.Adopted = TargetDownloading, adopted
		})
	})
	// Follow the targets until each is done or failed.
	stall := time.Duration(d.StallTimeoutMs) * time.Millisecond
	tick := time.NewTicker(distPoll)
	defer tick.Stop()
	for !m.cancelled(d) {
		m.eachTarget(d, func(i int, t DistTarget) {
			if t.Status != TargetDownloading {
				return
			}
			var st struct {
				State        string  `json:"state"`
				Error        string  `json:"error"`
				Complete     bool    `json:"complete"`
				Progress     float64 `json:"progress"`
				Completed    int64   `json:"completed"`
				Wanted       int64   `json:"wanted"`
				DownloadRate float64 `json:"download_rate"`
				Peers        int     `json:"peers"`
			}
			_, prob := m.await(t.Node, "GET", "/bittorrent/torrents/"+id, nil, &st)
			now := m.now()
			m.update(d, func() {
				x := &d.Targets[i]
				switch {
				case prob != nil && prob.Status == 404:
					x.Status, x.Error, x.Ended = TargetFailed, m.distProblem(KindRefused, "the torrent was removed from the member"), now.UTC()
					return
				case prob != nil:
					// A read that failed is tried again, until the stall
					// timeout.
					x.Error = prob
				default:
					x.Error = nil
					if st.Completed > x.Completed {
						x.lastAt = now
					}
					x.State, x.Progress, x.Completed, x.Wanted, x.DownloadRate, x.Peers = st.State, st.Progress, st.Completed, st.Wanted, st.DownloadRate, st.Peers
					switch {
					case st.Complete || st.State == "seeding":
						x.Status, x.Progress, x.Ended = TargetDone, 1, now.UTC()
						x.TookMs = x.Ended.Sub(x.Started).Milliseconds()
						return
					case st.State == "error":
						x.Status, x.Error, x.Ended = TargetFailed, m.distProblem(KindRefused, "the member's client: %s", clip(st.Error, 256)), now.UTC()
						return
					}
				}
				if now.Sub(x.lastAt) > stall {
					x.Status, x.Ended = TargetFailed, now.UTC()
					x.Error = m.distProblem(KindStalled, "no progress for %s", stall)
				}
			})
		})
		m.mu.Lock()
		active := d.Counts.Active > 0
		m.mu.Unlock()
		if !active {
			return
		}
		select {
		case <-tick.C:
		case <-d.cancel:
		case <-m.stop:
		}
	}
}

// eachTarget runs fn on each of d's targets, at most max_concurrency at
// once, each with its target as it is now, and waits for them.
func (m *Manager) eachTarget(d *Dist, fn func(i int, t DistTarget)) {
	sem := make(chan struct{}, m.o.Settings.Concurrency())
	var wg sync.WaitGroup
	for i := range d.Targets {
		select {
		case sem <- struct{}{}:
		case <-m.stop:
			wg.Wait()
			return
		}
		m.mu.Lock()
		t := d.Targets[i]
		m.mu.Unlock()
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			fn(i, t)
		}()
	}
	wg.Wait()
}

// endDist ends d: it removes the torrent from the targets it added it on
// (but those that keep seeding), marks the rest cancelled or failed,
// frees the targets, and records d in the audit log.
func (m *Manager) endDist(d *Dist) {
	stopped := false
	select {
	case <-m.stop:
		stopped = true
	default:
	}
	cancelled := !stopped && m.cancelled(d)
	m.update(d, func() {
		for i := range d.Targets {
			x := &d.Targets[i]
			if x.Status == TargetPending || x.Status == TargetAdding || x.Status == TargetDownloading {
				switch {
				case cancelled:
					x.Status = TargetCancelled
				default:
					x.Status, x.Error = TargetFailed, m.distProblem(KindUnreachable, "the manager stopped before the distribution ended")
				}
				x.Ended = m.now().UTC()
			}
		}
	})
	if d.Torrent != nil && !stopped {
		m.eachTarget(d, func(i int, t DistTarget) {
			if t.Adopted || t.Started.IsZero() || t.Status == TargetDone && d.KeepSeeding {
				return
			}
			path := "/bittorrent/torrents/" + d.Torrent.ID
			if t.Status != TargetDone {
				path += "?delete_data=1"
			}
			_, prob := m.await(t.Node, "DELETE", path, nil, nil)
			m.update(d, func() {
				x := &d.Targets[i]
				if prob == nil || prob.Status == 404 {
					x.Removed = prob == nil
				} else if x.Error == nil {
					x.Error = prob
				}
			})
		})
	}
	m.mu.Lock()
	d.Ended, d.Finished = m.now().UTC(), true
	switch {
	case cancelled:
		d.State, d.Reason = DistCancelled, "cancelled by "+d.cancelledBy
	case d.Error != nil || stopped:
		d.State = DistFailed
		if stopped && d.Reason == "" {
			d.Reason = "the manager stopped"
		}
	case d.Counts.Done == len(d.Targets):
		d.State = DistDone
	default:
		d.State = DistFailed
		d.Reason = fmt.Sprintf("%d of %d targets failed", d.Counts.Failed, len(d.Targets))
	}
	for _, t := range d.Targets {
		if m.dist.busy[t.Node] == d.ID {
			delete(m.dist.busy, t.Node)
		}
	}
	m.dist.running--
	m.trimDistsLocked()
	entry := d.audit()
	user, state, counts := d.User, d.State, d.Counts
	m.mu.Unlock()
	m.logf("cluster: the distribution %s ended %s: %d done, %d failed, %d cancelled", d.ID, state, counts.Done, counts.Failed, counts.Cancelled)
	if stopped {
		return
	}
	if err := m.change(user, func(*document) (AuditEntry, error) { return entry, nil }); err != nil {
		m.logf("cluster: the distribution %s is not in the audit log: %v", d.ID, err)
	}
}

// audit returns d's audit entry. m.mu is held.
func (d *Dist) audit() AuditEntry {
	what := d.Publish
	if d.Torrent != nil {
		what += "/" + d.Torrent.Name
	}
	e := AuditEntry{Action: ActionDistribute, Node: d.Source, Group: d.Group, Op: d.ID,
		Detail: clip(fmt.Sprintf("%s of %s to %d members: %s; %d done, %d failed, %d cancelled", what, d.Source, len(d.Targets), d.State,
			d.Counts.Done, d.Counts.Failed, d.Counts.Cancelled), 512)}
	if d.Reason != "" {
		e.Detail = clip(e.Detail+"; "+d.Reason, 768)
	}
	for _, t := range d.Targets {
		a := AuditOutcome{Node: t.Node, Status: t.Status}
		if t.Removed {
			a.Detail = "torrent removed"
		}
		if t.Error != nil {
			a.Error = clip(t.Error.Kind+": "+t.Error.Message, maxAuditError)
		}
		e.Outcomes = append(e.Outcomes, a)
	}
	e.Outcome = opOutcome(e.Outcomes, func(s string) bool { return s == TargetDone })
	return e
}

// ActionDistribute is a distribution's audit entry.
const ActionDistribute = "bt.distribute"

// waitDists waits for the distributions under way to end, at most wait.
func (m *Manager) waitDists(wait time.Duration) {
	ended := make(chan struct{})
	go func() { m.dist.wg.Wait(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(wait):
	}
}

// distsBody is GET /api/distributions's answer.
type distsBody struct {
	MaxRunning int    `json:"max_running"`
	MaxTargets int    `json:"max_targets"`
	ReadOnly   string `json:"read_only,omitempty"`
	Dists      []Dist `json:"distributions"`
}

func (m *Manager) distsBody() distsBody {
	m.mu.Lock()
	ro := m.readOnly
	m.mu.Unlock()
	return distsBody{MaxRunning: MaxRunningDists, MaxTargets: MaxDistTargets, ReadOnly: ro, Dists: m.Dists()}
}

// distRoutes adds the distributions' endpoints: GET and POST
// distributions, GET distributions/<id> and POST distributions/<id>/cancel.
func (m *Manager) distRoutes(api *router) {
	api.Handle("GET", "/api/distributions", func(r *console.Request) { _ = r.JSON(200, m.distsBody()) })
	api.Checked("POST", "/api/distributions", func(r *console.Request) {
		var req DistRequest
		if len(r.Context.Body()) > MaxBodyBytes {
			_ = console.Error(r.Context, 413, fmt.Sprintf("a distribution is %d bytes at most", MaxBodyBytes))
			return
		}
		if !r.Decode(&req) {
			return
		}
		d, err := m.StartDist(r.User, &req)
		if err == errClosed {
			_ = console.Error(r.Context, 503, err.Error())
			return
		}
		m.answer(r, 202, d, err)
	})
	api.HandlePrefix("GET", "/api/distributions/", func(r *console.Request) {
		id := strings.TrimPrefix(r.Path, "/api/distributions/")
		d, ok := m.DistByID(id)
		if !ok {
			_ = console.Error(r.Context, 404, errNoDist(id).Error())
			return
		}
		_ = r.JSON(200, d)
	})
	api.CheckedPrefix("POST", "/api/distributions/", func(r *console.Request) {
		id, ok := strings.CutSuffix(strings.TrimPrefix(r.Path, "/api/distributions/"), "/cancel")
		if !ok || id == "" || strings.Contains(id, "/") {
			_ = console.Error(r.Context, 404, "POST /api/distributions/<id>/cancel cancels a distribution")
			return
		}
		d, err := m.CancelDist(r.User, id)
		m.answer(r, 200, d, err)
	})
}
