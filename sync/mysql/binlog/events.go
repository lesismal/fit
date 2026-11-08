package binlog

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Rotate names the next binlog file and the position to read it from: at
// a file's end, and first in a replica's stream (an artificial one, with a
// LogPos of 0).
type Rotate struct {
	Position uint64
	NextFile string
}

// XID ends a transaction of a transactional engine.
type XID struct {
	XID uint64
}

// Query is a statement: BEGIN of a transaction of rows events, a DDL,
// COMMIT of a non-transactional engine's.
type Query struct {
	ThreadID  uint32
	ExecTime  uint32
	ErrorCode uint16
	// StatusVars are the statement's status variables (SQL mode, charset,
	// ...) as the server wrote them.
	StatusVars []byte
	// Schema is the statement's default database.
	Schema string
	SQL    string
}

// RowsQuery is the statement of the rows events after it: MySQL's
// Rows_query (binlog_rows_query_log_events) or MariaDB's Annotate_rows
// (binlog_annotate_row_events).
type RowsQuery struct {
	SQL string
}

// Heartbeat is what a server sends a replica while it has nothing to
// send: the file it is at and the position there.
type Heartbeat struct {
	LogFile  string
	Position uint64
}

// GTID begins a transaction of MySQL: its GTID (Anonymous_gtid when GTIDs
// are off, with SID and GNO zero), and the logical clock and timestamps of
// 5.7 and 8.0. Timestamps are microseconds since the epoch.
type GTID struct {
	Flags uint8
	SID   [16]byte
	GNO   int64
	// Tag is the GTID's tag, MySQL 8.3's tagged GTIDs.
	Tag                      string
	LastCommitted            int64
	SequenceNumber           int64
	ImmediateCommitTimestamp uint64
	OriginalCommitTimestamp  uint64
	// TransactionLength is the transaction's size in the binlog, this
	// event included.
	TransactionLength      uint64
	ImmediateServerVersion uint32
	OriginalServerVersion  uint32
	CommitGroupTicket      uint64
	// Anonymous is an Anonymous_gtid, of a server with GTIDs off.
	Anonymous bool
}

// FormatUUID is u as MySQL writes a server's UUID.
func FormatUUID(u [16]byte) string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:], u[10:])
	return string(b[:])
}

// String is the GTID as MySQL writes it: uuid:gno, or uuid:tag:gno.
func (g *GTID) String() string {
	if g.Tag != "" {
		return FormatUUID(g.SID) + ":" + g.Tag + ":" + strconv.FormatInt(g.GNO, 10)
	}
	return FormatUUID(g.SID) + ":" + strconv.FormatInt(g.GNO, 10)
}

func decodeGTID(t EventType, body []byte) (*GTID, error) {
	r := rd{b: body}
	g := &GTID{Anonymous: t == AnonymousGTIDEvent, Flags: r.u8()}
	copy(g.SID[:], r.take(16))
	g.GNO = int64(r.u64())
	if r.bad {
		return nil, malformed("gtid event")
	}
	// What follows came with later versions; each part is there when
	// bytes are left for it.
	if len(r.b) > 0 {
		if r.u8() != 2 { // LOGICAL_TIMESTAMP_TYPECODE
			return nil, malformed("gtid event: logical clock type")
		}
		g.LastCommitted = int64(r.u64())
		g.SequenceNumber = int64(r.u64())
	}
	if len(r.b) >= 7 {
		g.ImmediateCommitTimestamp = r.u56()
		g.OriginalCommitTimestamp = g.ImmediateCommitTimestamp
		if g.ImmediateCommitTimestamp&(1<<55) != 0 {
			g.ImmediateCommitTimestamp &^= 1 << 55
			g.OriginalCommitTimestamp = r.u56()
		}
	}
	if len(r.b) > 0 {
		g.TransactionLength, _ = r.lenEnc()
	}
	if len(r.b) >= 4 {
		g.ImmediateServerVersion = r.u32()
		g.OriginalServerVersion = g.ImmediateServerVersion
		if g.ImmediateServerVersion&(1<<31) != 0 {
			g.ImmediateServerVersion &^= 1 << 31
			g.OriginalServerVersion = r.u32()
		}
	}
	if len(r.b) >= 8 {
		g.CommitGroupTicket = r.u64()
	}
	if r.bad {
		return nil, malformed("gtid event")
	}
	return g, nil
}

