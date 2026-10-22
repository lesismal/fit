// Package consul is the Consul providers: one turns the services of a
// Consul catalog and their tags into upstreams, routes and services, the
// other reads parts from keys in Consul's KV store.
//
// Both speak Consul's HTTP API with net/http, and follow it with blocking
// queries; see docs/decisions/013-consul-and-etcd-over-http.md. They run in
// the control plane, on goroutines of their own.
package consul

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/provider/internal/watch"
)

// Bounds and timings, variables for tests to shorten.
var (
	// maxBodyBytes bounds an answer read from Consul.
	maxBodyBytes int64 = 32 << 20
	// blockWait is how long a blocking query waits for a change; Consul
	// adds up to a sixteenth of it.
	blockWait = 5 * time.Minute
	// minPollGap is the least time between the starts of two queries of
	// one poll, so that an index that changes all the time does not make
	// it spin.
	minPollGap = time.Second
)

// conn is what a provider reaches Consul with: where it is, and how.
type conn struct {
	endpoint   string
	datacenter string
	tokenFile  string
	files      watch.TLSFiles
	timeout    time.Duration
}

// client calls Consul's API, for one watch and the reads that go with it.
type client struct {
	conn
	hc    *http.Client
	token string
}

// open makes a client, reading the token and the TLS files.
func (c conn) open() (*client, error) {
	cl := &client{conn: c}
	if c.tokenFile != "" {
		data, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return nil, err
		}
		cl.token = strings.TrimSpace(string(data))
	}
	hc, err := watch.Client(c.files, c.timeout, 4)
	if err != nil {
		return nil, err
	}
	cl.hc = hc
	return cl, nil
}

func (c *client) close() { c.hc.CloseIdleConnections() }

// get asks Consul for path with query, and returns its answer and its
// index. With index above 0 it is a blocking query, which waits for an
// index past it. A 404, as for keys that are not there, is an empty
// answer.
func (c *client) get(ctx context.Context, path string, query url.Values, index uint64) ([]byte, uint64, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	if c.datacenter != "" {
		q.Set("dc", c.datacenter)
	}
	timeout := c.timeout
	if index > 0 {
		q.Set("index", strconv.FormatUint(index, 10))
		q.Set("wait", blockWait.String())
		timeout += blockWait + blockWait/16
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := c.endpoint + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	if c.token != "" {
		req.Header.Set("X-Consul-Token", c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	what := "GET " + path
	var next uint64
	if h := resp.Header.Get("X-Consul-Index"); h != "" {
		if next, err = strconv.ParseUint(h, 10, 64); err != nil {
			resp.Body.Close()
			return nil, 0, fmt.Errorf("%s: X-Consul-Index %q is not a number", what, h)
		}
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, next, nil
	default:
		return nil, 0, watch.StatusError(resp, what)
	}
	data, err := watch.ReadBody(resp, maxBodyBytes, what)
	return data, next, err
}

// poll follows path with blocking queries until ctx ends or a query fails,
// calling ready after the first answer and changed at each new index after
// it, as Consul's guidance on blocking queries has it: an index that went
// back starts over from 0, and one below 1 is taken as 1.
func (c *client) poll(ctx context.Context, path string, query url.Values, ready, changed func()) error {
	var index uint64
	var last time.Time
	started := false
	for {
		if wait := minPollGap - time.Since(last); !last.IsZero() && wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		last = time.Now()
		_, next, err := c.get(ctx, path, query, index)
		if err != nil {
			return err
		}
		switch {
		case !started:
			started = true
			ready()
		case next != index:
			changed()
		}
		switch {
		case next < index:
			index = 0
		case next < 1:
			index = 1
		default:
			index = next
		}
	}
}

// escapePath escapes a key's path, segment by segment.
func escapePath(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
