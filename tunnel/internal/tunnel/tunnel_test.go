package tunnel

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/proto"
)

// fakeLink records the frames an end sends.
type fakeLink struct {
	mu     sync.Mutex
	frames []proto.Method
}

func (l *fakeLink) SendTunnel(m proto.Method, _ uint64, _ []byte) error {
	l.mu.Lock()
	l.frames = append(l.frames, m)
	l.mu.Unlock()
	return nil
}

func TestLimit(t *testing.T) {
	limit := NewLimit(2)
	a, b := NewTable(limit), NewTable(limit)
	l := &fakeLink{}
	if err := a.Add(New(a, l, 1, true)); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(New(a, l, 1, true)); !errors.Is(err, ErrTaken) {
		t.Fatalf("a taken ID: %v", err)
	}
	e := New(b, l, 2, true)
	if err := b.Add(e); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(New(b, l, 3, true)); !errors.Is(err, ErrFull) {
		t.Fatalf("past the limit, across tables: %v", err)
	}
	e.Close("done")
	if limit.Open() != 1 {
		t.Fatalf("open %d", limit.Open())
	}
	if err := b.Add(New(b, l, 3, true)); err != nil {
		t.Fatalf("room again: %v", err)
	}
}

func TestCloseIdle(t *testing.T) {
	tb := NewTable(nil)
	l := &fakeLink{}
	idle, busy := New(tb, l, 1, true), New(tb, l, 2, true)
	_ = tb.Add(idle)
	_ = tb.Add(busy)
	idle.active.Store(time.Now().Add(-time.Hour).UnixNano())
	tb.CloseIdle(time.Minute)
	if tb.Get(1) != nil || tb.Get(2) == nil {
		t.Fatalf("left %v", tb.List())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.frames) != 1 || l.frames[0] != proto.MethodTunnelClose {
		t.Fatalf("frames %v", l.frames)
	}
}

// brokenLink fails every send.
type brokenLink struct{}

func (brokenLink) SendTunnel(proto.Method, uint64, []byte) error { return errors.New("gone") }

// A tunnel whose link fails as it sends is closed.
func TestLocalLinkFails(t *testing.T) {
	tb := NewTable(nil)
	e := New(tb, brokenLink{}, 1, true)
	_ = tb.Add(e)
	e.local([]byte("data"))
	if tb.Get(1) != nil {
		t.Fatal("the tunnel is open")
	}
	(&Handler{}).OnPriorityData(nil, nil)
}

func TestCloseLink(t *testing.T) {
	tb := NewTable(nil)
	a, b := &fakeLink{}, &fakeLink{}
	for id, l := range map[uint64]*fakeLink{3: a, 1: a, 2: b} {
		_ = tb.Add(New(tb, l, id, true))
	}
	if list := tb.List(); len(list) != 3 || list[0].ID != 1 || list[1].ID != 2 || list[2].ID != 3 {
		t.Fatalf("list %v", list)
	}
	tb.CloseLink(a, "link lost")
	if tb.Len() != 1 || tb.Get(2) == nil {
		t.Fatalf("left %v", tb.List())
	}
}
