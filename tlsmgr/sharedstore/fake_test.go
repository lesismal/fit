package sharedstore

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
)

// The tests here run each store against a fake of its server, which answers
// as the test says, errors and cut connections too, so that they need no
// Docker.

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// closedAddr is an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func writeFile(t *testing.T, name, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// serve accepts connections on ln and gives each to conn, until the test
// ends.
func serve(t *testing.T, ln net.Listener, conn func(net.Conn)) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	conns := map[net.Conn]struct{}{}
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[c] = struct{}{}
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				conn(c)
			}()
		}
	}()
}

// fakeRedis serves RESP on 127.0.0.1, over TLS with tc, answering each
// command with what handle returns, raw; "" cuts the connection.
func fakeRedis(t *testing.T, tc *tls.Config, handle func(args []string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tc != nil {
		ln = tls.NewListener(ln, tc)
	}
	serve(t, ln, func(c net.Conn) {
		br := bufio.NewReader(c)
		for {
			v, err := readReply(br, 0)
			if err != nil {
				return
			}
			a, _ := v.([]any)
			args := make([]string, len(a))
			for i, x := range a {
				args[i], _ = x.(string)
			}
			reply := handle(args)
			if reply == "" {
				return
			}
			if _, err := io.WriteString(c, reply); err != nil {
				return
			}
		}
	})
	return ln.Addr().String()
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func array(items ...string) string {
	return fmt.Sprintf("*%d\r\n", len(items)) + strings.Join(items, "")
}

func TestReadReplyMore(t *testing.T) {
	for _, c := range []struct {
		in   string
		want any
		err  string
	}{
		{"", nil, "EOF"},
		{":x\r\n", nil, "integer"},
		{"*-1\r\n", nil, ""},
		{"*2\r\n-ERR a\r\n:1\r\n", []any{redisError("ERR a"), int64(1)}, ""},
		{"*1\r\n$5\r\nab", nil, "EOF"},
	} {
		got, err := readReply(bufio.NewReader(strings.NewReader(c.in)), 0)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: %v, want an error with %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %#v %v", c.in, got, err)
		}
	}
}

func TestRedisTLSConfig(t *testing.T) {
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	notPEM := writeFile(t, "ca.pem", "not a certificate")
	for name, c := range map[string]*config.RedisStorage{
		"no CA file": {TLS: true, CAFile: filepath.Join(dir, "none.pem")},
		"CA not PEM": {TLS: true, CAFile: notPEM},
		"no key":     {TLS: true, CertFile: cert.CertFile, KeyFile: filepath.Join(dir, "none.key")},
	} {
		if _, err := newRedis(c, time.Second); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// New names the kind of a store that cannot be made.
	if _, err := New(&config.SharedStorage{Redis: &config.RedisStorage{TLS: true, CAFile: notPEM}}, nil); err == nil || !strings.Contains(err.Error(), "shared_storage.redis") {
		t.Errorf("New with a bad CA: %v", err)
	}
}

// TestRedisTLS talks to a fake Redis over TLS with a client certificate,
// authenticating as a user and selecting a database.
func TestRedisTLS(t *testing.T) {
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	kp, err := tls.LoadX509KeyPair(cert.CertFile, cert.KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	kv := map[string]string{}
	var seen []string
	addr := fakeRedis(t, &tls.Config{Certificates: []tls.Certificate{kp}}, func(args []string) string {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, strings.Join(args, " "))
		switch args[0] {
		case "AUTH", "SELECT":
			return "+OK\r\n"
		case "SET":
			kv[args[1]] = args[2]
			return "+OK\r\n"
		case "GET":
			if v, ok := kv[args[1]]; ok {
				return bulk(v)
			}
			return "$-1\r\n"
		}
		return "-ERR unknown\r\n"
	})
	pw := writeFile(t, "password", "pw\r\n")
	s, err := New(&config.SharedStorage{Redis: &config.RedisStorage{
		Address: addr, TLS: true, CAFile: cert.CertFile, CertFile: cert.CertFile, KeyFile: cert.KeyFile,
		Username: "u", PasswordFile: pw, DB: 3,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := testCtx(t)
	if err := s.Store(ctx, "a/b", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Load(ctx, "a/b"); err != nil || string(v) != "v" {
		t.Fatalf("Load: %q %v", v, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != "AUTH u pw" || seen[1] != "SELECT 3" {
		t.Fatalf("commands: %q", seen)
	}
}

func TestRedisDialErrors(t *testing.T) {
	ctx := testCtx(t)
	ok := func([]string) string { return "+OK\r\n" }
	refuse := func(cmd string) func([]string) string {
		return func(args []string) string {
			if args[0] == cmd {
				return "-ERR no\r\n"
			}
			return "+OK\r\n"
		}
	}
	// A server that cuts each connection as soon as it is made.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serve(t, ln, func(net.Conn) {})
	cut := ln.Addr().String()
	pw := writeFile(t, "password", "pw\n")
	for name, c := range map[string]struct {
		opts config.RedisStorage
		want string
	}{
		"nothing listens": {config.RedisStorage{Address: closedAddr(t)}, ""},
		"TLS handshake":   {config.RedisStorage{Address: cut, TLS: true}, ""},
		"no password":     {config.RedisStorage{Address: fakeRedis(t, nil, ok), PasswordFile: filepath.Join(t.TempDir(), "none")}, ""},
		"AUTH refused":    {config.RedisStorage{Address: fakeRedis(t, nil, refuse("AUTH")), PasswordFile: pw}, "AUTH"},
		"SELECT refused":  {config.RedisStorage{Address: fakeRedis(t, nil, refuse("SELECT")), DB: 1}, "SELECT"},
	} {
		r, err := newRedis(&c.opts, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.get(ctx, "k")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
		r.close()
	}
}

func TestRedisReplies(t *testing.T) {
	ctx := testCtx(t)
	var mu sync.Mutex
	replies := map[string]string{}
	answer := func(cmd, reply string) {
		mu.Lock()
		replies[cmd] = reply
		mu.Unlock()
	}
	addr := fakeRedis(t, nil, func(args []string) string {
		mu.Lock()
		defer mu.Unlock()
		if args[0] == "EVAL" {
			return replies[args[0]+" "+args[3]]
		}
		return replies[args[0]]
	})
	r, err := newRedis(&config.RedisStorage{Address: addr}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()

	// A command without a deadline, and an error Redis answers, which
	// keeps the connection.
	answer("PING", "+PONG\r\n")
	if v, err := r.do(context.Background(), "PING"); err != nil || v != "PONG" {
		t.Fatalf("PING: %v %v", v, err)
	}
	answer("GET", "-ERR wrong\r\n")
	if _, err := r.get(ctx, "k"); err == nil || err.Error() != "ERR wrong" {
		t.Fatalf("GET answered an error: %v", err)
	}
	if len(r.idle) != 1 {
		t.Fatalf("%d idle connections after an error answered", len(r.idle))
	}
	answer("GET", ":1\r\n")
	if _, err := r.get(ctx, "k"); err == nil || !strings.Contains(err.Error(), "int64") {
		t.Fatalf("GET answered an integer: %v", err)
	}
	// A cut connection is not kept.
	answer("GET", "")
	if _, err := r.get(ctx, "k"); err == nil {
		t.Fatal("GET on a cut connection")
	}
	if len(r.idle) != 0 {
		t.Fatalf("%d idle connections after one was cut", len(r.idle))
	}

	// SCAN: errors, a reply of another shape, pages, and too many keys.
	answer("SCAN", "-ERR scan\r\n")
	if _, err := r.list(ctx, "p/"); err == nil {
		t.Error("SCAN answered an error")
	}
	answer("SCAN", "+OK\r\n")
	if _, err := r.list(ctx, "p/"); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Errorf("SCAN answered no array: %v", err)
	}
	addr2 := fakeRedis(t, nil, func(args []string) string {
		if args[1] == "0" {
			return array(bulk("7"), array(bulk("p/a"), ":1\r\n"))
		}
		return array(bulk("0"), array(bulk("p/b")))
	})
	r2, err := newRedis(&config.RedisStorage{Address: addr2}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.close()
	if keys, err := r2.list(ctx, "p/"); err != nil || !reflect.DeepEqual(keys, []string{"p/a", "p/b"}) {
		t.Errorf("SCAN of two pages: %q %v", keys, err)
	}
	items := make([]string, maxKeys/2+1)
	for i := range items {
		items[i] = bulk("k")
	}
	page := array(bulk("1"), array(items...))
	addr3 := fakeRedis(t, nil, func([]string) string { return page })
	r3, err := newRedis(&config.RedisStorage{Address: addr3}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer r3.close()
	if _, err := r3.list(ctx, "p/"); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("SCAN of too many keys: %v", err)
	}

	// Locks: an error, held by another, taken, renewed, lost and given
	// back.
	answer("SET", "-ERR set\r\n")
	if _, _, err := r.tryLock(ctx, "l", time.Second); err == nil {
		t.Error("tryLock with SET answering an error")
	}
	answer("SET", "$-1\r\n")
	if _, ok, err := r.tryLock(ctx, "l", time.Second); ok || err != nil {
		t.Errorf("tryLock of a lock held: %v %v", ok, err)
	}
	answer("SET", "+OK\r\n")
	l, ok, err := r.tryLock(ctx, "l", time.Second)
	if !ok || err != nil {
		t.Fatalf("tryLock: %v %v", ok, err)
	}
	answer("EVAL l", ":1\r\n")
	if err := l.renew(ctx); err != nil {
		t.Errorf("renew: %v", err)
	}
	answer("EVAL l", ":0\r\n")
	if err := l.renew(ctx); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Errorf("renew of a lock lost: %v", err)
	}
	answer("EVAL l", "-ERR eval\r\n")
	if err := l.renew(ctx); err == nil {
		t.Error("renew with EVAL answering an error")
	}
	answer("EVAL l", ":1\r\n")
	if err := l.release(ctx); err != nil {
		t.Errorf("release: %v", err)
	}
}

func TestRedisConnFaults(t *testing.T) {
	r, err := newRedis(&config.RedisStorage{Address: closedAddr(t)}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// A connection that cannot be written to is closed, not kept.
	a, b := net.Pipe()
	b.Close()
	a.Close()
	r.idle = []*redisConn{{c: a, r: bufio.NewReader(a)}}
	if _, err := r.do(testCtx(t), "PING"); err == nil {
		t.Fatal("a command on a closed connection")
	}
	if len(r.idle) != 0 || len(r.slots) != 0 {
		t.Fatalf("%d idle, %d slots taken", len(r.idle), len(r.slots))
	}
	// Every connection busy: a command waits for one within its context.
	for range cap(r.slots) {
		r.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.do(ctx, "PING"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a command with every connection busy: %v", err)
	}
}

// fakeHTTP serves h on 127.0.0.1.
func fakeHTTP(t *testing.T, h http.HandlerFunc) string {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

// replies answers each path with the status and body set for it, 404
// otherwise.
type replies struct {
	mu   sync.Mutex
	m    map[string]func(w http.ResponseWriter, r *http.Request)
	seen []string
}

func (rs *replies) set(path string, status int, body string) {
	rs.setFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
}

func (rs *replies) setFunc(path string, f func(w http.ResponseWriter, r *http.Request)) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.m == nil {
		rs.m = map[string]func(w http.ResponseWriter, r *http.Request){}
	}
	rs.m[path] = f
}

func (rs *replies) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	rs.seen = append(rs.seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+r.Header.Get("X-Consul-Token"))
	f := rs.m[r.URL.Path]
	rs.mu.Unlock()
	if f == nil {
		http.NotFound(w, r)
		return
	}
	f(w, r)
}

func (rs *replies) last() string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.seen) == 0 {
		return ""
	}
	return rs.seen[len(rs.seen)-1]
}

// cutAnswer answers 200 with a body shorter than it says, then cuts the
// connection.
func cutAnswer(w http.ResponseWriter, _ *http.Request) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n")
	c.Close()
}

func newEtcdT(t *testing.T, c *config.EtcdStorage) *etcdBackend {
	t.Helper()
	e, err := newEtcd(c, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.close)
	return e
}

func TestEtcdEndpoints(t *testing.T) {
	ctx := testCtx(t)
	if _, err := newEtcd(&config.EtcdStorage{CAFile: filepath.Join(t.TempDir(), "none")}, time.Second); err == nil {
		t.Error("newEtcd with no CA file")
	}
	rs := &replies{}
	rs.set("/v3/kv/range", 200, `{"kvs":[{"value":"dg=="}]}`)
	url := fakeHTTP(t, rs.ServeHTTP)
	// The first endpoint cannot be reached; the second is used, now and
	// after.
	e := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{"http://" + closedAddr(t), url + "/"}})
	for range 2 {
		if v, err := e.get(ctx, "k"); err != nil || string(v) != "v" {
			t.Fatalf("get: %q %v", v, err)
		}
	}
	if e.next != 1 {
		t.Fatalf("next %d", e.next)
	}
	// An answer that is not 200 is not tried elsewhere.
	rs.set("/v3/kv/put", 500, "boom")
	err := e.put(ctx, "k", []byte("v"))
	var se *etcdStatusError
	if !errors.As(err, &se) || se.status != 500 || !strings.Contains(err.Error(), "boom") || e.next != 1 {
		t.Fatalf("put answered 500: %v, next %d", err, e.next)
	}
	// No endpoint can be reached.
	e2 := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{"http://" + closedAddr(t), "http://" + closedAddr(t)}})
	if err := e2.del(ctx, "k"); err == nil {
		t.Fatal("del with no endpoint reached")
	}
	// An endpoint that is not a URL.
	e3 := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{"http://a b"}})
	if err := e3.del(ctx, "k"); err == nil {
		t.Fatal("del with a bad endpoint")
	}
}

func TestEtcdAnswers(t *testing.T) {
	ctx := testCtx(t)
	rs := &replies{}
	e := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{fakeHTTP(t, rs.ServeHTTP)}})
	for name, c := range map[string]struct {
		h    func(w http.ResponseWriter, r *http.Request)
		want string
	}{
		"empty":     {func(http.ResponseWriter, *http.Request) {}, "empty answer"},
		"not JSON":  {func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "{\n") }, "POST /v3/kv/range"},
		"cut short": {cutAnswer, "POST /v3/kv/range"},
	} {
		rs.setFunc("/v3/kv/range", c.h)
		if _, err := e.get(ctx, "k"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	rs.set("/v3/kv/range", 200, `{}`)
	if _, err := e.get(ctx, "k"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("get of no key: %v", err)
	}

	// list
	rs.set("/v3/kv/range", 500, "")
	if _, err := e.list(ctx, "p/"); err == nil {
		t.Error("list answered 500")
	}
	rs.set("/v3/kv/range", 200, `{"kvs":[{"key":"cC9h"}],"more":true}`)
	if _, err := e.list(ctx, "p/"); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("list of too many keys: %v", err)
	}
	rs.set("/v3/kv/range", 200, `{"kvs":[{"key":"cC9h"}]}`)
	if keys, err := e.list(ctx, "p/"); err != nil || !reflect.DeepEqual(keys, []string{"p/a"}) {
		t.Errorf("list: %q %v", keys, err)
	}
	for in, want := range map[string]string{"a\xff": "b", "\xff\xff": "\x00", "ab": "ac"} {
		if got := string(rangeEnd([]byte(in))); got != want {
			t.Errorf("rangeEnd(%q) = %q, want %q", in, got, want)
		}
	}

	// Locks.
	rs.set("/v3/lease/grant", 500, "")
	if _, _, err := e.tryLock(ctx, "l", time.Second); err == nil {
		t.Error("tryLock with no grant")
	}
	rs.set("/v3/lease/grant", 200, `{"error":"too many leases"}`)
	if _, _, err := e.tryLock(ctx, "l", time.Second); err == nil || !strings.Contains(err.Error(), "too many leases") {
		t.Errorf("tryLock with no lease granted: %v", err)
	}
	rs.set("/v3/lease/grant", 200, `{"ID":"7"}`)
	var revoked atomic.Int32
	rs.setFunc("/v3/lease/revoke", func(w http.ResponseWriter, _ *http.Request) {
		revoked.Add(1)
		io.WriteString(w, "{}")
	})
	rs.set("/v3/kv/txn", 500, "")
	if _, _, err := e.tryLock(ctx, "l", time.Second); err == nil || revoked.Load() != 1 {
		t.Errorf("tryLock with the transaction failing: %v, %d revoked", err, revoked.Load())
	}
	rs.set("/v3/kv/txn", 200, `{"succeeded":false}`)
	if _, ok, err := e.tryLock(ctx, "l", time.Second); ok || err != nil || revoked.Load() != 2 {
		t.Errorf("tryLock of a lock held: %v %v, %d revoked", ok, err, revoked.Load())
	}
	rs.set("/v3/kv/txn", 200, `{"succeeded":true}`)
	l, ok, err := e.tryLock(ctx, "l", time.Second)
	if !ok || err != nil {
		t.Fatalf("tryLock: %v %v", ok, err)
	}
	rs.set("/v3/lease/keepalive", 500, "")
	if err := l.renew(ctx); err == nil {
		t.Error("renew answered 500")
	}
	rs.set("/v3/lease/keepalive", 200, `{"result":{"TTL":"0"}}`+"\n"+`{"result":{"TTL":"0"}}`)
	if err := l.renew(ctx); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Errorf("renew of a lease expired: %v", err)
	}
	rs.set("/v3/lease/keepalive", 200, `{"result":{"TTL":"3"}}`)
	if err := l.renew(ctx); err != nil {
		t.Errorf("renew: %v", err)
	}
	rs.set("/v3/lease/revoke", 404, `{"error":"etcdserver: requested lease not found"}`)
	if err := l.release(ctx); err != nil {
		t.Errorf("release of a lease expired: %v", err)
	}
	rs.set("/v3/lease/revoke", 500, "boom")
	if err := l.release(ctx); err == nil {
		t.Error("release answered 500")
	}
}

