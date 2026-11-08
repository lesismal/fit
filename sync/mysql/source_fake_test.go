package syncmysql

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// srcFake is a server as the source sees it: answers to queries, and the
// binlog streams each dump reads, in order.
type srcFake struct {
	mu      sync.Mutex
	version string
	mariadb bool
	// answers are the rows of queries, by their text; query, when set,
	// is asked first, and answers when ok.
	answers map[string][][]any
	cols    map[string][]mysqlwire.Column
	query   func(q string, args []any) (rows [][]any, err error, ok bool)
	execErr func(q string) error
	dialErr func(n int) error
	// streams are what each dump reads: events, then an error, or, past
	// them, a wait for the context's end.
	streams [][]evItem
	execs   []string
	cmds    [][]byte
	dumps   [][]byte
	dials   int
	open    int
}

type evItem struct {
	ev  []byte
	err error
}

func newSrcFake() *srcFake {
	return &srcFake{version: "8.4.3", answers: map[string][][]any{
		"SELECT @@GLOBAL.log_bin, @@GLOBAL.binlog_format, @@GLOBAL.binlog_row_image, @@GLOBAL.binlog_checksum": {{int64(1), []byte("ROW"), []byte("FULL"), []byte("CRC32")}},
		"SELECT @@GLOBAL.gtid_mode":       {{[]byte("ON")}},
		"SHOW BINARY LOG STATUS":          {{[]byte("bin.000001"), uint64(157), nil, nil, []byte(uuidA + ":1-5")}},
		"SHOW MASTER STATUS":              {{[]byte("bin.000001"), uint64(157), nil, nil, []byte(uuidA + ":1-5")}},
		"SELECT @@GLOBAL.gtid_binlog_pos": {{[]byte("0-1-5")}},
	}, cols: map[string][]mysqlwire.Column{}}
}

func (f *srcFake) dial(ctx context.Context, cfg mysqlwire.Config) (srcConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	if f.dialErr != nil {
		if err := f.dialErr(f.dials); err != nil {
			return nil, err
		}
	}
	f.open++
	return &srcFakeConn{f: f}, nil
}

type srcFakeConn struct {
	f      *srcFake
	stream []evItem
	closed bool
}

func (c *srcFakeConn) ServerVersion() string { return c.f.version }
func (c *srcFakeConn) IsMariaDB() bool       { return c.f.mariadb }

func (c *srcFakeConn) Close() error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	if !c.closed {
		c.f.open--
	}
	c.closed = true
	return nil
}

func (c *srcFakeConn) Exec(ctx context.Context, q string, args ...any) (mysqlwire.Result, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.execs = append(c.f.execs, q)
	if c.f.execErr != nil {
		if err := c.f.execErr(q); err != nil {
			return mysqlwire.Result{}, err
		}
	}
	return mysqlwire.Result{}, nil
}

func (c *srcFakeConn) QueryAll(ctx context.Context, q string, args ...any) ([]mysqlwire.Column, [][]any, error) {
	c.f.mu.Lock()
	query, answers, cols := c.f.query, c.f.answers, c.f.cols
	c.f.mu.Unlock()
	if query != nil {
		if rows, err, ok := query(q, args); ok {
			return cols[q], rows, err
		}
	}
	rows, ok := answers[q]
	if !ok {
		return nil, nil, &mysqlwire.Error{Code: 1064, State: "42000", Message: "fake: " + q}
	}
	return cols[q], rows, nil
}

func (c *srcFakeConn) Command(ctx context.Context, cmd byte, body []byte) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.cmds = append(c.f.cmds, append([]byte{cmd}, body...))
	return nil
}

func (c *srcFakeConn) StartBinlog(ctx context.Context, cmd byte, body []byte) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.dumps = append(c.f.dumps, append([]byte{cmd}, body...))
	if len(c.f.streams) > 0 {
		c.stream = c.f.streams[0]
		c.f.streams = c.f.streams[1:]
	}
	return nil
}

