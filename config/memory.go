package config

import (
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/lesismal/fit/internal/sysmem"
)

// Memory is an amount of memory, written in a config as an integer, a
// number of bytes, or as a string, a share of the memory fit may use, such
// as "50%": more than 0 and at most 100, to a hundredth of a percent. The
// memory is what sysmem.Total reads, the machine's, or its cgroup's limit
// where that is lower. A share is held as its hundredths of a percent,
// negated, so that a Memory is one integer.
type Memory int64

// MemoryType is Memory's type, which the decoder and the schema treat as an
// integer or a string.
var MemoryType = reflect.TypeFor[Memory]()

// MemoryPercent returns the share p percent of memory.
func MemoryPercent(p float64) Memory { return Memory(-math.Round(p * 100)) }

// Percent returns m's share of memory, in percent, and whether it is one.
func (m Memory) Percent() (float64, bool) { return float64(-m) / 100, m < 0 }

// Bytes returns m in bytes: a share of the memory sysmem.Total reads, or
// fallback where it reads none. A share is a byte at least.
func (m Memory) Bytes(fallback int64) int64 {
	p, ok := m.Percent()
	if !ok {
		return int64(m)
	}
	total := sysmem.Total()
	if total == 0 {
		return fallback
	}
	return max(int64(min(float64(total)*p/100, math.MaxInt64)), 1)
}

// String returns m as a config writes it: its bytes, or its share with %.
func (m Memory) String() string {
	if p, ok := m.Percent(); ok {
		return strconv.FormatFloat(p, 'f', -1, 64) + "%"
	}
	return strconv.FormatInt(int64(m), 10)
}

// MarshalJSON writes m as a config takes it.
func (m Memory) MarshalJSON() ([]byte, error) {
	if _, ok := m.Percent(); ok {
		return []byte(strconv.Quote(m.String())), nil
	}
	return []byte(m.String()), nil
}

func (d *decoder) decodeMemory(n *node, path string, rv reflect.Value) {
	switch n.kind {
	case kindNumber:
		v, err := strconv.ParseInt(n.s, 10, 64)
		if err != nil || v < 0 {
			d.errorf(n, n.off, path, `%s is not a number of bytes, 0 or more, nor a share of memory such as "50%%"`, n.s)
			return
		}
		rv.SetInt(v)
	case kindString:
		s, ok := strings.CutSuffix(n.s, "%")
		p, err := strconv.ParseFloat(s, 64)
		if !ok || err != nil || s != strings.TrimSpace(s) {
			d.errorf(n, n.off, path, `%q is not a share of memory, such as "50%%"`, n.s)
			return
		}
		if !(p > 0 && p <= 100) || MemoryPercent(p) == 0 {
			d.errorf(n, n.off, path, "%q is not more than 0%% and at most 100%%", n.s)
			return
		}
		rv.SetInt(int64(MemoryPercent(p)))
	default:
		d.errorf(n, n.off, path, "expected a number of bytes or a share of memory, got %s", n.kind)
	}
}