// decodeTaggedGTID reads MySQL 8.3's Gtid_tagged_log_event, a message of
// MySQL's serialization (see serial.go).
func decodeTaggedGTID(body []byte) (*GTID, error) {
	m, err := readMessage(body)
	if err != nil {
		return nil, fmt.Errorf("%w: tagged gtid event", err)
	}
	g := &GTID{}
	ok := true
	for m.next() {
		switch m.id {
		case 0:
			g.Flags = uint8(m.uint())
		case 1:
			m.bytes(g.SID[:])
		case 2:
			g.GNO = m.int()
		case 3:
			g.Tag = string(m.str())
		case 4:
			g.LastCommitted = m.int()
		case 5:
			g.SequenceNumber = m.int()
		case 6:
			g.ImmediateCommitTimestamp = m.uint()
		case 7:
			g.OriginalCommitTimestamp = m.uint()
			ok = false
		case 8:
			g.TransactionLength = m.uint()
		case 9:
			g.ImmediateServerVersion = uint32(m.uint())
		case 10:
			g.OriginalServerVersion = uint32(m.uint())
		case 11:
			g.CommitGroupTicket = m.uint()
		default:
			m.skip()
		}
	}
	if m.err != nil {
		return nil, fmt.Errorf("%w: tagged gtid event", m.err)
	}
	if ok {
		g.OriginalCommitTimestamp = g.ImmediateCommitTimestamp
	}
	if g.OriginalServerVersion == 0 {
		g.OriginalServerVersion = g.ImmediateServerVersion
	}
	return g, nil
}

func decodeHeartbeatV2(body []byte) (*Heartbeat, error) {
	m, err := readMessage(body)
	if err != nil {
		return nil, fmt.Errorf("%w: heartbeat v2 event", err)
	}
	h := &Heartbeat{}
	for m.next() {
		switch m.id {
		case 0:
			h.LogFile = string(m.str())
		case 1:
			h.Position = m.uint()
		default:
			m.skip()
		}
	}
	if m.err != nil {
		return nil, fmt.Errorf("%w: heartbeat v2 event", m.err)
	}
	return h, nil
}

// GTIDInterval is GNOs Start to End, End excluded.
type GTIDInterval struct {
	Start, End int64
}

// GTIDSetEntry is the GTIDs of one source (and tag) in a GTID set.
type GTIDSetEntry struct {
	SID       [16]byte
	Tag       string
	Intervals []GTIDInterval
}

// PreviousGTIDs is the GTID set of the files before this one, the second
// event of a file of a server with GTIDs.
type PreviousGTIDs struct {
	Set []GTIDSetEntry
}

// String is the set as MySQL writes it: uuid:1-5:7,uuid:tag:1-3.
func (p *PreviousGTIDs) String() string {
	var b strings.Builder
	for i, e := range p.Set {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(FormatUUID(e.SID))
		if e.Tag != "" {
			b.WriteString(":" + e.Tag)
		}
		for _, iv := range e.Intervals {
			b.WriteString(":" + strconv.FormatInt(iv.Start, 10))
			if iv.End-1 != iv.Start {
				b.WriteString("-" + strconv.FormatInt(iv.End-1, 10))
			}
		}
	}
	return b.String()
}

