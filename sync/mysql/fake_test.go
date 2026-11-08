package syncmysql

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// fakeDB is a server's tables in memory, with transactions: what a
// transaction writes is seen only once it commits, and a connection closed
// rolls it back. A statement is all or nothing, as InnoDB's are.
type fakeDB struct {
	mu sync.Mutex
	// keys are each table's key columns.
	keys    map[string][]string
	data    map[string]map[string]map[string]any
	version string
	// fail, when set, is asked before each statement: an error it returns
	// is the statement's, and broken ones break the connection.
	fail  func(q string, args []any) error
	stmts []string
	dials int
	conns []*fakeConn
}

type fakeConn struct {
	db     *fakeDB
	work   map[string]map[string]map[string]any
	broken error
	closed bool
}

var errBroken = errors.New("fake: connection broken")

func newFakeDB(keys map[string][]string) *fakeDB {
	return &fakeDB{keys: keys, data: map[string]map[string]map[string]any{}, version: "8.4.3"}
}

func (d *fakeDB) dial(ctx context.Context, cfg mysqlwire.Config) (db, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials++
	if d.fail != nil {
		if err := d.fail("dial", nil); err != nil {
			return nil, err
		}
	}
	c := &fakeConn{db: d}
	d.conns = append(d.conns, c)
	return c, nil
}

func clone(m map[string]map[string]map[string]any) map[string]map[string]map[string]any {
	out := map[string]map[string]map[string]any{}
	for t, rows := range m {
		out[t] = maps.Clone(rows)
	}
	return out
}

// rows returns a table's committed rows, each as k=v,... sorted.
func (d *fakeDB) rows(table string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.data[table] {
		var cols []string
		for k, v := range r {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			if b, ok := v.(json.RawMessage); ok {
				v = string(b)
			}
			cols = append(cols, fmt.Sprintf("%s=%v", k, v))
		}
		sort.Strings(cols)
		out = append(out, strings.Join(cols, ","))
	}
	sort.Strings(out)
	return out
}

func (d *fakeDB) statements() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.stmts...)
}

func (c *fakeConn) ServerVersion() string { return c.db.version }
func (c *fakeConn) Err() error            { return c.broken }

func (c *fakeConn) Close() error {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	c.work, c.closed = nil, true
	if c.broken == nil {
		c.broken = mysqlwire.ErrClosed
	}
	return nil
}

// begin starts a statement: it reports the fail hook's error, breaking the
// connection when it says so.
func (c *fakeConn) begin(q string, args []any) error {
	if c.broken != nil {
		return c.broken
	}
	c.db.stmts = append(c.db.stmts, q)
	if c.db.fail != nil {
		if err := c.db.fail(q, args); err != nil {
			if errors.Is(err, errBroken) {
				c.broken, c.work = err, nil
			}
			return err
		}
	}
	return nil
}

// tables returns what a statement changes: the transaction's tables, or
// a copy of the committed ones that it commits.
func (c *fakeConn) tables() (map[string]map[string]map[string]any, func()) {
	if c.work != nil {
		w := clone(c.work)
		return w, func() { c.work = w }
	}
	w := clone(c.db.data)
	return w, func() { c.db.data = w }
}

var (
	insertRE = regexp.MustCompile(`^INSERT INTO (.+) \((.+)\) VALUES$`)
	deleteRE = regexp.MustCompile(`^DELETE FROM (.+) WHERE (.+) IN \((.*)\)$`)
)

func unquote(s string) []string {
	var out []string
	for _, p := range strings.Split(strings.Trim(s, "()"), ",") {
		out = append(out, strings.Trim(p, "`"))
	}
	return out
}

func (c *fakeConn) keyOf(table string, row map[string]any) string {
	var parts []string
	for _, k := range c.db.keys[table] {
		parts = append(parts, fmt.Sprint(row[k]))
	}
	return strings.Join(parts, "|")
}

func (c *fakeConn) Exec(ctx context.Context, q string, args ...any) (mysqlwire.Result, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if err := c.begin(q, args); err != nil {
		return mysqlwire.Result{}, err
	}
	switch {
	case q == "START TRANSACTION":
		c.work = clone(c.db.data)
	case q == "COMMIT":
		if c.work != nil {
			c.db.data = c.work
		}
		c.work = nil
	case strings.HasPrefix(q, "SET ") || q == "SELECT 1":
	case strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS "):
		table := strings.ReplaceAll(strings.Fields(q)[5], "`", "")
		if _, ok := c.db.keys[table]; !ok {
			c.db.keys[table] = []string{"pipeline"}
		}
	default:
		m := deleteRE.FindStringSubmatch(q)
		if m == nil {
			return mysqlwire.Result{}, &mysqlwire.Error{Code: 1064, State: "42000", Message: "fake: " + q}
		}
		table := strings.ReplaceAll(m[1], "`", "")
		cols := unquote(m[2])
		tabs, done := c.tables()
		var n uint64
		for len(args) > 0 {
			row := map[string]any{}
			for i, col := range cols {
				row[col] = args[i]
			}
			args = args[len(cols):]
			k := c.keyOf(table, row)
			if _, ok := tabs[table][k]; ok {
				delete(tabs[table], k)
				n++
			}
		}
		done()
		return mysqlwire.Result{AffectedRows: n}, nil
	}
	return mysqlwire.Result{}, nil
}

var selectRE = regexp.MustCompile("^SELECT `lineage`, `position` FROM (.+) WHERE `pipeline` = \\?$")

