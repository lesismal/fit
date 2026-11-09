package syncmysql

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// gtidSet is a MySQL GTID set: by source, a UUID and, for MySQL 8.3's
// tagged GTIDs, ":tag", the intervals of transaction numbers, sorted and
// apart, each [start, end).
type gtidSet map[string][][2]int64

// parseGTIDSet reads a set as MySQL writes it: uuid:1-5:7,uuid:tag:1-3,
// with whitespace (MySQL breaks lines after the commas) ignored. A tag
// applies to the intervals after it.
func parseGTIDSet(s string) (gtidSet, error) {
	g := gtidSet{}
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return g, nil
	}
	for _, part := range strings.Split(s, ",") {
		fields := strings.Split(part, ":")
		uuid := strings.ToLower(fields[0])
		if b, err := hex.DecodeString(strings.ReplaceAll(uuid, "-", "")); err != nil || len(b) != 16 || len(uuid) != 36 {
			return nil, fmt.Errorf("mysql: %q is not a GTID set: %q is not a UUID", s, fields[0])
		}
		sid := uuid
		for _, f := range fields[1:] {
			if f == "" || f[0] < '0' || f[0] > '9' {
				if !validTag(f) {
					return nil, fmt.Errorf("mysql: %q is not a GTID set: a tag %q", s, f)
				}
				sid = uuid + ":" + strings.ToLower(f)
				continue
			}
			lo, hi, isRange := strings.Cut(f, "-")
			a, err1 := strconv.ParseInt(lo, 10, 64)
			b := a
			var err2 error
			if isRange {
				b, err2 = strconv.ParseInt(hi, 10, 64)
			}
			if err1 != nil || err2 != nil || a < 1 || b < a || b == 1<<63-1 {
				return nil, fmt.Errorf("mysql: %q is not a GTID set: an interval %q", s, f)
			}
			g.addRange(sid, a, b+1)
		}
	}
	return g, nil
}

// validTag reports whether f is a GTID's tag: a letter or _, then
// letters, digits or _, 32 at most.
func validTag(f string) bool {
	if f == "" || len(f) > 32 {
		return false
	}
	for i, c := range strings.ToLower(f) {
		if !(c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// add adds transaction gno of source sid.
func (g gtidSet) add(sid string, gno int64) { g.addRange(sid, gno, gno+1) }

// addRange adds [a, b) to sid's intervals, merging those it touches.
func (g gtidSet) addRange(sid string, a, b int64) {
	iv := g[sid]
	i := 0
	for i < len(iv) && iv[i][1] < a {
		i++
	}
	j := i
	for j < len(iv) && iv[j][0] <= b {
		a, b = min(a, iv[j][0]), max(b, iv[j][1])
		j++
	}
	g[sid] = slices.Replace(iv, i, j, [2]int64{a, b})
}

func (g gtidSet) sids() []string {
	var out []string
	for sid, iv := range g {
		if len(iv) > 0 {
			out = append(out, sid)
		}
	}
	slices.Sort(out)
	return out
}

// String is the set as MySQL reads it.
func (g gtidSet) String() string {
	var b strings.Builder
	for _, sid := range g.sids() {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(sid)
		for _, iv := range g[sid] {
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(iv[0], 10))
			if iv[1] > iv[0]+1 {
				b.WriteByte('-')
				b.WriteString(strconv.FormatInt(iv[1]-1, 10))
			}
		}
	}
	return b.String()
}

// tagged reports whether the set holds tagged GTIDs.
func (g gtidSet) tagged() bool {
	for sid := range g {
		if strings.Contains(sid, ":") {
			return true
		}
	}
	return false
}

// encode is the set as COM_BINLOG_DUMP_GTID sends it, for a set without
// tags: the count of sources, then each one's UUID, count of intervals and
// intervals.
func (g gtidSet) encode() []byte {
	sids := g.sids()
	b := binary.LittleEndian.AppendUint64(nil, uint64(len(sids)))
	for _, sid := range sids {
		u, _ := hex.DecodeString(strings.ReplaceAll(sid, "-", ""))
		b = append(b, u...)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(g[sid])))
		for _, iv := range g[sid] {
			b = binary.LittleEndian.AppendUint64(b, uint64(iv[0]))
			b = binary.LittleEndian.AppendUint64(b, uint64(iv[1]))
		}
	}
	return b
}

// mariaPos is a MariaDB GTID position: the last GTID of each replication
// domain, domain-server-sequence, as gtid_binlog_pos has it.
type mariaPos map[uint32]string

func parseMariaPos(s string) (mariaPos, error) {
	p := mariaPos{}
	for _, part := range strings.Split(strings.TrimSpace(s), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		f := strings.Split(part, "-")
		var n [3]uint64
		ok := len(f) == 3
		for i := range f {
			var err error
			if n[i], err = strconv.ParseUint(f[i], 10, 64); err != nil || i < 2 && n[i] > 1<<32-1 {
				ok = false
			}
		}
		if !ok {
			return nil, fmt.Errorf("mysql: %q is not a MariaDB GTID position", s)
		}
		p[uint32(n[0])] = part
	}
	return p, nil
}

func (p mariaPos) String() string {
	var ds []uint32
	for d := range p {
		ds = append(ds, d)
	}
	slices.Sort(ds)
	var parts []string
	for _, d := range ds {
		parts = append(parts, p[d])
	}
	return strings.Join(parts, ",")
}