func decodePreviousGTIDs(body []byte) (*PreviousGTIDs, error) {
	r := rd{b: body}
	head := r.u64()
	// MySQL 8.3 marks a set with tags by its format, 1, in the first and
	// the last byte; the count is then in the six bytes between, and each
	// source's tag follows its UUID, its length a varlen (serial.go).
	tagged := head&0xff == 1 && head>>56 == 1
	n := head
	if tagged {
		n = head >> 8 & (1<<48 - 1)
	}
	// Each entry takes at least 24 bytes.
	if r.bad || n > uint64(len(r.b))/24 {
		return nil, malformed("previous gtids event")
	}
	p := &PreviousGTIDs{Set: make([]GTIDSetEntry, n)}
	for i := range p.Set {
		e := &p.Set[i]
		copy(e.SID[:], r.take(16))
		if tagged {
			tl, k := readVarlen(r.b)
			r.take(k)
			if k == 0 || tl > 32 {
				return nil, malformed("previous gtids event's tag")
			}
			e.Tag = string(r.take(int(tl)))
		}
		ni := r.u64()
		if r.bad || ni > uint64(len(r.b))/16 {
			return nil, malformed("previous gtids event")
		}
		e.Intervals = make([]GTIDInterval, ni)
		for j := range e.Intervals {
			e.Intervals[j] = GTIDInterval{Start: int64(r.u64()), End: int64(r.u64())}
		}
	}
	if r.bad {
		return nil, malformed("previous gtids event")
	}
	return p, nil
}

// MariaDB's GTID flags.
const (
	MariaFlagStandalone    = 1
	MariaFlagGroupCommitID = 2
	MariaFlagTransactional = 4
	MariaFlagAllowParallel = 8
	MariaFlagWaited        = 16
	MariaFlagDDL           = 32
	MariaFlagPreparedXA    = 64
	MariaFlagCompletedXA   = 128
)

// MariaGTID begins a transaction (or a standalone statement, a DDL) of
// MariaDB: domain-server-sequence.
type MariaGTID struct {
	Domain   uint32
	ServerID uint32
	Sequence uint64
	Flags    uint8
	CommitID uint64
	// XID is an XA transaction's ID: format, gtrid and bqual.
	XAFormat   uint32
	XAGtrid    []byte
	XABqual    []byte
	FlagsExtra uint8
}

// Standalone tells whether the event group is one statement without a
// COMMIT or XID after it, a DDL.
func (g *MariaGTID) Standalone() bool { return g.Flags&MariaFlagStandalone != 0 }

// String is the GTID as MariaDB writes it: domain-server-sequence.
func (g *MariaGTID) String() string {
	return fmt.Sprintf("%d-%d-%d", g.Domain, g.ServerID, g.Sequence)
}

func decodeMariaGTID(h Header, body []byte) (*MariaGTID, error) {
	r := rd{b: body}
	g := &MariaGTID{ServerID: h.ServerID, Sequence: r.u64(), Domain: r.u32(), Flags: r.u8()}
	if g.Flags&MariaFlagGroupCommitID != 0 {
		g.CommitID = r.u64()
	}
	if g.Flags&(MariaFlagPreparedXA|MariaFlagCompletedXA) != 0 {
		g.XAFormat = r.u32()
		gl, bl := int(r.u8()), int(r.u8())
		g.XAGtrid = append([]byte(nil), r.take(gl)...)
		g.XABqual = append([]byte(nil), r.take(bl)...)
	}
	if r.bad {
		return nil, malformed("mariadb gtid event")
	}
	if len(r.b) > 0 {
		g.FlagsExtra = r.u8()
	}
	return g, nil
}

// MariaGTIDList is the GTID state at the start of a MariaDB binlog file:
// the last GTID of each domain and server.
type MariaGTIDList struct {
	Flags uint8
	GTIDs []MariaGTID
}

func decodeMariaGTIDList(body []byte) (*MariaGTIDList, error) {
	r := rd{b: body}
	v := r.u32()
	n := v & (1<<28 - 1)
	if r.bad || uint64(n) > uint64(len(r.b))/16 {
		return nil, malformed("gtid list event")
	}
	l := &MariaGTIDList{Flags: uint8(v >> 28), GTIDs: make([]MariaGTID, n)}
	for i := range l.GTIDs {
		l.GTIDs[i] = MariaGTID{Domain: r.u32(), ServerID: r.u32(), Sequence: r.u64()}
	}
	return l, nil
}

