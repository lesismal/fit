package syncmongo

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// srcFake is an in-process MongoDB for the source's tests: it speaks OP_MSG,
// keeps collections of documents and a log of change events, and answers
// hello, find, aggregate (a change stream, or a $sample), getMore,
// killCursors, count, listDatabases and listCollections from them. Its
// override, when it answers a command, answers in its place.
type srcFake struct {
	t    testing.TB
	ln   net.Listener
	addr string

	// omu serializes the override's calls, from several connections.
	omu      sync.Mutex
	mu       sync.Mutex
	hello    mongowire.D
	colls    map[string][]mongowire.D // by db.collection, in _id order
	events   []mongowire.D
	curs     map[int64]*srcCursor
	nextID   int64
	cmds     []mongowire.Raw
	override func(name string, cmd mongowire.Raw) any
}

// srcCursor is a cursor of the fake: next returns its next batch, the
// post-batch token (nil for none) and whether it goes on.
type srcCursor struct {
	ns   string
	next func(n int) (mongowire.A, any, bool)
}

// srcClose closes the connection, as an answer.
type srcClose struct{}

func newSrcFake(t testing.TB) *srcFake {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &srcFake{t: t, ln: ln, addr: ln.Addr().String(), colls: map[string][]mongowire.D{}, curs: map[int64]*srcCursor{},
		hello: mongowire.Doc("isWritablePrimary", true, "maxWireVersion", int32(21), "setName", "rs0")}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *srcFake) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// emit adds change events to the log, each with its token, {_data: "<n>"}.
func (f *srcFake) emit(evs ...mongowire.D) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range evs {
		tok := mongowire.Doc("_data", fmt.Sprintf("%08d", len(f.events)+1))
		f.events = append(f.events, append(mongowire.Doc("_id", tok), ev...))
	}
}

// commands returns the commands sent, but hello.
func (f *srcFake) commands() []mongowire.Raw {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

func (f *srcFake) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.conn(c)
	}
}

