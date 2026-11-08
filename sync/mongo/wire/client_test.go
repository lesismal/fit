package mongowire

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hang bounds a test that would otherwise hang; it is not a limit on how
// long anything takes.
func hang(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func newClient(t *testing.T, o Options) *Client {
	t.Helper()
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestRunAndAuth(t *testing.T) {
	ctx := hang(t)
	f := newFake(t)
	f.set(func() {
		f.user, f.pass = "fit,=user", "pencil"
		f.handle = func(name string, cmd Raw) any {
			if name != "ping" {
				return D{{"ok", 0.0}, {"code", int32(59)}, {"codeName", "CommandNotFound"}, {"errmsg", "no such command"}, {"errorLabels", A{"Lbl"}}}
			}
			db, _ := cmd.Lookup("$db")
			s, _ := db.StringValue()
			return D{{"db", s}}
		}
	})
	c := newClient(t, Options{Hosts: []string{f.addr}, Username: "fit,=user", Password: "pencil", AppName: strings.Repeat("a", 200)})
	for range 3 {
		r, err := c.Run(ctx, "shop", D{{"ping", int32(1)}})
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := r.Lookup("db"); v.Data == nil || !strings.Contains(r.String(), `"db":"shop"`) {
			t.Fatalf("answer %s", r)
		}
	}
	// One connection, authenticated once.
	if n := strings.Count(strings.Join(f.commands(), " "), "saslStart"); n != 1 {
		t.Errorf("%d authentications: %v", n, f.commands())
	}
	_, err := c.Run(ctx, "shop", D{{"nope", int32(1)}})
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != 59 || !ce.HasLabel("Lbl") || ce.HasLabel("x") || !strings.Contains(ce.Error(), "CommandNotFound") {
		t.Fatalf("error %v", err)
	}
	if (&CommandError{Code: 2, Message: "m"}).Error() != "mongowire: m (2)" {
		t.Error("an error without a code name")
	}
	if err := commandError(mustMarshal(t, D{{"ok", 0}})); err.(*CommandError).Message != "command failed" {
		t.Errorf("no message: %v", err)
	}
	if o := c.Options(); o.AuthSource != "admin" || o.MaxConns != 4 {
		t.Errorf("defaults %+v", o)
	}

	// A wrong password, or an unknown user, fails at once.
	for _, o := range []Options{{Username: "fit,=user", Password: "wrong"}, {Username: "other", Password: "pencil"}} {
		o.Hosts = []string{f.addr}
		c2 := newClient(t, o)
		if _, err := c2.Run(ctx, "x", D{{"ping", 1}}); !errors.As(err, &ce) || ce.Code != 18 {
			t.Errorf("%s: %v", o.Password, err)
		}
	}

	// Dial alone.
	cn, err := Dial(ctx, f.addr, Options{Username: "fit,=user", Password: "pencil"})
	if err != nil {
		t.Fatal(err)
	}
	if cn.Addr() != f.addr || !cn.Hello().IsWritablePrimary || cn.Err() != nil {
		t.Error("dialled conn")
	}
	cn.Close()
	if _, err := cn.Run(ctx, "x", D{{"ping", 1}}); !IsNetError(err) || !errors.Is(err, net.ErrClosed) {
		t.Errorf("closed: %v", err)
	}
	if _, err := Dial(ctx, f.addr, Options{Username: "fit,=user", Password: "bad"}); err == nil {
		t.Error("bad password dialled")
	}
	c.Close()
	if _, err := c.Run(ctx, "x", D{{"ping", 1}}); !errors.Is(err, ErrClosed) {
		t.Errorf("closed client: %v", err)
	}
}

func TestSCRAMVector(t *testing.T) {
	// RFC 7677's example.
	s := &scram{user: "user", password: "pencil", nonce: "rOprNGfwEbeRWgbNEkqO"}
	if got := s.first(); got != "n,,n=user,r=rOprNGfwEbeRWgbNEkqO" {
		t.Fatal(got)
	}
	final, err := s.final("r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")
	if err != nil {
		t.Fatal(err)
	}
	if final != "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ=" {
		t.Fatal(final)
	}
	if err := s.verify("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="); err != nil {
		t.Fatal(err)
	}
	if err := s.verify("v=AAAA"); err == nil {
		t.Error("a wrong signature verified")
	}
	if err := s.verify("e=invalid-proof"); err == nil {
		t.Error("an error verified")
	}
	for _, bad := range []string{
		"e=unknown-user",
		"r=other,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
		"r=rOprNGfwEbeRWgbNEkqO,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
		"r=rOprNGfwEbeRWgbNEkqOx,s=!!,i=4096",
		"r=rOprNGfwEbeRWgbNEkqOx,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=10",
	} {
		if _, err := s.final(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
	// The cache gives the same key, and keeps a few.
	var sc saltCache
	a, _ := sc.salted("p", []byte("s"), 4096)
	b, _ := sc.salted("p", []byte("s"), 4096)
	if string(a) != string(b) {
		t.Error("cache")
	}
	for i := range 10 {
		_, _ = sc.salted("p", []byte{byte(i)}, 4096)
	}
	if len(sc.m) > 8 {
		t.Errorf("cache of %d", len(sc.m))
	}
}

func TestSCRAMServerErrors(t *testing.T) {
	ctx := hang(t)
	for _, mode := range []string{"badfirst", "badsig", "neverdone", "continuefail", "lastfail", "sha1"} {
		t.Run(mode, func(t *testing.T) {
			f := newFake(t)
			f.set(func() {
				f.user, f.pass, f.scramMode = "u", "p", mode
				if mode == "sha1" {
					f.hello = func() D { return D{{"isWritablePrimary", true}, {"saslSupportedMechs", A{"SCRAM-SHA-1"}}} }
				}
			})
			if _, err := Dial(ctx, f.addr, Options{Username: "u", Password: "p"}); err == nil {
				t.Fatal("authenticated")
			}
		})
	}
}

func TestDiscoveryAndFailover(t *testing.T) {
	ctx := hang(t)
	a, b := newFake(t), newFake(t)
	var primary atomic.Value
	primary.Store(b.addr)
	helloOf := func(self string) func() D {
		return func() D {
			p := primary.Load().(string)
			return D{{"isWritablePrimary", p == self}, {"secondary", p != self}, {"setName", "rs"},
				{"primary", p}, {"me", self}, {"hosts", A{a.addr, b.addr}}, {"passives", A{}}}
		}
	}
	answer := func(self string) func(string, Raw) any {
		return func(name string, _ Raw) any {
			if primary.Load().(string) != self {
				return D{{"ok", 0}, {"code", int32(10107)}, {"codeName", "NotWritablePrimary"}, {"errmsg", "not primary"}}
			}
			return D{{"me", self}}
		}
	}
	a.set(func() { a.hello, a.handle = helloOf(a.addr), answer(a.addr) })
	b.set(func() { b.hello, b.handle = helloOf(b.addr), answer(b.addr) })

	// Only a is a seed: it names b the primary.
	c := newClient(t, Options{Hosts: []string{a.addr}, ReplicaSet: "rs"})
	if p, err := c.Primary(ctx); err != nil || p != b.addr {
		t.Fatalf("primary %q %v", p, err)
	}
	r, err := c.Run(ctx, "admin", D{{"ping", 1}})
	if err != nil || !strings.Contains(r.String(), b.addr) {
		t.Fatalf("%s %v", r, err)
	}
	// b steps down: the command it refuses is sent again to a.
	primary.Store(a.addr)
	r, err = c.Run(ctx, "admin", D{{"ping", 1}})
	if err != nil || !strings.Contains(r.String(), a.addr) {
		t.Fatalf("after failover: %s %v", r, err)
	}
	if p, _ := c.Primary(ctx); p != a.addr {
		t.Fatalf("primary %s", p)
	}
	// a's connections are cut: the next command finds a again.
	a.close()
	a2 := newFakeAt(t, a.addr)
	a2.set(func() { a2.hello, a2.handle = helloOf(a.addr), answer(a.addr) })
	if r, err = c.Run(ctx, "admin", D{{"ping", 1}}); err != nil || !strings.Contains(r.String(), a.addr) {
		t.Fatalf("after a cut: %s %v", r, err)
	}
	// While no member is primary, finding one waits, then gives up.
	primary.Store("nobody:1")
	c2 := newClient(t, Options{Hosts: []string{a.addr, b.addr}, ReplicaSet: "rs", ServerSelectionTimeout: 300 * time.Millisecond})
	if _, err := c2.Run(ctx, "admin", D{{"ping", 1}}); err == nil || !strings.Contains(err.Error(), "no primary found") {
		t.Fatalf("no primary: %v", err)
	}
	// A server of another set is not taken.
	primary.Store(b.addr)
	c3 := newClient(t, Options{Hosts: []string{b.addr}, ReplicaSet: "other", ServerSelectionTimeout: 200 * time.Millisecond})
	if _, err := c3.Primary(ctx); err == nil || !strings.Contains(err.Error(), `of replica set "rs"`) {
		t.Fatalf("other set: %v", err)
	}
	// The context's end ends the search.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c3.Primary(cctx); err == nil {
		t.Fatal("canceled search")
	}
}

// newFakeAt is a fakeMongo on addr, which a closed one held.
func newFakeAt(t *testing.T, addr string) *fakeMongo {
	var ln net.Listener
	var err error
	for range 100 {
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	return newFakeOn(t, ln)
}

func TestUnreachable(t *testing.T) {
	ctx := hang(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c := newClient(t, Options{Hosts: []string{addr}, ServerSelectionTimeout: 200 * time.Millisecond})
	if _, err := c.Run(ctx, "x", D{{"ping", 1}}); err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("unreachable: %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Error("no hosts")
	}
	if _, err := Dial(ctx, addr, Options{}); !IsNetError(err) {
		t.Errorf("dial: %v", err)
	}
	// An old server is refused.
	f := newFake(t)
	f.set(func() { f.hello = func() D { return D{{"isWritablePrimary", true}, {"maxWireVersion", int32(6)}} } })
	if _, err := Dial(ctx, f.addr, Options{}); err == nil || !strings.Contains(err.Error(), "wire version") {
		t.Errorf("old server: %v", err)
	}
	// A cached primary that is gone, then refuses hello, is found again.
	g := newFake(t)
	c2 := newClient(t, Options{Hosts: []string{g.addr}, ServerSelectionTimeout: 300 * time.Millisecond})
	if _, err := c2.Primary(ctx); err != nil {
		t.Fatal(err)
	}
	c2.Close()
	c2.closed = false
	g.set(func() { g.hello = func() D { return D{{"isWritablePrimary", false}} } })
	if _, err := c2.Primary(ctx); err == nil {
		t.Fatal("a secondary taken")
	}
	g.set(func() { g.hello = func() D { return D{{"isWritablePrimary", true}} }; g.user, g.pass = "u", "p" })
	c3 := newClient(t, Options{Hosts: []string{g.addr}, Username: "u", Password: "p"})
	if _, err := c3.Primary(ctx); err != nil {
		t.Fatal(err)
	}
	c3.Close()
	c3.closed = false
	c3.o.Password = "q"
	if _, err := c3.Primary(ctx); err == nil {
		t.Fatal("a wrong password on a cached primary")
	}
	c3.lost(c3.gen)
	if _, err := c3.Primary(ctx); err == nil {
		t.Fatal("a wrong password on a found primary")
	}
}

func TestTimeoutsAndBadAnswers(t *testing.T) {
	ctx := hang(t)
	f := newFake(t)
	var mode atomic.Value
	mode.Store("")
	f.set(func() {
		f.handle = func(name string, cmd Raw) any {
			switch mode.Load().(string) {
			case "hang":
				return noAnswer{}
			case "close":
				return closeConn{}
			case "huge":
				b := binary.LittleEndian.AppendUint32(nil, 100<<20)
				return rawAnswer{append(b, make([]byte, 12)...)}
			case "wrongid":
				m := appendMsg(nil, 0, mustMarshal(t, D{{"ok", 1}}), nil)
				binary.LittleEndian.PutUint32(m[8:], 999999)
				return rawAnswer{m}
			case "opcode":
				m := appendMsg(nil, 0, mustMarshal(t, D{{"ok", 1}}), nil)
				binary.LittleEndian.PutUint32(m[12:], 1)
				return rawAnswer{m}
			case "malformed":
				m := appendMsg(nil, 0, mustMarshal(t, D{{"ok", 1}}), nil)
				m[headerSize+4] = 9
				return rawAnswer{m}
			case "moretocome":
				m := appendMsg(nil, 0, mustMarshal(t, D{{"ok", 1}}), nil)
				m[headerSize] = flagMoreToCome
				return rawAnswer{m}
			}
			return D{}
		}
	})
	c := newClient(t, Options{Hosts: []string{f.addr}, Timeout: 150 * time.Millisecond})
	if _, err := c.Run(ctx, "x", D{{"ping", 1}}); err != nil {
		t.Fatal(err)
	}
	// No answer: the connection's timeout, then the context's deadline,
	// end the wait; both close the connection, and the next works.
	mode.Store("hang")
	if _, err := c.Run(context.Background(), "x", D{{"ping", 1}}); !IsNetError(err) {
		t.Fatalf("hang: %v", err)
	}
	c.o.Timeout = time.Hour
	// A connection is made first, so that the deadline cuts the round
	// trip; one that cuts the dial or the hello is the same error (below).
	mode.Store("")
	if _, err := c.Run(ctx, "x", D{{"ping", 1}}); err != nil {
		t.Fatal(err)
	}
	mode.Store("hang")
	dctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err := c.Run(dctx, "x", D{{"ping", 1}})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	mode.Store("")
	if _, err := c.Run(ctx, "x", D{{"ping", 1}}); err != nil {
		t.Fatalf("after: %v", err)
	}
	cctx, cancel2 := context.WithCancel(ctx)
	cancel2()
	if _, err := c.Run(cctx, "x", D{{"ping", 1}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	for _, m := range []string{"close", "huge", "wrongid", "opcode", "malformed", "moretocome"} {
		mode.Store(m)
		if _, err := c.Run(ctx, "x", D{{"ping", 1}}); !IsNetError(err) {
			t.Errorf("%s: %v", m, err)
		}
	}
	mode.Store("")
	// A command larger than a message may be is not sent.
	c.o.MaxMessageBytes = 1000
	c.lost(c.gen)
	if _, err := c.Run(ctx, "x", D{{"ping", 1}, {"pad", strings.Repeat("x", 2000)}}); err == nil || IsNetError(err) {
		t.Errorf("large: %v", err)
	}
	if _, err := c.Run(ctx, "x", D{{"ping", struct{}{}}}); err == nil {
		t.Error("an unwritable command sent")
	}
}

// TestDeadlineWhileFinding checks that a context's end while the primary
// is looked for (a dial, a hello that is never answered) is the context's
// error, whichever timer saw it first.
func TestDeadlineWhileFinding(t *testing.T) {
	ctx := hang(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // accepts, and never answers
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	c := newClient(t, Options{Hosts: []string{ln.Addr().String()}})
	dctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = c.Run(dctx, "x", D{{"ping", 1}})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	// The selection timeout's end is not the caller's.
	c2 := newClient(t, Options{Hosts: []string{ln.Addr().String()}, ConnectTimeout: 50 * time.Millisecond, ServerSelectionTimeout: 200 * time.Millisecond})
	_, err = c2.Run(ctx, "x", D{{"ping", 1}})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no primary found (within") {
		t.Fatalf("selection timeout: %v", err)
	}
	if err := ctxErr(ctx, errors.New("x")); err.Error() != "x" {
		t.Error(err)
	}
}

func TestCursor(t *testing.T) {
	ctx := hang(t)
	f := newFake(t)
	docs := func(from, n int) A {
		var a A
		for i := range n {
			a = append(a, D{{"_id", int64(from + i)}})
		}
		return a
	}
	var gm atomic.Int32
	var killed atomic.Int32
	f.set(func() {
		f.handle = func(name string, cmd Raw) any {
			switch name {
			case "find":
				gm.Store(0)
				return D{{"cursor", D{{"id", int64(7)}, {"ns", "db.coll"}, {"firstBatch", docs(0, 2)}}}}
			case "getMore":
				coll, _ := cmd.Lookup("collection")
				if s, _ := coll.StringValue(); s != "coll" {
					return D{{"ok", 0}, {"errmsg", "coll " + s}}
				}
				switch gm.Add(1) {
				case 1:
					return D{{"cursor", D{{"id", int64(7)}, {"ns", "db.coll"}, {"nextBatch", A{}}, {"postBatchResumeToken", D{{"_data", "x"}}}}}}
				case 2:
					return D{{"cursor", D{{"id", int64(7)}, {"ns", "db.coll"}, {"nextBatch", docs(2, 2)}}}}
				}
				return D{{"cursor", D{{"id", int64(0)}, {"ns", "db.coll"}, {"nextBatch", docs(4, 1)}}}}
			case "killCursors":
				killed.Add(1)
				return D{}
			case "bad":
				return D{{"cursor", 1}}
			case "noid":
				return D{{"cursor", D{{"firstBatch", A{}}}}}
			case "nondoc":
				return D{{"cursor", D{{"id", int64(0)}, {"firstBatch", A{1}}}}}
			case "fail":
				return D{{"ok", 0}, {"errmsg", "no"}}
			case "badmore":
				return D{{"cursor", D{{"id", int64(3)}, {"ns", "db.c"}, {"firstBatch", A{}}}}}
			}
			return D{}
		}
	})
	c := newClient(t, Options{Hosts: []string{f.addr}, MaxConns: 1})
	cur, err := c.Cursor(ctx, "db", D{{"find", "coll"}}, CursorOptions{BatchSize: 2, MaxAwait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if cur.ID() != 7 || len(cur.Batch()) != 2 || cur.PostBatchResumeToken() != nil {
		t.Fatal("first batch")
	}
	var ids []int64
	for {
		d, err := cur.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			break
		}
		v, _ := d.Lookup("_id")
		n, _ := v.Int()
		ids = append(ids, n)
		if n == 2 && cur.PostBatchResumeToken() == nil {
			t.Error("no resume token")
		}
	}
	if !slices.Equal(ids, []int64{0, 1, 2, 3, 4}) {
		t.Fatalf("ids %v", ids)
	}
	if more, err := cur.NextBatch(ctx); more || err != nil {
		t.Fatal("past the end")
	}
	if err := cur.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// The cursor gave its connection back: with MaxConns 1, this runs.
	cur, err = c.Cursor(ctx, "db", D{{"find", "coll"}}, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.Close(ctx); err != nil || killed.Load() != 1 {
		t.Fatalf("close: %v %d", err, killed.Load())
	}
	if more, err := cur.NextBatch(ctx); more || err != nil {
		t.Error("closed cursor read")
	}
	if cur.Close(ctx) != nil {
		t.Error("closed twice")
	}
	for _, name := range []string{"bad", "noid", "nondoc", "fail"} {
		if _, err := c.Cursor(ctx, "db", D{{name, "coll"}}, CursorOptions{}); err == nil {
			t.Errorf("%s: a cursor", name)
		}
	}
	// A getMore that fails, or whose answer cannot be read, ends it.
	cur, err = c.Cursor(ctx, "db", D{{"badmore", "c"}}, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cur.NextBatch(ctx); err == nil {
		t.Error("getMore of a wrong collection")
	}
	f.set(func() {
		h := f.handle
		f.handle = func(name string, cmd Raw) any {
			if name == "getMore" {
				return D{{"cursor", "x"}}
			}
			return h(name, cmd)
		}
	})
	cur, _ = c.Cursor(ctx, "db", D{{"find", "coll"}}, CursorOptions{})
	if _, err := cur.NextBatch(ctx); err == nil {
		t.Error("bad getMore answer")
	}
	// A cursor command refused by a primary that stepped down is sent again.
	var refused atomic.Bool
	f.set(func() {
		h := f.handle
		f.handle = func(name string, cmd Raw) any {
			if name == "find" && refused.CompareAndSwap(false, true) {
				return D{{"ok", 0}, {"code", int32(10107)}, {"errmsg", "not primary"}}
			}
			return h(name, cmd)
		}
	})
	if cur, err = c.Cursor(ctx, "db", D{{"find", "coll"}}, CursorOptions{}); err != nil {
		t.Fatal(err)
	}
	cur.Close(ctx)
	// Waiting for a connection ends with the context.
	cur, _ = c.Cursor(ctx, "db", D{{"find", "coll"}}, CursorOptions{})
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Cursor(cctx, "db", D{{"find", "coll"}}, CursorOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting: %v", err)
	}
	cur.Close(ctx)
}

func TestMsg(t *testing.T) {
	body := mustMarshal(t, D{{"insert", "c"}})
	docs := []Raw{mustMarshal(t, D{{"a", 1}}), mustMarshal(t, D{{"b", 2}})}
	m := appendMsg(nil, 5, body, []Sequence{{ID: "documents", Docs: docs}})
	if len(m) != msgSize(body, []Sequence{{ID: "documents", Docs: docs}}) {
		t.Fatal("size")
	}
	_, b, seqs, err := parseMsg(m)
	if err != nil || string(b) != string(body) || len(seqs) != 1 || seqs[0].ID != "documents" || len(seqs[0].Docs) != 2 {
		t.Fatalf("%v %v", seqs, err)
	}
	// With a checksum.
	cs := append([]byte(nil), m...)
	cs[headerSize] |= flagChecksum
	cs = binary.LittleEndian.AppendUint32(cs, crc32.Checksum(cs, castagnoli))
	if _, _, _, err := parseMsg(cs); err != nil {
		t.Fatal(err)
	}
	cs[len(cs)-1] ^= 1
	if _, _, _, err := parseMsg(cs); err == nil {
		t.Error("bad checksum")
	}
	bad := func(name string, mod func([]byte) []byte) {
		x := mod(append([]byte(nil), m...))
		if _, _, _, err := parseMsg(x); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	bad("short", func(b []byte) []byte { return b[:headerSize+3] })
	bad("flags", func(b []byte) []byte { b[headerSize] = 4; return b })
	bad("checksum cut", func(b []byte) []byte { return append(b[:headerSize], flagChecksum, 0, 0, 0, 0) })
	bad("two bodies", func(b []byte) []byte { return append(append(b, 0), body...) })
	bad("body", func(b []byte) []byte { return append(b[:headerSize+5], 3, 0, 0, 0) })
	bad("no body", func(b []byte) []byte { return append(b[:headerSize+4], b[headerSize+5+len(body):]...) })
	bad("seq len cut", func(b []byte) []byte { return append(b[:headerSize+5+len(body)], 1, 0) })
	bad("seq len", func(b []byte) []byte { return append(b[:headerSize+5+len(body)], 1, 99, 0, 0, 0) })
	bad("seq name", func(b []byte) []byte { return append(b[:headerSize+5+len(body)], 1, 6, 0, 0, 0, 'x', 'y') })
	bad("seq doc", func(b []byte) []byte { return append(b[:headerSize+5+len(body)], 1, 8, 0, 0, 0, 'x', 0, 9) })
}

func TestURI(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	certPEM, keyPEM := testCert(t)
	os.WriteFile(ca, certPEM, 0o600)
	pair := filepath.Join(dir, "pair.pem")
	os.WriteFile(pair, append(append([]byte(nil), certPEM...), keyPEM...), 0o600)
	o, err := ParseURI("mongodb://us%40r:p%3Ass@a,b:27018,[::1]/mydb?replicaSet=rs&authMechanism=SCRAM-SHA-256&tlsCAFile=" + ca +
		"&tlsCertificateKeyFile=" + pair + "&connectTimeoutMS=1500&socketTimeoutMS=2000&serverSelectionTimeoutMS=3000&maxPoolSize=7&appName=fit")
	if err != nil {
		t.Fatal(err)
	}
	if o.Username != "us@r" || o.Password != "p:ss" || !slices.Equal(o.Hosts, []string{"a:27017", "b:27018", "[::1]:27017"}) ||
		o.AuthSource != "mydb" || o.ReplicaSet != "rs" || o.ConnectTimeout != 1500*time.Millisecond || o.Timeout != 2*time.Second ||
		o.ServerSelectionTimeout != 3*time.Second || o.MaxConns != 7 || o.AppName != "fit" || o.TLS == nil || o.TLS.RootCAs == nil || len(o.TLS.Certificates) != 1 {
		t.Fatalf("%+v", o)
	}
	o, err = ParseURI("mongodb://h/?ssl=true&authSource=other")
	if err != nil || o.TLS == nil || o.AuthSource != "other" || o.TLS.MinVersion != tls.VersionTLS12 {
		t.Fatalf("%+v %v", o, err)
	}
	if withPort("::1") != "[::1]:27017" {
		t.Error(withPort("::1"))
	}
	for _, bad := range []string{
		"mongodb+srv://h", "mongodb://a,,b", "mongodb://h/?tlsInsecure=true", "mongodb://h/?authMechanism=SCRAM-SHA-1",
		"mongodb://h/?connectTimeoutMS=x", "mongodb://h/?tlsCAFile=/nonexistent", "mongodb://h/?tlsCAFile=" + pair + "x",
		"mongodb://h/?tlsCertificateKeyFile=" + ca, "mongodb://%zz@h", "mongodb://u:%zz@h", "mongodb://h/%zz", "mongodb://h/?a=%zz",
	} {
		if _, err := ParseURI(bad); err == nil {
			t.Errorf("%s: read", bad)
		}
	}
	empty := filepath.Join(dir, "empty.pem")
	os.WriteFile(empty, []byte("nothing"), 0o600)
	if _, err := ParseURI("mongodb://h/?tlsCAFile=" + empty); err == nil {
		t.Error("a CA file without certificates")
	}
}

func TestTLS(t *testing.T) {
	ctx := hang(t)
	certPEM, keyPEM := testCert(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeOn(t, ln)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0o600)
	o, err := ParseURI("mongodb://" + f.addr + "/?tlsCAFile=" + filepath.Join(dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cn, err := Dial(ctx, f.addr, o)
	if err != nil {
		t.Fatal(err)
	}
	cn.Close()
	// Without the CA, the server is not trusted.
	o.TLS.RootCAs = nil
	if _, err := Dial(ctx, f.addr, o); !IsNetError(err) {
		t.Fatalf("untrusted: %v", err)
	}
}
