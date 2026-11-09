package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

// The second round's sources (I222): messages published to RabbitMQ's
// queue bench and to MQTT's topic bench, and rows of SQL Server's
// shop.dbo.items, which CDC captures.

// message is record i as a message: its row as JSON, about 230 bytes.
func message(i int) []byte {
	return fmt.Appendf(nil, `{"id":%d,"k":%d,"c":"%s","pad":"%s"}`, i, i*7919%1000003, digits(i, 120), digits(i+7, 60))
}

// publisher sends messages, returning once the broker has them all.
type publisher interface {
	publish(ctx context.Context, bodies [][]byte) error
	close()
}

// pubSource writes records as messages through a pool of publishers, in
// runs of batch each, the runs spread over the publishers.
type pubSource struct {
	pubs  []publisher
	batch int
}

func (s *pubSource) write(ctx context.Context, first, n int) error {
	work := make(chan [2]int)
	errs := make(chan error, len(s.pubs))
	var wg sync.WaitGroup
	for _, p := range s.pubs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range work {
				bodies := make([][]byte, 0, r[1]-r[0])
				for i := r[0]; i < r[1]; i++ {
					bodies = append(bodies, message(i+1))
				}
				if err := p.publish(ctx, bodies); err != nil {
					errs <- err
					for range work {
					}
					return
				}
			}
		}()
	}
	for lo := first; lo < first+n; lo += s.batch {
		work <- [2]int{lo, min(lo+s.batch, first+n)}
	}
	close(work)
	wg.Wait()
	close(errs)
	return <-errs
}

func (s *pubSource) close() {
	for _, p := range s.pubs {
		p.close()
	}
}

// amqpConn is what rabbitPub needs of a RabbitMQ connection.
type amqpConn interface {
	Publish(ctx context.Context, exchange, key string, mandatory bool, p amqpwire.Properties, body []byte) (uint64, error)
	WaitConfirms(ctx context.Context, from, to uint64) ([]uint64, error)
	Close() error
}

// rabbitPub publishes to the queue bench, persistent, and waits for the
// broker's confirms.
type rabbitPub struct{ c amqpConn }

