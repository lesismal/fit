package bittorrent

import (
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// Adding a torrent by the URL of its .torrent file: the file is downloaded
// with fib's asynchronous HTTP client on the role's engine, whose callback
// only hands the answer over; the API call waits for it on its own
// goroutine, never a fib worker.

const (
	// MaxURLTorrentBytes bounds a .torrent file downloaded by URL, which
	// is held whole in memory until added.
	MaxURLTorrentBytes = 32 << 20
	// MaxURLFetches bounds the downloads by URL under way at once: past
	// it AddURL is refused with ErrBusy, so that at most
	// MaxURLFetches*MaxURLTorrentBytes are held.
	MaxURLFetches = 4
	// maxRedirects bounds the redirects a download by URL follows.
	maxRedirects = 5
	// maxURLBytes bounds a URL given to AddURL.
	maxURLBytes = 8 << 10
)

var (
	// ErrBusy is an AddURL while MaxURLFetches downloads are under way.
	ErrBusy = errors.New("bittorrent: too many .torrent files are being downloaded")
	// ErrFetch is a .torrent file that could not be downloaded.
	ErrFetch = errors.New("bittorrent: the .torrent file could not be downloaded")
)

// httpClientLocked returns the role's HTTP client, made on first use.
// r.mu is held.
func (r *Role) httpClientLocked() *fibhttp.Client {
	if r.httpc == nil {
		cc := fibhttp.DefaultClientConfig()
		cc.Timeout = r.cfg.URLFetchTimeout()
		cc.DialTimeout = min(cc.Timeout, 10*time.Second)
		cc.MaxResponseBodyBytes = MaxURLTorrentBytes
		cc.MaxResponseHeaderBytes = 64 << 10
		cc.MaxConnsPerHost, cc.MaxPendingPerHost, cc.MaxIdleConnsPerHost = 2, MaxURLFetches, 1
		cc.IdleConnTimeout = 30 * time.Second
		r.httpc, r.httpTimeout = fibhttp.NewClient(r.key, cc), cc.Timeout
	}
	return r.httpc
}

// AddURL adds the torrent of the .torrent file at rawURL, http or https,
// following at most 5 redirects, one to a magnet link included, within
// url_timeout, as Add adds req's (whose Magnet and Torrent must be empty).
// It waits for the download: it must not be called on a fib worker.
func (r *Role) AddURL(rawURL string, req AddRequest) (Status, error) {
	if req.Magnet != "" || len(req.Torrent) > 0 {
		return Status{}, fmt.Errorf("%w: a URL with a magnet link or a .torrent file", ErrBadRequest)
	}
	if len(rawURL) > maxURLBytes {
		return Status{}, fmt.Errorf("%w: a URL longer than %d bytes", ErrBadRequest, maxURLBytes)
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Status{}, fmt.Errorf("%w: %q is not an http or https URL", ErrBadRequest, rawURL)
	}
	select {
	case r.fetchSem <- struct{}{}:
	default:
		return Status{}, ErrBusy
	}
	defer func() { <-r.fetchSem }()
	r.mu.Lock()
	if r.client == nil || r.stopped {
		r.mu.Unlock()
		return Status{}, ErrNotRunning
	}
	c := r.httpClientLocked()
	timeout := r.cfg.URLFetchTimeout()
	r.mu.Unlock()

	deadline := time.Now().Add(timeout)
	for range maxRedirects + 1 {
		body, loc, err := fetch(c, u, time.Until(deadline))
		if err != nil {
			return Status{}, err
		}
		if loc == nil {
			req.Torrent = body
			s, err := r.Add(req)
			if errors.Is(err, ErrBadRequest) {
				err = fmt.Errorf("%w (from %s)", err, u.Redacted())
			}
			return s, err
		}
		if loc.Scheme == "magnet" {
			req.Magnet = loc.String()
			return r.Add(req)
		}
		if loc.Scheme != "http" && loc.Scheme != "https" {
			return Status{}, fmt.Errorf("%w: %s redirects to %q", ErrFetch, u.Redacted(), loc.Scheme+":")
		}
		u = loc
	}
	return Status{}, fmt.Errorf("%w: more than %d redirects", ErrFetch, maxRedirects)
}

// fetch downloads u: its body, or where it redirects to.
func fetch(c *fibhttp.Client, u *url.URL, timeout time.Duration) ([]byte, *url.URL, error) {
	if timeout <= 0 {
		return nil, nil, fmt.Errorf("%w: url_timeout passed", ErrFetch)
	}
	req, err := stdhttp.NewRequest(stdhttp.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	req.Header.Set("Accept", "application/x-bittorrent, */*")
	type answer struct {
		status int
		loc    string
		body   []byte
		err    error
	}
	ch := make(chan answer, 1)
	// The callback runs on a fib worker: it only copies the answer out.
	call := c.Start(req, func(resp *fibhttp.ClientResponse, err error) {
		if err != nil {
			ch <- answer{err: err}
			return
		}
		defer resp.Close()
		a := answer{status: resp.StatusCode, loc: resp.Header.Get("Location")}
		if a.status == stdhttp.StatusOK {
			body, ok := resp.BodyBytes()
			if !ok {
				a.err = errors.New("the body did not arrive whole")
			} else {
				a.body = append([]byte(nil), body...)
			}
		}
		ch <- a
	})
	// The client's Timeout ends the request; this only guards against a
	// callback that never comes.
	t := time.NewTimer(timeout + 5*time.Second)
	defer t.Stop()
	var a answer
	select {
	case a = <-ch:
	case <-t.C:
		call.Cancel(errors.New("url_timeout passed"))
		return nil, nil, fmt.Errorf("%w: %s: url_timeout passed", ErrFetch, u.Redacted())
	}
	switch {
	case a.err != nil:
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrFetch, u.Redacted(), a.err)
	case a.status == stdhttp.StatusOK:
		return a.body, nil, nil
	case a.status >= 300 && a.status < 400 && a.loc != "":
		loc, err := u.Parse(a.loc)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s redirects to a bad location: %v", ErrFetch, u.Redacted(), err)
		}
		return nil, loc, nil
	}
	return nil, nil, fmt.Errorf("%w: %s answered %d", ErrFetch, u.Redacted(), a.status)
}
