package syncredis

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/redis/wire"
)

// pipelineConfig makes a config of one pipeline.
func pipelineConfig(t *testing.T, stateDir string, p map[string]any) *config.Sync {
	t.Helper()
	p["name"] = "p"
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

// load loads a module of a pipeline's config, or returns its errors.
func load(t *testing.T, ns string, params map[string]any) (any, error) {
	t.Helper()
	ref := map[string]any{"module": "redis", "params": params}
	p := map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}}
	if ns == fitsync.Sinks {
		p["sinks"] = []any{map[string]any{"name": "s", "sink": ref}}
	} else {
		p["source"] = ref
	}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	pc := cfg.Sync.Pipelines[0]
	r := pc.Source
	if ns == fitsync.Sinks {
		r = pc.Sinks[0].Sink
	}
	m, err := module.Load(ns, r, nil)
	if err == nil {
		t.Cleanup(func() { module.Cleanup(m) })
	}
	return m, err
}

// certFiles writes a self-signed certificate and its key, and returns their
// paths.
func certFiles(t *testing.T) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "c"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	cert, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kf, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	return cert, kf
}

func TestProvision(t *testing.T) {
	t.Setenv("FIT_TEST_REDIS_PW", "pw")
	cert, key := certFiles(t)
	notPEM := filepath.Join(t.TempDir(), "x.pem")
	os.WriteFile(notPEM, []byte("no"), 0o600)
	for _, tc := range []struct {
		ns     string
		params map[string]any
		want   string
	}{
		{fitsync.Sinks, map[string]any{}, ""},
		{fitsync.Sinks, map[string]any{"mode": "keys", "value": "hash", "ttl": "1m", "username": "u", "password_env": "FIT_TEST_REDIS_PW"}, ""},
		{fitsync.Sinks, map[string]any{"tls": true, "ca_file": cert, "cert_file": cert, "key_file": key, "server_name": "r"}, ""},
		{fitsync.Sinks, map[string]any{"tls": true, "ca_file": notPEM}, "holds no PEM certificate"},
		{fitsync.Sinks, map[string]any{"tls": true, "ca_file": notPEM + ".missing"}, "no such file"},
		{fitsync.Sinks, map[string]any{"tls": true, "cert_file": notPEM, "key_file": notPEM}, "cert_file"},
		{fitsync.Sinks, map[string]any{"address": "nohost"}, "not a host and port"},
		{fitsync.Sinks, map[string]any{"protocol": 1}, "not 2 or 3"},
		{fitsync.Sinks, map[string]any{"username": "u"}, "username needs password_env"},
		{fitsync.Sinks, map[string]any{"password_env": "FIT_TEST_REDIS_UNSET"}, "not set"},
		{fitsync.Sinks, map[string]any{"password_env": "1-x"}, "not an environment variable's name"},
		{fitsync.Sinks, map[string]any{"ca_file": "x.pem"}, "ca_file needs tls"},
		{fitsync.Sinks, map[string]any{"tls": true, "cert_file": "c.pem"}, "go together"},
		{fitsync.Sinks, map[string]any{"mode": "keys", "stream": "s"}, "mode stream's"},
		{fitsync.Sinks, map[string]any{"mode": "keys", "max_len": 5}, "mode stream's"},
		{fitsync.Sinks, map[string]any{"mode": "keys", "key": "{table}"}, "only {namespace} and {key}"},
		{fitsync.Sinks, map[string]any{"mode": "keys", "ttl": "1us"}, "less than a millisecond"},
		{fitsync.Sinks, map[string]any{"mode": "keys", "ttl": "-1s"}, "negative"},
		{fitsync.Sinks, map[string]any{"value": "hash"}, "mode keys'"},
		{fitsync.Sinks, map[string]any{"ttl": "1m"}, "mode keys'"},
		{fitsync.Sinks, map[string]any{"stream": "a{namespace"}, "without a }"},
		{fitsync.Sinks, map[string]any{"stream": "a}"}, "without a {"},
		{fitsync.Sinks, map[string]any{"form": "record", "op_field": "op"}, "holds the operation"},
		{fitsync.Sources, map[string]any{"streams": []string{"a", "b"}}, ""},
		{fitsync.Sources, map[string]any{"streams": []string{"a", "a"}}, "named twice"},
		{fitsync.Sources, map[string]any{"streams": []string{}}, "no streams"},
	} {
		_, err := load(t, tc.ns, tc.params)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%v: %v", tc.params, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%v: %v, want %q", tc.params, err, tc.want)
		}
	}
}

