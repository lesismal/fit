package mssqlwire

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The TDS data types.
const (
	TypeNull           = 0x1f
	TypeInt1           = 0x30
	TypeBit            = 0x32
	TypeInt2           = 0x34
	TypeInt4           = 0x38
	TypeDateTime4      = 0x3a
	TypeFloat4         = 0x3b
	TypeMoney          = 0x3c
	TypeDateTime       = 0x3d
	TypeFloat8         = 0x3e
	TypeMoney4         = 0x7a
	TypeInt8           = 0x7f
	TypeGUID           = 0x24
	TypeIntN           = 0x26
	TypeDecimal        = 0x37
	TypeNumeric        = 0x3f
	TypeBitN           = 0x68
	TypeDecimalN       = 0x6a
	TypeNumericN       = 0x6c
	TypeFloatN         = 0x6d
	TypeMoneyN         = 0x6e
	TypeDateTimeN      = 0x6f
	TypeDateN          = 0x28
	TypeTimeN          = 0x29
	TypeDateTime2N     = 0x2a
	TypeDateTimeOffset = 0x2b
	TypeChar           = 0x2f
	TypeVarChar        = 0x27
	TypeBinary         = 0x2d
	TypeVarBinary      = 0x25
	TypeBigVarBinary   = 0xa5
	TypeBigVarChar     = 0xa7
	TypeBigBinary      = 0xad
	TypeBigChar        = 0xaf
	TypeNVarChar       = 0xe7
	TypeNChar          = 0xef
	TypeXML            = 0xf1
	TypeUDT            = 0xf0
	TypeText           = 0x23
	TypeImage          = 0x22
	TypeNText          = 0x63
	TypeVariant        = 0x62
)

// userTypeTimestamp is the user type of a rowversion (timestamp) column,
// sent as binary(8).
const userTypeTimestamp = 0x50

// Decimal is a decimal, numeric, money or smallmoney value, as its exact
// text: digits, a point and the scale's digits, a leading minus.
type Decimal string

// UUID is a uniqueidentifier, in the order of its text (RFC 4122), not
// the mixed byte order SQL Server sends.
type UUID [16]byte

func (u UUID) String() string {
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
	return strings.ToUpper(string(b[:]))
}

// ParseUUID reads a UUID's text, with or without braces and dashes.
func ParseUUID(s string) (UUID, error) {
	var u UUID
	t := strings.ReplaceAll(strings.Trim(s, "{}"), "-", "")
	if len(t) != 32 {
		return u, fmt.Errorf("mssql: %q is not a uniqueidentifier", s)
	}
	if _, err := hex.Decode(u[:], []byte(t)); err != nil {
		return u, fmt.Errorf("mssql: %q is not a uniqueidentifier", s)
	}
	return u, nil
}

// swapGUID turns a UUID into SQL Server's byte order, and back: its first
// three groups little-endian.
func swapGUID(b []byte) UUID {
	var u UUID
	copy(u[:], b)
	u[0], u[1], u[2], u[3] = u[3], u[2], u[1], u[0]
	u[4], u[5] = u[5], u[4]
	u[6], u[7] = u[7], u[6]
	return u
}

// Collation is a COLLATION as TDS sends it: the LCID and flags, then the
// sort ID.
type Collation [5]byte

// UTF8 reports whether it is a UTF-8 collation (SQL Server 2019 and
// later, _UTF8).
func (c Collation) UTF8() bool { return c[3]&0x04 != 0 }

// LCID is the collation's locale.
func (c Collation) LCID() uint32 { return binary.LittleEndian.Uint32(c[:4]) & 0xfffff }

// CodePage is the code page char and varchar values are in: 65001 for a
// UTF-8 collation, 1252 for the Latin collations, 0 for one the client
// does not decode (whose non-ASCII values come as their bytes).
func (c Collation) CodePage() int {
	if c.UTF8() {
		return 65001
	}
	if sort := c[4]; sort != 0 {
		if sort >= 51 && sort <= 54 || sort >= 183 && sort <= 186 || sort == 71 || sort == 72 {
			return 1252
		}
		return 0
	}
	switch c.LCID() & 0x3ff {
	case 0x03, 0x06, 0x07, 0x09, 0x0a, 0x0b, 0x0c, 0x0f, 0x10, 0x13, 0x14, 0x16, 0x1d, 0x21, 0x2d, 0x36, 0x38, 0x3e, 0x41, 0x56:
		return 1252
	}
	return 0
}

// cp1252 maps the bytes 0x80 to 0x9f of code page 1252; the rest are
// Latin-1's.
var cp1252 = [32]rune{
	0x20ac, 0x81, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021, 0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8d, 0x017d, 0x8f,
	0x90, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014, 0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0x9d, 0x017e, 0x0178,
}

