package mysqlwire

import (
	"context"
	"fmt"
	"io"
)

// The replica's commands.
const (
	ComBinlogDump     = 0x12
	ComRegisterSlave  = 0x15
	ComBinlogDumpGTID = 0x1e
)

// Command sends a command of cmd and body answered by OK or ERR, as
// COM_REGISTER_SLAVE is.
func (c *Conn) Command(ctx context.Context, cmd byte, body []byte) error {
	if c.dumping {
		return ErrBusy
	}
	return c.simple(ctx, cmd, body)
}

// StartBinlog sends a command that starts the binlog's stream,
// COM_BINLOG_DUMP or COM_BINLOG_DUMP_GTID with body, whose events
// ReadEvent then reads. The connection is the stream's from then on: any
// other call is ErrBusy, and Close ends it.
func (c *Conn) StartBinlog(ctx context.Context, cmd byte, body []byte) error {
	if c.dumping {
		return ErrBusy
	}
	if err := c.begin(ctx); err != nil {
		return err
	}
	defer c.end()
	if err := c.writePacket(append(c.startCommand(cmd), body...)); err != nil {
		return c.fail(err)
	}
	c.dumping = true
	return nil
}

// ReadEvent reads the stream's next event, its header, body and checksum,
// valid until the next call; io.EOF when the server ends the stream (a
// dump with BINLOG_DUMP_NON_BLOCK at the binlog's end), an *Error when it
// refuses it (a position it no longer has). Each wait is bounded by the
// connection's Timeout, which must be longer than the heartbeat period the
// replica asked for.
func (c *Conn) ReadEvent(ctx context.Context) ([]byte, error) {
	if !c.dumping {
		return nil, fmt.Errorf("%w: no binlog stream started", ErrMalformed)
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	defer c.end()
	p, err := c.readPacket()
	if err != nil {
		return nil, c.fail(err)
	}
	switch {
	case len(p) > 0 && p[0] == 0x00:
		return p[1:], nil
	case len(p) > 0 && p[0] == 0xff:
		err := c.parseErr(p)
		c.fail(err)
		return nil, err
	case len(p) > 0 && p[0] == 0xfe && len(p) < 9:
		c.fail(io.EOF)
		return nil, io.EOF
	}
	return nil, c.fail(fmt.Errorf("%w: a binlog packet beginning 0x%x", ErrMalformed, first(p)))
}
