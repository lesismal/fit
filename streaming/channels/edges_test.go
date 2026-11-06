package channels

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/observe"
)

// loadSource picks and loads an item's source, its tables read.
func loadSource(t *testing.T, l *medialib.Library, it *medialib.Item, rendition string, c choice) *source {
	t.Helper()
	src, err := newSource(l, it, rendition, c)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	src.load(func(err error) { done <- err })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return src
}

func TestSources(t *testing.T) {
	r := newRig(t)
	bbb := r.lib.Item("films/bbb")
	// A rendition by its name, and the best when no rendition has it.
	if rd := pickRendition(bbb, ""); rd.Name != "360p" {
		t.Errorf("best: %q", rd.Name)
	}
	if rd := pickRendition(bbb, "nope"); rd.Name != "360p" {
		t.Errorf("unknown name: %q", rd.Name)
	}
	if rd := pickRendition(bbb, "360p"); rd.Name != "360p" {
		t.Errorf("by name: %q", rd.Name)
	}
	if rd := pickRendition(bbb, bbb.Renditions[0].Name); rd != bbb.Renditions[0] {
		t.Errorf("by name: %q", rd.Name)
	}
	sintel := r.lib.Item("sintel")
	if rd := pickRendition(sintel, "sd"); rd.Name != "sd" {
		t.Errorf("sintel sd: %q", rd.Name)
	}
	for _, c := range []struct {
		codec, kind string
		video, ok   bool
	}{{"h264", medialib.KindVideo, true, true}, {"av1", medialib.KindAudio, true, false}, {"opus", medialib.KindAudio, false, true},
		{"mjpeg", medialib.KindVideo, false, false}} {
		if v, ok := playable(medialib.Track{Codec: c.codec, Kind: c.kind}); v != c.video || ok != c.ok {
			t.Errorf("%s %s: %v %v", c.codec, c.kind, v, ok)
		}
	}

	// A rendition of video alone takes the item's audio rendition.
	video := *pickRendition(bbb, "")
	video.Tracks = video.Tracks[:1]
	it := *bbb
	it.Renditions = []*medialib.Rendition{&video}
	src := loadSource(t, r.lib, &it, "", choice{item: it.ID})
	if len(src.tracks) != 2 || src.tracks[1].rend != bbb.Audio[0] || src.rend != &video {
		t.Fatalf("%+v", src.tracks)
	}
	// Audio alone: its rendition is the audio's; started half way, from
	// the sample shown there.
	it.Renditions = nil
	src = loadSource(t, r.lib, &it, "", choice{item: it.ID, offset: 500 * time.Millisecond})
	if len(src.tracks) != 1 || src.rend != bbb.Audio[0] || !src.place(0, 0, nil) {
		t.Fatalf("%+v", src)
	}
	if s := src.tracks[0].tb.Sample(src.tracks[0].next); src.tracks[0].tb.Time(s.PTS()+int64(s.Duration)) <= 500*time.Millisecond {
		t.Errorf("starts at %d", src.tracks[0].next)
	}
	// Nothing to play: no renditions, or past the end.
	it.Audio = nil
	if _, err := newSource(r.lib, &it, "", choice{}); !errors.Is(err, errNoMedia) {
		t.Errorf("no media: %v", err)
	}
	src = loadSource(t, r.lib, bbb, "", choice{offset: time.Hour})
	if src.place(0, 0, nil) {
		// The video's last keyframe, and audio from its time.
		if src.tracks[0].next == 0 {
			t.Error("an hour in starts at the first keyframe")
		}
	}
	for _, tr := range src.tracks {
		tr.next = tr.tb.Len()
	}
	if tk, _, _ := src.head(); tk != nil {
		t.Error("head past the end")
	}

	// NAL units of another length than 4 bytes are not carried.
	avc := func(b byte) []byte { return []byte{1, 0x64, 0, 0x1f, b} }
	hvc := make([]byte, 23)
	if !nalSize4(&av.Track{Codec: av.CodecH264, Config: avc(0xff)}) || nalSize4(&av.Track{Codec: av.CodecH264, Config: avc(0xfd)}) ||
		nalSize4(&av.Track{Codec: av.CodecH265, Config: hvc}) || !nalSize4(&av.Track{Codec: av.CodecAAC}) {
		t.Error("nalSize4")
	}

	// A sample larger than the limit, and a file changed under a read.
	src = loadSource(t, r.lib, bbb, "", choice{})
	src.place(0, 0, nil)
	if _, _, err := src.read(time.Second, 1<<20, 10); !errors.Is(err, errSample) {
		t.Errorf("a sample past the limit: %v", err)
	}
}

