package consul

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider/internal/kvpart"
	"github.com/lesismal/fit/provider/internal/watch"
)

// KV is the Consul KV provider: each key under its prefix holds a part, a
// JSON object of listeners, routes, upstreams and services, and it
// provides them together, in the order of their keys.
type KV struct {
	name string
	f    *watch.Follower
}

// NewKV returns a Consul KV provider named name, with the settings opts.
// logf, which may be nil, hears what the provider reports: keys left out
// and Consul failing.
func NewKV(name string, opts *config.ConsulKVProvider, logf func(format string, args ...any)) *KV {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	src := &kvSource{opts: opts, conn: conn{
		endpoint:   opts.URL(),
		datacenter: opts.Datacenter,
		tokenFile:  opts.TokenFile,
		files:      watch.TLSFiles{CA: opts.CAFile, Cert: opts.CertFile, Key: opts.KeyFile},
		timeout:    opts.Timeout(),
	}}
	return &KV{name: name, f: &watch.Follower{Name: name, Interval: opts.Refresh(), Logf: logf, Source: src}}
}

// Name returns the provider's name.
func (p *KV) Name() string { return p.name }

// Run follows the keys until ctx ends, calling update with what they
// provide: at once, after each change, and every refresh_interval whether
// or not it changed. When Consul cannot be reached, the last part stays,
// and Consul is tried again, from 1s to 30s apart.
func (p *KV) Run(ctx context.Context, update func([]byte)) { p.f.Run(ctx, update) }

type kvSource struct {
	conn
	opts *config.ConsulKVProvider
	c    *client // the watch's
}

func (s *kvSource) Open(context.Context) (func(), error) {
	c, err := s.conn.open()
	if err != nil {
		return nil, err
	}
	s.c = c
	return c.close, nil
}

func (s *kvSource) path() string { return "/v1/kv/" + escapePath(s.opts.KeyPrefix()) }

// Watch polls the keys under the prefix.
func (s *kvSource) Watch(ctx context.Context, ready, changed func()) error {
	return s.c.poll(ctx, s.path(), url.Values{"recurse": {"true"}}, ready, changed)
}

// Read reads the keys under the prefix and their values.
func (s *kvSource) Read(ctx context.Context) ([]byte, []string, error) {
	data, _, err := s.c.get(ctx, s.path(), url.Values{"recurse": {"true"}}, 0)
	if err != nil {
		return nil, nil, err
	}
	var kvs []struct {
		Key   string `json:"Key"`
		Value []byte `json:"Value"` // base64, as encoding/json reads []byte
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &kvs); err != nil {
			return nil, nil, fmt.Errorf("GET %s: %w", s.path(), err)
		}
	}
	entries := make([]kvpart.Entry, len(kvs))
	for i, kv := range kvs {
		entries[i] = kvpart.Entry{Key: kv.Key, Value: kv.Value}
	}
	part, problems := kvpart.Part(entries, s.opts.Keys())
	return part, problems, nil
}
