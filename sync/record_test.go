package fitsync

import (
	"testing"
	"unsafe"
)

// TestRecordSize keeps a Record at most 512 bytes: the largest object the
// GC allocates without a malloc header and marks by its span. Fields that
// took it to 536 cost S1 8-20% more CPU a record, in the GC's scanObject.
func TestRecordSize(t *testing.T) {
	if n := unsafe.Sizeof(Record{}); n > 512 {
		t.Errorf("a Record takes %d bytes, more than 512", n)
	}
	// An entry is a record in flight's, in the queue's pending slice.
	if n := unsafe.Sizeof(entry{}); n > 72 {
		t.Errorf("an entry takes %d bytes, more than 72", n)
	}
	// A record that has not reached a sink has no lineage or pipeline.
	if r := new(Record); r.Lineage() != "" || r.pipeline() != "" {
		t.Errorf("a new record of lineage %q, pipeline %q", r.Lineage(), r.pipeline())
	}
}