func sinkOf(t *testing.T, s *server, params map[string]any) *Sink {
	t.Helper()
	params["address"] = s.addr()
	m, err := load(t, fitsync.Sinks, params)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*Sink)
}

func sourceOf(t *testing.T, s *server, params map[string]any) *Source {
	t.Helper()
	params["address"] = s.addr()
	m, err := load(t, fitsync.Sources, params)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*Source)
}

func rec(ns string, op fitsync.Op, id int64, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: ns, Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})}
	if op == fitsync.OpDelete {
		r.Before = fitsync.Map(fields...)
	} else {
		r.After = fitsync.Map(fields...)
	}
	return r
}

// TestSinkCommands checks the commands each mode sends.
func TestSinkCommands(t *testing.T) {
	srv := newServer(t)
	v := fitsync.Field{Name: "v", Value: fitsync.String("x")}
	n := fitsync.Field{Name: "n", Value: fitsync.Null()}
	m := fitsync.Field{Name: "m", Value: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})}
	a := fitsync.Field{Name: "a", Value: fitsync.Array(fitsync.Int(1), fitsync.Bool(true))}
	keyOnly := &fitsync.Record{Namespace: "t", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(3)})}
	nullRow := rec("t", fitsync.OpInsert, 4, n)
	batch := []*fitsync.Record{rec("t", fitsync.OpInsert, 1, v, n, m, a), rec("t", fitsync.OpDelete, 2, v), {Namespace: "t", Op: fitsync.OpDDL}, keyOnly}
	for _, tc := range []struct {
		params map[string]any
		batch  []*fitsync.Record
		want   []string
	}{
		{map[string]any{"op_field": "op", "max_len": 100}, batch, []string{`XADD t MAXLEN ~ 100 * op insert v x m {"a":1} a [1,true]`, `XADD t MAXLEN ~ 100 * op delete v x`, `XADD t MAXLEN ~ 100 * op delete id 3`}},
		{map[string]any{"stream": "s:{namespace}:{key}", "max_pipeline": 1}, batch[:2], []string{`XADD s:t:1 * v x m {"a":1} a [1,true]`, `XADD s:t:2 * v x`}},
		{map[string]any{"mode": "keys", "ttl": "2s"}, batch, []string{`SET t:1 {"v":"x","n":null,"m":{"a":1},"a":[1,true]} PX 2000`, `DEL t:2`, `DEL t:3`}},
		{map[string]any{"mode": "keys"}, batch[:1], []string{`SET t:1 {"v":"x","n":null,"m":{"a":1},"a":[1,true]}`}},
		{map[string]any{"mode": "keys", "value": "hash", "ttl": "2s", "key": "k:{key}"}, append(batch[:2:2], nullRow),
			[]string{`MULTI`, `DEL k:1`, `HSET k:1 v x m {"a":1} a [1,true]`, `PEXPIRE k:1 2000`, `EXEC`, `DEL k:2`, `MULTI`, `DEL k:4`, `EXEC`}},
	} {
		srv.reset()
		if err := sinkOf(t, srv, tc.params).Write(context.Background(), tc.batch); err != nil {
			t.Fatalf("%v: %v", tc.params, err)
		}
		if got := srv.sent(); strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%v sent\n%s\nwant\n%s", tc.params, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
	}
	// form record: one field holding the record, read back whole.
	srv.reset()
	if err := sinkOf(t, srv, map[string]any{"form": "record"}).Write(context.Background(), batch[:1]); err != nil {
		t.Fatal(err)
	}
	doc := strings.TrimPrefix(srv.sent()[0], "XADD t * record ")
	if r, err := fitsync.ParseRecord([]byte(doc)); err != nil || !r.After.Equal(batch[0].After) {
		t.Fatalf("%s: %v", doc, err)
	}
	srv.reset()
	if err := sinkOf(t, srv, map[string]any{"mode": "keys", "value": "record"}).Write(context.Background(), batch[:1]); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srv.sent()[0], `SET t:1 {`) {
		t.Fatalf("%q", srv.sent())
	}
}

