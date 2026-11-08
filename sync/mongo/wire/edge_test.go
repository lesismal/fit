package mongowire

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

// doc1 is a document of one element of type t whose value is data.
func doc1(t byte, data []byte) Raw {
	b := []byte{0, 0, 0, 0, t, 'k', 0}
	b = append(b, data...)
	b = append(b, 0)
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	return b
}

func le32(n int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(n)) }

func TestMalformedValues(t *testing.T) {
	str := func(s string) []byte { return appendString(nil, s) }
	cat := func(bs ...[]byte) []byte {
		var o []byte
		for _, b := range bs {
			o = append(o, b...)
		}
		return o
	}
	inner := mustMarshal(t, D{{"x", int32(1)}})
	for name, c := range map[string]struct {
		t    byte
		data []byte
	}{
		"bool 2":         {TypeBool, []byte{2}},
		"bool cut":       {TypeBool, nil},
		"string cut":     {TypeString, []byte{1, 0}},
		"string len 0":   {TypeString, le32(0)},
		"string len big": {TypeString, cat(le32(50), []byte("ab"))},
		"string no nul":  {TypeString, cat(le32(2), []byte("ab"))},
		"binary cut":     {TypeBinary, []byte{1, 0}},
		"binary len":     {TypeBinary, cat(le32(-1), []byte{0})},
		"old binary":     {TypeBinary, cat(le32(5), []byte{2}, le32(9), []byte{1})},
		"regex pattern":  {TypeRegex, []byte("abc")},
		"regex options":  {TypeRegex, []byte("abc\x00im")},
		"dbpointer str":  {TypeDBPointer, le32(0)},
		"dbpointer cut":  {TypeDBPointer, cat(str("ns"), []byte{1, 2})},
		"cws cut":        {TypeCodeWithScope, []byte{1}},
		"cws len":        {TypeCodeWithScope, cat(le32(3), str("x"), inner)},
		"cws str":        {TypeCodeWithScope, cat(le32(4+4+5), le32(99), []byte{0}, make([]byte, 4))},
		"cws doc":        {TypeCodeWithScope, cat(le32(4+6+5), str("x"), []byte{9, 0, 0, 0, 0})},
		"cws parts":      {TypeCodeWithScope, cat(le32(4+6+len(inner)+1), str("x"), inner, []byte{0})},
		"int64 cut":      {TypeInt64, []byte{1, 2}},
		"unknown type":   {0x42, nil},
		"doc nested bad": {TypeDocument, []byte{5, 0, 0, 0, 1}},
		"doc short":      {TypeDocument, []byte{4, 0, 0, 0}},
		"doc elems":      {TypeDocument, []byte{8, 0, 0, 0, TypeInt32, 'a', 0, 0}},
		"doc key no nul": {TypeDocument, []byte{7, 0, 0, 0, TypeNull, 'a', 0}},
	} {
		r := doc1(c.t, c.data)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: valid", name)
		}
		if _, err := r.Value(); err == nil {
			t.Errorf("%s: read", name)
		}
		if r.Keys() != nil {
			t.Errorf("%s: keys", name)
		}
	}
	for _, b := range [][]byte{nil, {9, 0, 0, 0, 0}, {5, 0, 0, 0, 1}} {
		if Raw(b).Each(func(string, RawValue) error { return nil }) == nil {
			t.Errorf("%x: walked", b)
		}
	}
	if Raw(doc1(TypeNull, nil)).Validate() != nil || Raw(append(doc1(TypeNull, nil), 0)).Validate() == nil {
		t.Error("trailing bytes")
	}
	if _, err := (RawValue{Type: TypeInt32, Data: make([]byte, 5)}).Value(); err == nil {
		t.Error("a value with bytes after it")
	}
	if s := Raw([]byte{1}).String(); !strings.HasPrefix(s, "<") {
		t.Error(s)
	}
	if s := mustMarshal(t, D{{"n", math.NaN()}}).String(); !strings.HasPrefix(s, "<") {
		t.Error(s)
	}
}

