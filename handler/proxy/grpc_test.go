package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/grpc"
	"github.com/lesismal/fib/grpc/codes"
	"github.com/lesismal/fib/grpc/metadata"
	grpcstatus "github.com/lesismal/fib/grpc/status"
	fibhttp "github.com/lesismal/fib/http"
	fibtls "github.com/lesismal/fib/tls"

	"github.com/lesismal/fit/internal/certtest"
)

// msg is the test service's message, sent with gRPC's json codec.
type msg struct {
	Msg string
	N   int
}

// grpcService is the test service's implementation: Unary echoes, Count
// streams N messages back, Sum adds what it is sent, and Chat answers each
// message as it comes.
type grpcService struct {
	// deadline gets whether a "deadline" call had one, and how far off.
	deadline chan time.Duration
	// hang is what "hang" calls and "cut" streams wait on, ignoring their
	// context; entered says one has.
	hang    chan struct{}
	entered chan struct{}
	// sent counts the messages Count has sent, and gate is what a Sum whose
	// first message is "wait" waits on before it reads the rest.
	sent atomic.Int64
	gate chan struct{}
}

var grpcDesc = grpc.ServiceDesc{
	ServiceName: "test.Echo",
	Methods: []grpc.MethodDesc{{MethodName: "Unary", Handler: func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := new(msg)
		if err := dec(in); err != nil {
			return nil, err
		}
		return srv.(*grpcService).unary(ctx, in)
	}}},
	Streams: []grpc.StreamDesc{
		{StreamName: "Count", ServerStreams: true, Handler: func(srv any, s grpc.ServerStream) error {
			return srv.(*grpcService).count(s)
		}},
		{StreamName: "Sum", ClientStreams: true, Handler: func(srv any, s grpc.ServerStream) error {
			total, n := 0, 0
			for {
				in := new(msg)
				if err := s.RecvMsg(in); err == io.EOF {
					return s.SendMsg(&msg{Msg: "sum", N: total*1000 + n})
				} else if err != nil {
					return err
				}
				if n == 0 && in.Msg == "wait" {
					<-srv.(*grpcService).gate
				}
				total += in.N
				n++
			}
		}},
		{StreamName: "Chat", ServerStreams: true, ClientStreams: true, Handler: func(_ any, s grpc.ServerStream) error {
			for {
				in := new(msg)
				if err := s.RecvMsg(in); err == io.EOF {
					return nil
				} else if err != nil {
					return err
				}
				if err := s.SendMsg(&msg{Msg: strings.ToUpper(in.Msg), N: in.N}); err != nil {
					return err
				}
			}
		}},
	},
}

func (g *grpcService) unary(ctx context.Context, in *msg) (*msg, error) {
	switch in.Msg {
	case "fail":
		return nil, grpcstatus.Error(codes.NotFound, "no such thing")
	case "trailer":
		grpc.SetTrailer(ctx, metadata.Pairs("t", "trailer"))
	case "deadline":
		at, ok := ctx.Deadline()
		if !ok {
			g.deadline <- -1
		} else {
			g.deadline <- time.Until(at)
		}
	case "hang":
		g.entered <- struct{}{}
		<-g.hang
	}
	return &msg{Msg: in.Msg, N: in.N}, nil
}

func (g *grpcService) count(s grpc.ServerStream) error {
	in := new(msg)
	if err := s.RecvMsg(in); err != nil {
		return err
	}
	for i := range in.N {
		if in.Msg == "cut" && i == 2 {
			g.entered <- struct{}{}
			<-g.hang
		}
		if err := s.SendMsg(&msg{Msg: in.Msg, N: i}); err != nil {
			return err
		}
		g.sent.Add(1)
	}
	return nil
}

// runEngine starts a fib engine with bindings, and stops it when the test
// ends or stop is called.
func runEngine(t *testing.T, bindings ...fib.Binding) (engine *fib.Engine, stop func()) {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig(), bindings...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			engine.Stop()
			<-done
			_ = engine.Close()
		})
	}
	t.Cleanup(stop)
	return engine, stop
}