func dialRabbit(ctx context.Context, addr, user, password string) (publisher, error) {
	c, err := amqpwire.Dial(ctx, amqpwire.Config{Addrs: []string{addr}, Username: user, Password: password, Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	if err := c.ConfirmSelect(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return &rabbitPub{c}, nil
}

func (p *rabbitPub) publish(ctx context.Context, bodies [][]byte) error {
	var first, last uint64
	for i, b := range bodies {
		seq, err := p.c.Publish(ctx, "", benchQueue, false, amqpwire.Properties{DeliveryMode: 2}, b)
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
	if nacked, err := p.c.WaitConfirms(ctx, first, last); err != nil {
		return err
	} else if len(nacked) > 0 {
		return fmt.Errorf("rabbitmq: %d messages refused", len(nacked))
	}
	return nil
}

func (p *rabbitPub) close() { _ = p.c.Close() }

// rabbitDeclare makes the queue bench, durable, the one every mover reads
// (Kafka Connect's binds it to its exchange).
func rabbitDeclare(ctx context.Context, addr, user, password string) error {
	c, err := amqpwire.Dial(ctx, amqpwire.Config{Addrs: []string{addr}, Username: user, Password: password, Timeout: time.Minute})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.QueueDeclare(ctx, benchQueue, false, true, nil)
	return err
}

// The queue and topic messages go to.
const (
	benchQueue = "bench"
	benchTopic = "bench"
)

// mqttPub publishes to the topic bench at QoS 1.
type mqttPub struct{ c *mqttwire.Client }

func dialMQTT(ctx context.Context, addr string, n int) (publisher, error) {
	c, err := mqttwire.Dial(ctx, mqttwire.Config{Address: addr, ClientID: fmt.Sprintf("syncbench-%d-%d", n, time.Now().UnixNano()),
		CleanStart: true, Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	return &mqttPub{c}, nil
}

func (p *mqttPub) publish(ctx context.Context, bodies [][]byte) error {
	ds := make([]*mqttwire.Delivery, 0, len(bodies))
	for _, b := range bodies {
		d, err := p.c.Send(ctx, &mqttwire.Message{Topic: benchTopic, QoS: 1, Payload: b})
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

func (p *mqttPub) close() { p.c.Close() }

// tdsConn is what mssqlConn needs of a SQL Server connection.
type tdsConn interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	QueryAll(ctx context.Context, sql string, args ...any) ([]mssqlwire.Column, [][]any, error)
	Close() error
}

// mssqlConn is a SQL Server connection, as an execer.
type mssqlConn struct{ c tdsConn }

func dialMSSQL(ctx context.Context, addr, password, db string) (execer, error) {
	c, err := mssqlwire.Dial(ctx, mssqlwire.Config{Addr: addr, User: "sa", Password: password, Database: db,
		Encrypt: mssqlwire.EncryptOff, Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	return &mssqlConn{c}, nil
}

func (m *mssqlConn) exec(ctx context.Context, sql string) error {
	_, err := m.c.Exec(ctx, sql)
	return err
}

func (m *mssqlConn) count(ctx context.Context, sql string) (int64, error) {
	_, rows, err := m.c.QueryAll(ctx, sql)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return 0, fmt.Errorf("%s: no rows", sql)
	}
	return toInt(rows[0][0])
}

func (m *mssqlConn) close() { _ = m.c.Close() }

// mssqlSchema is shop.dbo.items, sysbench's rows as the others'.
const mssqlSchema = "CREATE TABLE dbo.items (id BIGINT NOT NULL PRIMARY KEY, k INT NOT NULL, c CHAR(120) NOT NULL, pad CHAR(60) NOT NULL)"

// mssqlPrepare makes the database shop with CDC on, and dbo.items afresh,
// captured; the capture job polls every second and takes up to 5000
// transactions a scan, so that it is not what bounds a mover.
func mssqlPrepare(ctx context.Context, o *options) error {
	c, err := mssqlDial(ctx, o.mssql, o.mssqlPassword, "master")
	if err != nil {
		return err
	}
	defer c.close()
	steps := []string{
		"IF DB_ID('shop') IS NULL CREATE DATABASE shop",
		"USE shop; IF (SELECT is_cdc_enabled FROM sys.databases WHERE name = 'shop') = 0 EXEC sys.sp_cdc_enable_db",
		"USE shop; IF OBJECT_ID('dbo.items') IS NOT NULL DROP TABLE dbo.items",
		"USE shop; " + mssqlSchema,
		"USE shop; EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'items', @role_name = NULL",
		"USE shop; EXEC sys.sp_cdc_change_job @job_type = N'capture', @maxtrans = 5000, @maxscans = 100, @pollinginterval = 1",
		"USE shop; EXEC sys.sp_cdc_stop_job @job_type = N'capture'",
		"USE shop; EXEC sys.sp_cdc_start_job @job_type = N'capture'",
	}
	for _, q := range steps {
		if err := c.exec(ctx, q); err != nil && !strings.Contains(q, "_job @job_type = N'capture'") {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}

// Dialers of the second round's sources, replaced by the tests.
var (
	rabbitDial    = dialRabbit
	mqttDial      = dialMQTT
	mssqlDial     = dialMSSQL
	rabbitPrepare = rabbitDeclare
)

// openPubSource opens n publishers of the scenario's broker.
func openPubSource(ctx context.Context, o *options, n int) (source, error) {
	var pubs []publisher
	for i := range n {
		var p publisher
		var err error
		if o.sc.source == "rabbitmq" {
			p, err = rabbitDial(ctx, o.rabbitmq, o.rabbitmqUser, o.rabbitmqPassword)
		} else {
			p, err = mqttDial(ctx, o.mqtt, i)
		}
		if err != nil {
			for _, p := range pubs {
				p.close()
			}
			return nil, err
		}
		pubs = append(pubs, p)
	}
	return &pubSource{pubs: pubs, batch: max(o.txRows, 1) * 10}, nil
}
