package sharedstore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/apiclient"
)

// etcdBackend speaks etcd's v3 API through its JSON gateway, on one
// endpoint at a time, the next after one fails.
type etcdBackend struct {
	opts    *config.EtcdStorage
	hc      *http.Client
	timeout time.Duration

	mu    sync.Mutex
	next  int    // the endpoint to use
	token string // from authenticating, when a user is set
}

func newEtcd(c *config.EtcdStorage, timeout time.Duration) (*etcdBackend, error) {
	hc, err := apiclient.Client(apiclient.TLSFiles{CA: c.CAFile, Cert: c.CertFile, Key: c.KeyFile}, timeout, 4)
	if err != nil {
		return nil, err
	}
	return &etcdBackend{opts: c, hc: hc, timeout: timeout}, nil
}

// etcdStatusError is an answer of etcd's that is not 200.
type etcdStatusError struct {
	status int
	err    error
}

func (e *etcdStatusError) Error() string { return e.err.Error() }

// call posts in to path and reads the answer's first JSON object into out,
// authenticating first when a user is set and no token is held, and again
// once when etcd refuses the token. An endpoint that cannot be reached
// makes the next one used, for this call and the ones after it.
func (e *etcdBackend) call(ctx context.Context, path string, in, out any) error {
	var err error
	for range len(e.opts.URLs()) {
		if err = e.callOne(ctx, path, in, out); err == nil || ctx.Err() != nil {
			return err
		}
		var se *etcdStatusError
		if errors.As(err, &se) {
			return err
		}
	}
	return err
}

func (e *etcdBackend) callOne(ctx context.Context, path string, in, out any) error {
	for try := 0; ; try++ {
		e.mu.Lock()
		eps := e.opts.URLs()
		ep := strings.TrimSuffix(eps[e.next%len(eps)], "/")
		token := e.token
		e.mu.Unlock()
		if e.opts.Username != "" && token == "" {
			var err error
			if token, err = e.authenticate(ctx, ep); err != nil {
				e.failed(ep, err)
				return err
			}
		}
		err := e.post(ctx, ep, path, token, in, out)
		var se *etcdStatusError
		if errors.As(err, &se) && e.opts.Username != "" && try == 0 && strings.Contains(err.Error(), "token") {
			e.mu.Lock()
			e.token = ""
			e.mu.Unlock()
			continue
		}
		e.failed(ep, err)
		return err
	}
}

// failed moves to the next endpoint after one could not be reached.
func (e *etcdBackend) failed(ep string, err error) {
	var se *etcdStatusError
	if err == nil || errors.As(err, &se) || errors.Is(err, context.Canceled) {
		return
	}
	e.mu.Lock()
	eps := e.opts.URLs()
	if strings.TrimSuffix(eps[e.next%len(eps)], "/") == ep {
		e.next++
		e.token = ""
	}
	e.mu.Unlock()
}

func (e *etcdBackend) authenticate(ctx context.Context, ep string) (string, error) {
	password, err := os.ReadFile(e.opts.PasswordFile)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	in := map[string]string{"name": e.opts.Username, "password": strings.TrimRight(string(password), "\r\n")}
	if err := e.post(ctx, ep, "/v3/auth/authenticate", "", in, &out); err != nil {
		return "", err
	}
	e.mu.Lock()
	e.token = out.Token
	e.mu.Unlock()
	return out.Token, nil
}

func (e *etcdBackend) post(ctx context.Context, ep, path, token string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return err
	}
	what := "POST " + path
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		return &etcdStatusError{status: status, err: apiclient.StatusError(resp, what)}
	}
	defer resp.Body.Close()
	// A stream, as /v3/lease/keepalive answers, has one object a line;
	// the first is the answer.
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 4096), 2*MaxValueBytes)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return fmt.Errorf("%s: an empty answer", what)
	}
	if err := json.Unmarshal(sc.Bytes(), out); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func (e *etcdBackend) get(ctx context.Context, key string) ([]byte, error) {
	var out struct {
		KVs []struct {
			Value []byte `json:"value"`
		} `json:"kvs"`
	}
	if err := e.call(ctx, "/v3/kv/range", map[string]any{"key": []byte(key)}, &out); err != nil {
		return nil, err
	}
	if len(out.KVs) == 0 {
		return nil, fs.ErrNotExist
	}
	return out.KVs[0].Value, nil
}

