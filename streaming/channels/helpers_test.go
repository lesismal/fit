package channels

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// fakeClock is a Clock a test moves: its timers run, in the order they
// fall due, from Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock(at time.Time) *fakeClock { return &fakeClock{now: at} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(max(d, 0)), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

// Advance moves the clock by d, running each timer that falls due on the
// way at its time.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		live := c.timers[:0]
		for _, t := range c.timers {
			if t.stopped || t.fired {
				continue
			}
			live = append(live, t)
			if !t.at.After(end) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		c.timers = live
		if next == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// rig is a hub, a library of medialib's test files and a manager on a fake
// clock.
type rig struct {
	t     *testing.T
	key   *fib.Engine
	lib   *medialib.Library
	hub   *hub.Hub
	clock *fakeClock
	m     *Manager
}

// t0 is the test's start: a Friday, 09:59:58 UTC.
var t0 = time.Date(2026, 11, 6, 9, 59, 58, 0, time.UTC)

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, key: new(fib.Engine), hub: hub.New(hub.Config{}), clock: newFakeClock(t0)}
	if err := medialib.Apply(r.key, []config.MediaLibrary{{Name: "lib", Roots: []string{"../../medialib/testdata/lib"}}}, nil); err != nil {
		t.Fatal(err)
	}
	r.lib = medialib.Lookup(r.key, "lib")
	deadline := time.Now().Add(20 * time.Second)
	for r.lib.Status().Scans == 0 || r.lib.Status().Scanning {
		if time.Now().After(deadline) {
			t.Fatal("the library was not scanned")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.m = New(Options{Hub: r.hub, Libraries: r.key, Clock: r.clock, Seed: 7})
	t.Cleanup(func() {
		r.m.Stop()
		r.hub.Close()
		medialib.Remove(r.key)
		r.lib.Pool().Wait()
	})
	return r
}

// apply applies channels given as their configs.
func (r *rig) apply(chs ...config.StreamChannel) {
	r.t.Helper()
	if err := r.m.Apply(chs, nil); err != nil {
		r.t.Fatal(err)
	}
	r.settle()
}

// settle waits until no channel has a load or a read under way.
func (r *rig) settle() {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		busy := false
		chs, _ := r.m.list()
		for _, c := range chs {
			c.mu.Lock()
			busy = busy || c.reading || c.loading
			c.mu.Unlock()
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatal("a channel is still reading after 20s")
		}
		time.Sleep(time.Millisecond)
	}
}

// run moves the clock by d, a step at a time, letting reads end between
// steps as a quick disk would.
func (r *rig) run(d time.Duration) {
	r.t.Helper()
	const step = 5 * time.Millisecond
	for elapsed := time.Duration(0); elapsed < d; elapsed += step {
		r.clock.Advance(step)
		r.settle()
	}
}

// recorder is a player of a channel: what it heard, and when.
type recorder struct {
	clock *fakeClock
	mu    sync.Mutex
	recs  []rec
	ended error
}

type rec struct {
	at     time.Time
	tracks []*av.Track // for OnTracks
	pkt    *av.Packet
}

func (r *recorder) OnTracks(tracks []*av.Track) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec{at: r.clock.Now(), tracks: tracks})
}

func (r *recorder) OnPacket(pkt *av.Packet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec{at: r.clock.Now(), pkt: pkt})
}

func (r *recorder) OnClose(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ended = err
}

func (r *recorder) all() []rec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.recs)
}

// play subscribes a recorder to path, waiting for it to be published.
func (r *rig) play(path string) *recorder {
	r.t.Helper()
	rec := &recorder{clock: r.clock}
	sub, err := r.hub.Subscribe(path, rec, hub.SubscribeOptions{Wait: time.Hour})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(sub.Close)
	return rec
}

// segments splits what a recorder heard at each OnTracks.
func segments(recs []rec) [][]rec {
	var out [][]rec
	for _, x := range recs {
		if x.tracks != nil {
			out = append(out, []rec{x})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], x)
		}
	}
	return out
}

func boolp(b bool) *bool { return &b }
