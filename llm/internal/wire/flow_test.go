package wire

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// A peer that reads nothing holds back the producer that feeds its link long before the slow timeout: once the
// link's send queue passes its limit, the link's streams stop sending, whatever credit the peer granted, and the
// link stays open. A dialer's hold on its reads ends after maxHold, its streams still held, so the peer's frames get
// through (a listener's reads stay paused by fib's write watermark until its queue drains). Once the peer reads
// again, the queue drains, the streams go on, and the stream ends whole. Either side may produce.
// 什么都不读的对端早在慢连接超时之前就阻住了向其连接写入的生产者：连接的发送队列一超过上限，无论对端授予了多少额度，连接的
// 流都停止发送，连接保持打开。拨号方的读取暂停在 maxHold 后结束，其流仍被停止，因此对端的帧能够送达（监听方的读取由 fib 的
// 写水位暂停，直到其队列排空）。对端恢复读取后队列排空，流继续，完整结束。两端都可以是生产者。
func TestSlowPeerHoldsProducers(t *testing.T) {
	for _, side := range []string{"listener", "dialer"} {
		t.Run(side, func(t *testing.T) { testSlowPeer(t, side == "dialer") })
	}
}

func testSlowPeer(t *testing.T, dialerProduces bool) {
	const (
		window = 64 << 20
		total  = 24 << 20
		frame  = 32 << 10
		slow   = 10 * time.Second
	)
	var sent atomic.Int64
	p := make([]byte, frame)
	_, _ = rand.Read(p) // incompressible / 不可压缩
	produce := func(ctx context.Context, st *Stream) error {
		for sent.Load() < total {
			if err := st.Send(ctx, p); err != nil {
				return err
			}
			sent.Add(frame)
		}
		return nil
	}
	consume := func(st *Stream) (int, error) {
		n := 0
		for {
			b, err := st.Recv(context.Background())
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			if err != nil {
				return n, err
			}
			n += len(b)
		}
	}
	got := make(chan int, 1)
	pushed := make(chan struct{}, 1)
	opened := make(chan *Conn, 1)
	closed := make(chan error, 2)
	opts := Options{Window: window}
	handlers := func(*Conn) Handlers {
		return Handlers{Push: func(*Request) { pushed <- struct{}{} }, Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
			if !dialerProduces {
				return nil, produce(ctx, st)
			}
			n, err := consume(st)
			got <- n
			return nil, err
		}}
	}
	cfg := ConnConfig{Session: opts, PingInterval: 3 * time.Second, SlowTimeout: slow, Handlers: handlers,
		OnClose: func(_ *Conn, err error) { closed <- err }}
	lcfg := cfg
	lcfg.OnOpen = func(c *Conn) { opened <- c }
	addr := Listen(t, NewHandler(lcfg))
	d, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{ConnConfig: cfg, Engine: SharedEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	l := <-opened
	producer, peer := l, d
	if dialerProduces {
		producer, peer = d, l
	}

	peer.ws.HoldReads(true) // the slow peer / 慢对端
	start := time.Now()
	st, err := d.OpenStream(context.Background(), Head{Method: "/bulk"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dialerProduces {
		go func() {
			if err := produce(context.Background(), st); err == nil {
				_ = st.CloseSend()
			}
		}()
	}
	// The producer stops: what it sent no longer grows.
	// 生产者停下：已发送的量不再增长。
	last, still := int64(-1), 0
	for still < 5 {
		time.Sleep(100 * time.Millisecond)
		if n := sent.Load(); n == last {
			still++
		} else {
			last, still = n, 0
		}
		if time.Since(start) > slow/2 {
			t.Fatalf("the producer was not held: %d bytes sent", sent.Load())
		}
	}
	if last >= total || last > window/4 {
		t.Fatalf("held after %d bytes, with a window of %d", last, window)
	}
	t.Logf("held after %d bytes, in %v", last, time.Since(start))
	producer.flowMu.Lock()
	above := producer.above
	producer.flowMu.Unlock()
	if !above || !producer.held.Load() {
		t.Fatalf("the link is not above its limit (%v) or its streams not held (%v)", above, producer.held.Load())
	}
	if dialerProduces {
		// The dialer reads again after maxHold, its streams still held: the peer's push gets through. Nothing goes the
		// dialer's way before its reads are let go: a frame from the peer carries a window update, after which the
		// kernel may take what was queued and the episode ends, as it should, with the queue at half its limit. Such
		// an episode, if one ends anyway, is followed by another, whose maxHold is waited for in turn.
		// 拨号方在 maxHold 之后恢复读取，其流仍被停止：对端的推送能够送达。在拨号方的读取被放开之前不向它发送任何东西：对端的
		// 帧带有窗口更新，此后内核可能接走排队的数据，时段随之结束（队列降到上限的一半），这是应有的行为。若某个时段仍这样结束，
		// 随后会有下一个时段，再等它的 maxHold。
		ws := producer.ws.NetConn()
		readsLetGo := false
		for deadline := time.Now().Add(slow / 2); !readsLetGo; time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("the dialer's reads stayed held past maxHold")
			}
			producer.flowMu.Lock()
			readsLetGo = producer.above && !ws.ReadsHeld()
			producer.flowMu.Unlock()
		}
		if !producer.held.Load() {
			t.Fatal("the streams went on with the queue above its limit")
		}
		if err := peer.Push(Head{Method: "/p"}, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case <-pushed:
		case <-time.After(slow / 2):
			t.Fatal("the link did not read again")
		}
	}
	select {
	case err := <-closed:
		t.Fatalf("the link closed: %v", err)
	default:
	}

	peer.ws.HoldReads(false)
	var n int
	if dialerProduces {
		n = <-got
	} else if n, err = consume(st); err != nil {
		t.Fatal(err)
	}
	if n != total {
		t.Fatalf("received %d of %d bytes", n, total)
	}
	waitUntil(t, func() bool { return !producer.held.Load() })
}

