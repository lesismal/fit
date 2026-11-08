package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/redis"
	"github.com/lesismal/fit/sync/redis/wire"
)

// redisServer is a Redis or Valkey server the tests use, through an ACL
// user, from FIT_SYNC_REDIS_ADDR (and FIT_SYNC_REDIS_TLS_ADDR with
// FIT_SYNC_REDIS_CA, its TLS port) and FIT_SYNC_VALKEY_ADDR, the user
// FIT_SYNC_REDIS_USER with FIT_SYNC_REDIS_PASSWORD.
type redisServer struct {
	name, addr, tlsAddr, ca, user, password string
	protocol                                int
}

func redisServers(t *testing.T) []*redisServer {
	t.Helper()
	var out []*redisServer
	user, password := os.Getenv("FIT_SYNC_REDIS_USER"), os.Getenv("FIT_SYNC_REDIS_PASSWORD")
	for _, s := range []struct{ name, env string }{{"redis", "REDIS"}, {"valkey", "VALKEY"}} {
		addr := os.Getenv("FIT_SYNC_" + s.env + "_ADDR")
		if addr == "" {
			if os.Getenv("FIT_SYNC_INTEROP") == "required" {
				t.Fatalf("FIT_SYNC_%s_ADDR is not set", s.env)
			}
			continue
		}
		out = append(out, &redisServer{name: s.name, addr: addr, user: user, password: password,
			tlsAddr: os.Getenv("FIT_SYNC_" + s.env + "_TLS_ADDR"), ca: os.Getenv("FIT_SYNC_" + s.env + "_CA")})
	}
	if len(out) == 0 {
		t.Skip("no Redis: FIT_SYNC_REDIS_ADDR and FIT_SYNC_VALKEY_ADDR are not set (test/sync/run.sh sets them)")
	}
	return out
}

// each runs f on each server, in RESP3 and in RESP2.
func eachRedis(t *testing.T, f func(t *testing.T, s *redisServer)) {
	for _, s := range redisServers(t) {
		for _, p := range []int{3, 2} {
			t.Run(fmt.Sprintf("%s/resp%d", s.name, p), func(t *testing.T) {
				c := *s
				c.protocol = p
				f(t, &c)
			})
		}
	}
}

// do runs a command as the test's user, failing the test on any error.
func (s *redisServer) do(t *testing.T, args ...string) wire.Value {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := wire.Dial(ctx, wire.Options{Addr: s.addr, Username: s.user, Password: s.password, Protocol: s.protocol})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	v, err := c.Do(ctx, args...)
	if err == nil {
		err = v.Err()
	}
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return v
}

// key returns a key of the test's own, deleted first.
func (s *redisServer) key(t *testing.T, name string) string {
	k := fmt.Sprintf("fit-sync-test:%s:%s:%d", strings.ReplaceAll(t.Name(), "/", ":"), name, rand.Uint64())
	t.Cleanup(func() { s.do(t, "DEL", k) })
	return k
}

