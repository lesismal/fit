package synctest

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// tidbRetryFor bounds how long a statement to the TiDB of tidb.sh is tried
// again: generous, as a CI runner holding every server of run.sh may stall
// TiKV for a while.
const tidbRetryFor = 3 * time.Minute

// tidbRetryable reports whether TiDB answered err for something that may
// go through when tried again: its schema not loaded yet (8027, 8028), a
// transaction to retry (8002, 8022), PD or TiKV timing out or busy (9001
// to 9005), a write conflict (9007), or a connection lost. Others, as a
// syntax error or a duplicate key, are the statement's.
func tidbRetryable(err error) bool {
	var me *mysqlwire.Error
	if errors.As(err, &me) {
		switch me.Code {
		case 8002, 8022, 8027, 8028, 9001, 9002, 9003, 9004, 9005, 9007:
			return true
		}
		return false
	}
	return err != nil
}

// tidbConn is a connection to the TiDB of tidb.sh that dials again after a
// broken connection and tries again what TiDB says may be tried again,
// within tidbRetryFor. Statements given to it must be idempotent: a retry
// after a lost answer runs them again.
type tidbConn struct {
	cfg   mysqlwire.Config
	setup []string // run on each new connection
	c     *mysqlwire.Conn
}

func (tc *tidbConn) Close() {
	if tc.c != nil {
		tc.c.Close()
		tc.c = nil
	}
}

// do runs fn on a connection until it succeeds, fails for good, or
// tidbRetryFor passes.
func (tc *tidbConn) do(ctx context.Context, what string, fn func(ctx context.Context, c *mysqlwire.Conn) error) error {
	deadline := time.Now().Add(tidbRetryFor)
	backoff := 100 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := tc.try(ctx, fn)
		if err == nil || !tidbRetryable(err) || ctx.Err() != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: still failing after %d attempts: %w", what, attempt, err)
		}
		if tc.c != nil && tc.c.Err() != nil {
			tc.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Second)
	}
}

func (tc *tidbConn) try(ctx context.Context, fn func(ctx context.Context, c *mysqlwire.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if tc.c == nil {
		c, err := mysqlwire.Dial(ctx, tc.cfg)
		if err != nil {
			return err
		}
		for _, q := range tc.setup {
			if _, err := c.Exec(ctx, q); err != nil {
				c.Close()
				return err
			}
		}
		tc.c = c
	}
	return fn(ctx, tc.c)
}

// exec runs one statement.
func (tc *tidbConn) exec(ctx context.Context, q string, args ...any) error {
	return tc.do(ctx, q, func(ctx context.Context, c *mysqlwire.Conn) error {
		_, err := c.Exec(ctx, q, args...)
		return err
	})
}

// tx runs statements in one transaction, rolled back and run again whole
// when TiDB says so.
func (tc *tidbConn) tx(ctx context.Context, stmts ...func(ctx context.Context, c *mysqlwire.Conn) error) error {
	return tc.do(ctx, "a transaction", func(ctx context.Context, c *mysqlwire.Conn) error {
		if _, err := c.Exec(ctx, "BEGIN"); err != nil {
			return err
		}
		for _, s := range append(stmts, func(ctx context.Context, c *mysqlwire.Conn) error {
			_, err := c.Exec(ctx, "COMMIT")
			return err
		}) {
			if err := s(ctx, c); err != nil {
				if c.Err() == nil {
					c.Exec(ctx, "ROLLBACK")
				}
				return err
			}
		}
		return nil
	})
}

// cdcCLI runs TiCDC's changefeed cli in container, tried again while it
// fails (TiCDC's owner moving, PD slow) within tidbRetryFor; ok says an
// answer that counts as done although the cli failed.
func cdcCLI(ctx context.Context, container string, ok func(out string) bool, args ...string) (string, error) {
	deadline := time.Now().Add(tidbRetryFor)
	for {
		out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container, "/cdc", "cli", "changefeed"},
			append(args, "--server=http://127.0.0.1:8300")...)...).CombinedOutput()
		if err == nil || ok != nil && ok(string(out)) {
			return string(out), nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return string(out), fmt.Errorf("cdc %s: %v: %s", strings.Join(args, " "), err, out)
		}
		time.Sleep(2 * time.Second)
	}
}
