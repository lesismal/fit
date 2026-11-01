package wire

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

// A session counts the bytes of every frame each way, as they crossed, and
// a stream the bytes of its data frames; a ping's pong records its round
// trip.
func TestTrafficAndRTT(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 300) // under CompressMin, so sent as it is
	d, l, _ := pair(t, Options{}, Handlers{}, Handlers{
		Request: func(_ context.Context, req *Request) ([]byte, error) { return req.Payload, nil },
		Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			for {
				_, err := st.Recv(ctx)
				if err == io.EOF {
					return nil, st.Send(ctx, bytes.Repeat([]byte{'y'}, 500))
				}
				if err != nil {
					return nil, err
				}
			}
		},
	})
	if in, out := d.Traffic(); in != 0 || out != 0 {
		t.Fatalf("before any frame: %d %d", in, out)
	}
	if _, err := d.Call(ctxT(t), Head{Method: "/echo"}, payload); err != nil {
		t.Fatal(err)
	}
	din, dout := d.Traffic()
	lin, lout := l.Traffic()
	if dout < 300 || din < 300 || dout != lin || din != lout {
		t.Fatalf("dialer in %d out %d, listener in %d out %d", din, dout, lin, lout)
	}

	st, err := d.OpenStream(ctxT(t), Head{Method: "/s"}, []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctxT(t), payload); err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctxT(t), payload[:100]); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseSend(); err != nil {
		t.Fatal(err)
	}
	b, err := st.Recv(ctxT(t))
	if err != nil || len(b) != 500 {
		t.Fatalf("recv %d %v", len(b), err)
	}
	if _, err := st.Recv(ctxT(t)); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
	if in, out := st.Bytes(); in != 500 || out != 400 {
		t.Fatalf("stream bytes in %d out %d", in, out)
	}

	if d.RTT().Samples != 0 {
		t.Fatal("a round trip before any ping")
	}
	for i := range 3 {
		if err := d.Ping(); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, func() bool { return d.RTT().Samples == i+1 })
	}
	r := d.RTT()
	if r.Last <= 0 || r.Avg <= 0 || r.Min <= 0 || r.Min > r.Avg*2 {
		t.Fatalf("rtt %+v", r)
	}
}

// A link whose dialer only listens still measures its latency on both ends:
// a ping goes out when the last round trip is two intervals old.
func TestRTTOnBothEnds(t *testing.T) {
	interval := 50 * time.Millisecond
	opened := make(chan *Conn, 1)
	addr := Listen(t, NewHandler(ConnConfig{PingInterval: interval, OnOpen: func(c *Conn) { opened <- c }}))
	d, err := Dial(ctxT(t), "ws://"+addr+"/ws", DialConfig{Engine: SharedEngine(t), ConnConfig: ConnConfig{PingInterval: interval}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	l := <-opened
	waitUntil(t, func() bool { return d.RTT().Samples >= 2 && l.RTT().Samples >= 2 })
	din, dout := d.Traffic()
	lin, lout := l.Traffic()
	if din == 0 || dout == 0 || lin == 0 || lout == 0 {
		t.Fatalf("pings carry bytes: %d %d %d %d", din, dout, lin, lout)
	}
}

func TestRTTMeter(t *testing.T) {
	var m rttMeter
	if !m.last().IsZero() || m.stats().Samples != 0 {
		t.Fatal("measured before a round trip")
	}
	now := time.Now()
	m.add(10*time.Millisecond, now)
	m.add(20*time.Millisecond, now)
	s := m.stats()
	if s.Last != 20 || s.Avg != 13 || s.Min != 10 || s.Samples != 2 || !m.last().Equal(now) {
		t.Fatalf("%+v", s)
	}
}
