package syncmongo

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// sinkFake is an in-process MongoDB for the sink's tests: it answers hello
// and ping, and applies update (replacement upserts) and delete statements
// by _id to documents it keeps, sent as OP_MSG document sequences.
type sinkFake struct {
	t    testing.TB
	ln   net.Listener
	addr string

	mu   sync.Mutex
	docs map[string]map[string]mongowire.Raw // db.coll, then the _id's type and bytes
	// cmds logs "update db.coll n" for each write command.
	cmds []string
	// bodies are the write commands' bodies.
	bodies []mongowire.Raw
	// action, when set, is asked about each write command: "" to run it,
	// "close" to close the connection, "wce" to run it and answer with a
	// writeConcernError, or a code to answer ok: 0 with.
	action func(name, ns string) string
	// fail, when set, is asked about each statement: a code to fail it
	// with, 0 to apply it.
	fail func(name, ns string, stmt mongowire.Raw) int32
}

func newSinkFake(t testing.TB) *sinkFake {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &sinkFake{t: t, ln: ln, addr: ln.Addr().String(), docs: map[string]map[string]mongowire.Raw{}}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *sinkFake) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *sinkFake) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// dump returns a collection's documents as text, sorted.
func (f *sinkFake) dump(ns string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, d := range f.docs[ns] {
		out = append(out, d.String())
	}
	sort.Strings(out)
	return out
}

func (f *sinkFake) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.conn(c)
	}
}

// sinkParseMsg reads an OP_MSG's body and document sequences, without
// checksums.
func sinkParseMsg(msg []byte) (body mongowire.Raw, seqs []mongowire.Sequence) {
	b := msg[20:]
	for len(b) > 0 {
		kind := b[0]
		b = b[1:]
		n := int(binary.LittleEndian.Uint32(b))
		if kind == 0 {
			body, b = mongowire.Raw(b[:n]), b[n:]
			continue
		}
		sec := b[4:n]
		b = b[n:]
		i := strings.IndexByte(string(sec), 0)
		s := mongowire.Sequence{ID: string(sec[:i])}
		sec = sec[i+1:]
		for len(sec) > 0 {
			dn := int(binary.LittleEndian.Uint32(sec))
			s.Docs = append(s.Docs, mongowire.Raw(sec[:dn]))
			sec = sec[dn:]
		}
		seqs = append(seqs, s)
	}
	return body, seqs
}

func (f *sinkFake) conn(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	for {
		var hdr [16]byte
		if _, err := io.ReadFull(rd, hdr[:]); err != nil {
			return
		}
		msg := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
		copy(msg, hdr[:])
		if _, err := io.ReadFull(rd, msg[16:]); err != nil {
			return
		}
		body, seqs := sinkParseMsg(msg)
		name := body.Keys()[0]
		var ans mongowire.D
		switch name {
		case "hello":
			ans = mongowire.Doc("isWritablePrimary", true, "maxWireVersion", int32(21))
		case "ping":
			ans = mongowire.D{}
		case "update", "delete":
			var ok bool
			if ans, ok = f.write(name, body, seqs); !ok {
				return
			}
		default:
			ans = mongowire.Doc("ok", 0.0, "code", int32(59), "errmsg", "no such command: "+name)
		}
		found := false
		for _, e := range ans {
			found = found || e.Key == "ok"
		}
		if !found {
			ans = append(ans, mongowire.E{Key: "ok", Value: 1.0})
		}
		r, err := mongowire.Marshal(ans)
		if err != nil {
			f.t.Errorf("fake: %v", err)
			return
		}
		out := binary.LittleEndian.AppendUint32(nil, uint32(16+4+1+len(r)))
		out = binary.LittleEndian.AppendUint32(out, 0)
		out = append(out, hdr[4:8]...)
		out = binary.LittleEndian.AppendUint32(out, 2013)
		out = binary.LittleEndian.AppendUint32(out, 0)
		out = append(out, 0)
		out = append(out, r...)
		if _, err := c.Write(out); err != nil {
			return
		}
	}
}

// write runs an update or a delete; false closes the connection.
func (f *sinkFake) write(name string, body mongowire.Raw, seqs []mongowire.Sequence) (mongowire.D, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	coll, _ := body.Lookup(name)
	db, _ := body.Lookup("$db")
	cs, _ := coll.StringValue()
	ds, _ := db.StringValue()
	ns := ds + "." + cs
	var stmts []mongowire.Raw
	for _, s := range seqs {
		stmts = append(stmts, s.Docs...)
	}
	f.cmds = append(f.cmds, name+" "+ns+" "+strconv.Itoa(len(stmts)))
	f.bodies = append(f.bodies, append(mongowire.Raw(nil), body...))
	act := ""
	if f.action != nil {
		act = f.action(name, ns)
	}
	switch act {
	case "close":
		return nil, false
	case "", "wce":
	default:
		code, _ := strconv.Atoi(act)
		return mongowire.Doc("ok", 0.0, "code", int32(code), "codeName", "Fake", "errmsg", "refused"), true
	}
	if f.docs[ns] == nil {
		f.docs[ns] = map[string]mongowire.Raw{}
	}
	var errs mongowire.A
	for i, st := range stmts {
		if f.fail != nil {
			if code := f.fail(name, ns, st); code != 0 {
				errs = append(errs, mongowire.Doc("index", int32(i), "code", code, "errmsg", "statement refused"))
				continue
			}
		}
		qv, _ := st.Lookup("q")
		idv, _ := qv.Document().Lookup("_id")
		id := string(idv.Type) + string(idv.Data)
		if name == "delete" {
			delete(f.docs[ns], id)
			continue
		}
		u, _ := st.Lookup("u")
		f.docs[ns][id] = append(mongowire.Raw(nil), u.Document()...)
	}
	ans := mongowire.Doc("n", int32(len(stmts)-len(errs)))
	if len(errs) > 0 {
		ans = append(ans, mongowire.E{Key: "writeErrors", Value: errs})
	}
	if act == "wce" {
		ans = append(ans, mongowire.E{Key: "writeConcernError", Value: mongowire.Doc("code", int32(64), "errmsg", "waiting for replication timed out")})
	}
	return ans, true
}
