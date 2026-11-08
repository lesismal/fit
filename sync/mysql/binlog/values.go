package binlog

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// value reads the value of column c.
func (dec *rowDecoder) value(r *rd, c *Column) (fitsync.Value, error) {
	switch c.Type {
	case TypeTiny, TypeShort, TypeInt24, TypeLong, TypeLongLong:
		n := map[byte]int{TypeTiny: 1, TypeShort: 2, TypeInt24: 3, TypeLong: 4, TypeLongLong: 8}[c.Type]
		u := r.uN(n)
		if r.bad {
			break
		}
		if c.Unsigned {
			return fitsync.Uint(u), nil
		}
		shift := 64 - 8*n
		return fitsync.Int(int64(u<<shift) >> shift), nil
	case TypeFloat:
		f := math.Float32frombits(r.u32())
		if r.bad {
			break
		}
		g, _ := strconv.ParseFloat(strconv.FormatFloat(float64(f), 'g', -1, 32), 64)
		return fitsync.Float(g), nil
	case TypeDouble:
		f := math.Float64frombits(r.u64())
		if r.bad {
			break
		}
		return fitsync.Float(f), nil
	case TypeNewDecimal:
		s, err := decodeDecimal(r, int(c.Meta>>8), int(c.Meta&0xff))
		if err != nil {
			return fitsync.Value{}, err
		}
		return fitsync.Decimal(s), nil
	case TypeYear:
		y := int64(r.u8())
		if y != 0 {
			y += 1900
		}
		return fitsync.Int(y), nil
	case TypeDate, TypeNewDate:
		v := r.uN(3)
		return dateValue(int(v>>9), int(v>>5&15), int(v&31)), nil
	case TypeTime:
		v := int64(r.uN(3)<<40) >> 40
		neg := v < 0
		if neg {
			v = -v
		}
		return fitsync.String(formatTime(neg, int(v/10000), int(v/100%100), int(v%100), 0, 0)), nil
	case TypeTime2:
		return dec.time2(r, int(c.Meta))
	case TypeDateTime:
		v := r.u64()
		d, t := v/1000000, v%1000000
		return dec.dateTime(int(d/10000), int(d/100%100), int(d%100), int(t/10000), int(t/100%100), int(t%100), 0, 0), nil
	case TypeDateTime2:
		return dec.dateTime2(r, int(c.Meta))
	case TypeTimestamp:
		return timestampValue(int64(r.u32()), 0, 0), nil
	case TypeTimestamp2:
		b := r.take(4)
		frac := readFrac(r, int(c.Meta))
		if r.bad {
			break
		}
		return timestampValue(int64(binary.BigEndian.Uint32(b)), frac, int(c.Meta)), nil
	case TypeBit:
		n := int(c.Meta>>8) + int(c.Meta&0xff+7)/8
		var u uint64
		for _, b := range r.take(n) {
			u = u<<8 | uint64(b)
		}
		return fitsync.Uint(u), nil
	case TypeEnum:
		u := r.uN(int(c.Meta & 0xff))
		if r.bad {
			break
		}
		if c.EnumValues != nil {
			if u == 0 {
				return fitsync.String(""), nil // the error value
			}
			if u <= uint64(len(c.EnumValues)) {
				return fitsync.String(c.EnumValues[u-1]), nil
			}
		}
		return fitsync.Uint(u), nil
	case TypeSet:
		n := int(c.Meta & 0xff)
		if n > 8 {
			return fitsync.Value{}, malformed("a SET of %d bytes", n)
		}
		u := r.uN(n)
		if r.bad {
			break
		}
		if c.SetValues != nil {
			var names []string
			for i, s := range c.SetValues {
				if i < 64 && u&(1<<i) != 0 {
					names = append(names, s)
				}
			}
			return fitsync.String(strings.Join(names, ",")), nil
		}
		return fitsync.Uint(u), nil
	case TypeString:
		ln := 1
		if c.Meta > 255 {
			ln = 2
		}
		return stringValue(c, r.take(int(r.uN(ln))), true), nil
	case TypeVarChar, TypeVarString:
		ln := 1
		if c.Meta > 255 {
			ln = 2
		}
		return stringValue(c, r.take(int(r.uN(ln))), true), nil
	case TypeBlob, TypeTinyBlob, TypeMediumBlob, TypeLongBlob:
		b, err := blob(r, c)
		if err != nil {
			return fitsync.Value{}, err
		}
		return stringValue(c, b, false), nil
	case TypeGeometry, TypeVector:
		b, err := blob(r, c)
		if err != nil {
			return fitsync.Value{}, err
		}
		return fitsync.Bytes(b), nil
	case TypeJSON:
		v, _, err := dec.json(r, c)
		return v, err
	case TypeNull:
		return fitsync.Null(), nil
	default:
		return fitsync.Value{}, fmt.Errorf("%w: column type %d", ErrUnsupported, c.Type)
	}
	return fitsync.Value{}, malformed("a value of type %d", c.Type)
}