func (c *srcFakeConn) ReadEvent(ctx context.Context) ([]byte, error) {
	if len(c.stream) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	it := c.stream[0]
	c.stream = c.stream[1:]
	return it.ev, it.err
}

func (f *srcFake) dumpList() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.dumps...)
}

func (f *srcFake) execList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

const (
	uuidA = "3e11fa47-71ca-11e1-9e33-c80aa9429562"
	uuidB = "4e11fa47-71ca-11e1-9e33-c80aa9429562"
)

// Builders of binlog events.

func le(v uint64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(v >> (8 * i))
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func lenenc(n int) []byte {
	if n < 251 {
		return []byte{byte(n)}
	}
	return cat([]byte{0xfc}, le(uint64(n), 2))
}

func lstr(s string) []byte { return cat(lenenc(len(s)), []byte(s)) }

// eventAt is an event of type t with body, ending at logPos, with a CRC32
// when crc.
func eventAt(t binlog.EventType, body []byte, logPos uint32, crc bool) []byte {
	size := binlog.HeaderSize + len(body)
	if crc {
		size += 4
	}
	b := cat(le(1700000000, 4), []byte{byte(t)}, le(1, 4), le(uint64(size), 4), le(uint64(logPos), 4), le(0, 2), body)
	if crc {
		b = binary.LittleEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	}
	return b
}

// stream builds a binlog stream's events, each ending at the next offset.
type stream struct {
	pos   uint32
	items []evItem
	crc   bool
}

func newStream(file string, pos uint32, version string) *stream {
	s := &stream{pos: pos, crc: true}
	s.items = append(s.items, evItem{ev: eventAt(binlog.RotateEvent, cat(le(uint64(pos), 8), []byte(file)), 0, true)})
	ph := make([]byte, 42)
	ph[binlog.QueryEvent-1] = 13
	ph[binlog.RotateEvent-1] = 8
	ph[binlog.TableMapEvent-1] = 8
	for _, t := range []binlog.EventType{binlog.WriteRowsEventV2, binlog.UpdateRowsEventV2, binlog.DeleteRowsEventV2} {
		ph[t-1] = 10
	}
	ph[binlog.GTIDEvent-1] = 42
	v := make([]byte, 50)
	copy(v, version)
	s.items = append(s.items, evItem{ev: eventAt(binlog.FormatDescriptionEvent, cat(le(4, 2), v, le(0, 4), []byte{19}, ph, []byte{1}), 0, true)})
	return s
}

// add adds an event of t and body.
func (s *stream) add(t binlog.EventType, body []byte) *stream {
	s.pos += uint32(binlog.HeaderSize + len(body) + 4)
	s.items = append(s.items, evItem{ev: eventAt(t, body, s.pos, s.crc)})
	return s
}

func (s *stream) fail(err error) *stream {
	s.items = append(s.items, evItem{err: err})
	return s
}

func (s *stream) gtid(uuid string, gno int64) *stream {
	u := uuidBytes(uuid)
	return s.add(binlog.GTIDEvent, cat([]byte{1}, u, le(uint64(gno), 8), []byte{2}, le(0, 8), le(1, 8), le(1700000000123456, 7)))
}

func (s *stream) mariaGTID(seq uint64, domain uint32, flags byte) *stream {
	return s.add(binlog.MariaGTIDEvent, cat(le(seq, 8), le(uint64(domain), 4), []byte{flags}))
}

func (s *stream) query(db, sql string) *stream {
	return s.add(binlog.QueryEvent, cat(le(1, 4), le(0, 4), []byte{byte(len(db))}, le(0, 2), le(0, 2), []byte(db), []byte{0}, []byte(sql)))
}

func (s *stream) xid() *stream { return s.add(binlog.XIDEvent, le(9, 8)) }

func (s *stream) heartbeat() *stream {
	s.items = append(s.items, evItem{ev: eventAt(binlog.HeartbeatEvent, []byte("bin.000001"), s.pos, s.crc)})
	return s
}

// table is a table of an INT and a VARCHAR column: names, when given, in
// the optional metadata with the primary key pk.
func (s *stream) table(id uint64, db, name string, names []string, pk ...int) *stream {
	types := []byte{binlog.TypeLong, binlog.TypeVarChar}
	meta := le(80, 2)
	b := cat(le(id, 6), le(1, 2), []byte{byte(len(db))}, []byte(db), []byte{0}, []byte{byte(len(name))}, []byte(name), []byte{0},
		lenenc(len(types)), types, lenenc(len(meta)), meta, []byte{0xff})
	if names != nil {
		var nb []byte
		for _, n := range names {
			nb = append(nb, lstr(n)...)
		}
		b = cat(b, []byte{binlog.MetaColumnName}, lenenc(len(nb)), nb)
		var pb []byte
		for _, i := range pk {
			pb = append(pb, byte(i))
		}
		b = cat(b, []byte{binlog.MetaSimplePrimaryKey}, lenenc(len(pb)), pb)
	}
	return s.add(binlog.TableMapEvent, b)
}

// img is an image of the two columns.
func img(id int32, v string) []byte {
	if v == "\x00null" {
		return cat([]byte{2}, le(uint64(uint32(id)), 4))
	}
	return cat([]byte{0}, le(uint64(uint32(id)), 4), []byte{byte(len(v))}, []byte(v))
}

func (s *stream) rows(t binlog.EventType, id uint64, images ...[]byte) *stream {
	b := cat(le(id, 6), le(binlog.RowsStmtEnd, 2), le(2, 2), lenenc(2), []byte{3})
	if t == binlog.UpdateRowsEventV2 {
		b = append(b, 3)
	}
	return s.add(t, cat(append([][]byte{b}, images...)...))
}

func uuidBytes(u string) []byte {
	var b []byte
	h := strings.ReplaceAll(u, "-", "")
	for i := 0; i < len(h); i += 2 {
		var x byte
		for _, c := range h[i : i+2] {
			x <<= 4
			switch {
			case c >= '0' && c <= '9':
				x |= byte(c - '0')
			default:
				x |= byte(c-'a') + 10
			}
		}
		b = append(b, x)
	}
	return b
}

// newSource provisions the source mysql with params, its connections
// made by f.
func newSource(t *testing.T, f *srcFake, params map[string]any) *Source {
	t.Helper()
	s, err := provisionSource(t, f, params)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func provisionSource(t *testing.T, f *srcFake, params map[string]any) (*Source, error) {
	t.Helper()
	p := map[string]any{"username": "repl", "server_id": 4101}
	for k, v := range params {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(fitsync.Sources, config.ModuleRef{Module: "mysql", Params: v}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	s := m.(*Source)
	s.dial = f.dial
	return s, nil
}

// runSrc runs s on a goroutine of its own, its records on a channel.
type runSrc struct {
	recs   chan *fitsync.Record
	lags   chan fitsync.SourceLag
	cancel context.CancelFunc
	done   chan error
}

func startSrc(s fitsync.Source, from fitsync.Position, once bool) *runSrc {
	ctx, cancel := context.WithCancel(context.Background())
	r := &runSrc{recs: make(chan *fitsync.Record, 1000), lags: make(chan fitsync.SourceLag, 100), cancel: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(rec *fitsync.Record) error {
			select {
			case r.recs <- rec:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, Lag: func(l fitsync.SourceLag) {
			select {
			case r.lags <- l:
			default:
			}
		}})
	}()
	return r
}

func (r *runSrc) take(t *testing.T, n int) []*fitsync.Record {
	t.Helper()
	var out []*fitsync.Record
	guard := time.After(time.Minute)
	for len(out) < n {
		select {
		case rec := <-r.recs:
			out = append(out, rec)
		case err := <-r.done:
			t.Fatalf("the source stopped after %d of %d records: %v", len(out), n, err)
		case <-guard:
			t.Fatalf("%d of %d records", len(out), n)
		}
	}
	return out
}

// wait returns the source's error once it stops by itself.
func (r *runSrc) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(time.Minute):
		t.Fatal("the source did not stop")
		return nil
	}
}

func (r *runSrc) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	return r.wait(t)
}

var errCut = errors.New("fake: connection cut")