func (e *etcdBackend) put(ctx context.Context, key string, value []byte) error {
	var out struct{}
	return e.call(ctx, "/v3/kv/put", map[string]any{"key": []byte(key), "value": value}, &out)
}

func (e *etcdBackend) del(ctx context.Context, key string) error {
	var out struct{}
	return e.call(ctx, "/v3/kv/deleterange", map[string]any{"key": []byte(key)}, &out)
}

func (e *etcdBackend) list(ctx context.Context, prefix string) ([]string, error) {
	var out struct {
		KVs []struct {
			Key []byte `json:"key"`
		} `json:"kvs"`
		More bool `json:"more"`
	}
	in := map[string]any{"key": []byte(prefix), "range_end": rangeEnd([]byte(prefix)), "keys_only": true, "limit": maxKeys}
	if err := e.call(ctx, "/v3/kv/range", in, &out); err != nil {
		return nil, err
	}
	if out.More {
		return nil, fmt.Errorf("more than %d keys", maxKeys)
	}
	keys := make([]string, len(out.KVs))
	for i, kv := range out.KVs {
		keys[i] = string(kv.Key)
	}
	return keys, nil
}

// rangeEnd is the end of the range of the keys starting with prefix.
func rangeEnd(prefix []byte) []byte {
	end := bytes.Clone(prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return []byte{0}
}

type etcdLease struct {
	e  *etcdBackend
	id string // the lease's ID, an int64 the gateway writes as a string
}

// tryLock grants a lease of ttl, and puts the lock's key with it if the key
// has never been made, in one transaction; the lease is revoked when the
// key is there.
func (e *etcdBackend) tryLock(ctx context.Context, key string, ttl time.Duration) (lease, bool, error) {
	var grant struct {
		ID    string `json:"ID"`
		Error string `json:"error"`
	}
	if err := e.call(ctx, "/v3/lease/grant", map[string]any{"TTL": int64(ttl / time.Second)}, &grant); err != nil {
		return nil, false, err
	}
	if grant.ID == "" {
		return nil, false, fmt.Errorf("no lease granted: %s", grant.Error)
	}
	l := &etcdLease{e: e, id: grant.ID}
	txn := map[string]any{
		"compare": []any{map[string]any{"key": []byte(key), "target": "CREATE", "result": "EQUAL", "create_revision": "0"}},
		"success": []any{map[string]any{"request_put": map[string]any{"key": []byte(key), "value": []byte(grant.ID), "lease": grant.ID}}},
	}
	var out struct {
		Succeeded bool `json:"succeeded"`
	}
	if err := e.call(ctx, "/v3/kv/txn", txn, &out); err != nil {
		_ = l.release(ctx)
		return nil, false, err
	}
	if !out.Succeeded {
		_ = l.release(ctx)
		return nil, false, nil
	}
	return l, true, nil
}

func (l *etcdLease) renew(ctx context.Context) error {
	var out struct {
		Result struct {
			TTL string `json:"TTL"`
		} `json:"result"`
	}
	if err := l.e.call(ctx, "/v3/lease/keepalive", map[string]any{"ID": l.id}, &out); err != nil {
		return err
	}
	if out.Result.TTL == "" || out.Result.TTL == "0" {
		return errors.New("the lock was lost: its lease expired")
	}
	return nil
}

func (l *etcdLease) release(ctx context.Context) error {
	var out struct{}
	err := l.e.call(ctx, "/v3/lease/revoke", map[string]any{"ID": l.id}, &out)
	var se *etcdStatusError
	if errors.As(err, &se) && strings.Contains(err.Error(), "lease not found") {
		return nil // expired already, and its key with it
	}
	return err
}

func (e *etcdBackend) close() { e.hc.CloseIdleConnections() }
