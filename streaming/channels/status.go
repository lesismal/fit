package channels

import "time"

// ChannelStatus is what a channel is doing, as the Admin API shows it.
type ChannelStatus struct {
	Name    string `json:"name"`
	Library string `json:"library"`
	// State is playing, loading, idle, waiting or stopped.
	State string `json:"state"`
	// Error is why the channel waits, or the last item it skipped or
	// ended early.
	Error    string `json:"error,omitempty"`
	Order    string `json:"order"`
	Loop     bool   `json:"loop"`
	Items    int    `json:"items"`
	Schedule int    `json:"schedule"`
	TimeZone string `json:"time_zone"`
	// Now is the item playing, Next the item read to follow it, and
	// NextSlot the schedule's next start.
	Now      *ItemStatus `json:"now,omitempty"`
	Next     *ItemStatus `json:"next,omitempty"`
	NextSlot *SlotStatus `json:"next_slot,omitempty"`
	// Buffered is how far ahead of what was sent the channel has read,
	// in seconds, and BufferedBytes its bytes.
	Buffered      float64 `json:"buffered"`
	BufferedBytes int64   `json:"buffered_bytes"`
	// Played, Failed and Underruns are counters: items started, items
	// skipped as unplayable, and pauses for reads too slow.
	Played    uint64 `json:"played"`
	Failed    uint64 `json:"failed"`
	Underruns uint64 `json:"underruns"`
	// Push is the channel's push targets.
	Push []PushStatus `json:"push"`
}

// ItemStatus is an item a channel plays, or will.
type ItemStatus struct {
	Item      string `json:"item"`
	Title     string `json:"title"`
	Rendition string `json:"rendition"`
	// From is list, schedule or admin.
	From string `json:"from"`
	// Slot is the start of the slot it plays, from the schedule.
	Slot time.Time `json:"slot,omitzero"`
	// Offset is where in the item it started, Duration its length and
	// Position where it is now, in seconds.
	Offset   float64 `json:"offset"`
	Duration float64 `json:"duration"`
	Position float64 `json:"position,omitempty"`
	// Started is when its first packet was due, or will be.
	Started time.Time `json:"started"`
}

// SlotStatus is a slot of a schedule to come.
type SlotStatus struct {
	At   time.Time `json:"at"`
	Item string    `json:"item"`
}

// Status returns what the channel is doing.
func (c *Channel) Status() ChannelStatus {
	s := c.set.Load()
	order := "sequence"
	if s.shuffle {
		order = "shuffle"
	}
	st := ChannelStatus{Name: c.name, Library: s.library, Order: order, Loop: s.loop, Items: len(s.items),
		Schedule: len(s.slots), TimeZone: s.loc.String(), Played: c.played.Load(), Failed: c.failed.Load(),
		Underruns: c.underruns.Load()}
	c.mu.Lock()
	now := c.m.clock.Now()
	switch {
	case !c.running:
		st.State = StateStopped
	case c.pub == nil:
		st.State, st.Error = StateWaiting, c.pubErr
	case c.now != nil:
		st.State = StatePlaying
	case c.loading || c.src != nil || len(c.queue) > 0:
		st.State = StateLoading
	default:
		st.State = StateIdle
	}
	if st.Error == "" {
		st.Error = c.lastErr
	}
	if c.now != nil {
		st.Now = c.itemStatus(c.now.src, c.now.started)
		st.Now.Position = max(now.Sub(st.Now.Started), 0).Seconds() + st.Now.Offset
	}
	if c.next != nil && (c.now == nil || c.next != c.now.src) {
		st.Next = c.itemStatus(c.next, c.wallOf(now, c.next.base))
	}
	if c.running {
		st.Buffered = max(c.buffered(now), 0).Seconds()
		st.BufferedBytes = c.qBytes
		if next, ok := c.nextSlot(now); ok {
			for _, sl := range s.slots {
				if at, ok := sl.at.Next(now, s.loc); ok && at.Equal(next) {
					st.NextSlot = &SlotStatus{At: next, Item: sl.item}
					break
				}
			}
		}
	}
	c.mu.Unlock()
	st.Push = c.m.pushesOf(c.name)
	return st
}

// itemStatus is src as Status shows it. c.mu is held.
func (c *Channel) itemStatus(src *source, started time.Time) *ItemStatus {
	is := &ItemStatus{Item: src.item.ID, Title: src.item.Title, From: src.choice.from, Offset: src.choice.offset.Seconds(),
		Duration: src.item.Duration.Seconds(), Started: started}
	if src.rend != nil {
		is.Rendition = src.rend.Name
	}
	if src.choice.from == fromSchedule {
		is.Slot = src.choice.slot
	}
	return is
}
