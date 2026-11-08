package pgwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// LSN is a position in the write-ahead log.
type LSN uint64

func (l LSN) String() string { return fmt.Sprintf("%X/%X", uint32(l>>32), uint32(l)) }

// ParseLSN reads an LSN written as PostgreSQL writes it, "16/B374D848".
func ParseLSN(s string) (LSN, error) {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok {
		return 0, fmt.Errorf("postgres: a bad LSN %q", s)
	}
	h, err1 := strconv.ParseUint(hi, 16, 32)
	l, err2 := strconv.ParseUint(lo, 16, 32)
	if err1 != nil || err2 != nil {
		return 0, fmt.Errorf("postgres: a bad LSN %q", s)
	}
	return LSN(h<<32 | l), nil
}

// System is IDENTIFY_SYSTEM's answer.
type System struct {
	SystemID string
	Timeline int64
	XLogPos  LSN
	DBName   string
}

func (c *Conn) replicationCommand(ctx context.Context, cmd string) (Result, error) {
	if !c.cfg.Replication {
		return Result{}, errors.New("postgres: a replication command on a connection without replication")
	}
	res, err := c.SimpleQuery(ctx, cmd)
	if err != nil {
		return Result{}, err
	}
	if len(res) == 0 {
		return Result{}, nil
	}
	return res[0], nil
}

func col(r Result, name string) string {
	if len(r.Rows) == 0 {
		return ""
	}
	for i, f := range r.Fields {
		if f.Name == name && i < len(r.Rows[0]) {
			return string(r.Rows[0][i])
		}
	}
	return ""
}

// IdentifySystem runs IDENTIFY_SYSTEM.
func (c *Conn) IdentifySystem(ctx context.Context) (System, error) {
	r, err := c.replicationCommand(ctx, "IDENTIFY_SYSTEM")
	if err != nil {
		return System{}, err
	}
	s := System{SystemID: col(r, "systemid"), DBName: col(r, "dbname")}
	s.Timeline, _ = strconv.ParseInt(col(r, "timeline"), 10, 64)
	if s.XLogPos, err = ParseLSN(col(r, "xlogpos")); err != nil {
		return s, err
	}
	return s, nil
}

// Snapshot actions of CREATE_REPLICATION_SLOT.
const (
	SnapshotExport  = "export"  // export a snapshot, usable until the next command on the connection
	SnapshotUse     = "use"     // use the snapshot for the connection's open transaction
	SnapshotNothing = "nothing" // no snapshot
)

// SlotOptions are CREATE_REPLICATION_SLOT's.
type SlotOptions struct {
	Temporary bool
	// Plugin is the output plugin; default pgoutput.
	Plugin string
	// Snapshot is one of the Snapshot constants; default nothing.
	Snapshot string
}

// Slot is CREATE_REPLICATION_SLOT's answer.
type Slot struct {
	Name            string
	ConsistentPoint LSN
	// SnapshotName is the exported snapshot, for SET TRANSACTION SNAPSHOT
	// on another connection while this one stays idle.
	SnapshotName string
	Plugin       string
}

// CreateReplicationSlot creates a logical slot, with the new syntax on
// PostgreSQL 15 and later and the old one before.
func (c *Conn) CreateReplicationSlot(ctx context.Context, name string, o SlotOptions) (Slot, error) {
	if o.Plugin == "" {
		o.Plugin = "pgoutput"
	}
	if o.Snapshot == "" {
		o.Snapshot = SnapshotNothing
	}
	var b strings.Builder
	b.WriteString("CREATE_REPLICATION_SLOT ")
	b.WriteString(QuoteIdentifier(name))
	if o.Temporary {
		b.WriteString(" TEMPORARY")
	}
	b.WriteString(" LOGICAL ")
	b.WriteString(QuoteIdentifier(o.Plugin))
	if c.ServerVersion() >= 150000 {
		b.WriteString(" (SNAPSHOT " + QuoteLiteral(o.Snapshot) + ")")
	} else {
		switch o.Snapshot {
		case SnapshotExport:
			b.WriteString(" EXPORT_SNAPSHOT")
		case SnapshotUse:
			b.WriteString(" USE_SNAPSHOT")
		case SnapshotNothing:
			b.WriteString(" NOEXPORT_SNAPSHOT")
		default:
			return Slot{}, fmt.Errorf("postgres: unknown snapshot action %q", o.Snapshot)
		}
	}
	r, err := c.replicationCommand(ctx, b.String())
	if err != nil {
		return Slot{}, err
	}
	s := Slot{Name: col(r, "slot_name"), SnapshotName: col(r, "snapshot_name"), Plugin: col(r, "output_plugin")}
	if s.ConsistentPoint, err = ParseLSN(col(r, "consistent_point")); err != nil {
		return s, err
	}
	return s, nil
}

