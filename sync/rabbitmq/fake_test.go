package syncrabbitmq

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

// fake is a broker as the connectors see it, behind the client interface.
type fake struct {
	mu      sync.Mutex
	dialErr error
	dials   int
	conns   []*fakeClient
	// hooks set on each client made.
	setup func(c *fakeClient)
}

type fakeAck struct {
	tag      uint64
	multiple bool
}

type fakePub struct {
	exchange, key string
	mandatory     bool
	props         amqpwire.Properties
	body          string
}

type fakeConsume struct {
	queue string
	args  amqpwire.Table
}

type fakeClient struct {
	mu         sync.Mutex
	deliveries chan *amqpwire.Delivery
	done       chan struct{}
	err        error
	qos        uint16
	consumes   []fakeConsume
	acks       []fakeAck
	pubs       []fakePub
	seq        uint64
	confirm    bool
	returns    []amqpwire.Return
	closed     bool

	// failAt is how many messages are published before publishErr.
	failAt int

	qosErr, consumeErr, declareErr, confirmErr, publishErr, waitErr error
	// nackKeys and returnKeys are routing keys whose messages the broker
	// nacks, or returns.
	nackKeys, returnKeys map[string]bool
	// ackHook, when set, is called for each ack.
	ackHook func(tag uint64, multiple bool) error
	// ready is each queue's ready messages, as queue.declare counts them,
	// and declares how many times it was asked.
	ready    map[string]uint32
	declares int
}

func (f *fake) dial(ctx context.Context, cfg amqpwire.Config) (client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	c := &fakeClient{deliveries: make(chan *amqpwire.Delivery, 100), done: make(chan struct{}), nackKeys: map[string]bool{}, returnKeys: map[string]bool{}}
	if f.setup != nil {
		f.setup(c)
	}
	f.conns = append(f.conns, c)
	return c, nil
}

func (f *fake) last() *fakeClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[len(f.conns)-1]
}

func (c *fakeClient) Qos(ctx context.Context, n uint16) error { c.qos = n; return c.qosErr }

func (c *fakeClient) Consume(ctx context.Context, q, tag string, args amqpwire.Table) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consumes = append(c.consumes, fakeConsume{q, args})
	return "ctag-" + q, c.consumeErr
}

func (c *fakeClient) Deliveries() <-chan *amqpwire.Delivery { return c.deliveries }

func (c *fakeClient) Ack(tag uint64, multiple bool) error {
	c.mu.Lock()
	hook := c.ackHook
	c.acks = append(c.acks, fakeAck{tag, multiple})
	c.mu.Unlock()
	if hook != nil {
		return hook(tag, multiple)
	}
	return nil
}

func (c *fakeClient) QueueDeclare(ctx context.Context, name string, passive, durable bool, args amqpwire.Table) (amqpwire.QueueInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.declares++
	return amqpwire.QueueInfo{Name: name, Messages: c.ready[name]}, c.declareErr
}

func (c *fakeClient) ConfirmSelect(ctx context.Context) error { c.confirm = true; return c.confirmErr }

func (c *fakeClient) Publish(ctx context.Context, exchange, key string, mandatory bool, p amqpwire.Properties, body []byte) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.publishErr != nil && len(c.pubs) >= c.failAt {
		return 0, c.publishErr
	}
	c.seq++
	c.pubs = append(c.pubs, fakePub{exchange, key, mandatory, p, string(body)})
	if mandatory && c.returnKeys[key] {
		c.returns = append(c.returns, amqpwire.Return{Code: 312, Text: "NO_ROUTE", Exchange: exchange, RoutingKey: key, Properties: p})
	}
	return c.seq, nil
}

func (c *fakeClient) WaitConfirms(ctx context.Context, from, to uint64) ([]uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waitErr != nil {
		return nil, c.waitErr
	}
	var n []uint64
	for s := from; s <= to; s++ {
		if c.nackKeys[c.pubs[s-1].key] {
			n = append(n, s)
		}
	}
	return n, nil
}

func (c *fakeClient) TakeReturns() []amqpwire.Return {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.returns
	c.returns = nil
	return r
}

func (c *fakeClient) Done() <-chan struct{} { return c.done }

func (c *fakeClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// fail ends the connection with err.
func (c *fakeClient) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
		close(c.deliveries)
	}
}

func (c *fakeClient) Close() error {
	c.fail(amqpwire.ErrClosed)
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeClient) ackList() []fakeAck {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fakeAck(nil), c.acks...)
}

func load(t *testing.T, ns, name string, params map[string]any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, config.ModuleRef{Module: name, Params: v}, nil)
	if err == nil {
		t.Cleanup(func() { module.Cleanup(m) })
	}
	return m, err
}

func mustLoad(t *testing.T, ns, name string, params map[string]any) any {
	t.Helper()
	m, err := load(t, ns, name, params)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func conn(params map[string]any) map[string]any {
	if params["addresses"] == nil {
		params["addresses"] = []string{"rabbit:5672"}
	}
	if params["auth"] == nil {
		if params["username"] == nil {
			params["username"] = "u"
		}
		if params["password_env"] == nil {
			params["password_env"] = "FIT_RABBITMQ_TEST_PW"
		}
	}
	return params
}

func source(t *testing.T, f *fake, params map[string]any) *Source {
	t.Helper()
	t.Setenv("FIT_RABBITMQ_TEST_PW", "secret")
	s := mustLoad(t, fitsync.Sources, "rabbitmq", conn(params)).(*Source)
	s.dial = f.dial
	return s
}

func sink(t *testing.T, f *fake, params map[string]any) *Sink {
	t.Helper()
	t.Setenv("FIT_RABBITMQ_TEST_PW", "secret")
	s := mustLoad(t, fitsync.Sinks, "rabbitmq", conn(params)).(*Sink)
	s.dial = f.dial
	return s
}

// eventually waits for cond, failing the test after two seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

// codecFunc is a codec of functions, for the tests.
type codecFunc struct {
	decode func(data []byte, emit func(*fitsync.Record)) error
	encode func(b []byte, recs []*fitsync.Record) ([]byte, error)
}

func (c codecFunc) Decode(data []byte, emit func(*fitsync.Record)) error { return c.decode(data, emit) }

func (c codecFunc) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) { return c.encode(b, recs) }