// grpcUpstream serves the test service over proto, h2c or h2, and returns
// its address, registered with backendFields, its service, and a function
// that stops it, ending its connections.
func grpcUpstream(t *testing.T, proto string) (string, *grpcService, func()) {
	t.Helper()
	svc := &grpcService{deadline: make(chan time.Duration, 1), hang: make(chan struct{}), entered: make(chan struct{}, 1),
		gate: make(chan struct{})}
	server := grpc.NewServer()
	server.RegisterService(&grpcDesc, svc)
	var handler fib.Handler = server
	fields := `, "protocol": "h2c"`
	if proto == "h2" {
		cert, err := certtest.Write(t.TempDir(), "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.LoadX509KeyPair(cert.CertFile, cert.KeyFile)
		if err != nil {
			t.Fatal(err)
		}
		handler = fibtls.NewServer(grpc.ConfigureTLS(&tls.Config{Certificates: []tls.Certificate{pair}}), server)
		fields = `, "protocol": "h2", "tls_ca_file": ` + quoteJSON(cert.CertFile)
	}
	engine, stop := runEngine(t, fib.Binding{Addr: "127.0.0.1:0", Handler: handler})
	addrs, err := engine.BindingAddrs(0)
	if err != nil {
		t.Fatal(err)
	}
	addr := addrs[0].String()
	backendFields.Store(addr, fields)
	// Forgotten before the engine stops, which frees the port, as in
	// serveBackend; stop, which a test may call early, forgets it first too.
	forget := func() { backendFields.Delete(addr) }
	t.Cleanup(forget)
	t.Cleanup(func() {
		for _, ch := range []chan struct{}{svc.hang, svc.gate} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	})
	return addr, svc, func() { forget(); stop() }
}

// grpcClient is a gRPC client of fit's listener at base.
func grpcClient(t *testing.T, base string) *grpc.ClientConn {
	t.Helper()
	engine, _ := runEngine(t)
	cc, err := grpc.NewClient(strings.TrimPrefix(base, "http://"), grpc.WithEngine(engine),
		grpc.WithDefaultCallOptions(grpc.CallContentSubtype("json")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

func grpcContext(t *testing.T) context.Context {
	return grpcContextFor(t, 10*time.Second)
}

// grpcContextFor is grpcContext with a deadline of d, for a test that moves
// enough to need more than 10s on a slow machine under the race detector.
func grpcContextFor(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func eachGRPCProto(t *testing.T, fn func(t *testing.T, proto string)) {
	for _, proto := range []string{"h2c", "h2"} {
		t.Run(proto, func(t *testing.T) { fn(t, proto) })
	}
}

// TestGRPCUnary sends unary calls through fit: an echo, the upstream's
// status and trailers passed on, and the call's deadline carried upstream.
func TestGRPCUnary(t *testing.T) {
	eachGRPCProto(t, func(t *testing.T, proto string) {
		addr, svc, _ := grpcUpstream(t, proto)
		cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), addr))
		ctx := grpcContext(t)

		out := new(msg)
		if err := cc.Invoke(ctx, "/test.Echo/Unary", &msg{Msg: "hello", N: 7}, out); err != nil || *out != (msg{"hello", 7}) {
			t.Fatalf("echo: %+v, %v", out, err)
		}
		big := strings.Repeat("x", 200<<10)
		if err := cc.Invoke(ctx, "/test.Echo/Unary", &msg{Msg: big}, out); err != nil || out.Msg != big {
			t.Fatalf("a 200KB echo: %d bytes, %v", len(out.Msg), err)
		}
		err := cc.Invoke(ctx, "/test.Echo/Unary", &msg{Msg: "fail"}, out)
		if st := grpcstatus.Convert(err); st.Code() != codes.NotFound || st.Message() != "no such thing" {
			t.Fatalf("the upstream's status: %v", err)
		}
		var trailer metadata.MD
		if err := cc.Invoke(ctx, "/test.Echo/Unary", &msg{Msg: "trailer"}, out, grpc.Trailer(&trailer)); err != nil ||
			strings.Join(trailer.Get("t"), ",") != "trailer" {
			t.Fatalf("the upstream's trailer: %v, %v", trailer, err)
		}
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := cc.Invoke(dctx, "/test.Echo/Unary", &msg{Msg: "deadline"}, out); err != nil {
			t.Fatal(err)
		}
		if d := <-svc.deadline; d < 4*time.Second || d > 5*time.Second {
			t.Fatalf("the upstream saw a deadline %v away, want about 5s", d)
		}
	})
}

// TestGRPCServerStreaming streams messages back from the upstream, more of
// them than fit holds whole.
func TestGRPCServerStreaming(t *testing.T) {
	eachGRPCProto(t, func(t *testing.T, proto string) {
		addr, _, _ := grpcUpstream(t, proto)
		cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), addr))
		s, err := cc.NewStream(grpcContext(t), &grpcDesc.Streams[0], "/test.Echo/Count")
		if err != nil {
			t.Fatal(err)
		}
		const n = 2000
		pad := strings.Repeat("p", 100)
		if err := s.SendMsg(&msg{Msg: pad, N: n}); err != nil {
			t.Fatal(err)
		}
		_ = s.CloseSend()
		for i := range n {
			in := new(msg)
			if err := s.RecvMsg(in); err != nil || in.N != i || in.Msg != pad {
				t.Fatalf("message %d: %+v, %v", i, in, err)
			}
		}
		if err := s.RecvMsg(new(msg)); err != io.EOF {
			t.Fatalf("after the last message: %v, want EOF", err)
		}
	})
}