// TestSinkRecordErrors fails records that cannot be written, for good, the
// records before them written.
func TestSinkRecordErrors(t *testing.T) {
	good := rec("t", fitsync.OpInsert, 1, fitsync.Field{Name: "v", Value: fitsync.Int(1)})
	nanRec := rec("t", fitsync.OpInsert, 1, fitsync.Field{Name: "m", Value: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(nanValue())})})
	for _, tc := range []struct {
		params map[string]any
		bad    *fitsync.Record
		want   string
	}{
		{map[string]any{"mode": "keys"}, &fitsync.Record{Namespace: "x", Op: fitsync.OpInsert, After: fitsync.Map()}, "no key"},
		{map[string]any{"mode": "keys"}, &fitsync.Record{Namespace: "x", Op: fitsync.OpInsert, Key: fitsync.Map()}, "no fields"},
		{map[string]any{"mode": "keys"}, &fitsync.Record{Namespace: "x", Op: fitsync.OpInsert, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Null()})}, "is null"},
		{map[string]any{"mode": "keys", "key": "{key}"}, &fitsync.Record{Op: fitsync.OpInsert, Key: fitsync.String("")}, "empty"},
		{map[string]any{"mode": "keys", "value": "hash"}, &fitsync.Record{Namespace: "x", Op: fitsync.OpInsert, Key: fitsync.Int(1), After: fitsync.Int(2)}, "not a map"},
		{map[string]any{"mode": "keys", "value": "hash"}, nanRec, "the field m"},
		{map[string]any{"mode": "keys"}, nanRec, "JSON number"},
		{map[string]any{"mode": "keys", "value": "record"}, nanRec, ""},
		{map[string]any{"form": "record"}, nanRec, ""},
		{map[string]any{}, rec("t", fitsync.OpInsert, 1, fitsync.Field{Name: "n", Value: fitsync.Null()}), "no field that is not null"},
		{map[string]any{"timeout": "5s"}, &fitsync.Record{Namespace: "x", Op: fitsync.OpInsert, After: fitsync.Int(2)}, "not a map"},
		{map[string]any{}, &fitsync.Record{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})}, "empty"},
		{map[string]any{"entry_id": "key"}, rec("t", fitsync.OpInsert, 1, fitsync.Field{Name: "v", Value: fitsync.Int(1)}), "not an entry ID"},
		{map[string]any{"entry_id": "key"}, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.Map(fitsync.Field{Name: "k", Value: fitsync.Int(1)})}, "no id field"},
		{map[string]any{"stream": "{key}"}, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})}, "no key"},
		{map[string]any{}, &fitsync.Record{Namespace: "t", Op: fitsync.Op(99), After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})}, ""},
	} {
		g := good
		if tc.params["entry_id"] == "key" {
			g = &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.String("1-1"), After: good.After}
		}
		err := sinkOf(t, newServer(t), tc.params).Write(context.Background(), []*fitsync.Record{g, tc.bad})
		if tc.want == "" {
			continue // written, or refused, as the JSON allows: only no panic
		}
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v %v: %v, want %q", tc.params, tc.bad, err, tc.want)
		}
	}
}

func nanValue() float64 {
	zero := 0.0
	return zero / zero
}

