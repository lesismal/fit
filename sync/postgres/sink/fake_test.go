package syncpgsink

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a PostgreSQL backend in the process that understands the
// statements the sink makes: tables of rows by key, transactions with
// savepoints, multi-row upserts and deletes, temporary tables and COPY.
// Every statement is logged, and inject may fail any of them.
type fake struct {
	t  *testing.T
	ln net.Listener

	mu     sync.Mutex
	tables map[string]map[string]row
	log    []string
	conns  int
	// inject, when set, is asked about each statement and its arguments:
	// an SQLSTATE fails it, "drop" closes the connection.
	inject func(sql string, args []*string) string
}

// row is a row's columns, by name, nil for NULL.
type row map[string]*string

func newFake(t *testing.T) *fake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{t: t, ln: ln, tables: map[string]map[string]row{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.serve(nc)
		}
	}()
	return f
}

func (f *fake) addr() string { return f.ln.Addr().String() }

func (f *fake) setInject(fn func(sql string, args []*string) string) {
	f.mu.Lock()
	f.inject = fn
	f.mu.Unlock()
}

// table returns a copy of a table's rows, by key.
func (f *fake) table(name string) map[string]row {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.tables[name])
}

func (f *fake) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fake) clearLog() {
	f.mu.Lock()
	f.log = nil
	f.mu.Unlock()
}

// session is one connection's state.
type session struct {
	f  *fake
	nc net.Conn
	br *bufio.Reader

	inTx, aborted bool
	work          map[string]map[string]row
	savepoints    []map[string]map[string]row
	temp          map[string]*tempTable
	sql           string
}

type tempTable struct {
	cols []string
	rows [][]*string
}

func cloneTables(t map[string]map[string]row) map[string]map[string]row {
	out := make(map[string]map[string]row, len(t))
	for k, v := range t {
		out[k] = maps.Clone(v)
	}
	return out
}

func (f *fake) serve(nc net.Conn) {
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(time.Minute)) // a hang guard
	s := &session{f: f, nc: nc, br: bufio.NewReader(nc), temp: map[string]*tempTable{}}
	var n uint32
	if binary.Read(s.br, binary.BigEndian, &n) != nil || n < 8 {
		return
	}
	if _, err := io.ReadFull(s.br, make([]byte, n-4)); err != nil {
		return
	}
	s.send('R', u32(0))
	s.send('S', cs("server_version"), cs("16.4"))
	s.send('K', u32(1), u32(2))
	s.ready()
	for {
		t, body := s.read()
		switch t {
		case 0, 'X':
			return
		case 'Q':
			sql, _, _ := strings.Cut(string(body), "\x00")
			if !s.query(sql) {
				return
			}
		case 'P':
			_, rest, _ := strings.Cut(string(body), "\x00")
			s.sql, _, _ = strings.Cut(rest, "\x00")
		case 'D':
		case 'S':
			// The Sync after Parse and Describe.
			s.send('1')
			np := 0
			for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(s.sql, -1) {
				np = max(np, atoi(m[1]))
			}
			b := u16(uint16(np))
			for range np {
				b = append(b, u32(25)...)
			}
			s.send('t', b)
			s.send('n')
			s.ready()
		case 'B':
			args := parseBind(body)
			s.read() // Execute
			s.read() // Sync
			s.send('2')
			tag, code := s.exec(s.sql, args)
			if code == "drop" {
				return
			}
			s.result(tag, code)
		}
	}
}

func (s *session) result(tag, code string) {
	if code != "" {
		if s.inTx {
			s.aborted = true
		}
		s.send('E', cs("SERROR"), cs("C"+code), cs("Mthe fake refuses it"), []byte{0})
	} else {
		s.send('C', cs(tag))
	}
	s.ready()
}

// query runs a simple query, and reports whether the connection goes on.
func (s *session) query(sql string) bool {
	switch {
	case sql == "SELECT 1":
		s.send('T', u16(1), cs("?column?"), u32(0), u16(0), u32(23), u16(4), u32(0xFFFFFFFF), u16(0))
		s.send('D', u16(1), u32(1), []byte("1"))
		s.send('C', cs("SELECT 1"))
		s.ready()
		return true
	case strings.HasPrefix(sql, "COPY "):
		return s.copy(sql)
	}
	tag, code := s.exec(sql, nil)
	if code == "drop" {
		return false
	}
	s.result(tag, code)
	return true
}

