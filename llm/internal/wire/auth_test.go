package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/websocket"
)

// nodeAuth is a node's listener: hubs and proxies link to it, each with a password of their own.
// nodeAuth 节点的监听：hub 与 proxy 连接它，各用自己的密码。
func nodeAuth(t *testing.T) *ListenAuth {
	t.Helper()
	a, err := NewListenAuth("node", NewSalt(), map[string]string{"hub": "pw-hub", "proxy": "pw-proxy"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The listener checks the dialer's proof with the password of the role it says it is, and both ends learn the
// role the other proved: a proxy that knows the node's proxy password cannot link as a hub.
// 监听方用拨号方所声明角色的密码校验其证明，两端都得知对方证明的角色：知道节点 proxy 密码的 proxy 无法以 hub 身份连接。
func TestHandshakeBindsRoles(t *testing.T) {
	engine := SharedEngine(t)
	opened := make(chan *Conn, 4)
	failed := make(chan string, 4)
	addr := Listen(t, NewHandler(ConnConfig{
		ListenAuth:   nodeAuth(t),
		OnOpen:       func(c *Conn) { opened <- c },
		OnAuthFailed: func(_ *http.Request, role string) { failed <- role },
	}))
	url := "ws://" + addr + Path
	dial := func(a *DialAuth) (*Conn, error) {
		c, err := Dial(ctxT(t), url, DialConfig{Engine: engine, ConnConfig: ConnConfig{DialAuth: a}})
		if c != nil {
			t.Cleanup(c.Close)
		}
		return c, err
	}

	// The proxy's password, the hub's role: refused. / proxy 的密码、hub 的角色：拒绝。
	if _, err := dial(&DialAuth{Role: "hub", PeerRole: "node", Password: "pw-proxy"}); !errors.Is(err, ErrAuth) {
		t.Fatalf("a proxy linked as a hub: %v", err)
	}
	if r := <-failed; r != "hub" {
		t.Fatalf("OnAuthFailed saw role %q", r)
	}
	// A role the listener takes no links from, and the listener's own role. / 监听方不接受的角色，以及监听方自身的角色。
	for _, role := range []string{"edge", "node"} {
		if _, err := dial(&DialAuth{Role: role, Password: "pw-hub"}); !errors.Is(err, ErrAuth) {
			t.Fatalf("role %s: %v", role, err)
		}
		<-failed
	}
	// A dialer that expects a hub there gives up before proving anything. / 期望对端是 hub 的拨号方在证明之前就放弃。
	if _, err := dial(&DialAuth{Role: "proxy", PeerRole: "hub", Password: "pw-proxy"}); !errors.Is(err, ErrAuth) ||
		!strings.Contains(err.Error(), `"node"`) {
		t.Fatalf("wrong listener role: %v", err)
	}

	for _, role := range []string{"hub", "proxy"} {
		keys := &KeyCache{}
		for range 2 { // the second dial reuses the key / 第二次拨号复用密钥
			c, err := dial(&DialAuth{Role: role, PeerRole: "node", Password: "pw-" + role, Keys: keys})
			if err != nil {
				t.Fatalf("%s: %v", role, err)
			}
			sc := <-opened
			if sc.PeerRole() != role || c.PeerRole() != "node" {
				t.Fatalf("%s: the listener saw %q, the dialer %q", role, sc.PeerRole(), c.PeerRole())
			}
		}
	}
	select {
	case c := <-opened:
		t.Fatalf("a refused dial was opened as %q", c.PeerRole())
	default:
	}
}

// A listener that does not know the password cannot complete a dial: one that takes any proof (no ListenAuth)
// and one with other passwords are both refused by the dialer.
// 不知道密码的监听方无法完成拨号：接受任何证明的监听方（没有 ListenAuth）与使用其他密码的监听方都被拨号方拒绝。
func TestHandshakeListenerMustProve(t *testing.T) {
	engine := SharedEngine(t)
	other, err := NewListenAuth("node", NewSalt(), map[string]string{"hub": "guess", "proxy": "pw-hub"})
	if err != nil {
		t.Fatal(err)
	}
	impostors := map[string]*ListenAuth{"no password": nil, "other passwords": other}
	for name, a := range impostors {
		addr := Listen(t, NewHandler(ConnConfig{ListenAuth: a}))
		c, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{Engine: engine,
			ConnConfig: ConnConfig{DialAuth: &DialAuth{Role: "hub", Password: "pw-hub"}}})
		if c != nil {
			c.Close()
		}
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("%s: the dial completed: %v", name, err)
		}
	}

	// A raw listener that acks with a made-up proof. / 用编造的证明确认的原始监听方。
	url := rawServer(t, websocket.HandlerFuncs{
		Open: func(ws *websocket.Connection, _ *http.Request) {
			_ = ws.WriteBinary(challenge{salt: NewSalt(), nonce: newNonce(), role: "node"}.marshal())
		},
		Message: func(ws *websocket.Connection, _ websocket.Opcode, _ []byte) {
			_ = ws.WriteBinary(append([]byte{ackOK}, make([]byte, proofSize)...))
		},
	})
	if _, err := Dial(ctxT(t), url, DialConfig{Engine: engine,
		ConnConfig: ConnConfig{DialAuth: &DialAuth{Role: "hub", Password: "pw-hub"}}}); !errors.Is(err, ErrAuth) {
		t.Fatalf("made-up proof: %v", err)
	}
	// A challenge of another version. / 其他版本的挑战。
	url = rawServer(t, websocket.HandlerFuncs{
		Open: func(ws *websocket.Connection, _ *http.Request) { _ = ws.WriteBinary([]byte("challenge")) },
	})
	if _, err := Dial(ctxT(t), url, DialConfig{Engine: engine,
		ConnConfig: ConnConfig{DialAuth: &DialAuth{Role: "hub", Password: "pw-hub"}}}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("old challenge: %v", err)
	}
}

// Proofs are bound to the transcript: an answer replayed to another challenge is refused.
// 证明绑定到握手记录：重放到另一个挑战上的应答被拒绝。
func TestHandshakeReplay(t *testing.T) {
	a := nodeAuth(t)
	d := &dialerState{a: &DialAuth{Role: "hub", Password: "pw-hub"}}
	first := newListenerState(a)
	msg, err := d.answer(first.chal.marshal())
	if err != nil {
		t.Fatal(err)
	}
	role, ack, err := first.verify(msg)
	if err != nil || role != "hub" {
		t.Fatalf("verify: %q %v", role, err)
	}
	if err := d.accept(ack); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newListenerState(a).verify(msg); !errors.Is(err, ErrAuth) {
		t.Fatalf("replayed answer: %v", err)
	}
	// The listener's ack of one handshake does not prove another. / 一次握手的确认不能证明另一次。
	d2 := &dialerState{a: d.a}
	if _, err := d2.answer(newListenerState(a).chal.marshal()); err != nil {
		t.Fatal(err)
	}
	if err := d2.accept(ack); !errors.Is(err, ErrAuth) {
		t.Fatalf("replayed ack: %v", err)
	}
	if _, err := NewListenAuth(strings.Repeat("r", maxRoleLen+1), NewSalt(), nil); err == nil {
		t.Fatal("a long role")
	}
	if _, err := NewListenAuth("node", []byte("short"), nil); err == nil {
		t.Fatal("a short salt")
	}
}

// Before its handshake completes, a connection may send the upgrade request and the handshake's answer, not one of
// the 16 MiB messages an authenticated link may: one that starts such a message is dropped once it has sent
// maxPreAuthBytes, long before the handshake times out.
// 握手完成之前，连接只能发送升级请求与握手应答，而不是已认证连接可以发送的 16 MiB 消息：开始发送这种消息的连接在发送
// maxPreAuthBytes 之后即被断开，远早于握手超时。
func TestPreAuthBytesBounded(t *testing.T) {
	addr := Listen(t, NewHandler(ConnConfig{ListenAuth: nodeAuth(t)}))
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	_, err = io.WriteString(nc, "GET "+Path+" HTTP/1.1\r\nHost: "+addr+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	// A masked binary frame that says 8 MiB follow. / 声明后面有 8 MiB 的掩码二进制帧。
	hdr := []byte{0x82, 0x80 | 127, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
	binary.BigEndian.PutUint64(hdr[2:10], 8<<20)
	start := time.Now()
	_ = nc.SetDeadline(start.Add(HandshakeTimeout + 5*time.Second))
	go func() {
		_, _ = nc.Write(hdr)
		chunk := make([]byte, 4<<10)
		for range 2048 {
			if _, err := nc.Write(chunk); err != nil {
				return
			}
		}
	}()
	_, _ = io.Copy(io.Discard, br) // the challenge, then the close / 挑战，然后关闭
	if d := time.Since(start); d >= HandshakeTimeout {
		t.Fatalf("dropped after %v: the handshake timed out, not the pre-auth bound", d)
	}
}
