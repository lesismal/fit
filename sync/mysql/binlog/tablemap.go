package binlog

import "fmt"

// Column types, as the binlog has them.
const (
	TypeDecimal    = 0x00
	TypeTiny       = 0x01
	TypeShort      = 0x02
	TypeLong       = 0x03
	TypeFloat      = 0x04
	TypeDouble     = 0x05
	TypeNull       = 0x06
	TypeTimestamp  = 0x07
	TypeLongLong   = 0x08
	TypeInt24      = 0x09
	TypeDate       = 0x0a
	TypeTime       = 0x0b
	TypeDateTime   = 0x0c
	TypeYear       = 0x0d
	TypeNewDate    = 0x0e
	TypeVarChar    = 0x0f
	TypeBit        = 0x10
	TypeTimestamp2 = 0x11
	TypeDateTime2  = 0x12
	TypeTime2      = 0x13
	TypeVector     = 0xf2
	TypeJSON       = 0xf5
	TypeNewDecimal = 0xf6
	TypeEnum       = 0xf7
	TypeSet        = 0xf8
	TypeTinyBlob   = 0xf9
	TypeMediumBlob = 0xfa
	TypeLongBlob   = 0xfb
	TypeBlob       = 0xfc
	TypeVarString  = 0xfd
	TypeString     = 0xfe
	TypeGeometry   = 0xff
)

// CharsetBinary is the collation ID of binary strings.
const CharsetBinary = 63

// Column is a column of a table map: its type and metadata, and what the
// optional metadata told of it (binlog_row_metadata MINIMAL or FULL, MySQL
// 8.0 and MariaDB 10.5 on). A source that knows more (from
// information_schema) may fill in Name, Unsigned, Charset, EnumValues and
// SetValues before the rows events of the table map are decoded; the
// values decoded follow them:
//
//   - the integer types are fitsync.Int, or fitsync.Uint when Unsigned;
//     YEAR is an Int;
//   - FLOAT and DOUBLE are Floats (a FLOAT as its shortest decimal);
//   - DECIMAL is a Decimal, its text;
//   - DATE is a Date, DATETIME a Time in the decoder's Location, TIMESTAMP
//     a Time in UTC, each with its fraction; a zero date, or one with zero
//     parts, which neither can hold, is a String of how MySQL writes it;
//   - TIME is a String, [-]HH:MM:SS with its fraction's digits;
//   - character strings (CHAR, VARCHAR, TEXT) are Strings of their bytes,
//     in the column's character set; binary ones (Charset is
//     CharsetBinary: BINARY, VARBINARY, BLOB) are Bytes; with the charset
//     unknown (0), CHAR and VARCHAR are Strings, BLOB and TEXT Bytes;
//   - BIT is a Uint;
//   - ENUM and SET are Strings of their names (a SET's joined by commas)
//     when EnumValues and SetValues are known, else a Uint of the index or
//     the bits;
//   - JSON (MySQL's binary form) is JSON's text; GEOMETRY is Bytes (the
//     SRID and the WKB); VECTOR is Bytes;
//   - NULL is Null.
type Column struct {
	// Type is the column's type; a CHAR, ENUM or SET is TypeString in the
	// table map, and its real type here.
	Type byte
	// Meta is the type's metadata (lengths, precision, fraction digits).
	Meta     uint16
	Nullable bool

	Name     string
	Unsigned bool
	// Charset is the column's collation ID, 0 when unknown; CharsetBinary
	// for a binary string.
	Charset    uint16
	EnumValues []string
	SetValues  []string
	// GeometryType is the type of a GEOMETRY column (0 for any).
	GeometryType uint64
	// Invisible is an invisible column (MySQL 8.0.23).
	Invisible bool
}

// TableMap ties a table ID to a table and its columns, for the rows events
// after it.
type TableMap struct {
	TableID uint64
	Flags   uint16
	Schema  string
	Table   string
	Columns []Column
	// PrimaryKey is the primary key's columns, by index, and
	// PrimaryKeyPrefix the length of each one's prefix (0 for the whole),
	// from binlog_row_metadata=FULL.
	PrimaryKey       []int
	PrimaryKeyPrefix []int
	// Metadata is the optional metadata the table map had, a bit for each
	// type (1 << MetaSignedness, ...).
	Metadata uint32
}

// Optional metadata types.
const (
	MetaSignedness               = 1
	MetaDefaultCharset           = 2
	MetaColumnCharset            = 3
	MetaColumnName               = 4
	MetaSetStrValue              = 5
	MetaEnumStrValue             = 6
	MetaGeometryType             = 7
	MetaSimplePrimaryKey         = 8
	MetaPrimaryKeyWithPrefix     = 9
	MetaEnumAndSetDefaultCharset = 10
	MetaEnumAndSetColumnCharset  = 11
	MetaColumnVisibility         = 12
)

