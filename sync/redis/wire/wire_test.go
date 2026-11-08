package wire

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a server answering each command with what answer returns, raw
// RESP, and keeping the commands it was sent.
type fake struct {
	ln     net.Listener
	answer func(args []string) string
	mu     sync.Mutex
	cmds   [][]string
}

func newFake(t *testing.T, tlsCfg *tls.Config, answer func(args []string) string) *fake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	f := &fake{ln: ln, answer: answer}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fake) addr() string { return f.ln.Addr().String() }

func (f *fake) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, args)
		f.mu.Unlock()
		reply := f.answer(args)
		if reply == "" {
			continue // no answer: the client waits
		}
		if _, err := io.WriteString(c, reply); err != nil {
			return
		}
	}
}

func (f *fake) sent() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.cmds...)
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || line[0] != '*' {
		return nil, errors.New("not a command")
	}
	args := make([]string, n)
	for i := range args {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		l, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
		buf := make([]byte, l+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args[i] = string(buf[:l])
	}
	return args, nil
}

func dial(t *testing.T, o Options) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestReplies reads a reply of every RESP3 type, an attribute before one and
// a push between two.
func TestReplies(t *testing.T) {
	replies := map[string]string{
		"simple":   "+OK\r\n",
		"error":    "-WRONGTYPE Operation against a key\r\n",
		"int":      ":-42\r\n",
		"bulk":     "$5\r\nhe\r\nl\r\n",
		"nullbulk": "$-1\r\n",
		"nullarr":  "*-1\r\n",
		"null":     "_\r\n",
		"double":   ",3.5\r\n",
		"inf":      ",-inf\r\n",
		"bool":     "#t\r\n",
		"big":      "(3492890328409238509324850943850943825024385\r\n",
		"verbatim": "=15\r\ntxt:Some string\r\n",
		"blobErr":  "!21\r\nSYNTAX invalid syntax\r\n",
		"map":      "%2\r\n+a\r\n:1\r\n+b\r\n*2\r\n:2\r\n$1\r\nx\r\n",
		"set":      "~2\r\n+a\r\n+b\r\n",
		"attr":     "|1\r\n+key-popularity\r\n%1\r\n$1\r\na\r\n,0.19\r\n:7\r\n",
		"push":     ">3\r\n+invalidate\r\n*1\r\n+k\r\n_\r\n:8\r\n",
	}
	f := newFake(t, nil, func(args []string) string {
		if args[0] == "HELLO" {
			return "%1\r\n+proto\r\n:3\r\n"
		}
		return replies[args[1]]
	})
	c := dial(t, Options{Addr: f.addr()})
	if c.Protocol() != 3 {
		t.Fatalf("protocol %d", c.Protocol())
	}
	check := func(name string, want Value) {
		t.Helper()
		v, err := c.Do(context.Background(), "ECHO", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got, w := describe(v), describe(want); got != w {
			t.Fatalf("%s: %s, want %s", name, got, w)
		}
	}
	check("simple", Value{Kind: KindString, Str: "OK"})
	check("error", Value{Kind: KindError, Str: "WRONGTYPE Operation against a key"})
	check("int", Value{Kind: KindInt, Int: -42})
	check("bulk", Value{Kind: KindBulk, Str: "he\r\nl"})
	check("nullbulk", Value{})
	check("nullarr", Value{})
	check("null", Value{})
	check("double", Value{Kind: KindDouble, Str: "3.5", Float: 3.5})
	check("bool", Value{Kind: KindBool, Int: 1})
	check("big", Value{Kind: KindBigNumber, Str: "3492890328409238509324850943850943825024385"})
	check("verbatim", Value{Kind: KindVerbatim, Str: "Some string"})
	check("blobErr", Value{Kind: KindError, Str: "SYNTAX invalid syntax"})
	check("map", Value{Kind: KindMap, Elems: []Value{{Kind: KindString, Str: "a"}, {Kind: KindInt, Int: 1}, {Kind: KindString, Str: "b"},
		{Kind: KindArray, Elems: []Value{{Kind: KindInt, Int: 2}, {Kind: KindBulk, Str: "x"}}}}})
	check("set", Value{Kind: KindSet, Elems: []Value{{Kind: KindString, Str: "a"}, {Kind: KindString, Str: "b"}}})
	check("attr", Value{Kind: KindInt, Int: 7})
	check("push", Value{Kind: KindInt, Int: 8})
	v, _ := c.Do(context.Background(), "ECHO", "inf")
	if v.Kind != KindDouble || v.Float > -1e308 {
		t.Errorf("-inf read as %+v", v)
	}
	var e *Error
	if v, _ := c.Do(context.Background(), "ECHO", "error"); !errors.As(v.Err(), &e) || e.Code() != "WRONGTYPE" {
		t.Errorf("error %v", v.Err())
	}
}

func describe(v Value) string {
	var b strings.Builder
	b.WriteString(v.Kind.String() + "(" + v.Str + "," + strconv.FormatInt(v.Int, 10) + "," + strconv.FormatFloat(v.Float, 'g', -1, 64))
	for _, e := range v.Elems {
		b.WriteString(" " + describe(e))
	}
	b.WriteString(")")
	return b.String()
}

// TestPipeline sends commands at once and reads their replies in order, an
// error among them.
func TestPipeline(t *testing.T) {
	f := newFake(t, nil, func(args []string) string {
		switch args[0] {
		case "HELLO":
			return "%0\r\n"
		case "BAD":
			return "-ERR bad\r\n"
		}
		return "$" + strconv.Itoa(len(args[1])) + "\r\n" + args[1] + "\r\n"
	})
	c := dial(t, Options{Addr: f.addr()})
	vs, err := c.Pipeline(context.Background(), [][]string{{"ECHO", "a"}, {"BAD"}, {"ECHO", ""}, {"ECHO", strings.Repeat("z", 100000)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 4 || vs[0].Str != "a" || vs[1].Err() == nil || vs[2].Kind != KindBulk || vs[2].Str != "" || len(vs[3].Str) != 100000 {
		t.Fatalf("%v", vs)
	}
}

// TestHandshake authenticates with HELLO in RESP3, falls back to AUTH in
// RESP2 when the server does not know HELLO, and speaks RESP2 when told.
func TestHandshake(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  int
		knowHello bool
		want      [][]string
		proto     int
	}{
		{"resp3", 0, true, [][]string{{"HELLO", "3", "AUTH", "u", "p", "SETNAME", "n"}, {"SELECT", "2"}}, 3},
		{"fallback", 0, false, [][]string{{"HELLO", "3", "AUTH", "u", "p", "SETNAME", "n"}, {"AUTH", "u", "p"}, {"CLIENT", "SETNAME", "n"}, {"SELECT", "2"}}, 2},
		{"resp2", 2, true, [][]string{{"AUTH", "u", "p"}, {"CLIENT", "SETNAME", "n"}, {"SELECT", "2"}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, nil, func(args []string) string {
				if args[0] == "HELLO" && !tc.knowHello {
					return "-ERR unknown command 'HELLO', with args beginning with: '3'\r\n"
				}
				if args[0] == "HELLO" {
					return "%1\r\n+proto\r\n:3\r\n"
				}
				return "+OK\r\n"
			})
			c := dial(t, Options{Addr: f.addr(), Username: "u", Password: "p", DB: 2, ClientName: "n", Protocol: tc.protocol})
			if c.Protocol() != tc.proto {
				t.Errorf("protocol %d", c.Protocol())
			}
			if got := f.sent(); !equal(got, tc.want) {
				t.Errorf("sent %q", got)
			}
		})
	}
	// A wrong password is an error, not a fall back.
	f := newFake(t, nil, func(args []string) string {
		return "-WRONGPASS invalid username-password pair or user is disabled.\r\n"
	})
	if _, err := Dial(context.Background(), Options{Addr: f.addr(), Password: "p"}); err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("%v", err)
	}
	if got := f.sent(); len(got) != 1 || got[0][3] != "default" {
		t.Fatalf("sent %q", got)
	}
}

func equal(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.Join(a[i], "\x00") != strings.Join(b[i], "\x00") {
			return false
		}
	}
	return true
}

