// Package etcd is the etcd provider: each key under its prefix holds a
// part, a JSON object of listeners, routes, upstreams and services, and it
// provides them together, in the order of their keys.
//
// It speaks etcd's v3 API through the JSON gateway etcd serves beside gRPC
// on its client port, with net/http, and follows the keys with a watch; see
// docs/decisions/013-consul-and-etcd-over-http.md. It runs in the control
// plane, on goroutines of its own.
package etcd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/apiclient"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider/internal/kvpart"
	"github.com/lesismal/fit/provider/internal/watch"
)

// maxBodyBytes bounds an answer read from etcd, and a message of a watch.
var maxBodyBytes int64 = 32 << 20

// Provider is an etcd provider.
type Provider struct {
	name string
	f    *watch.Follower
}

// New returns an etcd provider named name, with the settings opts. log,
// which may be nil, hears what the provider reports: keys left out and
// etcd failing.
func New(name string, opts *config.EtcdProvider, log *logs.Logger) *Provider {
	return &Provider{name: name, f: &watch.Follower{Name: name, Interval: opts.Refresh(), Log: log, Source: &source{opts: opts}}}
}

// Name returns the provider's name.
func (p *Provider) Name() string { return p.name }

// Run follows the keys until ctx ends, calling update with what they
// provide: at once, after each change, and every refresh_interval whether
// or not it changed. When etcd cannot be reached, the last part stays, and
// the next endpoint is tried, from 1s to 30s apart.
func (p *Provider) Run(ctx context.Context, update func([]byte)) { p.f.Run(ctx, update) }

type source struct {
	opts  *config.EtcdProvider
	tries int // the watches started, to take the endpoints in turn

	// The watch's.
	hc       *http.Client
	endpoint string
	token    string
}

// Open makes a client for the next endpoint, and authenticates when a
// user is set.
func (s *source) Open(ctx context.Context) (func(), error) {
	eps := s.opts.URLs()
	s.endpoint = strings.TrimSuffix(eps[s.tries%len(eps)], "/")
	s.tries++
	hc, err := apiclient.Client(apiclient.TLSFiles{CA: s.opts.CAFile, Cert: s.opts.CertFile, Key: s.opts.KeyFile}, s.opts.Timeout(), 4)
	if err != nil {
		return nil, err
	}
	s.hc, s.token = hc, ""
	if s.opts.Username != "" {
		password, err := os.ReadFile(s.opts.PasswordFile)
		if err != nil {
			hc.CloseIdleConnections()
			return nil, err
		}
		var out struct {
			Token string `json:"token"`
		}
		in := map[string]string{"name": s.opts.Username, "password": strings.TrimRight(string(password), "\r\n")}
		if err := s.call(ctx, "/v3/auth/authenticate", in, &out); err != nil {
			hc.CloseIdleConnections()
			return nil, err
		}
		s.token = out.Token
	}
	return hc.CloseIdleConnections, nil
}

// post sends in to path, and returns the answer, which the caller reads
// and closes.
func (s *source) post(ctx context.Context, path string, in any) (*http.Response, error) {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", s.token)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiclient.StatusError(resp, "POST "+path+" at "+s.endpoint)
	}
	return resp, nil
}

// call sends in to path and reads the answer into out, within the request
// timeout.
func (s *source) call(ctx context.Context, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout())
	defer cancel()
	resp, err := s.post(ctx, path, in)
	if err != nil {
		return err
	}
	data, err := apiclient.ReadBody(resp, maxBodyBytes, "POST "+path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	return nil
}

// keyRange is the range of the keys under the prefix; []byte is base64 in
// JSON, as the gateway has it.
type keyRange struct {
	Key      []byte `json:"key"`
	RangeEnd []byte `json:"range_end"`
}

func (s *source) keyRange() keyRange {
	p := []byte(s.opts.KeyPrefix())
	return keyRange{Key: p, RangeEnd: rangeEnd(p)}
}

// rangeEnd is the end of the range of the keys starting with prefix: the
// prefix with its last byte below 0xff incremented and what follows it
// dropped, or "\x00", every key, when it has none.
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

// Watch watches the keys under the prefix: the watch is in place once etcd
// says it is created, which must come within the request timeout.
func (s *source) Watch(ctx context.Context, ready, changed func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	created, late := make(chan struct{}), make(chan struct{})
	go func() {
		t := time.NewTimer(s.opts.Timeout())
		defer t.Stop()
		select {
		case <-t.C:
			close(late)
			cancel()
		case <-created:
		case <-ctx.Done():
		}
	}()
	err := s.watch(ctx, func() {
		close(created)
		ready()
	}, changed)
	select {
	case <-late:
		return fmt.Errorf("POST /v3/watch at %s: the watch was not created within %s", s.endpoint, s.opts.Timeout())
	default:
		return err
	}
}

func (s *source) watch(ctx context.Context, ready, changed func()) error {
	resp, err := s.post(ctx, "/v3/watch", map[string]any{"create_request": s.keyRange()})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readWatch(resp.Body, ready, changed)
}

// readWatch reads a watch's messages, one JSON object a line, calling
// ready at the first, which says the watch is created, and changed at each
// with events, until the stream ends or says the watch is canceled.
func readWatch(r io.Reader, ready, changed func()) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), int(maxBodyBytes))
	started := false
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var m struct {
			Result *struct {
				Created         bool              `json:"created"`
				Canceled        bool              `json:"canceled"`
				CancelReason    string            `json:"cancel_reason"`
				CompactRevision string            `json:"compact_revision"`
				Events          []json.RawMessage `json:"events"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return fmt.Errorf("a watch message that is not JSON: %w", err)
		}
		switch r := m.Result; {
		case m.Error != nil:
			return fmt.Errorf("the watch failed: %s", m.Error.Message)
		case r == nil:
			continue
		case r.Canceled:
			reason := r.CancelReason
			if r.CompactRevision != "" && r.CompactRevision != "0" {
				reason += fmt.Sprintf(" (compacted at revision %s)", r.CompactRevision)
			}
			return fmt.Errorf("etcd canceled the watch: %s", strings.TrimSpace(reason))
		case !started:
			if !r.Created {
				return errors.New("the watch's first message does not say it is created")
			}
			started = true
			ready()
		case len(r.Events) > 0:
			changed()
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("etcd ended the watch")
}

// Read reads the keys under the prefix and their values, at most max_keys
// of them.
func (s *source) Read(ctx context.Context) ([]byte, []string, error) {
	var out struct {
		KVs []struct {
			Key   []byte `json:"key"`
			Value []byte `json:"value"`
		} `json:"kvs"`
		More bool `json:"more"`
	}
	in := struct {
		keyRange
		Limit int `json:"limit"`
	}{s.keyRange(), s.opts.Keys()}
	if err := s.call(ctx, "/v3/kv/range", in, &out); err != nil {
		return nil, nil, err
	}
	entries := make([]kvpart.Entry, len(out.KVs))
	for i, kv := range out.KVs {
		entries[i] = kvpart.Entry{Key: string(kv.Key), Value: kv.Value}
	}
	part, problems := kvpart.Part(entries, s.opts.Keys())
	if out.More {
		problems = append(problems, fmt.Sprintf("the keys past the first %d under %s are left out: max_keys", s.opts.Keys(), s.opts.KeyPrefix()))
	}
	return part, problems, nil
}
