package channels

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
)

// checkPaced checks that every packet of recs was sent when its decode time
// fell due, on the timeline of the first, within sendAhead early and late
// by a step, or by the B-frames' delay of the fixtures' video at the start
// of an item, whose first pictures are decoded before the last item's
// audio ends; that decode times never go back on a track, nor PTS before
// DTS; and that a track's packets follow one another, across items too,
// without a gap: within 50ms, a frame of video, 20ms of audio and a step,
// or gaps[0] where the items' own content has one or a skip cuts in.
func checkPaced(t *testing.T, recs []rec, gaps ...time.Duration) {
	t.Helper()
	var start time.Time
	var dts0 time.Duration
	first := true
	last := map[int]time.Duration{}
	gap := 50 * time.Millisecond
	if len(gaps) > 0 {
		gap = gaps[0]
	}
	for _, seg := range segments(recs) {
		for _, r := range seg[1:] {
			p := r.pkt
			if first {
				start, dts0, first = r.at, p.DTS, false
			}
			due := start.Add(p.DTS - dts0)
			if early, late := due.Sub(r.at), r.at.Sub(due); early > sendAhead || late > 100*time.Millisecond {
				t.Fatalf("a packet of DTS %s was sent at %s, due at %s", p.DTS, r.at.Sub(t0), due.Sub(t0))
			}
			if l, ok := last[p.Track]; ok && (p.DTS <= l || p.DTS-l > gap) {
				t.Fatalf("track %d: DTS %s after %s", p.Track, p.DTS, l)
			}
			if p.PTS < p.DTS {
				t.Fatalf("PTS %s before DTS %s", p.PTS, p.DTS)
			}
			last[p.Track] = p.DTS
		}
	}
}

func TestBackToBack(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/one")
	r.apply(config.StreamChannel{Name: "tv/one", Library: "lib", Items: []string{"films/bbb", "sintel"}})
	r.run(4500 * time.Millisecond)
	recs := rec.all()
	segs := segments(recs)
	if len(segs) < 4 {
		t.Fatalf("%d items played in 4.5s, want 4 or more", len(segs))
	}
	// bbb at its 360p rendition (128x96), then sintel's, then bbb again.
	if v := segs[0][0].tracks[0]; v.Codec != av.CodecH264 || v.Width != 128 {
		t.Errorf("first item's video: %+v", v)
	}
	if len(segs[1][0].tracks) != 2 || len(segs[2][0].tracks) != 2 || segs[2][0].tracks[0].Width != 128 {
		t.Errorf("tracks: %v %v", segs[1][0].tracks, segs[2][0].tracks)
	}
	checkPaced(t, recs)
	// Each item of a second, its packets over about a second.
	for i, seg := range segs[:3] {
		d := seg[len(seg)-1].pkt.DTS - seg[1].pkt.DTS
		if d < 900*time.Millisecond || d > 1100*time.Millisecond {
			t.Errorf("item %d spans %s", i, d)
		}
		if !seg[1].pkt.Keyframe || seg[1].pkt.Track != 0 {
			t.Errorf("item %d starts with %+v", i, seg[1].pkt)
		}
	}
	st := r.m.Channel("tv/one").Status()
	if st.State != StatePlaying || st.Now == nil || st.Played < 4 || st.Failed != 0 || st.Underruns != 0 || st.Now.From != fromList {
		t.Errorf("%+v", st)
	}
	if info, ok := r.hub.Stream("tv/one"); !ok || !info.Publishing || info.Publisher.Protocol != "channel" {
		t.Errorf("%+v", info)
	}
}

