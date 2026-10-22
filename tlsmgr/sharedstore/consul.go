package sharedstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/apiclient"
)

// consulBackend keeps keys in Consul's KV store, and holds locks with
// sessions whose TTL is the lock's, deleting the lock's key when they end.
type consulBackend struct {
	opts  *config.ConsulStorage
	hc    *http.Client
	token string
}

func newConsul(c *config.ConsulStorage, timeout time.Duration) (*consulBackend, error) {
	b := &consulBackend{opts: c}
	if c.TokenFile != "" {
		data, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return nil, err
		}
		b.token = strings.TrimSpace(string(data))
	}
	hc, err := apiclient.Client(apiclient.TLSFiles{CA: c.CAFile, Cert: c.CertFile, Key: c.KeyFile}, timeout, 4)
	if err != nil {
		return nil, err
	}
	b.hc = hc
	return b, nil
}

// do sends method to path with query and body, and returns the answer's
// status and body; a 404 is not an error.
func (b *consulBackend) do(ctx context.Context, method, path string, query url.Values, body []byte) (int, []byte, error) {
	if query == nil {
		query = url.Values{}
	}
	if b.opts.Datacenter != "" {
		query.Set("dc", b.opts.Datacenter)
	}
	u := strings.TrimSuffix(b.opts.URL(), "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, nil, err
	}
	if b.token != "" {
		req.Header.Set("X-Consul-Token", b.token)
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	what := method + " " + path
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		resp.Body.Close()
		return http.StatusNotFound, nil, nil
	default:
		return 0, nil, apiclient.StatusError(resp, what)
	}
	data, err := apiclient.ReadBody(resp, MaxValueBytes, what)
	return http.StatusOK, data, err
}

// kvPath is the API's path of key, escaped part by part.
func kvPath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/v1/kv/" + strings.Join(parts, "/")
}

func (b *consulBackend) get(ctx context.Context, key string) ([]byte, error) {
	status, data, err := b.do(ctx, http.MethodGet, kvPath(key), url.Values{"raw": {""}}, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fs.ErrNotExist
	}
	return data, nil
}

func (b *consulBackend) put(ctx context.Context, key string, value []byte) error {
	_, data, err := b.do(ctx, http.MethodPut, kvPath(key), nil, value)
	if err == nil && strings.TrimSpace(string(data)) != "true" {
		err = fmt.Errorf("PUT %s: Consul answered %q", key, data)
	}
	return err
}

func (b *consulBackend) del(ctx context.Context, key string) error {
	_, _, err := b.do(ctx, http.MethodDelete, kvPath(key), nil, nil)
	return err
}

func (b *consulBackend) list(ctx context.Context, prefix string) ([]string, error) {
	status, data, err := b.do(ctx, http.MethodGet, kvPath(prefix), url.Values{"keys": {""}}, nil)
	if err != nil || status == http.StatusNotFound {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("GET %s?keys: %w", prefix, err)
	}
	if len(keys) > maxKeys {
		return nil, fmt.Errorf("more than %d keys", maxKeys)
	}
	return keys, nil
}

type consulLease struct {
	b       *consulBackend
	session string
}

// tryLock makes a session of ttl and acquires the lock's key with it; the
// session is destroyed when another holds the key.
func (b *consulBackend) tryLock(ctx context.Context, key string, ttl time.Duration) (lease, bool, error) {
	in, _ := json.Marshal(map[string]string{"Name": "fit " + key, "TTL": ttl.String(), "Behavior": "delete", "LockDelay": "0s"})
	_, data, err := b.do(ctx, http.MethodPut, "/v1/session/create", nil, in)
	if err != nil {
		return nil, false, err
	}
	var out struct{ ID string }
	if err := json.Unmarshal(data, &out); err != nil || out.ID == "" {
		return nil, false, fmt.Errorf("PUT /v1/session/create: no session in %q", data)
	}
	l := &consulLease{b: b, session: out.ID}
	_, data, err = b.do(ctx, http.MethodPut, kvPath(key), url.Values{"acquire": {out.ID}}, []byte(out.ID))
	if err != nil {
		_ = l.release(ctx)
		return nil, false, err
	}
	if strings.TrimSpace(string(data)) != "true" {
		_ = l.release(ctx)
		return nil, false, nil
	}
	return l, true, nil
}

func (l *consulLease) renew(ctx context.Context) error {
	status, _, err := l.b.do(ctx, http.MethodPut, "/v1/session/renew/"+url.PathEscape(l.session), nil, nil)
	if err == nil && status == http.StatusNotFound {
		err = fmt.Errorf("the lock was lost: its session %s expired", l.session)
	}
	return err
}

// release destroys the session, which deletes the lock's key.
func (l *consulLease) release(ctx context.Context) error {
	_, _, err := l.b.do(ctx, http.MethodPut, "/v1/session/destroy/"+url.PathEscape(l.session), nil, nil)
	return err
}

func (b *consulBackend) close() { b.hc.CloseIdleConnections() }
