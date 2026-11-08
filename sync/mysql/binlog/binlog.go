// Package binlog decodes MySQL's and MariaDB's binary log, the events a
// replica reads (I174) and the files a server writes: the format
// description, rotate, GTIDs (MySQL's, tagged ones included, and
// MariaDB's), query, table map with its optional metadata, the rows events
// (v1 and v2, MySQL's partial JSON updates, MariaDB's compressed ones),
// XID, heartbeats, the previous GTIDs and MariaDB's GTID list, with each
// event's CRC32 checked. A row's columns come out as fitsync.Values,
// typed by their column (see Column).
//
// A Decoder keeps what the stream told it so far: the format description
// (header and post-header lengths, the checksum) and the table maps by ID,
// which the rows events after them refer to. It is for one goroutine at a
// time, and keeps nothing of the bytes it is given: an event's strings and
// values are copies.
//
// Decoding is bounded: an event is at most MaxEventSize, a table at most
// MaxColumns columns, the table maps kept at most MaxTables, a JSON
// document at most 100 levels deep, and the values decoded from one rows
// event at most MaxRowsSize (or twice the event's size, if more); past a
// bound, Decode returns an error rather than allocate.
package binlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"time"
)

// EventType is an event's type, the header's fifth byte.
type EventType uint8

// The event types, MySQL's and MariaDB's (from 160).
const (
	UnknownEvent           EventType = 0
	StartEventV3           EventType = 1
	QueryEvent             EventType = 2
	StopEvent              EventType = 3
	RotateEvent            EventType = 4
	IntvarEvent            EventType = 5
	RandEvent              EventType = 13
	UserVarEvent           EventType = 14
	FormatDescriptionEvent EventType = 15
	XIDEvent               EventType = 16
	TableMapEvent          EventType = 19
	WriteRowsEventV1       EventType = 23
	UpdateRowsEventV1      EventType = 24
	DeleteRowsEventV1      EventType = 25
	IncidentEvent          EventType = 26
	HeartbeatEvent         EventType = 27
	IgnorableEvent         EventType = 28
	RowsQueryEvent         EventType = 29
	WriteRowsEventV2       EventType = 30
	UpdateRowsEventV2      EventType = 31
	DeleteRowsEventV2      EventType = 32
	GTIDEvent              EventType = 33
	AnonymousGTIDEvent     EventType = 34
	PreviousGTIDsEvent     EventType = 35
	TransactionContext     EventType = 36
	ViewChangeEvent        EventType = 37
	XAPrepareEvent         EventType = 38
	PartialUpdateRowsEvent EventType = 39
	TransactionPayload     EventType = 40
	HeartbeatEventV2       EventType = 41
	GTIDTaggedEvent        EventType = 42

	MariaAnnotateRowsEvent      EventType = 160
	MariaBinlogCheckpointEvent  EventType = 161
	MariaGTIDEvent              EventType = 162
	MariaGTIDListEvent          EventType = 163
	MariaStartEncryptionEvent   EventType = 164
	MariaQueryCompressedEvent   EventType = 165
	MariaWriteRowsCompressedV1  EventType = 166
	MariaUpdateRowsCompressedV1 EventType = 167
	MariaDeleteRowsCompressedV1 EventType = 168
	MariaWriteRowsCompressedV2  EventType = 169
	MariaUpdateRowsCompressedV2 EventType = 170
	MariaDeleteRowsCompressedV2 EventType = 171
)

var typeNames = map[EventType]string{
	UnknownEvent: "Unknown", StartEventV3: "Start_v3", QueryEvent: "Query", StopEvent: "Stop",
	RotateEvent: "Rotate", IntvarEvent: "Intvar", RandEvent: "Rand", UserVarEvent: "User_var",
	FormatDescriptionEvent: "Format_desc", XIDEvent: "Xid", TableMapEvent: "Table_map",
	WriteRowsEventV1: "Write_rows_v1", UpdateRowsEventV1: "Update_rows_v1", DeleteRowsEventV1: "Delete_rows_v1",
	IncidentEvent: "Incident", HeartbeatEvent: "Heartbeat", IgnorableEvent: "Ignorable",
	RowsQueryEvent: "Rows_query", WriteRowsEventV2: "Write_rows", UpdateRowsEventV2: "Update_rows",
	DeleteRowsEventV2: "Delete_rows", GTIDEvent: "Gtid", AnonymousGTIDEvent: "Anonymous_Gtid",
	PreviousGTIDsEvent: "Previous_gtids", TransactionContext: "Transaction_context",
	ViewChangeEvent: "View_change", XAPrepareEvent: "XA_prepare", PartialUpdateRowsEvent: "Update_rows_partial",
	TransactionPayload: "Transaction_payload", HeartbeatEventV2: "Heartbeat_v2", GTIDTaggedEvent: "Gtid_tagged",
	MariaAnnotateRowsEvent: "Annotate_rows", MariaBinlogCheckpointEvent: "Binlog_checkpoint",
	MariaGTIDEvent: "Gtid (MariaDB)", MariaGTIDListEvent: "Gtid_list", MariaStartEncryptionEvent: "Start_encryption",
	MariaQueryCompressedEvent: "Query_compressed", MariaWriteRowsCompressedV1: "Write_rows_compressed_v1",
	MariaUpdateRowsCompressedV1: "Update_rows_compressed_v1", MariaDeleteRowsCompressedV1: "Delete_rows_compressed_v1",
	MariaWriteRowsCompressedV2: "Write_rows_compressed", MariaUpdateRowsCompressedV2: "Update_rows_compressed",
	MariaDeleteRowsCompressedV2: "Delete_rows_compressed",
}

