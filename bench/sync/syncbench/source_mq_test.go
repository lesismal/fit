package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

func TestMessage(t *testing.T) {
	var m struct {
		ID     int
		K      int
		C, Pad string
	}
	if err := json.Unmarshal(message(5), &m); err != nil || m.ID != 5 || len(m.C) != 120 || len(m.Pad) != 60 {
		t.Fatal(m, err)
	}
}

// fakePub keeps the messages published, failing at fail.
type fakePub struct {
	mu     sync.Mutex
	n      int
	fail   string
	closed atomic.Int32
}

func (f *fakePub) publish(_ context.Context, bodies [][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range bodies {
		if f.fail != "" && strings.Contains(string(b), f.fail) {
			return errors.New("fake: refused")
		}
	}
	f.n += len(bodies)
	return nil
}

func (f *fakePub) close() { f.closed.Add(1) }

func TestPubSource(t *testing.T) {
	a, b := &fakePub{}, &fakePub{}
	s := &pubSource{pubs: []publisher{a, b}, batch: 7}
	if err := s.write(context.Background(), 10, 100); err != nil || a.n+b.n != 100 {
		t.Fatal(err, a.n, b.n)
	}
	a.fail, b.fail = `"id":50,`, `"id":50,`
	if err := s.write(context.Background(), 0, 100); err == nil {
		t.Fatal("no error")
	}
	s.close()
	if a.closed.Load() != 1 || b.closed.Load() != 1 {
		t.Fatal("not closed")
	}
}

// TestOpenPubSource opens a publisher a writer, of the scenario's broker,
// and closes those opened when one fails.
func TestOpenPubSource(t *testing.T) {
	r, m := rabbitDial, mqttDial
	t.Cleanup(func() { rabbitDial, mqttDial = r, m })
	var opened []*fakePub
	fails := -1
	dial := func() (publisher, error) {
		if len(opened) == fails {
			return nil, errors.New("fake: down")
		}
		p := &fakePub{}
		opened = append(opened, p)
		return p, nil
	}
	rabbitDial = func(context.Context, string, string, string) (publisher, error) { return dial() }
	mqttDial = func(context.Context, string, int) (publisher, error) { return dial() }
	for _, sc := range []string{"rabbitmq-kafka", "mqtt-kafka"} {
		opened = nil
		o := &options{sc: scenarios[sc], txRows: 3}
		src, err := openSource(context.Background(), o, 3)
		if err != nil || len(src.(*pubSource).pubs) != 3 || src.(*pubSource).batch != 30 {
			t.Fatal(sc, err)
		}
	}
	opened, fails = nil, 2
	if _, err := openSource(context.Background(), &options{sc: scenarios["mqtt-kafka"]}, 3); err == nil || opened[0].closed.Load() != 1 {
		t.Fatal(err)
	}
}

// TestMSSQLSourceAndPrepare writes SQL Server's transactions and readies
// its table and CDC.
func TestMSSQLSourceAndPrepare(t *testing.T) {
	d := mssqlDial
	t.Cleanup(func() { mssqlDial = d })
	db := &fakeDB{}
	mssqlDial = func(context.Context, string, string, string) (execer, error) { return db, nil }
	o := &options{sc: scenarios["mssql-kafka"], txRows: 4}
	src, err := openSource(context.Background(), o, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.write(context.Background(), 0, 10); err != nil || db.n() != 10 {
		t.Fatal(err, db.n())
	}
	if s := db.stmts[0]; !strings.HasPrefix(s, "BEGIN TRANSACTION;INSERT INTO dbo.items") || !strings.HasSuffix(s, "COMMIT TRANSACTION") {
		t.Fatal(s)
	}
	src.close()
	if err := prepare(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// The capture job's restart may fail; the rest may not.
	db.fail = "sp_cdc_start_job"
	if err := prepare(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	db.fail = "CREATE TABLE"
	if err := prepare(context.Background(), o); err == nil {
		t.Fatal("no error")
	}
	mssqlDial = func(context.Context, string, string, string) (execer, error) { return nil, errors.New("fake: down") }
	if err := prepare(context.Background(), o); err == nil {
		t.Fatal("no error")
	}
	if _, err := openSource(context.Background(), o, 1); err == nil {
		t.Fatal("no error")
	}
}

func TestPrepareQueues(t *testing.T) {
	p := rabbitPrepare
	t.Cleanup(func() { rabbitPrepare = p })
	calls := 0
	rabbitPrepare = func(context.Context, string, string, string) error { calls++; return nil }
	if err := prepare(context.Background(), &options{sc: scenarios["rabbitmq-kafka"]}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	rabbitPrepare = func(context.Context, string, string, string) error { return errors.New("fake: down") }
	if err := prepare(context.Background(), &options{sc: scenarios["rabbitmq-kafka"]}); err == nil {
		t.Fatal("no error")
	}
	if err := prepare(context.Background(), &options{sc: scenarios["mqtt-kafka"]}); err != nil {
		t.Fatal(err)
	}
}

// fakeMQTT is an MQTT 5 broker that takes a CONNECT and acknowledges each
// PUBLISH of QoS 1, counting them; it refuses a message holding refuse by
// closing the connection.
type fakeMQTT struct {
	n      atomic.Int64
	refuse string
}

func (f *fakeMQTT) serve(c net.Conn) {
	r := bufio.NewReader(c)
	for {
		h, err := r.ReadByte()
		if err != nil {
			return
		}
		n, mul := 0, 1
		for {
			b, err := r.ReadByte()
			if err != nil {
				return
			}
			n += int(b&0x7f) * mul
			mul *= 128
			if b < 0x80 {
				break
			}
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		switch h >> 4 {
		case 1: // CONNECT
			c.Write([]byte{0x20, 3, 0, 0, 0})
		case 3: // PUBLISH
			tl := int(body[0])<<8 | int(body[1])
			id := body[2+tl : 4+tl]
			if f.refuse != "" && strings.Contains(string(body), f.refuse) {
				return
			}
			f.n.Add(1)
			c.Write([]byte{0x40, 2, id[0], id[1]})
		case 12: // PINGREQ
			c.Write([]byte{0xd0, 0})
		case 14: // DISCONNECT
			return
		}
	}
}

// TestMQTTPub publishes to a broker, which acknowledges each message.
func TestMQTTPub(t *testing.T) {
	f := &fakeMQTT{}
	addr := listen(t, f.serve)
	p, err := dialMQTT(context.Background(), addr, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.publish(context.Background(), [][]byte{message(1), message(2)}); err != nil || f.n.Load() != 2 {
		t.Fatal(err, f.n.Load())
	}
	f.refuse = `"id":3,`
	if err := p.publish(context.Background(), [][]byte{message(3)}); err == nil {
		t.Fatal("no error")
	}
	p.close()
	if _, err := dialMQTT(context.Background(), "127.0.0.1:1", 0); err == nil {
		t.Fatal("no broker")
	}
}

// TestRealDialers: the RabbitMQ and SQL Server dialers fail without a
// server.
func TestRealDialers(t *testing.T) {
	ctx := context.Background()
	if _, err := dialRabbit(ctx, "127.0.0.1:1", "u", "p"); err == nil {
		t.Error("no RabbitMQ")
	}
	if err := rabbitDeclare(ctx, "127.0.0.1:1", "u", "p"); err == nil {
		t.Error("no RabbitMQ")
	}
	if _, err := dialMSSQL(ctx, "127.0.0.1:1", "p", "shop"); err == nil {
		t.Error("no SQL Server")
	}
}

// fakeAMQP is a confirming channel: it confirms every message but those
// holding nack, and fails a publish holding fail.
type fakeAMQP struct {
	seq            uint64
	nack, fail     string
	nacked, closed bool
	bodies         []string
}

func (f *fakeAMQP) Publish(_ context.Context, exchange, key string, _ bool, p amqpwire.Properties, body []byte) (uint64, error) {
	if f.fail != "" && strings.Contains(string(body), f.fail) {
		return 0, errors.New("fake: closed")
	}
	if exchange != "" || key != benchQueue || p.DeliveryMode != 2 {
		return 0, errors.New("fake: not to the queue, persistent")
	}
	f.seq++
	f.bodies = append(f.bodies, string(body))
	if f.nack != "" && strings.Contains(string(body), f.nack) {
		f.nacked = true
	}
	return f.seq, nil
}

func (f *fakeAMQP) WaitConfirms(_ context.Context, from, to uint64) ([]uint64, error) {
	if from > to {
		return nil, errors.New("fake: from past to")
	}
	if f.nacked {
		f.nacked = false
		return []uint64{to}, nil
	}
	return nil, nil
}

func (f *fakeAMQP) Close() error { f.closed = true; return nil }

func TestRabbitPub(t *testing.T) {
	f := &fakeAMQP{}
	p := &rabbitPub{f}
	if err := p.publish(context.Background(), [][]byte{message(1), message(2)}); err != nil || len(f.bodies) != 2 {
		t.Fatal(err, f.bodies)
	}
	if err := p.publish(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	f.nack = `"id":3,`
	if err := p.publish(context.Background(), [][]byte{message(3)}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatal(err)
	}
	f.fail = `"id":4,`
	if err := p.publish(context.Background(), [][]byte{message(4)}); err == nil {
		t.Fatal("no error")
	}
	p.close()
	if !f.closed {
		t.Fatal("not closed")
	}
}

// fakeTDS runs statements and answers a count.
type fakeTDS struct {
	stmts  []string
	rows   [][]any
	err    error
	closed bool
}

func (f *fakeTDS) Exec(_ context.Context, sql string, _ ...any) (int64, error) {
	f.stmts = append(f.stmts, sql)
	return 1, f.err
}

func (f *fakeTDS) QueryAll(_ context.Context, sql string, _ ...any) ([]mssqlwire.Column, [][]any, error) {
	f.stmts = append(f.stmts, sql)
	return nil, f.rows, f.err
}

func (f *fakeTDS) Close() error { f.closed = true; return nil }

func TestMSSQLConn(t *testing.T) {
	f := &fakeTDS{rows: [][]any{{int64(42)}}}
	c := &mssqlConn{f}
	if err := c.exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if n, err := c.count(context.Background(), "SELECT COUNT(*) FROM dbo.items"); err != nil || n != 42 {
		t.Fatal(n, err)
	}
	f.rows = nil
	if _, err := c.count(context.Background(), "SELECT COUNT(*) FROM dbo.items"); err == nil {
		t.Fatal("no rows")
	}
	f.err = errors.New("fake: down")
	if _, err := c.count(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("no error")
	}
	c.close()
	if !f.closed || !slices.Contains(f.stmts, "SELECT 1") {
		t.Fatal(f.stmts)
	}
}