func TestFileChanged(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../../medialib/testdata/lib/films/bbb.mp4")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "bbb.mp4")
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	key := new(fib.Engine)
	if err := medialib.Apply(key, []config.MediaLibrary{{Name: "tmp", Roots: []string{dir}}}, nil); err != nil {
		t.Fatal(err)
	}
	l := medialib.Lookup(key, "tmp")
	t.Cleanup(func() {
		medialib.Remove(key)
		l.Pool().Wait()
	})
	for deadline := time.Now().Add(20 * time.Second); l.Status().Scans == 0 || l.Status().Scanning; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no scan")
		}
	}
	src := loadSource(t, l, l.Item("bbb"), "", choice{})
	src.place(0, 0, nil)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.read(time.Second, 1<<20, 1<<20); !errors.Is(err, medialib.ErrChanged) {
		t.Errorf("a file changed: %v", err)
	}
}

func TestChannelEdges(t *testing.T) {
	r := newRig(t)
	// Settings that do not compile change nothing.
	if err := r.m.Apply([]config.StreamChannel{{Name: "x", TimeZone: "Mars/Base"}}, nil); err == nil {
		t.Error("a bad time zone")
	}
	if err := r.m.Apply([]config.StreamChannel{{Name: "x", Schedule: []config.ChannelSlot{{At: "nope"}}}}, nil); err == nil {
		t.Error("a bad at")
	}
	if err := r.m.Apply(nil, []config.PushTarget{{StreamPush: config.StreamPush{Name: "p", URL: "ftp://x"}}}); err == nil {
		t.Error("a bad url")
	}
	r.apply(config.StreamChannel{Name: "tv/e", Library: "lib", Items: []string{"films/bbb"}})
	c := r.m.Channel("tv/e")
	if c.Name() != "tv/e" {
		t.Error(c.Name())
	}
	r.run(100 * time.Millisecond)

	// A stale timer, a stale load and read, a stale origin.
	c.mu.Lock()
	id, gen := c.timerID, c.gen
	c.mu.Unlock()
	c.fire(id + 100)
	c.loaded(gen+1, nil, nil)
	c.readDone(gen+1, nil, nil, 0, nil)
	c.mu.Lock()
	c.queue = append([]entry{{pkt: &av.Packet{DTS: 42}}}, c.queue...)
	if o := c.origin(); c.anchored || o != 42 {
		if !c.anchored {
			t.Errorf("origin %s", o)
		}
	}
	c.queue = c.queue[1:]
	c.mu.Unlock()

	// A busy pool: the load and the read are tried again.
	c.mu.Lock()
	src := c.now.src
	c.loading = true
	c.mu.Unlock()
	c.loaded(c.gen, &source{choice: choice{item: "films/bbb", from: fromList}}, medialib.ErrBusy)
	c.mu.Lock()
	c.reading = true
	c.mu.Unlock()
	c.readDone(c.gen, src, nil, 0, medialib.ErrBusy)
	// A read that fails ends the item where it was read to.
	c.mu.Lock()
	c.reading, c.src = true, src
	c.mu.Unlock()
	c.readDone(c.gen, src, nil, 0, errors.New("disk"))
	if st := c.Status(); !strings.Contains(st.Error, "disk") {
		t.Errorf("%+v", st)
	}
	r.run(2 * time.Second)
	if st := c.Status(); st.State != StatePlaying {
		t.Errorf("%+v", st)
	}
	// An item placed nowhere: nothing to play from its offset.
	c.mu.Lock()
	c.loading = true
	bad := &source{choice: choice{item: "films/bbb"}}
	c.mu.Unlock()
	failed := c.failed.Load()
	c.loaded(c.gen, bad, nil)
	if c.failed.Load() != failed+1 {
		t.Error("an item with nothing to play")
	}
	// The pool refusing a read: the library removed.
	medialib.Remove(r.key)
	c.mu.Lock()
	c.src, c.reading, c.queue, c.qBytes = src, false, nil, 0
	c.read(r.clock.Now())
	if c.reading {
		t.Error("a read the pool refused")
	}
	c.mu.Unlock()
	if c.itemDuration("films/bbb") != 0 {
		t.Error("an item of a library removed")
	}
	// Removed for good: a start does nothing.
	c.remove()
	c.Start()
	if st := c.Status(); st.State != StateStopped {
		t.Errorf("%+v", st)
	}
	c.Stop()
}