// decodeChars decodes a char or varchar value in the collation's code
// page: a string, or its bytes when the client cannot decode it.
func decodeChars(c Collation, b []byte) any {
	ascii := true
	for _, x := range b {
		if x >= 0x80 {
			ascii = false
			break
		}
	}
	switch {
	case ascii, c.CodePage() == 65001:
		return string(b)
	case c.CodePage() == 1252:
		var sb strings.Builder
		for _, x := range b {
			if x >= 0x80 && x < 0xa0 {
				sb.WriteRune(cp1252[x-0x80])
			} else {
				sb.WriteRune(rune(x))
			}
		}
		return sb.String()
	}
	return append([]byte(nil), b...)
}

// encodeChars encodes s in the collation's code page.
func encodeChars(c Collation, s string) ([]byte, error) {
	cp := c.CodePage()
	if cp == 65001 {
		return []byte(s), nil
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			b = append(b, byte(r))
			continue
		case cp == 1252 && r >= 0xa0 && r <= 0xff:
			b = append(b, byte(r))
			continue
		case cp == 1252:
			found := false
			for i, x := range cp1252 {
				if x == r {
					b = append(b, byte(0x80+i))
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		return nil, fmt.Errorf("mssql: %q is not in the column's code page %d (use nvarchar)", r, cp)
	}
	return b, nil
}

// Column is a column of a result set, or the type of a parameter or of a
// bulk load's column.
type Column struct {
	Name     string
	UserType uint32
	Flags    uint16
	// Type is the TDS type, one of the Type constants.
	Type byte
	// Size is the type's length in bytes; -1 for the max types, xml and
	// CLR types (PLP streams).
	Size      int
	Precision uint8
	Scale     uint8
	Collation Collation
	// UDTName is a CLR type's name (geography, hierarchyid, ...).
	UDTName string
}

// Nullable reports whether the column may hold NULL.
func (c *Column) Nullable() bool { return c.Flags&0x01 != 0 }

// Identity reports whether the column is an identity column.
func (c *Column) Identity() bool { return c.Flags&0x10 != 0 }

// Computed reports whether the column is computed.
func (c *Column) Computed() bool { return c.Flags&0x20 != 0 }

// value length kinds.
const (
	kindFixed = iota
	kindByte
	kindUShort
	kindPLP
	kindText
	kindVariant
)

func (c *Column) kind() int {
	switch c.Type {
	case TypeNull, TypeInt1, TypeBit, TypeInt2, TypeInt4, TypeDateTime4, TypeFloat4, TypeMoney, TypeDateTime, TypeFloat8, TypeMoney4, TypeInt8:
		return kindFixed
	case TypeBigVarBinary, TypeBigVarChar, TypeBigBinary, TypeBigChar, TypeNVarChar, TypeNChar:
		if c.Size < 0 {
			return kindPLP
		}
		return kindUShort
	case TypeXML, TypeUDT:
		return kindPLP
	case TypeText, TypeImage, TypeNText:
		return kindText
	case TypeVariant:
		return kindVariant
	}
	return kindByte
}

var fixedSizes = map[byte]int{TypeNull: 0, TypeInt1: 1, TypeBit: 1, TypeInt2: 2, TypeInt4: 4, TypeDateTime4: 4, TypeFloat4: 4,
	TypeMoney: 8, TypeDateTime: 8, TypeFloat8: 8, TypeMoney4: 4, TypeInt8: 8}

// timeLen is the bytes a time of a scale takes.
func timeLen(scale uint8) int {
	switch {
	case scale <= 2:
		return 3
	case scale <= 4:
		return 4
	}
	return 5
}

// decimalLen is the bytes a decimal of a precision takes, its sign byte
// included.
func decimalLen(p uint8) int {
	switch {
	case p <= 9:
		return 5
	case p <= 19:
		return 9
	case p <= 28:
		return 13
	}
	return 17
}

// TypeName is the column's type as SQL writes it: int, decimal(18,2),
// nvarchar(max), datetime2(7).
func (c *Column) TypeName() string {
	n := func(name string, div int) string {
		if c.Size < 0 {
			return name + "(max)"
		}
		return name + "(" + strconv.Itoa(max(c.Size/div, 1)) + ")"
	}
	switch c.Type {
	case TypeInt1:
		return "tinyint"
	case TypeBit, TypeBitN:
		return "bit"
	case TypeInt2:
		return "smallint"
	case TypeInt4:
		return "int"
	case TypeInt8:
		return "bigint"
	case TypeIntN:
		return map[int]string{1: "tinyint", 2: "smallint", 4: "int", 8: "bigint"}[c.Size]
	case TypeFloat4:
		return "real"
	case TypeFloat8:
		return "float"
	case TypeFloatN:
		if c.Size == 4 {
			return "real"
		}
		return "float"
	case TypeMoney:
		return "money"
	case TypeMoney4:
		return "smallmoney"
	case TypeMoneyN:
		if c.Size == 4 {
			return "smallmoney"
		}
		return "money"
	case TypeDateTime:
		return "datetime"
	case TypeDateTime4:
		return "smalldatetime"
	case TypeDateTimeN:
		if c.Size == 4 {
			return "smalldatetime"
		}
		return "datetime"
	case TypeDecimal, TypeDecimalN:
		return fmt.Sprintf("decimal(%d,%d)", c.Precision, c.Scale)
	case TypeNumeric, TypeNumericN:
		return fmt.Sprintf("numeric(%d,%d)", c.Precision, c.Scale)
	case TypeGUID:
		return "uniqueidentifier"
	case TypeDateN:
		return "date"
	case TypeTimeN:
		return fmt.Sprintf("time(%d)", c.Scale)
	case TypeDateTime2N:
		return fmt.Sprintf("datetime2(%d)", c.Scale)
	case TypeDateTimeOffset:
		return fmt.Sprintf("datetimeoffset(%d)", c.Scale)
	case TypeBigChar, TypeChar:
		return n("char", 1)
	case TypeBigVarChar, TypeVarChar:
		return n("varchar", 1)
	case TypeNChar:
		return n("nchar", 2)
	case TypeNVarChar:
		return n("nvarchar", 2)
	case TypeBigBinary, TypeBinary:
		if c.UserType == userTypeTimestamp {
			return "timestamp"
		}
		return n("binary", 1)
	case TypeBigVarBinary, TypeVarBinary:
		return n("varbinary", 1)
	case TypeXML:
		return "xml"
	case TypeText:
		return "text"
	case TypeNText:
		return "ntext"
	case TypeImage:
		return "image"
	case TypeVariant:
		return "sql_variant"
	case TypeUDT:
		return c.UDTName
	}
	return fmt.Sprintf("type 0x%02x", c.Type)
}

// ParseType makes the column type of a SQL type name, as a parameter's or
// a bulk load's: bit, tinyint, smallint, int, bigint, real, float,
// decimal(p,s), numeric(p,s), money, smallmoney, date, time(s), datetime,
// smalldatetime, datetime2(s), datetimeoffset(s), uniqueidentifier,
// char(n), varchar(n|max), nchar(n), nvarchar(n|max), binary(n),
// varbinary(n|max), xml. A char or varchar has the collation given.
func ParseType(decl string, coll Collation) (Column, error) {
	s := strings.ToLower(strings.TrimSpace(decl))
	name, args, _ := strings.Cut(s, "(")
	name = strings.TrimSpace(name)
	var nums []string
	if args != "" {
		if !strings.HasSuffix(args, ")") {
			return Column{}, fmt.Errorf("mssql: %q is not a type", decl)
		}
		for _, a := range strings.Split(strings.TrimSuffix(args, ")"), ",") {
			nums = append(nums, strings.TrimSpace(a))
		}
	}
	arg := func(i, def, lo, hi int) (int, bool) {
		if i >= len(nums) {
			return def, true
		}
		if nums[i] == "max" && hi < 0 {
			return -1, true
		}
		v, err := strconv.Atoi(nums[i])
		return v, err == nil && v >= lo && v <= abs(hi)
	}
	c := Column{Collation: coll}
	ok := true
	fixed := func(t byte, size int) {
		c.Type, c.Size = t, size
		if len(nums) > 0 {
			ok = false
		}
	}
	switch name {
	case "bit":
		fixed(TypeBitN, 1)
	case "tinyint":
		fixed(TypeIntN, 1)
	case "smallint":
		fixed(TypeIntN, 2)
	case "int":
		fixed(TypeIntN, 4)
	case "bigint":
		fixed(TypeIntN, 8)
	case "real":
		fixed(TypeFloatN, 4)
	case "float":
		fixed(TypeFloatN, 8)
	case "money":
		fixed(TypeMoneyN, 8)
	case "smallmoney":
		fixed(TypeMoneyN, 4)
	case "datetime":
		fixed(TypeDateTimeN, 8)
	case "smalldatetime":
		fixed(TypeDateTimeN, 4)
	case "date":
		fixed(TypeDateN, 3)
	case "uniqueidentifier":
		fixed(TypeGUID, 16)
	case "xml":
		fixed(TypeXML, -1)
	case "decimal", "numeric":
		c.Type = TypeDecimalN
		if name == "numeric" {
			c.Type = TypeNumericN
		}
		var p, sc int
		var ok1, ok2 bool
		p, ok1 = arg(0, 18, 1, 38)
		sc, ok2 = arg(1, 0, 0, p)
		ok = ok1 && ok2 && len(nums) <= 2
		c.Precision, c.Scale, c.Size = uint8(p), uint8(sc), decimalLen(uint8(p))
	case "time", "datetime2", "datetimeoffset":
		sc, ok1 := arg(0, 7, 0, 7)
		ok = ok1 && len(nums) <= 1
		c.Scale = uint8(sc)
		switch name {
		case "time":
			c.Type, c.Size = TypeTimeN, timeLen(c.Scale)
		case "datetime2":
			c.Type, c.Size = TypeDateTime2N, timeLen(c.Scale)+3
		default:
			c.Type, c.Size = TypeDateTimeOffset, timeLen(c.Scale)+5
		}
	case "char", "varchar", "binary", "varbinary", "nchar", "nvarchar":
		hi, div := 8000, 1
		if name[0] == 'n' {
			hi, div = 4000, 2
		}
		if name == "varchar" || name == "varbinary" || name == "nvarchar" {
			hi = -hi
		}
		n, ok1 := arg(0, 1, 1, hi)
		ok = ok1 && len(nums) <= 1
		c.Size = n * div
		if n < 0 {
			c.Size = -1
		}
		c.Type = map[string]byte{"char": TypeBigChar, "varchar": TypeBigVarChar, "binary": TypeBigBinary,
			"varbinary": TypeBigVarBinary, "nchar": TypeNChar, "nvarchar": TypeNVarChar}[name]
	default:
		ok = false
	}
	if !ok {
		return Column{}, fmt.Errorf("mssql: %q is not a type the client sends", decl)
	}
	return c, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// readTypeInfo reads a TYPE_INFO into c; meta says it is a COLMETADATA's,
// where text, ntext and image are followed by their table's name.
func (r *msgReader) readTypeInfo(c *Column, meta bool) error {
	t, err := r.u8()
	if err != nil {
		return err
	}
	c.Type = t
	if n, ok := fixedSizes[t]; ok {
		c.Size = n
		return nil
	}
	switch t {
	case TypeGUID, TypeIntN, TypeBitN, TypeFloatN, TypeMoneyN, TypeDateTimeN, TypeChar, TypeVarChar, TypeBinary, TypeVarBinary:
		n, err := r.u8()
		c.Size = int(n)
		return err
	case TypeDecimal, TypeNumeric, TypeDecimalN, TypeNumericN:
		b, err := r.next(3)
		if err != nil {
			return err
		}
		c.Size, c.Precision, c.Scale = int(b[0]), b[1], b[2]
		return nil
	case TypeDateN:
		c.Size = 3
		return nil
	case TypeTimeN, TypeDateTime2N, TypeDateTimeOffset:
		s, err := r.u8()
		if err != nil {
			return err
		}
		if s > 7 {
			return fmt.Errorf("mssql: a time scale of %d", s)
		}
		c.Scale = s
		c.Size = timeLen(s) + map[byte]int{TypeTimeN: 0, TypeDateTime2N: 3, TypeDateTimeOffset: 5}[t]
		return nil
	case TypeBigVarBinary, TypeBigBinary, TypeBigVarChar, TypeBigChar, TypeNVarChar, TypeNChar:
		n, err := r.u16()
		if err != nil {
			return err
		}
		c.Size = int(n)
		if n == 0xffff {
			c.Size = -1
		}
		if t == TypeBigVarChar || t == TypeBigChar || t == TypeNVarChar || t == TypeNChar {
			b, err := r.next(5)
			if err != nil {
				return err
			}
			copy(c.Collation[:], b)
		}
		return nil
	case TypeXML:
		c.Size = -1
		present, err := r.u8()
		if err != nil || present == 0 {
			return err
		}
		if _, err := r.bVarchar(); err != nil {
			return err
		}
		if _, err := r.bVarchar(); err != nil {
			return err
		}
		_, err = r.usVarchar()
		return err
	case TypeUDT:
		if _, err := r.u16(); err != nil {
			return err
		}
		c.Size = -1
		for i := 0; i < 3; i++ {
			s, err := r.bVarchar()
			if err != nil {
				return err
			}
			c.UDTName = s
		}
		_, err := r.usVarchar()
		return err
	case TypeText, TypeNText, TypeImage:
		n, err := r.u32()
		if err != nil {
			return err
		}
		c.Size = int(n)
		if t != TypeImage {
			b, err := r.next(5)
			if err != nil {
				return err
			}
			copy(c.Collation[:], b)
		}
		if !meta {
			return nil
		}
		parts, err := r.u8()
		for i := 0; err == nil && i < int(parts); i++ {
			_, err = r.usVarchar()
		}
		return err
	case TypeVariant:
		n, err := r.u32()
		c.Size = int(n)
		return err
	}
	return fmt.Errorf("mssql: the server sent a type 0x%02x the client does not know", t)
}

// readValue reads a value of column c.
func (r *msgReader) readValue(c *Column, limit int) (any, error) {
	var data []byte
	var err error
	switch c.kind() {
	case kindFixed:
		data, err = r.next(c.Size)
	case kindByte:
		var n byte
		if n, err = r.u8(); err == nil && n == 0 {
			return nil, nil
		}
		if err == nil {
			data, err = r.next(int(n))
		}
	case kindUShort:
		var n uint16
		if n, err = r.u16(); err == nil && n == 0xffff {
			return nil, nil
		}
		if err == nil {
			data, err = r.next(int(n))
		}
	case kindPLP:
		return r.readPLP(c, limit)
	case kindText:
		var n byte
		if n, err = r.u8(); err == nil && n == 0 {
			return nil, nil
		}
		if err == nil {
			err = r.skip(int(n) + 8)
		}
		var size uint32
		if err == nil {
			size, err = r.u32()
		}
		if err == nil && int64(size) > int64(limit) {
			return nil, fmt.Errorf("mssql: a value of %d bytes, past the bound of %d", size, limit)
		}
		if err == nil {
			data, err = r.next(int(size))
		}
	case kindVariant:
		var size uint32
		if size, err = r.u32(); err == nil && size == 0 {
			return nil, nil
		}
		if err == nil && int64(size) > int64(limit) {
			return nil, fmt.Errorf("mssql: a value of %d bytes, past the bound of %d", size, limit)
		}
		if err == nil {
			data, err = r.next(int(size))
		}
		if err == nil {
			return variant(data)
		}
	}
	if err != nil {
		return nil, err
	}
	return convert(c, data)
}

// readPLP reads a PLP stream whole.
func (r *msgReader) readPLP(c *Column, limit int) (any, error) {
	total, err := r.u64()
	if err != nil {
		return nil, err
	}
	if total == math.MaxUint64 {
		return nil, nil
	}
	if total != math.MaxUint64-1 && total > uint64(limit) {
		return nil, fmt.Errorf("mssql: a value of %d bytes, past the bound of %d", total, limit)
	}
	var out []byte
	if total != math.MaxUint64-1 {
		out = make([]byte, 0, total)
	}
	for {
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		if len(out)+int(n) > limit {
			return nil, fmt.Errorf("mssql: a value past the bound of %d bytes", limit)
		}
		b, err := r.next(int(n))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	if out == nil {
		out = []byte{}
	}
	return convertOwned(c, out)
}

// variant reads a sql_variant's base type and value.
func variant(data []byte) (any, error) {
	if len(data) < 2 || len(data) < 2+int(data[1]) {
		return nil, errors.New("mssql: a malformed sql_variant")
	}
	c := &Column{Type: data[0]}
	props, val := data[2:2+int(data[1])], data[2+int(data[1]):]
	switch c.Type {
	case TypeDecimalN, TypeNumericN:
		if len(props) < 2 {
			return nil, errors.New("mssql: a malformed sql_variant")
		}
		c.Precision, c.Scale = props[0], props[1]
	case TypeTimeN, TypeDateTime2N, TypeDateTimeOffset:
		if len(props) < 1 {
			return nil, errors.New("mssql: a malformed sql_variant")
		}
		c.Scale = props[0]
	case TypeBigVarChar, TypeBigChar, TypeNVarChar, TypeNChar:
		if len(props) < 7 {
			return nil, errors.New("mssql: a malformed sql_variant")
		}
		copy(c.Collation[:], props[2:7])
	}
	if n, ok := fixedSizes[c.Type]; ok && len(val) != n {
		return nil, errors.New("mssql: a malformed sql_variant")
	}
	return convert(c, val)
}

// convert makes a value of c's type of its bytes, which it copies.
func convert(c *Column, b []byte) (any, error) {
	switch c.Type {
	case TypeBigVarBinary, TypeBigBinary, TypeBinary, TypeVarBinary, TypeImage, TypeUDT:
		return append([]byte(nil), b...), nil
	}
	return convertOwned(c, b)
}

// convertOwned is convert of bytes the value may keep.
func convertOwned(c *Column, b []byte) (any, error) {
	bad := func() (any, error) {
		return nil, fmt.Errorf("mssql: a %s value of %d bytes", c.TypeName(), len(b))
	}
	switch c.Type {
	case TypeInt1, TypeInt2, TypeInt4, TypeInt8, TypeIntN:
		switch len(b) {
		case 1:
			return int64(b[0]), nil
		case 2:
			return int64(int16(binary.LittleEndian.Uint16(b))), nil
		case 4:
			return int64(int32(binary.LittleEndian.Uint32(b))), nil
		case 8:
			return int64(binary.LittleEndian.Uint64(b)), nil
		}
	case TypeBit, TypeBitN:
		if len(b) == 1 {
			return b[0] != 0, nil
		}
	case TypeFloat4, TypeFloat8, TypeFloatN:
		switch len(b) {
		case 4:
			return math.Float32frombits(binary.LittleEndian.Uint32(b)), nil
		case 8:
			return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
		}
	case TypeMoney, TypeMoney4, TypeMoneyN:
		switch len(b) {
		case 4:
			return scaled(big.NewInt(int64(int32(binary.LittleEndian.Uint32(b)))), 4), nil
		case 8:
			v := int64(int32(binary.LittleEndian.Uint32(b)))<<32 | int64(binary.LittleEndian.Uint32(b[4:]))
			return scaled(big.NewInt(v), 4), nil
		}
	case TypeDecimal, TypeNumeric, TypeDecimalN, TypeNumericN:
		if len(b) >= 1 && len(b) <= 17 {
			mag := make([]byte, len(b)-1)
			for i := range mag {
				mag[i] = b[len(b)-1-i]
			}
			v := new(big.Int).SetBytes(mag)
			if b[0] == 0 {
				v.Neg(v)
			}
			return scaled(v, int(c.Scale)), nil
		}
	case TypeDateTime, TypeDateTime4, TypeDateTimeN:
		switch len(b) {
		case 4:
			days, mins := binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:])
			return day1900.AddDate(0, 0, int(days)).Add(time.Duration(mins) * time.Minute), nil
		case 8:
			days, ticks := int32(binary.LittleEndian.Uint32(b)), binary.LittleEndian.Uint32(b[4:])
			ms := (int64(ticks)*10 + 1) / 3
			return day1900.AddDate(0, 0, int(days)).Add(time.Duration(ms) * time.Millisecond), nil
		}
	case TypeDateN:
		if len(b) == 3 {
			return dateOf(b), nil
		}
	case TypeTimeN:
		if len(b) == timeLen(c.Scale) {
			return timeOf(b, c.Scale), nil
		}
	case TypeDateTime2N:
		if n := timeLen(c.Scale); len(b) == n+3 {
			return dateOf(b[n:]).Add(timeOf(b[:n], c.Scale)), nil
		}
	case TypeDateTimeOffset:
		if n := timeLen(c.Scale); len(b) == n+5 {
			t := dateOf(b[n : n+3]).Add(timeOf(b[:n], c.Scale))
			off := int(int16(binary.LittleEndian.Uint16(b[n+3:])))
			return t.In(time.FixedZone("", off*60)), nil
		}
	case TypeGUID:
		if len(b) == 16 {
			return swapGUID(b), nil
		}
	case TypeBigVarChar, TypeBigChar, TypeChar, TypeVarChar, TypeText:
		return decodeChars(c.Collation, b), nil
	case TypeNVarChar, TypeNChar, TypeNText, TypeXML:
		if len(b)%2 == 0 {
			return strings.TrimPrefix(ucs2String(b), "\ufeff"), nil
		}
	case TypeBigVarBinary, TypeBigBinary, TypeBinary, TypeVarBinary, TypeImage, TypeUDT:
		return b, nil
	case TypeNull:
		return nil, nil
	}
	return bad()
}

// scaled is v divided by 10^scale, as text.
func scaled(v *big.Int, scale int) Decimal {
	s := v.String()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if scale > 0 {
		if len(s) <= scale {
			s = strings.Repeat("0", scale-len(s)+1) + s
		}
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if neg {
		s = "-" + s
	}
	return Decimal(s)
}

var (
	dayZero = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	day1900 = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
)

// daysSince counts the days from base to t's date, as its clock reads.
func daysSince(t, base time.Time) int64 {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return (day.Unix() - base.Unix()) / 86400
}

func dateOf(b []byte) time.Time {
	days := int(b[0]) | int(b[1])<<8 | int(b[2])<<16
	return dayZero.AddDate(0, 0, days)
}

var pow10 = [...]int64{1, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000, 1000000000}

func timeOf(b []byte, scale uint8) time.Duration {
	var v int64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | int64(b[i])
	}
	return time.Duration(v * pow10[9-scale])
}

// appendTypeInfo appends c's TYPE_INFO; meta says for a COLMETADATA,
// where text, ntext and image name their table (none here).
func appendTypeInfo(b []byte, c *Column, meta bool) []byte {
	b = append(b, c.Type)
	switch c.Type {
	case TypeGUID, TypeIntN, TypeBitN, TypeFloatN, TypeMoneyN, TypeDateTimeN, TypeChar, TypeVarChar, TypeBinary, TypeVarBinary:
		b = append(b, byte(c.Size))
	case TypeDecimal, TypeNumeric, TypeDecimalN, TypeNumericN:
		b = append(b, byte(decimalLen(c.Precision)), c.Precision, c.Scale)
	case TypeTimeN, TypeDateTime2N, TypeDateTimeOffset:
		b = append(b, c.Scale)
	case TypeBigVarBinary, TypeBigBinary, TypeBigVarChar, TypeBigChar, TypeNVarChar, TypeNChar:
		size := uint16(c.Size)
		if c.Size < 0 {
			size = 0xffff
		}
		b = binary.LittleEndian.AppendUint16(b, size)
		if c.Type == TypeBigVarChar || c.Type == TypeBigChar || c.Type == TypeNVarChar || c.Type == TypeNChar {
			b = append(b, c.Collation[:]...)
		}
	case TypeXML:
		b = append(b, 0)
	case TypeUDT:
		b = binary.LittleEndian.AppendUint16(b, 0xffff)
		b = appendBVarchar(appendBVarchar(appendBVarchar(b, ""), "sys"), c.UDTName)
		b = appendUSVarchar(b, "")
	case TypeText, TypeNText, TypeImage:
		b = binary.LittleEndian.AppendUint32(b, uint32(c.Size))
		if c.Type != TypeImage {
			b = append(b, c.Collation[:]...)
		}
		if meta {
			b = append(b, 0)
		}
	case TypeVariant:
		b = binary.LittleEndian.AppendUint32(b, uint32(c.Size))
	}
	return b
}

// appendValue appends v as a value of column c, its length first as c's
// type has it.
func appendValue(b []byte, c *Column, v any) ([]byte, error) {
	if v == nil {
		switch c.kind() {
		case kindByte:
			return append(b, 0), nil
		case kindUShort:
			return binary.LittleEndian.AppendUint16(b, 0xffff), nil
		case kindPLP:
			return binary.LittleEndian.AppendUint64(b, math.MaxUint64), nil
		case kindText:
			return append(b, 0), nil
		}
		return nil, fmt.Errorf("mssql: NULL as a %s, which cannot hold it", c.TypeName())
	}
	data, err := encode(c, v)
	if err != nil {
		return nil, err
	}
	switch c.kind() {
	case kindFixed:
	case kindByte:
		b = append(b, byte(len(data)))
	case kindUShort:
		if len(data) > c.Size {
			return nil, fmt.Errorf("mssql: a value of %d bytes for a %s", len(data), c.TypeName())
		}
		b = binary.LittleEndian.AppendUint16(b, uint16(len(data)))
	case kindPLP:
		b = binary.LittleEndian.AppendUint64(b, math.MaxUint64-1)
		if len(data) > 0 {
			b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
		}
		b = append(b, data...)
		return binary.LittleEndian.AppendUint32(b, 0), nil
	case kindText:
		b = append(b, 16)
		b = append(b, make([]byte, 24)...)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	default:
		return nil, fmt.Errorf("mssql: the client does not send %s values", c.TypeName())
	}
	return append(b, data...), nil
}

// encode makes v the bytes of a value of c's type.
func encode(c *Column, v any) ([]byte, error) {
	bad := func() ([]byte, error) {
		return nil, fmt.Errorf("mssql: a %T is not a %s", v, c.TypeName())
	}
	switch c.Type {
	case TypeInt1, TypeInt2, TypeInt4, TypeInt8, TypeIntN:
		n, ok := toInt(v)
		if !ok {
			return bad()
		}
		size := c.Size
		lo, hi := map[int]int64{1: 0, 2: math.MinInt16, 4: math.MinInt32, 8: math.MinInt64}[size], map[int]int64{1: 255, 2: math.MaxInt16, 4: math.MaxInt32, 8: math.MaxInt64}[size]
		if n < lo || n > hi {
			return nil, fmt.Errorf("mssql: %d is out of a %s's range", n, c.TypeName())
		}
		return binary.LittleEndian.AppendUint64(nil, uint64(n))[:size], nil
	case TypeBit, TypeBitN:
		switch x := v.(type) {
		case bool:
			if x {
				return []byte{1}, nil
			}
			return []byte{0}, nil
		}
		if n, ok := toInt(v); ok && (n == 0 || n == 1) {
			return []byte{byte(n)}, nil
		}
		return bad()
	case TypeFloat4, TypeFloat8, TypeFloatN:
		f, ok := toFloat(v)
		if !ok {
			return bad()
		}
		if c.Size == 4 {
			return binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(f))), nil
		}
		return binary.LittleEndian.AppendUint64(nil, math.Float64bits(f)), nil
	case TypeMoney, TypeMoney4, TypeMoneyN:
		n, err := toScaled(v, 4, 19)
		if err != nil {
			return nil, fmt.Errorf("mssql: %v as a %s: %w", v, c.TypeName(), err)
		}
		if c.Size == 4 {
			if !n.IsInt64() || n.Int64() < math.MinInt32 || n.Int64() > math.MaxInt32 {
				return nil, fmt.Errorf("mssql: %v is out of a smallmoney's range", v)
			}
			return binary.LittleEndian.AppendUint32(nil, uint32(int32(n.Int64()))), nil
		}
		if !n.IsInt64() {
			return nil, fmt.Errorf("mssql: %v is out of a money's range", v)
		}
		x := n.Int64()
		b := binary.LittleEndian.AppendUint32(nil, uint32(x>>32))
		return binary.LittleEndian.AppendUint32(b, uint32(x)), nil
	case TypeDecimal, TypeNumeric, TypeDecimalN, TypeNumericN:
		n, err := toScaled(v, int(c.Scale), int(c.Precision))
		if err != nil {
			return nil, fmt.Errorf("mssql: %v as a %s: %w", v, c.TypeName(), err)
		}
		size := decimalLen(c.Precision)
		b := make([]byte, size)
		b[0] = 1
		if n.Sign() < 0 {
			b[0] = 0
		}
		mag := new(big.Int).Abs(n).Bytes()
		for i := range mag {
			b[1+i] = mag[len(mag)-1-i]
		}
		return b, nil
	case TypeDateTime, TypeDateTime4, TypeDateTimeN:
		t, ok := v.(time.Time)
		if !ok {
			return bad()
		}
		days := daysSince(t, day1900)
		in := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())
		if c.Size == 4 {
			mins := int64((in + 30*time.Second) / time.Minute)
			if mins == 1440 {
				days, mins = days+1, 0
			}
			if days < 0 || days > math.MaxUint16 {
				return nil, fmt.Errorf("mssql: %v is out of a smalldatetime's range", t)
			}
			b := binary.LittleEndian.AppendUint16(nil, uint16(days))
			return binary.LittleEndian.AppendUint16(b, uint16(mins)), nil
		}
		ticks := (int64(in/time.Millisecond)*3 + 5) / 10
		if ticks == 300*86400 {
			days, ticks = days+1, 0
		}
		if days < -53690 || days > 2958463 {
			return nil, fmt.Errorf("mssql: %v is out of a datetime's range", t)
		}
		b := binary.LittleEndian.AppendUint32(nil, uint32(int32(days)))
		return binary.LittleEndian.AppendUint32(b, uint32(ticks)), nil
	case TypeDateN:
		t, ok := v.(time.Time)
		if !ok {
			return bad()
		}
		return appendDate(nil, t)
	case TypeTimeN:
		var d time.Duration
		switch x := v.(type) {
		case time.Duration:
			d = x
		case time.Time:
			d = clock(x)
		default:
			return bad()
		}
		if d < 0 || d >= 24*time.Hour {
			return nil, fmt.Errorf("mssql: %v is not a time of day", d)
		}
		return appendTime(nil, d, c.Scale), nil
	case TypeDateTime2N:
		t, ok := v.(time.Time)
		if !ok {
			return bad()
		}
		return appendDate(appendTime(nil, clock(t), c.Scale), t)
	case TypeDateTimeOffset:
		t, ok := v.(time.Time)
		if !ok {
			return bad()
		}
		_, off := t.Zone()
		u := t.UTC()
		b, err := appendDate(appendTime(nil, clock(u), c.Scale), u)
		if err != nil {
			return nil, err
		}
		return binary.LittleEndian.AppendUint16(b, uint16(int16(off/60))), nil
	case TypeGUID:
		var u UUID
		switch x := v.(type) {
		case UUID:
			u = x
		case [16]byte:
			u = x
		case string:
			var err error
			if u, err = ParseUUID(x); err != nil {
				return nil, err
			}
		default:
			return bad()
		}
		g := swapGUID(u[:])
		return g[:], nil
	case TypeBigVarChar, TypeBigChar, TypeChar, TypeVarChar, TypeText:
		switch x := v.(type) {
		case string:
			return encodeChars(c.Collation, x)
		case Decimal:
			return []byte(x), nil
		case []byte:
			return x, nil
		}
		return bad()
	case TypeNVarChar, TypeNChar, TypeNText, TypeXML:
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case Decimal:
			s = string(x)
		case []byte:
			if !utf8.Valid(x) {
				return nil, fmt.Errorf("mssql: bytes that are not UTF-8 as a %s", c.TypeName())
			}
			s = string(x)
		default:
			return bad()
		}
		var b []byte
		if c.Type == TypeXML {
			// Without its byte order mark, the server reads xml as
			// UTF-8.
			b = []byte{0xff, 0xfe}
		}
		return appendUCS2(b, s), nil
	case TypeBigVarBinary, TypeBigBinary, TypeBinary, TypeVarBinary, TypeImage, TypeUDT:
		switch x := v.(type) {
		case []byte:
			return x, nil
		case string:
			return []byte(x), nil
		}
		return bad()
	}
	return bad()
}

