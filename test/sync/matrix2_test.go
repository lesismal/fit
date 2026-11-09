package synctest

// The second round's sources in the matrix (I222): the queues (RabbitMQ,
// NATS JetStream, Pulsar and MQTT) carry Debezium's change events of the
// load, as the kafka source's run does, each key's in order on one queue,
// stream, topic or MQTT topic; SQL Server is read through CDC; Doris is
// polled by a cursor column, ver, its deletes a deleted flag. Each is one
// family of FIT_SYNC_MATRIX_SOURCES: rabbitmq, nats, pulsar, mqtt, mssql,
// doris; run.sh's mq, mssql and olap families start their servers.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lesismal/fit/sync/doris"
	_ "github.com/lesismal/fit/sync/mqtt"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
	_ "github.com/lesismal/fit/sync/mssql"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
	_ "github.com/lesismal/fit/sync/nats"
	natswire "github.com/lesismal/fit/sync/nats/wire"
	_ "github.com/lesismal/fit/sync/pulsar"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
	_ "github.com/lesismal/fit/sync/rabbitmq"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

// mxSources2 are the second round's sources of the environment, each of a
// family FIT_SYNC_MATRIX_SOURCES picks, on every server of its kind the
// environment has: run.sh's matrix starts one (RabbitMQ 4.1, NATS 2.11,
// Pulsar 4, Mosquitto, SQL Server 2022, Doris), the nightly matrix each.
func mxSources2(t *testing.T) []mxSource {
	var out []mxSource
	if mxPick("rabbitmq") {
		for _, s := range rabbitServers(t) {
			out = append(out, mxSource{name: mxSafe(s.name), family: "rabbitmq", rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxRabbitSource(t, s, run, m) }})
		}
	}
	if mxPick("nats") {
		for _, n := range natsClusters(t) {
			out = append(out, mxSource{name: mxSafe(n.name), family: "nats", rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxNATSSource(t, n, run, m) }})
		}
	}
	if mxPick("pulsar") {
		for _, s := range pulsarServers(t) {
			out = append(out, mxSource{name: mxSafe(s.name), family: "pulsar", rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxPulsarSource(t, s, run, m) }})
		}
	}
	if mxPick("mqtt") {
		for _, b := range mqttBrokers(t) {
			out = append(out, mxSource{name: b.name, family: "mqtt", rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxMQTTSource(t, b, run, m) }})
		}
	}
	if mxPick("mssql") {
		for _, s := range mssqlServers(t) {
			out = append(out, mxSource{name: "mssql" + s.name, family: "mssql", txs: true, rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxMSSQLSource(t, s, run, m) }})
		}
	}
	if mxPick("doris") {
		for _, s := range olapServers(t) {
			if s.flavor == "doris" {
				out = append(out, mxSource{name: "doris", family: "doris", rekey: true,
					setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxDorisSource(t, s, run, m) }})
			}
		}
	}
	return out
}

// mxSafe makes a server's name a run's, which names tables and databases:
// letters and digits.
func mxSafe(name string) string {
	return strings.NewReplacer("-", "", ".", "").Replace(name)
}

// mxEvent is the Debezium change event of a change of id (op c, u, r or
// d), its row after (nil for a delete), committed at ver milliseconds past
// an epoch, as the kafka source's run makes them.
func mxEvent(run, op string, id int64, r *mxRow, ver int64) []byte {
	var before, after any
	if op == "d" {
		before = map[string]any{"id": id}
	} else {
		after = map[string]any{"id": id, "v": r.V, "n": r.N, "ver": r.Ver}
	}
	b, _ := json.Marshal(map[string]any{"op": op, "before": before, "after": after, "ts_ms": 1_700_000_000_000 + ver,
		"source": map[string]any{"db": "mx", "table": run, "ts_ms": 1_700_000_000_000 + ver}})
	return b
}

// mxEvents are the events of a transaction of the load: a key change is a
// delete and a create.
func mxEvents(run string, ops []mxOp) [][]byte {
	var out [][]byte
	for _, op := range ops {
		switch op.Kind {
		case 'i':
			out = append(out, mxEvent(run, "c", op.ID, &op.Row, op.Row.Ver))
		case 'u':
			out = append(out, mxEvent(run, "u", op.ID, &op.Row, op.Row.Ver))
		case 'd':
			out = append(out, mxEvent(run, "d", op.ID, nil, op.Row.Ver))
		case 'k':
			out = append(out, mxEvent(run, "d", op.ID, nil, op.Row.Ver), mxEvent(run, "c", op.To, &op.Row, op.Row.Ver))
		}
	}
	return out
}

// mxSnapshotEvents are the model's rows as read events.
func mxSnapshotEvents(run string, m *mxModel) [][]byte {
	var out [][]byte
	for id, r := range m.rows {
		out = append(out, mxEvent(run, "r", id, &r, r.Ver))
	}
	return out
}