func (d *Decoder) decodeTableMap(body []byte) (*TableMap, error) {
	r := rd{b: body}
	tm := &TableMap{}
	if d.fd.postHeader(TableMapEvent, 8) == 6 {
		tm.TableID = uint64(r.u32())
	} else {
		tm.TableID = r.uN(6)
	}
	tm.Flags = r.u16()
	tm.Schema = string(r.take(int(r.u8())))
	r.take(1)
	tm.Table = string(r.take(int(r.u8())))
	r.take(1)
	n, _ := r.lenEnc()
	if r.bad {
		return nil, malformed("table map event")
	}
	if n > MaxColumns || n > uint64(len(r.b)) {
		return nil, malformed("table map of %d columns", n)
	}
	types := r.take(int(n))
	meta := rd{b: r.lenEncBytes()}
	nulls := r.take((int(n) + 7) / 8)
	if r.bad {
		return nil, malformed("table map event")
	}
	tm.Columns = make([]Column, n)
	for i := range tm.Columns {
		c := &tm.Columns[i]
		c.Type = types[i]
		c.Nullable = nulls[i/8]&(1<<(i%8)) != 0
		switch c.Type {
		case TypeFloat, TypeDouble, TypeBlob, TypeTinyBlob, TypeMediumBlob, TypeLongBlob,
			TypeGeometry, TypeJSON, TypeVector, TypeTimestamp2, TypeDateTime2, TypeTime2:
			c.Meta = uint16(meta.u8())
		case TypeVarChar, TypeVarString, TypeBit:
			c.Meta = meta.u16()
		case TypeNewDecimal, TypeString, TypeEnum, TypeSet:
			hi := meta.u8()
			c.Meta = uint16(hi)<<8 | uint16(meta.u8())
		}
		if c.Type == TypeString || c.Type == TypeEnum || c.Type == TypeSet {
			c.Type, c.Meta = stringType(c.Type, c.Meta)
		}
	}
	if meta.bad {
		return nil, malformed("table map's column metadata")
	}
	if err := tm.optional(r.rest(), d.fd != nil && d.fd.MariaDB()); err != nil {
		return nil, err
	}
	if d.tables == nil || len(d.tables) >= d.maxTables() {
		d.tables = map[uint64]*TableMap{}
	}
	d.tables[tm.TableID] = tm
	return tm, nil
}

func (d *Decoder) maxTables() int {
	if d.MaxTables > 0 {
		return d.MaxTables
	}
	return DefaultMaxTables
}

// stringType reads a STRING column's metadata: its real type (STRING,
// ENUM or SET) in the first byte and its length in the second, with the
// length's two high bits in the first byte's bits 4 and 5, inverted, for
// a CHAR of more than 255 bytes. It returns the real type and the length.
func stringType(t byte, meta uint16) (byte, uint16) {
	if meta < 256 {
		return t, meta
	}
	hi := byte(meta >> 8)
	n := meta & 0xff
	if hi&0x30 != 0x30 {
		n |= uint16((hi&0x30)^0x30) << 4
		hi |= 0x30
	}
	return hi, n
}

// The columns each piece of optional metadata covers.
func isNumeric(t byte) bool {
	switch t {
	case TypeTiny, TypeShort, TypeInt24, TypeLong, TypeLongLong, TypeFloat, TypeDouble, TypeNewDecimal, TypeDecimal, TypeYear:
		return true
	}
	return false
}

func isCharacter(t byte) bool {
	switch t {
	case TypeString, TypeVarString, TypeVarChar, TypeBlob, TypeTinyBlob, TypeMediumBlob, TypeLongBlob:
		return true
	}
	return false
}

// isMariaCharacter is MariaDB's character columns, which count GEOMETRY.
func isMariaCharacter(t byte) bool { return isCharacter(t) || t == TypeGeometry }

func isEnumSet(t byte) bool { return t == TypeEnum || t == TypeSet }