// BinlogCheckpoint is MariaDB's oldest binlog file still needed for crash
// recovery.
type BinlogCheckpoint struct {
	File string
}

func (d *Decoder) decodeQuery(t EventType, body []byte) (*Query, error) {
	r := rd{b: body}
	q := &Query{ThreadID: r.u32(), ExecTime: r.u32()}
	dbLen := int(r.u8())
	q.ErrorCode = r.u16()
	vl := 0
	if ph := d.fd.postHeader(QueryEvent, 13); ph >= 13 {
		vl = int(r.u16())
		r.take(ph - 13)
	}
	q.StatusVars = append([]byte(nil), r.take(vl)...)
	q.Schema = string(r.take(dbLen))
	r.take(1) // NUL
	if r.bad {
		return nil, malformed("query event")
	}
	sql := r.rest()
	if t == MariaQueryCompressedEvent {
		var err error
		if sql, err = d.uncompress(sql); err != nil {
			return nil, err
		}
	}
	q.SQL = string(sql)
	return q, nil
}

// uncompress reads MariaDB's compressed binlog data: a byte 0x80 | n, the
// length uncompressed in n big-endian bytes, then zlib's stream.
func (d *Decoder) uncompress(b []byte) ([]byte, error) {
	if len(b) < 1 || b[0]&0xe0 != 0x80 || int(b[0]&7) > 4 || len(b) < 1+int(b[0]&7) {
		return nil, malformed("compressed event's header")
	}
	nl := int(b[0] & 7)
	var n int
	for _, c := range b[1 : 1+nl] {
		n = n<<8 | int(c)
	}
	if n > d.maxEvent() {
		return nil, fmt.Errorf("%w: a compressed event of %d bytes uncompressed, past MaxEventSize %d", ErrTooLarge, n, d.maxEvent())
	}
	zr, err := zlib.NewReader(bytes.NewReader(b[1+nl:]))
	if err != nil {
		return nil, malformed("compressed event: %v", err)
	}
	// What is read grows with the stream, not with the length it claims.
	out, err := io.ReadAll(io.LimitReader(zr, int64(n)+1))
	if err != nil {
		return nil, malformed("compressed event: %v", err)
	}
	if len(out) != n {
		return nil, malformed("compressed event of %d bytes says %d", len(out), n)
	}
	return out, nil
}