// TestSinkServerErrors checks what a write's errors say: the record the
// server refused, for good or not, the records before it written; an entry
// at or below its stream's last, with IDs from the keys, written.
func TestSinkServerErrors(t *testing.T) {
	srv := newServer(t)
	srv.setInject(func(args []string) (string, bool) {
		if len(args) > 1 && args[1] == "oom" {
			return "-OOM command not allowed when used memory > 'maxmemory'.\r\n", true
		}
		return "", false
	})
	srv.strs["wrong"] = "a string"
	s := sinkOf(t, srv, map[string]any{"max_pipeline": 2})
	one := fitsync.Field{Name: "v", Value: fitsync.Int(1)}
	batch := []*fitsync.Record{rec("a", fitsync.OpInsert, 1, one), rec("b", fitsync.OpInsert, 2, one), rec("c", fitsync.OpInsert, 3, one), rec("wrong", fitsync.OpInsert, 4, one)}
	var be *fitsync.BatchError
	if err := s.Write(context.Background(), batch); !errors.As(err, &be) || be.Index != 3 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("WRONGTYPE: %v", err)
	}
	batch[3].Namespace = "oom"
	if err := s.Write(context.Background(), batch); !errors.As(err, &be) || be.Index != 3 || fitsync.IsPermanent(err) {
		t.Fatalf("OOM: %v", err)
	}
	if err := s.Write(context.Background(), batch[3:]); errors.As(err, &be) || err == nil || fitsync.IsPermanent(err) {
		t.Fatalf("OOM first: %v", err)
	}
	if err := s.Write(context.Background(), []*fitsync.Record{batch[0], batch[3]}); !errors.As(err, &be) || be.Index != 1 || fitsync.IsPermanent(err) {
		t.Fatalf("OOM second: %v", err)
	}
	// A hash's MULTI: an error queueing, and one inside EXEC.
	hs := sinkOf(t, srv, map[string]any{"mode": "keys", "value": "hash", "key": "{namespace}"})
	if err := hs.Write(context.Background(), []*fitsync.Record{batch[0], batch[3]}); !errors.As(err, &be) || be.Index != 1 || fitsync.IsPermanent(err) {
		t.Fatalf("OOM in EXEC: %v", err)
	}
	srv.setInject(nil)
	// Entry IDs from the keys: a replay's entries are written already, an
	// ID of 0-0 is refused for good.
	ids := sinkOf(t, srv, map[string]any{"stream": "dup", "entry_id": "key"})
	r := &fitsync.Record{Namespace: "x", Op: fitsync.OpMessage, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.String("5-1")}), After: fitsync.Map(one)}
	for range 2 {
		if err := ids.Write(context.Background(), []*fitsync.Record{r}); err != nil {
			t.Fatalf("a replayed entry: %v", err)
		}
	}
	r.Key = fitsync.String("0-0")
	if err := sinkOf(t, srv, map[string]any{"stream": "zero", "entry_id": "key"}).Write(context.Background(), []*fitsync.Record{r}); !fitsync.IsPermanent(err) {
		t.Fatalf("0-0: %v", err)
	}
	// The server gone: an error that may pass, after the records written.
	srv.ln.Close()
	s.conn.close()
	if err := s.Write(context.Background(), batch[:1]); err == nil || fitsync.IsPermanent(err) {
		t.Fatalf("no server: %v", err)
	}
}

// TestSinkMultiErrors fails a hash's record whose MULTI is refused, queueing
// or inside EXEC.
func TestSinkMultiErrors(t *testing.T) {
	srv := newServer(t)
	hs := sinkOf(t, srv, map[string]any{"mode": "keys", "value": "hash", "key": "{namespace}"})
	one := fitsync.Field{Name: "v", Value: fitsync.Int(1)}
	for _, tc := range []struct {
		inject func(args []string) (string, bool)
		want   string
	}{
		{func(args []string) (string, bool) {
			if args[0] == "HSET" && args[1] == "b" {
				return errWrongType, true
			}
			return "", false
		}, "WRONGTYPE"},
		{func(args []string) (string, bool) {
			if args[0] == "EXEC" {
				return "-EXECABORT Transaction discarded because of previous errors.\r\n", true
			}
			return "", false
		}, ""},
	} {
		srv.setInject(tc.inject)
		err := hs.Write(context.Background(), []*fitsync.Record{rec("a", fitsync.OpInsert, 1, one), rec("b", fitsync.OpInsert, 2, one)})
		var be *fitsync.BatchError
		if tc.want != "" && (!errors.As(err, &be) || be.Index != 1 || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%v", err)
		}
	}
	// MULTI answered with an error, or EXEC.
	it := item{cmds: [][]string{{"MULTI"}, {"DEL", "k"}, {"EXEC"}}, multi: true}
	ok, bad := wire.Value{Kind: wire.KindString, Str: "OK"}, wire.Value{Kind: wire.KindError, Str: "ERR no"}
	if err := hs.itemError(it, []wire.Value{bad, ok, ok}); err == nil {
		t.Error("MULTI's error passed")
	}
	if err := hs.itemError(it, []wire.Value{ok, ok, bad}); err == nil {
		t.Error("EXEC's error passed")
	}
}