func (t EventType) String() string {
	if s, ok := typeNames[t]; ok {
		return s
	}
	return "type " + strconv.Itoa(int(t))
}

// HeaderSize is the size of a version 4 event's common header.
const HeaderSize = 19

// The bounds' defaults.
const (
	DefaultMaxEventSize = 64 << 20
	DefaultMaxRowsSize  = 64 << 20
	DefaultMaxTables    = 4096
	// MaxColumns is the most columns a table has, MySQL's limit.
	MaxColumns = 4096
	// maxJSONDepth is MySQL's limit of a JSON document's depth.
	maxJSONDepth = 100
)

var (
	// ErrMalformed is an event that does not parse.
	ErrMalformed = errors.New("binlog: malformed event")
	// ErrChecksum is an event whose CRC32 does not match.
	ErrChecksum = errors.New("binlog: checksum mismatch")
	// ErrTooLarge is an event, or what it decodes to, past a bound.
	ErrTooLarge = errors.New("binlog: past a bound")
	// ErrUnsupported is an event or a value this package does not decode.
	ErrUnsupported = errors.New("binlog: not supported")
	// ErrNoTableMap is a rows event whose table map was not seen.
	ErrNoTableMap = errors.New("binlog: rows event without its table map")
)

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrMalformed}, args...)...)
}

// Header is an event's common header.
type Header struct {
	// Timestamp is when the statement began, in seconds since the epoch.
	Timestamp uint32
	Type      EventType
	ServerID  uint32
	// Size is the event's size, header and checksum included.
	Size uint32
	// LogPos is the position of the next event in the binlog file; 0 in
	// an event that is not in a file (an artificial one).
	LogPos uint32
	Flags  uint16
}

// Event is a decoded event: its header and its body, one of the types of
// this package, *Unknown for an event it does not decode further.
type Event struct {
	Header
	Body any
}

// Unknown is the body of an event this package does not decode: Intvar,
// Rand, User_var, Stop, Incident, Ignorable, XA_prepare, a view change,
// Start_encryption, an old rows event and any type it does not know.
type Unknown struct {
	Data []byte
}

// Checksum algorithms of a format description.
const (
	ChecksumOff       = 0
	ChecksumCRC32     = 1
	ChecksumUndefined = 255
)

// FormatDescription is the first event of a binlog file and of a
// replica's stream; it tells the server, the headers' lengths and whether
// events carry a CRC32.
type FormatDescription struct {
	BinlogVersion   uint16
	ServerVersion   string
	CreateTimestamp uint32
	HeaderLength    uint8
	// PostHeaderLengths is the length of each event type's post-header,
	// by type - 1.
	PostHeaderLengths []byte
	// Checksum is the algorithm of every event after it, the description
	// itself included: ChecksumOff, ChecksumCRC32, or ChecksumUndefined
	// for a server older than checksums.
	Checksum uint8
}

// MariaDB tells whether the server is MariaDB.
func (f *FormatDescription) MariaDB() bool {
	return strings.Contains(strings.ToLower(f.ServerVersion), "mariadb")
}

// postHeader is the post-header length of type t, def when the
// description has none for it.
func (f *FormatDescription) postHeader(t EventType, def int) int {
	if f != nil && t > 0 && int(t) <= len(f.PostHeaderLengths) {
		return int(f.PostHeaderLengths[t-1])
	}
	return def
}

