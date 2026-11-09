package syncmqtt

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// fakeNet is the brokers the connectors dial in the tests: each dial is a
// fakeClient, unless dialErr refuses the address.
type fakeNet struct {
	mu      sync.Mutex
	clients []*fakeClient
	dialErr map[string]error
	subErr  error
	// subGate, when not nil, holds Subscribe until it is closed: a SUBACK
	// that comes after the session's messages.
	subGate chan struct{}
	// sendErr and waitErr, when not nil, fail a publish of a message.
	sendErr func(m *mqttwire.Message) error
	waitErr func(m *mqttwire.Message) error
}

type fakeClient struct {
	n    *fakeNet
	cfg  mqttwire.Config
	msgs chan *mqttwire.Message
	done chan struct{}

	mu     sync.Mutex
	err    error
	acks   []*mqttwire.Message
	subs   []mqttwire.Subscription
	sent   []*mqttwire.Message
	closed bool
}

func newFakeNet() *fakeNet { return &fakeNet{dialErr: map[string]error{}} }

func (n *fakeNet) dial(ctx context.Context, cfg mqttwire.Config) (client, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.dialErr[cfg.Address]; err != nil {
		return nil, err
	}
	c := &fakeClient{n: n, cfg: cfg, msgs: make(chan *mqttwire.Message, 100), done: make(chan struct{})}
	n.clients = append(n.clients, c)
	return c, nil
}

// dialed is how many clients were dialed so far.
func (n *fakeNet) dialed() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.clients)
}

// last returns the last client dialed, waiting for one.
func (n *fakeNet) last(t *testing.T, count int) *fakeClient {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n.mu.Lock()
		if len(n.clients) >= count {
			c := n.clients[count-1]
			n.mu.Unlock()
			return c
		}
		n.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no client %d", count)
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *fakeClient) Subscribe(ctx context.Context, subs ...mqttwire.Subscription) ([]byte, error) {
	c.mu.Lock()
	c.subs = append(c.subs, subs...)
	c.mu.Unlock()
	c.n.mu.Lock()
	gate := c.n.subGate
	c.n.mu.Unlock()
	if gate != nil {
		<-gate
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return make([]byte, len(subs)), c.n.subErr
}

func (c *fakeClient) Messages() <-chan *mqttwire.Message { return c.msgs }

func (c *fakeClient) Ack(m *mqttwire.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.acks = append(c.acks, m)
	return nil
}

type fakeWaiter struct{ err error }

func (w fakeWaiter) Wait(ctx context.Context) error { return w.err }

func (c *fakeClient) Send(ctx context.Context, m *mqttwire.Message) (waiter, error) {
	c.n.mu.Lock()
	sendErr, waitErr := c.n.sendErr, c.n.waitErr
	c.n.mu.Unlock()
	if sendErr != nil {
		if err := sendErr(m); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	var w fakeWaiter
	if waitErr != nil {
		w.err = waitErr(m)
	}
	return w, nil
}

func (c *fakeClient) Done() <-chan struct{} { return c.done }

func (c *fakeClient) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// lose ends the connection on err.
func (c *fakeClient) lose(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}

func (c *fakeClient) Close() error {
	c.lose(mqttwire.ErrClosed)
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fakeClient) acked() []*mqttwire.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*mqttwire.Message(nil), c.acks...)
}

func (c *fakeClient) published() []*mqttwire.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*mqttwire.Message(nil), c.sent...)
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

func source(t *testing.T, n *fakeNet, params map[string]any) *Source {
	t.Helper()
	if params["servers"] == nil {
		params["servers"] = []string{"b:1883"}
	}
	if params["client_id"] == nil {
		params["client_id"] = "fit-test"
	}
	if params["topics"] == nil {
		params["topics"] = []string{"t/#"}
	}
	s := mustLoad(t, fitsync.Sources, "mqtt", params).(*Source)
	s.dial = n.dial
	return s
}

func sink(t *testing.T, n *fakeNet, params map[string]any) *Sink {
	t.Helper()
	if params["servers"] == nil {
		params["servers"] = []string{"b:1883"}
	}
	s := mustLoad(t, fitsync.Sinks, "mqtt", params).(*Sink)
	s.dial = n.dial
	return s
}

// testCodec is a codec of the tests: "none" makes no record, "bad" fails,
// "two" makes two records, the second with a key and namespace of its
// own; anything else a record of the text.
type testCodec struct{}

func (testCodec) Decode(data []byte, emit func(*fitsync.Record)) error {
	switch string(data) {
	case "none":
	case "bad":
		return errors.New("bad payload")
	case "two":
		emit(&fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "n", Value: fitsync.Int(1)})})
		emit(&fitsync.Record{Op: fitsync.OpInsert, Namespace: "own", Key: fitsync.Int(2), CommitTime: time.Unix(5, 0),
			After: fitsync.Map(fitsync.Field{Name: "n", Value: fitsync.Int(2)})})
	default:
		emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "text", Value: fitsync.String(string(data))})})
	}
	return nil
}

func (testCodec) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	for _, r := range recs {
		if r.Namespace == "fail" {
			return b, errors.New("cannot encode")
		}
		b = append(append(b, r.Namespace...), '\n')
	}
	return b, nil
}

// notCodec is a module of sync.codecs that is not a codec.
type notCodec struct{}

func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".mqtt_test_not_codec", New: func() any { return new(notCodec) }, Doc: "Not a codec, for the tests."})
}
