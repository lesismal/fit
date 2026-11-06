// Package channels is the streaming role's channels and push targets
// (decision 032, I134): a channel plays items of a media library into the
// role's hub as a live stream, in order, shuffled, looped or on a schedule;
// a push target relays a stream of the hub, a channel or any other, to an
// outside RTMP or SRT server, reconnecting with backoff. See
// docs/channels.md.
//
// A channel never blocks a fib worker, nor any goroutine it does not own.
// Its item's sample tables are read by the library (medialib.Library.Table),
// and its samples on the library's file pool (medialib.Library.Submit), up
// to read_ahead and max_buffer_bytes ahead of what is sent. What was read
// is sent into the hub from the channel's timer (Clock.AfterFunc), each
// packet when its decode time is due, so that the stream runs at the pace
// of the samples' timestamps. The next item is loaded and read while the
// one before it plays, and its timestamps carry on from where that one
// ends, so that players see no gap and no jump; its tracks are set anew
// (hub.Publisher.SetTracks), which HLS marks with a discontinuity.
//
// A push target dials from the role's engine, from a timer, and hears the
// outcome in fib's callbacks, which only arm its next timer.
//
// The role makes a Manager for its hub and moves it to every config
// applied: channels and push targets are changed in place, a new list or
// schedule taking effect from the next item. The Admin API finds it with
// Lookup.
package channels

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
)

// The errors of the Admin API's actions.
var (
	// ErrStopped is the error of a skip on a channel that is stopped.
	ErrStopped = errors.New("channels: the channel is stopped")
	// ErrNoItem is the error of a skip to an item its library does not
	// hold.
	ErrNoItem = errors.New("channels: the library holds no such item")
	// ErrAction is the error of an action that is not known.
	ErrAction = errors.New("channels: the action is start, stop or skip; a push target's start, stop or restart")
	// ErrClosed is the error of an action on a manager that has stopped.
	ErrClosed = errors.New("channels: the streaming role has stopped")
)

// Options are what a Manager runs with.
type Options struct {
	// Hub is the role's hub, which channels publish into and push
	// targets subscribe to.
	Hub *hub.Hub
	// Libraries is the engine the media libraries are found by
	// (medialib.Lookup).
	Libraries *fib.Engine
	// Dial is the engine push targets dial from.
	Dial *fib.Engine
	// Log hears what goes wrong, and each item played; nil for none.
	Log *logs.Logger
	// Clock is the wall clock when nil.
	Clock Clock
	// Seed seeds shuffle's order; a random seed when 0.
	Seed uint64
}

// Manager runs the channels and push targets of a hub.
type Manager struct {
	o     Options
	clock Clock

	mu       sync.Mutex
	channels map[string]*Channel
	pushes   map[string]*Push
	stopped  bool

	randMu sync.Mutex
	rand   *rand.Rand

	// enc is how the channels that encrypt their HLS do it, by name, read
	// without a lock.
	enc atomic.Pointer[map[string]Encryption]
}

// New returns a Manager with nothing to run, which Apply gives its
// channels and push targets.
func New(o Options) *Manager {
	m := &Manager{o: o, clock: o.Clock, channels: map[string]*Channel{}, pushes: map[string]*Push{}}
	if m.clock == nil {
		m.clock = wallClock{}
	}
	seed := o.Seed
	if seed == 0 {
		seed = rand.Uint64()
	}
	m.rand = rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return m
}

// Apply moves the manager to chs and targets: a channel or push target of
// the same name is changed in place, and those left out end. A channel's
// new list, schedule, library or rendition takes effect from its next
// item; a push target whose stream, url or timeouts change connects again.
// An error, a channel that does not compile, changes nothing.
func (m *Manager) Apply(chs []config.StreamChannel, targets []config.PushTarget) error {
	sets := make([]*settings, len(chs))
	var errs []error
	for i := range chs {
		s, err := compile(&chs[i])
		if err != nil {
			errs = append(errs, err)
		}
		sets[i] = s
	}
	urls := make([]*config.PushURL, len(targets))
	for i := range targets {
		u, err := config.ParsePushURL(targets[i].URL)
		if err != nil {
			errs = append(errs, errors.New("push "+targets[i].Name+": "+err.Error()))
		}
		urls[i] = u
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return ErrClosed
	}
	var start, end []func()
	keep := map[string]bool{}
	for _, s := range sets {
		keep[s.name] = true
		if c := m.channels[s.name]; c != nil {
			start = append(start, func() { c.apply(s) })
			continue
		}
		c := newChannel(m, s)
		m.channels[s.name] = c
		start = append(start, c.Start)
	}
	for name, c := range m.channels {
		if !keep[name] {
			delete(m.channels, name)
			end = append(end, c.remove)
		}
	}
	keep = map[string]bool{}
	for i, t := range targets {
		keep[t.Name] = true
		if p := m.pushes[t.Name]; p != nil {
			if p.same(t) {
				p.retarget(t)
				continue
			}
			end = append(end, p.remove)
		}
		p := newPush(m, t, urls[i])
		m.pushes[t.Name] = p
		start = append(start, p.Start)
	}
	for name, p := range m.pushes {
		if !keep[name] {
			delete(m.pushes, name)
			end = append(end, p.remove)
		}
	}
	enc := encryptions(chs)
	m.enc.Store(&enc)
	m.mu.Unlock()
	// Ended first, so that a channel renamed frees its stream, and pushes
	// dial outside the manager's lock.
	for _, f := range end {
		f()
	}
	for _, f := range start {
		f()
	}
	return nil
}

// Stop ends every channel and push target; the manager runs nothing more.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	m.enc.Store(nil)
	chs, ps := m.channels, m.pushes
	m.channels, m.pushes = map[string]*Channel{}, map[string]*Push{}
	m.mu.Unlock()
	for _, c := range chs {
		c.remove()
	}
	for _, p := range ps {
		p.remove()
	}
}