// Decoder decodes the events of one stream or file, in order.
type Decoder struct {
	// MaxEventSize bounds an event; zero is DefaultMaxEventSize.
	MaxEventSize int
	// MaxRowsSize bounds the values of one rows event, reckoned as
	// fitsync reckons a record (32 bytes a value, and its text); the bound
	// is twice the event's size when that is more. Zero is
	// DefaultMaxRowsSize.
	MaxRowsSize int
	// MaxTables bounds the table maps kept; past it, they are all
	// forgotten, as each transaction carries the maps of its rows. Zero is
	// DefaultMaxTables.
	MaxTables int
	// Location is the zone of DATETIME values, which have none; nil is
	// UTC. TIMESTAMP values are instants, in UTC.
	Location *time.Location
	// OnTableMap, when set, is called with each table map decoded, before
	// any rows event after it: the place to complete its columns (see
	// Column), also for the table maps inside a transaction payload, whose
	// rows are decoded in the same Decode.
	OnTableMap func(*TableMap)

	fd       *FormatDescription
	checksum bool
	tables   map[uint64]*TableMap
}

// NewDecoder returns a Decoder with the default bounds.
func NewDecoder() *Decoder { return &Decoder{} }

// SetChecksum tells the decoder whether the events before the stream's
// format description carry a CRC32: a replica's stream starts with an
// artificial rotate, which does when the replica asked for checksums
// (@source_binlog_checksum). The format description sets it after.
func (d *Decoder) SetChecksum(on bool) { d.checksum = on }

// FormatDescription is the last format description decoded, nil before
// one.
func (d *Decoder) FormatDescription() *FormatDescription { return d.fd }

// Table is the table map of id, nil if none was decoded (or it was
// forgotten).
func (d *Decoder) Table(id uint64) *TableMap { return d.tables[id] }

func (d *Decoder) maxEvent() int {
	if d.MaxEventSize > 0 {
		return d.MaxEventSize
	}
	return DefaultMaxEventSize
}

func (d *Decoder) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.UTC
}

// ParseHeader reads an event's common header from b's first HeaderSize
// bytes.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, malformed("a header of %d bytes", len(b))
	}
	return Header{
		Timestamp: binary.LittleEndian.Uint32(b),
		Type:      EventType(b[4]),
		ServerID:  binary.LittleEndian.Uint32(b[5:]),
		Size:      binary.LittleEndian.Uint32(b[9:]),
		LogPos:    binary.LittleEndian.Uint32(b[13:]),
		Flags:     binary.LittleEndian.Uint16(b[17:]),
	}, nil
}

// Decode decodes one whole event, b, its header and its checksum
// included. b is not kept.
func (d *Decoder) Decode(b []byte) (*Event, error) {
	h, err := ParseHeader(b)
	if err != nil {
		return nil, err
	}
	if int(h.Size) != len(b) {
		return nil, malformed("%s event of %d bytes says %d", h.Type, len(b), h.Size)
	}
	if len(b) > d.maxEvent() {
		return nil, fmt.Errorf("%w: a %s event of %d bytes, past MaxEventSize %d", ErrTooLarge, h.Type, len(b), d.maxEvent())
	}
	hl := HeaderSize
	if d.fd != nil && int(d.fd.HeaderLength) > hl {
		hl = int(d.fd.HeaderLength)
	}
	if h.Type == FormatDescriptionEvent {
		hl = HeaderSize
	}
	if len(b) < hl {
		return nil, malformed("%s event of %d bytes, shorter than its header", h.Type, len(b))
	}
	ev := &Event{Header: h}
	if h.Type == FormatDescriptionEvent {
		fd, err := parseFormatDescription(b)
		if err != nil {
			return nil, err
		}
		d.fd = fd
		d.checksum = fd.Checksum == ChecksumCRC32
		ev.Body = fd
		return ev, nil
	}
	body := b[hl:]
	if d.checksum {
		if body, err = checkCRC(b, hl); err != nil {
			return nil, err
		}
	}
	ev.Body, err = d.decodeBody(h, body)
	if err != nil {
		return nil, err
	}
	return ev, nil
}

// checkCRC checks the CRC32 at the end of event b and returns its body,
// after the header of hl bytes, without it.
func checkCRC(b []byte, hl int) ([]byte, error) {
	if len(b) < hl+4 {
		return nil, malformed("an event of %d bytes, too short for its checksum", len(b))
	}
	n := len(b) - 4
	if crc32.ChecksumIEEE(b[:n]) != binary.LittleEndian.Uint32(b[n:]) {
		return nil, fmt.Errorf("%w: %s event", ErrChecksum, EventType(b[4]))
	}
	return b[hl:n], nil
}

