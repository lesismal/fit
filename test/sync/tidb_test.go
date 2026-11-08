package synctest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
	_ "github.com/lesismal/fit/sync/postgres/sink"
	_ "github.com/lesismal/fit/sync/tidb"
)

const cdcColumns = `(id BIGINT PRIMARY KEY, v VARCHAR(40), n DECIMAL(12,4), at DATETIME(6), d DATE,
	j JSON, b VARBINARY(16), e ENUM('a','b','c'), f DOUBLE)`

const cdcPGColumns = `(id bigint PRIMARY KEY, v text, n numeric(12,4), at timestamp(6), d date,
	j jsonb, b bytea, e text, f double precision)`

// cdcRow writes row i's values, version ver, as a statement's arguments.
func cdcRow(i, ver int64) []any {
	if i%7 == 3 {
		return []any{i, nil, nil, nil, nil, nil, nil, nil, nil}
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, int(i*1000+ver)*1000, time.UTC)
	return []any{i, fmt.Sprintf("row %d v%d é", i, ver), fmt.Sprintf("%d.%04d", ver, i%10000), mysqlwire.FromTime(at),
		mysqlwire.FromTime(at.AddDate(0, 0, int(i))), fmt.Sprintf(`{"k": %d}`, ver), []byte{byte(i), 0, 0xff, byte(ver)},
		[]string{"a", "b", "c"}[(i+ver)%3], float64(i+ver) / 2}
}

const cdcTiDBSum = `SELECT COUNT(*), MD5(GROUP_CONCAT(CONCAT_WS('|', id, IFNULL(v,'~'), IFNULL(n,'~'),
	IFNULL(DATE_FORMAT(at,'%Y-%m-%d %H:%i:%s.%f'),'~'), IFNULL(DATE_FORMAT(d,'%Y-%m-%d'),'~'), IFNULL(CAST(j AS CHAR),'~'),
	IFNULL(HEX(b),'~'), IFNULL(e,'~'), IFNULL(f,'~')) ORDER BY id SEPARATOR ',')) FROM t`

const cdcPGSum = `SELECT count(*)::text || ':' || coalesce(md5(string_agg(concat_ws('|', id, coalesce(v,'~'), coalesce(n::text,'~'),
	coalesce(to_char(at,'YYYY-MM-DD HH24:MI:SS.US'),'~'), coalesce(to_char(d,'YYYY-MM-DD'),'~'), coalesce(j::text,'~'),
	coalesce(upper(encode(b,'hex')),'~'), coalesce(e,'~'), coalesce(f::text,'~')), ',' ORDER BY id)), '') FROM %s.t`

func tidbSum(t *testing.T, c *mysqlwire.Conn) string {
	t.Helper()
	rows, err := c.Query(hangGuard(t), cdcTiDBSum)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	v := rows.Values()
	sum := ""
	if b, ok := v[1].([]byte); ok {
		sum = string(b)
	}
	return fmt.Sprintf("%v:%s", v[0], sum)
}