func (s *redisServer) params(extra map[string]any) map[string]any {
	p := map[string]any{"address": s.addr, "protocol": s.protocol}
	if s.password != "" {
		p["password_env"] = "FIT_TEST_SYNC_REDIS_PASSWORD"
		if s.user != "" {
			p["username"] = s.user
		}
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// pipeline loads a config of one pipeline.
func (s *redisServer) pipeline(t *testing.T, stateDir string, p map[string]any) *config.Sync {
	t.Helper()
	t.Setenv("FIT_TEST_SYNC_REDIS_PASSWORD", s.password)
	p["name"] = "p"
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Sync
}

func (s *redisServer) sink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Helper()
	cfg := s.pipeline(t, t.TempDir(), map[string]any{"source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "redis", "params": s.params(params)}}}})
	m, err := module.Load(fitsync.Sinks, cfg.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Sink)
}

func (s *redisServer) source(t *testing.T, params map[string]any) fitsync.Source {
	t.Helper()
	cfg := s.pipeline(t, t.TempDir(), map[string]any{"source": map[string]any{"module": "redis", "params": s.params(params)},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}})
	m, err := module.Load(fitsync.Sources, cfg.Pipelines[0].Source, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Source)
}

// entries returns a stream's entries as id=field:value,... lines.
func (s *redisServer) entries(t *testing.T, stream string) []string {
	t.Helper()
	var out []string
	for _, e := range s.do(t, "XRANGE", stream, "-", "+").Elems {
		id, _ := e.Elems[0].Text()
		var fs []string
		for i := 0; i+1 < len(e.Elems[1].Elems); i += 2 {
			k, _ := e.Elems[1].Elems[i].Text()
			v, _ := e.Elems[1].Elems[i+1].Text()
			fs = append(fs, k+":"+v)
		}
		out = append(out, id+"="+strings.Join(fs, ","))
	}
	return out
}

// TestRedisStreamCopyAcrossKills copies a stream to another while entries
// are added to it, the pipeline killed at random points and started again
// from its checkpoint, the sink taking entry IDs from the source's: the copy
// ends with every entry, once, with its ID and fields.
func TestRedisStreamCopyAcrossKills(t *testing.T) {
	eachRedis(t, func(t *testing.T, s *redisServer) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 7))
		src, dst := s.key(t, "src"), s.key(t, "dst")
		stateDir := t.TempDir()
		cfg := s.pipeline(t, stateDir, map[string]any{"checkpoint_interval": "1ms",
			"queue":  map[string]any{"max_records": 20 + rnd.IntN(200)},
			"source": map[string]any{"module": "redis", "params": s.params(map[string]any{"streams": []string{src}, "count": 1 + rnd.IntN(100), "block": "20ms"})},
			"sinks": []any{map[string]any{"name": "r", "batch": map[string]any{"max_records": 1 + rnd.IntN(50), "max_wait": "1ms"},
				"sink": map[string]any{"module": "redis", "params": s.params(map[string]any{"stream": dst, "entry_id": "key", "max_pipeline": 1 + rnd.IntN(40)})}}}})
		if err := fitsync.Check(cfg); err != nil {
			t.Fatal(err)
		}

		const total = 3000
		var wrote atomic.Int64
		var writing sync.WaitGroup
		writing.Go(func() {
			ctx := context.Background()
			c, err := wire.Dial(ctx, wire.Options{Addr: s.addr, Username: s.user, Password: s.password})
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			for n := 0; n < total; {
				var cmds [][]string
				for range 1 + rand.IntN(20) {
					if n == total {
						break
					}
					cmds = append(cmds, []string{"XADD", src, "*", "n", strconv.Itoa(n), "pad", strings.Repeat("x", n%37)})
					n++
				}
				if _, err := c.Pipeline(ctx, cmds); err != nil {
					t.Error(err)
					return
				}
				wrote.Store(int64(n))
				time.Sleep(time.Millisecond)
			}
		})

		points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
			fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
		kills := 0
		for round := 0; wrote.Load() < total || round < 12; round++ {
			at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(100))
			var seen atomic.Int32
			rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
				return p == at && seen.Add(1) == nth
			}})
			if err := rt.Apply(cfg); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Duration(50+rnd.IntN(200)) * time.Millisecond)
			for time.Now().Before(deadline) && redisState(rt) != "killed" {
				time.Sleep(time.Millisecond)
			}
			if redisState(rt) == "killed" {
				kills++
			}
			rt.Close(context.Background())
		}
		writing.Wait()
		rt := fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(cfg); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Minute) // a hang guard
		for s.do(t, "XLEN", dst).Int < total {
			if time.Now().After(deadline) {
				t.Fatalf("%d of %d entries copied", s.do(t, "XLEN", dst).Int, total)
			}
			time.Sleep(5 * time.Millisecond)
		}
		rt.Close(context.Background())
		want, got := s.entries(t, src), s.entries(t, dst)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("the copy has %d entries, the stream %d", len(got), len(want))
		}
		t.Logf("%d entries copied across %d kills", total, kills)
	})
}

func redisState(rt *fitsync.Runtime) string {
	for _, st := range rt.Status() {
		return st.State
	}
	return ""
}

// redisChanges is a stream of inserts, updates and deletes of 100 ids in
// namespace ns, and the rows it leaves, by id.
func redisChanges(ns string) ([]*fitsync.Record, map[int64]string) {
	var recs []*fitsync.Record
	want := map[int64]string{}
	add := func(op fitsync.Op, id int64, v string) {
		r := &fitsync.Record{Namespace: ns, Op: op, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}),
			CommitTime: time.Unix(1_700_000_000+int64(len(recs)), 0), Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, len(recs)))}
		row := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(id)}, fitsync.Field{Name: "v", Value: fitsync.String(v)},
			fitsync.Field{Name: "gone", Value: fitsync.Null()})
		if op == fitsync.OpDelete {
			r.Before = row
			delete(want, id)
		} else {
			r.After = row
			want[id] = v
		}
		recs = append(recs, r)
	}
	for i := range int64(100) {
		add(fitsync.OpInsert, i, "v1")
	}
	for i := range int64(100) {
		switch {
		case i%10 == 0:
			add(fitsync.OpDelete, i, "")
			add(fitsync.OpInsert, i, "v3")
		case i%5 == 0:
			add(fitsync.OpDelete, i, "")
		case i%2 == 0:
			add(fitsync.OpUpdate, i, "v2")
		}
	}
	return recs, want
}

