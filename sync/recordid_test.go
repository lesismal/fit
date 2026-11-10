package fitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lesismal/fit/module"
)

func TestRecordIDs(t *testing.T) {
	pos := func(s string) Position { return Position(s) }
	a := &Record{Op: OpInsert, Tx: Tx{ID: "t", First: true}}
	b := &Record{Op: OpInsert, Tx: Tx{ID: "t", Last: true}, Position: pos(`{"n":2}`)}
	c := &Record{Op: OpInsert, Position: pos(`{"n":3}`)}
	c2 := &Record{Op: OpUpdate, Position: pos(`{"n":3}`)} // a transform's second record of c
	d := &Record{Op: OpInsert, Tx: Tx{ID: "u", First: true}}
	whole := RecordIDs([]*Record{a, b, c, c2, d})
	seen := map[string]bool{}
	for i, id := range whole {
		if len(id) != 32 || seen[id] {
			t.Fatalf("id %d %q: not 32 digits, or a repeat: %v", i, id, whole)
		}
		seen[id] = true
	}
	// The same records cut at a checkpoint keep their IDs.
	tail := RecordIDs([]*Record{c, c2, d})
	for i, id := range tail {
		if id != whole[i+2] {
			t.Errorf("record %d after a cut: %s, was %s", i+2, id, whole[i+2])
		}
	}
	if got := RecordIDs([]*Record{a, b}); got[0] != whole[0] || got[1] != whole[1] {
		t.Errorf("a batch ending at b: %v", got)
	}
	// No positions at all: by content and place, the same each time.
	x, y := RecordIDs([]*Record{a, d}), RecordIDs([]*Record{a, d})
	if x[0] != y[0] || x[1] != y[1] || x[0] == x[1] {
		t.Errorf("no positions: %v %v", x, y)
	}
	if len(RecordIDs(nil)) != 0 {
		t.Error("nil batch")
	}
}

// partitioned plays a Kafka or Pulsar source: it reads the messages of
// several partitions in the order given, each record with its origin and,
// as those sources do, a position of every partition's next message in the
// order the run met them. It returns the records and the position after
// each.
type partitioned struct {
	source string
	// origin names partition p's message m.
	origin func(p, m int) Origin
	// next is each partition's next message, in the order met.
	order []int
	next  map[int]int
}

func (s *partitioned) read(p int) *Record {
	m, ok := s.next[p]
	if !ok {
		s.order = append(s.order, p)
	}
	s.next[p] = m + 1
	pos := []byte("{")
	for i, q := range s.order {
		if i > 0 {
			pos = append(pos, ',')
		}
		pos = fmt.Appendf(pos, `"%d":%d`, q, s.next[q])
	}
	pos = append(pos, '}')
	return &Record{Source: s.source, Op: OpMessage, Origin: s.origin(p, m), Position: pos,
		After: Map(Field{Name: "p", Value: Int(int64(p))}, Field{Name: "m", Value: Int(int64(m))})}
}

// replayIDs reads the partitions in first's order, cut at a checkpoint after
// k records, then reads them again from the checkpoint in second's order
// (every partition's messages after its checkpointed one), as a restart
// does, and checks each record replayed gets the ID it had.
func replayIDs(t *testing.T, source string, origin func(p, m int) Origin) {
	t.Helper()
	first := []int{0, 1, 0, 2, 1, 1, 0, 2, 2, 0, 1, 2}
	run1 := &partitioned{source: source, origin: origin, next: map[int]int{}}
	var recs []*Record
	for _, p := range first {
		recs = append(recs, run1.read(p))
	}
	ids := map[string]string{} // partition/message -> ID
	for i, id := range RecordIDs(recs) {
		ids[fmt.Sprint(recs[i].After)] = id
	}
	// The checkpoint after the fourth record: each partition's next.
	const k = 4
	var at map[string]int
	if err := json.Unmarshal(recs[k-1].Position, &at); err != nil {
		t.Fatal(err)
	}
	// The restart meets the partitions in another order, and so writes
	// other positions.
	run2 := &partitioned{source: source, origin: origin, next: map[int]int{}}
	for _, p := range []int{2, 1, 0} {
		run2.order = append(run2.order, p)
		run2.next[p] = at[fmt.Sprint(p)]
	}
	left := map[int]int{}
	for _, p := range first[k:] {
		left[p]++
	}
	var again []*Record
	for _, p := range []int{2, 2, 1, 2, 1, 0, 1, 0, 1, 2, 0} {
		if left[p] > 0 {
			left[p]--
			again = append(again, run2.read(p))
		}
	}
	if len(again) != len(first)-k {
		t.Fatalf("%d records replayed, want %d", len(again), len(first)-k)
	}
	// Cut into batches otherwise than the first run's.
	for _, cut := range [][]*Record{again, again[:3], again[3:]} {
		for i, id := range RecordIDs(cut) {
			key := fmt.Sprint(cut[i].After)
			if was, ok := ids[key]; !ok || was != id {
				t.Errorf("%s: record %s replayed with ID %s, was %s", source, key, id, was)
			}
		}
	}
	// Without the origin, the positions alone give other IDs: the gap the
	// origin closes.
	strip := func(rs []*Record) []*Record {
		out := make([]*Record, len(rs))
		for i, r := range rs {
			c := *r
			c.Origin = Origin{}
			out[i] = &c
		}
		return out
	}
	before, after := RecordIDs(strip(recs)), RecordIDs(strip(again))
	same := 0
	for i, id := range after {
		for j, was := range before {
			if was == id && fmt.Sprint(recs[j].After) == fmt.Sprint(again[i].After) {
				same++
			}
		}
	}
	if same == len(after) {
		t.Errorf("%s: positions alone kept every ID, so the test shows nothing", source)
	}
}

