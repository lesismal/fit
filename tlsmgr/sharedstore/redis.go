package sharedstore

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
)

// redisConns bounds the connections kept to Redis, idle or not.
const redisConns = 4

// Scripts that act on a lock only while its value is the holder's token.
const (
	redisRenew   = `if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) else return 0 end`
	redisRelease = `if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`
)

// redisBackend speaks RESP2 to one Redis server, over a few connections.
type redisBackend struct {
	opts    *config.RedisStorage
	tls     *tls.Config // nil without TLS
	timeout time.Duration
	slots   chan struct{} // one per connection open or being opened
	mu      sync.Mutex
	idle    []*redisConn
}

func newRedis(c *config.RedisStorage, timeout time.Duration) (*redisBackend, error) {
	r := &redisBackend{opts: c, timeout: timeout, slots: make(chan struct{}, redisConns)}
	if c.TLS {
		host, _, _ := net.SplitHostPort(c.Addr())
		r.tls = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if c.CAFile != "" {
			pem, err := os.ReadFile(c.CAFile)
			if err != nil {
				return nil, err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("%s: no PEM certificates", c.CAFile)
			}
			r.tls.RootCAs = pool
		}
		if c.CertFile != "" {
			cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
			if err != nil {
				return nil, err
			}
			r.tls.Certificates = []tls.Certificate{cert}
		}
	}
	return r, nil
}

type redisConn struct {
	c net.Conn
	r *bufio.Reader
}

// redisError is an error Redis answered with.
type redisError string

func (e redisError) Error() string { return string(e) }

// do sends a command and reads its reply: a string, an int64, nil, a
// redisError or a []any.
func (r *redisBackend) do(ctx context.Context, args ...string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case r.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r.mu.Lock()
	var c *redisConn
	if n := len(r.idle); n > 0 {
		c, r.idle = r.idle[n-1], r.idle[:n-1]
	}
	r.mu.Unlock()
	if c == nil {
		var err error
		if c, err = r.dial(ctx); err != nil {
			<-r.slots
			return nil, err
		}
	}
	reply, err := c.roundTrip(ctx, args)
	if err != nil {
		var re redisError
		if !errors.As(err, &re) { // the connection is in an unknown state
			c.c.Close()
			<-r.slots
			return nil, err
		}
	}
	r.mu.Lock()
	r.idle = append(r.idle, c)
	r.mu.Unlock()
	<-r.slots
	return reply, err
}

