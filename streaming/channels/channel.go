package channels

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// The states of a channel.
const (
	// StatePlaying is a channel sending an item.
	StatePlaying = "playing"
	// StateLoading is a channel with nothing sent yet of the item it
	// loads.
	StateLoading = "loading"
	// StateIdle is a channel with nothing to play now: its list played
	// out, its items not served, or between its slots; it stays published.
	StateIdle = "idle"
	// StateWaiting is a channel whose stream has another publisher, or
	// that the hub has no room for; it tries again.
	StateWaiting = "waiting"
	// StateStopped is a channel the Admin API stopped.
	StateStopped = "stopped"
)

// The pace of a channel.
const (
	// sendAhead is how far ahead of its time a packet may be sent, so
	// that a tick sends what falls due within it at once.
	sendAhead = 5 * time.Millisecond
	// maxLate is how late a packet may be sent before the channel takes
	// it that it paused, its reads too slow, and sends on from there
	// rather than catching up.
	maxLate = time.Second
	// readSpan is the most media one read on the pool takes.
	readSpan = time.Second
	// retryBusy is how soon a read or a load the library's pool refused
	// is tried again; retryIdle how soon a channel with nothing to play,
	// or whose stream is taken, tries again.
	retryBusy = 200 * time.Millisecond
	retryIdle = 5 * time.Second
	// minGap is the least time between the last packet sent and the first
	// of an item cut in.
	minGap = time.Millisecond
	// slotSlack is how far past a slot's start an item chosen to follow
	// another still starts it.
	slotSlack = 50 * time.Millisecond
)

// settings is a channel's config, compiled: immutable, and replaced whole
// by a reload.
type settings struct {
	name      string
	library   string
	items     []string
	shuffle   bool
	loop      bool
	slots     []slot
	loc       *time.Location
	rendition string
	readAhead time.Duration
	maxBytes  int64
}

// slot is a slot of a channel's schedule.
type slot struct {
	at   config.ChannelAt
	item string
}

func compile(c *config.StreamChannel) (*settings, error) {
	loc, err := c.Location()
	if err != nil {
		return nil, fmt.Errorf("channel %s: %w", c.Name, err)
	}
	s := &settings{name: c.Name, library: c.Library, items: slices.Clone(c.Items), shuffle: c.Shuffled(), loop: c.Looped(),
		loc: loc, rendition: c.Rendition, readAhead: c.ReadAheadOr(), maxBytes: c.BufferBytes()}
	for _, sl := range c.Schedule {
		at, err := config.ParseChannelAt(sl.At)
		if err != nil {
			return nil, fmt.Errorf("channel %s: %w", c.Name, err)
		}
		s.slots = append(s.slots, slot{at: at, item: sl.Item})
	}
	return s, nil
}

// entry is what a channel has read and not sent: a packet, or the start
// of an item, whose tracks the stream takes there.
type entry struct {
	pkt   *av.Packet
	start *source
}

// Channel is a channel of the streaming role: items of a library published
// into the hub under the channel's name. Its methods never block.
type Channel struct {
	m    *Manager
	name string
	set  atomic.Pointer[settings]

	mu      sync.Mutex
	running bool
	removed bool
	// gen counts what drops the items read and being read: a stop, a
	// skip, a slot cutting in. An outcome of a read or a load of another
	// gen is dropped.
	gen uint64
	pub *hub.Publisher
	// tracks are the stream's tracks, as last set.
	tracks  []*av.Track
	pubErr  string
	timer   Timer
	timerAt time.Time
	timerID uint64

	// The timeline: a packet whose DTS is d is due at
	// anchorWall + (d - anchorDTS).
	anchored   bool
	anchorWall time.Time
	anchorDTS  time.Duration
	sentDTS    time.Duration
	// lastEnd is where the presentation of the last item read ends, where
	// the next one's starts; nextDTS the least DTS of the next item's
	// first packet, after a cut; lastDelay the last item's delay.
	lastEnd, nextDTS, lastDelay time.Duration
	// queued and sent are, by track, past the DTS of the last packet
	// queued and sent: where the next item's packets may start.
	queued, sent []time.Duration

	queue  []entry
	qBytes int64
	qEnd   time.Duration // the DTS of the last packet queued

	src     *source // the item being read
	reading bool    // a read of src on the pool
	loading bool    // an item's tables being read
	fails   int     // items that failed in a row
	now     *playing
	next    *source // the last item queued, until it starts
	// waitUntil is when a channel that found nothing to play, or a busy
	// pool, chooses again.
	waitUntil time.Time
	list      listState
	// slotDone is the start of the latest slot started, or passed over:
	// a slot whose start is later than it starts when it falls due.
	slotDone time.Time
	lastErr  string

	played, failed, underruns atomic.Uint64
}

