package l4_test

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// udpConfig is a config with a udp listener whose service, with params,
// proxies to the upstream at addrs.
func udpConfig(params string, addrs ...string) string {
	quoted := make([]string, len(addrs))
	for i, a := range addrs {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	return `{
  "listeners": [{"name": "udp", "address": "127.0.0.1:0", "protocols": ["udp"]}],
  "upstreams": [{"name": "app", "addresses": [` + strings.Join(quoted, ", ") + `]}],
  "services": [{"name": "svc", "listeners": ["udp"], "upstream": "app"` + params + `}]
}`
}

// udpBackend answers each datagram with what answer makes of it and of the
// address it came from, and counts the addresses it has heard from.
type udpBackend struct {
	addr  string
	mu    sync.Mutex
	peers map[string]int
}

func startUDPBackend(t testing.TB, answer func(from net.Addr, data []byte) []byte) *udpBackend {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &udpBackend{addr: pc.LocalAddr().String(), peers: map[string]int{}}
	done := make(chan struct{})
	t.Cleanup(func() {
		pc.Close()
		<-done
	})
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			b.mu.Lock()
			b.peers[from.String()]++
			b.mu.Unlock()
			if reply := answer(from, buf[:n]); reply != nil {
				_, _ = pc.WriteTo(reply, from)
			}
		}
	}()
	return b
}

func udpEcho(_ net.Addr, data []byte) []byte { return append([]byte(nil), data...) }

// udpWhoAmI answers with the address the datagram came from: the session's
// own socket in fit.
func udpWhoAmI(from net.Addr, _ []byte) []byte { return []byte(from.String()) }

// sessions are how many addresses the backend has heard from.
func (b *udpBackend) sessions() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.peers)
}

// refusedUDP is a UDP address nothing listens on.
func refusedUDP(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	return addr
}