func TestNoLoop(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/once")
	r.apply(config.StreamChannel{Name: "tv/once", Library: "lib", Items: []string{"films/bbb"}, Loop: boolp(false)})
	r.run(3 * time.Second)
	if n := len(segments(rec.all())); n != 1 {
		t.Errorf("%d items, want 1", n)
	}
	st := r.m.Channel("tv/once").Status()
	if st.State != StateIdle || st.Now != nil {
		t.Errorf("%+v", st)
	}
	// Still published, quiet.
	if info, ok := r.hub.Stream("tv/once"); !ok || !info.Publishing {
		t.Errorf("%+v", info)
	}
	// A start from the Admin API of a channel that runs does nothing; a
	// stop and a start play the list played out again from the first.
	c := r.m.Channel("tv/once")
	c.Start()
	c.Stop()
	if st := c.Status(); st.State != StateStopped {
		t.Errorf("%+v", st)
	}
	if err := c.Skip(""); !errors.Is(err, ErrStopped) {
		t.Errorf("skip of a stopped channel: %v", err)
	}
	if info, ok := r.hub.Stream("tv/once"); ok && info.Publishing {
		t.Error("a stopped channel stays published")
	}
	c.Start()
	r.settle()
	r.run(200 * time.Millisecond)
	if st := c.Status(); st.State != StatePlaying || st.Now.Item != "films/bbb" || st.Played != 2 {
		t.Errorf("started again: %+v", st)
	}
}

// TestSameTracks plays an item twice: the second carries on the first's
// tracks, which are set once, and its timestamps.
func TestSameTracks(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/same")
	r.apply(config.StreamChannel{Name: "tv/same", Library: "lib", Items: []string{"films/bbb", "films/bbb"}})
	r.run(2500 * time.Millisecond)
	recs := rec.all()
	if n := len(segments(recs)); n != 1 {
		t.Errorf("the tracks were set %d times", n)
	}
	checkPaced(t, recs)
	if st := r.m.Channel("tv/same").Status(); st.Played < 3 {
		t.Errorf("%+v", st)
	}
}

func TestSkipAndSkipTo(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/skip")
	r.apply(config.StreamChannel{Name: "tv/skip", Library: "lib", Items: []string{"films/bbb", "sintel", "clips/late"}})
	r.run(300 * time.Millisecond)
	c := r.m.Channel("tv/skip")
	if st := c.Status(); st.Now == nil || st.Now.Item != "films/bbb" {
		t.Fatalf("%+v", st)
	}
	if err := c.Skip(""); err != nil {
		t.Fatal(err)
	}
	r.settle()
	r.run(200 * time.Millisecond)
	if st := c.Status(); st.Now == nil || st.Now.Item != "sintel" {
		t.Fatalf("after a skip: %+v", st.Now)
	}
	if err := c.Skip("nope"); !errors.Is(err, ErrNoItem) {
		t.Errorf("skip to an item not held: %v", err)
	}
	if err := c.Skip("films/bbb"); err != nil {
		t.Fatal(err)
	}
	r.settle()
	r.run(200 * time.Millisecond)
	if st := c.Status(); st.Now == nil || st.Now.Item != "films/bbb" || st.Now.From != fromAdmin {
		t.Fatalf("after a skip to bbb: %+v", st.Now)
	}
	// Then the list goes on after sintel, the last it played.
	r.run(time.Second)
	if st := c.Status(); st.Now == nil || st.Now.Item != "clips/late" {
		t.Fatalf("after bbb: %+v", st.Now)
	}
	checkPaced(t, rec.all(), 150*time.Millisecond)
}