func TestReloadKicksIdle(t *testing.T) {
	r := newRig(t)
	ch := config.StreamChannel{Name: "tv/k", Library: "lib", Items: []string{"nope"}}
	r.apply(ch)
	c := r.m.Channel("tv/k")
	r.run(50 * time.Millisecond)
	if st := c.Status(); st.State != StateIdle {
		t.Fatalf("%+v", st)
	}
	// A reload of an idle channel chooses from its new list at once.
	ch.Items = []string{"films/bbb"}
	ch.Order = "shuffle"
	r.apply(ch)
	r.run(50 * time.Millisecond)
	if st := c.Status(); st.State != StatePlaying {
		t.Fatalf("%+v", st)
	}
	// A stopped channel takes a reload, and plays it once started.
	c.Stop()
	ch.Items = []string{"sintel"}
	ch.Order = ""
	r.apply(ch)
	c.Start()
	r.settle()
	r.run(50 * time.Millisecond)
	if st := c.Status(); st.Now == nil || st.Now.Item != "sintel" {
		t.Fatalf("%+v", st.Now)
	}
	// The list's state: a reload keeps its place, an undo gives an item
	// back.
	var l listState
	s := &settings{items: []string{"a", "b", "c"}, loop: true}
	l.next(s, nil)
	l.next(s, nil)
	s2 := &settings{items: []string{"x", "b", "y"}, loop: true}
	if id, _ := l.next(s2, nil); id != "y" {
		t.Errorf("after b: %q", id)
	}
	l.undo(choice{item: "y", from: fromList})
	if id, _ := l.next(s2, nil); id != "y" {
		t.Errorf("given back: %q", id)
	}
	l.undo(choice{item: "z", from: fromList})
	if id, _ := l.next(&settings{}, nil); id != "" {
		t.Error("an empty list")
	}
}

func TestSlots(t *testing.T) {
	r := newRig(t)
	r.apply(config.StreamChannel{Name: "tv/s", Library: "lib", Items: []string{"films/bbb"},
		Schedule: []config.ChannelSlot{{At: "09:00", Item: "sintel"}, {At: "09:59:57", Item: "nope"}}})
	c := r.m.Channel("tv/s")
	c.mu.Lock()
	// 09:00's sintel, a second long, is over: passed over. 09:59:57's item
	// is not known: due until started, but the channel's start passed it.
	if _, _, ok := c.dueSlot(t0); ok {
		t.Error("a slot passed at the start is due")
	}
	c.slotDone = time.Time{}
	if _, sl, ok := c.dueSlot(t0); !ok || sl.item != "nope" {
		t.Errorf("due: %v %v", sl, ok)
	}
	c.slotDone = time.Time{}
	if _, _, ok := c.dueSlot(t0.Add(-time.Hour + 2500*time.Millisecond)); !ok {
		t.Error("09:00 under way")
	}
	if _, _, ok := c.dueSlot(t0.Add(-time.Hour + 4*time.Second)); ok {
		t.Error("09:00 over")
	}
	c.mu.Unlock()
	r.run(100 * time.Millisecond)
	if st := c.Status(); st.Now == nil || st.Now.Item != "films/bbb" {
		t.Errorf("%+v", st.Now)
	}
}

// metrics is a MetricWriter that keeps what it is given.
type metrics struct {
	mu      sync.Mutex
	samples map[string]float64
}

func (m *metrics) Family(string, string, string) {}

func (m *metrics) Sample(name string, v float64, labels ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples[name+"{"+strings.Join(labels, ",")+"}"] = v
}

func (m *metrics) Histogram(string, observe.HistogramSnapshot, ...string) {}

func TestMetricsAndRegistry(t *testing.T) {
	r := newRig(t)
	r.apply(config.StreamChannel{Name: "tv/m", Library: "lib", Items: []string{"films/bbb"}})
	r.run(100 * time.Millisecond)
	w := &metrics{samples: map[string]float64{}}
	r.m.WriteMetrics(w)
	if w.samples["fit_streaming_channels{state,playing}"] != 1 || w.samples["fit_streaming_channel_items_total{channel,tv/m,result,played}"] != 1 {
		t.Errorf("%v", w.samples)
	}
	(*Manager)(nil).WriteMetrics(w)

	key := new(fib.Engine)
	Register(key, r.m)
	other := New(Options{Hub: r.hub})
	Unregister(key, other)
	if Lookup(key) != r.m {
		t.Error("another's unregister")
	}
	Unregister(key, r.m)
	if Lookup(key) != nil {
		t.Error("unregistered")
	}
	r.m.Stop()
	if err := r.m.Apply(nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("a stopped manager: %v", err)
	}
	if len(r.m.Channels()) != 0 || len(r.m.Pushes()) != 0 {
		t.Error("left")
	}
}

