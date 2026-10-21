package proxy

import (
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"slices"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/upstream"
)

// Retry is when a proxy sends a request again.
type Retry struct {
	Attempts           int      `json:"attempts,omitempty" default:"2" min:"1" max:"10" doc:"How many times a request may be sent again after the first; 2 when left out."`
	Statuses           []int    `json:"statuses,omitempty" doc:"Statuses of the upstream's, 5xx ones, that have a request of an idempotent method sent again, as 502, 503 and 504; none when left out, so that only requests that got no response are."`
	BudgetRatio        *float64 `json:"budget_ratio,omitempty" default:"0.2" min:"0" max:"1" doc:"Retries may add at most this share of the requests the proxy takes, so that retries cannot multiply the load on an upstream already failing; 0.2 when left out."`
	BudgetMinPerSecond *float64 `json:"budget_min_per_second,omitempty" default:"10" min:"0" doc:"Retries allowed each second beyond budget_ratio, so that a proxy taking few requests may still retry; 10 when left out."`
}

// retryPolicy is a Retry compiled, with its budget.
type retryPolicy struct {
	attempts int
	statuses []int
	budget   retryBudget
}

func newRetryPolicy(r *Retry) *retryPolicy {
	p := &retryPolicy{attempts: r.Attempts, statuses: r.Statuses}
	if p.attempts == 0 {
		p.attempts = 2
	}
	ratio, perSecond := 0.2, 10.0
	if r.BudgetRatio != nil {
		ratio = *r.BudgetRatio
	}
	if r.BudgetMinPerSecond != nil {
		perSecond = *r.BudgetMinPerSecond
	}
	// It starts with a second's allowance, so that the first requests may
	// be retried too.
	p.budget = retryBudget{tokens: perSecond, ratio: ratio, perSecond: perSecond, max: max(10*perSecond, 10), last: time.Now()}
	return p
}

// retryBudget bounds retries, as Finagle's and Linkerd's budgets do: each
// request adds ratio of a retry to it, it gains perSecond retries each second,
// and it holds at most max; a retry takes one, and none is made without.
type retryBudget struct {
	mu        sync.Mutex
	tokens    float64
	last      time.Time
	ratio     float64
	perSecond float64
	max       float64
}

// deposit counts a request.
func (b *retryBudget) deposit() {
	b.mu.Lock()
	b.tokens = min(b.tokens+b.ratio, b.max)
	b.mu.Unlock()
}

// withdraw takes a retry, and reports false when there is none.
func (b *retryBudget) withdraw() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.tokens+now.Sub(b.last).Seconds()*b.perSecond, b.max)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// idempotent reports whether a request of method may be sent twice without
// the server acting on it twice, as RFC 9110 says.
func idempotent(method string) bool {
	switch method {
	case stdhttp.MethodGet, stdhttp.MethodHead, stdhttp.MethodOptions, stdhttp.MethodTrace,
		stdhttp.MethodPut, stdhttp.MethodDelete:
		return true
	}
	return false
}

// notSent reports whether err says the request never left: its connection
// could not be made, or the backend had max_pending requests waiting already.
func notSent(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial" || errors.Is(err, upstream.ErrBusy)
}

// retry sends the request again, to the backend picked next, if the policy
// allows it for the response or error the last attempt got, and reports
// whether it did. It runs in the upstream client's callback, before anything
// was written to the client.
func (x *exchange) retry(resp *fibhttp.ClientResponse, err error) bool {
	pol := x.policy
	if pol == nil || x.attempt >= pol.attempts || x.base.Body != nil && x.base.GetBody == nil || x.causeErr() != nil {
		return false
	}
	var again bool
	switch {
	case err != nil:
		again = notSent(err) || idempotent(x.base.Method) && !errors.Is(err, upstream.ErrClosed)
	default:
		again = idempotent(x.base.Method) && slices.Contains(pol.statuses, resp.StatusCode)
	}
	if !again {
		return false
	}
	var body io.ReadCloser
	if x.base.GetBody != nil {
		var gerr error
		if body, gerr = x.base.GetBody(); gerr != nil {
			return false
		}
	}
	if !pol.budget.withdraw() {
		return false
	}
	if resp != nil {
		resp.Close() // a body still arriving is not wanted
	}
	x.pool.Done(x.backend, err != nil && !errors.Is(err, upstream.ErrBusy))
	x.attempt++
	x.backend = x.pool.Pick(x.c.Request)
	x.setCookie = x.pool.SetCookie(x.c.Request, x.backend)
	if body != nil {
		x.base.Body = body
	}
	x.start = time.Now()
	x.send(x.out)
	return true
}
