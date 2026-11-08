package syncmongo

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
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// sinkLoad loads the sink mongo with params, as a pipeline's config would.
func sinkLoad(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	p := map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "mongo", "params": params}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	m, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Sink), nil
}

func sinkFor(t *testing.T, f *sinkFake, extra map[string]any) *Sink {
	t.Helper()
	p := map[string]any{"hosts": []string{f.addr}, "timeout": "20s", "connect_timeout": "20s"}
	for k, v := range extra {
		p[k] = v
	}
	s, err := sinkLoad(t, p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sinkCert writes a self-signed certificate and its key, apart and in one
// file, and returns their paths.
func sinkCert(t *testing.T) (cert, key, both string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "c"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	kder, _ := x509.MarshalECPrivateKey(k)
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kp := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	dir := t.TempDir()
	cert, key, both = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), filepath.Join(dir, "both.pem")
	os.WriteFile(cert, cp, 0o600)
	os.WriteFile(key, kp, 0o600)
	os.WriteFile(both, append(cp, kp...), 0o600)
	return cert, key, both
}

func TestSinkParams(t *testing.T) {
	cert, key, both := sinkCert(t)
	notPEM := filepath.Join(t.TempDir(), "x")
	os.WriteFile(notPEM, []byte("x"), 0o600)
	t.Setenv("FIT_TEST_MONGO_PW", "secret")
	bad := []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{}, "uri or hosts"},
		{map[string]any{"uri": "mongodb://h", "hosts": []string{"h"}}, "set one"},
		{map[string]any{"uri": "mongodb://u:p@h"}, "holds a password"},
		{map[string]any{"uri": "http://h"}, "mongodb://"},
		{map[string]any{"hosts": []string{"a,b"}}, "is not a host"},
		{map[string]any{"uri": "mongodb://u@h", "username": "v", "password_env": "FIT_TEST_MONGO_PW"}, "names a user already"},
		{map[string]any{"hosts": []string{"h"}, "username": "u"}, "needs password_env"},
		{map[string]any{"hosts": []string{"h"}, "password_env": "FIT_TEST_MONGO_PW"}, "needs username"},
		{map[string]any{"hosts": []string{"h"}, "username": "u", "password_env": "1x"}, "not an environment variable"},
		{map[string]any{"hosts": []string{"h"}, "username": "u", "password_env": "FIT_TEST_MONGO_UNSET"}, "not set"},
		{map[string]any{"hosts": []string{"h"}, "ca_file": cert}, "needs tls"},
		{map[string]any{"hosts": []string{"h"}, "tls": true, "ca_file": filepath.Join(t.TempDir(), "none")}, "ca_file"},
		{map[string]any{"hosts": []string{"h"}, "tls": true, "ca_file": notPEM}, "no PEM"},
		{map[string]any{"hosts": []string{"h"}, "tls": true, "key_file": key}, "needs cert_file"},
		{map[string]any{"hosts": []string{"h"}, "tls": true, "cert_file": notPEM}, "cert_file"},
		{map[string]any{"hosts": []string{"h"}, "write_concern": "0"}, "unacknowledged"},
		{map[string]any{"hosts": []string{"h"}, "database": "a.b"}, "database"},
		{map[string]any{"hosts": []string{"h"}, "collection": "$x"}, "collection"},
	}
	for _, tt := range bad {
		if _, err := sinkLoad(t, tt.params); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: %v, want %q", tt.params, err, tt.want)
		}
	}
	s, err := sinkLoad(t, map[string]any{"uri": "mongodb://u@h1,h2/?replicaSet=rs", "password_env": "FIT_TEST_MONGO_PW",
		"replica_set": "rs2", "auth_source": "users", "tls": true, "ca_file": cert, "cert_file": both, "server_name": "db",
		"connect_timeout": "3s", "timeout": "4s", "max_conns": 9, "write_concern": "majority", "journal": true, "write_timeout": "5s"})
	if err != nil {
		t.Fatal(err)
	}
	o := s.client.Options()
	if o.Username != "u" || o.Password != "secret" || o.ReplicaSet != "rs2" || o.AuthSource != "users" || o.TLS == nil ||
		o.TLS.ServerName != "db" || len(o.TLS.Certificates) != 1 || o.ConnectTimeout != 3*time.Second || o.Timeout != 4*time.Second ||
		o.MaxConns != 9 || o.AppName != "fit-sync" || len(o.Hosts) != 2 {
		t.Errorf("options %+v", o)
	}
	if got := mongowire.Raw(must(mongowire.Marshal(s.wc))).String(); got != `{"w":"majority","j":true,"wtimeout":5000}` {
		t.Errorf("write concern %s", got)
	}
	s, err = sinkLoad(t, map[string]any{"hosts": []string{"h"}, "tls": true, "cert_file": cert, "key_file": key, "write_concern": "2", "max_request_bytes": 100})
	if err != nil {
		t.Fatal(err)
	}
	if got := mongowire.Raw(must(mongowire.Marshal(s.wc))).String(); got != `{"w":2}` || s.maxBytes != 100 {
		t.Errorf("write concern %s, max bytes %d", got, s.maxBytes)
	}
	(&Sink{}).Cleanup()
	s.ParallelWrites()
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func rec(ns string, op fitsync.Op, key fitsync.Value, after fitsync.Value) *fitsync.Record {
	r := &fitsync.Record{Namespace: ns, Op: op, Key: key, After: after}
	if op == fitsync.OpDelete {
		r.Before, r.After = after, fitsync.Null()
	}
	return r
}