// blob reads a value with its length before it, in Meta bytes (1 to 4).
func blob(r *rd, c *Column) ([]byte, error) {
	if c.Meta < 1 || c.Meta > 4 {
		return nil, malformed("a length of %d bytes", c.Meta)
	}
	n := r.uN(int(c.Meta))
	b := r.take(int(min(n, uint64(len(r.b)+1))))
	if r.bad {
		return nil, malformed("a value of %d bytes", n)
	}
	return b, nil
}

// stringValue is b as a String, or Bytes for a binary string or one of an
// unknown charset that is not a CHAR or VARCHAR.
func stringValue(c *Column, b []byte, char bool) fitsync.Value {
	if c.Charset == CharsetBinary || c.Charset == 0 && !char {
		return fitsync.Bytes(b)
	}
	return fitsync.String(string(b))
}

func dateValue(y, m, d int) fitsync.Value {
	if y == 0 || m == 0 || d == 0 {
		return fitsync.String(fmt.Sprintf("%04d-%02d-%02d", y, m, d))
	}
	return fitsync.Date(y, time.Month(m), d)
}

// readFrac reads the fraction of a TIME2, DATETIME2 or TIMESTAMP2 of fsp
// digits, in microseconds: big-endian, in a byte for two digits.
func readFrac(r *rd, fsp int) int {
	switch fsp {
	case 1, 2:
		return int(r.u8()) * 10000
	case 3, 4:
		b := r.take(2)
		if b == nil {
			return 0
		}
		return int(binary.BigEndian.Uint16(b)) * 100
	case 5, 6:
		b := r.take(3)
		if b == nil {
			return 0
		}
		return int(b[0])<<16 | int(b[1])<<8 | int(b[2])
	}
	return 0
}

func fraction(us, fsp int) string {
	if fsp <= 0 || fsp > 6 {
		return ""
	}
	s := fmt.Sprintf("%06d", us)
	return "." + s[:fsp]
}

func formatTime(neg bool, h, m, s, us, fsp int) string {
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%02d:%02d:%02d%s", sign, h, m, s, fraction(us, fsp))
}

// time2 reads a TIME2: 3 bytes of sign, hours, minutes and seconds,
// big-endian with 0x800000 for zero, then the fraction.
func (dec *rowDecoder) time2(r *rd, fsp int) (fitsync.Value, error) {
	b := r.take(3)
	if b == nil {
		return fitsync.Value{}, malformed("a TIME value")
	}
	ip := int64(b[0])<<16 | int64(b[1])<<8 | int64(b[2]) - 0x800000
	var frac int64
	switch fsp {
	case 1, 2:
		frac = int64(r.u8())
		if ip < 0 && frac != 0 {
			ip++
			frac -= 0x100
		}
		frac *= 10000
	case 3, 4:
		bb := r.take(2)
		if bb != nil {
			frac = int64(binary.BigEndian.Uint16(bb))
		}
		if ip < 0 && frac != 0 {
			ip++
			frac -= 0x10000
		}
		frac *= 100
	case 5, 6:
		bb := r.take(3)
		if bb != nil {
			frac = int64(bb[0])<<16 | int64(bb[1])<<8 | int64(bb[2])
		}
		if ip < 0 && frac != 0 {
			ip++
			frac -= 0x1000000
		}
	}
	if r.bad {
		return fitsync.Value{}, malformed("a TIME value")
	}
	packed := ip<<24 + frac
	neg := packed < 0
	if neg {
		packed = -packed
	}
	hms := packed >> 24
	us := packed % (1 << 24)
	return fitsync.String(formatTime(neg, int(hms>>12%(1<<10)), int(hms>>6%64), int(hms%64), int(us), fsp)), nil
}