func TestRecordIDsReplayKafka(t *testing.T) {
	replayIDs(t, "kafka", func(p, m int) Origin {
		return Origin{Stream: "orders", N: [4]uint64{uint64(p), uint64(100 + m), 0}}
	})
}

func TestRecordIDsReplayPulsar(t *testing.T) {
	replayIDs(t, "pulsar", func(p, m int) Origin {
		// Ledger and entry, the message's place in its batch.
		return Origin{Stream: fmt.Sprintf("persistent://public/default/orders-partition-%d", p),
			N: [4]uint64{uint64(7 + p), uint64(m / 2), uint64(m % 2), 0}}
	})
}

func TestRecordIDsOrigin(t *testing.T) {
	o := Origin{Stream: "s", N: [4]uint64{1, 2}}
	r := &Record{Source: "kafka", Origin: o, Position: Position(`{"a":1}`)}
	same := &Record{Source: "kafka", Origin: o, Position: Position(`{"b":2}`)}
	other := &Record{Source: "pulsar", Origin: o}
	sub := r.Clone()
	sub.sub = 1
	plain := &Record{Op: OpInsert}
	got := RecordIDs([]*Record{r, sub, other, plain})
	if again := RecordIDs([]*Record{same}); again[0] != got[0] {
		t.Errorf("one origin at another position: %s, was %s", again[0], got[0])
	}
	if got[0] == got[1] || got[0] == got[2] || got[1] == got[2] {
		t.Errorf("records of other places or sources share an ID: %v", got)
	}
	// A record without an origin is placed by the position before it, as
	// before.
	if want := recordID(sub.Position, "+", 2); got[3] != want {
		t.Errorf("a record without an origin: %s, want %s", got[3], want)
	}
	if b := plain.AppendOrigin([]byte("x")); string(b) != "x" {
		t.Errorf("no origin appended %q", b)
	}
	if b := r.AppendOrigin(nil); len(b) == 0 {
		t.Error("an origin appended nothing")
	}
	// Another pipeline's record of the same origin has an ID of its own:
	// two Kafka clusters' topics of one name feeding one stream.
	piped := same.Clone()
	piped.run = &recordRun{pipeline: "other"}
	if again := RecordIDs([]*Record{piped}); again[0] == got[0] {
		t.Errorf("two pipelines share an origin's ID %s", got[0])
	}
	if n := testing.AllocsPerRun(100, func() { _ = originID(r) }); n > 1 {
		t.Errorf("originID: %v allocations, want at most 1", n)
	}
}

// originSource is generate with an origin on each record: its number.
type originSource struct {
	Records int64 `json:"records"`
}

func (s *originSource) Run(ctx context.Context, r *Run) error {
	emit := r.Emit
	r.Emit = func(rec *Record) error {
		rec.Origin = Origin{Stream: "gen", N: [4]uint64{uint64(keyID(rec))}}
		return emit(rec)
	}
	return (&Generate{Records: s.Records}).Run(ctx, r)
}

func init() {
	module.Register(module.Info{ID: Sources + ".origin", New: func() any { return new(originSource) }, Doc: "Tests'."})
}

// A transform's records of one source record share its origin, and get IDs
// of their own by their place.
func TestRecordIDsSplit(t *testing.T) {
	s, sn := memStore(t)
	rt := NewRuntime(Options{})
	defer rt.Close(context.Background())
	if err := rt.Apply(syncConfig(t, t.TempDir(), fmt.Sprintf(`{"name": "p", "mode": "once",
		"source": {"module": "origin", "params": {"records": 10}},
		"transforms": [{"module": "test", "params": {"split": 2}}],
		"sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_wait": "1ms"}}]}`, sn))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the end", func() bool { return state(rt, "p").State == "finished" })
	recs := s.Records()
	if len(recs) != 15 {
		t.Fatalf("%d records", len(recs))
	}
	seen := map[string]bool{}
	for i, id := range RecordIDs(recs) {
		if seen[id] {
			t.Fatalf("record %d (%d): a repeated ID", i, keyID(recs[i]))
		}
		seen[id] = true
	}
	// One record at a time, as another cut of batches would hand them.
	for i, r := range recs {
		if one := RecordIDs([]*Record{r}); !seen[one[0]] {
			t.Errorf("record %d alone: another ID", i)
		}
	}
}