func clock(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond())
}

func appendTime(b []byte, d time.Duration, scale uint8) []byte {
	unit := pow10[9-scale]
	v := (int64(d) + unit/2) / unit
	if v >= 86400*pow10[scale] {
		v = 86400*pow10[scale] - 1
	}
	for i := 0; i < timeLen(scale); i++ {
		b = append(b, byte(v>>(8*i)))
	}
	return b
}

func appendDate(b []byte, t time.Time) ([]byte, error) {
	if t.Year() < 1 || t.Year() > 9999 {
		return nil, fmt.Errorf("mssql: %v is out of a date's range", t)
	}
	days := daysSince(t, dayZero)
	return append(b, byte(days), byte(days>>8), byte(days>>16)), nil
}

func toInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int8:
		return int64(x), true
	case int16:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), x <= math.MaxInt64
	case uint8:
		return int64(x), true
	case uint16:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), x <= math.MaxInt64
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	if n, ok := toInt(v); ok {
		return float64(n), true
	}
	return 0, false
}

// toScaled makes v an integer of scale's decimals, rounded half away from
// zero, of at most precision digits.
func toScaled(v any, scale, precision int) (*big.Int, error) {
	var s string
	switch x := v.(type) {
	case Decimal:
		s = string(x)
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'f', -1, 32)
	default:
		n, ok := toInt(v)
		if !ok {
			if u, isU := v.(uint64); isU {
				s = strconv.FormatUint(u, 10)
				break
			}
			return nil, fmt.Errorf("a %T is not a number", v)
		}
		s = strconv.FormatInt(n, 10)
	}
	t := strings.TrimSpace(s)
	neg := strings.HasPrefix(t, "-")
	t = strings.TrimPrefix(strings.TrimPrefix(t, "-"), "+")
	whole, frac, _ := strings.Cut(t, ".")
	if whole == "" && frac == "" || strings.Trim(whole+frac, "0123456789") != "" {
		return nil, fmt.Errorf("%q is not a number", s)
	}
	up := false
	if len(frac) > scale {
		up = frac[scale] >= '5'
		frac = frac[:scale]
	}
	frac += strings.Repeat("0", scale-len(frac))
	n, _ := new(big.Int).SetString("0"+whole+frac, 10)
	if up {
		n.Add(n, big.NewInt(1))
	}
	if len(n.String()) > precision {
		return nil, fmt.Errorf("%s has more than %d digits", s, precision)
	}
	if neg {
		n.Neg(n)
	}
	return n, nil
}
