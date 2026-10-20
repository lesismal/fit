package tlsmgr

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
)

// Why a handshake gets no certificate on demand.
var (
	ErrOnDemandRefused     = errors.New("tlsmgr: on-demand TLS: the ask URL refused the name")
	ErrOnDemandRateLimited = errors.New("tlsmgr: on-demand TLS: too many certificates obtained lately")
	ErrOnDemandTooMany     = errors.New("tlsmgr: on-demand TLS: as many names as max_names have certificates")
	ErrOnDemandTimeout     = errors.New("tlsmgr: on-demand TLS: the certificate is still being obtained")
	errOnDemandBadName     = errors.New("tlsmgr: on-demand TLS: not a host name a certificate can be obtained for")
	errOnDemandOff         = errors.New("tlsmgr: on-demand TLS is not configured")
)

// refusedFor is how long a name the ask URL refused is refused without
// asking again, and maxRefused how many such names are remembered.
const (
	refusedFor = time.Minute
	maxRefused = 10000
)

// onDemand is what ACME keeps to take on names during handshakes.
type onDemand struct {
	ask      *url.URL
	http     *http.Client
	limit    int
	every    time.Duration
	maxNames int
	wait     time.Duration

	mu sync.Mutex
	// taken are when the last names were taken on, at most limit of them,
	// oldest first: another may be taken on once the oldest is every old.
	taken []time.Time
	// names counts the names taken on that ACME keeps.
	names int
	// refused holds the names the ask URL refused, until when.
	refused map[string]time.Time
	// waits holds, for each name being taken on, what its handshakes wait
	// for.
	waits map[string]*onDemandWait
}

// onDemandWait is a certificate being obtained on demand, which handshakes
// wait for.
type onDemandWait struct {
	done chan struct{}
	cert *tls.Certificate
	err  error
}

func newOnDemand(c *config.OnDemand) *onDemand {
	ask, _ := url.Parse(c.Ask) // checked by config.Validate
	return &onDemand{
		ask:      ask,
		http:     &http.Client{Timeout: c.Wait()},
		limit:    c.Limit(),
		every:    c.Every(),
		maxNames: c.Names(),
		wait:     c.Wait(),
		refused:  map[string]time.Time{},
		waits:    map[string]*onDemandWait{},
	}
}

// OnDemand returns a certificate for name, obtained now if a has none: if
// the ask URL allows the name, and neither the rate nor max_names is
// reached. It may block, for the ask and for the certificate, up to
// on_demand's timeout; a handshake calls it from fib's handshake pool, never
// from a worker. Handshakes for the same name wait for one certificate, and
// one that gives up leaves it being obtained.
func (a *ACME) OnDemand(name string) (*tls.Certificate, error) {
	od := a.onDemand
	if od == nil {
		return nil, errOnDemandOff
	}
	name = normalizeName(name)
	if !onDemandName(name) {
		return nil, errOnDemandBadName
	}
	if c := a.Certificate(name); c != nil {
		return c, nil
	}
	now := a.now()
	od.mu.Lock()
	if w := od.waits[name]; w != nil {
		od.mu.Unlock()
		return od.await(w)
	}
	if until, ok := od.refused[name]; ok {
		if now.Before(until) {
			od.mu.Unlock()
			return nil, ErrOnDemandRefused
		}
		delete(od.refused, name)
	}
	w := &onDemandWait{done: make(chan struct{})}
	od.waits[name] = w
	od.mu.Unlock()

	if err := a.allow(name); err != nil {
		od.finish(name, nil, err)
		return nil, err
	}
	a.mu.Lock()
	if a.names[name] == nil {
		a.names[name] = &managed{name: name, due: a.now(), onDemand: true}
	} else {
		// Listed meanwhile, or taken on by a reload: not counted on
		// demand, and worked on as it is.
		od.drop()
	}
	a.mu.Unlock()
	a.poke()
	return od.await(w)
}

// allow asks whether name may have a certificate, and counts it against
// the rate and max_names if it may. Both are looked at first too, so that a
// name past them is not asked about.
func (a *ACME) allow(name string) error {
	od := a.onDemand
	if err := od.room(a.now(), false); err != nil {
		return err
	}
	if err := od.askFor(name); err != nil {
		if errors.Is(err, ErrOnDemandRefused) {
			od.mu.Lock()
			if len(od.refused) >= maxRefused {
				clear(od.refused)
			}
			od.refused[name] = a.now().Add(refusedFor)
			od.mu.Unlock()
		}
		a.logf("acme: %s: on demand: %v", name, err)
		return err
	}
	return od.room(a.now(), true)
}

// room reports whether another name may be taken on now, and counts it with
// take.
func (od *onDemand) room(now time.Time, take bool) error {
	od.mu.Lock()
	defer od.mu.Unlock()
	if od.names >= od.maxNames {
		return ErrOnDemandTooMany
	}
	full := len(od.taken) >= od.limit
	if full && now.Sub(od.taken[0]) < od.every {
		return ErrOnDemandRateLimited
	}
	if take {
		if full {
			od.taken = append(od.taken[:0], od.taken[1:]...)
		}
		od.taken = append(od.taken, now)
		od.names++
	}
	return nil
}

// askFor asks the ask URL about name: ErrOnDemandRefused for any answer
// but 2xx, and another error when there is no answer.
func (od *onDemand) askFor(name string) error {
	u := *od.ask
	q := u.Query()
	q.Set("domain", name)
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), od.wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := od.http.Do(req)
	if err != nil {
		return fmt.Errorf("asking %s: %w", od.ask.Redacted(), err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w (%s)", ErrOnDemandRefused, resp.Status)
	}
	return nil
}

// await waits for w, up to the timeout.
func (od *onDemand) await(w *onDemandWait) (*tls.Certificate, error) {
	t := time.NewTimer(od.wait)
	defer t.Stop()
	select {
	case <-w.done:
		return w.cert, w.err
	case <-t.C:
		return nil, ErrOnDemandTimeout
	}
}

// finish tells the handshakes waiting for name what came of it. od may be
// nil, for an ACME without on-demand TLS.
func (od *onDemand) finish(name string, cert *tls.Certificate, err error) {
	if od == nil {
		return
	}
	od.mu.Lock()
	w := od.waits[name]
	delete(od.waits, name)
	od.mu.Unlock()
	if w != nil {
		if cert == nil && err == nil {
			err = ErrOnDemandTimeout
		}
		w.cert, w.err = cert, err
		close(w.done)
	}
}

// drop counts a name taken on as no longer kept on demand. od may be nil.
func (od *onDemand) drop() {
	if od == nil {
		return
	}
	od.mu.Lock()
	od.names--
	od.mu.Unlock()
}

// onDemandName reports whether name is one a certificate may be obtained
// for on demand: a host name of letters, digits and hyphens, in two labels
// or more, not an IP address and not a wildcard.
func onDemandName(name string) bool {
	if len(name) == 0 || len(name) > 253 || net.ParseIP(name) != nil || !strings.Contains(name, ".") {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
