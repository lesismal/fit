package tracker

import (
	"bufio"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	fibtracker "github.com/lesismal/fib/bittorrent/tracker"
)

// TestSlowAndCutClients serves a client that sends its announce a byte at a
// time, clients that leave midway over HTTP, and datagrams that are not
// tracker requests over UDP, and still serves the others.
func TestSlowAndCutClients(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, load(t, `{"listen": "127.0.0.1:0"}`, ""))
	base := serveHTTP(t, e)
	addr := strings.TrimPrefix(base, "http://")
	h, id := hashOf("slow"), peerID(1)
	q := "info_hash=" + url.QueryEscape(string(h[:])) + "&peer_id=" + url.QueryEscape(string(id[:])) +
		"&port=6881&uploaded=0&downloaded=0&left=0&compact=1"
	req := "GET /announce?" + q + " HTTP/1.1\r\nHost: t\r\nConnection: close\r\n\r\n"

	// Cut midway: in the request line, and after the request line.
	for _, part := range []string{req[:20], req[:len(req)-10]} {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}

	// A byte at a time.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := range len(req) {
		if _, err := c.Write([]byte{req[i]}); err != nil {
			t.Fatal(err)
		}
		if i%16 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	_ = c.SetReadDeadline(time.Now().Add(hang))
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, res); res.StatusCode != http.StatusOK || !strings.Contains(body, "8:intervali1800e") {
		t.Fatalf("slow client: %d %q", res.StatusCode, body)
	}

	// Datagrams that are not tracker requests get no answer, and harm none.
	u, err := net.Dial("udp", r.UDPAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	for _, d := range [][]byte{{}, []byte("garbage"), make([]byte, 16), make([]byte, 1500)} {
		_, _ = u.Write(d)
	}
	_ = u.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := u.Read(make([]byte, 64)); err == nil {
		t.Fatalf("a garbage datagram was answered with %d bytes", n)
	}

	cl := newClient(t, e, time.Second, 8)
	resp := mustAnnounce(t, cl, udpURL(r, "/announce"), h, 2, 100, fibtracker.EventStarted)
	if len(resp.Peers) != 1 || resp.Complete != 1 {
		t.Fatalf("after the slow and cut clients: %+v", resp)
	}
}