func TestScheduleStartsOnTime(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/sched")
	// t0 is 09:59:58: bbb plays, again, until sintel's slot at 10:00:00.
	r.apply(config.StreamChannel{Name: "tv/sched", Library: "lib", Items: []string{"films/bbb"},
		Schedule: []config.ChannelSlot{{At: "10:00", Item: "sintel"}, {At: "2026-11-06 10:00:05", Item: "clips/late"}}})
	c := r.m.Channel("tv/sched")
	if st := c.Status(); st.NextSlot == nil || st.NextSlot.Item != "sintel" || !st.NextSlot.At.Equal(t0.Add(2*time.Second)) {
		t.Errorf("next slot: %+v", st.NextSlot)
	}
	nowAt := func(at time.Duration) string {
		r.run(t0.Add(at).Sub(r.clock.Now()))
		if st := c.Status(); st.Now != nil {
			return st.Now.Item
		}
		return ""
	}
	// bbb from 09:59:58, again at :59, sintel at 10:00:00, bbb after it,
	// and late at 10:00:05, once.
	for _, want := range []struct {
		at   time.Duration
		item string
	}{{500 * time.Millisecond, "films/bbb"}, {1500 * time.Millisecond, "films/bbb"}, {2500 * time.Millisecond, "sintel"},
		{3500 * time.Millisecond, "films/bbb"}, {7500 * time.Millisecond, "clips/late"}, {8500 * time.Millisecond, "films/bbb"}} {
		if got := nowAt(want.at); got != want.item {
			t.Errorf("at 09:59:58 + %s: %q plays, want %q", want.at, got, want.item)
		}
	}
	recs := rec.all()
	checkPaced(t, recs, 250*time.Millisecond)
	// Each slot's first picture shown at its start, within a tick.
	shown := shownAt(recs)
	for _, slotAt := range []time.Duration{2 * time.Second, 7 * time.Second} {
		if !slices.ContainsFunc(shown, func(at time.Duration) bool { return (at - slotAt).Abs() <= 5*time.Millisecond }) {
			t.Errorf("no item shown from 09:59:58 + %s: %v", slotAt, shown)
		}
	}
}

// shownAt returns, for each item recs hold, when its first picture is shown
// on the channel's timeline, after t0: the timeline of the first packet's
// DTS, sent when due.
func shownAt(recs []rec) []time.Duration {
	var out []time.Duration
	var start time.Time
	var dts0 time.Duration
	for _, seg := range segments(recs) {
		first := time.Duration(-1)
		for _, r := range seg[1:] {
			if start.IsZero() {
				start, dts0 = r.at, r.pkt.DTS
			}
			if r.pkt.Track == 0 && (first < 0 || r.pkt.PTS < first) {
				first = r.pkt.PTS
			}
		}
		if first >= 0 {
			out = append(out, start.Add(first-dts0).Sub(t0))
		}
	}
	return out
}

func TestScheduleCutsIn(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/cut")
	// The slot falls half way through an item: it cuts in at its time.
	r.clock.Advance(500 * time.Millisecond) // 09:59:58.5
	r.apply(config.StreamChannel{Name: "tv/cut", Library: "lib", Items: []string{"films/bbb"},
		Schedule: []config.ChannelSlot{{At: "10:00:00", Item: "sintel"}}, TimeZone: "UTC"})
	r.run(2500 * time.Millisecond)
	recs := rec.all()
	checkPaced(t, recs)
	if shown := shownAt(recs); !slices.ContainsFunc(shown, func(at time.Duration) bool { return (at - 2*time.Second).Abs() <= 5*time.Millisecond }) {
		t.Fatalf("sintel was not shown from 10:00:00: %v", shown)
	}
	c := r.m.Channel("tv/cut")
	if st := c.Status(); st.Now == nil || st.Now.Item != "films/bbb" && st.Now.Item != "sintel" {
		t.Errorf("%+v", st.Now)
	}
}

func TestScheduleJoined(t *testing.T) {
	r := newRig(t)
	r.clock.Advance(2500 * time.Millisecond) // 10:00:00.5
	rec := r.play("tv/join")
	r.apply(config.StreamChannel{Name: "tv/join", Library: "lib", Items: []string{"films/bbb"},
		Schedule: []config.ChannelSlot{{At: "sat-fri 10:00", Item: "sintel"}}})
	r.run(100 * time.Millisecond)
	st := r.m.Channel("tv/join").Status()
	if st.Now == nil || st.Now.Item != "sintel" || st.Now.From != fromSchedule || st.Now.Offset != 0.5 || !st.Now.Slot.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("%+v", st.Now)
	}
	r.run(1500 * time.Millisecond)
	checkPaced(t, rec.all())
	if st := r.m.Channel("tv/join").Status(); st.Now == nil || st.Now.Item != "films/bbb" {
		t.Errorf("after the slot: %+v", st.Now)
	}
	// A slot passed over whole is not joined.
	r2 := newRig(t)
	r2.clock.Advance(10 * time.Second)
	r2.apply(config.StreamChannel{Name: "tv/join", Library: "lib", Items: []string{"films/bbb"},
		Schedule: []config.ChannelSlot{{At: "10:00", Item: "sintel"}}})
	r2.run(100 * time.Millisecond)
	if st := r2.m.Channel("tv/join").Status(); st.Now == nil || st.Now.Item != "films/bbb" {
		t.Errorf("a slot over is joined: %+v", st.Now)
	}
}

