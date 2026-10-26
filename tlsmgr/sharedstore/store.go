// Package sharedstore keeps what certificate management keeps, account
// keys, certificates and challenge answers, in a store that instances of
// fit on several hosts share: Redis, etcd or Consul. A Store is a
// tlsmgr.Storage; it does not import tlsmgr, which builds one from the
// config.
//
// Its locks are leased: Redis keys with an expiry, etcd leases and Consul
// sessions, each lasting the lock TTL and renewed every third of it while
// held, so that an instance that stops holding one loses it once the TTL
// passes. Every call goes to the network and blocks, within the request
// timeout, so a Store is for the control plane's goroutines; see decision
// 014.
package sharedstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// MaxValueBytes bounds a value stored or loaded, as tlsmgr.MaxValueBytes
// does a file's.
const MaxValueBytes = 4 << 20

// maxKeys bounds the keys List returns.
const maxKeys = 100000

// lockPoll is how often a waiter for a lock tries again; a variable for
// tests to shorten.
var lockPoll = time.Second

// backend is what each kind of store does, on full keys.
type backend interface {
	get(ctx context.Context, key string) ([]byte, error) // fs.ErrNotExist when there is none
	put(ctx context.Context, key string, value []byte) error
	del(ctx context.Context, key string) error
	list(ctx context.Context, prefix string) ([]string, error)
	// tryLock takes the lock at key for ttl if nobody holds it, and
	// returns what renews and gives it back.
	tryLock(ctx context.Context, key string, ttl time.Duration) (lease, bool, error)
	close()
}

// A lease is a lock held.
type lease interface {
	renew(ctx context.Context) error
	release(ctx context.Context) error
}

// Store is a shared storage of one kind.
type Store struct {
	b       backend
	kind    string
	prefix  string
	ttl     time.Duration
	timeout time.Duration
	log     *logs.Logger

	mu   sync.Mutex
	held map[string]*holder
}

type holder struct {
	l    lease
	stop chan struct{}
	done chan struct{}
}

// New returns the store c names. log, which may be nil, hears what the
// store reports: locks that could not be renewed.
func New(c *config.SharedStorage, log *logs.Logger) (*Store, error) {
	s := &Store{prefix: c.KeyPrefix(), ttl: c.TTL(), timeout: c.Timeout(), log: log, held: map[string]*holder{}}
	var err error
	switch {
	case c.Redis != nil:
		s.kind = "redis"
		s.b, err = newRedis(c.Redis, s.timeout)
	case c.Etcd != nil:
		s.kind = "etcd"
		s.b, err = newEtcd(c.Etcd, s.timeout)
	case c.Consul != nil:
		s.kind = "consul"
		s.b, err = newConsul(c.Consul, s.timeout)
	default:
		return nil, errors.New("shared_storage: no kind is set")
	}
	if err != nil {
		return nil, fmt.Errorf("shared_storage.%s: %w", s.kind, err)
	}
	return s, nil
}

// Shared says the store is shared between hosts, so that what an instance
// needs another to answer, a challenge, is kept there.
func (s *Store) Shared() bool { return true }

// Kind names the store's kind.
func (s *Store) Kind() string { return s.kind }

// Close gives back the locks still held and closes the store's
// connections.
func (s *Store) Close() error {
	s.mu.Lock()
	names := make([]string, 0, len(s.held))
	for name := range s.held {
		names = append(names, name)
	}
	s.mu.Unlock()
	for _, name := range names {
		_ = s.Unlock(context.Background(), name)
	}
	s.b.close()
	return nil
}

// checkKey refuses a key that is empty, absolute, or has an empty, "." or
// ".." part, as FileStorage does.
func checkKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) || strings.ContainsRune(key, 0) {
		return fmt.Errorf("sharedstore: bad storage key %q", key)
	}
	for part := range strings.SplitSeq(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("sharedstore: bad storage key %q", key)
		}
	}
	return nil
}

// locksPart is where locks are kept, under the prefix; it is not a key
// List returns.
const locksPart = "locks/"

func (s *Store) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

func (s *Store) Load(ctx context.Context, key string) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	v, err := s.b.get(ctx, s.prefix+key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%s: loading %s: %w", s.kind, key, err)
	}
	return v, nil
}

func (s *Store) Store(ctx context.Context, key string, value []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if len(value) > MaxValueBytes {
		return fmt.Errorf("sharedstore: %s is larger than %d bytes", key, MaxValueBytes)
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if err := s.b.put(ctx, s.prefix+key, value); err != nil {
		return fmt.Errorf("%s: storing %s: %w", s.kind, key, err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if err := s.b.del(ctx, s.prefix+key); err != nil {
		return fmt.Errorf("%s: deleting %s: %w", s.kind, key, err)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		if err := checkKey(prefix); err != nil {
			return nil, err
		}
		prefix += "/"
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	full, err := s.b.list(ctx, s.prefix+prefix)
	if err != nil {
		return nil, fmt.Errorf("%s: listing %s: %w", s.kind, prefix, err)
	}
	keys := make([]string, 0, len(full))
	for _, k := range full {
		k, ok := strings.CutPrefix(k, s.prefix)
		if !ok || strings.HasPrefix(k, locksPart) || strings.HasSuffix(k, "/") {
			continue
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}

// lockKey is the key of the lock name.
func (s *Store) lockKey(name string) string {
	return s.prefix + locksPart + strings.ReplaceAll(name, "/", "_")
}

// Lock takes the lock name, trying again every second while another
// holder, in this process or another, has it and ctx allows. Once taken,
// it is renewed every third of the lock TTL until Unlock gives it back.
func (s *Store) Lock(ctx context.Context, name string) error {
	if err := checkKey(name); err != nil {
		return err
	}
	var timer *time.Timer
	for {
		s.mu.Lock()
		_, mine := s.held[name]
		s.mu.Unlock()
		if !mine {
			tctx, cancel := s.ctx(ctx)
			l, ok, err := s.b.tryLock(tctx, s.lockKey(name), s.ttl)
			cancel()
			if err != nil {
				return fmt.Errorf("%s: taking lock %s: %w", s.kind, name, err)
			}
			if ok {
				s.mu.Lock()
				if _, mine := s.held[name]; !mine {
					h := &holder{l: l, stop: make(chan struct{}), done: make(chan struct{})}
					s.held[name] = h
					s.mu.Unlock()
					go s.keep(name, h)
					return nil
				}
				s.mu.Unlock()
				// Taken by another goroutine of this process meanwhile,
				// which cannot be, since the store held it; give it back.
				rctx, cancel := s.ctx(context.Background())
				_ = l.release(rctx)
				cancel()
			}
		}
		if timer == nil {
			timer = time.NewTimer(lockPoll)
			defer timer.Stop()
		} else {
			timer.Reset(lockPoll)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// keep renews the lock name every third of the TTL until it is given back.
func (s *Store) keep(name string, h *holder) {
	defer close(h.done)
	t := time.NewTicker(s.ttl / 3)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			ctx, cancel := s.ctx(context.Background())
			err := h.l.renew(ctx)
			cancel()
			if err != nil {
				s.log.Warnf("%s: renewing lock %s: %v", s.kind, name, err)
			}
		}
	}
}

func (s *Store) Unlock(ctx context.Context, name string) error {
	s.mu.Lock()
	h, ok := s.held[name]
	delete(s.held, name)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("sharedstore: lock %q is not held", name)
	}
	close(h.stop)
	<-h.done
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if err := h.l.release(ctx); err != nil {
		return fmt.Errorf("%s: giving back lock %s: %w", s.kind, name, err)
	}
	return nil
}
