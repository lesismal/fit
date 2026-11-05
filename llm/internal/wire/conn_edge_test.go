package wire

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"
)

// rawServer serves plain WebSocket handlers on 127.0.0.1, standing in for a listener that breaks the handshake.
// rawServer 在 127.0.0.1 上提供普通 WebSocket 处理函数，充当破坏握手的监听方。
func rawServer(t *testing.T, h websocket.HandlerFuncs) string {
	t.Helper()
	e := SharedEngine(t)
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: websocket.NewHandlerWithConfig(websocket.DefaultConfig(), h)})
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := e.BindingAddrs(i)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveBinding(i) })
	return "ws://" + addrs[0].String() + Path
}

// rawDial connects to a listener without the wire handshake; the test drives it with h.
// rawDial 不经 wire 握手连接监听方；测试用 h 驱动它。
func rawDial(t *testing.T, addr string, h websocket.HandlerFuncs) {
	t.Helper()
	d := websocket.NewDialer(SharedEngine(t), websocket.DefaultDialerConfig())
	ws, _, err := d.Go("ws://"+addr+Path, nil, h).Wait()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close(websocket.CloseNormal, "") })
}

type authFunc func([]byte) ([]byte, error)

func (f authFunc) Prove(c []byte) ([]byte, error) { return f(c) }
func (authFunc) Verify(_, _ []byte) error         { return nil }

// A listener and a dialer with their own frame limit and no heartbeat; a closed connection's beat does nothing,
// and the dialer can drop its TCP connection.
// 自定帧上限且无心跳的监听方与拨号方；已关闭连接的 beat 什么也不做，拨号方可以断开其 TCP 连接。
func TestConnOptionsAndAbort(t *testing.T) {
	opened := make(chan *Conn, 2)
	closed := make(chan error, 2)
	cfg := ConnConfig{Session: Options{MaxFrame: 1 << 20}, PingInterval: -1}
	lcfg := cfg
	lcfg.OnOpen = func(c *Conn) { opened <- c }
	lcfg.OnClose = func(_ *Conn, err error) { closed <- err }
	l := NewHandler(lcfg)
	addr := Listen(t, l)
	if l.lookup("192.0.2.1:1") != nil {
		t.Fatal("lookup found an unknown address")
	}

	d, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{ConnConfig: cfg, Engine: SharedEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	c := <-opened
	if c.hb != nil || d.hb != nil || d.RemoteAddr() != "" || c.RemoteAddr() == "" {
		t.Fatalf("heartbeat armed (%v %v) or bad addrs %q %q", c.hb, d.hb, d.RemoteAddr(), c.RemoteAddr())
	}
	d.Close()
	<-closed
	d.beat(time.Second) // closed: returns at once / 已关闭：立即返回

	d, err = Dial(ctxT(t), "ws://"+addr+Path, DialConfig{ConnConfig: cfg, Engine: SharedEngine(t)})
	if err != nil {
		t.Fatal(err)
	}
	<-opened
	d.abort()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not see the dropped connection")
	}
}

// Once CloseAll ran, the listener closes every new connection at once.
// CloseAll 之后，监听方立即关闭每条新连接。
func TestListenerClosedRefusesNew(t *testing.T) {
	l := NewHandler(ConnConfig{})
	addr := Listen(t, l)
	l.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := Dial(ctx, "ws://"+addr+Path, DialConfig{Engine: SharedEngine(t)}); err == nil {
		c.Close()
		t.Fatal("dial succeeded after CloseAll")
	}
}

type plainHandler struct{}

func (plainHandler) OnOpen(*fib.Connection)                 {}
func (plainHandler) OnData(*fib.Connection, []byte)         {}
func (plainHandler) OnPriorityData(*fib.Connection, []byte) {}
func (plainHandler) OnClose(*fib.Connection, error)         {}

type fullHandler struct {
	plainHandler
	shutdown, engineClose int
}

func (h *fullHandler) OnShutdown(*fib.Connection)    { h.shutdown++ }
func (h *fullHandler) OnEngineClose(*fib.Connection) { h.engineClose++ }