func TestScheduleOnly(t *testing.T) {
	r := newRig(t)
	r.apply(config.StreamChannel{Name: "tv/slots", Library: "lib", Schedule: []config.ChannelSlot{{At: "10:00", Item: "sintel"}}})
	c := r.m.Channel("tv/slots")
	if st := c.Status(); st.State != StateIdle {
		t.Fatalf("%+v", st)
	}
	rec := r.play("tv/slots")
	r.run(2200 * time.Millisecond)
	if st := c.Status(); st.State != StatePlaying || st.Now.Item != "sintel" {
		t.Fatalf("%+v", st)
	}
	r.run(1500 * time.Millisecond)
	if st := c.Status(); st.State != StateIdle || st.NextSlot == nil || !st.NextSlot.At.Equal(t0.Add(2*time.Second).AddDate(0, 0, 1)) {
		t.Fatalf("%+v", st)
	}
	if n := len(segments(rec.all())); n != 1 {
		t.Errorf("%d items", n)
	}
}

func TestReloadTakesEffectNextItem(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/reload")
	ch := config.StreamChannel{Name: "tv/reload", Library: "lib", Items: []string{"films/bbb", "sintel"}, ReadAhead: config.Duration(500 * time.Millisecond)}
	r.apply(ch)
	r.run(200 * time.Millisecond)
	c := r.m.Channel("tv/reload")
	ch.Items = []string{"clips/late"}
	r.apply(ch)
	if c2 := r.m.Channel("tv/reload"); c2 != c {
		t.Fatal("the channel was made again")
	}
	if st := c.Status(); st.Now == nil || st.Now.Item != "films/bbb" {
		t.Fatalf("the item playing changed: %+v", st.Now)
	}
	r.run(2 * time.Second)
	var items []string
	for _, seg := range segments(rec.all()) {
		items = append(items, seg[0].tracks[len(seg[0].tracks)-1].Codec.String())
	}
	if st := c.Status(); st.Now == nil || st.Now.Item != "clips/late" {
		t.Errorf("after the reload: %+v (%v)", st.Now, items)
	}
	checkPaced(t, rec.all(), 250*time.Millisecond)
	// A channel renamed is another: the old one's stream ends.
	ch.Name = "tv/renamed"
	r.apply(ch)
	if r.m.Channel("tv/reload") != nil || r.m.Channel("tv/renamed") == nil {
		t.Error("rename")
	}
	if info, ok := r.hub.Stream("tv/reload"); ok && info.Publishing {
		t.Error("the old channel still publishes")
	}
	// Nothing left: every channel ends.
	r.apply()
	if len(r.m.Channels()) != 0 {
		t.Error("channels left")
	}
}

func TestShuffle(t *testing.T) {
	r := newRig(t)
	r.apply(config.StreamChannel{Name: "tv/shuffle", Library: "lib", Items: []string{"films/bbb", "sintel", "clips/late"}, Order: "shuffle"})
	c := r.m.Channel("tv/shuffle")
	var seen []string
	for len(seen) < 6 {
		r.run(100 * time.Millisecond)
		if st := c.Status(); st.Now != nil && (len(seen) == 0 || seen[len(seen)-1] != st.Now.Item || st.Played > uint64(len(seen))) {
			if st.Played > uint64(len(seen)) {
				seen = append(seen, st.Now.Item)
			}
		}
	}
	for round := range 2 {
		got := map[string]bool{}
		for _, id := range seen[round*3 : round*3+3] {
			got[id] = true
		}
		if len(got) != 3 {
			t.Errorf("round %d: %v", round, seen)
		}
	}
	if seen[2] == seen[3] {
		t.Errorf("the same item twice in a row: %v", seen)
	}
}