// startEngine starts a fib engine to dial from.
func startEngine(t *testing.T) *fib.Engine {
	t.Helper()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run() }()
	t.Cleanup(func() {
		e.Stop()
		<-done
		_ = e.Close()
	})
	return e
}

func TestPushFailures(t *testing.T) {
	h := hub.New(hub.Config{})
	t.Cleanup(h.Close)
	clock := newFakeClock(t0)
	e := startEngine(t)
	m := New(Options{Hub: h, Dial: e, Clock: clock})
	t.Cleanup(m.Stop)
	pub, err := h.Publish("live/x", []*av.Track{{Codec: av.CodecAAC, Config: []byte{0x11, 0x90}, SampleRate: 48000, Channels: 2}}, hub.PublishInfo{})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := l.Addr().String()
	l.Close()
	targets := (&config.Streaming{Push: []config.StreamPush{
		{Name: "rtmp", Stream: "live/x", URL: "rtmp://" + refused + "/app/key-secret"},
		{Name: "srt", Stream: "live/x", URL: "srt://no-such-host.invalid:9000?streamid=key-secret"},
		{Name: "idle", Stream: "live/none", URL: "rtmp://" + refused + "/app/k"},
	}}).PushTargets()
	if err := m.Apply(nil, targets); err != nil {
		t.Fatal(err)
	}
	wait := func(name, state string) PushStatus {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			st := m.Push(name).Status()
			if st.State == state {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v, want %s", name, st, state)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	st := wait("rtmp", PushRetrying)
	if st.Failures == 0 || st.RetryIn <= 0 || strings.Contains(st.Error, "secret") || m.Push("rtmp").Name() != "rtmp" {
		t.Errorf("%+v", st)
	}
	if st := wait("srt", PushRetrying); strings.Contains(st.Error, "secret") {
		t.Errorf("%+v", st)
	}
	if st := wait("idle", PushWaiting); st.Error != errNotPublished.Error() {
		t.Errorf("%+v", st)
	}
	// Retries back off, up to retry_max.
	p := m.Push("rtmp")
	for range 8 {
		clock.Advance(time.Minute)
		wait("rtmp", PushRetrying)
	}
	p.mu.Lock()
	if p.backoff != p.t.RetryMaxOr() {
		t.Errorf("backoff %s", p.backoff)
	}
	p.mu.Unlock()
	// A reload of the same target keeps it; one of another url makes it
	// again; one left out ends.
	targets[0].RetryMax = config.Duration(10 * time.Second)
	if err := m.Apply(nil, targets[:2]); err != nil {
		t.Fatal(err)
	}
	if m.Push("rtmp") != p || m.Push("idle") != nil {
		t.Error("reload")
	}
	targets[0].URL = "rtmp://" + refused + "/app/other"
	if err := m.Apply(nil, targets[:1]); err != nil {
		t.Fatal(err)
	}
	if m.Push("rtmp") == p || m.Push("srt") != nil {
		t.Error("a new url")
	}
	// The redaction of an error that holds the url.
	if got := m.Push("rtmp").redact(errors.New("dial " + targets[0].URL + ": refused")); strings.Contains(got, "other") {
		t.Error(got)
	}
	if m.Push("rtmp").redact(nil) != "" {
		t.Error("nil")
	}
	// Without an engine, a push cannot dial.
	m2 := New(Options{Hub: h, Clock: clock})
	t.Cleanup(m2.Stop)
	if err := m2.Apply(nil, targets[:1]); err != nil {
		t.Fatal(err)
	}
	if st := m2.Push("rtmp").Status(); st.State != PushRetrying || st.Error != errNoEngine.Error() {
		t.Errorf("%+v", st)
	}
	// The push's sink hands on what it hears.
	rec := &recorder{clock: clock}
	s := &pushSink{p: m2.Push("rtmp"), next: rec}
	s.OnTracks(nil)
	s.OnPacket(&av.Packet{Data: []byte{1, 2}})
	s.OnClose(nil)
	if len(rec.all()) != 2 || rec.ended != nil || m2.Push("rtmp").sent.Load() != 2 {
		t.Error("the sink")
	}
	// A start of one that runs does nothing; a stale attempt neither.
	m2.Push("rtmp").Start()
	m2.Push("rtmp").attempt(0)
	m2.Push("rtmp").ready(0, nil, errors.New("x"))
}