// playing is the item a channel plays.
type playing struct {
	src     *source
	started time.Time // when its first packet was due
}

func newChannel(m *Manager, s *settings) *Channel {
	c := &Channel{m: m, name: s.name}
	c.set.Store(s)
	return c
}

// Name returns the channel's name, its stream's path.
func (c *Channel) Name() string { return c.name }

// Start starts a channel that is stopped: it claims its stream and plays,
// its list from where it was. It does nothing to one that runs.
func (c *Channel) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running || c.removed {
		return
	}
	c.running = true
	c.gen++
	if c.list.pos >= len(c.list.order) {
		// A list played out, without loop: from the first again.
		c.list.pos = 0
	}
	now := c.m.clock.Now()
	c.passSlots(now)
	c.claim(now)
}

// Stop stops the channel: its stream is unpublished and nothing more is
// read, until Start.
func (c *Channel) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.halt()
}

// halt stops the channel. c.mu is held.
func (c *Channel) halt() {
	if !c.running {
		return
	}
	c.running = false
	c.rewind()
	c.drop()
	c.now = nil
	c.disarm()
	if c.pub != nil {
		c.pub.Close()
		c.pub = nil
	}
}

// remove ends the channel for good, as a reload that leaves it out does.
func (c *Channel) remove() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.halt()
	c.removed = true
}

// apply moves the channel to s, from its next item. A slot whose start has
// passed is not started by it.
func (c *Channel) apply(s *settings) {
	c.set.Store(s)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	now := c.m.clock.Now()
	c.passSlots(now)
	if c.pub != nil && c.src == nil && !c.loading && c.next == nil {
		// Nothing chosen to come: choose now, from the new settings.
		c.waitUntil = time.Time{}
		c.arm(now)
	}
}

// Skip ends what the channel plays, and plays item next, or the next item
// of its list when item is "".
func (c *Channel) Skip(item string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return ErrStopped
	}
	now := c.m.clock.Now()
	if item != "" {
		l := medialib.Lookup(c.m.o.Libraries, c.set.Load().library)
		if l == nil || l.Item(item) == nil {
			return ErrNoItem
		}
		c.cut(now, &choice{item: item, from: fromAdmin})
		return nil
	}
	c.cut(now, nil)
	return nil
}

// rewind puts the list back where it was when the item playing was taken
// from it, so that items read ahead and dropped are played. c.mu is held.
func (c *Channel) rewind() {
	if c.now != nil {
		c.list = c.now.src.listAfter.clone()
	}
}

// drop drops what was read and is being read. c.mu is held.
func (c *Channel) drop() {
	c.gen++
	c.queued = slices.Clone(c.sent)
	c.queue, c.qBytes = nil, 0
	c.src, c.next = nil, nil
	c.reading, c.loading = false, false
}

// claim makes the channel the publisher of its stream, its tracks to come,
// and starts playing; or tries again later when the stream has another
// publisher, or the hub no room. c.mu is held.
func (c *Channel) claim(now time.Time) {
	pub, err := c.m.o.Hub.Reserve(c.name, hub.PublishInfo{Protocol: "channel"})
	if err != nil {
		c.pubErr = err.Error()
		c.arm(now.Add(retryIdle))
		return
	}
	c.pub, c.pubErr, c.tracks = pub, "", nil
	c.advance(now, true)
}

// channelNow is the channel time now: where the timeline is at, or, before
// anything was sent, where it starts.
func (c *Channel) channelNow(now time.Time) time.Duration {
	if !c.anchored {
		return c.origin()
	}
	return c.anchorDTS + now.Sub(c.anchorWall)
}

// origin is where the timeline starts once the first packet is sent: the
// first item queued, or the next item's.
func (c *Channel) origin() time.Duration {
	if len(c.queue) > 0 {
		if h := c.queue[0]; h.start != nil {
			return h.start.base
		} else {
			return h.pkt.DTS
		}
	}
	return c.nextDTS
}