var (
	reUpsert = regexp.MustCompile(`^INSERT INTO (\S+) \(([^)]*)\) VALUES .* ON CONFLICT \(([^)]*)\) DO (NOTHING|UPDATE SET .*)$`)
	reMerge  = regexp.MustCompile(`^INSERT INTO (\S+) \(([^)]*)\) SELECT \S+ FROM (\S+) ON CONFLICT \(([^)]*)\) DO (NOTHING|UPDATE SET .*)$`)
	reDelete = regexp.MustCompile(`^DELETE FROM (\S+) WHERE \(?([^)]*?)\)? IN \(`)
	reTemp   = regexp.MustCompile(`^CREATE TEMPORARY TABLE (\S+) ON COMMIT DROP AS SELECT (\S+) FROM (\S+) WITH NO DATA$`)
	reCopy   = regexp.MustCompile(`^COPY (\S+) \(([^)]*)\) FROM STDIN$`)
)

// exec runs a statement, returning its tag or an SQLSTATE.
func (s *session) exec(sql string, args []*string) (string, string) {
	f := s.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, sql)
	if f.inject != nil {
		if code := f.inject(sql, args); code != "" {
			return "", code
		}
	}
	switch {
	case sql == "BEGIN":
		s.inTx, s.aborted, s.work, s.savepoints = true, false, cloneTables(f.tables), nil
		return "BEGIN", ""
	case sql == "COMMIT":
		if !s.aborted {
			f.tables = s.work
		}
		s.end()
		return "COMMIT", ""
	case sql == "ROLLBACK":
		s.end()
		return "ROLLBACK", ""
	case strings.HasPrefix(sql, "ROLLBACK TO SAVEPOINT "):
		s.work = cloneTables(s.savepoints[len(s.savepoints)-1])
		s.aborted = false
		return "ROLLBACK", ""
	case s.aborted:
		return "", "25P02"
	case strings.HasPrefix(sql, "SAVEPOINT "):
		s.savepoints = append(s.savepoints, cloneTables(s.work))
		return "SAVEPOINT", ""
	}
	if !s.inTx {
		f.t.Errorf("%s outside a transaction", sql)
		return "", "25000"
	}
	if reCopy.MatchString(sql) {
		return "COPY", ""
	}
	if m := reTemp.FindStringSubmatch(sql); m != nil {
		s.temp[m[1]] = &tempTable{cols: strings.Split(m[2], ",")}
		return "SELECT 0", ""
	}
	if m := reMerge.FindStringSubmatch(sql); m != nil {
		tt := s.temp[m[3]]
		return s.upsert(m[1], strings.Split(m[2], ","), strings.Split(m[4], ","), m[5] == "NOTHING", tt.rows)
	}
	if m := reUpsert.FindStringSubmatch(sql); m != nil {
		cols := strings.Split(m[2], ",")
		var rows [][]*string
		for i := 0; i < len(args); i += len(cols) {
			rows = append(rows, args[i:i+len(cols)])
		}
		return s.upsert(m[1], cols, strings.Split(m[3], ","), m[4] == "NOTHING", rows)
	}
	if m := reDelete.FindStringSubmatch(sql); m != nil {
		keys := strings.Split(m[2], ",")
		t := s.work[m[1]]
		n := 0
		for i := 0; i < len(args); i += len(keys) {
			k := keyOf(args[i : i+len(keys)])
			if _, ok := t[k]; ok {
				delete(t, k)
				n++
			}
		}
		return "DELETE " + strconv.Itoa(n), ""
	}
	f.t.Errorf("the fake does not know %q", sql)
	return "", "42601"
}

func (s *session) end() {
	s.inTx, s.aborted, s.work, s.savepoints = false, false, nil, nil
	clear(s.temp)
}

func keyOf(vals []*string) string {
	var b []string
	for _, v := range vals {
		if v == nil {
			b = append(b, "NULL")
		} else {
			b = append(b, strconv.Quote(*v))
		}
	}
	return strings.Join(b, ",")
}