func TestAccessors(t *testing.T) {
	r := allTypes(t)
	get := func(k string) RawValue { v, _ := r.Lookup(k); return v }
	if _, ok := r.Lookup("missing"); ok {
		t.Error("missing found")
	}
	if n, ok := get("int32").Int(); !ok || n != -7 {
		t.Error("int32")
	}
	if n, ok := get("int64").Int(); !ok || n != math.MinInt64 {
		t.Error("int64")
	}
	if _, ok := get("double").Int(); ok {
		t.Error("1.5 an int")
	}
	if n, ok := (RawValue{TypeDouble, binary.LittleEndian.AppendUint64(nil, math.Float64bits(3))}).Int(); !ok || n != 3 {
		t.Error("3.0")
	}
	if _, ok := get("string").Int(); ok {
		t.Error("string an int")
	}
	if f, ok := get("int32").Float(); !ok || f != -7 {
		t.Error("float of int")
	}
	if _, ok := get("string").Float(); ok {
		t.Error("float of string")
	}
	if b, ok := get("true").Bool(); !ok || !b {
		t.Error("bool")
	}
	if _, ok := get("string").Bool(); ok {
		t.Error("bool of string")
	}
	for k, want := range map[string]bool{"true": true, "false": false, "int32": true, "null": false, "undefined": false, "string": true, "double": true} {
		if get(k).Truthy() != want {
			t.Errorf("truthy %s", k)
		}
	}
	if (RawValue{}).Truthy() {
		t.Error("zero truthy")
	}
	if _, ok := get("oid").ObjectID(); !ok {
		t.Error("oid")
	}
	if _, ok := get("string").ObjectID(); ok {
		t.Error("oid of string")
	}
	if _, ok := get("date").Time(); !ok {
		t.Error("time")
	}
	if _, ok := get("string").Time(); ok {
		t.Error("time of string")
	}
	if ts, ok := get("ts").Timestamp(); !ok || ts.I != 3 {
		t.Error("ts")
	}
	if _, ok := get("string").Timestamp(); ok {
		t.Error("ts of string")
	}
	if b, ok := get("oldbin").Binary(); !ok || string(b.Data) != "old" {
		t.Error("old binary")
	}
	if _, ok := get("string").Binary(); ok {
		t.Error("binary of string")
	}
	if get("string").Document() != nil || get("doc").Document() == nil {
		t.Error("document")
	}
	if get("doc").Array() != nil || len(get("array").Array()) != 4 {
		t.Error("array")
	}
	if _, ok := get("int32").StringValue(); ok {
		t.Error("string of int")
	}
	for _, s := range []string{"", "zz", "65a1b2c3d4e5f60718293a4g"} {
		if _, err := ParseObjectID(s); err == nil {
			t.Errorf("oid %q", s)
		}
	}
	// A coefficient past 34 digits reads as zero.
	if s := (Decimal128{H: 0x3041ED09BEAD87C0, L: math.MaxUint64}).String(); s != "0" {
		t.Error(s)
	}
	for _, bad := range []string{strings.Repeat("1", 7001), "1x"} {
		if _, err := ParseDecimal128(bad); err == nil {
			t.Errorf("%.10s read", bad)
		}
	}
}