// wallOf is when channel time d falls due: before anything was sent, as
// though the first packet were sent now.
func (c *Channel) wallOf(now time.Time, d time.Duration) time.Time {
	if !c.anchored {
		return now.Add(d - c.origin())
	}
	return c.anchorWall.Add(d - c.anchorDTS)
}

// advance chooses the item to play after what was read, and loads it; join
// lets it join a slot under way. With nothing to play the channel idles,
// trying again at the next slot, or after retryIdle. c.mu is held.
func (c *Channel) advance(now time.Time, join bool) {
	// When the next item's presentation starts, if its delay is the last
	// one's.
	at := c.wallOf(now, max(c.lastEnd, max(c.nextDTS, c.channelNow(now))+c.lastDelay))
	ch := c.choose(at, join)
	if ch == nil {
		c.idle(now)
		return
	}
	c.load(now, *ch)
}

// idle arms the channel's timer for when it has something to play: its
// next slot, or retryIdle from now. c.mu is held.
func (c *Channel) idle(now time.Time) {
	c.wait(now.Add(retryIdle))
	if next, ok := c.nextSlot(now); ok {
		c.arm(next)
	}
}

// wait makes the channel choose its next item no sooner than at.
// c.mu is held.
func (c *Channel) wait(at time.Time) {
	c.waitUntil = at
	c.arm(at)
}

// choose returns the item to play from at, a time to come or now: a slot
// that starts then, or has started and not been, joined where it is with
// join; or the next item of the list; nil when there is none. A slot
// started before a time to come is left to the channel's timer, which
// cuts in at its start. c.mu is held.
func (c *Channel) choose(at time.Time, join bool) *choice {
	if start, sl, ok := c.dueSlot(at); ok {
		if off := at.Sub(start); join || off <= slotSlack {
			c.slotDone = start
			return &choice{item: sl.item, from: fromSchedule, offset: max(off, 0), slot: start}
		}
	}
	id, ok := c.list.next(c.set.Load(), c.m.shuffle)
	if !ok {
		return nil
	}
	return &choice{item: id, from: fromList}
}

// dueSlot returns the slot with the latest start at or before at, when it
// is later than slotDone and its item, when known, would still be playing
// at at. c.mu is held.
func (c *Channel) dueSlot(at time.Time) (time.Time, slot, bool) {
	s := c.set.Load()
	var best time.Time
	var bs slot
	found := false
	for _, sl := range s.slots {
		if start, ok := sl.at.Prev(at, s.loc); ok && (!found || !start.Before(best)) {
			best, bs, found = start, sl, true
		}
	}
	if !found || !best.After(c.slotDone) {
		return time.Time{}, slot{}, false
	}
	if d := c.itemDuration(bs.item); d > 0 && at.Sub(best) >= d {
		return time.Time{}, slot{}, false
	}
	return best, bs, true
}

// passSlots marks the slots whose start has passed as started, but for
// one under way, which the channel's next choice joins. c.mu is held.
func (c *Channel) passSlots(now time.Time) {
	s := c.set.Load()
	for _, sl := range s.slots {
		start, ok := sl.at.Prev(now, s.loc)
		if !ok || !start.After(c.slotDone) {
			continue
		}
		if d := c.itemDuration(sl.item); d > 0 && now.Sub(start) < d && c.now == nil {
			continue // under way, to be joined
		}
		c.slotDone = start
	}
}

// nextSlot returns the first slot start after t.
func (c *Channel) nextSlot(t time.Time) (time.Time, bool) {
	s := c.set.Load()
	var best time.Time
	found := false
	for _, sl := range s.slots {
		if start, ok := sl.at.Next(t, s.loc); ok && (!found || start.Before(best)) {
			best, found = start, true
		}
	}
	return best, found
}

// itemDuration returns how long the library's item id plays, 0 when it is
// not known.
func (c *Channel) itemDuration(id string) time.Duration {
	if l := medialib.Lookup(c.m.o.Libraries, c.set.Load().library); l != nil {
		if it := l.Item(id); it != nil {
			return it.Duration
		}
	}
	return 0
}

