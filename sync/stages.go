package fitsync

import (
	"errors"
	"fmt"
	"slices"
)

// staged is a record on its way through the transforms, and the index, in
// the dispatcher's batch, of the source record it comes from.
type staged struct {
	rec *Record
	src int
}

// stages puts the dispatcher's batches through the transforms, one
// transform at a time: a Transform record by record, a BatchTransform with
// the whole batch at once. Its buffers and callbacks are made once.
type stages struct {
	ts []Transform
	// batch is the source records of the run under way; outs what they are
	// after the transforms so far, in order of their source; next what the
	// current transform makes of them.
	batch      []*Record
	outs, next []staged
	in         []*Record
	// errs is each source record's failure, and where the transform's
	// index; failedNow marks the records the current batch transform failed.
	errs      []error
	where     []int
	failedNow []bool
	// cur is the source record Apply has; stage the transform ApplyBatch
	// runs, and last the index it emitted last.
	cur, stage, last int
	bad              bool
	emit             func(*Record)
	emitAt           func(int, *Record)
	failAt           func(int, error)
}

var errEmitOrder = errors.New("a batch transform emitted records out of order")

func newStages(ts []Transform) *stages {
	c := &stages{ts: ts}
	c.emit = func(o *Record) {
		o.seq = c.batch[c.cur].seq
		c.next = append(c.next, staged{o, c.cur})
	}
	c.emitAt = func(i int, o *Record) {
		if i < c.last || i >= len(c.outs) {
			c.bad = true
			return
		}
		c.last = i
		src := c.outs[i].src
		o.seq = c.batch[src].seq
		c.next = append(c.next, staged{o, src})
	}
	c.failAt = func(i int, err error) {
		if i < 0 || i >= len(c.outs) {
			c.bad = true
			return
		}
		src := c.outs[i].src
		c.failedNow[src] = true
		c.fail(src, err, c.stage)
	}
	return c
}

// fail records that source record src failed at transform t, unless it had.
func (c *stages) fail(src int, err error, t int) {
	if c.errs[src] == nil {
		c.errs[src], c.where[src] = err, t
	}
}

// run puts batch through the transforms: c.outs then holds what each
// source record became, in order, and c.errs which of them failed.
func (c *stages) run(batch []*Record) {
	c.batch = batch
	c.outs = c.outs[:0]
	c.errs = resize(c.errs, len(batch))
	c.where = resize(c.where, len(batch))
	for i, r := range batch {
		c.outs = append(c.outs, staged{r, i})
	}
	for t, tr := range c.ts {
		if len(c.outs) == 0 {
			break
		}
		c.next = c.next[:0]
		if bt, ok := tr.(BatchTransform); ok {
			c.applyBatch(t, bt)
		} else {
			c.apply(t, tr)
		}
		clear(c.outs)
		c.outs, c.next = c.next, c.outs
	}
}

// done lets go of the records of the last run.
func (c *stages) done() {
	clear(c.outs)
	c.outs, c.batch = c.outs[:0], nil
}

// apply puts c.outs through tr, record by record.
func (c *stages) apply(t int, tr Transform) {
	for k := 0; k < len(c.outs); {
		src := c.outs[k].src
		c.cur = src
		start := len(c.next)
		for ; k < len(c.outs) && c.outs[k].src == src; k++ {
			if err := tr.Apply(c.outs[k].rec, c.emit); err != nil {
				clear(c.next[start:])
				c.next = c.next[:start]
				c.fail(src, err, t)
				for k < len(c.outs) && c.outs[k].src == src {
					k++
				}
				break
			}
		}
	}
}

// applyBatch puts c.outs through bt at once.
func (c *stages) applyBatch(t int, bt BatchTransform) {
	c.in = c.in[:0]
	for _, s := range c.outs {
		c.in = append(c.in, s.rec)
	}
	c.failedNow = resize(c.failedNow, len(c.batch))
	c.stage, c.last, c.bad = t, 0, false
	err := bt.ApplyBatch(c.in, c.emitAt, c.failAt)
	clear(c.in)
	if err == nil && c.bad {
		err = errEmitOrder
	}
	if err != nil {
		err = fmt.Errorf("in a batch of %d records: %w", len(c.outs), err)
		for _, s := range c.outs {
			c.fail(s.src, err, t)
		}
		clear(c.next)
		c.next = c.next[:0]
		return
	}
	// What the records it failed made before failing is dropped.
	keep := c.next[:0]
	for _, s := range c.next {
		if !c.failedNow[s.src] {
			keep = append(keep, s)
		}
	}
	clear(c.next[len(keep):])
	c.next = keep
}

// resize returns s with n zero elements, reusing its array when it can.
func resize[T any](s []T, n int) []T {
	s = slices.Grow(s[:0], n)[:n]
	clear(s)
	return s
}