// optional reads the optional metadata, type, length and value each.
func (tm *TableMap) optional(b []byte, mariadb bool) error {
	r := rd{b: b}
	chars := isCharacter
	if mariadb {
		chars = isMariaCharacter
	}
	for len(r.b) > 0 {
		t := r.u8()
		v := rd{b: r.lenEncBytes()}
		if r.bad {
			return malformed("table map's optional metadata")
		}
		if t < 32 {
			tm.Metadata |= 1 << t
		}
		var err error
		switch t {
		case MetaSignedness:
			err = tm.bitsTo(&v, isNumeric, func(c *Column) { c.Unsigned = true })
		case MetaDefaultCharset:
			err = tm.defaultCharset(&v, chars)
		case MetaEnumAndSetDefaultCharset:
			err = tm.defaultCharset(&v, isEnumSet)
		case MetaColumnCharset:
			err = tm.eachColumn(&v, chars, func(c *Column) { c.Charset = uint16(v.packed()) })
		case MetaEnumAndSetColumnCharset:
			err = tm.eachColumn(&v, isEnumSet, func(c *Column) { c.Charset = uint16(v.packed()) })
		case MetaColumnName:
			err = tm.eachColumn(&v, nil, func(c *Column) { c.Name = string(v.lenEncBytes()) })
		case MetaSetStrValue:
			err = tm.eachColumn(&v, func(t byte) bool { return t == TypeSet }, func(c *Column) { c.SetValues = v.strings() })
		case MetaEnumStrValue:
			err = tm.eachColumn(&v, func(t byte) bool { return t == TypeEnum }, func(c *Column) { c.EnumValues = v.strings() })
		case MetaGeometryType:
			err = tm.eachColumn(&v, func(t byte) bool { return t == TypeGeometry }, func(c *Column) { c.GeometryType = v.packed() })
		case MetaSimplePrimaryKey, MetaPrimaryKeyWithPrefix:
			for len(v.b) > 0 && !v.bad {
				i := v.packed()
				p := uint64(0)
				if t == MetaPrimaryKeyWithPrefix {
					p = v.packed()
				}
				if i >= uint64(len(tm.Columns)) || len(tm.PrimaryKey) >= len(tm.Columns) {
					v.bad = true
					break
				}
				tm.PrimaryKey = append(tm.PrimaryKey, int(i))
				tm.PrimaryKeyPrefix = append(tm.PrimaryKeyPrefix, int(min(p, 1<<31)))
			}
		case MetaColumnVisibility:
			// A bit set is a visible column.
			for i := range tm.Columns {
				tm.Columns[i].Invisible = true
			}
			err = tm.bitsTo(&v, nil, func(c *Column) { c.Invisible = false })
		}
		if err == nil && v.bad {
			err = malformed("table map's optional metadata %d", t)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// packed reads a length-encoded integer, as the optional metadata's.
func (r *rd) packed() uint64 {
	n, _ := r.lenEnc()
	return n
}

// strings reads a count and as many length-encoded strings.
func (r *rd) strings() []string {
	n := r.packed()
	if n > uint64(len(r.b)) {
		r.bad = true
		return nil
	}
	s := make([]string, 0, n)
	for range n {
		s = append(s, string(r.lenEncBytes()))
	}
	return s
}

// bitsTo reads a bitmap, the highest bit first, of the columns of
// which, and calls set for each column whose bit is set.
func (tm *TableMap) bitsTo(v *rd, which func(byte) bool, set func(*Column)) error {
	k := 0
	for i := range tm.Columns {
		c := &tm.Columns[i]
		if which != nil && !which(c.Type) {
			continue
		}
		if k/8 >= len(v.b) {
			return malformed("table map's optional metadata: a bitmap too short")
		}
		if v.b[k/8]&(0x80>>(k%8)) != 0 {
			set(c)
		}
		k++
	}
	v.b = nil
	return nil
}

// eachColumn calls read for each column of which, in order.
func (tm *TableMap) eachColumn(v *rd, which func(byte) bool, read func(*Column)) error {
	for i := range tm.Columns {
		c := &tm.Columns[i]
		if which != nil && !which(c.Type) {
			continue
		}
		read(c)
		if v.bad {
			return malformed("table map's optional metadata of column %d", i)
		}
	}
	return nil
}

// defaultCharset reads a default charset and the columns of which that
// have another: pairs of the column's index among them and its charset.
func (tm *TableMap) defaultCharset(v *rd, which func(byte) bool) error {
	def := uint16(v.packed())
	var cols []*Column
	for i := range tm.Columns {
		if which(tm.Columns[i].Type) {
			tm.Columns[i].Charset = def
			cols = append(cols, &tm.Columns[i])
		}
	}
	for len(v.b) > 0 && !v.bad {
		i := v.packed()
		cs := uint16(v.packed())
		if i >= uint64(len(cols)) {
			return fmt.Errorf("%w: table map's default charset of column %d of %d", ErrMalformed, i, len(cols))
		}
		cols[i].Charset = cs
	}
	return nil
}
