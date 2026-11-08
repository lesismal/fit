package fitsync

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// batchTransform is a BatchTransform that splits records whose id is a
// multiple of split, fails those equal to fail, and fails the whole batch
// when one's id is all, or emits out of order when it is back.
type batchTransform struct {
	split, fail, all, back int64
	calls                  int
}

func (b *batchTransform) Apply(r *Record, emit func(*Record)) error {
	var err error
	if e := b.ApplyBatch([]*Record{r}, func(_ int, o *Record) { emit(o) }, func(_ int, e error) { err = e }); e != nil {
		return e
	}
	return err
}

func (b *batchTransform) ApplyBatch(in []*Record, emit func(int, *Record), fail func(int, error)) error {
	b.calls++
	for _, r := range in {
		if keyID(r) == b.all {
			return errors.New("the plugin trapped")
		}
	}
	for i, r := range in {
		switch id := keyID(r); {
		case id == b.fail:
			emit(i, r) // dropped, as the record fails
			fail(i, fmt.Errorf("record %d refused", id))
		case id == b.back && i > 0:
			emit(i, r)
			emit(i-1, r)
		case b.split > 0 && id%b.split == 0:
			emit(i, r)
			c := r.Clone()
			c.Namespace += ".copy"
			emit(i, c)
		default:
			emit(i, r)
		}
	}
	return nil
}

// TestStages checks a batch transform beside per-record ones: what each
// source record becomes stays with it, in order, and a failure fails the
// source record whatever it was split into, or the whole batch.
func TestStages(t *testing.T) {
	src := make([]*Record, 8)
	for i := range src {
		src[i] = GenerateRecord("ns", int64(i), 0, 1)
		src[i].seq = uint64(100 + i)
	}
	run := func(ts ...Transform) (string, string) {
		s := newStages(ts)
		s.run(src)
		var outs, errs []string
		for _, o := range s.outs {
			outs = append(outs, fmt.Sprintf("%d:%d%s", o.src, keyID(o.rec), strings.TrimPrefix(o.rec.Namespace, "ns")))
			if o.rec.seq != src[o.src].seq {
				t.Errorf("record %d has seq %d", o.src, o.rec.seq)
			}
		}
		for i, err := range s.errs {
			if err != nil {
				errs = append(errs, fmt.Sprintf("%d@%d:%v", i, s.where[i], err))
			}
		}
		s.done()
		return strings.Join(outs, " "), strings.Join(errs, " ")
	}
	split := &testTransform{Split: 3}
	bt := &batchTransform{split: 2, fail: 5, all: -1, back: -1}
	outs, errs := run(split, bt, &testTransform{Fail: 7})
	if outs != "0:0 0:0.copy 0:0.copy 0:0.copy.copy 1:1 2:2 2:2.copy 3:3 3:3.copy 4:4 4:4.copy 6:6 6:6.copy 6:6.copy 6:6.copy.copy" ||
		errs != "5@1:record 5 refused 7@2:record 7 refused" || bt.calls != 1 {
		t.Errorf("%s\n%s, %d calls", outs, errs, bt.calls)
	}
	_, errs = run(&batchTransform{all: 4, fail: -1, back: -1})
	if strings.Count(errs, "in a batch of 8 records: the plugin trapped") != 8 {
		t.Error(errs)
	}
	_, errs = run(&batchTransform{all: -1, fail: -1, back: 3})
	if strings.Count(errs, errEmitOrder.Error()) != 8 {
		t.Error(errs)
	}
	// After a record failed in an earlier transform, the batch transform
	// does not see it.
	outs, errs = run(&testTransform{Fail: 2}, &batchTransform{all: 2, fail: -1, back: -1})
	if strings.Contains(outs, "2:") || errs != "2@0:record 2 refused" {
		t.Errorf("%s\n%s", outs, errs)
	}
}