// dial connects, authenticates and selects the database.
func (r *redisBackend) dial(ctx context.Context) (*redisConn, error) {
	d := net.Dialer{Timeout: r.timeout, KeepAlive: 15 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", r.opts.Addr())
	if err != nil {
		return nil, err
	}
	if r.tls != nil {
		tc := tls.Client(nc, r.tls)
		if err := tc.HandshakeContext(ctx); err != nil {
			nc.Close()
			return nil, err
		}
		nc = tc
	}
	c := &redisConn{c: nc, r: bufio.NewReader(nc)}
	if r.opts.PasswordFile != "" {
		data, err := os.ReadFile(r.opts.PasswordFile)
		if err != nil {
			nc.Close()
			return nil, err
		}
		args := []string{"AUTH", strings.TrimRight(string(data), "\r\n")}
		if r.opts.Username != "" {
			args = []string{"AUTH", r.opts.Username, args[1]}
		}
		if _, err := c.roundTrip(ctx, args); err != nil {
			nc.Close()
			return nil, fmt.Errorf("AUTH: %w", err)
		}
	}
	if r.opts.DB != 0 {
		if _, err := c.roundTrip(ctx, []string{"SELECT", strconv.Itoa(r.opts.DB)}); err != nil {
			nc.Close()
			return nil, fmt.Errorf("SELECT: %w", err)
		}
	}
	return c, nil
}

func (c *redisConn) roundTrip(ctx context.Context, args []string) (any, error) {
	if dl, ok := ctx.Deadline(); ok {
		c.c.SetDeadline(dl)
	} else {
		c.c.SetDeadline(time.Time{})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(c.c, b.String()); err != nil {
		return nil, err
	}
	return readReply(c.r, 0)
}

// readReply reads one RESP2 reply, its bulk strings at most MaxValueBytes
// and its arrays at most maxKeys long and two deep.
func readReply(r *bufio.Reader, depth int) (any, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || line[len(line)-2] != '\r' {
		return nil, errors.New("redis: a reply that is not RESP")
	}
	kind, rest := line[0], string(line[1:len(line)-2])
	switch kind {
	case '+':
		return rest, nil
	case '-':
		return nil, redisError(rest)
	case ':':
		n, err := strconv.ParseInt(rest, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("redis: integer %q", rest)
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(rest)
		switch {
		case err != nil || n < -1 || n > MaxValueBytes:
			return nil, fmt.Errorf("redis: bulk string of length %q", rest)
		case n == -1:
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(rest)
		switch {
		case err != nil || n < -1 || n > maxKeys || depth > 1:
			return nil, fmt.Errorf("redis: array of length %q", rest)
		case n == -1:
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = readReply(r, depth+1); err != nil {
				var re redisError
				if !errors.As(err, &re) {
					return nil, err
				}
				out[i] = re
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("redis: a reply of type %q", kind)
}

func (r *redisBackend) get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.do(ctx, "GET", key)
	if err != nil {
		return nil, err
	}
	switch v := v.(type) {
	case nil:
		return nil, fs.ErrNotExist
	case string:
		return []byte(v), nil
	}
	return nil, fmt.Errorf("GET: a reply of %T", v)
}

func (r *redisBackend) put(ctx context.Context, key string, value []byte) error {
	_, err := r.do(ctx, "SET", key, string(value))
	return err
}

func (r *redisBackend) del(ctx context.Context, key string) error {
	_, err := r.do(ctx, "DEL", key)
	return err
}

// list scans the keys starting with prefix, its glob characters escaped.
func (r *redisBackend) list(ctx context.Context, prefix string) ([]string, error) {
	var pattern strings.Builder
	for _, c := range prefix {
		if strings.ContainsRune(`*?[]\`, c) {
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(c)
	}
	pattern.WriteByte('*')
	var keys []string
	cursor := "0"
	for {
		v, err := r.do(ctx, "SCAN", cursor, "MATCH", pattern.String(), "COUNT", "1000")
		if err != nil {
			return nil, err
		}
		a, ok := v.([]any)
		if !ok || len(a) != 2 {
			return nil, errors.New("SCAN: a reply that is not a cursor and keys")
		}
		cursor, _ = a[0].(string)
		batch, _ := a[1].([]any)
		for _, k := range batch {
			if s, ok := k.(string); ok {
				keys = append(keys, s)
			}
		}
		if len(keys) > maxKeys {
			return nil, fmt.Errorf("more than %d keys", maxKeys)
		}
		if cursor == "0" || cursor == "" {
			return keys, nil
		}
	}
}

type redisLease struct {
	r          *redisBackend
	key, token string
	ttl        time.Duration
}

func (r *redisBackend) tryLock(ctx context.Context, key string, ttl time.Duration) (lease, bool, error) {
	var b [16]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	v, err := r.do(ctx, "SET", key, token, "NX", "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	if err != nil {
		return nil, false, err
	}
	if v == nil {
		return nil, false, nil
	}
	return &redisLease{r: r, key: key, token: token, ttl: ttl}, true, nil
}

func (l *redisLease) renew(ctx context.Context) error {
	v, err := l.r.do(ctx, "EVAL", redisRenew, "1", l.key, l.token, strconv.FormatInt(l.ttl.Milliseconds(), 10))
	if err != nil {
		return err
	}
	if n, _ := v.(int64); n != 1 {
		return errors.New("the lock was lost: another holder has it, or it expired")
	}
	return nil
}

func (l *redisLease) release(ctx context.Context) error {
	_, err := l.r.do(ctx, "EVAL", redisRelease, "1", l.key, l.token)
	return err
}

func (r *redisBackend) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.idle {
		c.c.Close()
	}
	r.idle = nil
}
