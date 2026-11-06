package channels

import (
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/hub"
)

type dropSink struct{}

func (dropSink) OnTracks([]*av.Track) {}
func (dropSink) OnPacket(*av.Packet)  {}
func (dropSink) OnClose(error)        {}

// Subscribers counts the hub's subscribers of a stream; none for a nil
// Manager, one without a hub, or a stream the hub has not.
func TestSubscribers(t *testing.T) {
	var none *Manager
	if n := none.Subscribers("tv/a"); n != 0 {
		t.Errorf("a nil manager: %d", n)
	}
	if n := New(Options{}).Subscribers("tv/a"); n != 0 {
		t.Errorf("no hub: %d", n)
	}
	h := hub.New(hub.Config{})
	t.Cleanup(h.Close)
	m := New(Options{Hub: h})
	if n := m.Subscribers("tv/a"); n != 0 {
		t.Errorf("no stream: %d", n)
	}
	sub, err := h.Subscribe("tv/a", dropSink{}, hub.SubscribeOptions{Wait: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if n := m.Subscribers("tv/a"); n != 1 {
		t.Errorf("one subscriber: %d", n)
	}
	sub.Close()
}
