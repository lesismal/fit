package proxy

import (
	"bytes"
	"io"
	"math/rand/v2"
	stdhttp "net/http"
	"sync/atomic"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

// Split is one of the upstreams a proxy spreads its requests over.
type Split struct {
	Upstream string `json:"upstream" fit:"required" doc:"The name of the upstream."`
	Weight   int    `json:"weight" fit:"required" min:"1" max:"1000" doc:"Its share of the requests, against the others' weights."`
}

// Mirror sends a copy of some requests to a second upstream.
type Mirror struct {
	Upstream string   `json:"upstream" fit:"required" doc:"The name of the upstream the copies go to."`
	Percent  *float64 `json:"percent,omitempty" default:"100" min:"0" max:"100" doc:"The share of requests copied, chosen at random; all of them when left out."`
}

// maxMirrored bounds the copies under way at once for one proxy: past it,
// requests are not copied, so that a slow mirror costs memory and
// connections up to this and no more, and never slows the requests.
const maxMirrored = 256

// mirror is a Mirror compiled.
type mirror struct {
	pool     *upstream.Pool
	percent  float64
	inFlight atomic.Int64
}

// weighted is an upstream of a split, with the sum of the weights up to it.
type weighted struct {
	pool *upstream.Pool
	upTo int
}

// provisionSplit finds the upstreams of upstreams, failover and mirror.
func (p *Proxy) provisionSplit(ctx *module.Context) error {
	lookup := func(field, name string) (*upstream.Pool, error) {
		pool, ok := ctx.Upstream(name)
		if !ok {
			return nil, ctx.Errorf(field, "there is no upstream named %q", name)
		}
		return pool, nil
	}
	switch {
	case p.Upstream != "" && len(p.Upstreams) > 0:
		return ctx.Errorf("upstreams", "upstream and upstreams are each other's alternatives: set one")
	case p.Upstream == "" && len(p.Upstreams) == 0:
		return ctx.Errorf("upstream", "upstream or upstreams is needed")
	case p.Upstream != "":
		pool, err := lookup("upstream", p.Upstream)
		if err != nil {
			return err
		}
		p.pool = pool
	}
	total := 0
	for i, s := range p.Upstreams {
		pool, ok := ctx.Upstream(s.Upstream)
		if !ok {
			return ctx.Params.Field("upstreams").Index(i).Field("upstream").Errorf("there is no upstream named %q", s.Upstream)
		}
		total += s.Weight
		p.split = append(p.split, weighted{pool: pool, upTo: total})
	}
	if len(p.split) > 0 {
		p.pool = p.split[0].pool
	}
	if p.Failover != "" {
		pool, err := lookup("failover", p.Failover)
		if err != nil {
			return err
		}
		p.failover = pool
	}
	if m := p.Mirror; m != nil {
		pool, ok := ctx.Upstream(m.Upstream)
		if !ok {
			return ctx.Params.Field("mirror").Field("upstream").Errorf("there is no upstream named %q", m.Upstream)
		}
		p.mirror = &mirror{pool: pool, percent: 100}
		if m.Percent != nil {
			p.mirror.percent = *m.Percent
		}
	}
	return nil
}

// pickPool chooses the upstream a request goes to: one of upstreams by
// weight, or the one upstream; then the failover instead, if none of the
// chosen one's backends is in rotation and one of the failover's is.
func (p *Proxy) pickPool() *upstream.Pool {
	pool := p.pool
	if len(p.split) > 1 {
		n := rand.IntN(p.split[len(p.split)-1].upTo)
		for _, w := range p.split {
			if n < w.upTo {
				pool = w.pool
				break
			}
		}
	}
	if p.failover != nil && !pool.Available() && p.failover.Available() {
		pool = p.failover
	}
	return pool
}

// copyTo sends a copy of out, the request c serves as it goes upstream, to
// the mirror, if it is chosen, its body arrived whole, and the mirror has
// fewer than maxMirrored copies under way. The copy's answer is dropped.
// The copy has its own header, URL and body, since it may outlive c.
func (m *mirror) copyTo(c *fibhttp.Context, out *stdhttp.Request) {
	if c.RequestBody() != nil || m.percent < 100 && rand.Float64()*100 >= m.percent {
		return
	}
	if m.inFlight.Add(1) > maxMirrored {
		m.inFlight.Add(-1)
		return
	}
	u := *out.URL
	cp := &stdhttp.Request{
		Method: out.Method, URL: &u, Proto: out.Proto, ProtoMajor: out.ProtoMajor, ProtoMinor: out.ProtoMinor,
		Header: out.Header.Clone(), Host: out.Host,
	}
	if body := c.Body(); len(body) > 0 {
		data := bytes.Clone(body)
		cp.Body, cp.ContentLength = io.NopCloser(bytes.NewReader(data)), int64(len(data))
		cp.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	}
	m.pool.Do(cp, func(resp *fibhttp.ClientResponse, _ error) {
		if resp != nil {
			resp.Close()
		}
		m.inFlight.Add(-1)
	})
}
