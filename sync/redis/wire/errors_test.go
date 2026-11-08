package wire

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// TestBadReplies breaks the connection on replies that are not RESP.
func TestBadReplies(t *testing.T) {
	replies := map[string]string{
		"long":     "+" + strings.Repeat("x", 40<<10) + "\r\n",
		"nocr":     "+OK\n",
		"empty":    "\r\n",
		"int":      ":x\r\n",
		"bool":     "#x\r\n",
		"double":   ",x\r\n",
		"verbatim": "=3\r\nabc\r\n",
		"arrlen":   "*x\r\n",
		"elem":     "*1\r\n:x\r\n",
		"short":    "$10\r\nab", // then the server closes
	}
	f := newFake(t, nil, func(args []string) string {
		if args[0] == "HELLO" {
			return "%0\r\n"
		}
		return replies[args[1]]
	})
	for name := range replies {
		c := dial(t, Options{Addr: f.addr()})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		if _, err := c.Do(ctx, "ECHO", name); err == nil || c.Broken() == nil {
			t.Errorf("%s: %v", name, err)
		}
		cancel()
	}
}

// TestDoubles reads RESP3's doubles, infinities and NaN among them.
func TestDoubles(t *testing.T) {
	f := newFake(t, nil, func(args []string) string {
		if args[0] == "HELLO" {
			return "%0\r\n"
		}
		return "," + args[1] + "\r\n"
	})
	c := dial(t, Options{Addr: f.addr()})
	for _, s := range []string{"inf", "-inf", "nan", "1e3"} {
		v, err := c.Do(context.Background(), "ECHO", s)
		if err != nil || v.Kind != KindDouble || v.Str != s {
			t.Errorf("%s: %+v %v", s, v, err)
		}
		if text, ok := v.Text(); !ok || text != s {
			t.Errorf("%s: text %q", s, text)
		}
	}
}

func TestValue(t *testing.T) {
	if s, ok := (Value{Kind: KindInt, Int: -3}).Text(); !ok || s != "-3" {
		t.Error(s)
	}
	if _, ok := (Value{Kind: KindArray}).Text(); ok {
		t.Error("an array's text")
	}
	if p, ok := (Value{Kind: KindMap, Elems: make([]Value, 2)}).Pairs(); !ok || len(p) != 2 {
		t.Error("a map's pairs")
	}
	if _, ok := (Value{Kind: KindArray, Elems: make([]Value, 3)}).Pairs(); ok {
		t.Error("an odd array's pairs")
	}
	if _, ok := (Value{Kind: KindInt}).Pairs(); ok {
		t.Error("an integer's pairs")
	}
	f := newFake(t, nil, func(args []string) string {
		if args[0] == "HELLO" {
			return "%0\r\n"
		}
		return "#f\r\n"
	})
	if v, err := dial(t, Options{Addr: f.addr()}).Do(context.Background(), "X"); err != nil || v.Kind != KindBool || v.Int != 0 {
		t.Errorf("#f: %+v %v", v, err)
	}
	if (Value{}).Err() != nil || !(Value{}).IsNull() {
		t.Error("null")
	}
	if Kind(200).String() != "kind(200)" || KindPush.String() != "push" {
		t.Error(Kind(200).String())
	}
	if e := (&Error{Msg: "NOAUTH x"}); e.Code() != "NOAUTH" || e.Error() != "redis: NOAUTH x" {
		t.Error(e)
	}
}

// TestHandshakeErrors fails Dial on each refusal, and on a server not there.
func TestHandshakeErrors(t *testing.T) {
	for _, tc := range []struct {
		refuse string
		opts   Options
	}{
		{"HELLO", Options{Protocol: 3}},
		{"AUTH", Options{Protocol: 2, Password: "p"}},
		{"CLIENT", Options{Protocol: 2, ClientName: "n"}},
		{"SELECT", Options{DB: 1}},
	} {
		f := newFake(t, nil, func(args []string) string {
			if args[0] == tc.refuse {
				return "-ERR unknown command 'X'\r\n"
			}
			if args[0] == "HELLO" {
				return "%0\r\n"
			}
			return "+OK\r\n"
		})
		o := tc.opts
		o.Addr = f.addr()
		if _, err := Dial(context.Background(), o); err == nil || !strings.Contains(err.Error(), tc.refuse) {
			t.Errorf("%s: %v", tc.refuse, err)
		}
	}
	// A server that closes the connection at once.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Dial(context.Background(), Options{Addr: ln.Addr().String(), DialTimeout: time.Second}); err == nil {
		t.Error("a closed connection")
	}
	ln.Close()
	if _, err := Dial(context.Background(), Options{Addr: ln.Addr().String()}); err == nil {
		t.Error("no server")
	}
}

// TestDoneContext sends nothing once ctx has ended; a write to a closed
// connection breaks it.
func TestDoneContext(t *testing.T) {
	f := newFake(t, nil, func(args []string) string { return "%0\r\n" })
	c := dial(t, Options{Addr: f.addr()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Do(ctx, "PING"); !errors.Is(err, context.Canceled) || c.Broken() != nil {
		t.Fatalf("%v %v", err, c.Broken())
	}
	c.nc.Close()
	if _, err := c.Do(context.Background(), "PING"); err == nil || c.Broken() == nil {
		t.Fatalf("%v", err)
	}
}