// DropReplicationSlot drops a slot; with wait, it waits for a slot in use
// by another connection to be released instead of failing.
func (c *Conn) DropReplicationSlot(ctx context.Context, name string, wait bool) error {
	cmd := "DROP_REPLICATION_SLOT " + QuoteIdentifier(name)
	if wait {
		cmd += " WAIT"
	}
	_, err := c.replicationCommand(ctx, cmd)
	return err
}

// StartReplication runs START_REPLICATION SLOT name LOGICAL lsn (options),
// with the plugin's options as name-value pairs ("proto_version", "2",
// "publication_names", "p1"); the values are quoted here. On success the
// connection is in the replication stream: ReceiveReplication reads it,
// SendStandbyStatus answers it, EndReplication leaves it.
func (c *Conn) StartReplication(ctx context.Context, slot string, start LSN, options ...string) error {
	if !c.cfg.Replication {
		return errors.New("postgres: START_REPLICATION on a connection without replication")
	}
	if len(options)%2 != 0 {
		return errors.New("postgres: replication options come in name-value pairs")
	}
	var b strings.Builder
	b.WriteString("START_REPLICATION SLOT " + QuoteIdentifier(slot) + " LOGICAL " + start.String())
	if len(options) > 0 {
		b.WriteString(" (")
		for i := 0; i < len(options); i += 2 {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(QuoteIdentifier(options[i]) + " " + QuoteLiteral(options[i+1]))
		}
		b.WriteString(")")
	}
	exit, err := c.enter(ctx)
	if err != nil {
		return err
	}
	i := c.begin('Q')
	c.wbuf = append(append(c.wbuf, b.String()...), 0)
	c.end(i)
	if err := c.flush(); err != nil {
		return exit(err)
	}
	for {
		t, body, err := c.recv()
		if err != nil {
			return exit(err)
		}
		switch t {
		case 'W':
			c.busy = true
			c.streaming.Store(true)
			return exit(nil)
		case 'E':
			return exit(c.drain(parseError(body)))
		default:
			return exit(c.unexpected(t, "START_REPLICATION"))
		}
	}
}

// XLogData is a piece of the stream: for pgoutput, one message of the
// plugin's protocol.
type XLogData struct {
	WALStart   LSN
	ServerEnd  LSN
	ServerTime time.Time
	// Data is valid until the next ReceiveReplication.
	Data []byte
}

// Keepalive is the server's primary keepalive message; ReplyRequested asks
// for a standby status update at once.
type Keepalive struct {
	ServerEnd      LSN
	ServerTime     time.Time
	ReplyRequested bool
}

// ErrReplicationEnded is returned by ReceiveReplication when the server ends
// the stream (CopyDone); EndReplication then leaves it.
var ErrReplicationEnded = errors.New("postgres: the server ended the replication stream")