func row(id int64, v string) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)})
}

func idKey(id int64) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)})
}

func TestSinkWrites(t *testing.T) {
	f := newSinkFake(t)
	s := sinkFor(t, f, nil)
	ctx := context.Background()
	if err := s.Test(ctx); err != nil {
		t.Fatal(err)
	}
	oid := fitsync.Map(fitsync.Field{Name: "$oid", Value: fitsync.String("65a1b2c3d4e5f60718293a4b")})
	batch := []*fitsync.Record{
		rec("shop.orders", fitsync.OpInsert, idKey(1), row(1, "a")),
		rec("shop.orders", fitsync.OpInsert, idKey(2), row(2, "a")),
		rec("shop.orders", fitsync.OpUpdate, idKey(1), row(1, "b")),
		{Namespace: "shop.orders", Op: fitsync.OpDDL},
		rec("shop.orders", fitsync.OpDelete, idKey(2), row(2, "a")),
		rec("shop.orders", fitsync.OpInsert, idKey(3), row(3, "c")),
		rec("shop.items", fitsync.OpInsert, fitsync.Map(fitsync.Field{Name: "o", Value: fitsync.Int(1)}, fitsync.Field{Name: "n", Value: fitsync.Int(2)}), row(9, "x")),
		rec("shop.items", fitsync.OpMessage, fitsync.String("k"), row(8, "y")),
		rec("shop.items", fitsync.OpInsert, fitsync.Null(), fitsync.Map(fitsync.Field{Name: "_id", Value: fitsync.Int(7)}, fitsync.Field{Name: "v", Value: fitsync.String("z")})),
		rec("shop.items", fitsync.OpInsert, oid, fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.String("o")})),
		rec("shop.items", fitsync.OpUpdate, fitsync.Map(fitsync.Field{Name: "_id", Value: oid}), fitsync.Map(fitsync.Field{Name: "_id", Value: oid}, fitsync.Field{Name: "v", Value: fitsync.String("p")})),
		rec("shop.items", fitsync.OpDelete, fitsync.Null(), fitsync.Map(fitsync.Field{Name: "_id", Value: fitsync.Int(7)})),
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.dump("shop.orders"), " "); got != `{"_id":1,"id":1,"v":"b"} {"_id":3,"id":3,"v":"c"}` {
		t.Errorf("orders %s", got)
	}
	if got := strings.Join(f.dump("shop.items"), " "); got != `{"_id":"k","id":8,"v":"y"} {"_id":{"$oid":"65a1b2c3d4e5f60718293a4b"},"v":"p"} {"_id":{"o":1,"n":2},"id":9,"v":"x"}` {
		t.Errorf("items %s", got)
	}
	// One update and one delete a collection, each document's last record.
	if got := strings.Join(f.commands(), ","); got != "update shop.orders 2,delete shop.orders 1,update shop.items 3,delete shop.items 1" {
		t.Errorf("commands %s", got)
	}
	// A replay converges.
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if got := len(f.dump("shop.orders")) + len(f.dump("shop.items")); got != 5 {
		t.Errorf("%d documents after a replay", got)
	}
	if err := s.Write(ctx, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSinkTargets(t *testing.T) {
	f := newSinkFake(t)
	ctx := context.Background()
	s := sinkFor(t, f, map[string]any{"database": "dst"})
	if err := s.Write(ctx, []*fitsync.Record{rec("shop.orders", fitsync.OpInsert, idKey(1), row(1, "a")), rec("plain", fitsync.OpInsert, idKey(1), row(1, "a"))}); err != nil {
		t.Fatal(err)
	}
	s = sinkFor(t, f, map[string]any{"collection": "all"})
	if err := s.Write(ctx, []*fitsync.Record{rec("shop.orders", fitsync.OpInsert, idKey(2), row(2, "a"))}); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"dst.orders", "dst.plain", "shop.all"} {
		if len(f.dump(ns)) != 1 {
			t.Errorf("%s: %v", ns, f.dump(ns))
		}
	}
	s = sinkFor(t, f, nil)
	err := s.Write(ctx, []*fitsync.Record{rec("shop.orders", fitsync.OpInsert, idKey(3), row(3, "a")), rec("plain", fitsync.OpInsert, idKey(1), row(1, "a"))})
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "names no database") {
		t.Fatalf("%v", err)
	}
	if len(f.dump("shop.orders")) != 1 {
		t.Error("the record before the bad one was not written")
	}
}

