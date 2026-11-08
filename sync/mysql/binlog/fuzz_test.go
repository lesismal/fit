package binlog

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"maps"
	"os"
	"runtime"
	"testing"
)

var capturedFiles = []string{"mysql57", "mysql80", "mysql84", "mariadb1011", "mariadb11"}

// splitEvents splits a captured binlog file into its events.
func splitEvents(t testing.TB, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".binlog")
	if err != nil {
		t.Fatal(err)
	}
	b = b[len(Magic):]
	var evs [][]byte
	for len(b) >= HeaderSize {
		n := binary.LittleEndian.Uint32(b[9:])
		evs = append(evs, b[:n])
		b = b[n:]
	}
	return evs
}

// withoutChecksums is a captured file's events without their CRC32s, its
// format description saying so, for the fuzzer, whose changes a checksum
// would refuse before they reach the decoding.
func withoutChecksums(t testing.TB, name string) []byte {
	out := append([]byte(nil), Magic...)
	for i, ev := range splitEvents(t, name) {
		ev = append([]byte(nil), ev...)
		if i == 0 {
			ev[len(ev)-5] = ChecksumOff
		} else {
			ev = ev[:len(ev)-4]
			binary.LittleEndian.PutUint32(ev[9:], uint32(len(ev)))
		}
		out = append(out, ev...)
	}
	return out
}

func (d *Decoder) clone() *Decoder {
	c := *d
	c.tables = maps.Clone(d.tables)
	return &c
}

// TestMutations decodes each event of the captured binlogs with every byte
// changed and cut at every length, its checksum made right: errors, never
// a panic.
func TestMutations(t *testing.T) {
	for _, name := range capturedFiles {
		base := NewDecoder()
		for k, ev := range splitEvents(t, name) {
			if k > 0 {
				body := len(ev) - 4
				m := make([]byte, len(ev))
				for i := range body {
					for _, f := range []func(byte) byte{
						func(byte) byte { return 0 }, func(byte) byte { return 0xff }, func(c byte) byte { return c ^ 1 },
					} {
						copy(m, ev)
						m[i] = f(m[i])
						binary.LittleEndian.PutUint32(m[body:], crc32.ChecksumIEEE(m[:body]))
						_, _ = base.clone().Decode(m)
					}
				}
				for n := HeaderSize; n < body; n++ {
					c := append(append([]byte(nil), ev[:n]...), 0, 0, 0, 0)
					binary.LittleEndian.PutUint32(c[9:], uint32(n+4))
					binary.LittleEndian.PutUint32(c[n:], crc32.ChecksumIEEE(c[:n]))
					_, _ = base.clone().Decode(c)
				}
			}
			if _, err := base.Decode(ev); err != nil {
				t.Fatalf("%s: event %d: %v", name, k, err)
			}
		}
	}
}

// FuzzReader reads a binlog file of anything: it must not panic, nor
// allocate past a bound of its size.
func FuzzReader(f *testing.F) {
	for _, name := range capturedFiles {
		f.Add(withoutChecksums(f, name)[len(Magic):])
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		r, err := NewReader(bytes.NewReader(append(append([]byte(nil), Magic...), data...)))
		if err != nil {
			t.Fatal(err)
		}
		for range 10000 {
			if _, err := r.Next(); err != nil {
				break
			}
		}
		runtime.ReadMemStats(&after)
		// The reader's buffer, then the values: at most what the rows
		// bound allows (32 bytes a value, a value's bitmap bit at least),
		// JSON's text 16 times its bytes.
		if alloc := after.TotalAlloc - before.TotalAlloc; alloc > uint64(2048*len(data)+(4<<20)) {
			t.Fatalf("%d bytes allocated for %d", alloc, len(data))
		}
	})
}
