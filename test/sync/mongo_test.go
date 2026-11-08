package synctest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// mongoSets are the replica sets of FIT_SYNC_MONGO_6, _7 and _8 (connection
// strings), each tried in a subtest of its own.
func mongoSets(t *testing.T) map[string]string {
	t.Helper()
	sets := map[string]string{}
	for _, v := range []string{"6", "7", "8"} {
		if u := os.Getenv("FIT_SYNC_MONGO_" + v); u != "" {
			sets["mongo"+v] = u
		}
	}
	if len(sets) == 0 {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_MONGO_6, _7 and _8 are not set")
		}
		t.Skip("no MongoDB: FIT_SYNC_MONGO_6, _7 and _8 are not set (test/sync/run.sh sets them)")
	}
	return sets
}

func mongoClient(t *testing.T, uri string, edit func(*mongowire.Options)) *mongowire.Client {
	t.Helper()
	o, err := mongowire.ParseURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(&o)
	}
	c, err := mongowire.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func mongoRun(t *testing.T, ctx context.Context, c *mongowire.Client, db string, cmd mongowire.D, seqs ...mongowire.Sequence) mongowire.Raw {
	t.Helper()
	r, err := c.Run(ctx, db, cmd, seqs...)
	if err != nil {
		t.Fatalf("%v: %v", cmd[0].Key, err)
	}
	return r
}

