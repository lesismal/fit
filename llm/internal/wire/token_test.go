package wire

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/internal/linktoken"
)

// tokenSet is a listener's tokens, by ID.
type tokenSet map[string]linktoken.Secret

func (s tokenSet) Lookup(id string) ([]byte, error) {
	sec, ok := s[id]
	if !ok {
		return nil, linktoken.ErrUnknown
	}
	return sec.Stored(), nil
}

// A listener with tokens takes a dialer that proves one, with the token's
// roles, and proves it back; it refuses an unknown token, a forged proof and
// a password it was not given.
// 带令牌的监听方接受证明其中一个令牌的拨号方，连接得到令牌的角色，并回证该令牌；拒绝未知令牌、伪造的证明和未给定的密码。
func TestTokenHandshake(t *testing.T) {
	engine := SharedEngine(t)
	sec := linktoken.New()
	opened := make(chan *Conn, 4)
	addr := Listen(t, NewHandler(ConnConfig{
		Tokens: tokenSet{sec.ID: sec}, TokenRoles: []string{"node", "proxy"},
		OnOpen: func(c *Conn) { opened <- c },
	}))
	url := "ws://" + addr + Path
	dial := func(cfg ConnConfig) error {
		c, err := Dial(ctxT(t), url, DialConfig{Engine: engine, ConnConfig: cfg})
		if c != nil {
			c.Close()
		}
		return err
	}
	if err := dial(ConnConfig{Token: &sec}); err != nil {
		t.Fatal(err)
	}
	c := <-opened
	if c.Token != sec.ID || strings.Join(c.Roles, ",") != "node,proxy" {
		t.Fatalf("token %q roles %v", c.Token, c.Roles)
	}
	unknown := linktoken.New()
	if err := dial(ConnConfig{Token: &unknown}); !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "no such token") {
		t.Fatalf("unknown: %v", err)
	}
	forged, _ := linktoken.Parse(linktoken.New().String())
	forged.ID = sec.ID
	if err := dial(ConnConfig{Token: &forged}); !errors.Is(err, ErrAuth) {
		t.Fatalf("forged: %v", err)
	}
	// No password is taken: Tokens alone, no Credentials.
	if err := dial(ConnConfig{Auth: Password("pw")}); !errors.Is(err, ErrAuth) {
		t.Fatalf("password: %v", err)
	}
	// A listener of before tokens, or with none, refuses a token.
	plain := Listen(t, NewHandler(ConnConfig{Auth: Password("pw")}))
	if _, err := Dial(ctxT(t), "ws://"+plain+Path, DialConfig{Engine: engine, ConnConfig: ConnConfig{Token: &sec}}); !errors.Is(err, ErrAuth) {
		t.Fatalf("plain listener: %v", err)
	}
}

// A dialer with a token takes no listener that cannot prove it: one that only
// acknowledges, or proves nothing, or sends back the dialer's own proof.
// 带令牌的拨号方不接受无法证明令牌的监听方：只回确认的、证明不成立的，或把拨号方自己的证明发回的。
func TestTokenListenerProof(t *testing.T) {
	engine := SharedEngine(t)
	sec := linktoken.New()
	for name, ack := range map[string]func(answer []byte) []byte{
		"a plain ack":        func([]byte) []byte { return []byte{ackOK} },
		"a proof of nothing": func([]byte) []byte { return append([]byte{ackOK}, make([]byte, linktoken.ProofSize)...) },
		"the dialer's proof": func(a []byte) []byte { return append([]byte{ackOK}, a[len(a)-linktoken.ProofSize:]...) },
		"a proof too long":   func([]byte) []byte { return append([]byte{ackOK}, make([]byte, 2*linktoken.ProofSize)...) },
	} {
		var mu sync.Mutex
		var got []byte
		url := rawServer(t, websocket.HandlerFuncs{
			Open: func(ws *websocket.Connection, _ *http.Request) { _ = ws.WriteBinary(make([]byte, challengeSize)) },
			Message: func(ws *websocket.Connection, _ websocket.Opcode, data []byte) {
				mu.Lock()
				got = bytes.Clone(data)
				mu.Unlock()
				_ = ws.WriteBinary(ack(data))
			},
		})
		_, err := Dial(ctxT(t), url, DialConfig{Engine: engine, ConnConfig: ConnConfig{Token: &sec}})
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("%s: %v", name, err)
		}
		mu.Lock()
		if id, _, _, ok := parseTokenAnswer(got); !ok || id != sec.ID {
			t.Fatalf("%s: the dialer sent %x", name, got)
		}
		mu.Unlock()
	}
}

func TestParseTokenAnswer(t *testing.T) {
	sec := linktoken.New()
	good, _ := tokenAnswer(&sec, make([]byte, challengeSize))
	if _, _, _, ok := parseTokenAnswer(good); !ok {
		t.Fatal("a good answer refused")
	}
	for _, bad := range [][]byte{nil, []byte(tokenMagic), good[:len(good)-1], append(bytes.Clone(good), 0),
		append([]byte(tokenMagic), make([]byte, 1+linktoken.NonceSize+linktoken.ProofSize)...), []byte("XXXX" + string(good[4:]))} {
		if _, _, _, ok := parseTokenAnswer(bad); ok {
			t.Fatalf("%x taken", bad)
		}
	}
	s := &server{}
	if _, _, err := s.verifyToken(nil, good); !errors.Is(err, ErrAuth) {
		t.Fatal(err)
	}
}
