package syncnats

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/nats/natstest"
	natswire "github.com/lesismal/fit/sync/nats/wire"
)

// split is a codec for the tests: two records of a message, an error for
// "bad", none for "none", and inserts of rows for "row".
type split struct{}

func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".nats_test_split", New: func() any { return new(split) }, Doc: "A test codec."})
	module.Register(module.Info{ID: fitsync.Codecs + ".nats_test_notcodec", New: func() any { return new(struct{}) }, Doc: "Not a codec."})
}

func (split) Decode(data []byte, emit func(*fitsync.Record)) error {
	switch string(data) {
	case "bad":
		return errors.New("bad data")
	case "none":
		return nil
	case "row":
		emit(&fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})})
		emit(&fitsync.Record{Op: fitsync.OpDelete, Namespace: "own", Key: fitsync.Int(2), Tx: fitsync.Tx{ID: "mine"}})
		return nil
	case "scalar":
		emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.String("s")})
		return nil
	}
	emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.Bytes(data)})})
	emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "subject", Value: fitsync.String("mine")})})
	return nil
}

func (split) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	if recs[0].Namespace == "fail.codec" {
		return nil, errors.New("cannot")
	}
	return append(b, recs[0].Key.Text()...), nil
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

func server(t *testing.T, o natstest.Options) *natstest.Server {
	t.Helper()
	s, err := natstest.Start(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func source(t *testing.T, srv *natstest.Server, params map[string]any) *Source {
	t.Helper()
	params["servers"] = []string{srv.Addr()}
	if params["mode"] == nil {
		if params["stream"] == nil {
			params["stream"] = "S"
		}
		if params["consumer"] == nil {
			params["consumer"] = "C"
		}
	}
	return mustLoad(t, fitsync.Sources, "nats", params).(*Source)
}

func sink(t *testing.T, srv *natstest.Server, params map[string]any) *Sink {
	t.Helper()
	params["servers"] = []string{srv.Addr()}
	return mustLoad(t, fitsync.Sinks, "nats", params).(*Sink)
}

// collect runs a source until it ends, or until it has n records and then
// is stopped; each record emitted is acknowledged when ack says.
func collect(t *testing.T, s fitsync.Source, from fitsync.Position, once bool, n int, ack bool) ([]*fitsync.Record, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // a hang guard
	defer cancel()
	var out []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		out = append(out, r)
		if ack && r.Position != nil {
			s.(fitsync.Acknowledger).Acknowledged(r.Position)
		}
		if n > 0 && len(out) >= n {
			cancel()
		}
		return nil
	}})
	if errors.Is(err, context.Canceled) && n > 0 && len(out) >= n {
		err = nil
	}
	return out, err
}