// TestGRPCClientStreaming streams messages to the upstream, more of them
// than fit holds whole, before the one answer.
func TestGRPCClientStreaming(t *testing.T) {
	eachGRPCProto(t, func(t *testing.T, proto string) {
		addr, _, _ := grpcUpstream(t, proto)
		cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), addr))
		s, err := cc.NewStream(grpcContext(t), &grpcDesc.Streams[1], "/test.Echo/Sum")
		if err != nil {
			t.Fatal(err)
		}
		const n = 500
		pad := strings.Repeat("p", 1000)
		want := 0
		for i := range n {
			want += i
			if err := s.SendMsg(&msg{Msg: pad, N: i}); err != nil {
				t.Fatal(err)
			}
		}
		_ = s.CloseSend()
		out := new(msg)
		if err := s.RecvMsg(out); err != nil || out.N != want*1000+n {
			t.Fatalf("sum: %+v, %v; want %d", out, err, want*1000+n)
		}
	})
}

// TestGRPCBidiStreaming answers each message before the next is sent, which
// only works if neither side's messages wait at fit for more to come.
func TestGRPCBidiStreaming(t *testing.T) {
	eachGRPCProto(t, func(t *testing.T, proto string) {
		addr, _, _ := grpcUpstream(t, proto)
		cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), addr))
		s, err := cc.NewStream(grpcContext(t), &grpcDesc.Streams[2], "/test.Echo/Chat")
		if err != nil {
			t.Fatal(err)
		}
		for i := range 50 {
			text := "ping"
			if i%10 == 9 {
				text = strings.Repeat("big", 100<<10)
			}
			if err := s.SendMsg(&msg{Msg: text, N: i}); err != nil {
				t.Fatal(err)
			}
			in := new(msg)
			if err := s.RecvMsg(in); err != nil || in.N != i || in.Msg != strings.ToUpper(text) {
				t.Fatalf("answer %d: %d bytes, %d, %v", i, len(in.Msg), in.N, err)
			}
		}
		_ = s.CloseSend()
		if err := s.RecvMsg(new(msg)); err != io.EOF {
			t.Fatalf("after the last message: %v, want EOF", err)
		}
	})
}