func TestTaggedAndJSONEdges(t *testing.T) {
	m := func(fs ...fitsync.Field) fitsync.Value { return fitsync.Map(fs...) }
	f := func(n string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: n, Value: v} }
	s, i := fitsync.String, fitsync.Int
	// Maps that look like tagged values but are not are written as documents.
	for _, v := range []fitsync.Value{
		m(f("$oid", s("zz"))),
		m(f("$oid", i(1))),
		m(f("$timestamp", m(f("t", i(-1)), f("i", i(1))))),
		m(f("$timestamp", m(f("t", fitsync.Uint(math.MaxUint64)), f("i", i(1))))),
		m(f("$timestamp", m(f("t", s("x")), f("i", i(1))))),
		m(f("$timestamp", m(f("i", i(1))))),
		m(f("$binary", m(f("base64", s("!!")), f("subType", s("00"))))),
		m(f("$regularExpression", m(f("pattern", s("a\x00")), f("options", s(""))))),
		m(f("$numberDecimal", s("x"))),
		m(f("$minKey", i(2))),
		m(f("$maxKey", i(2))),
		m(f("$undefined", fitsync.Bool(false))),
		m(f("$symbol", i(1))),
		m(f("$code", i(1))),
		m(f("$code", s("x")), f("$scope", i(1))),
		m(f("$code", s("x")), f("$scope", m(f("a\x00", i(1))))),
		m(f("$dbPointer", m(f("$ref", s("x")), f("$id", m(f("$oid", s("zz"))))))),
		m(f("$dbPointer", m(f("$ref", s("x")), f("$id", m(f("$minKey", i(1))))))),
		m(f("a", i(1)), f("b", i(2))),
	} {
		doc := m(f("v", v))
		r, err := MarshalValue(doc)
		if err != nil {
			if strings.Contains(err.Error(), "0 byte") {
				continue
			}
			t.Fatalf("%v: %v", v, err)
		}
		e, _ := r.Lookup("v")
		if e.Type != TypeDocument {
			t.Errorf("%v: written as type %#x", v, e.Type)
		}
	}
	if _, err := MarshalValue(m(f("$oid", s("65a1b2c3d4e5f60718293a4b")))); err == nil {
		t.Error("a tagged value as a document")
	}
	// Uint timestamps, and a uint past int64 that is still written.
	r, err := MarshalValue(m(f("ts", m(f("$timestamp", m(f("t", fitsync.Uint(5)), f("i", fitsync.Uint(1))))))))
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := r.Lookup("ts"); e.Type != TypeTimestamp {
		t.Error("uint timestamp")
	}
	// JSON edges.
	for doc, want := range map[string]byte{
		`12345678901234567890`: TypeDecimal128,
		`1e999`:                TypeDouble,
		`1.5`:                  TypeDouble,
		`[1,[2,{"a":{}}]]`:     TypeArray,
		`false`:                TypeBool,
		`null`:                 TypeNull,
		`1234567890123456789012345678901234567890`: TypeDouble,
	} {
		r, err := MarshalValue(m(f("j", fitsync.JSON([]byte(doc)))))
		if err != nil {
			t.Errorf("%s: %v", doc, err)
			continue
		}
		if e, _ := r.Lookup("j"); e.Type != want {
			t.Errorf("%s: type %#x", doc, e.Type)
		}
	}
	deep := strings.Repeat("[", maxDepth+2) + strings.Repeat("]", maxDepth+2)
	for _, bad := range []string{`1 2`, `{"a":1`, `[1,`, `{"a":}`, deep, `[{"a\u0000":1}]`, `{"a":[1}`, `]`} {
		if _, err := MarshalValue(m(f("j", fitsync.JSON([]byte(bad))))); err == nil {
			t.Errorf("%.20s: written", bad)
		}
	}
	// Values too deep, and errors inside arrays and maps.
	v := i(1)
	for range maxDepth + 2 {
		v = fitsync.Array(v)
	}
	for _, bad := range []fitsync.Value{
		m(f("deep", v)),
		m(f("a", fitsync.Array(fitsync.Decimal("x")))),
		m(f("a", m(f("b", fitsync.Decimal("x"))))),
		m(f("a\x00", m())),
		m(f("a\x00", fitsync.Array())),
		m(f("a\x00", fitsync.Bytes(nil))),
	} {
		if _, err := MarshalValue(bad); err == nil {
			t.Errorf("%v written", bad)
		}
	}
	for _, d := range []D{
		{{"r", Regex{Pattern: "a\x00"}}},
		{{"r", Regex{Options: "a\x00"}}},
		{{"a", A{struct{}{}}}},
		{{"d", []D{{{"x", struct{}{}}}}}},
		{{"raw", Raw{1, 2}}},
		{{"v", fitsync.Decimal("x")}},
	} {
		if _, err := Marshal(d); err == nil {
			t.Errorf("%v written", d)
		}
	}
	if r, err := Marshal(D{{"d", []D{{{"x", 1}}}}, {"r", []Raw{mustMarshal(t, D{})}}, {"v", fitsync.Int(1)}, {"i", 5}}); err != nil || len(r.Keys()) != 4 {
		t.Error("slices of documents")
	}
	if itoa(12345) != "12345" {
		t.Error(itoa(12345))
	}
}