// dateTime2 reads a DATETIME2: 5 bytes big-endian, a sign bit (set),
// year*13+month in 17 bits, then day, hour, minute and second in 5, 5, 6
// and 6, then the fraction.
func (dec *rowDecoder) dateTime2(r *rd, fsp int) (fitsync.Value, error) {
	b := r.take(5)
	frac := readFrac(r, fsp)
	if r.bad {
		return fitsync.Value{}, malformed("a DATETIME value")
	}
	v := int64(b[0])<<32 | int64(b[1])<<24 | int64(b[2])<<16 | int64(b[3])<<8 | int64(b[4]) - 0x8000000000
	if v < 0 {
		return fitsync.Value{}, malformed("a negative DATETIME")
	}
	ymd, hms := v>>17, v&(1<<17-1)
	ym := ymd >> 5
	return dec.dateTime(int(ym/13), int(ym%13), int(ymd&31), int(hms>>12), int(hms>>6&63), int(hms&63), frac, fsp), nil
}

// dateTime is a DATETIME as a Time in the decoder's Location, or a String
// when it has zero parts.
func (dec *rowDecoder) dateTime(y, mo, d, h, mi, s, us, fsp int) fitsync.Value {
	if y == 0 || mo == 0 || d == 0 {
		return fitsync.String(fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d%s", y, mo, d, h, mi, s, fraction(us, fsp)))
	}
	return fitsync.Time(time.Date(y, time.Month(mo), d, h, mi, s, us*1000, dec.d.location()))
}

// timestampValue is a TIMESTAMP, seconds since the epoch, as a Time in
// UTC; 0 is the zero timestamp, a String.
func timestampValue(sec int64, us, fsp int) fitsync.Value {
	if sec == 0 && us == 0 {
		return fitsync.String("0000-00-00 00:00:00" + fraction(0, fsp))
	}
	return fitsync.Time(time.Unix(sec, int64(us)*1000).UTC())
}

var dig2bytes = [10]int{0, 1, 1, 2, 2, 3, 3, 4, 4, 4}

// decodeDecimal reads a DECIMAL(precision, scale) in MySQL's binary form:
// groups of nine digits in four bytes, big-endian, the leading and
// trailing groups shorter, the first bit inverted for the sign and every
// byte inverted for a negative number.
func decodeDecimal(r *rd, precision, scale int) (string, error) {
	if precision < 1 || precision > 65 || scale > 30 || scale > precision {
		return "", malformed("DECIMAL(%d,%d)", precision, scale)
	}
	intg := precision - scale
	ig, ix := intg/9, intg%9
	fg, fx := scale/9, scale%9
	size := ig*4 + dig2bytes[ix] + fg*4 + dig2bytes[fx]
	src := r.take(size)
	if src == nil {
		return "", malformed("a DECIMAL value")
	}
	var buf [32]byte
	b := buf[:size]
	copy(b, src)
	neg := b[0]&0x80 == 0
	b[0] ^= 0x80
	if neg {
		for i := range b {
			b[i] = ^b[i]
		}
	}
	group := func(n int) uint32 {
		var v uint32
		for _, c := range b[:n] {
			v = v<<8 | uint32(c)
		}
		b = b[n:]
		return v
	}
	var s strings.Builder
	if neg {
		s.WriteByte('-')
	}
	start := s.Len()
	digits := func(v uint32, width int) {
		t := strconv.FormatUint(uint64(v), 10)
		if len(t) > width {
			t = t[len(t)-width:]
		}
		if s.Len() == start {
			if v != 0 {
				s.WriteString(t)
			}
			return
		}
		s.WriteString(strings.Repeat("0", width-len(t)) + t)
	}
	if ix > 0 {
		digits(group(dig2bytes[ix]), ix)
	}
	for range ig {
		digits(group(4), 9)
	}
	if s.Len() == start {
		s.WriteByte('0')
	}
	if scale > 0 {
		s.WriteByte('.')
		for range fg {
			t := strconv.FormatUint(uint64(group(4)), 10)
			s.WriteString(strings.Repeat("0", max(0, 9-len(t))) + t)
		}
		if fx > 0 {
			t := strconv.FormatUint(uint64(group(dig2bytes[fx])), 10)
			s.WriteString(strings.Repeat("0", max(0, fx-len(t))) + t)
		}
	}
	return s.String(), nil
}