func TestSinkBadRecords(t *testing.T) {
	f := newSinkFake(t)
	s := sinkFor(t, f, nil)
	defer func(n int) { sinkMaxDocBytes = n }(sinkMaxDocBytes)
	sinkMaxDocBytes = 200
	good := rec("d.c", fitsync.OpInsert, idKey(1), row(1, "a"))
	for _, tt := range []struct {
		r    *fitsync.Record
		want string
	}{
		{rec("d.c", fitsync.OpInsert, fitsync.Null(), row(1, "a")), "no key"},
		{rec("d.c", fitsync.OpInsert, fitsync.Map(), row(1, "a")), "no fields"},
		{rec("d.c", fitsync.OpInsert, fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Null()}), row(1, "a")), "is null"},
		{rec("d.c", fitsync.OpInsert, idKey(1), fitsync.String("x")), "not a map"},
		{rec("d.c", fitsync.OpInsert, fitsync.Decimal("x"), row(1, "a")), "_id"},
		{rec("d.c", fitsync.OpInsert, idKey(1), fitsync.Map(fitsync.Field{Name: "d", Value: fitsync.Decimal("1.00000000000000000000000000000000000001")})), "decimal"},
		{rec("d.c", fitsync.OpInsert, idKey(1), row(1, strings.Repeat("x", 300))), "more than"},
	} {
		err := s.Write(context.Background(), []*fitsync.Record{good, tt.r})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
			t.Errorf("%v, want %q", err, tt.want)
		}
	}
}