func parseFormatDescription(b []byte) (*FormatDescription, error) {
	r := rd{b: b[HeaderSize:]}
	fd := &FormatDescription{BinlogVersion: r.u16()}
	ver := r.take(50)
	fd.CreateTimestamp = r.u32()
	fd.HeaderLength = r.u8()
	if r.bad || fd.BinlogVersion != 4 || fd.HeaderLength < HeaderSize {
		return nil, malformed("format description (binlog version %d, header length %d)", fd.BinlogVersion, fd.HeaderLength)
	}
	if i := strings.IndexByte(string(ver), 0); i >= 0 {
		ver = ver[:i]
	}
	fd.ServerVersion = string(ver)
	rest := r.rest()
	fd.Checksum = ChecksumUndefined
	if hasChecksumAlg(fd.ServerVersion) {
		// The algorithm's byte and the CRC's four bytes end it, whether
		// checksums are on or not.
		if len(rest) < 5 {
			return nil, malformed("format description without its checksum")
		}
		fd.Checksum = rest[len(rest)-5]
		if fd.Checksum == ChecksumCRC32 {
			if _, err := checkCRC(b, HeaderSize); err != nil {
				return nil, err
			}
		}
		rest = rest[:len(rest)-5]
	}
	fd.PostHeaderLengths = append([]byte(nil), rest...)
	return fd, nil
}

// hasChecksumAlg tells whether a server of version v writes a checksum
// algorithm in its format description: MySQL from 5.6.1, every MariaDB
// this package reads (MariaDB's versions are 5.5 or 10 and later; 5.5's
// is a 5.3+ that has it too).
func hasChecksumAlg(v string) bool {
	var n [3]int
	s := v
	for i := range n {
		j := 0
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		n[i], _ = strconv.Atoi(s[:j])
		s = s[j:]
		if len(s) == 0 || s[0] != '.' {
			break
		}
		s = s[1:]
	}
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return n[0] > 5 || n[0] == 5 && n[1] >= 3
	}
	return n[0]*10000+n[1]*100+n[2] >= 50601
}

func (d *Decoder) decodeBody(h Header, body []byte) (any, error) {
	switch h.Type {
	case RotateEvent:
		r := rd{b: body}
		rot := &Rotate{Position: r.u64(), NextFile: string(r.rest())}
		if r.bad {
			return nil, malformed("rotate event")
		}
		return rot, nil
	case XIDEvent:
		r := rd{b: body}
		x := &XID{XID: r.u64()}
		if r.bad {
			return nil, malformed("xid event")
		}
		return x, nil
	case QueryEvent, MariaQueryCompressedEvent:
		return d.decodeQuery(h.Type, body)
	case TableMapEvent:
		return d.decodeTableMap(body)
	case WriteRowsEventV1, UpdateRowsEventV1, DeleteRowsEventV1,
		WriteRowsEventV2, UpdateRowsEventV2, DeleteRowsEventV2, PartialUpdateRowsEvent,
		MariaWriteRowsCompressedV1, MariaUpdateRowsCompressedV1, MariaDeleteRowsCompressedV1,
		MariaWriteRowsCompressedV2, MariaUpdateRowsCompressedV2, MariaDeleteRowsCompressedV2:
		return d.decodeRows(h.Type, body)
	case GTIDEvent, AnonymousGTIDEvent:
		return decodeGTID(h.Type, body)
	case GTIDTaggedEvent:
		return decodeTaggedGTID(body)
	case PreviousGTIDsEvent:
		return decodePreviousGTIDs(body)
	case HeartbeatEvent:
		return &Heartbeat{LogFile: string(body), Position: uint64(h.LogPos)}, nil
	case HeartbeatEventV2:
		return decodeHeartbeatV2(body)
	case RowsQueryEvent:
		// A length byte, which a long query overflows, then the query.
		if len(body) < 1 {
			return nil, malformed("rows query event")
		}
		return &RowsQuery{SQL: string(body[1:])}, nil
	case MariaAnnotateRowsEvent:
		return &RowsQuery{SQL: string(body)}, nil
	case MariaGTIDEvent:
		return decodeMariaGTID(h, body)
	case MariaGTIDListEvent:
		return decodeMariaGTIDList(body)
	case MariaBinlogCheckpointEvent:
		r := rd{b: body}
		n := r.u32()
		f := r.take(int(min(n, uint32(len(body)+1))))
		if r.bad {
			return nil, malformed("binlog checkpoint event")
		}
		return &BinlogCheckpoint{File: string(f)}, nil
	case TransactionPayload:
		return d.decodePayload(body)
	}
	return &Unknown{Data: append([]byte(nil), body...)}, nil
}