func TestMongoWire(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
			defer cancel()
			c := mongoClient(t, uri, nil)
			const db, coll = "fit_wire_test", "docs"
			_, _ = c.Run(ctx, db, mongowire.Doc("drop", coll))

			// Every BSON type, written from record values and read back.
			oid, _ := mongowire.ParseObjectID("65a1b2c3d4e5f60718293a4b")
			rec := fitsync.Map(
				fitsync.Field{Name: "_id", Value: fitsync.Int(-1)},
				fitsync.Field{Name: "dec", Value: fitsync.Decimal("-12.3400")},
				fitsync.Field{Name: "big", Value: fitsync.Uint(1 << 63)},
				fitsync.Field{Name: "f", Value: fitsync.Float(2.5)},
				fitsync.Field{Name: "s", Value: fitsync.String("héllo")},
				fitsync.Field{Name: "b", Value: fitsync.Bytes([]byte{0, 1, 2})},
				fitsync.Field{Name: "t", Value: fitsync.Time(time.UnixMilli(1_700_000_000_123).UTC())},
				fitsync.Field{Name: "d", Value: fitsync.Date(2024, 2, 29)},
				fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(`{"a":[1,"x",null]}`))},
				fitsync.Field{Name: "arr", Value: fitsync.Array(fitsync.Bool(true), fitsync.Null())},
				fitsync.Field{Name: "oid", Value: fitsync.Map(fitsync.Field{Name: "$oid", Value: fitsync.String(oid.String())})},
				fitsync.Field{Name: "ts", Value: fitsync.Map(fitsync.Field{Name: "$timestamp", Value: fitsync.Map(
					fitsync.Field{Name: "t", Value: fitsync.Int(1_700_000_000)}, fitsync.Field{Name: "i", Value: fitsync.Int(3)})})},
				fitsync.Field{Name: "uuid", Value: fitsync.Map(fitsync.Field{Name: "$binary", Value: fitsync.Map(
					fitsync.Field{Name: "base64", Value: fitsync.String("AAECAwQFBgcICQoLDA0ODw==")}, fitsync.Field{Name: "subType", Value: fitsync.String("04")})})},
				fitsync.Field{Name: "re", Value: fitsync.Map(fitsync.Field{Name: "$regularExpression", Value: fitsync.Map(
					fitsync.Field{Name: "pattern", Value: fitsync.String("^a")}, fitsync.Field{Name: "options", Value: fitsync.String("i")})})},
				fitsync.Field{Name: "min", Value: fitsync.Map(fitsync.Field{Name: "$minKey", Value: fitsync.Int(1)})},
				fitsync.Field{Name: "max", Value: fitsync.Map(fitsync.Field{Name: "$maxKey", Value: fitsync.Int(1)})},
				fitsync.Field{Name: "nan", Value: fitsync.Map(fitsync.Field{Name: "$numberDecimal", Value: fitsync.String("NaN")})},
			)
			special, err := mongowire.MarshalValue(rec)
			if err != nil {
				t.Fatal(err)
			}
			docs := []mongowire.Raw{special}
			for i := range 95 {
				d, _ := mongowire.Marshal(mongowire.Doc("_id", int64(i), "n", int32(i), "pad", strings.Repeat("x", i)))
				docs = append(docs, d)
			}
			r := mongoRun(t, ctx, c, db, mongowire.Doc("insert", coll, "ordered", true),
				mongowire.Sequence{ID: "documents", Docs: docs})
			if n, _ := mustLookup(t, r, "n").Int(); n != 96 {
				t.Fatalf("inserted %s", r)
			}

			// The server sees the types the values stand for.
			for field, typ := range map[string]string{"dec": "decimal", "big": "decimal", "t": "date", "d": "date",
				"j": "object", "oid": "objectId", "ts": "timestamp", "uuid": "binData", "re": "regex", "min": "minKey",
				"max": "maxKey", "nan": "decimal", "f": "double", "s": "string", "arr": "array", "b": "binData"} {
				r := mongoRun(t, ctx, c, db, mongowire.Doc("count", coll, "query", mongowire.Doc(field, mongowire.Doc("$type", typ))))
				if n, _ := mustLookup(t, r, "n").Int(); n != 1 {
					t.Errorf("%s is not a %s: %s", field, typ, r)
				}
			}
			cur, err := c.Cursor(ctx, db, mongowire.Doc("aggregate", coll, "pipeline", mongowire.A{
				mongowire.Doc("$match", mongowire.Doc("_id", -1)),
				mongowire.Doc("$project", mongowire.Doc("dec", mongowire.Doc("$toString", "$dec"), "big", mongowire.Doc("$toString", "$big"))),
			}, "cursor", mongowire.Doc()), mongowire.CursorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := cur.Batch()[0].String(); got != `{"_id":-1,"dec":"-12.3400","big":"9223372036854775808"}` {
				t.Errorf("as the server writes them: %s", got)
			}
			cur.Close(ctx)

			// Read back, the document is the record it was written from.
			cur, err = c.Cursor(ctx, db, mongowire.Doc("find", coll, "filter", mongowire.Doc("_id", -1)), mongowire.CursorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			back, err := cur.Batch()[0].Value()
			if err != nil {
				t.Fatal(err)
			}
			cur.Close(ctx)
			want := fitsync.Map(append([]fitsync.Field(nil), rec.Fields()...)...)
			// Dates come back as times, JSON as the document it held, a uint
			// past int64 as a decimal.
			for i, f := range want.Fields() {
				switch f.Name {
				case "d":
					want.Fields()[i].Value = fitsync.Time(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC))
				case "j":
					want.Fields()[i].Value = fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Array(fitsync.Int(1), fitsync.String("x"), fitsync.Null())})
				case "big":
					want.Fields()[i].Value = fitsync.Decimal("9223372036854775808")
				}
			}
			if !back.Equal(want) {
				a, _ := fitsync.AppendPlain(nil, back)
				b, _ := fitsync.AppendPlain(nil, want)
				t.Errorf("read back:\n%s\nwant\n%s", a, b)
			}

			// A cursor in batches of 10 through getMore.
			cur, err = c.Cursor(ctx, db, mongowire.Doc("find", coll, "filter", mongowire.Doc("_id", mongowire.Doc("$gte", 0)), "sort", mongowire.Doc("_id", 1), "batchSize", int32(10)), mongowire.CursorOptions{BatchSize: 10})
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for {
				d, err := cur.Next(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if d == nil {
					break
				}
				if n, _ := mustLookup(t, d, "n").Int(); n != int64(seen) {
					t.Fatalf("document %d is %s", seen, d)
				}
				seen++
			}
			if seen != 95 {
				t.Fatalf("read %d of 95", seen)
			}
			// A cursor closed early is killed on the server.
			cur, err = c.Cursor(ctx, db, mongowire.Doc("find", coll, "batchSize", int32(5)), mongowire.CursorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			id := cur.ID()
			if err := cur.Close(ctx); err != nil || id == 0 {
				t.Fatalf("kill %d: %v", id, err)
			}

			// A command cut by its context's deadline fails with it, and the
			// client goes on.
			dctx, dcancel := context.WithTimeout(ctx, 300*time.Millisecond)
			_, err = c.Run(dctx, db, mongowire.Doc("find", coll, "filter", mongowire.Doc("$where", "sleep(2000) || true"), "limit", 1))
			dcancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("deadline: %v", err)
			}
			mongoRun(t, ctx, c, db, mongowire.Doc("ping", 1))

			// A wrong password is refused.
			bad := mongoClient(t, uri, func(o *mongowire.Options) { o.Password = "wrong" })
			var ce *mongowire.CommandError
			if _, err := bad.Run(ctx, "admin", mongowire.Doc("ping", 1)); !errors.As(err, &ce) || ce.Code != 18 {
				t.Errorf("wrong password: %v", err)
			}
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
		})
	}
}