func (f *srcFake) conn(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	for {
		var hdr [16]byte
		if _, err := io.ReadFull(rd, hdr[:]); err != nil {
			return
		}
		msg := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
		if _, err := io.ReadFull(rd, msg[16:]); err != nil {
			return
		}
		// flags, then a kind 0 section: the body; the source sends no
		// sequences.
		body := mongowire.Raw(msg[21:])
		body = body[:binary.LittleEndian.Uint32(body)]
		ans := f.answer(body)
		if _, ok := ans.(srcClose); ok {
			return
		}
		d := ans.(mongowire.D)
		if !srcHas(d, "ok") {
			d = append(d, mongowire.E{Key: "ok", Value: 1.0})
		}
		r, err := mongowire.Marshal(d)
		if err != nil {
			f.t.Errorf("fake: %v", err)
			return
		}
		out := binary.LittleEndian.AppendUint32(nil, uint32(16+4+1+len(r)))
		out = binary.LittleEndian.AppendUint32(out, 0)
		out = append(out, hdr[4:8]...)
		out = binary.LittleEndian.AppendUint32(out, 2013)
		out = binary.LittleEndian.AppendUint32(out, 0)
		out = append(append(out, 0), r...)
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}

func srcHas(d mongowire.D, k string) bool {
	return slices.ContainsFunc(d, func(e mongowire.E) bool { return e.Key == k })
}

func srcErr(code int32, msg string) mongowire.D {
	return mongowire.Doc("ok", 0.0, "code", code, "errmsg", msg)
}

func (f *srcFake) answer(cmd mongowire.Raw) any {
	name := cmd.Keys()[0]
	f.mu.Lock()
	if _, ok := cmd.Lookup("helloOk"); ok && name == "hello" {
		defer f.mu.Unlock()
		return f.hello // a connection's handshake
	}
	f.cmds = append(f.cmds, append(mongowire.Raw(nil), cmd...))
	over := f.override
	f.mu.Unlock()
	if over != nil {
		f.omu.Lock()
		a := over(name, cmd)
		f.omu.Unlock()
		if a != nil {
			return a
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	db, _ := rawField(cmd, "$db").StringValue()
	switch name {
	case "ping":
		return mongowire.Doc()
	case "hello":
		return f.hello
	case "getMore":
		id, _ := rawField(cmd, "getMore").Int()
		cur := f.curs[id]
		if cur == nil {
			return srcErr(43, "cursor not found")
		}
		n, _ := rawField(cmd, "batchSize").Int()
		return f.batch(id, cur, int(n), "nextBatch")
	case "killCursors":
		for _, v := range rawField(cmd, "cursors").Array() {
			id, _ := v.Int()
			delete(f.curs, id)
		}
		return mongowire.Doc()
	case "count":
		coll, _ := rawField(cmd, "count").StringValue()
		return mongowire.Doc("n", int64(len(f.colls[db+"."+coll])))
	case "listDatabases":
		var dbs mongowire.A
		seen := map[string]bool{"admin": true}
		dbs = append(dbs, mongowire.Doc("name", "admin"))
		for _, ns := range srcSortedKeys(f.colls) {
			d, _ := nsParts(ns)
			if !seen[d] {
				seen[d] = true
				dbs = append(dbs, mongowire.Doc("name", d))
			}
		}
		return mongowire.Doc("databases", dbs)
	case "listCollections":
		var names mongowire.A
		for _, ns := range srcSortedKeys(f.colls) {
			if d, c := nsParts(ns); d == db {
				names = append(names, mongowire.Doc("name", c, "type", "collection"))
			}
		}
		names = append(names, mongowire.Doc("name", "system.views", "type", "collection"))
		return f.open(db+".$cmd.listCollections", srcSlice(names), 0)
	case "find":
		coll, _ := rawField(cmd, "find").StringValue()
		lo := srcBound(rawField(cmd, "min"))
		hi := srcBound(rawField(cmd, "max"))
		var docs mongowire.A
		for _, d := range f.colls[db+"."+coll] {
			id := srcID(d)
			if (lo == nil || srcCompare(id, *lo) >= 0) && (hi == nil || srcCompare(id, *hi) < 0) {
				docs = append(docs, d)
			}
		}
		n, _ := rawField(cmd, "batchSize").Int()
		return f.open(db+"."+coll, srcSlice(docs), int(n))
	case "aggregate":
		coll, _ := rawField(cmd, "aggregate").StringValue()
		stage := rawField(cmd, "pipeline").Array()[0].Document()
		if s, ok := stage.Lookup("$sample"); ok {
			size, _ := rawField(s.Document(), "size").Int()
			var ids mongowire.A
			for _, d := range f.colls[db+"."+coll] {
				ids = append(ids, mongowire.Doc("_id", d[0].Value))
			}
			sample := ids
			if int(size) < len(ids) {
				sample = nil
				for i := range int(size) {
					sample = append(sample, ids[i*len(ids)/int(size)])
				}
			}
			return f.open(db+"."+coll, srcSlice(sample), 0)
		}
		cs := rawField(stage, "$changeStream").Document()
		at := len(f.events)
		if tok, ok := cs.Lookup("resumeAfter"); ok {
			s, _ := rawField(tok.Document(), "_data").StringValue()
			n, err := strconv.Atoi(s)
			if err != nil {
				return srcErr(286, "resume point may no longer be in the oplog")
			}
			at = n
		}
		n, _ := rawField(rawField(cmd, "cursor").Document(), "batchSize").Int()
		return f.open(db+"."+coll, func(k int) (mongowire.A, any, bool) {
			var out mongowire.A
			for at < len(f.events) && (k <= 0 || len(out) < k) {
				out = append(out, f.events[at])
				at++
			}
			return out, mongowire.Doc("_data", fmt.Sprintf("%08d", at)), true
		}, int(n))
	}
	return srcErr(59, "no such command: "+name)
}

// open makes a cursor of next and answers its first batch of n (0: all).
func (f *srcFake) open(ns string, next func(n int) (mongowire.A, any, bool), n int) mongowire.D {
	f.nextID++
	cur := &srcCursor{ns: ns, next: next}
	f.curs[f.nextID] = cur
	return f.batch(f.nextID, cur, n, "firstBatch")
}

func (f *srcFake) batch(id int64, cur *srcCursor, n int, field string) mongowire.D {
	docs, pbrt, more := cur.next(n)
	if !more {
		delete(f.curs, id)
		id = 0
	}
	if docs == nil {
		docs = mongowire.A{}
	}
	c := mongowire.Doc("id", id, "ns", cur.ns, field, docs)
	if pbrt != nil {
		c = append(c, mongowire.E{Key: "postBatchResumeToken", Value: pbrt})
	}
	return mongowire.Doc("cursor", c)
}

// srcSlice is a cursor's next over docs.
func srcSlice(docs mongowire.A) func(int) (mongowire.A, any, bool) {
	return func(n int) (mongowire.A, any, bool) {
		if n <= 0 || n > len(docs) {
			n = len(docs)
		}
		out := docs[:n]
		docs = docs[n:]
		return out, nil, len(docs) > 0
	}
}

func srcSortedKeys(m map[string][]mongowire.D) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

// srcBound reads a find's min or max, {_id: v}.
func srcBound(v mongowire.RawValue) *fitsync.Value {
	if v.Type != mongowire.TypeDocument {
		return nil
	}
	id, err := rawField(v.Document(), "_id").Value()
	if err != nil {
		return nil
	}
	return &id
}

func srcID(d mongowire.D) fitsync.Value {
	r, _ := mongowire.Marshal(mongowire.Doc("_id", d[0].Value))
	v, _ := rawField(r, "_id").Value()
	return v
}

// srcCompare orders ints before strings, as BSON does numbers and strings.
func srcCompare(a, b fitsync.Value) int {
	rank := func(v fitsync.Value) int {
		if v.Kind() == fitsync.KindString {
			return 1
		}
		return 0
	}
	if ra, rb := rank(a), rank(b); ra != rb {
		return ra - rb
	}
	if a.Kind() == fitsync.KindString {
		switch {
		case a.Text() < b.Text():
			return -1
		case a.Text() > b.Text():
			return 1
		}
		return 0
	}
	switch {
	case a.AsInt() < b.AsInt():
		return -1
	case a.AsInt() > b.AsInt():
		return 1
	}
	return 0
}
