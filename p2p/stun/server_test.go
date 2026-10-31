package stun_test

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fit/p2p/stun"
)

// startServer runs a STUN server on a UDP and a TCP binding of loopback,
// and returns their addresses.
func startServer(t *testing.T, c stun.ServerConfig) (udp, tcp string) {
	t.Helper()
	s := stun.NewServer(c)
	engine, err := fib.New(fib.DefaultConfig(),
		fib.Binding{Network: "udp", Addr: "127.0.0.1:0", Handler: s},
		fib.Binding{Network: "tcp", Addr: "127.0.0.1:0", Handler: s})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	t.Cleanup(func() {
		engine.Stop()
		<-done
		_ = engine.Close()
	})
	addr := func(i int) string {
		a, err := engine.BindingAddrs(i)
		if err != nil {
			t.Fatal(err)
		}
		return a[0].String()
	}
	return addr(0), addr(1)
}

func bindingRequest(fingerprint bool, extra ...stun.AttrType) *stun.Message {
	var m stun.Message
	m.Build(stun.Type{Method: stun.MethodBinding, Class: stun.ClassRequest}, stun.NewTransactionID())
	for _, t := range extra {
		m.Add(t, []byte{1, 2, 3, 4})
	}
	if fingerprint {
		m.AddFingerprint()
	}
	return &m
}

// exchange sends req over conn and reads one message back.
func exchange(t *testing.T, conn net.Conn, req []byte, stream bool) *stun.Message {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	return read(t, conn, stream)
}

func read(t *testing.T, conn net.Conn, stream bool) *stun.Message {
	t.Helper()
	buf := make([]byte, 2048)
	var n int
	var err error
	if stream {
		if _, err = io.ReadFull(conn, buf[:20]); err == nil {
			l := stun.MessageLen(buf[:20])
			_, err = io.ReadFull(conn, buf[20:l])
			n = l
		}
	} else {
		n, err = conn.Read(buf)
	}
	if err != nil {
		t.Fatal(err)
	}
	var m stun.Message
	if err := m.Parse(buf[:n]); err != nil {
		t.Fatal(err)
	}
	return &m
}

func checkMapped(t *testing.T, resp *stun.Message, req *stun.Message, conn net.Conn) {
	t.Helper()
	if resp.ID != req.ID || resp.Type != (stun.Type{Method: stun.MethodBinding, Class: stun.ClassSuccess}) {
		t.Fatalf("response %+v %x to %x", resp.Type, resp.ID, req.ID)
	}
	got, err := resp.XORAddr(stun.AttrXORMappedAddress)
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParseAddrPort(conn.LocalAddr().String()); got != want {
		t.Fatalf("mapped %v, want %v", got, want)
	}
}

func TestServerUDP(t *testing.T) {
	udp, _ := startServer(t, stun.ServerConfig{Software: "fit"})
	conn, err := net.Dial("udp", udp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	req := bindingRequest(false)
	resp := exchange(t, conn, req.Raw, false)
	checkMapped(t, resp, req, conn)
	if v, _ := resp.Get(stun.AttrSoftware); string(v) != "fit" {
		t.Fatalf("SOFTWARE %q", v)
	}
	if resp.Has(stun.AttrFingerprint) {
		t.Fatal("a FINGERPRINT the request did not ask for")
	}

	// A request with FINGERPRINT gets one back.
	req = bindingRequest(true)
	resp = exchange(t, conn, req.Raw, false)
	checkMapped(t, resp, req, conn)
	if err := resp.CheckFingerprint(); err != nil {
		t.Fatal(err)
	}

	// An unknown comprehension-required attribute is a 420 that names it;
	// an unknown optional one is ignored.
	req = bindingRequest(true, 0x0042, 0x8042)
	resp = exchange(t, conn, req.Raw, false)
	if code, _, _ := resp.ErrorCode(); code != stun.CodeUnknownAttribute || resp.Type.Class != stun.ClassError {
		t.Fatalf("class %d code %d", resp.Type.Class, code)
	}
	if u, _ := resp.UnknownAttributes(nil); len(u) != 1 || u[0] != 0x0042 {
		t.Fatalf("UNKNOWN-ATTRIBUTES %v", u)
	}

	// A method the server does not serve is a 400.
	var alloc stun.Message
	alloc.Build(stun.Type{Method: stun.MethodAllocate, Class: stun.ClassRequest}, stun.NewTransactionID())
	resp = exchange(t, conn, alloc.Raw, false)
	if code, _, _ := resp.ErrorCode(); code != stun.CodeBadRequest || resp.Type.Method != stun.MethodAllocate {
		t.Fatalf("method %#x code %d", resp.Type.Method, code)
	}

	// What is not a request, or not STUN, or fails its fingerprint, gets
	// nothing; the next request is still answered.
	var ind stun.Message
	ind.Build(stun.Type{Method: stun.MethodBinding, Class: stun.ClassIndication}, stun.NewTransactionID())
	bad := bindingRequest(true)
	bad.Raw[len(bad.Raw)-1] ^= 1
	for _, b := range [][]byte{ind.Raw, []byte("not stun at all, not at all"), bad.Raw} {
		if _, err := conn.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	req = bindingRequest(false)
	resp = exchange(t, conn, req.Raw, false)
	checkMapped(t, resp, req, conn)
}

func TestServerTCP(t *testing.T) {
	_, tcp := startServer(t, stun.ServerConfig{})
	conn, err := net.Dial("tcp", tcp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Two requests in one write, then one a byte at a time.
	a, b := bindingRequest(true), bindingRequest(false)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(append([]byte(nil), a.Raw...), b.Raw...)); err != nil {
		t.Fatal(err)
	}
	checkMapped(t, read(t, conn, true), a, conn)
	checkMapped(t, read(t, conn, true), b, conn)
	c := bindingRequest(true, 0x8042)
	for i := range c.Raw {
		if _, err := conn.Write(c.Raw[i : i+1]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	checkMapped(t, read(t, conn, true), c, conn)
}

// closed reports whether the server closes conn after what was written.
func closed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var buf [1]byte
	if _, err := conn.Read(buf[:]); !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		var op *net.OpError
		if !errors.As(err, &op) || op.Timeout() {
			t.Fatalf("connection not closed: %v", err)
		}
	}
}

func TestServerTCPRefuses(t *testing.T) {
	_, tcp := startServer(t, stun.ServerConfig{MaxTCPMessage: 64, TCPIdleTimeout: 200 * time.Millisecond})
	for name, write := range map[string][]byte{
		// Not STUN: no next message can be found in the stream.
		"foreign": []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"),
		// A header that announces more than the server takes.
		"too large": bindingRequest(false, stun.AttrSoftware, stun.AttrSoftware, stun.AttrSoftware,
			stun.AttrSoftware, stun.AttrSoftware, stun.AttrSoftware, stun.AttrSoftware).Raw[:20],
		// Half a message, then nothing until the idle timeout.
		"idle": bindingRequest(false).Raw[:10],
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.Dial("tcp", tcp)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write(write); err != nil {
				t.Fatal(err)
			}
			closed(t, conn)
		})
	}
}