// TestSourceReads reads streams in RESP3 and RESP2, starting from a
// checkpoint for one stream and after the last entry for the other, and
// ends a job when nothing more comes.
func TestSourceReads(t *testing.T) {
	for _, protocol := range []int{3, 2} {
		t.Run(strconv.Itoa(protocol), func(t *testing.T) {
			srv := newServer(t)
			srv.streams["a"] = []entryOf{{999, 0, []string{"n", "0"}}, {1000, 0, []string{"n", "1", "b", "\xff\xfe"}}, {1000, 1, []string{"record", "bad"}}}
			doc := func(r *fitsync.Record) string {
				b, err := r.AppendJSON(nil)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			srv.streams["b"] = []entryOf{{900, 0, []string{"n", "0"}},
				{2000, 0, []string{"record", doc(&fitsync.Record{Op: fitsync.OpDelete, Key: fitsync.Int(7), Namespace: "orig", Tx: fitsync.Tx{ID: "x", First: true}})}},
				{2001, 0, []string{"record", doc(&fitsync.Record{Op: fitsync.OpInsert, Key: fitsync.Int(8), After: fitsync.Map()})}},
				{2002, 0, []string{"x", "y", "record", doc(&fitsync.Record{Op: fitsync.OpInsert, Key: fitsync.Int(9), CommitTime: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)})}}}
			src := sourceOf(t, srv, map[string]any{"streams": []string{"a", "b"}, "form": "record", "snapshot": "never", "count": 7, "protocol": protocol})
			// b's last entry, 2002-0, is where never starts: rewind it to 900-0.
			srv.setInject(func(args []string) (string, bool) {
				if args[0] == "XREVRANGE" {
					return arr(arr(bulk("900-0"), arr(bulk("n"), bulk("0")))), true
				}
				return "", false
			})
			var got []*fitsync.Record
			err := src.Run(context.Background(), &fitsync.Run{From: fitsync.Position(`{"streams":{"a":"999-0"}}`), Once: true, Emit: func(r *fitsync.Record) error {
				got = append(got, r)
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			sent := srv.sent()
			if len(sent) != 3 || sent[0] != "XREVRANGE b + - COUNT 1" || sent[1] != "XREAD COUNT 7 STREAMS a b 999-0 900-0" || sent[2] != "XREAD COUNT 7 STREAMS a b 1000-1 2002-0" {
				t.Fatalf("sent %q", sent)
			}
			if len(got) != 5 {
				t.Fatalf("%d records", len(got))
			}
			n, _ := got[0].After.Get("n")
			b, _ := got[0].After.Get("b")
			id, _ := got[0].Key.Get("id")
			if got[0].Op != fitsync.OpMessage || got[0].Namespace != "a" || n.Text() != "1" || b.Kind() != fitsync.KindBytes || id.Text() != "1000-0" ||
				!got[0].CommitTime.Equal(time.UnixMilli(1000)) || string(got[0].Position) != `{"streams":{"a":"1000-0","b":"900-0"}}` {
				t.Errorf("record 0: %+v %s", got[0], got[0].Position)
			}
			if rec, _ := got[1].After.Get("record"); got[1].Op != fitsync.OpMessage || rec.Text() != "bad" {
				t.Errorf("record 1, not a record: %+v", got[1])
			}
			if got[2].Op != fitsync.OpDelete || got[2].Namespace != "orig" || got[2].Key.AsInt() != 7 || got[2].Source != "redis" || got[2].Tx.ID != "" ||
				!got[2].CommitTime.Equal(time.UnixMilli(2000)) || string(got[2].Position) != `{"streams":{"a":"1000-1","b":"2000-0"}}` {
				t.Errorf("record 2: %+v %s", got[2], got[2].Position)
			}
			if got[3].Namespace != "b" || got[4].CommitTime.Year() != 2026 {
				t.Errorf("records 3, 4: %+v %+v", got[3], got[4])
			}
			// A namespace of the source's own names every record.
			src = sourceOf(t, srv, map[string]any{"streams": []string{"b"}, "form": "record", "namespace": "ns", "protocol": protocol})
			got = got[:0]
			if err := src.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(r *fitsync.Record) error { got = append(got, r); return nil }}); err != nil {
				t.Fatal(err)
			}
			for _, r := range got {
				if r.Namespace != "ns" {
					t.Errorf("namespace %s", r.Namespace)
				}
			}
		})
	}
}

// TestSourceWaits reads entries as they are added, with BLOCK, until ctx
// ends; a stream added to the config after the checkpoint starts at its
// first entry; Emit's error ends Run.
func TestSourceWaits(t *testing.T) {
	srv := newServer(t)
	srv.old(false)
	src := sourceOf(t, srv, map[string]any{"streams": []string{"s", "t"}, "block": "10ms"})
	srv.mu.Lock()
	srv.streams["t"] = []entryOf{{1, 0, []string{"a", "1"}}}
	srv.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan *fitsync.Record, 10)
	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, &fitsync.Run{From: fitsync.Position(`{"streams":{"s":"0-0","gone":"1-1"}}`), Emit: func(r *fitsync.Record) error {
			got <- r
			return nil
		}})
	}()
	if r := <-got; r.Namespace != "t" {
		t.Fatalf("%+v", r)
	}
	time.Sleep(30 * time.Millisecond) // a few XREADs find nothing
	srv.mu.Lock()
	srv.streams["s"] = []entryOf{{5, 0, []string{"a", "2"}}}
	srv.mu.Unlock()
	if r := <-got; r.Namespace != "s" || string(r.Position) != `{"streams":{"s":"5-0","t":"1-0"}}` {
		t.Fatalf("%+v %s", r, r.Position)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	if err := src.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }}); err != stop {
		t.Fatal(err)
	}
}