// tcpConns passes OnShutdown and OnEngineClose on only to a handler that has them.
// tcpConns 只把 OnShutdown 与 OnEngineClose 转给实现了它们的处理器。
func TestTCPConnsOptionalHandlers(t *testing.T) {
	(&tcpConns{Handler: plainHandler{}}).OnShutdown(nil)
	(&tcpConns{Handler: plainHandler{}}).OnEngineClose(nil)
	h := &fullHandler{}
	tc := &tcpConns{Handler: h}
	tc.OnShutdown(nil)
	tc.OnEngineClose(nil)
	if h.shutdown != 1 || h.engineClose != 1 {
		t.Fatalf("shutdown %d, engine close %d", h.shutdown, h.engineClose)
	}
}

// An authenticated peer that sends a malformed frame is dropped.
// 发送畸形帧的已认证对端被断开。
func TestConnProtocolError(t *testing.T) {
	closed := make(chan error, 1)
	addr := Listen(t, NewHandler(ConnConfig{OnClose: func(_ *Conn, err error) { closed <- err }}))
	var n int
	var mu sync.Mutex
	rawDial(t, addr, websocket.HandlerFuncs{Message: func(ws *websocket.Connection, _ websocket.Opcode, _ []byte) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			_ = ws.WriteBinary(nil) // the proof / 证明
		} else {
			_ = ws.WriteBinary([]byte{0xff}) // after the ack: garbage / 确认之后：垃圾数据
		}
	}})
	select {
	case err := <-closed:
		if !errors.Is(err, ErrBadFrame) {
			t.Fatalf("closed with %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("peer not dropped")
	}
}

// A dialer that never answers the challenge is dropped after HandshakeTimeout.
// 从不应答挑战的拨号方在 HandshakeTimeout 之后被断开。
func TestHandshakeTimeout(t *testing.T) {
	t.Parallel()
	addr := Listen(t, NewHandler(ConnConfig{Auth: Password("pw")}))
	codes := make(chan uint16, 1)
	rawDial(t, addr, websocket.HandlerFuncs{Close: func(_ *websocket.Connection, code uint16, _ string, _ error) { codes <- code }})
	select {
	case code := <-codes:
		if code != closeAuthFailed {
			t.Fatalf("closed with code %d", code)
		}
	case <-time.After(HandshakeTimeout + 3*time.Second):
		t.Fatal("silent dialer not dropped")
	}
}

// Dials that fail: no engine, a proof that cannot be made, a listener that breaks or stalls the handshake.
// 失败的拨号：没有引擎、无法给出证明、监听方破坏或拖住握手。
func TestDialFailures(t *testing.T) {
	engine := SharedEngine(t)
	if _, err := Dial(ctxT(t), "ws://127.0.0.1:1/ws", DialConfig{}); err == nil {
		t.Fatal("dial without an engine")
	}

	failed := make(chan string, 1)
	addr := Listen(t, NewHandler(ConnConfig{Auth: Password("pw"), OnAuthFailed: func(r *http.Request) { failed <- r.URL.Path }}))
	if _, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{Engine: engine, ConnConfig: ConnConfig{Auth: Password("no")}}); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
	if p := <-failed; p != Path {
		t.Fatalf("OnAuthFailed saw %q", p)
	}
	boom := errors.New("no proof")
	_, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{Engine: engine, ConnConfig: ConnConfig{
		Auth: authFunc(func([]byte) ([]byte, error) { return nil, boom }),
	}})
	if !errors.Is(err, boom) {
		t.Fatalf("prove error: %v", err)
	}

	// A bad ack. / 错误的确认。
	url := rawServer(t, websocket.HandlerFuncs{
		Open:    func(ws *websocket.Connection, _ *http.Request) { _ = ws.WriteBinary(make([]byte, challengeSize)) },
		Message: func(ws *websocket.Connection, _ websocket.Opcode, _ []byte) { _ = ws.WriteBinary([]byte{9, 9}) },
	})
	if _, err := Dial(ctxT(t), url, DialConfig{Engine: engine}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("bad ack: %v", err)
	}

	// Closed during the handshake. / 握手期间被关闭。
	url = rawServer(t, websocket.HandlerFuncs{
		Open: func(ws *websocket.Connection, _ *http.Request) { _ = ws.Close(websocket.CloseNormal, "bye") },
	})
	if _, err := Dial(ctxT(t), url, DialConfig{Engine: engine}); err == nil {
		t.Fatal("closed handshake: no error")
	}

	// A challenge but no ack: the context ends the dial, which closes the connection.
	// 有挑战但没有确认：context 结束拨号，并关闭连接。
	gone := make(chan struct{}, 1)
	url = rawServer(t, websocket.HandlerFuncs{
		Open:  func(ws *websocket.Connection, _ *http.Request) { _ = ws.WriteBinary(make([]byte, challengeSize)) },
		Close: func(*websocket.Connection, uint16, string, error) { gone <- struct{}{} },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Dial(ctx, url, DialConfig{Engine: engine}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no ack: %v", err)
	}
	select {
	case <-gone:
	case <-time.After(3 * time.Second):
		t.Fatal("abandoned dial left its connection open")
	}

	// No challenge before the context ends; one that comes later is answered with a close.
	// context 结束前没有挑战；之后到来的挑战以关闭作答。
	late := make(chan *websocket.Connection, 1)
	url = rawServer(t, websocket.HandlerFuncs{
		Open:  func(ws *websocket.Connection, _ *http.Request) { late <- ws },
		Close: func(*websocket.Connection, uint16, string, error) { gone <- struct{}{} },
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if _, err := Dial(ctx2, url, DialConfig{Engine: engine}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no challenge: %v", err)
	}
	ws := <-late
	_ = ws.WriteBinary(make([]byte, challengeSize))
	select {
	case <-gone:
	case <-time.After(3 * time.Second):
		t.Fatal("late challenge not answered with a close")
	}
}

// The listener proves the password too: a dialer with a password takes no listener that only acknowledges, or
// acknowledges with a proof that is not one, and answers no challenge of another size, which could be a listener's
// proof to be had from it. A dialer of an earlier version, which sends no nonce, still gets in, with the plain ack.
// 监听方也要证明密码：有密码的拨号方不接受只回确认、或所附证明不成立的监听方，也不应答其他长度的挑战（那可能是想从它这里套取
// 监听方的证明）。不发送随机数的早先版本拨号方仍可接入，得到普通确认。
func TestMutualAuth(t *testing.T) {
	engine := SharedEngine(t)
	dialPW := func(url string) error {
		c, err := Dial(ctxT(t), url, DialConfig{Engine: engine, ConnConfig: ConnConfig{Auth: Password("pw")}})
		if c != nil {
			c.Close()
		}
		return err
	}
	for name, ack := range map[string][]byte{
		"a plain ack":        {ackOK},
		"a proof of nothing": append([]byte{ackOK}, make([]byte, proofSize)...),
	} {
		var got []byte
		var mu sync.Mutex
		url := rawServer(t, websocket.HandlerFuncs{
			Open: func(ws *websocket.Connection, _ *http.Request) { _ = ws.WriteBinary(make([]byte, challengeSize)) },
			Message: func(ws *websocket.Connection, _ websocket.Opcode, data []byte) {
				mu.Lock()
				got = bytes.Clone(data)
				mu.Unlock()
				_ = ws.WriteBinary(ack)
			},
		})
		if err := dialPW(url); !errors.Is(err, ErrAuth) {
			t.Fatalf("an impersonating listener with %s: %v", name, err)
		}
		mu.Lock()
		if len(got) != proofSize+NonceSize {
			t.Fatalf("the dialer sent %d bytes", len(got))
		}
		mu.Unlock()
	}
	// A challenge that is a listener's proof's input. / 作为监听方证明输入的挑战。
	url := rawServer(t, websocket.HandlerFuncs{
		Open: func(ws *websocket.Connection, _ *http.Request) {
			_ = ws.WriteBinary(append([]byte(listenerLabel), make([]byte, challengeSize+NonceSize)...))
		},
	})
	if err := dialPW(url); !errors.Is(err, ErrProtocol) {
		t.Fatalf("a long challenge: %v", err)
	}

	addr := Listen(t, NewHandler(ConnConfig{Auth: Password("pw")}))
	if err := dialPW("ws://" + addr + Path); err != nil {
		t.Fatalf("both of this version: %v", err)
	}
	old := authFunc(func(c []byte) ([]byte, error) { return Password("pw").Prove(c) })
	c, err := Dial(ctxT(t), "ws://"+addr+Path, DialConfig{Engine: engine, ConnConfig: ConnConfig{Auth: old}})
	if err != nil {
		t.Fatalf("a dialer of an earlier version: %v", err)
	}
	c.Close()
	if Password("").Mutual() || Password("pw").VerifyListener([]byte("c"), []byte("n"), nil) == nil {
		t.Fatal("an empty password proves both ways, or a listener proved nothing")
	}
}

// A listener with credentials takes a dialer that proves any of them, gives the connection the roles of the one it
// proved, and proves that same password back: a dialer of each password links (its own check of the listener's
// proof passes), with its roles, and so does one of an earlier version; a password of none fails.
// 带凭据的监听方接受证明其中任一凭据的拨号方，连接得到被证明凭据的角色，并用同一密码回证：每个密码的拨号方都能连上（其对监听方
// 证明的校验通过），带着各自的角色，早先版本的拨号方也一样；不属于任何凭据的密码失败。
func TestCredentials(t *testing.T) {
	engine := SharedEngine(t)
	opened := make(chan []string, 4)
	addr := Listen(t, NewHandler(ConnConfig{
		Credentials: []Credential{{Password: "np", Roles: []string{"node"}}, {Password: "pp", Roles: []string{"proxy", "hub"}}},
		OnOpen:      func(c *Conn) { opened <- c.Roles },
	}))
	url := "ws://" + addr + Path
	dial := func(auth Authenticator) error {
		c, err := Dial(ctxT(t), url, DialConfig{Engine: engine, ConnConfig: ConnConfig{Auth: auth}})
		if c != nil {
			c.Close()
		}
		return err
	}
	for _, tc := range []struct {
		auth Authenticator
		want string
	}{
		{Password("np"), "node"},
		{Password("pp"), "proxy,hub"},
		{authFunc(func(c []byte) ([]byte, error) { return Password("np").Prove(c) }), "node"}, // no nonce / 不带随机数
	} {
		if err := dial(tc.auth); err != nil {
			t.Fatalf("%v: %v", tc.want, err)
		}
		if got := strings.Join(<-opened, ","); got != tc.want {
			t.Fatalf("roles %q, want %q", got, tc.want)
		}
	}
	for _, pw := range []Password{"other", ""} {
		if err := dial(pw); !errors.Is(err, ErrAuth) {
			t.Fatalf("password %q: %v", pw, err)
		}
	}
}

// A dial whose context ends while its connection is being set up closes that connection; one whose context ends
// as the handshake fails reports the failure or the context, whichever settled first.
// context 在连接建立过程中结束的拨号会关闭该连接；context 在握手失败的同时结束的拨号报告先确定的那个结果。
func TestDialContextRaces(t *testing.T) {
	engine := SharedEngine(t)
	closed := make(chan struct{}, 1)
	addr := Listen(t, NewHandler(ConnConfig{OnClose: func(*Conn, error) { closed <- struct{}{} }}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Dial(ctx, "ws://"+addr+Path, DialConfig{Engine: engine, ConnConfig: ConnConfig{
		Handlers: func(*Conn) Handlers {
			cancel()
			time.Sleep(50 * time.Millisecond) // let Dial give up first / 让 Dial 先放弃
			return Handlers{}
		},
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("connection of an abandoned dial left open")
	}

	boom := errors.New("no proof")
	for range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := Dial(ctx, "ws://"+addr+Path, DialConfig{Engine: engine, ConnConfig: ConnConfig{
			Auth: authFunc(func([]byte) ([]byte, error) { cancel(); return nil, boom }),
		}})
		cancel()
		if !errors.Is(err, boom) && !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	}
}