func TestSinkWriteErrors(t *testing.T) {
	f := newSinkFake(t)
	s := sinkFor(t, f, nil)
	ctx := context.Background()
	failing := map[string]int32{}
	f.set(func() {
		f.fail = func(_, _ string, st mongowire.Raw) int32 {
			u, ok := st.Lookup("u")
			if !ok {
				return 0
			}
			v, _ := u.Document().Lookup("v")
			sv, _ := v.StringValue()
			return failing[sv]
		}
	})
	// A document refused for good, its key once in the batch: named at once.
	failing["bad"] = 121
	batch := []*fitsync.Record{rec("d.c", fitsync.OpInsert, idKey(1), row(1, "a")), rec("d.c", fitsync.OpInsert, idKey(2), row(2, "bad")), rec("d.c", fitsync.OpInsert, idKey(3), row(3, "c"))}
	err := s.Write(ctx, batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if n := len(f.commands()); n != 1 {
		t.Errorf("%d commands", n)
	}
	// Its key earlier in the batch too: runs find the record that fails.
	f.set(func() { f.cmds = nil })
	batch = []*fitsync.Record{rec("d.c", fitsync.OpInsert, idKey(1), row(1, "a")), rec("d.c", fitsync.OpInsert, idKey(2), row(2, "ok")),
		rec("d.c", fitsync.OpInsert, idKey(3), row(3, "c")), rec("d.c", fitsync.OpUpdate, idKey(2), row(2, "bad")), {Namespace: "d.c", Op: fitsync.OpDDL}}
	if err := s.Write(ctx, batch); !errors.As(err, &be) || be.Index != 3 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if got := strings.Join(f.commands(), ","); got != "update d.c 3,update d.c 2,update d.c 1" {
		t.Errorf("commands %s", got)
	}
	// A conflict may pass.
	failing["bad"] = 112
	if err := s.Write(ctx, batch[:4]); !errors.As(err, &be) || be.Index != 3 || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	// Written once it passes.
	failing["bad"] = 0
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
}

func TestSinkCommandErrors(t *testing.T) {
	f := newSinkFake(t)
	s := sinkFor(t, f, map[string]any{"write_concern": "majority"})
	ctx := context.Background()
	batch := []*fitsync.Record{rec("d.a", fitsync.OpInsert, idKey(1), row(1, "a")), rec("d.a", fitsync.OpInsert, idKey(2), row(2, "a")),
		rec("d.b", fitsync.OpInsert, idKey(1), row(1, "a")), rec("d.a", fitsync.OpUpdate, idKey(1), row(1, "b"))}
	for _, tt := range []struct {
		action    string
		index     int
		permanent bool
	}{
		{"13", 2, false},    // unauthorized on d.b: d.a written, d.b's record named
		{"73", 2, true},     // an invalid namespace
		{"wce", 2, false},   // the write concern
		{"close", 2, false}, // the connection lost, twice
	} {
		f.set(func() {
			f.action = func(_, ns string) string {
				if ns == "d.b" {
					return tt.action
				}
				return ""
			}
		})
		err := s.Write(ctx, batch)
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != tt.index || fitsync.IsPermanent(err) != tt.permanent {
			t.Errorf("%s: %v", tt.action, err)
		}
	}
	// The first command failing names the first record of every document.
	f.set(func() {
		f.action = func(string, string) string { return "13" }
	})
	var be *fitsync.BatchError
	if err := s.Write(ctx, batch); !errors.As(err, &be) || be.Index != 0 {
		t.Errorf("%v", err)
	}
	f.set(func() { f.action = nil })
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	wc, ok := f.bodies[len(f.bodies)-1].Lookup("writeConcern")
	f.mu.Unlock()
	if !ok || wc.Document().String() != `{"w":"majority"}` {
		t.Errorf("write concern %v", wc)
	}
}

func TestSinkCommandsCut(t *testing.T) {
	f := newSinkFake(t)
	s := sinkFor(t, f, map[string]any{"max_request_bytes": 150})
	defer func(n int) { sinkMaxOps = n }(sinkMaxOps)
	sinkMaxOps = 3
	var batch []*fitsync.Record
	for i := range 10 {
		batch = append(batch, rec("d.c", fitsync.OpInsert, idKey(int64(i)), row(int64(i), "v")))
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	cmds := f.commands()
	if len(cmds) < 4 || len(f.dump("d.c")) != 10 {
		t.Errorf("commands %v, %d documents", cmds, len(f.dump("d.c")))
	}
	for _, c := range cmds {
		if n, _ := strconv.Atoi(c[strings.LastIndexByte(c, ' ')+1:]); n > 3 {
			t.Errorf("command %s", c)
		}
	}
}

func TestSinkDown(t *testing.T) {
	f := newSinkFake(t)
	f.ln.Close()
	s := sinkFor(t, f, map[string]any{"connect_timeout": "1s"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Test(ctx); err == nil {
		t.Error("a test of a server that is down passed")
	}
}

// TestSinkPipeline runs generate's records, of every type, into the fake.
func TestSinkPipeline(t *testing.T) {
	f := newSinkFake(t)
	p := map[string]any{"name": "p", "mode": "once", "source": map[string]any{"module": "generate", "params": map[string]any{"records": 300, "tx_size": 7}},
		"sinks": []any{map[string]any{"name": "m", "batch": map[string]any{"max_records": 50},
			"sink": map[string]any{"module": "mongo", "params": map[string]any{"hosts": []string{f.addr}}}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // a hang guard
	defer cancel()
	st, err := fitsync.RunPipeline(ctx, cfg.Sync, "p", fitsync.Options{})
	if err != nil {
		t.Fatalf("%v (%+v)", err, st)
	}
	n := 0
	f.mu.Lock()
	for _, docs := range f.docs {
		n += len(docs)
	}
	f.mu.Unlock()
	if n == 0 {
		t.Fatal("nothing written")
	}
	t.Logf("%d documents", n)
}