// TestSourceErrors fails Run on a bad checkpoint, a server that refuses, a
// reply XREAD would not give, and a server gone.
func TestSourceErrors(t *testing.T) {
	srv := newServer(t)
	srv.streams["s"] = []entryOf{{1, 0, []string{"a", "1"}}}
	run := func(src *Source, from string) error {
		var p fitsync.Position
		if from != "" {
			p = fitsync.Position(from)
		}
		return src.Run(context.Background(), &fitsync.Run{From: p, Once: true, Emit: func(*fitsync.Record) error { return nil }})
	}
	src := sourceOf(t, srv, map[string]any{"streams": []string{"s"}, "snapshot": "never"})
	for _, tc := range []struct{ from, want string }{{`{`, "the checkpoint"}, {`{"streams":{"s":"x"}}`, "the checkpoint's ID"}} {
		if err := run(src, tc.from); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.from, err)
		}
	}
	for _, tc := range []struct {
		reply, want string
	}{
		{"-NOPERM no\r\n", "NOPERM"},
		{":1\r\n", "answered a integer"},
		{arr(arr(bulk("other"), arr())), "not asked for"},
		{arr(arr(bulk("s"), bulk("x"))), "answered a array"},
		{arr(arr(bulk("s"))), "answered a array"},
		{"%1\r\n" + bulk("s") + ":1\r\n", "answered a map"},
		{arr(arr(bulk("s"), arr(arr(bulk("1-0"))))), "an entry that is"},
		{arr(arr(bulk("s"), arr(arr(bulk("x"), arr())))), "an entry ID"},
		{arr(arr(bulk("s"), arr(arr(bulk("1-0"), arr(bulk("odd")))))), "fields that are"},
	} {
		srv.setInject(func(args []string) (string, bool) {
			if args[0] == "XREAD" {
				return tc.reply, true
			}
			return "", false
		})
		if err := run(src, `{"streams":{"s":"0-0"}}`); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want %q", tc.reply, err, tc.want)
		}
	}
	// A reply of no entries ends a job.
	srv.setInject(func(args []string) (string, bool) {
		if args[0] == "XREAD" {
			return arr(arr(bulk("s"), arr())), true
		}
		return "", false
	})
	if err := run(src, `{"streams":{"s":"0-0"}}`); err != nil {
		t.Errorf("no entries: %v", err)
	}
	// A null entry's fields are none.
	srv.setInject(func(args []string) (string, bool) {
		if args[0] == "XREAD" && args[len(args)-1] == "0-0" {
			return arr(arr(bulk("s"), arr(arr(bulk("1-0"), "*-1\r\n")))), true
		}
		return "", false
	})
	if err := run(src, `{"streams":{"s":"0-0"}}`); err != nil {
		t.Errorf("a null entry: %v", err)
	}
	srv.setInject(func(args []string) (string, bool) {
		if args[0] == "XREVRANGE" {
			return "-ERR no\r\n", true
		}
		return "", false
	})
	if err := run(src, ""); err == nil || !strings.Contains(err.Error(), "the last entry") {
		t.Errorf("XREVRANGE: %v", err)
	}
	srv.setInject(nil)
	srv.ln.Close()
	src.conn.close()
	if err := run(src, `{"streams":{"s":"0-0"}}`); err == nil {
		t.Error("no server")
	}
	if err := run(src, ""); err == nil {
		t.Error("no server, snapshot never")
	}
}