func TestItemsSkipped(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/skipped")
	r.apply(config.StreamChannel{Name: "tv/skipped", Library: "lib", Items: []string{"nope", "broken", "films/bbb"}})
	r.run(300 * time.Millisecond)
	c := r.m.Channel("tv/skipped")
	st := c.Status()
	if st.Now == nil || st.Now.Item != "films/bbb" || st.Failed < 2 {
		t.Fatalf("%+v", st)
	}
	if len(segments(rec.all())) != 1 {
		t.Error("segments")
	}
	// Every item failing: the channel idles, and tries again later.
	r.apply(config.StreamChannel{Name: "tv/none", Library: "lib", Items: []string{"nope"}})
	c = r.m.Channel("tv/none")
	r.run(100 * time.Millisecond)
	if st := c.Status(); st.State != StateIdle || !strings.Contains(st.Error, "nope") {
		t.Fatalf("%+v", st)
	}
	failed := c.Status().Failed
	r.run(retryIdle + 100*time.Millisecond)
	if st := c.Status(); st.Failed <= failed {
		t.Errorf("not tried again: %+v", st)
	}
	// A library that does not run.
	r.apply(config.StreamChannel{Name: "tv/nolib", Library: "nolib", Items: []string{"films/bbb"}})
	r.run(10 * time.Millisecond)
	if st := r.m.Channel("tv/nolib").Status(); st.State != StateIdle || !strings.Contains(st.Error, "library") {
		t.Errorf("%+v", st)
	}
}

func TestStreamTaken(t *testing.T) {
	r := newRig(t)
	other, err := r.hub.Publish("tv/taken", []*av.Track{{Codec: av.CodecAAC}}, hub.PublishInfo{Protocol: "rtmp"})
	if err != nil {
		t.Fatal(err)
	}
	r.apply(config.StreamChannel{Name: "tv/taken", Library: "lib", Items: []string{"films/bbb"}})
	c := r.m.Channel("tv/taken")
	if st := c.Status(); st.State != StateWaiting || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	other.Close()
	r.run(retryIdle + 100*time.Millisecond)
	if st := c.Status(); st.State != StatePlaying {
		t.Fatalf("%+v", st)
	}
}

func TestStopStart(t *testing.T) {
	r := newRig(t)
	rec := r.play("tv/ss")
	r.apply(config.StreamChannel{Name: "tv/ss", Library: "lib", Items: []string{"films/bbb", "sintel"}})
	r.run(1500 * time.Millisecond)
	c := r.m.Channel("tv/ss")
	c.Stop()
	if info, ok := r.hub.Stream("tv/ss"); ok && info.Publishing {
		t.Fatal("still published")
	}
	r.run(time.Second)
	c.Start()
	r.settle()
	r.run(300 * time.Millisecond)
	if st := c.Status(); st.State != StatePlaying {
		t.Fatalf("%+v", st)
	}
	// Timestamps run on across the stop.
	last := time.Duration(-1)
	for _, x := range rec.all() {
		if x.pkt != nil && x.pkt.Track == 0 {
			if x.pkt.DTS <= last {
				t.Fatalf("DTS %s after %s", x.pkt.DTS, last)
			}
			last = x.pkt.DTS
		}
	}
	if len(r.m.Channels()) != 1 || r.m.Push("none") != nil {
		t.Error("lists")
	}
}

func TestLateTicksPause(t *testing.T) {
	r := newRig(t)
	r.apply(config.StreamChannel{Name: "tv/late", Library: "lib", Items: []string{"films/bbb"}})
	r.run(100 * time.Millisecond)
	c := r.m.Channel("tv/late")
	// The timer comes 2s late, as a stalled read would make it: the
	// channel goes on from there rather than sending 2s at once.
	c.mu.Lock()
	c.tick(r.clock.Now().Add(2 * time.Second))
	c.mu.Unlock()
	if c.underruns.Load() != 1 {
		t.Errorf("underruns %d", c.underruns.Load())
	}
}
