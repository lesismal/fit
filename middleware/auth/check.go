package auth

import (
	stdhttp "net/http"
	"runtime"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/module"
)

// check is a password being checked off fib's workers.
type check struct {
	ba       *Basic
	c        *fibhttp.Context
	next     module.Handler
	account  *account // nil for a user basic_auth does not know
	user     string
	password string
	queued   time.Time
	// run is the request's Async of verify, which releases it once verify
	// has returned.
	run func()
	// busy says the pool had no room: verify answers 503 at once.
	busy bool
}

// verify checks the password, then lets the request through or refuses it.
// It runs on the pool, or on the worker for a check the pool had no room
// for.
func (k *check) verify() {
	defer k.ba.graph.Release()
	c := k.c
	switch {
	case k.busy || time.Since(k.queued) > maxWait:
		_ = c.WriteResponse(fibhttp.Response{
			StatusCode: stdhttp.StatusServiceUnavailable,
			Header:     stdhttp.Header{"Retry-After": {"1"}},
		})
		return
	case c.Err() != nil:
		return // the client has gone
	}
	// An unknown user's password is checked against another's hash all the
	// same, so that a refusal takes as long whether the user exists or not.
	hash := k.ba.decoy
	if k.account != nil {
		hash = k.account.hash
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(k.password)) != nil || k.account == nil {
		k.ba.refuse(c)
		return
	}
	mac := k.ba.mac(k.password)
	k.account.good.Store(&mac)
	k.ba.admit(c, k.user, k.next)
}

// The pool's bounds: how many checks may wait, and for how long. A check
// past either is answered 503 Service Unavailable, so that a flood of wrong
// passwords costs at most the pool's share of the CPU, and users whose
// password is remembered are let in meanwhile.
const (
	queueSize = 1024
	maxWait   = 5 * time.Second
)

// pool runs password checks: a quarter of the CPUs, at least one, so that
// fib's workers keep the rest.
type pool struct {
	once    sync.Once
	workers int
	jobs    chan *check
}

// checks is the pool every basic_auth shares.
var checks = &pool{}

// submit queues k, and reports false when the queue is full.
func (p *pool) submit(k *check) bool {
	p.once.Do(p.start)
	k.queued = time.Now()
	select {
	case p.jobs <- k:
		return true
	default:
		return false
	}
}

func (p *pool) start() {
	if p.workers == 0 {
		p.workers = max(1, runtime.GOMAXPROCS(0)/4)
	}
	p.jobs = make(chan *check, queueSize)
	for range p.workers {
		go p.work()
	}
}

func (p *pool) work() {
	for k := range p.jobs {
		p.do(k)
	}
}

// do runs k, and survives a handler that panics: the request is released
// by its Async all the same.
func (p *pool) do(k *check) {
	defer func() { _ = recover() }()
	k.run()
}