// dialUDP is a client of fit's udp listener.
func (f *fit) dialUDP() *net.UDPConn {
	f.t.Helper()
	c, err := net.Dial("udp", f.addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	return c.(*net.UDPConn)
}

// exchange sends data and returns the answer, sending again, up to five
// times, when none comes within wait: UDP may drop a datagram.
func exchange(c *net.UDPConn, data []byte, wait time.Duration) ([]byte, error) {
	buf := make([]byte, 65536)
	var err error
	for range 5 {
		if _, err = c.Write(data); err != nil {
			return nil, err
		}
		_ = c.SetReadDeadline(time.Now().Add(wait))
		var n int
		if n, err = c.Read(buf); err == nil {
			return buf[:n], nil
		}
		if !isTimeout(err) {
			return nil, err
		}
	}
	return nil, err
}

// conns is how many connections and sessions fit's service proxies now.
func (f *fit) conns() int64 {
	g := f.rt.Current()
	defer g.Release()
	return g.Listeners[0].Service.Conns()
}

// waitConns waits for the service to proxy want sessions.
func (f *fit) waitConns(want int64, within time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for f.conns() != want {
		if time.Now().After(deadline) {
			f.t.Fatalf("the service has %d sessions, want %d", f.conns(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestUDPEcho proxies datagrams to an echo server: each answered as one
// datagram, whatever its size, and 200 clients at once, each a session of
// its own, which the upstream sees as one address however much it sends.
func TestUDPEcho(t *testing.T) {
	b := startUDPBackend(t, udpEcho)
	f := startFit(t, udpConfig("", b.addr))
	c := f.dialUDP()
	for _, size := range []int{1, 1400, 60000} {
		want := pattern(size, byte(size))
		got, err := exchange(c, want, time.Second)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("a datagram of %d bytes came back as %d, %v", size, len(got), err)
		}
	}
	const clients, rounds = 200, 10
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := range clients {
		c := f.dialUDP()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range rounds {
				want := []byte(fmt.Sprintf("client %d round %d", i, r))
				if got, err := exchange(c, want, time.Second); err != nil || !bytes.Equal(got, want) {
					errs <- fmt.Errorf("client %d round %d: %q, %v", i, r, got, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := b.sessions(); n != clients+1 {
		t.Fatalf("the upstream heard from %d addresses, want %d", n, clients+1)
	}
	if n := f.conns(); n != clients+1 {
		t.Fatalf("the service has %d sessions, want %d", n, clients+1)
	}
}

// TestUDPSessionsExpire ends a session that carries nothing for the idle
// timeout, keeps one that does, and starts a new one, from a new socket,
// when an expired client sends again.
func TestUDPSessionsExpire(t *testing.T) {
	b := startUDPBackend(t, udpWhoAmI)
	f := startFit(t, udpConfig(`, "idle_timeout": "300ms"`, b.addr))
	busy, idle := f.dialUDP(), f.dialUDP()
	first, err := exchange(busy, []byte("x"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	before, err := exchange(idle, []byte("x"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for start := time.Now(); time.Since(start) < 900*time.Millisecond; time.Sleep(100 * time.Millisecond) {
		got, err := exchange(busy, []byte("x"), time.Second)
		if err != nil || !bytes.Equal(got, first) {
			t.Fatalf("the busy session became %q, %v; was %q", got, err, first)
		}
	}
	f.waitConns(1, time.Second)
	f.waitConns(0, 2*time.Second)
	after, err := exchange(idle, []byte("x"), time.Second)
	if err != nil || bytes.Equal(after, before) {
		t.Fatalf("an expired client's session went on from %q to %q, %v", before, after, err)
	}
}

// TestUDPMaxConns drops the datagrams of a session past max_conns, and takes
// it once another has expired.
func TestUDPMaxConns(t *testing.T) {
	b := startUDPBackend(t, udpEcho)
	f := startFit(t, udpConfig(`, "max_conns": 2, "idle_timeout": "300ms"`, b.addr))
	for range 2 {
		if _, err := exchange(f.dialUDP(), []byte("ok"), time.Second); err != nil {
			t.Fatal(err)
		}
	}
	third := f.dialUDP()
	_, _ = third.Write([]byte("ok"))
	_ = third.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := third.Read(make([]byte, 2)); !isTimeout(err) {
		t.Fatalf("a third session got %d bytes, %v", n, err)
	}
	f.waitConns(0, 2*time.Second)
	if _, err := exchange(third, []byte("ok"), time.Second); err != nil {
		t.Fatalf("the third session, once the others expired: %v", err)
	}
}

// TestUDPRefused ends a session whose upstream refuses it, and the client's
// next datagram goes to another address.
func TestUDPRefused(t *testing.T) {
	f := startFit(t, udpConfig("", refusedUDP(t)))
	c := f.dialUDP()
	// The refusal comes back to the session's socket as it sends again.
	for start := time.Now(); f.conns() != 0; time.Sleep(20 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatal("a refused session was not ended")
		}
		_, _ = c.Write([]byte("hello"))
	}

	live := startUDPBackend(t, udpEcho)
	f = startFit(t, udpConfig("", refusedUDP(t), live.addr))
	c = f.dialUDP()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := exchange(c, []byte("hello"), 200*time.Millisecond)
		if err == nil && string(got) == "hello" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer from the live address: %v", err)
		}
	}
}

// TestUDPReload keeps a udp listener across a reload: a session started
// before goes on with the old service until the drain timeout, after which
// its client's next datagram starts a session with the new one, as a new
// client's does at once.
func TestUDPReload(t *testing.T) {
	greet := func(word string) func(net.Addr, []byte) []byte {
		return func(net.Addr, []byte) []byte { return []byte(word) }
	}
	f := startFit(t, udpConfig("", startUDPBackend(t, greet("old")).addr))
	f.s.SetDrainTimeout(500 * time.Millisecond)
	old := f.dialUDP()
	if got, err := exchange(old, []byte("x"), time.Second); err != nil || string(got) != "old" {
		t.Fatalf("read %q, %v", got, err)
	}
	f.reload(udpConfig("", startUDPBackend(t, greet("new")).addr))
	if got, err := exchange(f.dialUDP(), []byte("x"), time.Second); err != nil || string(got) != "new" {
		t.Fatalf("after the reload, read %q, %v", got, err)
	}
	if got, err := exchange(old, []byte("x"), time.Second); err != nil || string(got) != "old" {
		t.Fatalf("the old session: read %q, %v", got, err)
	}
	time.Sleep(time.Second)
	if got, err := exchange(old, []byte("x"), time.Second); err != nil || string(got) != "new" {
		t.Fatalf("the old client, once drained: read %q, %v", got, err)
	}
}