// TestGRPCBackpressure has each side of a stream outpace the other: a
// client that reads nothing holds back the upstream's stream to it, and an
// upstream that reads nothing holds back the client's. fit holds neither
// stream whole, and every message arrives once the slow side reads.
//
// It moves 32 MB each way, which takes about 5s on one CPU under the race
// detector and has taken more than 10s on a CI runner; its deadline is 60s,
// and a failure says how far each stream got and when, which tells a
// stream that stopped from one that was slow.
func TestGRPCBackpressure(t *testing.T) {
	eachGRPCProto(t, func(t *testing.T, proto string) {
		addr, svc, _ := grpcUpstream(t, proto)
		cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), addr))
		ctx := grpcContextFor(t, 60*time.Second)
		start := time.Now()
		const n = 1000
		pad := strings.Repeat("p", 32<<10)

		down, err := cc.NewStream(ctx, &grpcDesc.Streams[0], "/test.Echo/Count")
		if err != nil {
			t.Fatal(err)
		}
		if err := down.SendMsg(&msg{Msg: pad, N: n}); err != nil {
			t.Fatal(err)
		}
		_ = down.CloseSend()
		time.Sleep(time.Second)
		if sent := svc.sent.Load(); sent >= n/2 {
			t.Errorf("the upstream sent %d of %d messages to a client that read none", sent, n)
		}
		for i := range n {
			in := new(msg)
			if err := down.RecvMsg(in); err != nil || in.N != i {
				t.Fatalf("message %d after %v (the upstream had sent %d): %d, %v", i, time.Since(start), svc.sent.Load(), in.N, err)
			}
		}
		downDone := time.Since(start)

		up, err := cc.NewStream(ctx, &grpcDesc.Streams[1], "/test.Echo/Sum")
		if err != nil {
			t.Fatal(err)
		}
		var sent atomic.Int64
		done := make(chan error, 1)
		go func() {
			if err := up.SendMsg(&msg{Msg: "wait"}); err != nil {
				done <- err
				return
			}
			for range n {
				if err := up.SendMsg(&msg{Msg: pad, N: 1}); err != nil {
					done <- err
					return
				}
				sent.Add(1)
			}
			done <- up.CloseSend()
		}()
		time.Sleep(time.Second)
		if k := sent.Load(); k >= n/2 {
			t.Errorf("the client sent %d of %d messages to an upstream that read none", k, n)
		}
		close(svc.gate)
		if err := <-done; err != nil {
			t.Fatalf("the client's stream after %v (the download took %v), %d of %d messages sent: %v", time.Since(start), downDone, sent.Load(), n, err)
		}
		out := new(msg)
		if err := up.RecvMsg(out); err != nil || out.N != n*1000+n+1 {
			t.Fatalf("sum: %+v, %v", out.N, err)
		}
	})
}

// TestGRPCNoUpstream answers a call whose upstream refuses connections with
// UNAVAILABLE.
func TestGRPCNoUpstream(t *testing.T) {
	cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), refused(t)))
	err := cc.Invoke(grpcContext(t), "/test.Echo/Unary", &msg{Msg: "hello"}, new(msg))
	if st := grpcstatus.Convert(err); st.Code() != codes.Unavailable || st.Message() != "fit: the upstream is unavailable" {
		t.Fatalf("got %v, want fit's UNAVAILABLE", err)
	}
}

// TestGRPCUpstreamGone ends a server stream whose upstream went away midway
// with UNAVAILABLE, after the messages that came.
func TestGRPCUpstreamGone(t *testing.T) {
	eachGRPCProto(t, func(t *testing.T, proto string) {
		addr, svc, stop := grpcUpstream(t, proto)
		cc := grpcClient(t, startFit(t, config1(`{"upstream": "app"}`), addr))
		s, err := cc.NewStream(grpcContext(t), &grpcDesc.Streams[0], "/test.Echo/Count")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SendMsg(&msg{Msg: "cut", N: 5}); err != nil {
			t.Fatal(err)
		}
		_ = s.CloseSend()
		for i := range 2 {
			if err := s.RecvMsg(new(msg)); err != nil {
				t.Fatalf("message %d: %v", i, err)
			}
		}
		<-svc.entered
		// The call waits in its handler, which the engine's stop need not
		// wait for: the test's end lets it go.
		go stop()
		err = s.RecvMsg(new(msg))
		if st := grpcstatus.Convert(err); st.Code() != codes.Unavailable || st.Message() != "fit: the upstream is unavailable" {
			t.Fatalf("got %v, want fit's UNAVAILABLE", err)
		}
	})
}

// grpcFrame is a gRPC message of body.
func grpcFrame(body []byte) []byte {
	return append(binary.BigEndian.AppendUint32([]byte{0}, uint32(len(body))), body...)
}