// Channel returns the channel named name, or nil.
func (m *Manager) Channel(name string) *Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.channels[name]
}

// Push returns the push target named name, or nil.
func (m *Manager) Push(name string) *Push {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pushes[name]
}

// list returns the channels and push targets, sorted by name.
func (m *Manager) list() ([]*Channel, []*Push) {
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	chs := make([]*Channel, 0, len(m.channels))
	for _, c := range m.channels {
		chs = append(chs, c)
	}
	ps := make([]*Push, 0, len(m.pushes))
	for _, p := range m.pushes {
		ps = append(ps, p)
	}
	slices.SortFunc(chs, func(a, b *Channel) int { return strings.Compare(a.name, b.name) })
	slices.SortFunc(ps, func(a, b *Push) int { return strings.Compare(a.name, b.name) })
	return chs, ps
}

// Channels returns the status of every channel, sorted by name.
func (m *Manager) Channels() []ChannelStatus {
	chs, _ := m.list()
	out := make([]ChannelStatus, len(chs))
	for i, c := range chs {
		out[i] = c.Status()
	}
	return out
}

// Pushes returns the status of every push target, sorted by name.
func (m *Manager) Pushes() []PushStatus {
	_, ps := m.list()
	out := make([]PushStatus, len(ps))
	for i, p := range ps {
		out[i] = p.Status()
	}
	return out
}

// pushesOf returns the status of the push targets of stream.
func (m *Manager) pushesOf(stream string) []PushStatus {
	_, ps := m.list()
	out := []PushStatus{}
	for _, p := range ps {
		if p.stream == stream {
			out = append(out, p.Status())
		}
	}
	return out
}

// shuffle puts order in a random order.
func (m *Manager) shuffle(order []int) {
	m.randMu.Lock()
	defer m.randMu.Unlock()
	m.rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
}

// WriteMetrics writes the metrics of the manager's channels and push
// targets; of none for a nil Manager.
func (m *Manager) WriteMetrics(w observe.MetricWriter) {
	chs, ps := m.list()
	states := map[string]int{}
	for _, c := range chs {
		states[c.Status().State]++
	}
	w.Family("fit_streaming_channels", "gauge", "Channels, by state: playing, loading an item, idle with nothing to play, waiting for their stream's path, or stopped.")
	for _, st := range []string{StatePlaying, StateLoading, StateIdle, StateWaiting, StateStopped} {
		w.Sample("fit_streaming_channels", float64(states[st]), "state", st)
	}
	w.Family("fit_streaming_channel_items_total", "counter", "Items a channel started (played), or skipped as its library could not serve them (failed).")
	for _, c := range chs {
		w.Sample("fit_streaming_channel_items_total", float64(c.played.Load()), "channel", c.name, "result", "played")
		w.Sample("fit_streaming_channel_items_total", float64(c.failed.Load()), "channel", c.name, "result", "failed")
	}
	w.Family("fit_streaming_channel_underruns_total", "counter", "Times a channel had nothing read to send when it was due, its file pool or disk too slow, and its stream paused.")
	for _, c := range chs {
		w.Sample("fit_streaming_channel_underruns_total", float64(c.underruns.Load()), "channel", c.name)
	}
	pstates := map[string]int{}
	for _, p := range ps {
		pstates[p.Status().State]++
	}
	w.Family("fit_streaming_push_targets", "gauge", "Push targets, by state: connected, connecting, retrying after a failure, waiting for their stream, or stopped.")
	for _, st := range []string{PushConnected, PushConnecting, PushRetrying, PushWaiting, PushStopped} {
		w.Sample("fit_streaming_push_targets", float64(pstates[st]), "state", st)
	}
	w.Family("fit_streaming_push_connects_total", "counter", "Connections of a push target, by whether the target took the stream (ok) or not (failed).")
	for _, p := range ps {
		w.Sample("fit_streaming_push_connects_total", float64(p.connects.Load()), "target", p.name, "result", "ok")
		w.Sample("fit_streaming_push_connects_total", float64(p.failures.Load()), "target", p.name, "result", "failed")
	}
	w.Family("fit_streaming_push_bytes_total", "counter", "Bytes a push target sent: the media handed to RTMP, the payload SRT sent.")
	for _, p := range ps {
		w.Sample("fit_streaming_push_bytes_total", float64(p.Status().Bytes), "target", p.name)
	}
}

var (
	regMu    sync.Mutex
	registry atomic.Pointer[map[*fib.Engine]*Manager]
)

// Register makes m the manager Lookup finds for engine.
func Register(engine *fib.Engine, m *Manager) {
	swap(engine, func(cur *Manager) *Manager { return m })
}

// Unregister forgets engine's manager if it is m.
func Unregister(engine *fib.Engine, m *Manager) {
	swap(engine, func(cur *Manager) *Manager {
		if cur == m {
			return nil
		}
		return cur
	})
}

// swap sets engine's manager to what next makes of the current one, nil
// for none.
func swap(engine *fib.Engine, next func(*Manager) *Manager) {
	regMu.Lock()
	defer regMu.Unlock()
	m := map[*fib.Engine]*Manager{}
	if old := registry.Load(); old != nil {
		for k, v := range *old {
			m[k] = v
		}
	}
	if n := next(m[engine]); n != nil {
		m[engine] = n
	} else {
		delete(m, engine)
	}
	registry.Store(&m)
}

// Lookup returns the manager of the streaming role whose libraries are
// engine's, the engine the Admin API names, or nil.
func Lookup(engine *fib.Engine) *Manager {
	if r := registry.Load(); r != nil {
		return (*r)[engine]
	}
	return nil
}