var mxDebezium = map[string]any{"module": "debezium"}

// mxKeyByID builds a queue's records' keys from their id: Debezium's
// events carry the key in Kafka's message key, which these brokers have
// none of.
var mxKeyByID = []any{map[string]any{"module": "key", "params": map[string]any{"fields": []string{"id"}}}}

// mxRabbitSource: a quorum queue of the events, consumed with a prefetch,
// each delivery acknowledged once every sink has it.
func mxRabbitSource(t *testing.T, s rabbitServer, run string, m *mxModel) mxRun {
	c := s.conn(t)
	queue := "fit-mx-" + run
	if _, err := c.QueueDeclare(hangGuard(t), queue, false, true, amqpwire.Table{"x-queue-type": "quorum"}); err != nil {
		t.Fatal(err)
	}
	send := func(ctx context.Context, bodies [][]byte) error {
		var first, last uint64
		for i, b := range bodies {
			seq, err := c.Publish(ctx, "", queue, false, amqpwire.Properties{DeliveryMode: 2}, b)
			if err != nil {
				return err
			}
			if i == 0 {
				first = seq
			}
			last = seq
		}
		if len(bodies) == 0 {
			return nil
		}
		if nacked, err := c.WaitConfirms(ctx, first, last); err != nil || nacked != nil {
			return fmt.Errorf("confirms: %v %v", nacked, err)
		}
		return nil
	}
	if err := send(hangGuard(t), mxSnapshotEvents(run, m)); err != nil {
		t.Fatal(err)
	}
	os.Setenv("FIT_TEST_MX_RABBITMQ_PASSWORD", s.password)
	return mxRun{
		source: map[string]any{"module": "rabbitmq", "params": map[string]any{"addresses": []string{s.addr}, "username": s.user,
			"password_env": "FIT_TEST_MX_RABBITMQ_PASSWORD", "queues": []string{queue}, "prefetch": 300, "codec": mxDebezium}},
		transforms: mxKeyByID,
		apply:      func(ctx context.Context, ops []mxOp) error { return send(ctx, mxEvents(run, ops)) },
	}
}

// mxNATSSource: a stream of three replicas, read through a durable pull
// consumer the source makes.
func mxNATSSource(t *testing.T, n natsCluster, run string, m *mxModel) mxRun {
	c := n.client(t)
	stream := n.stream(t, c, "mx")
	send := func(ctx context.Context, bodies [][]byte) error {
		var last *natswire.PendingAck
		for _, b := range bodies {
			if last != nil {
				last.Cancel()
			}
			pa, err := c.PublishJS(stream+".e", nil, b)
			if err != nil {
				return err
			}
			last = pa
		}
		if last == nil {
			return nil
		}
		_, err := last.Wait(ctx)
		return err
	}
	if err := send(hangGuard(t), mxSnapshotEvents(run, m)); err != nil {
		t.Fatal(err)
	}
	os.Setenv("FIT_TEST_MX_NATS_PASSWORD", n.password)
	return mxRun{
		source: map[string]any{"module": "nats", "params": map[string]any{"servers": n.servers, "username": n.user,
			"password_env": "FIT_TEST_MX_NATS_PASSWORD", "stream": stream, "consumer": "fit", "create_consumer": true,
			"ack_wait": "30s", "max_wait": "100ms", "codec": mxDebezium}},
		transforms: mxKeyByID,
		apply:      func(ctx context.Context, ops []mxOp) error { return send(ctx, mxEvents(run, ops)) },
	}
}