func publish(t *testing.T, srv *natstest.Server, subject string, hdr natswire.Header, data ...string) {
	t.Helper()
	c, err := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, d := range data {
		pa, err := c.PublishJS(subject, hdr, []byte(d))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pa.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	if !waitFor(f) {
		t.Fatal("not in time")
	}
}

// waitFor waits up to 5s for f, for goroutines that cannot fail a test.
func waitFor(f func() bool) bool {
	for range 500 {
		if f() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func seqOf(t *testing.T, p fitsync.Position) uint64 {
	t.Helper()
	var pos position
	if err := json.Unmarshal(p, &pos); err != nil {
		t.Fatal(err)
	}
	return pos.Seq
}

func TestProvision(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("not a certificate"), 0o644)
	seed := natswire.SeedOf(make([]byte, 32))
	nkey := filepath.Join(dir, "nkey")
	os.WriteFile(nkey, []byte(seed+"\n"), 0o600)
	creds := filepath.Join(dir, "creds")
	os.WriteFile(creds, []byte("-----BEGIN NATS USER JWT-----\na.b.c\n------END NATS USER JWT------\n-----BEGIN USER NKEY SEED-----\n"+seed+"\n------END USER NKEY SEED------\n"), 0o600)
	badCreds := filepath.Join(dir, "badcreds")
	os.WriteFile(badCreds, []byte("-----BEGIN NATS USER JWT-----\na.b.c\n------END\n-----BEGIN USER NKEY SEED-----\nSUXX\n"), 0o600)
	_, pem := natstest.TLS()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem, 0o644)
	t.Setenv("FIT_NATS_TEST_PW", "secret")
	js := func(p map[string]any) map[string]any {
		p["stream"], p["consumer"] = "S", "C"
		if p["servers"] == nil {
			p["servers"] = []string{"n:4222"}
		}
		return p
	}
	for _, p := range []map[string]any{
		js(map[string]any{"servers": []string{"nohost"}}),
		js(map[string]any{"servers": []string{}}),
		js(map[string]any{"token_env": "1bad"}),
		js(map[string]any{"token_env": "FIT_NATS_TEST_UNSET"}),
		js(map[string]any{"token_env": "FIT_NATS_TEST_PW", "username": "u", "password_env": "FIT_NATS_TEST_PW"}),
		js(map[string]any{"username": "u"}),
		js(map[string]any{"password_env": "FIT_NATS_TEST_PW"}),
		js(map[string]any{"nkey_file": filepath.Join(dir, "none")}),
		js(map[string]any{"nkey_file": bad}),
		js(map[string]any{"creds_file": filepath.Join(dir, "none")}),
		js(map[string]any{"creds_file": badCreds}),
		js(map[string]any{"creds_file": bad}),
		js(map[string]any{"ca_file": ca}),
		js(map[string]any{"tls": true, "ca_file": bad}),
		js(map[string]any{"tls": true, "ca_file": filepath.Join(dir, "none")}),
		js(map[string]any{"tls": true, "cert_file": ca}),
		js(map[string]any{"tls": true, "cert_file": bad, "key_file": bad}),
		js(map[string]any{"subjects": []string{"a.>.b"}}),
		js(map[string]any{"subjects": []string{"a..b"}}),
		js(map[string]any{"subjects": []string{"a.b*"}}),
		js(map[string]any{"queue": "q"}),
		js(map[string]any{"codec": map[string]any{"module": "nope"}}),
		js(map[string]any{"codec": map[string]any{"module": "nats_test_notcodec"}}),
		{"servers": []string{"n:4222"}},
		{"servers": []string{"n:4222"}, "stream": "S", "consumer": "a.b"},
		{"servers": []string{"n:4222"}, "mode": "core"},
		{"servers": []string{"n:4222"}, "mode": "core", "subjects": []string{"a"}, "queue": "q q"},
		{"servers": []string{"n:4222"}, "mode": "core", "subjects": []string{" "}},
	} {
		if _, err := load(t, fitsync.Sources, "nats", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	s := mustLoad(t, fitsync.Sources, "nats", js(map[string]any{"username": "u", "password_env": "FIT_NATS_TEST_PW", "tls": true, "ca_file": ca,
		"server_name": "n", "timeout": "5s", "name": "me", "subjects": []string{"a.*", "b.>"}})).(*Source)
	if s.cfg.Password != "secret" || s.cfg.TLS.ServerName != "n" || s.timeout != 5*time.Second || s.cfg.Name != "me" {
		t.Fatalf("%+v", s.cfg)
	}
	s = mustLoad(t, fitsync.Sources, "nats", js(map[string]any{"nkey_file": nkey})).(*Source)
	if s.cfg.NKey == nil || s.cfg.Name != "fit-sync" || s.timeout != DefaultTimeout {
		t.Fatalf("%+v", s.cfg)
	}
	s = mustLoad(t, fitsync.Sources, "nats", js(map[string]any{"creds_file": creds})).(*Source)
	if s.cfg.NKey == nil || s.cfg.JWT != "a.b.c" {
		t.Fatalf("%+v", s.cfg)
	}
	s = mustLoad(t, fitsync.Sources, "nats", js(map[string]any{"token_env": "FIT_NATS_TEST_PW"})).(*Source)
	if s.cfg.Token != "secret" {
		t.Fatalf("%+v", s.cfg)
	}
	for _, p := range []map[string]any{
		{"subject": "{other}"}, {"subject": "a b"}, {"subject": "a.*"}, {"subject": "a..{namespace}"},
		{"codec": map[string]any{"module": "nope"}},
		{"servers": []string{}},
	} {
		if p["servers"] == nil {
			p["servers"] = []string{"n:4222"}
		}
		if _, err := load(t, fitsync.Sinks, "nats", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	mustLoad(t, fitsync.Sinks, "nats", map[string]any{"servers": []string{"n:4222"}, "subject": "out.{namespace}"})
}

func TestSourceStream(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", natswire.Header{"K": {"1", "2"}, "J": {"x"}}, "m1")
	publish(t, srv, "s.b", nil, "m2", "m3")
	s := source(t, srv, map[string]any{"create_consumer": true, "subjects": []string{"s.>"}, "batch": 2, "max_wait": "50ms"})
	var lags []fitsync.SourceLag
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var recs []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		if len(recs) == 3 {
			cancel()
		}
		return nil
	}, Lag: func(l fitsync.SourceLag) { lags = append(lags, l) }})
	if !errors.Is(err, context.Canceled) || len(recs) != 3 {
		t.Fatal(err, recs)
	}
	r := recs[0]
	if r.Source != "nats" || r.Namespace != "s.a" || r.Op != fitsync.OpMessage || seqOf(t, r.Position) != 1 || r.CommitTime.IsZero() {
		t.Fatalf("%+v", r)
	}
	if v, _ := r.After.Get("value"); v.Text() != "m1" {
		t.Fatal(r.After)
	}
	if v, _ := r.After.Get("subject"); v.Text() != "s.a" {
		t.Fatal(r.After)
	}
	hs, _ := r.After.Get("headers")
	if k, _ := hs.Get("K"); len(k.Elems()) != 2 {
		t.Fatal(hs)
	}
	if j, _ := hs.Get("J"); j.Text() != "x" {
		t.Fatal(hs)
	}
	if v, _ := r.After.Get("sequence"); v.AsUint() != 1 {
		t.Fatal(r.After)
	}
	if len(lags) != 3 || lags[2].Unit != fitsync.LagMessages {
		t.Fatal(lags)
	}
	// Nothing acknowledged: a run again reads them again, the server
	// delivering them once ack_wait passes; here the checkpoint skips the
	// first two, which are acknowledged at once.
	if n, _ := srv.Pending("S", "C"); n != 3 {
		t.Fatal(n)
	}
	// The consumer, found with messages pending, is made again from the
	// checkpoint.
	recs, err = collect(t, s, recs[1].Position, false, 1, true)
	if err != nil || len(recs) != 1 || seqOf(t, recs[0].Position) != 3 {
		t.Fatal(err, recs)
	}
	eventually(t, func() bool { n, floor := srv.Pending("S", "C"); return n == 0 && floor == 3 })
	// Acknowledged ignores what is not its stream's.
	s.Acknowledged(fitsync.Position(`{"stream":"T","seq":9}`))
	s.Acknowledged(fitsync.Position(`nope`))
	if s.ackedSeq() != 3 {
		t.Fatal(s.ackedSeq())
	}
}

func TestSourceExplicitAndRedelivery(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", nil, "1", "2", "3", "4")
	c, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	defer c.Close()
	if _, err := c.CreateConsumer(context.Background(), "S", natswire.ConsumerConfig{Durable: "C", AckPolicy: "explicit", AckWait: int64(30 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	s := source(t, srv, map[string]any{"max_wait": "20ms", "batch": 1})
	// A consumer of the user's own is read from its acknowledgement floor,
	// whatever the checkpoint says. Nothing is acknowledged while the four
	// are emitted; the server delivers them again, and they are kept, not
	// emitted twice, then acknowledged each.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var recs []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{From: fitsync.Position(`{"stream":"S","seq":3}`), Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		if len(recs) == 4 {
			time.Sleep(100 * time.Millisecond) // ack_wait passes for all
			go func() {
				time.Sleep(100 * time.Millisecond) // they come again
				s.Acknowledged(recs[3].Position)
				waitFor(func() bool { n, _ := srv.Pending("S", "C"); return n == 0 })
				cancel()
			}()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || len(recs) != 4 || seqOf(t, recs[0].Position) != 1 {
		t.Fatal(err, recs)
	}
	if n, floor := srv.Pending("S", "C"); n != 0 || floor != 4 {
		t.Fatal(n, floor)
	}
	// Ack policy all is refused on a consumer of the user's own.
	c.CreateConsumer(context.Background(), "S", natswire.ConsumerConfig{Durable: "ALL", AckPolicy: "all"})
	if _, err := collect(t, source(t, srv, map[string]any{"consumer": "ALL"}), nil, false, 0, false); err == nil || !strings.Contains(err.Error(), "ack policy all") {
		t.Fatal(err)
	}
}

// A consumer fit makes, found with nothing pending and behind the
// checkpoint: what it delivers up to the checkpoint is acknowledged, not
// emitted.
func TestSourceBehindCheckpoint(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", nil, "1", "2", "3")
	c, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	defer c.Close()
	c.CreateConsumer(context.Background(), "S", natswire.ConsumerConfig{Durable: "C", AckPolicy: "all"})
	s := source(t, srv, map[string]any{"create_consumer": true})
	recs, err := collect(t, s, fitsync.Position(`{"stream":"S","seq":2}`), true, 0, true)
	if err != nil || len(recs) != 1 || seqOf(t, recs[0].Position) != 3 {
		t.Fatal(err, recs)
	}
	eventually(t, func() bool { n, floor := srv.Pending("S", "C"); return n == 0 && floor == 3 })
}

func TestSourceJob(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	s := source(t, srv, map[string]any{"create_consumer": true, "max_wait": "20ms"})
	// An empty stream: done at once.
	if recs, err := collect(t, s, nil, true, 0, false); err != nil || len(recs) != 0 {
		t.Fatal(err, recs)
	}
	publish(t, srv, "s.a", nil, "1", "2")
	publish(t, srv, "s.b", nil, "3")
	recs, err := collect(t, s, nil, true, 0, true)
	if err != nil || len(recs) != 3 {
		t.Fatal(err, recs)
	}
	// A filter that the last message is not of: done when nothing is left.
	f := source(t, srv, map[string]any{"consumer": "F", "create_consumer": true, "max_wait": "20ms", "subjects": []string{"s.a"}, "start": "all"})
	if recs, err := collect(t, f, nil, true, 0, true); err != nil || len(recs) != 2 {
		t.Fatal(err, recs)
	}
	// The checkpoint at the end: done.
	if recs, err := collect(t, s, fitsync.Position(`{"stream":"S","seq":3}`), true, 0, false); err != nil || len(recs) != 0 {
		t.Fatal(err, recs)
	}
	// A consumer gone when the job looks whether it is finished.
	g := source(t, srv, map[string]any{"consumer": "G", "create_consumer": true, "max_wait": "20ms", "subjects": []string{"s.a"}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = g.Run(ctx, &fitsync.Run{Once: true, Emit: func(r *fitsync.Record) error {
		if seqOf(t, r.Position) == 2 {
			srv.SetHook(func(op, subject string) natstest.Action {
				if strings.HasPrefix(subject, "$JS.API.CONSUMER.INFO") {
					return natstest.Fatal
				}
				return natstest.Normal
			})
		}
		return nil
	}})
	srv.SetHook(nil)
	if err == nil {
		t.Fatal("no error")
	}
}

// A delivery of a consumer fit makes that the run never reads (a pull's
// answer lost to a reconnect, a run killed after a consumer's info read
// during a leader's move said nothing was delivered) shows as a gap in the
// consumer sequence: the consumer is made again from what the sinks have,
// rather than a later message emitted and acknowledged under ack all,
// which would acknowledge the lost one unread.
func TestSourceLostDelivery(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", nil, "1", "2", "3")
	c, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	defer c.Close()
	s := source(t, srv, map[string]any{"create_consumer": true, "batch": 1, "max_wait": "20ms"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var seqs []uint64
	err := s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
		seqs = append(seqs, seqOf(t, r.Position))
		s.Acknowledged(r.Position)
		if len(seqs) == 1 {
			// Delivered to someone else: lost to this run.
			if msgs, err := c.Fetch(ctx, "S", "C", natswire.FetchRequest{Batch: 1, NoWait: true}); err != nil || len(msgs) != 1 {
				t.Error(err, msgs)
			}
		}
		if len(seqs) == 3 {
			cancel()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || fmt.Sprint(seqs) != "[1 2 3]" {
		t.Fatal(err, seqs)
	}
	// A pull answered 409 by a leader's move, the consumer kept, after it
	// delivered to someone else: the consumer is made again too.
	publish(t, srv, "s.a", nil, "4", "5")
	s = source(t, srv, map[string]any{"create_consumer": true, "batch": 1, "max_wait": "5s"})
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seqs = nil
	err = s.Run(ctx, &fitsync.Run{From: fitsync.Position(`{"stream":"S","seq":3}`), Emit: func(r *fitsync.Record) error {
		seqs = append(seqs, seqOf(t, r.Position))
		s.Acknowledged(r.Position)
		switch len(seqs) {
		case 1:
			c.Fetch(ctx, "S", "C", natswire.FetchRequest{Batch: 1, NoWait: true})
			go func(ctx context.Context) { // ctx is taken again below
				waitFor(func() bool { return srv.MoveLeader("S", "C") > 0 })
				if pa, err := c.PublishJS("s.a", nil, []byte("6")); err == nil {
					pa.Wait(ctx)
				}
			}(ctx)
		case 3:
			cancel()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || fmt.Sprint(seqs) != "[4 5 6]" {
		t.Fatal(err, seqs)
	}
	// The consumer cannot be made again: the run ends with the error.
	publish(t, srv, "s.a", nil, "7", "8")
	s = source(t, srv, map[string]any{"create_consumer": true, "batch": 1, "max_wait": "20ms", "timeout": "300ms"})
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := 0
	err = s.Run(ctx, &fitsync.Run{From: fitsync.Position(`{"stream":"S","seq":6}`), Emit: func(r *fitsync.Record) error {
		if n++; n == 1 {
			c.Fetch(ctx, "S", "C", natswire.FetchRequest{Batch: 1, NoWait: true})
			publish(t, srv, "s.a", nil, "9")
			srv.SetHook(func(op, subject string) natstest.Action {
				if strings.HasPrefix(subject, "$JS.API.CONSUMER.DELETE") {
					return natstest.Fatal
				}
				return natstest.Normal
			})
		}
		return nil
	}})
	srv.SetHook(nil)
	if err == nil || !strings.Contains(err.Error(), "consumer C of S") || n != 1 {
		t.Fatal(err, n)
	}
}

func TestSourceConsumerLost(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", nil, "1", "2")
	s := source(t, srv, map[string]any{"create_consumer": true, "max_wait": "2s", "start": "new"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var recs []*fitsync.Record
	go func() {
		waitFor(func() bool { n, _ := srv.Pending("S", "C"); return n == 0 })
		time.Sleep(50 * time.Millisecond)
		srv.RemoveConsumer("S", "C") // under the waiting pull: 409
		waitFor(func() bool { n, _ := srv.Pending("S", "C"); return n == 0 })
		c, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
		c.Publish("s.a", "", nil, []byte("3"))
		c.Flush(context.Background())
		c.Close()
	}()
	err := s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
		recs = append(recs, r)
		cancel()
		return nil
	}})
	// start new skipped 1 and 2; the consumer made again after the
	// checkpoint (none: 0, so from new again... after what is acked, 0)
	// delivers 3 only, as new.
	if !errors.Is(err, context.Canceled) || len(recs) != 1 || seqOf(t, recs[0].Position) != 3 {
		t.Fatal(err, recs)
	}
}

func TestConsumerConfig(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	s := source(t, srv, map[string]any{"create_consumer": true, "ack_wait": "2m", "max_ack_pending": 5, "subjects": []string{"s.a", "s.b"}})
	if _, err := collect(t, s, nil, true, 0, false); err != nil {
		t.Fatal(err)
	}
	c, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	defer c.Close()
	ci, err := c.ConsumerInfo(context.Background(), "S", "C")
	if err != nil || ci.Config.AckWait != int64(2*time.Minute) || ci.Config.MaxAckPending != 5 || len(ci.Config.FilterSubjects) != 2 || ci.Config.AckPolicy != "all" {
		t.Fatal(ci, err)
	}
}

func TestSourceErrors(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	s := source(t, srv, map[string]any{})
	if _, err := collect(t, s, nil, false, 0, false); err == nil || !strings.Contains(err.Error(), "create_consumer is not set") {
		t.Fatal(err)
	}
	if _, err := collect(t, s, fitsync.Position(`{"stream":"T","seq":1}`), false, 0, false); err == nil || !strings.Contains(err.Error(), "stream T") {
		t.Fatal(err)
	}
	if _, err := collect(t, s, fitsync.Position(`nope`), false, 0, false); err == nil {
		t.Fatal("bad checkpoint")
	}
	nostream := source(t, srv, map[string]any{"stream": "X", "create_consumer": true})
	if _, err := collect(t, nostream, nil, false, 0, false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
	// A job whose stream info fails.
	job := source(t, srv, map[string]any{"create_consumer": true})
	srv.SetHook(func(op, subject string) natstest.Action {
		if strings.HasPrefix(subject, "$JS.API.STREAM.INFO") {
			return natstest.Fatal
		}
		return natstest.Normal
	})
	if _, err := collect(t, job, nil, true, 0, false); err == nil {
		t.Fatal("no error")
	}
	// A pull whose connection is lost: the run fails.
	pull := source(t, srv, map[string]any{"consumer": "P", "create_consumer": true})
	srv.SetHook(func(op, subject string) natstest.Action {
		if strings.HasPrefix(subject, "$JS.API.CONSUMER.MSG.NEXT") {
			return natstest.Close
		}
		return natstest.Normal
	})
	if _, err := collect(t, pull, nil, false, 0, false); err == nil || !strings.Contains(err.Error(), "pull from P") {
		t.Fatal(err)
	}
	srv.SetHook(nil)
	// A consumer deleted under a pull, not to be made again.
	srv.AddStream("Q", "q.>")
	cl, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	defer cl.Close()
	cl.CreateConsumer(context.Background(), "Q", natswire.ConsumerConfig{Durable: "C", AckPolicy: "explicit"})
	lost := source(t, srv, map[string]any{"stream": "Q", "max_wait": "2s"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		srv.RemoveConsumer("Q", "C")
	}()
	if _, err := collect(t, lost, nil, false, 0, false); err == nil || !strings.Contains(err.Error(), "create_consumer is not set") {
		t.Fatal(err)
	}
	// A message whose reply is not JetStream's.
	if err := (&reader{s: job}).take(&natswire.Msg{Reply: "x"}); err == nil {
		t.Fatal("no error")
	}
	// The server is down.
	srv.Close()
	down := source(t, srv, map[string]any{"timeout": "200ms"})
	if _, err := collect(t, down, nil, false, 0, false); err == nil {
		t.Fatal("no error")
	}
	if err := down.Test(context.Background()); err == nil {
		t.Fatal("no error")
	}
}

func TestSourceEmitFails(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", nil, "1")
	s := source(t, srv, map[string]any{"create_consumer": true})
	stop := errors.New("stop")
	if err := s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	c := source(t, srv, map[string]any{"mode": "core", "subjects": []string{"c.>"}})
	go func() {
		waitFor(func() bool { return c.cl.current() != nil })
		time.Sleep(50 * time.Millisecond)
		c.cl.current().Publish("c.x", "", nil, []byte("v"))
	}()
	if err := c.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }}); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestSourceCodecs(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	publish(t, srv, "s.a", nil, "two", "bad", "none", "row", "scalar")
	s := source(t, srv, map[string]any{"create_consumer": true, "codec": map[string]any{"module": "nats_test_split"}})
	recs, err := collect(t, s, nil, true, 0, false)
	if err != nil || len(recs) != 7 {
		t.Fatal(err, len(recs))
	}
	// two: a transaction of two message records, the second's own subject
	// kept.
	if recs[0].Tx.ID != "S@1" || !recs[0].Tx.First || recs[0].Position != nil || !recs[1].Tx.Last || seqOf(t, recs[1].Position) != 1 {
		t.Fatalf("%+v %+v", recs[0], recs[1])
	}
	if v, _ := recs[1].After.Get("subject"); v.Text() != "mine" {
		t.Fatal(recs[1].After)
	}
	for i, why := range []string{"bad data", "the codec made no record of it"} {
		if v, _ := recs[2+i].After.Get("error"); v.Text() != why {
			t.Fatal(recs[2+i].After)
		}
	}
	// row: no metadata in rows without metadata_field; the codec's own
	// transaction and namespace kept.
	if _, ok := recs[4].After.Get("subject"); ok || recs[5].Tx.ID != "mine" || recs[5].Namespace != "own" || recs[4].Tx.ID != "S@4" {
		t.Fatalf("%+v %+v", recs[4], recs[5])
	}
	if recs[6].After.Text() != "s" {
		t.Fatal(recs[6].After)
	}
	m := source(t, srv, map[string]any{"consumer": "M", "create_consumer": true, "codec": map[string]any{"module": "nats_test_split"},
		"metadata_field": "_nats", "namespace": "ns"})
	recs, err = collect(t, m, nil, true, 0, false)
	if err != nil || len(recs) != 7 {
		t.Fatal(err, len(recs))
	}
	if md, ok := recs[4].After.Get("_nats"); !ok || recs[4].Namespace != "ns" || recs[5].Namespace != "ns" {
		t.Fatalf("%+v", recs[4])
	} else if v, _ := md.Get("stream"); v.Text() != "S" {
		t.Fatal(md)
	}
}

func TestSourceCore(t *testing.T) {
	srv := server(t, natstest.Options{})
	s := source(t, srv, map[string]any{"mode": "core", "subjects": []string{"t.>", "u"}, "queue": "q"})
	if _, err := collect(t, s, nil, true, 0, false); err == nil || !strings.Contains(err.Error(), "core") {
		t.Fatal(err)
	}
	go func() {
		waitFor(func() bool { return s.cl.current() != nil })
		time.Sleep(50 * time.Millisecond)
		c := s.cl.current()
		c.Publish("t.a", "", natswire.Header{"H": {"v"}}, []byte("1"))
		c.Publish("u", "", nil, []byte("2"))
	}()
	recs, err := collect(t, s, nil, false, 2, false)
	if err != nil || len(recs) != 2 || recs[0].Position != nil {
		t.Fatal(err, recs)
	}
	ns := map[string]bool{recs[0].Namespace: true, recs[1].Namespace: true}
	if !ns["t.a"] || !ns["u"] {
		t.Fatal(ns)
	}
	if _, ok := recs[0].After.Get("stream"); ok {
		t.Fatal(recs[0].After)
	}
	// A subscription refused: the client is closed.
	s.cl.current().Close()
	if _, err := collect(t, s, nil, false, 0, false); err == nil {
		t.Fatal("no error")
	}
}

func TestTest(t *testing.T) {
	stls, pem := natstest.TLS()
	srv := server(t, natstest.Options{TLS: stls, User: "u", Pass: "p"})
	srv.AddStream("S", "s.>")
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, pem, 0o644)
	t.Setenv("FIT_NATS_TEST_PW", "p")
	auth := func(p map[string]any) map[string]any {
		p["tls"], p["ca_file"], p["username"], p["password_env"] = true, ca, "u", "FIT_NATS_TEST_PW"
		return p
	}
	ctx := context.Background()
	s := source(t, srv, auth(map[string]any{}))
	if err := s.Test(ctx); err == nil || !strings.Contains(err.Error(), "consumer C") {
		t.Fatal(err)
	}
	if err := source(t, srv, auth(map[string]any{"create_consumer": true})).Test(ctx); err != nil {
		t.Fatal(err)
	}
	if err := source(t, srv, auth(map[string]any{"stream": "X"})).Test(ctx); err == nil || !strings.Contains(err.Error(), "stream X") {
		t.Fatal(err)
	}
	if err := source(t, srv, auth(map[string]any{"mode": "core", "subjects": []string{"a"}})).Test(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sink(t, srv, auth(map[string]any{})).Test(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sink(t, srv, map[string]any{"timeout": "200ms"}).Test(ctx); err == nil {
		t.Fatal("no TLS")
	}
}

func TestAuthModes(t *testing.T) {
	seedBytes := make([]byte, 32)
	seedBytes[3] = 9
	priv := ed25519.NewKeyFromSeed(seedBytes)
	pub := natstest.NKeyPublic(priv.Public().(ed25519.PublicKey))
	dir := t.TempDir()
	nkey := filepath.Join(dir, "nkey")
	os.WriteFile(nkey, []byte(natswire.SeedOf(seedBytes)), 0o600)
	enc := base64.RawURLEncoding.EncodeToString
	jwt := enc([]byte(`{}`)) + "." + enc([]byte(`{"sub":"`+pub+`"}`)) + ".x"
	creds := filepath.Join(dir, "creds")
	os.WriteFile(creds, []byte(fmt.Sprintf("-----BEGIN NATS USER JWT-----\n%s\n------END NATS USER JWT------\n-----BEGIN USER NKEY SEED-----\n%s\n------END USER NKEY SEED------\n", jwt, natswire.SeedOf(seedBytes))), 0o600)
	t.Setenv("FIT_NATS_TEST_TOKEN", "tok")
	for _, c := range []struct {
		o natstest.Options
		p map[string]any
	}{
		{natstest.Options{Token: "tok"}, map[string]any{"token_env": "FIT_NATS_TEST_TOKEN"}},
		{natstest.Options{NKey: pub}, map[string]any{"nkey_file": nkey}},
		{natstest.Options{JWT: true}, map[string]any{"creds_file": creds}},
	} {
		srv := server(t, c.o)
		if err := sink(t, srv, c.p).Test(context.Background()); err != nil {
			t.Fatal(c.p, err)
		}
	}
}

func gen(n int) []*fitsync.Record {
	recs := make([]*fitsync.Record, n)
	for i := range recs {
		recs[i] = fitsync.GenerateRecord("s.gen", int64(i), int64(n), 1)
	}
	return recs
}

func TestSink(t *testing.T) {
	srv := server(t, natstest.Options{MaxPayload: 4096})
	srv.AddStream("S", "s.>")
	k := sink(t, srv, map[string]any{"max_in_flight": 2})
	ctx := context.Background()
	batch := gen(5)
	if err := k.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := k.Write(ctx, batch[2:]); err != nil {
		t.Fatal(err)
	}
	msgs := srv.Messages("S")
	if len(msgs) != 5 || msgs[0].Subject != "s.gen" || !strings.Contains(string(msgs[0].Header), "Nats-Msg-Id") {
		t.Fatalf("%d %+v", len(msgs), msgs)
	}
	// Records the codec cannot make, or of a bad subject, fail at once.
	var be *fitsync.BatchError
	bad := gen(3)
	bad[1].Namespace = "a b"
	if err := k.Write(ctx, bad); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	c := sink(t, srv, map[string]any{"codec": map[string]any{"module": "nats_test_split"}, "subject": "s.{namespace}"})
	bad = gen(2)
	bad[0].Position = fitsync.Position(`{"n":100}`) // not the record written above
	bad[1].Namespace = "fail.codec"
	if err := c.Write(ctx, bad); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	if msgs := srv.Messages("S"); msgs[len(msgs)-1].Subject != "s.s.gen" {
		t.Fatal(msgs[len(msgs)-1])
	}
	// Too large for the server.
	big := gen(2)
	big[1].After = fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.Bytes(make([]byte, 5000))})
	if err := k.Write(ctx, big); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// No stream takes the subject: the record fails, not permanently.
	none := gen(2)
	none[0].Namespace = "nostream"
	if err := k.Write(ctx, none); !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// A record that fails after one whose acknowledgement fails: the
	// first's error.
	none = gen(2)
	none[0].Namespace, none[1].Namespace = "nostream", "a b"
	if err := k.Write(ctx, none); !errors.As(err, &be) || be.Index != 0 || fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// An acknowledgement that never comes.
	srv.SetHook(func(op, subject string) natstest.Action {
		if subject == "s.gen" {
			return natstest.Drop
		}
		return natstest.Normal
	})
	short := sink(t, srv, map[string]any{"timeout": "100ms"})
	if err := short.Write(ctx, gen(1)); !errors.As(err, &be) || be.Index != 0 {
		t.Fatal(err)
	}
	// A publish refused when the connection is gone, after one sent.
	srv.SetHook(func(op, subject string) natstest.Action {
		if subject == "s.gen" {
			return natstest.Close
		}
		return natstest.Normal
	})
	if err := k.Write(ctx, gen(3)); !errors.As(err, &be) || be.Index != 0 {
		t.Fatal(err)
	}
	srv.SetHook(nil)
	k.cl.current().Close()
	if err := k.Write(ctx, gen(3)); !errors.As(err, &be) || be.Index != 0 || !errors.Is(err, natswire.ErrClosed) {
		t.Fatal(err)
	}
	srv.Close()
	if err := sink(t, srv, map[string]any{"timeout": "100ms"}).Write(ctx, gen(1)); err == nil {
		t.Fatal("no server")
	}
}

func TestSinkCore(t *testing.T) {
	srv := server(t, natstest.Options{MaxPayload: 4096})
	k := sink(t, srv, map[string]any{"mode": "core", "subject": "c.{namespace}"})
	ctx := context.Background()
	got := make(chan *natswire.Msg, 10)
	c, _ := natswire.Dial(ctx, natswire.Config{Servers: []string{srv.Addr()}})
	defer c.Close()
	c.Subscribe("c.>", "", got)
	c.Flush(ctx)
	if err := k.Write(ctx, gen(2)); err != nil {
		t.Fatal(err)
	}
	if m := <-got; m.Subject != "c.s.gen" {
		t.Fatal(m)
	}
	var be *fitsync.BatchError
	big := gen(2)
	big[1].After = fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.Bytes(make([]byte, 5000))})
	if err := k.Write(ctx, big); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	k.cl.current().Close()
	if err := k.Write(ctx, gen(1)); !errors.Is(err, natswire.ErrClosed) {
		t.Fatal(err)
	}
}

// The acker: started once, stopped by Cleanup, acknowledging nothing
// without a client or under ack policy none.
func TestAcker(t *testing.T) {
	s := &Source{Stream: "S"}
	s.Acknowledged(fitsync.Position(`{"stream":"S","seq":1}`)) // no acker yet
	s.ackStart.Do(s.startAcker)
	s.addPending(2, "$JS.ACK.S.C.1.2.2.0.0")
	s.addPending(1, "$JS.ACK.S.C.1.1.1.0.0")
	if s.pending[0].seq != 1 {
		t.Fatal(s.pending)
	}
	s.policy = "none"
	s.Acknowledged(fitsync.Position(`{"stream":"S","seq":2}`))
	eventually(t, func() bool {
		s.ackMu.Lock()
		defer s.ackMu.Unlock()
		return len(s.pending) == 0
	})
	s.Cleanup()
}

// Keys go as Fit-Key, with Fit-Op and Fit-Namespace, and come back as the
// records' keys: strings and bytes as they are, anything else as plain
// JSON; a value a header line cannot hold is left out.
func TestKeyHeaders(t *testing.T) {
	srv := server(t, natstest.Options{})
	srv.AddStream("S", "s.>")
	k := sink(t, srv, map[string]any{})
	recs := gen(6)
	recs[0].Key = fitsync.String("k0")
	recs[1].Key = fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(7)})
	recs[2].Key = fitsync.Bytes([]byte{0xff, 'x'})
	recs[3].Key = fitsync.Null()
	recs[4].Key = fitsync.String("a\r\nb")
	recs[5].Key, recs[5].Namespace = fitsync.String(""), "s.gen"
	if err := k.Write(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
	h := string(srv.Messages("S")[1].Header)
	if !strings.Contains(h, "Fit-Op: insert") || !strings.Contains(h, "Fit-Namespace: s.gen") || !strings.Contains(h, `Fit-Key: {"id":7}`) {
		t.Fatal(h)
	}
	if h := string(srv.Messages("S")[4].Header); strings.Contains(h, "Fit-Key") {
		t.Fatal(h)
	}
	got, err := collect(t, source(t, srv, map[string]any{"create_consumer": true, "max_wait": "50ms"}), nil, true, 0, true)
	if err != nil || len(got) != 6 {
		t.Fatal(err, got)
	}
	want := []fitsync.Value{fitsync.String("k0"), fitsync.String(`{"id":7}`), fitsync.Bytes([]byte{0xff, 'x'}), fitsync.Null(), fitsync.Null(), fitsync.String("")}
	for i, r := range got {
		if r.Key.Kind() != want[i].Kind() || r.Key.Text() != want[i].Text() {
			t.Fatalf("%d: %v, want %v", i, r.Key, want[i])
		}
	}
	// The header's name is matched without regard to case.
	s := source(t, srv, map[string]any{"mode": "core", "subjects": []string{"x.>"}})
	m := &natswire.Msg{Subject: "x.a", Header: natswire.Header{"fit-key": {"h"}}, Data: []byte("v")}
	if r := s.records(m, nil)[0]; r.Key.Text() != "h" {
		t.Fatal(r.Key)
	}
	// Core publishes carry them too; a namespace with a line break is
	// left out.
	c := sink(t, srv, map[string]any{"mode": "core", "subject": "c.x"})
	ch := make(chan *natswire.Msg, 1)
	sub, _ := natswire.Dial(context.Background(), natswire.Config{Servers: []string{srv.Addr()}})
	defer sub.Close()
	sub.Subscribe("c.x", "", ch)
	sub.Flush(context.Background())
	one := gen(1)
	one[0].Key, one[0].Namespace = fitsync.Int(3), "n\n"
	if err := c.Write(context.Background(), one); err != nil {
		t.Fatal(err)
	}
	if m := <-ch; m.Header.Get("Fit-Key") != "3" || m.Header.Get("Fit-Op") == "" || m.Header.Get("Fit-Namespace") != "" {
		t.Fatal(m.Header)
	}
}