// QueryRow answers the checkpoint's SELECT from the committed rows, or the
// transaction's.
func (c *fakeConn) QueryRow(ctx context.Context, q string, args ...any) ([]any, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	if err := c.begin(q, args); err != nil {
		return nil, err
	}
	m := selectRE.FindStringSubmatch(q)
	if m == nil {
		return nil, &mysqlwire.Error{Code: 1064, State: "42000", Message: "fake: " + q}
	}
	tabs, _ := c.tables()
	row := tabs[strings.ReplaceAll(m[1], "`", "")][fmt.Sprint(args[0])]
	if row == nil {
		return nil, nil
	}
	return []any{[]byte(row["lineage"].(string)), row["position"]}, nil
}

// InsertRows upserts rows; a value "BAD" fails the statement whole, as a
// value a column refuses.
func (c *fakeConn) InsertRows(ctx context.Context, head string, rows [][]any, tail string) (mysqlwire.Result, error) {
	c.db.mu.Lock()
	defer c.db.mu.Unlock()
	var args []any
	for _, r := range rows {
		args = append(args, r...)
	}
	if err := c.begin(head, args); err != nil {
		return mysqlwire.Result{}, err
	}
	m := insertRE.FindStringSubmatch(head)
	if m == nil || !strings.HasPrefix(tail, "ON DUPLICATE KEY UPDATE ") {
		return mysqlwire.Result{}, &mysqlwire.Error{Code: 1064, State: "42000", Message: "fake: " + head + " " + tail}
	}
	table := strings.ReplaceAll(m[1], "`", "")
	if _, ok := c.db.keys[table]; !ok {
		return mysqlwire.Result{}, &mysqlwire.Error{Code: 1146, State: "42S02", Message: "no table " + table}
	}
	cols := unquote(m[2])
	for _, a := range args {
		if a == "BAD" {
			return mysqlwire.Result{}, &mysqlwire.Error{Code: 1366, State: "HY000", Message: "Incorrect value"}
		}
	}
	tabs, done := c.tables()
	if tabs[table] == nil {
		tabs[table] = map[string]map[string]any{}
	}
	for _, r := range rows {
		row := map[string]any{}
		for i, col := range cols {
			row[col] = r[i]
		}
		k := c.keyOf(table, row)
		if old := tabs[table][k]; old != nil {
			maps.Copy(old, row)
			row = old
		}
		tabs[table][k] = row
	}
	done()
	return mysqlwire.Result{AffectedRows: uint64(len(rows))}, nil
}

// loadSink loads a mysql sink with params, cleaned up with the test.
func loadSink(t *testing.T, params map[string]any) *Sink {
	t.Helper()
	m, err := load(t, params)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Sink)
}

func load(t *testing.T, params map[string]any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	return module.Load(fitsync.Sinks, config.ModuleRef{Module: "mysql", Params: v}, nil)
}

// fakeSink is a sink writing to d.
func fakeSink(t *testing.T, d *fakeDB, params map[string]any) *Sink {
	t.Helper()
	p := map[string]any{"username": "u"}
	maps.Copy(p, params)
	s := loadSink(t, p)
	s.dial = d.dial
	return s
}

// fakeServer is a MySQL server on loopback that logs a client in with
// mysql_native_password and answers every command OK, for the client's
// real dial; it closes when the test ends.
func fakeServer(t *testing.T, version string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer nc.Close()
				serveFake(nc, version)
			}()
		}
	}()
	return ln.Addr().String()
}

func serveFake(nc net.Conn, version string) {
	br := bufio.NewReader(nc)
	write := func(seq byte, p []byte) {
		h := []byte{byte(len(p)), byte(len(p) >> 8), byte(len(p) >> 16), seq}
		_, _ = nc.Write(append(h, p...))
	}
	read := func() ([]byte, bool) {
		var h [4]byte
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return nil, false
		}
		b := make([]byte, int(h[0])|int(h[1])<<8|int(h[2])<<16)
		_, err := io.ReadFull(br, b)
		return b, err == nil
	}
	const caps = 1<<0 | 1<<2 | 1<<9 | 1<<13 | 1<<15 | 1<<17 | 1<<18 | 1<<19 | 1<<21
	scramble := []byte("abcdefghij0123456789")
	g := []byte{10}
	g = append(g, version...)
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint32(g, 7)
	g = append(g, scramble[:8]...)
	g = append(g, 0)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps&0xffff))
	g = append(g, 45)
	g = binary.LittleEndian.AppendUint16(g, 2)
	g = binary.LittleEndian.AppendUint16(g, uint16(caps>>16))
	g = append(g, 21)
	g = append(g, make([]byte, 10)...)
	g = append(g, scramble[8:]...)
	g = append(g, 0)
	g = append(g, "mysql_native_password"...)
	g = append(g, 0)
	write(0, g)
	if _, ok := read(); !ok {
		return
	}
	ok := []byte{0, 0, 0, 2, 0, 0, 0}
	write(2, ok)
	for {
		p, alive := read()
		if !alive || len(p) == 0 || p[0] == 1 { // COM_QUIT
			return
		}
		if p[0] == 3 && string(p[1:]) == "SELECT 'row'" {
			// A result set of one text column and one row.
			lenenc := func(b []byte, v string) []byte { return append(append(b, byte(len(v))), v...) }
			col := lenenc(lenenc(lenenc(lenenc(lenenc(lenenc(nil, "def"), ""), ""), ""), "c"), "")
			col = append(col, 0x0c, 45, 0, 64, 0, 0, 0, 0xfd, 0, 0, 0, 0, 0)
			eof := []byte{0xfe, 0, 0, 2, 0}
			write(1, []byte{1})
			write(2, col)
			write(3, eof)
			write(4, lenenc(nil, "row"))
			write(5, eof)
			continue
		}
		write(1, ok)
	}
}
