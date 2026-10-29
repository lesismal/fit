package sharedstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// The images of the servers the stores are tested against, as test/e2e's.
const (
	redisImage  = "redis:8.2.2"
	etcdImage   = "quay.io/coreos/etcd:v3.6.5"
	consulImage = "hashicorp/consul:1.21.5"
)

// server runs image in Docker with its port published on 127.0.0.1 and
// returns the address, skipping the test without Docker.
func server(t *testing.T, image string, port int, args ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" || os.Getenv("FIT_E2E_DOCKER") == "0" {
		t.Skip("no Docker here")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		if out, err := exec.Command("docker", "pull", image).CombinedOutput(); err != nil {
			t.Fatalf("docker pull: %v\n%s", err, out)
		}
	}
	out, err := exec.Command("docker", append([]string{"run", "-d", "--rm", "-p", fmt.Sprintf("127.0.0.1::%d", port), image}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	out, err = exec.Command("docker", "port", id, fmt.Sprintf("%d/tcp", port)).CombinedOutput()
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, out)
	}
	addr, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return addr
}

// await waits until ok, failing the test after a minute.
func await(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// configs starts each kind's server and returns its config, under a prefix
// of the test's own.
func configs(t *testing.T) map[string]*config.SharedStorage {
	out := map[string]*config.SharedStorage{}
	ttl := config.Duration(10 * time.Second)

	pw := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pw, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	redis := server(t, redisImage, 6379, "redis-server", "--requirepass", "s3cret")
	out["redis"] = &config.SharedStorage{Redis: &config.RedisStorage{Address: redis, PasswordFile: pw, DB: 2}, LockTTL: ttl, Prefix: "t/"}

	etcd := "http://" + server(t, etcdImage, 2379, "etcd", "--listen-client-urls", "http://0.0.0.0:2379", "--advertise-client-urls", "http://127.0.0.1:2379")
	out["etcd"] = &config.SharedStorage{Etcd: &config.EtcdStorage{Endpoints: []string{"http://127.0.0.1:1", etcd}}, LockTTL: ttl, Prefix: "t/"}

	consul := "http://" + server(t, consulImage, 8500, "agent", "-dev", "-client", "0.0.0.0")
	out["consul"] = &config.SharedStorage{Consul: &config.ConsulStorage{Endpoint: consul}, LockTTL: ttl, Prefix: "t/"}

	await(t, "etcd", func() bool {
		resp, err := http.Post(etcd+"/v3/kv/range", "application/json", strings.NewReader(`{"key": "AA=="}`))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil && resp.StatusCode == 200
	})
	await(t, "Consul", func() bool {
		resp, err := http.Get(consul + "/v1/status/leader")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		return resp.StatusCode == 200 && len(strings.Trim(line, "\"\n")) > 0
	})
	return out
}

func open(t *testing.T, c *config.SharedStorage) *Store {
	t.Helper()
	s, err := New(c, logs.Func(t.Logf))
	if err != nil {
		t.Fatal(err)
	}
	// Redis may not take connections for a moment after it starts.
	await(t, "the store", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err = s.Load(ctx, "probe")
		return errors.Is(err, fs.ErrNotExist)
	})
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStores(t *testing.T) {
	poll := lockPoll
	lockPoll = 50 * time.Millisecond
	t.Cleanup(func() { lockPoll = poll })
	for kind, c := range configs(t) {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, b := open(t, c), open(t, c)
			if a.Kind() != kind || !a.Shared() {
				t.Fatalf("Kind %q, Shared %v", a.Kind(), a.Shared())
			}
			testKeys(t, a, b)
			testLocks(t, a, b, c.TTL())
		})
	}
}