// Held streams: TrySend refuses, Credit says none, OnCredit and Send wait, as with no credit, until the streams go
// again and wake.
// 停止的流：TrySend 拒绝，Credit 报告没有额度，OnCredit 与 Send 等待，如同没有额度，直到流被放行并唤醒。
func TestHoldStreams(t *testing.T) {
	recv := make(chan []byte, 4)
	d, l, closeFn := Pipe(Options{}, Handlers{}, Handlers{Stream: func(ctx context.Context, st *Stream) ([]byte, error) {
		for {
			p, err := st.Recv(ctx)
			if err != nil {
				return nil, nil
			}
			recv <- p
		}
	}})
	defer closeFn()
	_ = l
	st, err := d.OpenStream(context.Background(), Head{Method: "/s"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.HoldStreams(true)
	if ok, err := st.TrySend([]byte("a")); ok || err != nil {
		t.Fatalf("TrySend on a held stream: %v %v", ok, err)
	}
	if st.Credit() {
		t.Fatal("Credit on a held stream")
	}
	resumed := make(chan struct{})
	if !st.OnCredit(func() { close(resumed) }) {
		t.Fatal("OnCredit refused on a held stream")
	}
	sent := make(chan error, 1)
	go func() { sent <- st.Send(context.Background(), []byte("b")) }()
	select {
	case <-sent:
		t.Fatal("Send went on a held stream")
	case <-time.After(50 * time.Millisecond):
	}
	d.wakeStreams() // still held: nothing runs / 仍被停止：什么都不执行
	select {
	case <-resumed:
		t.Fatal("OnCredit ran while held")
	case <-time.After(20 * time.Millisecond):
	}
	d.HoldStreams(false)
	<-resumed
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if p := <-recv; string(p) != "b" {
		t.Fatalf("got %q", p)
	}
	if ok, err := st.TrySend([]byte("c")); !ok || err != nil {
		t.Fatalf("TrySend once let go: %v %v", ok, err)
	}
}

// An episode found drained already ends at once; a late notice or hold timer of an earlier episode does nothing.
// 发现已排空的时段立即结束；早先时段迟到的通知或暂停定时器什么也不做。
func TestEpisodes(t *testing.T) {
	opened := make(chan *Conn, 1)
	addr := Listen(t, NewHandler(ConnConfig{OnOpen: func(c *Conn) { opened <- c }}))
	d, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{Engine: SharedEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	<-opened
	d.overLimit() // nothing queued: OnWritable refuses / 没有排队：OnWritable 拒绝
	d.flowMu.Lock()
	above, gen := d.above, d.gen
	d.flowMu.Unlock()
	if above || d.held.Load() || gen != 1 {
		t.Fatalf("after a drained episode: above %v, held %v, gen %d", above, d.held.Load(), gen)
	}
	d.flowMu.Lock()
	d.above, d.gen = true, 2
	d.flowMu.Unlock()
	d.overLimit() // one under way: nothing more / 已在时段中：不再处理
	if d.gen != 2 {
		t.Fatalf("a second episode began: %d", d.gen)
	}
	d.writable(1)
	d.releaseReads(1)
	d.flowMu.Lock()
	above = d.above
	d.flowMu.Unlock()
	if !above {
		t.Fatal("a stale notice ended the episode")
	}
	d.releaseReads(2)
	d.writable(2)
	d.flowMu.Lock()
	above = d.above
	d.flowMu.Unlock()
	if above {
		t.Fatal("the episode did not end")
	}
}

func TestMaxHold(t *testing.T) {
	for _, tc := range []struct {
		ping, slow, want time.Duration
	}{
		{0, 0, DefaultPingInterval / 4},
		{4 * time.Second, 0, time.Second},
		{8 * time.Second, 2 * time.Second, 500 * time.Millisecond},
		{-1, 2 * time.Second, 500 * time.Millisecond},
		{-1, -1, DefaultPingInterval / 4},
	} {
		cfg := ConnConfig{PingInterval: tc.ping, SlowTimeout: tc.slow}
		if got := cfg.maxHold(); got != tc.want {
			t.Errorf("ping %v slow %v: %v, want %v", tc.ping, tc.slow, got, tc.want)
		}
	}
}