func TestConnEdges(t *testing.T) {
	ctx := hang(t)
	f := newFake(t)
	var n atomic.Int32
	f.set(func() {
		f.hello = func() D {
			return D{{"ismaster", true}, {"maxMessageSizeBytes", int32(4 << 20)}}
		}
		f.handle = func(name string, cmd Raw) any {
			n.Add(1)
			return D{}
		}
	})
	cn, err := Dial(ctx, f.addr, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	if !cn.Hello().IsWritablePrimary || cn.maxMsg != 4<<20 {
		t.Fatalf("hello %+v", cn.Hello())
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cn.Run(cctx, "x", D{{"ping", 1}}); err == nil || cn.Err() != nil {
		t.Errorf("canceled: %v %v", err, cn.Err())
	}
	// A large command, whose buffer is not kept.
	if _, err := cn.Run(ctx, "x", D{{"ping", 1}, {"pad", strings.Repeat("x", 5<<20)}}); err == nil {
		t.Error("larger than the server takes")
	}
	if _, err := cn.Run(ctx, "x", D{{"ping", 1}, {"pad", strings.Repeat("x", 2<<20)}}); err != nil {
		t.Fatal(err)
	}
	if cn.wbuf != nil {
		t.Error("a large buffer kept")
	}
	// A write to a connection the server closed fails.
	f.set(func() { f.handle = func(string, Raw) any { return closeConn{} } })
	_, _ = cn.Run(ctx, "x", D{{"ping", 1}})
	if _, err := cn.Run(ctx, "x", D{{"ping", 1}}); !IsNetError(err) {
		t.Errorf("after close: %v", err)
	}

	// A hello that fails, a hello that times out.
	g := newFake(t)
	g.set(func() { g.hello = func() D { return D{{"ok", 0}, {"errmsg", "no"}} } })
	if _, err := Dial(ctx, g.addr, Options{}); err == nil {
		t.Error("hello failed")
	}
	// Defaults.
	o := Options{Timeout: -1, MaxMessageBytes: 1 << 40}.withDefaults()
	if o.Timeout != 0 || o.MaxMessageBytes != 48_000_000 {
		t.Errorf("%+v", o)
	}
}

func TestPoolEdges(t *testing.T) {
	ctx := hang(t)
	f := newFake(t)
	c := newClient(t, Options{Hosts: []string{f.addr}, MaxConns: 2})
	// Two cursors' connections, both put back: the pool keeps at most
	// MaxConns, and a stale generation's are closed.
	var curs []*Cursor
	f.set(func() {
		f.handle = func(string, Raw) any {
			return D{{"cursor", D{{"id", int64(1)}, {"ns", "d.c"}, {"firstBatch", A{}}}}}
		}
	})
	for range 2 {
		cur, err := c.Cursor(ctx, "d", D{{"find", "c"}}, CursorOptions{})
		if err != nil {
			t.Fatal(err)
		}
		curs = append(curs, cur)
	}
	c.lost(c.gen)
	c.lost(c.gen - 1) // an old generation: nothing
	for _, cur := range curs {
		cur.Close(ctx)
	}
	if len(c.idle) != 0 {
		t.Errorf("%d idle of a lost generation", len(c.idle))
	}
	// A cached primary that refuses the connection is found again among
	// the hosts.
	if _, err := c.Primary(ctx); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	for _, cn := range c.idle {
		cn.Close()
	}
	c.mu.Unlock()
	if _, err := c.Primary(ctx); err != nil {
		t.Fatal(err)
	}
	// A closed client closes what is put back.
	cn, err := c.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.release(cn, nil)
	if cn.Err() == nil {
		t.Error("put back into a closed client")
	}
}

func TestDoc(t *testing.T) {
	if d := Doc("a", 1, "b", "x"); len(d) != 2 || d[1].Key != "b" || d[1].Value != "x" {
		t.Fatal(d)
	}
	for _, kv := range [][]any{{"a"}, {1, 2}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%v: no panic", kv)
				}
			}()
			Doc(kv...)
		}()
	}
}