func (s *server) old(noHello bool) {
	s.mu.Lock()
	s.noHello = noHello
	s.mu.Unlock()
}

// TestCopyAcrossKills copies a stream into another through a pipeline,
// killed at random points and started again from its checkpoint, with
// entry IDs from the source's: the copy has every entry once.
func TestCopyAcrossKills(t *testing.T) {
	srv := newServer(t)
	srv.old(true) // and in RESP2, as before Redis 6
	const total = 600
	for i := range total {
		srv.streams["src"] = append(srv.streams["src"], entryOf{uint64(1000 + i/3), uint64(i % 3), []string{"n", strconv.Itoa(i)}})
	}
	stateDir := t.TempDir()
	cfg := pipelineConfig(t, stateDir, map[string]any{"checkpoint_interval": "1ms", "queue": map[string]any{"max_records": 30},
		"source": map[string]any{"module": "redis", "params": map[string]any{"address": srv.addr(), "streams": []string{"src"}, "count": 17, "block": "5ms"}},
		"sinks": []any{map[string]any{"name": "r", "batch": map[string]any{"max_records": 9, "max_wait": "1ms"},
			"sink": map[string]any{"module": "redis", "params": map[string]any{"address": srv.addr(), "stream": "dst", "entry_id": "key", "max_pipeline": 4}}}}})
	points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
		fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
	copied := func() int {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.streams["dst"])
	}
	for round := 0; round < 12 && copied() < total; round++ {
		var seen atomic.Int32
		at, nth := points[round%len(points)], int32(1+round*7)
		rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool { return p == at && seen.Add(1) == nth }})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		rt.Close(context.Background())
	}
	rt := fitsync.NewRuntime(fitsync.Options{})
	if err := rt.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute) // a hang guard
	for copied() < total && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	rt.Close(context.Background())
	srv.mu.Lock()
	defer srv.mu.Unlock()
	src, dst := srv.streams["src"], srv.streams["dst"]
	if fmt.Sprint(src) != fmt.Sprint(dst) {
		t.Fatalf("the copy has %d entries of %d", len(dst), len(src))
	}
}

func TestParseID(t *testing.T) {
	for id, ok := range map[string]bool{"0-0": true, "1700000000000-12": true, "1": false, "a-1": false, "1-": false, "-1-1": false} {
		if _, _, got := ParseID(id); got != ok {
			t.Errorf("%q: %v", id, got)
		}
	}
}

func TestTransient(t *testing.T) {
	srv := newServer(t)
	srv.setInject(func(args []string) (string, bool) {
		if args[0] == "XADD" {
			return "-" + args[1] + " x\r\n", true
		}
		return "", false
	})
	s := sinkOf(t, srv, map[string]any{})
	for code, want := range map[string]bool{"LOADING": true, "READONLY": true, "NOPERM": true, "ERR": false, "WRONGTYPE": false} {
		err := s.Write(context.Background(), []*fitsync.Record{rec(code, fitsync.OpInsert, 1, fitsync.Field{Name: "a", Value: fitsync.Int(1)})})
		if err == nil || fitsync.IsPermanent(err) == want {
			t.Errorf("%s: %v", code, err)
		}
	}
}