// mxPulsarSource: a topic of one partition, read from the message IDs in
// the checkpoint.
func mxPulsarSource(t *testing.T, s pulsarServer, run string, m *mxModel) mxRun {
	c := pulsarClient(t, s)
	topic := pulsarTopic(t, s, 0)
	p, err := c.CreateProducer(hangGuard(t), topic, "", pulsarwire.LZ4)
	if err != nil {
		t.Fatal(err)
	}
	send := func(ctx context.Context, bodies [][]byte) error {
		if len(bodies) == 0 {
			return nil
		}
		msgs := make([]pulsarwire.ProducerMessage, len(bodies))
		for i, b := range bodies {
			msgs[i] = pulsarwire.ProducerMessage{Payload: b}
		}
		ch, err := p.Send(msgs)
		if err != nil {
			return err
		}
		select {
		case r := <-ch:
			return r.Err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := send(hangGuard(t), mxSnapshotEvents(run, m)); err != nil {
		t.Fatal(err)
	}
	return mxRun{
		source:     map[string]any{"module": "pulsar", "params": map[string]any{"url": s.url, "topics": []string{topic}, "codec": mxDebezium}},
		transforms: mxKeyByID,
		apply:      func(ctx context.Context, ops []mxOp) error { return send(ctx, mxEvents(run, ops)) },
	}
}

// mxMQTTSource: a topic of the events at QoS 1, its session made (and
// subscribed) before the first is published, so that the broker keeps
// them for the source, which resumes it.
func mxMQTTSource(t *testing.T, b mqttBroker, run string, m *mxModel) mxRun {
	topic := "fit-mx/" + run
	ctx := hangGuard(t)
	sess, err := mqttwire.Dial(ctx, mqttwire.Config{Address: b.addr, ClientID: run, SessionExpiry: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Subscribe(ctx, mqttwire.Subscription{Filter: topic, QoS: 1}); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	pub := mqttDial(t, mqttwire.Config{Address: b.addr, ClientID: run + "-pub", CleanStart: true})
	send := func(ctx context.Context, bodies [][]byte) error {
		var ds []*mqttwire.Delivery
		for _, body := range bodies {
			d, err := pub.Send(ctx, &mqttwire.Message{Topic: topic, QoS: 1, Payload: body})
			if err != nil {
				return err
			}
			ds = append(ds, d)
		}
		for _, d := range ds {
			if err := d.Wait(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if err := send(ctx, mxSnapshotEvents(run, m)); err != nil {
		t.Fatal(err)
	}
	return mxRun{
		source: map[string]any{"module": "mqtt", "params": map[string]any{"servers": []string{b.addr}, "client_id": run,
			"topics": []string{topic}, "qos": 1, "codec": mxDebezium}},
		transforms: mxKeyByID,
		apply:      func(ctx context.Context, ops []mxOp) error { return send(ctx, mxEvents(run, ops)) },
	}
}

// mxQuote quotes s as an SQL string literal: N'...' (SQL Server) or
// '...' with backslashes escaped (Doris).
func mxQuote(s string, mssql bool) string {
	if mssql {
		return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

func mxN(n *int64) string {
	if n == nil {
		return "NULL"
	}
	return strconv.FormatInt(*n, 10)
}

// mxMSSQLSource: a table captured by CDC, read whole then by LSN.
func mxMSSQLSource(t *testing.T, s mssqlServer, run string, m *mxModel) mxRun {
	c := s.dial(t, mssqlwire.EncryptOn)
	mssqlExec(t, c, "CREATE TABLE dbo."+run+" (id bigint PRIMARY KEY, v nvarchar(100) NOT NULL, n bigint NULL, ver bigint NOT NULL)")
	mssqlCapture(t, s, c, run)
	var vals []string
	for id, r := range m.rows {
		vals = append(vals, fmt.Sprintf("(%d, %s, %s, %d)", id, mxQuote(r.V, true), mxN(r.N), r.Ver))
		if len(vals) == 500 {
			mssqlExec(t, c, "INSERT INTO dbo."+run+" VALUES "+strings.Join(vals, ","))
			vals = nil
		}
	}
	if len(vals) > 0 {
		mssqlExec(t, c, "INSERT INTO dbo."+run+" VALUES "+strings.Join(vals, ","))
	}
	lc := s.dial(t, mssqlwire.EncryptOff)
	os.Setenv("FIT_TEST_MX_MSSQL_PASSWORD", s.password)
	return mxRun{
		source: map[string]any{"module": "mssql", "params": map[string]any{"address": s.addr, "username": "sa",
			"password_env": "FIT_TEST_MX_MSSQL_PASSWORD", "database": "fit_sync", "ca_file": os.Getenv("FIT_SYNC_MSSQL_CA"),
			"server_name": "localhost", "poll_interval": "100ms", "tables": []string{"dbo." + run}, "mode": "cdc", "batch_size": 50}},
		apply: func(ctx context.Context, ops []mxOp) error {
			var stmts []string
			for _, op := range ops {
				switch op.Kind {
				case 'i':
					stmts = append(stmts, fmt.Sprintf("INSERT INTO dbo.%s VALUES (%d, %s, %s, %d)", run, op.ID, mxQuote(op.Row.V, true), mxN(op.Row.N), op.Row.Ver))
				case 'u':
					stmts = append(stmts, fmt.Sprintf("UPDATE dbo.%s SET v = %s, n = %s, ver = %d WHERE id = %d", run, mxQuote(op.Row.V, true), mxN(op.Row.N), op.Row.Ver, op.ID))
				case 'd':
					stmts = append(stmts, fmt.Sprintf("DELETE FROM dbo.%s WHERE id = %d", run, op.ID))
				case 'k':
					stmts = append(stmts, fmt.Sprintf("UPDATE dbo.%s SET id = %d, ver = %d WHERE id = %d", run, op.To, op.Row.Ver, op.ID))
				}
			}
			q := strings.Join(stmts, "; ")
			if len(stmts) > 1 {
				q = "BEGIN TRANSACTION; " + q + "; COMMIT TRANSACTION"
			}
			_, err := lc.Exec(ctx, q)
			return err
		},
		state: func(t *testing.T) mxState {
			_, rows, err := c.QueryAll(context.Background(), "SELECT id, v, n, ver FROM dbo."+run)
			if err != nil {
				t.Fatal(err)
			}
			st := mxState{}
			for _, r := range rows {
				id := r[0].(int64)
				var n *int64
				if r[2] != nil {
					x := r[2].(int64)
					n = &x
				}
				st[id] = mxLine(id, r[1].(string), n, r[3].(int64))
			}
			return st
		},
		cleanup: func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c.Exec(ctx, "EXEC sys.sp_cdc_disable_table @source_schema = N'dbo', @source_name = @p1, @capture_instance = N'all'", run)
			c.Exec(ctx, "DROP TABLE dbo."+run)
		},
	}
}

// mxDorisSource: a table of the unique model read whole, then polled past
// the last ver read; a delete is the row's deleted flag set, and a key
// change a delete and an insert (Doris updates no key). The flag is
// dropped on the way to the sinks.
func mxDorisSource(t *testing.T, s olapServer, run string, m *mxModel) mxRun {
	c := s.conn(t)
	table := s.table(t, c, strings.ToLower(run), "id BIGINT NOT NULL, v VARCHAR(200) NOT NULL, n BIGINT NULL, ver BIGINT NOT NULL, deleted BOOLEAN NOT NULL", false)
	row := func(id int64, r mxRow) string {
		return fmt.Sprintf("(%d, %s, %s, %d, false)", id, mxQuote(r.V, false), mxN(r.N), r.Ver)
	}
	var vals []string
	for id, r := range m.rows {
		vals = append(vals, row(id, r))
	}
	if len(vals) > 0 {
		mustExec(t, c, "INSERT INTO "+table+" VALUES "+strings.Join(vals, ","))
	}
	lc := s.conn(t)
	exec := func(ctx context.Context, q string) error { _, err := lc.Exec(ctx, q); return err }
	return mxRun{
		source: map[string]any{"module": "doris", "params": map[string]any{"address": s.addr, "username": "root", "table": table,
			"key": []string{"id"}, "cursor": "ver", "deleted_column": "deleted", "page_rows": 50, "poll_interval": "100ms"}},
		transforms: []any{map[string]any{"module": "fields", "params": map[string]any{"drop": []string{"deleted"}}}},
		apply: func(ctx context.Context, ops []mxOp) error {
			for _, op := range ops {
				var err error
				switch op.Kind {
				case 'i':
					err = exec(ctx, "INSERT INTO "+table+" VALUES "+row(op.ID, op.Row))
				case 'u':
					err = exec(ctx, fmt.Sprintf("UPDATE %s SET v = %s, n = %s, ver = %d WHERE id = %d", table, mxQuote(op.Row.V, false), mxN(op.Row.N), op.Row.Ver, op.ID))
				case 'd':
					err = exec(ctx, fmt.Sprintf("UPDATE %s SET deleted = true, ver = %d WHERE id = %d", table, op.Row.Ver, op.ID))
				case 'k':
					if err = exec(ctx, fmt.Sprintf("UPDATE %s SET deleted = true, ver = %d WHERE id = %d", table, op.Row.Ver, op.ID)); err == nil {
						err = exec(ctx, "INSERT INTO "+table+" VALUES "+row(op.To, op.Row))
					}
				}
				if err != nil {
					return fmt.Errorf("%c %d: %w", op.Kind, op.ID, err)
				}
			}
			return nil
		},
		state: func(t *testing.T) mxState { return mxDorisRows(t, c, table) },
		cleanup: func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c.Exec(ctx, "DROP TABLE IF EXISTS "+table)
		},
	}
}

func mxDorisRows(t *testing.T, c *mysqlwire.Conn, table string) mxState {
	t.Helper()
	rows, err := c.Query(hangGuard(t), "SELECT id, v, n, ver FROM "+table+" WHERE deleted = false")
	if err != nil {
		t.Fatal(err)
	}
	st := mxState{}
	for rows.Next() {
		v := rows.Values()
		text := func(x any) string {
			if b, ok := x.([]byte); ok {
				return string(b)
			}
			return fmt.Sprint(x)
		}
		id, _ := strconv.ParseInt(text(v[0]), 10, 64)
		ver, _ := strconv.ParseInt(text(v[3]), 10, 64)
		var n *int64
		if v[2] != nil {
			x, _ := strconv.ParseInt(text(v[2]), 10, 64)
			n = &x
		}
		st[id] = mxLine(id, text(v[1]), n, ver)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return st
}