// load starts loading ch's item: its tracks picked and their sample tables
// read, then placed after what was read. c.mu is held.
func (c *Channel) load(now time.Time, ch choice) {
	s := c.set.Load()
	l := medialib.Lookup(c.m.o.Libraries, s.library)
	var err error
	var src *source
	switch {
	case l == nil:
		err = errNoLibrary
	case l.Catalog().Scanned.IsZero():
		// The first scan has not ended: the item is chosen again then.
		c.list.undo(ch)
		c.lastErr = errNotReady.Error()
		c.wait(now.Add(retryBusy))
		return
	default:
		it := l.Item(ch.item)
		if it == nil {
			err = fmt.Errorf("%s: %w", ch.item, ErrNoItem)
			break
		}
		if src, err = newSource(l, it, s.rendition, ch); err != nil {
			err = fmt.Errorf("%s: %w", ch.item, err)
		}
	}
	if err != nil {
		c.loadFailed(now, err)
		return
	}
	src.listAfter = c.list.clone()
	c.loading = true
	gen := c.gen
	// The library calls back at once for a table it keeps: under c.mu,
	// which loaded takes, so that call goes on a goroutine of its own.
	var inside atomic.Bool
	inside.Store(true)
	src.load(func(err error) {
		if inside.Load() {
			go c.loaded(gen, src, err)
			return
		}
		c.loaded(gen, src, err)
	})
	inside.Store(false)
}

// loadFailed skips an item that cannot be played, choosing the next, or
// idles once every item in a row failed. c.mu is held.
func (c *Channel) loadFailed(now time.Time, err error) {
	c.failed.Add(1)
	c.fails++
	c.lastErr = err.Error()
	c.m.o.Log.Warnf("channel %s: skipped: %v", c.name, err)
	if c.fails > len(c.set.Load().items) {
		c.fails = 0
		c.idle(now)
		return
	}
	c.advance(now, c.now == nil && len(c.queue) == 0)
}

// loaded places src on the timeline once its tables are read, queues its
// start and reads it.
func (c *Channel) loaded(gen uint64, src *source, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen || !c.running {
		return
	}
	c.loading = false
	now := c.m.clock.Now()
	if errors.Is(err, medialib.ErrBusy) {
		c.list.undo(src.choice)
		c.wait(now.Add(retryBusy))
		return
	}
	if err != nil {
		c.loadFailed(now, fmt.Errorf("%s: %w", src.choice.item, err))
		return
	}
	dts := c.nextDTS
	if c.anchored {
		dts = max(dts, c.channelNow(now))
	}
	if !src.place(c.lastEnd, dts, c.queued) {
		c.loadFailed(now, fmt.Errorf("%s: nothing to play from %s", src.choice.item, src.choice.offset))
		return
	}
	// A list item, or a slot's, is cut where the next slot starts.
	pres := src.base + src.delay
	at := c.wallOf(now, pres)
	if next, ok := c.nextSlot(at); ok {
		if cut := pres + next.Sub(at); cut < src.end {
			src.cut = cut
		}
	}
	c.lastDelay = src.delay
	c.fails = 0
	c.src = src
	c.next = src
	c.queue = append(c.queue, entry{start: src})
	c.qEnd = src.base
	c.read(now)
	c.arm(now)
}

// buffered is how far ahead of now what was read reaches.
func (c *Channel) buffered(now time.Time) time.Duration {
	if len(c.queue) == 0 {
		return 0
	}
	return c.qEnd - max(c.channelNow(now), c.sentDTS)
}

// wantMore reports whether less than read_ahead and max_buffer_bytes is
// read ahead. c.mu is held.
func (c *Channel) wantMore(now time.Time) bool {
	s := c.set.Load()
	return c.buffered(now) < s.readAhead && c.qBytes < s.maxBytes
}

// read reads more of the item being read on the library's pool, when
// less than read_ahead and max_buffer_bytes is read ahead. c.mu is held.
func (c *Channel) read(now time.Time) {
	s := c.set.Load()
	src := c.src
	if src == nil || c.reading || !c.wantMore(now) {
		return
	}
	c.reading = true
	gen := c.gen
	budget := max(s.maxBytes-c.qBytes, 1)
	run := func() {
		pkts, n, err := src.read(readSpan, budget, s.maxBytes)
		c.readDone(gen, src, pkts, n, err)
	}
	busy := func() { c.readDone(gen, src, nil, 0, medialib.ErrBusy) }
	if !src.lib.Submit(run, busy) {
		c.reading = false
		c.arm(now.Add(retryBusy))
	}
}

