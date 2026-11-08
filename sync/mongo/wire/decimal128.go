package mongowire

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

// Decimal128 is a BSON decimal128: IEEE 754-2008's 128-bit decimal in its
// binary integer form, as the high and low 64 bits.
type Decimal128 struct {
	H, L uint64
}

const (
	decExpBias = 6176
	decExpMax  = 6111
	decExpMin  = -6176
	decDigits  = 34
)

// decimal128 reads the 16 bytes of a BSON decimal128.
func decimal128(b []byte) Decimal128 {
	return Decimal128{L: binary.LittleEndian.Uint64(b), H: binary.LittleEndian.Uint64(b[8:])}
}

func (d Decimal128) append(b []byte) []byte {
	b = binary.LittleEndian.AppendUint64(b, d.L)
	return binary.LittleEndian.AppendUint64(b, d.H)
}

// IsNaN and IsInf report whether d is a NaN or an infinity.
func (d Decimal128) IsNaN() bool { return d.H>>58&0x1F == 0x1F }
func (d Decimal128) IsInf() bool { return d.H>>58&0x1F == 0x1E }

// parts returns d's sign, coefficient (high and low) and exponent; a
// coefficient past 34 digits, which no canonical value has, is 0.
func (d Decimal128) parts() (neg bool, hi, lo uint64, exp int) {
	neg = d.H>>63 == 1
	if d.H>>61&3 == 3 {
		// The coefficient's implied leading bits make it larger than 34
		// digits can be: non-canonical, read as 0.
		return neg, 0, 0, int(d.H>>47&0x3FFF) - decExpBias
	}
	hi, lo = d.H&(1<<49-1), d.L
	exp = int(d.H>>49&0x3FFF) - decExpBias
	// 10^34 - 1 = 0x1ED09BEAD87C0378D8E63FFFFFFFF.
	const maxHi, maxLo = 0x1ED09BEAD87C0, 0x378D8E63FFFFFFFF
	if hi > maxHi || hi == maxHi && lo > maxLo {
		hi, lo = 0, 0
	}
	return neg, hi, lo, exp
}

// String writes d as the BSON decimal128 specification does: plain for an
// exponent from -6-(digits-1) to 0, as "-12.3400"; scientific otherwise, as
// "1.0E+3"; "NaN", "Infinity" and "-Infinity".
func (d Decimal128) String() string {
	if d.IsNaN() {
		return "NaN"
	}
	neg, hi, lo, exp := d.parts()
	if d.IsInf() {
		if neg {
			return "-Infinity"
		}
		return "Infinity"
	}
	digits := uint128String(hi, lo)
	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	adjusted := exp + len(digits) - 1
	switch {
	case exp == 0:
		sb.WriteString(digits)
	case exp < 0 && adjusted >= -6:
		if n := len(digits) + exp; n > 0 {
			sb.WriteString(digits[:n])
			sb.WriteByte('.')
			sb.WriteString(digits[n:])
		} else {
			sb.WriteString("0.")
			sb.WriteString(strings.Repeat("0", -n))
			sb.WriteString(digits)
		}
	default:
		sb.WriteByte(digits[0])
		if len(digits) > 1 {
			sb.WriteByte('.')
			sb.WriteString(digits[1:])
		}
		sb.WriteByte('E')
		if adjusted >= 0 {
			sb.WriteByte('+')
		}
		sb.WriteString(strconv.Itoa(adjusted))
	}
	return sb.String()
}

// uint128String writes hi:lo in decimal.
func uint128String(hi, lo uint64) string {
	if hi == 0 {
		return strconv.FormatUint(lo, 10)
	}
	const e19 = 10_000_000_000_000_000_000
	var parts []uint64
	for hi != 0 || lo != 0 {
		var r uint64
		hi, r = hi/e19, hi%e19
		lo, r = bits.Div64(r, lo, e19)
		parts = append(parts, r)
	}
	s := strconv.FormatUint(parts[len(parts)-1], 10)
	for i := len(parts) - 2; i >= 0; i-- {
		p := strconv.FormatUint(parts[i], 10)
		s += strings.Repeat("0", 19-len(p)) + p
	}
	return s
}

// ParseDecimal128 reads a decimal number, as "-12.3400", "1E+3", "NaN" or
// "-Infinity". A number that decimal128 cannot hold exactly (more than 34
// significant digits, or an exponent out of its range) is an error, not
// rounded.
func ParseDecimal128(s string) (Decimal128, error) {
	bad := func(why string) (Decimal128, error) {
		return Decimal128{}, fmt.Errorf("mongowire: decimal %q: %s", s, why)
	}
	t := s
	neg := false
	if t != "" && (t[0] == '-' || t[0] == '+') {
		neg = t[0] == '-'
		t = t[1:]
	}
	var sign uint64
	if neg {
		sign = 1 << 63
	}
	switch strings.ToLower(t) {
	case "nan":
		return Decimal128{H: 0x7C00_0000_0000_0000}, nil
	case "inf", "infinity":
		return Decimal128{H: sign | 0x7800_0000_0000_0000}, nil
	}
	var digits []byte
	exp, sawDigit, sawDot := 0, false, false
	i := 0
	for ; i < len(t); i++ {
		c := t[i]
		if c == '.' {
			if sawDot {
				return bad("two points")
			}
			sawDot = true
			continue
		}
		if c < '0' || c > '9' {
			break
		}
		sawDigit = true
		if sawDot {
			exp--
		}
		if len(digits) == 0 && c == '0' {
			continue // a leading zero
		}
		digits = append(digits, c)
		if len(digits) > 7000 {
			return bad("too many digits")
		}
	}
	if !sawDigit {
		return bad("no digits")
	}
	if i < len(t) {
		if t[i] != 'e' && t[i] != 'E' {
			return bad("not a number")
		}
		e, err := strconv.Atoi(t[i+1:])
		if err != nil || e > 1e6 || e < -1e6 {
			return bad("an exponent")
		}
		exp += e
	}
	// Drop trailing zeros while there are too many digits or the exponent
	// is too small; add them while it is too large.
	for len(digits) > 0 && digits[len(digits)-1] == '0' && (len(digits) > decDigits || exp < decExpMin) {
		digits = digits[:len(digits)-1]
		exp++
	}
	if len(digits) == 0 {
		exp = min(max(exp, decExpMin), decExpMax)
	}
	for exp > decExpMax && len(digits) > 0 && len(digits) < decDigits {
		digits = append(digits, '0')
		exp--
	}
	switch {
	case len(digits) > decDigits:
		return bad("more than 34 significant digits")
	case exp > decExpMax:
		return bad("too large")
	case exp < decExpMin:
		return bad("too small")
	}
	var hi, lo uint64
	for _, c := range digits {
		var carry uint64
		carry, lo = bits.Mul64(lo, 10)
		hi = hi*10 + carry
		var c2 uint64
		lo, c2 = bits.Add64(lo, uint64(c-'0'), 0)
		hi += c2
	}
	return Decimal128{H: sign | uint64(exp+decExpBias)<<49 | hi, L: lo}, nil
}
