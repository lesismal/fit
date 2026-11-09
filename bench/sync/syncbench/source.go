package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// A source is where the records come from: it writes records first+1 to
// first+n, and returns once they are committed (or written, for files).
type source interface {
	write(ctx context.Context, first, n int) error
	close()
}

// The rows are sysbench's: an id, an int and two fixed strings, about 200
// bytes a row.
const (
	mysqlSchema = "CREATE TABLE shop.items (id BIGINT NOT NULL PRIMARY KEY, k INT NOT NULL, " +
		"c CHAR(120) NOT NULL, pad CHAR(60) NOT NULL) ENGINE=InnoDB"
	pgSchema = "CREATE TABLE items (id BIGINT NOT NULL PRIMARY KEY, k INT NOT NULL, " +
		"c CHAR(120) NOT NULL, pad CHAR(60) NOT NULL)"
)

// digits makes n characters of sysbench's c and pad from seed: groups of
// eleven digits joined by dashes.
func digits(seed, n int) string {
	var b strings.Builder
	x := uint64(seed)*2654435761 + 1
	for b.Len() < n {
		if b.Len() > 0 && b.Len()%12 == 11 {
			b.WriteByte('-')
			continue
		}
		x = x*6364136223846793005 + 1442695040888963407
		b.WriteByte('0' + byte(x>>59)%10)
	}
	return b.String()
}

// row is record i's values as SQL text.
func row(i int) string {
	return fmt.Sprintf("(%d,%d,'%s','%s')", i, i*7919%1000003, digits(i, 120), digits(i+7, 60))
}

// line is record i of the files scenario, size bytes with its newline.
func line(i, size int) []byte {
	b := make([]byte, 0, size)
	b = append(b, "2026-11-09T08:00:00.000000Z INFO bench "...)
	b = strconv.AppendInt(b, int64(i), 10)
	b = append(b, ' ')
	for len(b) < size-1 {
		b = append(b, byte('a'+len(b)%26))
	}
	b = b[:size-1]
	return append(b, '\n')
}

// fileSource appends lines to files in a directory: in-0.log to
// in-<files-1>.log, a record's file chosen by its number.
type fileSource struct {
	dir   string
	files int
	size  int
}

func (s *fileSource) path(i int) string {
	return filepath.Join(s.dir, fmt.Sprintf("in-%d.log", i))
}

// write appends records first+1..first+n: each file gets a run of them, so
// that every file holds about as many.
func (s *fileSource) write(_ context.Context, first, n int) error {
	files := min(s.files, n)
	per := (n + files - 1) / files
	for f := range files {
		lo, hi := first+f*per, min(first+(f+1)*per, first+n)
		if lo >= hi {
			break
		}
		fh, err := os.OpenFile(s.path(f), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		buf := make([]byte, 0, 1<<20)
		for i := lo; i < hi; i++ {
			buf = append(buf, line(i+1, s.size)...)
			if len(buf) >= 1<<20-s.size {
				if _, err = fh.Write(buf); err != nil {
					break
				}
				buf = buf[:0]
			}
		}
		if err == nil {
			_, err = fh.Write(buf)
		}
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *fileSource) close() {}

// execer is a connection to a database that runs SQL text.
type execer interface {
	exec(ctx context.Context, sql string) error
	count(ctx context.Context, sql string) (int64, error)
	close()
}

type mysqlConn struct{ c *mysqlwire.Conn }

func dialMySQL(ctx context.Context, addr, user, password string) (execer, error) {
	c, err := mysqlwire.Dial(ctx, mysqlwire.Config{Addr: addr, User: user, Password: password, Timeout: time.Minute})
	if err != nil {
		return nil, err
	}
	return &mysqlConn{c}, nil
}

func (m *mysqlConn) exec(ctx context.Context, sql string) error {
	_, err := m.c.Exec(ctx, sql)
	return err
}

func (m *mysqlConn) count(ctx context.Context, sql string) (int64, error) {
	rows, err := m.c.Query(ctx, sql)
	if err != nil {
		return 0, err
	}
	var n int64
	for rows.Next() {
		n, err = toInt(rows.Values()[0])
	}
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func (m *mysqlConn) close() { _ = m.c.Close() }

type pgConn struct{ c *pgwire.Conn }

func dialPG(ctx context.Context, addr, user, password, db string) (execer, error) {
	c, err := pgwire.Connect(ctx, pgwire.Config{Addr: addr, User: user, Password: password, Database: db,
		SSLMode: pgwire.SSLDisable, ApplicationName: "syncbench"})
	if err != nil {
		return nil, err
	}
	return &pgConn{c}, nil
}

func (p *pgConn) exec(ctx context.Context, sql string) error {
	_, err := p.c.SimpleQuery(ctx, sql)
	return err
}

func (p *pgConn) count(ctx context.Context, sql string) (int64, error) {
	res, err := p.c.SimpleQuery(ctx, sql)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 || len(res[0].Rows) == 0 || len(res[0].Rows[0]) == 0 {
		return 0, fmt.Errorf("%s: no rows", sql)
	}
	return strconv.ParseInt(string(res[0].Rows[0][0]), 10, 64)
}

func (p *pgConn) close() { _ = p.c.Close() }

// toInt reads a count as the MySQL client gives it.
func toInt(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case uint64:
		return int64(x), nil
	case []byte:
		return strconv.ParseInt(string(x), 10, 64)
	case string:
		return strconv.ParseInt(x, 10, 64)
	}
	return 0, fmt.Errorf("a count of %T", v)
}

// sqlSource writes rows through a pool of connections, a transaction of
// txRows single-row INSERTs at a time, the transactions spread over the
// connections; multi sends a transaction as one text of several
// statements, as PostgreSQL's simple query protocol takes it.
type sqlSource struct {
	conns  []execer
	table  string
	txRows int
	multi  bool
	// begin and commit bound a multi transaction: BEGIN; and COMMIT when
	// empty, SQL Server's BEGIN TRANSACTION; and COMMIT TRANSACTION.
	begin, commit string
}

func (s *sqlSource) write(ctx context.Context, first, n int) error {
	type tx struct{ lo, hi int }
	work := make(chan tx)
	errs := make(chan error, len(s.conns))
	var wg sync.WaitGroup
	for _, c := range s.conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				if err := s.tx(ctx, c, t.lo, t.hi); err != nil {
					errs <- err
					for range work {
					}
					return
				}
			}
		}()
	}
	for lo := first; lo < first+n; lo += s.txRows {
		work <- tx{lo, min(lo+s.txRows, first+n)}
	}
	close(work)
	wg.Wait()
	close(errs)
	return <-errs
}