// TestLimits breaks the connection on a reply past its limits.
func TestLimits(t *testing.T) {
	replies := map[string]string{
		"bulk":  "$100\r\n" + strings.Repeat("x", 100) + "\r\n",
		"elems": "*3\r\n:1\r\n:2\r\n:3\r\n",
		"deep":  "*1\r\n*1\r\n*1\r\n:1\r\n",
		"bytes": "*2\r\n$40\r\n" + strings.Repeat("x", 40) + "\r\n$40\r\n" + strings.Repeat("x", 40) + "\r\n",
		"crlf":  "$2\r\nabcd\r\n",
		"len":   "$-2\r\n",
		"type":  "?1\r\n",
	}
	f := newFake(t, nil, func(args []string) string {
		if args[0] == "HELLO" {
			return "%0\r\n"
		}
		return replies[args[1]]
	})
	for name := range replies {
		c := dial(t, Options{Addr: f.addr(), Limits: Limits{MaxBulk: 50, MaxElems: 2, MaxDepth: 2, MaxBytes: 60}})
		if _, err := c.Do(context.Background(), "ECHO", name); err == nil || !errors.Is(c.Broken(), ErrBroken) {
			t.Errorf("%s: %v, %v", name, err, c.Broken())
		}
		if _, err := c.Do(context.Background(), "PING"); err == nil {
			t.Errorf("%s: a broken connection answered", name)
		}
	}
}

// TestCancel interrupts a command waiting for its reply when its context
// ends, breaking the connection.
func TestCancel(t *testing.T) {
	f := newFake(t, nil, func(args []string) string {
		if args[0] == "HELLO" {
			return "%0\r\n"
		}
		return "" // never answered
	})
	c := dial(t, Options{Addr: f.addr()})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := c.Do(ctx, "XREAD", "BLOCK", "0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if c.Broken() == nil {
		t.Fatal("not broken")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c = dial(t, Options{Addr: f.addr()})
	if _, err := c.Do(ctx, "XREAD", "BLOCK", "0"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
}

// TestTLS dials over TLS, the server's certificate checked.
func TestTLS(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "redis.test"}, DNSNames: []string{"redis.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	f := newFake(t, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, func(args []string) string {
		if args[0] == "HELLO" {
			return "%0\r\n"
		}
		return "+PONG\r\n"
	})
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	c := dial(t, Options{Addr: f.addr(), TLS: &tls.Config{RootCAs: pool, ServerName: "redis.test"}})
	if v, err := c.Do(context.Background(), "PING"); err != nil || v.Str != "PONG" {
		t.Fatalf("%v %v", v, err)
	}
	// Another name, or no CA, fails the handshake.
	if _, err := Dial(context.Background(), Options{Addr: f.addr(), TLS: &tls.Config{RootCAs: pool, ServerName: "other.test"}}); err == nil {
		t.Fatal("a certificate of another name was taken")
	}
	if _, err := Dial(context.Background(), Options{Addr: f.addr(), TLS: &tls.Config{}}); err == nil {
		t.Fatal("a certificate of an unknown CA was taken")
	}
}