// unzstd uncompresses a transaction payload of n bytes (0 when the header
// does not say): at most MaxEventSize, read as the stream gives it, not
// as its header claims.
func (d *Decoder) unzstd(b []byte, n uint64) ([]byte, error) {
	if n > uint64(d.maxEvent()) {
		return nil, fmt.Errorf("%w: a transaction payload of %d bytes uncompressed, past MaxEventSize %d", ErrTooLarge, n, d.maxEvent())
	}
	zr, err := zstd.NewReader(bytes.NewReader(b), zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(uint64(d.maxEvent())), zstd.WithDecoderMaxWindow(uint64(d.maxEvent())))
	if err != nil {
		return nil, malformed("transaction payload: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, int64(d.maxEvent())+1))
	if errors.Is(err, zstd.ErrDecoderSizeExceeded) || err == nil && len(out) > d.maxEvent() {
		return nil, fmt.Errorf("%w: a transaction payload past MaxEventSize %d uncompressed", ErrTooLarge, d.maxEvent())
	}
	if err != nil {
		return nil, malformed("transaction payload: %v", err)
	}
	if n != 0 && uint64(len(out)) != n {
		return nil, malformed("transaction payload of %d bytes uncompressed says %d", len(out), n)
	}
	return out, nil
}

// Transaction compression types.
const (
	CompressionZSTD = 0
	CompressionNone = 255
)

// Payload is MySQL's Transaction_payload (binlog_transaction_compression):
// a transaction's events in one, compressed with ZSTD or not; Events are
// those events, decoded. The payload uncompressed is at most MaxEventSize.
type Payload struct {
	Compression      uint64
	UncompressedSize uint64
	Events           []*Event
}

func (d *Decoder) decodePayload(body []byte) (*Payload, error) {
	r := rd{b: body}
	p := &Payload{}
	size := uint64(len(body))
	for {
		f, _ := r.lenEnc()
		if r.bad {
			return nil, malformed("transaction payload event")
		}
		if f == 0 { // the end of the header
			break
		}
		l, _ := r.lenEnc()
		fr := rd{b: r.take(int(min(l, uint64(len(r.b)+1))))}
		v, _ := fr.lenEnc()
		switch f {
		case 1:
			size = v
		case 2:
			p.Compression = v
		case 3:
			p.UncompressedSize = v
		}
		if r.bad || fr.bad {
			return nil, malformed("transaction payload event")
		}
	}
	data := r.rest()
	if size != uint64(len(data)) {
		return nil, malformed("transaction payload of %d bytes says %d", len(data), size)
	}
	switch p.Compression {
	case CompressionNone:
	case CompressionZSTD:
		var err error
		if data, err = d.unzstd(data, p.UncompressedSize); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: a transaction payload compressed with type %d", ErrUnsupported, p.Compression)
	}
	// The events inside carry no checksum of their own.
	sum := d.checksum
	d.checksum = false
	defer func() { d.checksum = sum }()
	for len(data) > 0 {
		h, err := ParseHeader(data)
		if err != nil {
			return nil, err
		}
		if h.Type == TransactionPayload || h.Type == FormatDescriptionEvent || int(h.Size) > len(data) {
			return nil, malformed("transaction payload's events")
		}
		ev, err := d.Decode(data[:h.Size])
		if err != nil {
			return nil, err
		}
		p.Events = append(p.Events, ev)
		data = data[h.Size:]
	}
	return p, nil
}

// rd reads the fields of an event; a read past its end marks it bad and
// returns zeros.
type rd struct {
	b   []byte
	bad bool
}

func (r *rd) take(n int) []byte {
	if n < 0 || n > len(r.b) {
		r.bad = true
		r.b = nil
		return nil
	}
	v := r.b[:n:n]
	r.b = r.b[n:]
	return v
}

func (r *rd) u8() byte {
	if b := r.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (r *rd) u16() uint16 {
	if b := r.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (r *rd) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (r *rd) u56() uint64 {
	if b := r.take(7); b != nil {
		var x [8]byte
		copy(x[:], b)
		return binary.LittleEndian.Uint64(x[:])
	}
	return 0
}

func (r *rd) u64() uint64 {
	if b := r.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

// uN reads an n-byte little-endian unsigned integer, n up to 8.
func (r *rd) uN(n int) uint64 {
	var v uint64
	for i, c := range r.take(n) {
		v |= uint64(c) << (8 * i)
	}
	return v
}

// lenEnc reads a length-encoded integer; null is its NULL marker, 0xfb.
func (r *rd) lenEnc() (n uint64, null bool) {
	switch c := r.u8(); c {
	case 0xfb:
		return 0, true
	case 0xfc:
		return uint64(r.u16()), false
	case 0xfd:
		return r.uN(3), false
	case 0xfe:
		return r.u64(), false
	case 0xff:
		r.bad = true
		return 0, false
	default:
		return uint64(c), false
	}
}

// lenEncBytes reads a length-encoded string.
func (r *rd) lenEncBytes() []byte {
	n, _ := r.lenEnc()
	if n > uint64(len(r.b)) {
		r.bad = true
		r.b = nil
		return nil
	}
	return r.take(int(n))
}

func (r *rd) rest() []byte {
	v := r.b
	r.b = nil
	return v
}
