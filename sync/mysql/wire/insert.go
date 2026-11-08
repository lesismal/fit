package mysqlwire

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MaxPlaceholders is the most parameters a statement may have.
const MaxPlaceholders = 65535

// MaxAllowedPacket is the server's max_allowed_packet, asked once per
// connection.
func (c *Conn) MaxAllowedPacket(ctx context.Context) (int, error) {
	if c.maxAllow > 0 {
		return c.maxAllow, nil
	}
	rows, err := c.Query(ctx, "SELECT @@max_allowed_packet")
	if err != nil {
		return 0, err
	}
	var n int
	if rows.Next() {
		switch v := rows.Values()[0].(type) {
		case int64:
			n = int(v)
		case uint64:
			n = int(v)
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("mysql: max_allowed_packet not read")
	}
	c.maxAllow = n
	return n, nil
}

// InsertRows runs head, then "(?, ...)" for each row joined by commas,
// then tail: head as "INSERT INTO t (a, b) VALUES" and tail as "" or "ON
// DUPLICATE KEY UPDATE ...", with each row's values as its parameters. The
// rows go in as few statements as the placeholder limit and the server's
// max_allowed_packet allow, in order, each prepared once for its count of
// rows and closed at the end; every row must have as many values as the
// first. The result's AffectedRows are the statements', summed, and its
// LastInsertID the first statement's. A failure past the first statement
// leaves the ones before it done, unless they run in a transaction.
func (c *Conn) InsertRows(ctx context.Context, head string, rows [][]any, tail string) (Result, error) {
	var total Result
	if len(rows) == 0 {
		return total, nil
	}
	ncols := len(rows[0])
	if ncols == 0 || ncols > MaxPlaceholders {
		return total, fmt.Errorf("mysql: rows of %d values", ncols)
	}
	for i, r := range rows {
		if len(r) != ncols {
			return total, fmt.Errorf("mysql: row %d has %d values, the first %d", i, len(r), ncols)
		}
	}
	maxPacket, err := c.MaxAllowedPacket(ctx)
	if err != nil {
		return total, err
	}
	// The execute packet's fixed part, and room for headers and slack.
	budget := maxPacket - 1024
	tuple := "(" + strings.Repeat("?,", ncols-1) + "?)"
	maxRows := MaxPlaceholders / ncols

	stmts := map[int]*Stmt{}
	defer func() {
		for _, st := range stmts {
			if c.broken == nil && c.rows == nil {
				_ = st.Close(context.WithoutCancel(ctx))
			}
		}
	}()
	first := true
	args := make([]any, 0, min(len(rows), maxRows)*ncols)
	for len(rows) > 0 {
		// The rows that fit: their values and types, and the SQL, which
		// is sent in the prepare.
		n, size := 0, 0
		for n < len(rows) && n < maxRows {
			rs := rowSize(rows[n])
			sqlSize := len(head) + len(tail) + (n+1)*(len(tuple)+1)
			if n > 0 && (size+rs+(n+1)*ncols/8+16 > budget || sqlSize > budget) {
				break
			}
			size += rs
			n++
		}
		st := stmts[n]
		if st == nil {
			var sb strings.Builder
			sb.Grow(len(head) + len(tail) + n*(len(tuple)+1) + 2)
			sb.WriteString(head)
			sb.WriteByte(' ')
			for i := range n {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(tuple)
			}
			if tail != "" {
				sb.WriteByte(' ')
				sb.WriteString(tail)
			}
			if st, err = c.Prepare(ctx, sb.String()); err != nil {
				return total, err
			}
			stmts[n] = st
		}
		args = args[:0]
		for _, r := range rows[:n] {
			args = append(args, r...)
		}
		res, err := st.Exec(ctx, args...)
		if err != nil {
			return total, err
		}
		total.AffectedRows += res.AffectedRows
		total.Status, total.Warnings = res.Status, total.Warnings+res.Warnings
		if first {
			total.LastInsertID = res.LastInsertID
			first = false
		}
		rows = rows[n:]
	}
	clear(args)
	return total, nil
}

// rowSize is about what a row's values take in an execute packet.
func rowSize(r []any) int {
	n := 0
	for _, v := range r {
		n += 2 + 9
		switch v := v.(type) {
		case string:
			n += len(v)
		case []byte:
			n += len(v)
		case json.RawMessage:
			n += len(v)
		case time.Time, DateTime, time.Duration:
			n += 4
		}
	}
	return n
}