// readDone queues what a read gave, and loads the next item once the one
// read has been read whole.
func (c *Channel) readDone(gen uint64, src *source, pkts []*av.Packet, n int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen || !c.running {
		return
	}
	c.reading = false
	now := c.m.clock.Now()
	for _, p := range pkts {
		c.queue = append(c.queue, entry{pkt: p})
		c.qEnd = p.DTS
		for len(c.queued) <= p.Track {
			c.queued = append(c.queued, 0)
		}
		c.queued[p.Track] = p.DTS + minGap
	}
	c.qBytes += n
	switch {
	case errors.Is(err, medialib.ErrBusy):
		c.arm(now.Add(retryBusy))
		return
	case err != nil:
		// The item ends where it was read to.
		c.m.o.Log.Warnf("channel %s: %s ends early: %v", c.name, src.choice.item, err)
		c.lastErr = err.Error()
		src.done = true
		src.end = max(c.qEnd, src.base)
		src.cut, src.cutHit = 0, false
	}
	if src.done {
		c.lastEnd, c.nextDTS = src.end, 0
		if src.cutHit {
			// The next is shown from where this one was cut.
			c.lastEnd = src.cut
		}
		c.src = nil
		if c.wantMore(now) {
			c.advance(now, false)
		}
	} else {
		c.read(now)
	}
	if len(c.queue) > 0 {
		c.arm(now)
	}
}

// cut ends what plays now, and plays ch, or the next item when ch is nil:
// a skip, or a slot falling due. c.mu is held.
func (c *Channel) cut(now time.Time, ch *choice) {
	c.rewind()
	c.drop()
	c.lastEnd = 0
	c.nextDTS = c.channelNow(now)
	if c.anchored && c.nextDTS <= c.sentDTS {
		c.nextDTS = c.sentDTS + minGap
	}
	if c.pub == nil {
		return
	}
	if ch == nil {
		c.advance(now, false)
		return
	}
	c.load(now, *ch)
}

// arm sets the channel's timer for at, unless it is set for earlier.
// c.mu is held.
func (c *Channel) arm(at time.Time) {
	if c.timer != nil && !c.timerAt.After(at) {
		return
	}
	c.disarm()
	c.timerID++
	id := c.timerID
	c.timerAt = at
	c.timer = c.m.clock.AfterFunc(max(at.Sub(c.m.clock.Now()), 0), func() { c.fire(id) })
}