// TestTiDBThroughTiCDCToPostgres syncs a TiDB table into PostgreSQL through
// the tidb source: a snapshot of the rows written before, then a TiCDC
// changefeed's changes from Kafka, in canal-json and in the simple
// protocol, while a mixed load writes on and the pipeline is killed at
// random points; the tables end equal by checksum.
func TestTiDBThroughTiCDCToPostgres(t *testing.T) {
	addr := kafkaEnv(t, "FIT_SYNC_TICDC_TIDB")
	container, cdcKafka := os.Getenv("FIT_SYNC_TICDC_CONTAINER"), os.Getenv("FIT_SYNC_TICDC_KAFKA")
	brokers := strings.Split(kafkaEnv(t, "FIT_SYNC_KAFKA_BROKERS"), ",")
	pg := pgServers(t)[0]
	for _, proto := range []string{"canal-json", "simple"} {
		t.Run(proto, func(t *testing.T) {
			seed := uint64(time.Now().UnixNano())
			t.Logf("seed %d", seed)
			rnd := rand.New(rand.NewPCG(seed, 5))
			name := "fit_cdc_" + strings.ReplaceAll(proto, "-", "_")
			c, err := mysqlwire.Dial(hangGuard(t), mysqlwire.Config{Addr: addr, User: "root"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			mysqlDB(t, c, name)
			mustExec(t, c, "SET time_zone = '+00:00'")
			mustExec(t, c, "SET SESSION group_concat_max_len = 1073741824")
			mustExec(t, c, "CREATE TABLE t "+cdcColumns)
			ins := "INSERT INTO t VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
			ver := map[int64]int64{}
			for i := range int64(300) {
				mustExec(t, c, ins, cdcRow(i, 0)...)
				ver[i] = 0
			}

			kc := kafkaClient(t, kafkawire.Config{Brokers: brokers})
			topic := kafkaTopic(t, kc, 3, 3, nil)
			uri := "kafka://" + strings.Split(cdcKafka, ",")[0] + "/" + topic + "?protocol=" + proto + "&partition-num=3&replication-factor=3&max-message-bytes=1048576"
			if proto == "canal-json" {
				uri += "&enable-tidb-extension=true"
			}
			id := strings.ReplaceAll(topic, "_", "-")
			if len(id) > 120 {
				id = id[len(id)-120:]
			}
			id = strings.Trim(id, "-")
			cdc := func(args ...string) {
				out, err := exec.Command("docker", append([]string{"exec", container, "/cdc", "cli", "changefeed"}, append(args, "--server=http://127.0.0.1:8300")...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("cdc %v: %v: %s", args, err, out)
				}
			}
			// The changefeed starts before the snapshot is taken.
			cdc("create", "--changefeed-id="+id, "--sink-uri="+uri)
			t.Cleanup(func() {
				exec.Command("docker", "exec", container, "/cdc", "cli", "changefeed", "remove", "--changefeed-id="+id, "--server=http://127.0.0.1:8300").Run()
			})

			p := pg.connect(t, nil)
			pgExec(t, p, "DROP SCHEMA IF EXISTS "+name+" CASCADE")
			pgExec(t, p, "CREATE SCHEMA "+name)
			pgExec(t, p, "CREATE TABLE "+name+".t "+cdcPGColumns)

			pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
				"queue": map[string]any{"max_records": 20 + rnd.IntN(300)},
				"source": map[string]any{"module": "tidb", "params": map[string]any{"address": addr, "tables": []string{name + ".t"},
					"protocol": proto, "page_rows": 1 + rnd.IntN(120), "kafka": map[string]any{"brokers": brokers, "topics": []string{topic}, "max_wait": "100ms"}}},
				"sinks": []any{map[string]any{"name": "pg", "batch": map[string]any{"max_records": 1 + rnd.IntN(80), "max_wait": "5ms"},
					"sink": map[string]any{"module": "postgres", "params": pgSinkParams(pg, nil)}}}}
			cfg := loadSync(t, filepath.Join(t.TempDir(), "state"), pipeline)

			// The load, while the pipeline runs and is killed.
			var stop atomic.Bool
			loadDone := make(chan error, 1)
			go func() {
				lc, err := mysqlwire.Dial(context.Background(), mysqlwire.Config{Addr: addr, User: "root", Database: name})
				if err != nil {
					loadDone <- err
					return
				}
				defer lc.Close()
				lr := rand.New(rand.NewPCG(seed, 6))
				next := int64(300)
				for n := 0; n < 600 && !stop.Load(); n++ {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					var err error
					switch k := lr.IntN(10); {
					case k < 3:
						_, err = lc.Exec(ctx, ins, cdcRow(next, 0)...)
						next++
					case k < 7:
						i := lr.Int64N(next)
						v := int64(n + 1)
						r := cdcRow(i, v)
						_, err = lc.Exec(ctx, "UPDATE t SET v = ?, n = ?, at = ?, d = ?, j = ?, b = ?, e = ?, f = ? WHERE id = ?", append(r[1:], i)...)
					case k < 9:
						_, err = lc.Exec(ctx, "DELETE FROM t WHERE id = ?", lr.Int64N(next))
					default:
						// A key changed, in a transaction with an insert.
						i := lr.Int64N(next)
						if _, err = lc.Exec(ctx, "BEGIN"); err == nil {
							if _, err = lc.Exec(ctx, "UPDATE t SET id = ? WHERE id = ?", next+100000, i); err == nil {
								if _, err = lc.Exec(ctx, ins, cdcRow(next, 1)...); err == nil {
									_, err = lc.Exec(ctx, "COMMIT")
								}
							}
						}
						next++
					}
					cancel()
					if err != nil {
						loadDone <- err
						return
					}
					time.Sleep(time.Duration(lr.IntN(20)) * time.Millisecond)
				}
				loadDone <- nil
			}()

			points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
				fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
			kills := 0
			var rt *fitsync.Runtime
			start := func(crash func(string, fitsync.Point) bool) {
				rt = fitsync.NewRuntime(fitsync.Options{Crash: crash})
				if err := rt.Apply(cfg.Sync); err != nil {
					t.Fatal(err)
				}
			}
			for range 4 {
				at := points[rnd.IntN(len(points))]
				nth := int32(1 + rnd.IntN(120))
				if at == fitsync.CrashCheckpointWritten || at == fitsync.CrashCheckpointSaved {
					nth = int32(1 + rnd.IntN(5))
				}
				var seen atomic.Int32
				start(func(_ string, p fitsync.Point) bool { return p == at && seen.Add(1) == nth })
				deadline := time.Now().Add(10 * time.Second)
				for redisState(rt) != "killed" && time.Now().Before(deadline) {
					if st := redisState(rt); st == "stopped" {
						t.Fatalf("the pipeline stopped: %+v", rt.Status())
					}
					time.Sleep(time.Millisecond)
				}
				if redisState(rt) == "killed" {
					kills++
				} else {
					t.Logf("not killed at %s #%d", at, nth)
				}
				rt.Close(context.Background())
			}
			start(nil)
			defer rt.Close(context.Background())
			select {
			case err := <-loadDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Minute): // a hang guard
				stop.Store(true)
				t.Fatal("the load did not end")
			}
			want := tidbSum(t, c)
			var got string
			deadline := time.Now().Add(4 * time.Minute) // a hang guard
			for {
				got = fmt.Sprint(pgOne(t, p, fmt.Sprintf(cdcPGSum, name))[0])
				if got == want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("PostgreSQL's table is %s, TiDB's %s: %+v", got, want, rt.Status())
				}
				time.Sleep(200 * time.Millisecond)
			}
			t.Logf("%s: equal (%s) across %d kills", proto, want, kills)
		})
	}
}