func (s *session) upsert(table string, cols, keys []string, nothing bool, rows [][]*string) (string, string) {
	t := s.work[table]
	if t == nil {
		t = map[string]row{}
		s.work[table] = t
	}
	at := map[string]int{}
	for i, c := range cols {
		at[c] = i
	}
	seen := map[string]bool{}
	for _, r := range rows {
		var kv []*string
		for _, k := range keys {
			kv = append(kv, r[at[k]])
		}
		k := keyOf(kv)
		if seen[k] {
			return "", "21000"
		}
		seen[k] = true
		old, ok := t[k]
		if ok && nothing {
			continue
		}
		next := row{}
		if ok {
			next = maps.Clone(old)
		}
		for i, c := range cols {
			next[c] = r[i]
		}
		t[k] = next
	}
	return "INSERT 0 " + strconv.Itoa(len(rows)), ""
}

// copy runs a COPY FROM STDIN into a temporary table.
func (s *session) copy(sql string) bool {
	tag, code := s.exec(sql, nil)
	if code == "drop" {
		return false
	}
	m := reCopy.FindStringSubmatch(sql)
	tt := s.temp[m[1]]
	if code == "" && tt == nil {
		code = "42P01"
	}
	if code != "" {
		s.result(tag, code)
		return true
	}
	s.send('G', []byte{0}, u16(0))
	var data []byte
	for {
		t, body := s.read()
		switch t {
		case 'd':
			data = append(data, body...)
			continue
		case 'c':
			n := 0
			for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
				var r []*string
				for _, field := range strings.Split(line, "\t") {
					r = append(r, unescapeCopy(field))
				}
				tt.rows = append(tt.rows, r)
				n++
			}
			s.result("COPY "+strconv.Itoa(n), "")
		case 'f':
			s.result("", "57014")
		default:
			return false
		}
		return true
	}
}

func unescapeCopy(s string) *string {
	if s == `\N` {
		return nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	out := b.String()
	return &out
}

func parseBind(b []byte) []*string {
	_, b = cut(b) // portal
	_, b = cut(b) // statement
	nf := int(binary.BigEndian.Uint16(b))
	b = b[2+2*nf:]
	np := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	args := make([]*string, np)
	for i := range np {
		n := int32(binary.BigEndian.Uint32(b))
		b = b[4:]
		if n < 0 {
			continue
		}
		v := string(b[:n])
		args[i] = &v
		b = b[n:]
	}
	return args
}

func cut(b []byte) (string, []byte) {
	i := 0
	for b[i] != 0 {
		i++
	}
	return string(b[:i]), b[i+1:]
}

func (s *session) read() (byte, []byte) {
	var hdr [5]byte
	if _, err := io.ReadFull(s.br, hdr[:]); err != nil {
		return 0, nil
	}
	b := make([]byte, binary.BigEndian.Uint32(hdr[1:])-4)
	if _, err := io.ReadFull(s.br, b); err != nil {
		return 0, nil
	}
	return hdr[0], b
}

func (s *session) send(t byte, body ...[]byte) {
	var all []byte
	for _, b := range body {
		all = append(all, b...)
	}
	m := append([]byte{t, 0, 0, 0, 0}, all...)
	binary.BigEndian.PutUint32(m[1:], uint32(len(all)+4))
	s.nc.Write(m)
}

func (s *session) ready() {
	st := byte('I')
	switch {
	case s.aborted:
		st = 'E'
	case s.inTx:
		st = 'T'
	}
	s.send('Z', []byte{st})
}

func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func cs(s string) []byte  { return append([]byte(s), 0) }

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// dump returns a table's rows as text, sorted, each its columns in order.
func dump(t map[string]row, cols ...string) string {
	var lines []string
	for _, r := range t {
		var vals []string
		for _, c := range cols {
			if v := r[`"`+c+`"`]; v != nil {
				vals = append(vals, *v)
			} else {
				vals = append(vals, "NULL")
			}
		}
		lines = append(lines, strings.Join(vals, "|"))
	}
	sort.Strings(lines)
	return fmt.Sprint(lines)
}