// ReceiveReplication reads the stream's next message: an *XLogData or a
// *Keepalive, each valid until the next call. A context's end breaks the
// connection, as for any call.
func (c *Conn) ReceiveReplication(ctx context.Context) (any, error) {
	if !c.streaming.Load() {
		return nil, errors.New("postgres: not in a replication stream")
	}
	if err := c.Err(); err != nil {
		return nil, err
	}
	c.busy = false
	exit, err := c.enter(ctx)
	c.busy = true
	if err != nil {
		return nil, err
	}
	for {
		t, body, err := c.recv()
		if err != nil {
			return nil, exit(err)
		}
		switch t {
		case 'd':
			if len(body) == 0 {
				return nil, exit(c.protoErr("CopyData"))
			}
			switch body[0] {
			case 'w':
				if len(body) < 25 {
					return nil, exit(c.protoErr("XLogData"))
				}
				return &XLogData{
					WALStart:   LSN(binary.BigEndian.Uint64(body[1:])),
					ServerEnd:  LSN(binary.BigEndian.Uint64(body[9:])),
					ServerTime: pgTime(int64(binary.BigEndian.Uint64(body[17:]))),
					Data:       body[25:],
				}, exit(nil)
			case 'k':
				if len(body) < 18 {
					return nil, exit(c.protoErr("PrimaryKeepalive"))
				}
				return &Keepalive{
					ServerEnd:      LSN(binary.BigEndian.Uint64(body[1:])),
					ServerTime:     pgTime(int64(binary.BigEndian.Uint64(body[9:]))),
					ReplyRequested: body[17] != 0,
				}, exit(nil)
			}
			// Other CopyData kinds are skipped.
		case 'c':
			return nil, exit(ErrReplicationEnded)
		case 'E':
			c.streaming.Store(false)
			c.busy = false
			return nil, exit(c.drain(parseError(body)))
		default:
			return nil, exit(c.unexpected(t, "replication"))
		}
	}
}

func pgTime(us int64) time.Time { return time.UnixMicro(us + pgEpochUnixMicro).UTC() }

// StandbyStatus is a standby status update: the positions written, flushed
// and applied. A logical slot's confirmed_flush_lsn follows Flush.
type StandbyStatus struct {
	Write, Flush, Apply LSN
	ReplyRequested      bool
}

// SendStandbyStatus sends a standby status update. It may run on another
// goroutine than the one in ReceiveReplication.
func (c *Conn) SendStandbyStatus(ctx context.Context, s StandbyStatus) error {
	if !c.streaming.Load() {
		return errors.New("postgres: not in a replication stream")
	}
	b := make([]byte, 0, 40)
	b = append(b, 'd', 0, 0, 0, 0, 'r')
	b = binary.BigEndian.AppendUint64(b, uint64(s.Write))
	b = binary.BigEndian.AppendUint64(b, uint64(s.Flush))
	b = binary.BigEndian.AppendUint64(b, uint64(s.Apply))
	b = binary.BigEndian.AppendUint64(b, uint64(time.Now().UnixMicro()-pgEpochUnixMicro))
	if s.ReplyRequested {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	binary.BigEndian.PutUint32(b[1:], uint32(len(b)-1))
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		c.nc.SetWriteDeadline(dl)
		defer c.nc.SetWriteDeadline(time.Time{})
	}
	if _, err := c.nc.Write(b); err != nil {
		return fmt.Errorf("postgres: standby status: %w", err)
	}
	return nil
}

// EndReplication leaves the stream: it sends CopyDone and reads what the
// server sends until it is ready for commands again.
func (c *Conn) EndReplication(ctx context.Context) error {
	if !c.streaming.Load() {
		return nil
	}
	c.busy = false
	exit, err := c.enter(ctx)
	if err != nil {
		return err
	}
	c.streaming.Store(false)
	i := c.begin('c')
	c.end(i)
	if err := c.flush(); err != nil {
		return exit(err)
	}
	var qerr error
	for {
		t, body, err := c.recv()
		if err != nil {
			return exit(err)
		}
		switch t {
		case 'E':
			if qerr == nil {
				qerr = parseError(body)
			}
		case 'Z':
			if len(body) > 0 {
				c.txStatus = body[0]
			}
			return exit(qerr)
		}
	}
}