// tx writes rows lo+1..hi as one transaction.
func (s *sqlSource) tx(ctx context.Context, c execer, lo, hi int) error {
	if s.multi {
		begin, commit := s.begin, s.commit
		if begin == "" {
			begin, commit = "BEGIN;", "COMMIT"
		}
		var b strings.Builder
		b.WriteString(begin)
		for i := lo; i < hi; i++ {
			b.WriteString("INSERT INTO " + s.table + " (id,k,c,pad) VALUES " + row(i+1) + ";")
		}
		b.WriteString(commit)
		return c.exec(ctx, b.String())
	}
	if err := c.exec(ctx, "START TRANSACTION"); err != nil {
		return err
	}
	for i := lo; i < hi; i++ {
		if err := c.exec(ctx, "INSERT INTO "+s.table+" (id,k,c,pad) VALUES "+row(i+1)); err != nil {
			return err
		}
	}
	return c.exec(ctx, "COMMIT")
}

func (s *sqlSource) close() {
	for _, c := range s.conns {
		c.close()
	}
}

// bulkInsert fills table with records 1..n, a thousand rows a statement,
// over the connections: a batch job's source, written before the job.
func bulkInsert(ctx context.Context, conns []execer, table string, n int) error {
	const per = 1000
	work := make(chan int)
	errs := make(chan error, len(conns))
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for lo := range work {
				var b strings.Builder
				b.WriteString("INSERT INTO " + table + " (id,k,c,pad) VALUES ")
				for i := lo; i < min(lo+per, n); i++ {
					if i > lo {
						b.WriteByte(',')
					}
					b.WriteString(row(i + 1))
				}
				if err := c.exec(ctx, b.String()); err != nil {
					errs <- err
					for range work {
					}
					return
				}
			}
		}()
	}
	for lo := 0; lo < n; lo += per {
		work <- lo
	}
	close(work)
	wg.Wait()
	close(errs)
	return <-errs
}

// Dialers, replaced by the tests.
var (
	mysqlDial = dialMySQL
	pgDial    = dialPG
)

// dialPool opens n connections with dial.
func dialPool(ctx context.Context, n int, dial func(context.Context) (execer, error)) ([]execer, error) {
	var conns []execer
	for range n {
		c, err := dial(ctx)
		if err != nil {
			for _, c := range conns {
				c.close()
			}
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, nil
}

// openSource opens the scenario's source with n connections.
func openSource(ctx context.Context, o *options, n int) (source, error) {
	switch o.sc.source {
	case "files":
		return &fileSource{dir: o.inDir, files: o.files, size: o.lineBytes}, nil
	case "rabbitmq", "mqtt":
		return openPubSource(ctx, o, n)
	case "mssql":
		conns, err := dialPool(ctx, n, func(ctx context.Context) (execer, error) {
			return mssqlDial(ctx, o.mssql, o.mssqlPassword, "shop")
		})
		if err != nil {
			return nil, err
		}
		return &sqlSource{conns: conns, table: "dbo.items", txRows: o.txRows, multi: true, begin: "BEGIN TRANSACTION;", commit: "COMMIT TRANSACTION"}, nil
	case "pg":
		conns, err := dialPool(ctx, n, func(ctx context.Context) (execer, error) {
			return pgDial(ctx, o.pg, o.pgUser, o.pgPassword, o.pgDB)
		})
		if err != nil {
			return nil, err
		}
		return &sqlSource{conns: conns, table: "items", txRows: o.txRows, multi: true}, nil
	}
	conns, err := dialPool(ctx, n, func(ctx context.Context) (execer, error) {
		return mysqlDial(ctx, o.mysql, o.mysqlUser, o.mysqlPassword)
	})
	if err != nil {
		return nil, err
	}
	return &sqlSource{conns: conns, table: "shop.items", txRows: o.txRows}, nil
}
