package ws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// The releases before fib's WebSocket framed their links themselves, as
// RFC 6455 has it: the handshakes below, byte for byte, then final binary
// frames of at most 1 MiB, masked from the client, a pong to each ping and
// a close frame echoed; no fragments and no pings of their own. They
// parsed what arrived with fib's websocket.Parser, at most 2 MiB a
// message. These tests speak that to this release's listener and dialer,
// which must stay within it.

// oldMaxFrame and oldMaxMessage are the earlier releases' limits.
const (
	oldMaxFrame   = 1 << 20
	oldMaxMessage = 2 * oldMaxFrame
)

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h[:])
}

// switched is the earlier releases' answer to a handshake with key.
func switched(key string) string {
	return "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + acceptKey(key) + "\r\n\r\n"
}

// frameHead is the head of a final frame of op with n bytes of payload,
// with a random mask key at its end when masked.
func frameHead(op websocket.Opcode, n int, masked bool) []byte {
	head := []byte{0x80 | byte(op), 0}
	switch {
	case n < 126:
		head[1] = byte(n)
	case n <= 0xffff:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	if masked {
		head[1] |= 0x80
		var key [4]byte
		_, _ = rand.Read(key[:])
		head = append(head, key[:]...)
	}
	return head
}

func mask(b, key []byte) {
	for i := range b {
		b[i] ^= key[i&3]
	}
}

// clientFrame is a final frame of op with payload, masked as a client's
// must be.
func clientFrame(op websocket.Opcode, payload []byte) []byte {
	head := frameHead(op, len(payload), true)
	b := append(head, payload...)
	mask(b[len(head):], head[len(head)-4:])
	return b
}

func serverFrame(op websocket.Opcode, payload []byte) []byte {
	return append(frameHead(op, len(payload), false), payload...)
}

// oldFrames is how the earlier releases sent b: final binary frames of at
// most oldMaxFrame, masked when client.
func oldFrames(b []byte, client bool) []byte {
	var out []byte
	for len(b) > 0 {
		n := min(len(b), oldMaxFrame)
		if client {
			out = append(out, clientFrame(websocket.Binary, b[:n])...)
		} else {
			out = append(out, serverFrame(websocket.Binary, b[:n])...)
		}
		b = b[n:]
	}
	return out
}

// readFrame reads an unmasked frame off r.
func readFrame(t *testing.T, r io.Reader) (websocket.Opcode, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		t.Fatal(err)
	}
	if h[1]&0x80 != 0 {
		t.Fatal("a server's frame is masked")
	}
	n := int(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		_, _ = io.ReadFull(r, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		_, _ = io.ReadFull(r, b[:])
		n = int(binary.BigEndian.Uint64(b[:]))
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		t.Fatal(err)
	}
	return websocket.Opcode(h[0] & 0x0f), p
}

// readClientFrame reads a small masked frame and unmasks it.
func readClientFrame(br *bufio.Reader) (websocket.Opcode, []byte) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return 0, nil
	}
	n := int(h[1] & 0x7f)
	var key [4]byte
	_, _ = io.ReadFull(br, key[:])
	p := make([]byte, n)
	_, _ = io.ReadFull(br, p)
	mask(p, key[:])
	return websocket.Opcode(h[0] & 0x0f), p
}

// parseAll feeds what r gives to an earlier release's parser until it has
// n bytes of binary payload, and fails on a message it would have refused
// or a frame it would not have sent.
func parseAll(r io.Reader, parser *websocket.Parser, n int) ([]byte, error) {
	var got []byte
	buf := make([]byte, 64<<10)
	for len(got) < n {
		k, err := r.Read(buf)
		if err != nil {
			return got, fmt.Errorf("after %d bytes: %w", len(got), err)
		}
		data := buf[:k]
		for {
			ev, ok, err := parser.FeedOne(data)
			data = nil
			if err != nil {
				return got, fmt.Errorf("an earlier release would refuse the frame: %w", err)
			}
			if !ok {
				break
			}
			if ev.Opcode != websocket.Binary || len(ev.Payload) > oldMaxFrame {
				return got, fmt.Errorf("a message of %v, %d bytes", ev.Opcode, len(ev.Payload))
			}
			got = append(got, ev.Payload...)
		}
	}
	return got, nil
}

// A node or proxy of an earlier release links to this release's hub: its
// handshake opens the link, its frames reach the link whole, and what the
// link sends, a 3 MiB frame of it too, comes in frames its parser takes.
func TestOldClient(t *testing.T) {
	l, _ := listen(t, "/tunnel", true)
	c := rawDial(t, l)
	host := strings.TrimSuffix(strings.TrimPrefix(l.URL(), "ws://"), "/tunnel")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	req := "GET /tunnel HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\nUser-Agent: fit\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	rsp, err := stdhttp.ReadResponse(br, nil)
	if err != nil || rsp.StatusCode != stdhttp.StatusSwitchingProtocols || rsp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		t.Fatalf("handshake: %v, %v", rsp, err)
	}
	want := make([]byte, 3*oldMaxFrame+5)
	_, _ = rand.Read(want)
	go func() { _, _ = c.Write(oldFrames(want, true)) }()
	got, err := parseAll(br, websocket.NewServerFrameParser(oldMaxMessage), len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("the echo differs")
	}
	_, _ = c.Write(clientFrame(websocket.Close, []byte{0x03, 0xe8}))
	if op, p := readFrame(t, br); op != websocket.Close || !bytes.Equal(p, []byte{0x03, 0xe8}) {
		t.Fatalf("answer to a close: %v %x", op, p)
	}
}

// A node or proxy of this release links to a hub of an earlier release:
// the hub's answer opens the link, what the link sends comes in frames the
// hub's parser takes, and the hub's frames reach the link whole.
func TestOldServer(t *testing.T) {
	want := make([]byte, 3*oldMaxFrame+5)
	_, _ = rand.Read(want)
	type parsed struct {
		b   []byte
		err error
	}
	got := make(chan parsed, 1)
	reqs := make(chan *stdhttp.Request, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		br := bufio.NewReader(c)
		req, err := stdhttp.ReadRequest(br)
		if err != nil {
			return
		}
		reqs <- req
		_, _ = c.Write([]byte(switched(req.Header.Get("Sec-WebSocket-Key"))))
		_, _ = c.Write(oldFrames(want, false))
		b, err := parseAll(br, websocket.NewParser(oldMaxMessage), len(want))
		got <- parsed{b, err}
		_, _ = c.Write(serverFrame(websocket.Close, []byte{0x03, 0xe8}))
		_, _ = io.Copy(io.Discard, br)
	}()
	var r *recorder
	conn, err := transport.Dial(tunneltest.Engine(t), "ws://"+ln.Addr().String()+"/tunnel", 5*time.Second, transport.ClientOptions{}, func(c transport.Conn) transport.Handler {
		r = newRecorder(c, false)
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := <-reqs
	if req.URL.Path != "/tunnel" || req.Header.Get("Connection") != "Upgrade" || req.Header.Get("Upgrade") != "websocket" ||
		req.Header.Get("Sec-WebSocket-Version") != "13" || req.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatalf("handshake %+v", req)
	}
	if err := conn.Send(bytes.Clone(want)); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.err != nil || !bytes.Equal(p.b, want) {
		t.Fatalf("the hub got another stream: %v", p.err)
	}
	waitFor(t, "the hub's frames", func() bool { return r.len() >= len(want) })
	if !bytes.Equal(r.bytes(), want) {
		t.Fatal("the link got another stream")
	}
	if err := waitClosed(t, r.closed); err != nil {
		t.Errorf("a plain close: %v", err)
	}
}