func TestEtcdAuth(t *testing.T) {
	ctx := testCtx(t)
	rs := &replies{}
	var tokens atomic.Int32
	rs.setFunc("/v3/auth/authenticate", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"password":"pw"`) {
			w.WriteHeader(401)
			io.WriteString(w, "authentication failed")
			return
		}
		fmt.Fprintf(w, `{"token":"T%d"}`, tokens.Add(1))
	})
	// The first token is refused as if it had expired.
	rs.setFunc("/v3/kv/put", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "T1" {
			w.WriteHeader(401)
			io.WriteString(w, "etcdserver: invalid auth token")
			return
		}
		io.WriteString(w, "{}")
	})
	url := fakeHTTP(t, rs.ServeHTTP)
	e := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{url}, Username: "u", PasswordFile: writeFile(t, "pw", "pw\n")})
	if err := e.put(ctx, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if last := rs.last(); !strings.HasSuffix(last, " T2") || e.token != "T2" {
		t.Fatalf("the last request %q, token %q", last, e.token)
	}
	// A password refused, and one that cannot be read.
	bad := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{url}, Username: "u", PasswordFile: writeFile(t, "pw", "wrong")})
	if err := bad.put(ctx, "k", nil); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("a wrong password: %v", err)
	}
	none := newEtcdT(t, &config.EtcdStorage{Endpoints: []string{url}, Username: "u", PasswordFile: filepath.Join(t.TempDir(), "none")})
	if err := none.put(ctx, "k", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("no password file: %v", err)
	}
}

func TestConsul(t *testing.T) {
	ctx := testCtx(t)
	if _, err := newConsul(&config.ConsulStorage{TokenFile: filepath.Join(t.TempDir(), "none")}, time.Second); err == nil {
		t.Error("newConsul with no token file")
	}
	if _, err := newConsul(&config.ConsulStorage{CAFile: filepath.Join(t.TempDir(), "none")}, time.Second); err == nil {
		t.Error("newConsul with no CA file")
	}
	if _, err := New(&config.SharedStorage{Consul: &config.ConsulStorage{TokenFile: filepath.Join(t.TempDir(), "none")}}, nil); err == nil {
		t.Error("New with no token file")
	}
	rs := &replies{}
	url := fakeHTTP(t, rs.ServeHTTP)
	b, err := newConsul(&config.ConsulStorage{Endpoint: url + "/", Datacenter: "dc2", TokenFile: writeFile(t, "token", " tok\n")}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	rs.set("/v1/kv/p/a", 200, "v")
	if v, err := b.get(ctx, "p/a"); err != nil || string(v) != "v" {
		t.Fatalf("get: %q %v", v, err)
	}
	if last := rs.last(); last != "GET /v1/kv/p/a?dc=dc2&raw= tok" {
		t.Fatalf("the request %q", last)
	}
	rs.set("/v1/kv/p/a", 500, "boom")
	if _, err := b.get(ctx, "p/a"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("get answered 500: %v", err)
	}
	rs.set("/v1/kv/p/a", 200, "false")
	if err := b.put(ctx, "p/a", nil); err == nil || !strings.Contains(err.Error(), "false") {
		t.Errorf("put answered false: %v", err)
	}

	// list
	rs.set("/v1/kv/p/", 200, "nope")
	if _, err := b.list(ctx, "p/"); err == nil {
		t.Error("list answered no JSON")
	}
	many := make([]string, maxKeys+1)
	for i := range many {
		many[i] = `"k"`
	}
	rs.set("/v1/kv/p/", 200, "["+strings.Join(many, ",")+"]")
	if _, err := b.list(ctx, "p/"); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("list of too many keys: %v", err)
	}
	rs.set("/v1/kv/p/", 404, "")
	if keys, err := b.list(ctx, "p/"); err != nil || keys != nil {
		t.Errorf("list of none: %q %v", keys, err)
	}

	// Locks.
	var destroyed atomic.Int32
	rs.setFunc("/v1/session/destroy/s1", func(w http.ResponseWriter, _ *http.Request) {
		destroyed.Add(1)
		io.WriteString(w, "true")
	})
	rs.set("/v1/session/create", 500, "")
	if _, _, err := b.tryLock(ctx, "p/l", time.Second); err == nil {
		t.Error("tryLock with no session made")
	}
	rs.set("/v1/session/create", 200, "{}")
	if _, _, err := b.tryLock(ctx, "p/l", time.Second); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Errorf("tryLock with no session in the answer: %v", err)
	}
	rs.set("/v1/session/create", 200, `{"ID":"s1"}`)
	rs.set("/v1/kv/p/l", 500, "")
	if _, _, err := b.tryLock(ctx, "p/l", time.Second); err == nil || destroyed.Load() != 1 {
		t.Errorf("tryLock with acquiring failing: %v, %d destroyed", err, destroyed.Load())
	}
	rs.set("/v1/kv/p/l", 200, "false")
	if _, ok, err := b.tryLock(ctx, "p/l", time.Second); ok || err != nil || destroyed.Load() != 2 {
		t.Errorf("tryLock of a lock held: %v %v, %d destroyed", ok, err, destroyed.Load())
	}
	rs.set("/v1/kv/p/l", 200, "true")
	l, ok, err := b.tryLock(ctx, "p/l", time.Second)
	if !ok || err != nil {
		t.Fatalf("tryLock: %v %v", ok, err)
	}
	if err := l.renew(ctx); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Errorf("renew of a session gone: %v", err)
	}
	rs.set("/v1/session/renew/s1", 200, "[]")
	if err := l.renew(ctx); err != nil {
		t.Errorf("renew: %v", err)
	}
	if err := l.release(ctx); err != nil {
		t.Errorf("release: %v", err)
	}

	bad, err := newConsul(&config.ConsulStorage{Endpoint: "http://a b"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.del(ctx, "k"); err == nil {
		t.Error("del with a bad endpoint")
	}
}

// fakeBackend is a backend whose calls fail with err, and whose locks are
// taken with lock.
type fakeBackend struct {
	err    error
	lock   func(key string) (lease, bool, error)
	closed atomic.Bool
}

func (f *fakeBackend) get(context.Context, string) ([]byte, error)    { return nil, f.err }
func (f *fakeBackend) put(context.Context, string, []byte) error      { return f.err }
func (f *fakeBackend) del(context.Context, string) error              { return f.err }
func (f *fakeBackend) list(context.Context, string) ([]string, error) { return nil, f.err }
func (f *fakeBackend) close()                                         { f.closed.Store(true) }
func (f *fakeBackend) tryLock(_ context.Context, key string, _ time.Duration) (lease, bool, error) {
	if f.lock == nil {
		return nil, false, f.err
	}
	return f.lock(key)
}

type fakeLease struct {
	err      error
	released atomic.Int32
}

func (l *fakeLease) renew(context.Context) error   { return l.err }
func (l *fakeLease) release(context.Context) error { l.released.Add(1); return l.err }

func fakeStore(b backend, ttl time.Duration, logf func(string, ...any)) *Store {
	return &Store{b: b, kind: "fake", prefix: "p/", ttl: ttl, timeout: 5 * time.Second, logf: logf, held: map[string]*holder{}}
}

func TestStoreErrors(t *testing.T) {
	ctx := testCtx(t)
	if _, err := New(&config.SharedStorage{}, nil); err == nil {
		t.Error("New of no kind")
	}
	s, err := New(&config.SharedStorage{Etcd: &config.EtcdStorage{}}, nil)
	if err != nil || s.Kind() != "etcd" {
		t.Fatalf("New: %v", err)
	}
	s.logf("a nil logf is replaced")
	s.Close()

	boom := errors.New("boom")
	s = fakeStore(&fakeBackend{err: boom}, time.Minute, t.Logf)
	if _, err := s.Load(ctx, "/a"); err == nil {
		t.Error("Load of a bad key")
	}
	if _, err := s.Load(ctx, "a"); !errors.Is(err, boom) {
		t.Errorf("Load: %v", err)
	}
	if err := s.Store(ctx, "a", nil); !errors.Is(err, boom) {
		t.Errorf("Store: %v", err)
	}
	if err := s.Delete(ctx, "a/../b"); err == nil {
		t.Error("Delete of a bad key")
	}
	if err := s.Delete(ctx, "a"); !errors.Is(err, boom) {
		t.Errorf("Delete: %v", err)
	}
	if _, err := s.List(ctx, "a/./b"); err == nil {
		t.Error("List of a bad prefix")
	}
	if _, err := s.List(ctx, "a"); !errors.Is(err, boom) {
		t.Errorf("List: %v", err)
	}
	if err := s.Lock(ctx, "a//b"); err == nil {
		t.Error("Lock of a bad name")
	}
	if err := s.Lock(ctx, "a"); !errors.Is(err, boom) {
		t.Errorf("Lock: %v", err)
	}
}

func TestStoreLocks(t *testing.T) {
	ctx := testCtx(t)
	// A lock whose renewals fail is reported, and one that cannot be
	// given back says so.
	logged := make(chan string, 1)
	l := &fakeLease{err: errors.New("lost")}
	fb := &fakeBackend{lock: func(string) (lease, bool, error) { return l, true, nil }}
	s := fakeStore(fb, 30*time.Millisecond, func(format string, args ...any) {
		select {
		case logged <- fmt.Sprintf(format, args...):
		default:
		}
	})
	if err := s.Lock(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if msg := <-logged; !strings.Contains(msg, "renewing lock a/b: lost") {
		t.Errorf("logged %q", msg)
	}
	if err := s.Unlock(ctx, "a/b"); err == nil || !strings.Contains(err.Error(), "giving back") {
		t.Errorf("Unlock: %v", err)
	}

	// Close gives back the locks held.
	l2 := &fakeLease{}
	fb2 := &fakeBackend{lock: func(string) (lease, bool, error) { return l2, true, nil }}
	s2 := fakeStore(fb2, time.Minute, t.Logf)
	for _, name := range []string{"x", "y"} {
		if err := s2.Lock(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	s2.Close()
	if l2.released.Load() != 2 || !fb2.closed.Load() || len(s2.held) != 0 {
		t.Fatalf("Close: %d released, closed %v, %d held", l2.released.Load(), fb2.closed.Load(), len(s2.held))
	}

	// A lock taken while this store came to hold it is given back.
	l3 := &fakeLease{}
	var s3 *Store
	fb3 := &fakeBackend{lock: func(string) (lease, bool, error) {
		s3.mu.Lock()
		s3.held["z"] = &holder{l: &fakeLease{}, stop: make(chan struct{}), done: make(chan struct{})}
		s3.mu.Unlock()
		return l3, true, nil
	}}
	s3 = fakeStore(fb3, time.Minute, t.Logf)
	lctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := s3.Lock(lctx, "z"); !errors.Is(err, context.DeadlineExceeded) || l3.released.Load() != 1 {
		t.Fatalf("Lock of a lock taken meanwhile: %v, %d released", err, l3.released.Load())
	}
}
