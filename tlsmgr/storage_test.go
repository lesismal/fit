package tlsmgr

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileStorage(t *testing.T) {
	ctx := context.Background()
	s := &FileStorage{Root: t.TempDir()}
	if _, err := s.Load(ctx, "certs/a.example/cert.pem"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("load of a missing key: %v", err)
	}
	for key, value := range map[string]string{
		"certs/a.example/cert.pem": "a cert",
		"certs/a.example/key.pem":  "a key",
		"certs/b.example/cert.pem": "b cert",
		"accounts/acme/key.pem":    "account",
	} {
		if err := s.Store(ctx, key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Store(ctx, "certs/a.example/cert.pem", []byte("a cert, renewed")); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Load(ctx, "certs/a.example/cert.pem"); err != nil || string(v) != "a cert, renewed" {
		t.Fatalf("load: %q, %v", v, err)
	}
	fi, err := os.Stat(filepath.Join(s.Root, "certs", "a.example", "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 && filepath.Separator == '/' {
		t.Errorf("a key file is readable by others: %v", mode)
	}

	// A lock held does not show in List.
	if err := s.Lock(ctx, "issue/a.example"); err != nil {
		t.Fatal(err)
	}
	defer s.Unlock(ctx, "issue/a.example")
	for prefix, want := range map[string][]string{
		"":                {"accounts/acme/key.pem", "certs/a.example/cert.pem", "certs/a.example/key.pem", "certs/b.example/cert.pem"},
		"certs":           {"certs/a.example/cert.pem", "certs/a.example/key.pem", "certs/b.example/cert.pem"},
		"certs/a.example": {"certs/a.example/cert.pem", "certs/a.example/key.pem"},
		"missing":         nil,
	} {
		if got, err := s.List(ctx, prefix); err != nil || !slices.Equal(got, want) {
			t.Errorf("list %q: %v, %v", prefix, got, err)
		}
	}

	if err := s.Delete(ctx, "certs/b.example/cert.pem"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "certs/b.example/cert.pem"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
	if _, err := s.Load(ctx, "certs/b.example/cert.pem"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("load after delete: %v", err)
	}

	for _, key := range []string{"", "/abs", "a/../b", "a//b", "./a", "a/.", `a\b`, "..", "a\x00"} {
		if err := s.Store(ctx, key, nil); err == nil || !strings.Contains(err.Error(), "bad storage key") {
			t.Errorf("store %q: %v", key, err)
		}
		if _, err := s.Load(ctx, key); err == nil {
			t.Errorf("load %q: no error", key)
		}
	}
	if err := s.Store(ctx, "big", make([]byte, MaxValueBytes+1)); err == nil {
		t.Error("a value past the limit was stored")
	}
	if err := os.WriteFile(filepath.Join(s.Root, "big"), make([]byte, MaxValueBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, "big"); err == nil {
		t.Error("a value past the limit was loaded")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Store(cancelled, "x", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("store with a cancelled context: %v", err)
	}
}

// TestFileStorageLock has holders in two FileStorages on the same directory,
// as two instances of fit would be, take a lock in turn.
func TestFileStorageLock(t *testing.T) {
	root := t.TempDir()
	stores := []*FileStorage{
		{Root: root, LockPoll: 5 * time.Millisecond},
		{Root: root, LockPoll: 5 * time.Millisecond},
	}
	ctx := context.Background()
	var in, overlaps atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			s := stores[i%2]
			for range 5 {
				if err := s.Lock(ctx, "issue/a.example"); err != nil {
					t.Error(err)
					return
				}
				if in.Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(time.Millisecond)
				in.Add(-1)
				if err := s.Unlock(ctx, "issue/a.example"); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if overlaps.Load() != 0 {
		t.Errorf("the lock was held by two at once %d times", overlaps.Load())
	}
	if err := stores[0].Unlock(ctx, "issue/a.example"); err == nil {
		t.Error("a lock not held was given back")
	}
}

func TestFileStorageLockWait(t *testing.T) {
	root := t.TempDir()
	a := &FileStorage{Root: root, LockRefresh: 10 * time.Millisecond, LockStale: 200 * time.Millisecond, LockPoll: 5 * time.Millisecond}
	b := &FileStorage{Root: root, LockRefresh: 10 * time.Millisecond, LockStale: 200 * time.Millisecond, LockPoll: 5 * time.Millisecond}
	ctx := context.Background()
	if err := a.Lock(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	// A holder that keeps its lock fresh keeps it past the stale time.
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := b.Lock(short, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock while held: %v", err)
	}
	// Once given back, the waiter gets it.
	done := make(chan error, 1)
	go func() { done <- b.Lock(ctx, "x") }()
	time.Sleep(20 * time.Millisecond)
	if err := a.Unlock(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter did not get the lock")
	}
	if err := b.Unlock(ctx, "x"); err != nil {
		t.Fatal(err)
	}
}

// TestFileStorageStaleLock has a holder stop without giving its lock back.
func TestFileStorageStaleLock(t *testing.T) {
	root := t.TempDir()
	s := &FileStorage{Root: root, LockStale: 200 * time.Millisecond, LockPoll: 5 * time.Millisecond}
	file, err := s.lockFile("x")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := s.Lock(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("a fresh lock was taken over after %v", waited)
	}
	if err := s.Unlock(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("lock file left: %v", err)
	}
}