func testKeys(t *testing.T, a, b *Store) {
	ctx := context.Background()
	if _, err := a.Load(ctx, "certificates/ca/none.pem"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a key not there: %v", err)
	}
	for k, v := range map[string]string{"certificates/ca/a.pem": "A", "certificates/ca/b.pem": "B", "acme/ca/account.pem": "K", "a?b/c": "glob", "aXb/c": "not"} {
		if err := a.Store(ctx, k, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Store(ctx, "certificates/ca/a.pem", []byte("A2")); err != nil {
		t.Fatal(err)
	}
	if v, err := b.Load(ctx, "certificates/ca/a.pem"); err != nil || string(v) != "A2" {
		t.Fatalf("another store's load: %q %v", v, err)
	}
	// A lock is not a key.
	if err := a.Lock(ctx, "issue/ca/a"); err != nil {
		t.Fatal(err)
	}
	keys, err := b.List(ctx, "")
	if err != nil || !reflect.DeepEqual(keys, []string{"a?b/c", "aXb/c", "acme/ca/account.pem", "certificates/ca/a.pem", "certificates/ca/b.pem"}) {
		t.Fatalf("List all: %q %v", keys, err)
	}
	if err := a.Unlock(ctx, "issue/ca/a"); err != nil {
		t.Fatal(err)
	}
	if keys, err := b.List(ctx, "certificates/"); err != nil || !reflect.DeepEqual(keys, []string{"certificates/ca/a.pem", "certificates/ca/b.pem"}) {
		t.Fatalf("List certificates/: %q %v", keys, err)
	}
	if keys, err := b.List(ctx, "a?b"); err != nil || !reflect.DeepEqual(keys, []string{"a?b/c"}) {
		t.Fatalf("a prefix with glob characters: %q %v", keys, err)
	}
	if keys, err := b.List(ctx, "nothing"); err != nil || len(keys) != 0 {
		t.Fatalf("List nothing: %q %v", keys, err)
	}
	if err := b.Delete(ctx, "certificates/ca/b.pem"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "certificates/ca/b.pem"); err != nil {
		t.Fatalf("deleting a key not there: %v", err)
	}
	if _, err := a.Load(ctx, "certificates/ca/b.pem"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a deleted key: %v", err)
	}
	for _, bad := range []string{"", "/a", "a//b", "a/../b", "./a", `a\b`} {
		if err := a.Store(ctx, bad, nil); err == nil {
			t.Errorf("Store(%q) took a bad key", bad)
		}
	}
	if err := a.Store(ctx, "big", make([]byte, MaxValueBytes+1)); err == nil {
		t.Error("a value past MaxValueBytes was stored")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Load(cancelled, "certificates/ca/a.pem"); err == nil {
		t.Error("a load with a cancelled context")
	}
}

func testLocks(t *testing.T, a, b *Store, ttl time.Duration) {
	ctx := context.Background()
	// Eight holders over the two stores take the lock in turn, never two
	// at once.
	var holding, most atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			lctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if err := s.Lock(lctx, "issue/ca/turns"); err != nil {
				t.Error(err)
				return
			}
			n := holding.Add(1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(20 * time.Millisecond)
			holding.Add(-1)
			if err := s.Unlock(ctx, "issue/ca/turns"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Fatalf("%d holders at once", most.Load())
	}
	if err := a.Unlock(ctx, "issue/ca/turns"); err == nil {
		t.Error("a lock not held was given back")
	}

	// A lock held past its TTL is renewed, and waited for; given back,
	// it is taken.
	if err := a.Lock(ctx, "issue/ca/long"); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, ttl+ttl/2)
	err := b.Lock(wctx, "issue/ca/long")
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a lock held past its TTL was taken: %v", err)
	}
	got := make(chan error, 1)
	go func() {
		lctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		got <- b.Lock(lctx, "issue/ca/long")
	}()
	time.Sleep(100 * time.Millisecond)
	if err := a.Unlock(ctx, "issue/ca/long"); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("a lock given back: %v", err)
	}
	if err := b.Unlock(ctx, "issue/ca/long"); err != nil {
		t.Fatal(err)
	}

	// A holder that stops renewing without giving it back loses the lock
	// once its TTL passes (Consul's sessions may take up to twice it).
	if err := a.Lock(ctx, "issue/ca/left"); err != nil {
		t.Fatal(err)
	}
	a.abandon("issue/ca/left")
	start := time.Now()
	lctx, cancel := context.WithTimeout(ctx, 3*ttl+5*time.Second)
	defer cancel()
	if err := b.Lock(lctx, "issue/ca/left"); err != nil {
		t.Fatalf("a lock left behind: %v", err)
	}
	t.Logf("a lock left behind was taken after %s", time.Since(start).Round(time.Second))
	if err := b.Unlock(ctx, "issue/ca/left"); err != nil {
		t.Fatal(err)
	}
}

// abandon stops renewing the lock name without giving it back, as an
// instance that stopped would.
func (s *Store) abandon(name string) {
	s.mu.Lock()
	h := s.held[name]
	delete(s.held, name)
	s.mu.Unlock()
	close(h.stop)
	<-h.done
}

func TestReadReply(t *testing.T) {
	for _, c := range []struct {
		in   string
		want any
		err  string
	}{
		{"+OK\r\n", "OK", ""},
		{"-ERR wrong\r\n", nil, "ERR wrong"},
		{":42\r\n", int64(42), ""},
		{"$3\r\nabc\r\n", "abc", ""},
		{"$-1\r\n", nil, ""},
		{"*2\r\n$1\r\n0\r\n*1\r\n$1\r\nk\r\n", []any{"0", []any{"k"}}, ""},
		{"*1\r\n*1\r\n*1\r\n:1\r\n", nil, "array"},
		{fmt.Sprintf("$%d\r\n", MaxValueBytes+1), nil, "bulk string"},
		{"$5\r\nab", nil, "EOF"},
		{"?\r\n", nil, "type"},
		{"+OK\n", nil, "not RESP"},
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

// A socket timed out at the caller's deadline is the caller's deadline,
// even before its context's timer has fired; any other error is kept.
func TestCallerErr(t *testing.T) {
	timeout := fmt.Errorf("write: %w", os.ErrDeadlineExceeded)
	past, cancel := context.WithDeadline(context.WithoutCancel(context.Background()), time.Now().Add(-time.Second))
	defer cancel()
	if err := callerErr(past, timeout); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("past the deadline: %v", err)
	}
	later, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if err := callerErr(later, timeout); err != timeout {
		t.Errorf("before the deadline: %v", err)
	}
	other := errors.New("refused")
	if err := callerErr(context.Background(), other); err != other {
		t.Errorf("no deadline: %v", err)
	}
	done, stop := context.WithCancel(context.Background())
	stop()
	if err := callerErr(done, other); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}
