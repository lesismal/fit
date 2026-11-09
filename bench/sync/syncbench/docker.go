package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// dockerAPI is the little of the Docker Engine API measure uses: pausing
// and unpausing a mover's containers, and their stats.
type dockerAPI struct {
	client *http.Client
	base   string
}

// newDocker reaches the API at endpoint: unix:///path or http://host:port.
// Version 1.44 (Docker 25) is the oldest Docker 29 still takes.
func newDocker(endpoint string, timeout time.Duration) (*dockerAPI, error) {
	client, base, err := httpClient(endpoint, timeout)
	if err != nil {
		return nil, fmt.Errorf("-docker: %w", err)
	}
	return &dockerAPI{client: client, base: base + "/v1.44"}, nil
}

// httpClient is a client of the HTTP server at endpoint, unix:///path or
// http://host:port, and the base its URLs start with.
func httpClient(endpoint string, timeout time.Duration) (*http.Client, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, "", err
	}
	switch u.Scheme {
	case "unix":
		path := u.Path
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}
		return &http.Client{Transport: tr, Timeout: timeout}, "http://localhost", nil
	case "http":
		return &http.Client{Timeout: timeout}, strings.TrimRight(endpoint, "/"), nil
	}
	return nil, "", fmt.Errorf("%q: unix:///path or http://host:port", endpoint)
}

func (d *dockerAPI) call(ctx context.Context, method, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("docker %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (d *dockerAPI) start(ctx context.Context, name string) error {
	_, err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start")
	return err
}

func (d *dockerAPI) pause(ctx context.Context, name string) error {
	_, err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/pause")
	return err
}

func (d *dockerAPI) unpause(ctx context.Context, name string) error {
	_, err := d.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/unpause")
	return err
}

// usage is a container's CPU time so far and its anonymous memory now:
// cgroup v2's anon, or v1's rss.
type usage struct {
	cpu  time.Duration
	anon int64
}

func (d *dockerAPI) stats(ctx context.Context, name string) (usage, error) {
	body, err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/stats?stream=false&one-shot=true")
	if err != nil {
		return usage{}, err
	}
	var s struct {
		CPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
		} `json:"cpu_stats"`
		Memory struct {
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return usage{}, fmt.Errorf("docker stats %s: %v", name, err)
	}
	anon, ok := s.Memory.Stats["anon"]
	if !ok {
		anon = s.Memory.Stats["rss"]
	}
	return usage{cpu: time.Duration(s.CPU.Usage.Total), anon: int64(anon)}, nil
}

// sample sums the stats of every container.
func (d *dockerAPI) sample(ctx context.Context, names []string) (usage, error) {
	var u usage
	for _, n := range names {
		s, err := d.stats(ctx, n)
		if err != nil {
			return u, err
		}
		u.cpu += s.cpu
		u.anon += s.anon
	}
	return u, nil
}