// disarm stops the channel's timer. c.mu is held.
func (c *Channel) disarm() {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// fire is the channel's timer.
func (c *Channel) fire(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id != c.timerID {
		return
	}
	c.timer = nil
	if !c.running {
		return
	}
	c.tick(c.m.clock.Now())
}

// tick does what is due: a slot cutting in, the packets due sent, the next
// item chosen and read, and the timer set for what falls due next. c.mu is
// held.
func (c *Channel) tick(now time.Time) {
	if c.pub == nil {
		c.claim(now)
		return
	}
	if start, sl, ok := c.dueSlot(now); ok {
		c.slotDone = start
		c.cut(now, &choice{item: sl.item, from: fromSchedule, offset: max(now.Sub(start), 0), slot: start})
		return
	}
	c.send(now)
	if c.src == nil && !c.loading && !now.Before(c.waitUntil) && c.wantMore(now) {
		c.advance(now, len(c.queue) == 0)
	}
	c.read(now)
	if len(c.queue) == 0 && c.src == nil && !c.loading {
		c.now = nil
	}
	if len(c.queue) > 0 {
		e := c.queue[0]
		if e.start != nil {
			c.arm(now)
		} else {
			c.arm(c.wallOf(now, e.pkt.DTS).Add(-sendAhead))
		}
	}
	if next, ok := c.nextSlot(now); ok {
		c.arm(next)
	}
}

// send sends the packets that are due, and starts the items whose first
// packet is. c.mu is held.
func (c *Channel) send(now time.Time) {
	i := 0
	for ; i < len(c.queue); i++ {
		e := c.queue[i]
		if e.start != nil {
			if !c.anchored {
				c.anchored, c.anchorWall, c.anchorDTS = true, now, e.start.base
			}
			if !sameTracks(c.tracks, e.start.avs) {
				// New codec configurations: players take them, and HLS
				// marks a discontinuity. An item whose tracks are the
				// last one's carries on without, its timestamps running
				// on.
				c.pub.SetTracks(e.start.avs)
				c.tracks = e.start.avs
			}
			c.now = &playing{src: e.start, started: c.wallOf(now, e.start.base)}
			if c.next == e.start {
				c.next = nil
			}
			c.played.Add(1)
			c.lastErr = ""
			c.m.o.Log.Infof("channel %s: playing %s (%s)", c.name, e.start.item.ID, e.start.choice.from)
			continue
		}
		due := c.wallOf(now, e.pkt.DTS)
		if due.After(now.Add(sendAhead)) {
			break
		}
		if now.Sub(due) > maxLate {
			// It paused, its reads too slow: it goes on from here.
			c.underruns.Add(1)
			c.anchorWall, c.anchorDTS = now, e.pkt.DTS
			if c.now != nil {
				c.now.started = c.now.started.Add(now.Sub(due))
			}
		}
		c.pub.WritePacket(e.pkt)
		c.sentDTS = max(c.sentDTS, e.pkt.DTS)
		for len(c.sent) <= e.pkt.Track {
			c.sent = append(c.sent, 0)
		}
		c.sent[e.pkt.Track] = e.pkt.DTS + minGap
		c.qBytes -= int64(len(e.pkt.Data))
	}
	if i > 0 {
		clear(c.queue[:i])
		c.queue = c.queue[i:]
		if len(c.queue) == 0 {
			c.queue = nil
		}
	}
}

// sameTracks reports whether a and b are the same tracks: the same codecs
// with the same configurations.
func sameTracks(a, b []*av.Track) bool {
	return slices.EqualFunc(a, b, func(x, y *av.Track) bool {
		return x.Codec == y.Codec && bytes.Equal(x.Config, y.Config) && x.Width == y.Width && x.Height == y.Height &&
			x.SampleRate == y.SampleRate && x.Channels == y.Channels
	})
}

// listState is where a channel is in its list.
type listState struct {
	items    []string // the list order and pos are of
	shuffled bool     // order is shuffled
	order    []int    // indexes of items, in the order they play
	pos      int      // the next in order
	last     string   // the last taken
	taken    bool     // last was taken, and not given back
}

// clone returns a copy of l that next does not change.
func (l listState) clone() listState {
	l.order = slices.Clone(l.order)
	return l
}

// next returns the next item of s's list, and false when there is none:
// an empty list, or one played out without loop. A list changed by a
// reload goes on after the item taken last, when it still has it.
func (l *listState) next(s *settings, shuffle func([]int)) (string, bool) {
	if !slices.Equal(l.items, s.items) || l.shuffled != s.shuffle || len(l.order) != len(s.items) {
		l.reset(s, shuffle)
	}
	if len(l.items) == 0 {
		return "", false
	}
	if l.pos >= len(l.order) {
		if !s.loop {
			return "", false
		}
		l.pos = 0
		if s.shuffle {
			shuffle(l.order)
			if len(l.order) > 1 && l.items[l.order[0]] == l.last {
				// Not the same item twice in a row, across a round.
				l.order[0], l.order[len(l.order)-1] = l.order[len(l.order)-1], l.order[0]
			}
		}
	}
	l.last, l.taken = l.items[l.order[l.pos]], true
	l.pos++
	return l.last, true
}

// reset makes the list's order from s, going on after the item taken last
// when s's list has it and is not shuffled.
func (l *listState) reset(s *settings, shuffle func([]int)) {
	l.items, l.shuffled = s.items, s.shuffle
	l.order = make([]int, len(s.items))
	for i := range l.order {
		l.order[i] = i
	}
	l.pos = 0
	if s.shuffle {
		shuffle(l.order)
		return
	}
	if i := slices.Index(s.items, l.last); i >= 0 && l.last != "" {
		l.pos = i + 1
	}
}

// undo gives back the item ch took from the list, to be taken again.
func (l *listState) undo(ch choice) {
	if ch.from == fromList && l.taken && l.pos > 0 && l.pos <= len(l.order) && l.items[l.order[l.pos-1]] == ch.item {
		l.pos--
		l.taken = false
	}
}
