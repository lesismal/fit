package tlsmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Storage keeps what certificate management needs across restarts, account
// keys, certificates and their keys, and shares it between instances of fit
// that use the same one. Keys are paths of slash-separated parts.
//
// Its methods may block, on a disk or a network store, so the manager calls
// them on goroutines of its own, never on a fib worker, with a context that
// bounds them.
type Storage interface {
	// Load returns the value of key, or an error that is fs.ErrNotExist
	// when there is none.
	Load(ctx context.Context, key string) ([]byte, error)
	// Store sets the value of key, all of it or none.
	Store(ctx context.Context, key string, value []byte) error
	// Delete removes key; removing one that is not there is no error.
	Delete(ctx context.Context, key string) error
	// List returns the keys under prefix, at any depth, sorted.
	List(ctx context.Context, prefix string) ([]string, error)
	// Lock takes the lock name, waiting for as long as another holder, in
	// this process or another, has it and ctx allows. A holder that stops
	// without unlocking loses it once it goes stale.
	Lock(ctx context.Context, name string) error
	// Unlock gives back the lock name, taken with Lock.
	Unlock(ctx context.Context, name string) error
}

// MaxValueBytes bounds a value FileStorage stores or loads.
const MaxValueBytes = 4 << 20

// Defaults of FileStorage's lock timings.
const (
	DefaultLockRefresh = 5 * time.Second
	DefaultLockStale   = 30 * time.Second
	DefaultLockPoll    = time.Second
)

// FileStorage is a Storage in a directory: each key a file under Root, and
// each lock a file under Root/locks, which its holder touches every
// LockRefresh. A lock file left untouched for LockStale is taken to be
// left by a holder that stopped, and is removed. Files and directories are
// made readable by their owner only.
type FileStorage struct {
	Root string
	// LockRefresh, LockStale and LockPoll are how often a holder touches
	// its lock, how old a lock gets before it is stale, and how often a
	// waiter looks again; zero means the default.
	LockRefresh, LockStale, LockPoll time.Duration

	mu   sync.Mutex
	held map[string]chan struct{} // the locks held, each with what stops its refresh
}

// locksDir is where FileStorage keeps its locks, under Root; it is not a
// key List returns.
const locksDir = "locks"

var _ Storage = (*FileStorage)(nil)

// filename returns the file of key, or an error for a key that is empty,
// absolute, or has an empty, "." or ".." part, or a backslash.
func (s *FileStorage) filename(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) || strings.ContainsRune(key, 0) {
		return "", fmt.Errorf("tlsmgr: bad storage key %q", key)
	}
	for part := range strings.SplitSeq(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("tlsmgr: bad storage key %q", key)
		}
	}
	return filepath.Join(s.Root, filepath.FromSlash(key)), nil
}

func (s *FileStorage) Load(ctx context.Context, key string) ([]byte, error) {
	name, err := s.filename(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxValueBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxValueBytes {
		return nil, fmt.Errorf("tlsmgr: %s is larger than %d bytes", key, MaxValueBytes)
	}
	return data, nil
}

func (s *FileStorage) Store(ctx context.Context, key string, value []byte) error {
	name, err := s.filename(key)
	if err != nil {
		return err
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("tlsmgr: %s is larger than %d bytes", key, MaxValueBytes)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeFile(name, value)
}

// writeFile writes name whole or not at all: to a file beside it, synced,
// and then renamed over it.
func writeFile(name string, value []byte) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(value)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

func (s *FileStorage) Delete(ctx context.Context, key string) error {
	name, err := s.filename(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FileStorage) List(ctx context.Context, prefix string) ([]string, error) {
	dir := s.Root
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		var err error
		if dir, err = s.filename(prefix); err != nil {
			return nil, err
		}
	}
	var keys []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.Root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			if rel == locksDir {
				return fs.SkipDir
			}
		case !strings.HasPrefix(d.Name(), ".tmp-"):
			keys = append(keys, rel)
		}
		return nil
	})
	return keys, err
}

// lockFile returns the file of the lock name.
func (s *FileStorage) lockFile(name string) (string, error) {
	return s.filename(path.Join(locksDir, strings.ReplaceAll(name, "/", "_")+".lock"))
}

func (s *FileStorage) Lock(ctx context.Context, name string) error {
	file, err := s.lockFile(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	stale := orDefault(s.LockStale, DefaultLockStale)
	var timer *time.Timer
	for {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = fmt.Fprintf(f, "%d\n", os.Getpid())
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(file)
				return err
			}
			s.keep(name, file)
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		// Held. A lock nobody has touched for stale is removed, and taken
		// on the next try; any other is waited for.
		if fi, err := os.Stat(file); err == nil && time.Since(fi.ModTime()) > stale {
			_ = os.Remove(file)
			continue
		}
		if timer == nil {
			timer = time.NewTimer(orDefault(s.LockPoll, DefaultLockPoll))
			defer timer.Stop()
		} else {
			timer.Reset(orDefault(s.LockPoll, DefaultLockPoll))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// keep touches the lock file every LockRefresh until the lock is given back.
func (s *FileStorage) keep(name, file string) {
	stop := make(chan struct{})
	s.mu.Lock()
	if s.held == nil {
		s.held = map[string]chan struct{}{}
	}
	s.held[name] = stop
	s.mu.Unlock()
	go func() {
		t := time.NewTicker(orDefault(s.LockRefresh, DefaultLockRefresh))
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				_ = os.Chtimes(file, now, now)
			}
		}
	}()
}

func (s *FileStorage) Unlock(_ context.Context, name string) error {
	file, err := s.lockFile(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	stop, ok := s.held[name]
	delete(s.held, name)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("tlsmgr: lock %q is not held", name)
	}
	close(stop)
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
