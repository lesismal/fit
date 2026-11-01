package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// A receiver that does not release holds the sender back within the
// window; releasing lets the rest through, and the end comes last.
func TestOnDataRelease(t *testing.T) {
	const window = 4 << 10
	got := make(chan int, 1024)
	ended := make(chan error, 1)
	var st0 atomic.Pointer[Stream]
	d, _, _ := pair(t, Options{Window: window}, Handlers{}, Handlers{
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			st0.Store(st)
			st.OnData(func(data []byte, err error) {
				if err != nil {
					ended <- err
					return
				}
				got <- len(data)
			})
			<-ctx.Done()
			return nil, nil
		},
	})
	ctx := ctxT(t)
	st, err := d.OpenStream(ctx, Head{Method: "/x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	piece := bytes.Repeat([]byte("y"), 512)
	sent := 0
	for {
		ok, err := st.TrySend(piece)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		sent += len(piece)
	}
	if sent < window || sent > window+len(piece) {
		t.Fatalf("sent %d with a window of %d", sent, window)
	}
	credit := make(chan struct{})
	if !st.OnCredit(func() { close(credit) }) {
		t.Fatal("credit without a release")
	}
	n := 0
	for n < sent {
		n += <-got
	}
	st0.Load().Release(n)
	select {
	case <-credit:
	case <-time.After(2 * time.Second):
		t.Fatal("no credit after the release")
	}
	_ = st.CloseSend()
	select {
	case err := <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("ended with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no end")
	}
}

// Pump moves frames between two streams with credit linked: a slow reader
// at the far end holds the writer at the near end, and the end arrives
// after every frame.
func TestPumpBackpressure(t *testing.T) {
	const window = 8 << 10
	opts := Options{Window: window}
	// The far side reads slowly.
	var read atomic.Int64
	slow := make(chan struct{})
	farD, farL, _ := pair(t, opts, Handlers{}, Handlers{
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			for {
				<-slow
				b, err := st.Recv(ctx)
				if errors.Is(err, io.EOF) {
					return []byte("far-done"), nil
				}
				if err != nil {
					return nil, err
				}
				read.Add(int64(len(b)))
			}
		},
	})
	_ = farL
	ctx := ctxT(t)
	far, err := farD.OpenStream(ctx, Head{Method: "/far"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The near side relays what its opener sends to far.
	ended := make(chan error, 1)
	nearD, _, _ := pair(t, opts, Handlers{}, Handlers{
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			Pump(st, far, nil, func(err error, frames int) {
				if errors.Is(err, io.EOF) {
					_ = far.CloseSend()
				}
				ended <- err
			})
			<-ctx.Done()
			return nil, nil
		},
	})
	near, err := nearD.OpenStream(ctx, Head{Method: "/near"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	piece := bytes.Repeat([]byte("x"), 1024)
	sent := 0
	deadline := time.After(time.Second)
	// Without the far side reading, the writer stops at about two windows
	// (near's and far's) and a frame or two.
fill:
	for {
		ok, err := near.TrySend(piece)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			sent += len(piece)
			continue
		}
		credit := make(chan struct{})
		if near.OnCredit(func() { close(credit) }) {
			select {
			case <-credit:
			case <-deadline:
				break fill
			}
		}
	}
	if sent > 3*window+4*len(piece) {
		t.Fatalf("sent %d bytes past the windows with a reader that reads nothing", sent)
	}
	// The reader reads: everything goes through, then the end.
	close(slow)
	for range 64 {
		ok, err := near.TrySend(piece)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			credit := make(chan struct{})
			if near.OnCredit(func() { close(credit) }) {
				<-credit
			}
			continue
		}
		sent += len(piece)
	}
	_ = near.CloseSend()
	select {
	case err := <-ended:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("pump ended with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not end")
	}
	for i := 0; read.Load() != int64(sent); i++ {
		if i > 300 {
			t.Fatalf("read %d of %d", read.Load(), sent)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