func mustLookup(t *testing.T, r mongowire.Raw, k string) mongowire.RawValue {
	t.Helper()
	v, ok := r.Lookup(k)
	if !ok {
		t.Fatalf("no %s in %s", k, r)
	}
	return v
}

func TestMongoTLS(t *testing.T) {
	sets := mongoSets(t)
	caFile := os.Getenv("FIT_SYNC_MONGO_CA")
	if caFile == "" {
		t.Skip("FIT_SYNC_MONGO_CA is not set")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	for name, uri := range sets {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			c := mongoClient(t, uri, func(o *mongowire.Options) { o.TLS = &tls.Config{RootCAs: pool} })
			mongoRun(t, ctx, c, "admin", mongowire.Doc("ping", 1))
			// Without the CA the servers are not trusted: nothing is found.
			c2 := mongoClient(t, uri, func(o *mongowire.Options) {
				o.TLS = &tls.Config{}
				o.ServerSelectionTimeout = time.Second
			})
			if _, err := c2.Run(ctx, "admin", mongowire.Doc("ping", 1)); err == nil || !strings.Contains(err.Error(), "certificate") {
				t.Errorf("untrusted: %v", err)
			}
		})
	}
}

// TestMongoFailover steps the primary down and checks that writes find
// the new one.
func TestMongoFailover(t *testing.T) {
	for name, uri := range mongoSets(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			c := mongoClient(t, uri, func(o *mongowire.Options) { o.ServerSelectionTimeout = 2 * time.Minute })
			old, err := c.Primary(ctx)
			if err != nil {
				t.Fatal(err)
			}
			const db = "fit_wire_failover"
			mongoRun(t, ctx, c, db, mongowire.Doc("insert", "c", "documents", mongowire.A{mongowire.Doc("_id", 0)}, "writeConcern", mongowire.Doc("w", "majority")))
			// The step down cuts the connections it was sent on; either answer
			// will do.
			_, err = c.Run(ctx, "admin", mongowire.Doc("replSetStepDown", 30, "secondaryCatchUpPeriodSecs", 15))
			if err != nil && !mongowire.IsNetError(err) {
				t.Fatalf("step down: %v", err)
			}
			for i := 1; ; i++ {
				_, err := c.Run(ctx, db, mongowire.Doc("insert", "c", "documents", mongowire.A{mongowire.Doc("_id", i)}, "writeConcern", mongowire.Doc("w", "majority")))
				if err == nil {
					break
				}
				if ctx.Err() != nil {
					t.Fatalf("no write after the step down: %v", err)
				}
				t.Logf("write %d: %v", i, err)
				time.Sleep(200 * time.Millisecond)
			}
			now, err := c.Primary(ctx)
			if err != nil || now == old {
				t.Fatalf("primary %s, was %s: %v", now, old, err)
			}
			r := mongoRun(t, ctx, c, db, mongowire.Doc("count", "c"))
			if n, _ := mustLookup(t, r, "n").Int(); n < 2 {
				t.Errorf("count %s", r)
			}
			t.Logf("%s: primary %s -> %s", name, old, now)
			mongoRun(t, ctx, c, db, mongowire.Doc("dropDatabase", 1))
		})
	}
}