func writeRedis(t *testing.T, s fitsync.Sink, recs []*fitsync.Record, size int) {
	t.Helper()
	for i := 0; i < len(recs); i += size {
		if err := s.Write(context.Background(), recs[i:min(i+size, len(recs))]); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRedisKeys writes changes as keys, as JSON, records and hashes, then
// replays them from the middle and from the start: the keys hold each id's
// last state, deleted ids none, with the TTL set.
func TestRedisKeys(t *testing.T) {
	eachRedis(t, func(t *testing.T, s *redisServer) {
		for _, value := range []string{"json", "record", "hash"} {
			t.Run(value, func(t *testing.T) {
				ns := s.key(t, value)
				recs, want := redisChanges(ns)
				t.Cleanup(func() {
					for id := range 100 {
						s.do(t, "DEL", fmt.Sprintf("%s:%d", ns, id))
					}
				})
				sk := s.sink(t, map[string]any{"mode": "keys", "value": value, "ttl": "1h", "max_pipeline": 7})
				writeRedis(t, sk, recs, 37)
				writeRedis(t, sk, recs[len(recs)/2:], 23)
				writeRedis(t, sk, recs, 101)
				for id := range int64(100) {
					key := fmt.Sprintf("%s:%d", ns, id)
					v, ok := want[id]
					if !ok {
						if n := s.do(t, "EXISTS", key).Int; n != 0 {
							t.Fatalf("%s is there after its delete", key)
						}
						continue
					}
					var got string
					switch value {
					case "json":
						got, _ = s.do(t, "GET", key).Text()
						if w := fmt.Sprintf(`{"id":%d,"v":%q,"gone":null}`, id, v); got != w {
							t.Fatalf("%s holds %s, want %s", key, got, w)
						}
					case "record":
						doc, _ := s.do(t, "GET", key).Text()
						r, err := fitsync.ParseRecord([]byte(doc))
						if err != nil {
							t.Fatal(err)
						}
						if f, _ := r.After.Get("v"); f.Text() != v {
							t.Fatalf("%s holds %s", key, doc)
						}
					case "hash":
						h, _ := s.do(t, "HGETALL", key).Pairs()
						var fs []string
						for i := 0; i+1 < len(h); i += 2 {
							k, _ := h[i].Text()
							x, _ := h[i+1].Text()
							fs = append(fs, k+"="+x)
						}
						if got, w := strings.Join(fs, ","), fmt.Sprintf("id=%d,v=%s", id, v); got != w {
							t.Fatalf("%s holds %s, want %s", key, got, w)
						}
					}
					if ttl := s.do(t, "PTTL", key).Int; ttl <= 0 || ttl > time.Hour.Milliseconds() {
						t.Fatalf("%s has a TTL of %dms", key, ttl)
					}
				}
			})
		}
	})
}

// TestRedisRecordsRoundTrip writes records to a stream in form record, with
// the operation in a field too in form fields, and reads them back with the
// source: the records come back with their operation, key and rows, then
// the source, in a job, stops at the stream's end.
func TestRedisRecordsRoundTrip(t *testing.T) {
	eachRedis(t, func(t *testing.T, s *redisServer) {
		stream, plain := s.key(t, "records"), s.key(t, "plain")
		recs, _ := redisChanges("shop.items")
		writeRedis(t, s.sink(t, map[string]any{"stream": stream, "form": "record"}), recs, 50)
		writeRedis(t, s.sink(t, map[string]any{"stream": plain, "op_field": "op"}), recs, 50)
		got := collect(t, s.source(t, map[string]any{"streams": []string{stream, plain}, "form": "record", "count": 33}), nil, true, 0)
		if len(got) != 2*len(recs) {
			t.Fatalf("%d records, want %d", len(got), 2*len(recs))
		}
		var fromRecords, fromPlain []*fitsync.Record
		for _, r := range got {
			if r.Namespace == "shop.items" {
				fromRecords = append(fromRecords, r)
			} else if r.Namespace == plain {
				fromPlain = append(fromPlain, r)
			}
		}
		if len(fromRecords) != len(recs) || len(fromPlain) != len(recs) {
			t.Fatalf("%d records of form record, %d of fields", len(fromRecords), len(fromPlain))
		}
		for i, r := range fromRecords {
			w := recs[i]
			if r.Op != w.Op || !r.Key.Equal(w.Key) || !r.After.Equal(w.After) || !r.Before.Equal(w.Before) || !r.CommitTime.Equal(w.CommitTime) || r.Source != "redis" {
				t.Fatalf("record %d is %s %v, want %s %v", i, r.Op, r.Key, w.Op, w.Key)
			}
		}
		for i, r := range fromPlain {
			op, _ := r.After.Get("op")
			id, _ := r.After.Get("id")
			wid, _ := recs[i].Key.Get("id")
			if r.Op != fitsync.OpMessage || op.Text() != recs[i].Op.String() || id.Text() != strconv.FormatInt(wid.AsInt(), 10) {
				t.Fatalf("entry %d is %v", i, r.After)
			}
			if _, ok := r.After.Get("gone"); ok {
				t.Fatalf("entry %d has the null field: %v", i, r.After)
			}
		}
		// Resumed from the middle, the rest comes, and nothing before it.
		mid := got[len(got)/2]
		rest := collect(t, s.source(t, map[string]any{"streams": []string{stream, plain}, "form": "record"}), mid.Position, true, 0)
		if len(rest) != len(got)-len(got)/2-1 {
			t.Fatalf("%d records after the position %s, want %d", len(rest), mid.Position, len(got)-len(got)/2-1)
		}
	})
}

// TestRedisSnapshotNever starts after the entries there are, and waits for
// the next.
func TestRedisSnapshotNever(t *testing.T) {
	eachRedis(t, func(t *testing.T, s *redisServer) {
		stream := s.key(t, "never")
		for i := range 10 {
			s.do(t, "XADD", stream, "*", "n", strconv.Itoa(i))
		}
		src := s.source(t, map[string]any{"streams": []string{stream}, "snapshot": "never", "block": "50ms"})
		done := make(chan []*fitsync.Record, 1)
		go func() { done <- collect(t, src, nil, false, 1) }()
		for i := 10; ; i++ {
			s.do(t, "XADD", stream, "*", "n", strconv.Itoa(i))
			select {
			case got := <-done:
				n, _ := got[0].After.Get("n")
				if i, err := strconv.Atoi(n.Text()); len(got) != 1 || err != nil || i < 10 {
					t.Errorf("read %v", got[0].After)
				}
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	})
}

// TestRedisTLSAndACL writes and reads over TLS as an ACL user; a wrong
// password is refused.
func TestRedisTLSAndACL(t *testing.T) {
	var srv *redisServer
	for _, s := range redisServers(t) {
		if s.tlsAddr != "" {
			srv = s
		}
	}
	if srv == nil {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_REDIS_TLS_ADDR is not set")
		}
		t.Skip("no TLS port: FIT_SYNC_REDIS_TLS_ADDR is not set")
	}
	if srv.user == "" {
		t.Skip("no ACL user: FIT_SYNC_REDIS_USER is not set")
	}
	stream := srv.key(t, "tls")
	tlsParams := map[string]any{"address": srv.tlsAddr, "tls": true, "ca_file": srv.ca, "server_name": "localhost", "stream": stream}
	writeRedis(t, srv.sink(t, tlsParams), []*fitsync.Record{{Namespace: "n", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})}}, 1)
	if n := srv.do(t, "XLEN", stream).Int; n != 1 {
		t.Fatalf("XLEN %d", n)
	}
	got := collect(t, srv.source(t, map[string]any{"address": srv.tlsAddr, "tls": true, "ca_file": srv.ca, "server_name": "localhost", "streams": []string{stream}}), nil, true, 0)
	if len(got) != 1 {
		t.Fatalf("%d records", len(got))
	}
	wrong := *srv
	wrong.password = "not-" + srv.password
	err := wrong.sink(t, map[string]any{"stream": stream}).Write(context.Background(), []*fitsync.Record{{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})}})
	if err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("a wrong password: %v", err)
	}
}