// rawCall sends a unary call through fit with fields, by HTTP/2 rather
// than a gRPC client, so that the client keeps no deadline of its own, and
// returns the response's fields, trailers included.
func rawCall(t *testing.T, base string, m msg, fields ...string) stdhttp.Header {
	t.Helper()
	engine, _ := runEngine(t)
	config := fibhttp.DefaultClientConfig()
	config.UnencryptedHTTP2 = true
	config.Timeout = 10 * time.Second
	client := fibhttp.NewClient(engine, config)
	t.Cleanup(client.Close)
	body, _ := json.Marshal(m)
	req, err := stdhttp.NewRequest(stdhttp.MethodPost, base+"/test.Echo/Unary", bytes.NewReader(grpcFrame(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc+json")
	req.Header.Set("TE", "trailers")
	for i := 0; i < len(fields); i += 2 {
		req.Header.Set(fields[i], fields[i+1])
	}
	resp, err := client.Go(req).Wait()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	h := resp.Header.Clone()
	for k, v := range resp.Trailer {
		h[k] = v
	}
	return h
}

// TestGRPCTimeouts cuts calls short at fit: by their grpc-timeout, and by
// the route's timeout, which the upstream is told; either is answered
// DEADLINE_EXCEEDED, and neither counts against the backend.
func TestGRPCTimeouts(t *testing.T) {
	t.Run("grpc-timeout", func(t *testing.T) {
		addr, svc, _ := grpcUpstream(t, "h2c")
		base := startFit(t, config1(`{"upstream": "app"}`), addr)
		start := time.Now()
		h := rawCall(t, base, msg{Msg: "hang"}, "Grpc-Timeout", "300m")
		<-svc.entered
		if took := time.Since(start); h.Get("Grpc-Status") != "4" || h.Get("Grpc-Message") != "fit: the call took too long" ||
			took > 3*time.Second {
			t.Fatalf("after %v: %v, want fit's DEADLINE_EXCEEDED", took, h)
		}
	})
	t.Run("route", func(t *testing.T) {
		addr, svc, _ := grpcUpstream(t, "h2c")
		base := startFit(t, config1(`{"upstream": "app", "timeout": "2s"}`), addr)
		if h := rawCall(t, base, msg{Msg: "deadline"}); h.Get("Grpc-Status") != "0" {
			t.Fatalf("a call within the timeout: %v", h)
		}
		if d := <-svc.deadline; d < time.Second || d > 2*time.Second {
			t.Fatalf("the upstream saw a deadline %v away, want about 2s", d)
		}
		h := rawCall(t, base, msg{Msg: "deadline"}, "Grpc-Timeout", "5S")
		if d := <-svc.deadline; h.Get("Grpc-Status") != "0" || d < time.Second || d > 2*time.Second {
			t.Fatalf("a call with a longer grpc-timeout: %v; the upstream saw a deadline %v away, want about 2s", h, d)
		}
		h = rawCall(t, base, msg{Msg: "hang"})
		<-svc.entered
		if h.Get("Grpc-Status") != "4" || h.Get("Grpc-Message") != "fit: the call took too long" {
			t.Fatalf("%v, want fit's DEADLINE_EXCEEDED", h)
		}
		// The backend is still in rotation.
		if h := rawCall(t, base, msg{Msg: "ok"}); h.Get("Grpc-Status") != "0" {
			t.Fatalf("after the timeout: %v", h)
		}
	})
}

func TestGRPCHelpers(t *testing.T) {
	for _, ct := range []string{"application/grpc", "application/grpc+proto", "Application/GRPC+json", "application/grpc;charset=x"} {
		if !grpcContentType(ct) {
			t.Errorf("%q is gRPC's", ct)
		}
	}
	for _, ct := range []string{"application/grpc-web", "application/grpcx", "application/json", ""} {
		if grpcContentType(ct) {
			t.Errorf("%q is not gRPC's", ct)
		}
	}
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{{"300m", 300 * time.Millisecond, true}, {"5S", 5 * time.Second, true}, {"1H", time.Hour, true}, {"99999999n", 99999999, true},
		{"m", 0, false}, {"123456789S", 0, false}, {"10x", 0, false}, {"-1S", 0, false}} {
		if d, ok := grpcTimeout([]string{tc.in}); d != tc.want || ok != tc.ok {
			t.Errorf("grpcTimeout(%q) = %v, %v", tc.in, d, ok)
		}
	}
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{{2 * time.Second, "2000000u"}, {time.Nanosecond, "1n"}, {0, "1n"}, {200 * time.Hour, "720000S"}} {
		if got := encodeGRPCTimeout(tc.d); got != tc.want {
			t.Errorf("encodeGRPCTimeout(%v) = %q, want %q", tc.d, got, tc.want)
		}
		if d, ok := grpcTimeout([]string{encodeGRPCTimeout(tc.d)}); !ok || d < tc.d {
			t.Errorf("%v does not round trip: %v", tc.d, d)
		}
	}
}
