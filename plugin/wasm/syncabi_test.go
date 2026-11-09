package wasm

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
)

// The sync ABI as plugins a compiler would not build use it: hand-made
// modules that call fit_sync's host functions with what they like.

// The imports of every module abiModule makes, by index.
const (
	impInput = iota
	impRecordEmit
	impFail
	impOutput
	impCheckpoint
	impHTTPRequest
	impHTTPResponse
	impHTTPHeader
	impSleep
	impLog
)

var abiImports = []wasmImport{
	{syncModule, "input", 2, 1},
	{syncModule, "record_emit", 3, 0},
	{syncModule, "fail", 3, 0},
	{syncModule, "output", 2, 0},
	{syncModule, "checkpoint", 2, 0},
	{syncModule, "http_request", 2, 1},
	{syncModule, "http_response", 3, 1},
	{syncModule, "http_header", 4, 1},
	{syncModule, "sleep", 1, 0},
	{hostModule, "log", 3, 0},
}

const (
	opLoop = 0x03
	opBr   = 0x0c
)

// callImport is the instructions that call import fn with args.
func callImport(fn int, args ...int32) []byte {
	var b []byte
	for _, a := range args {
		b = append(b, i32(a)...)
	}
	return uleb(append(b, opCall), uint64(fn))
}

// abiData lays out a module's strings in its memory, from offset 16.
type abiData struct {
	segs []wasmData
	next int32
}

// str places s and returns its offset and length.
func (d *abiData) str(s string) (int32, int32) {
	if d.next == 0 {
		d.next = 16
	}
	off := d.next
	d.segs = append(d.segs, wasmData{off, []byte(s)})
	d.next += int32(len(s)) + 16
	return off, int32(len(s))
}

// abiModule is a plugin of one page of memory holding d's strings, whose
// export runs body, which leaves its status, and whose _initialize runs
// init, when it is set.
func abiModule(export string, d *abiData, body, init []byte) []byte {
	m := wasmModule{imports: abiImports, pages: 1, funcs: []wasmFunc{{export: export, params: 1, results: 1, body: body}}}
	if d != nil {
		m.data = d.segs
	}
	if init != nil {
		m.funcs = append(m.funcs, wasmFunc{export: "_initialize", body: init})
	}
	return m.encode()
}

// abiPool makes a pool of code, for o's export, with o's settings or ones
// of the tests'.
func abiPool(t *testing.T, code []byte, o syncOpts) (*syncPool, error) {
	t.Helper()
	o.path = filepath.Join(t.TempDir(), "plugin.wasm")
	if err := os.WriteFile(o.path, code, 0o644); err != nil {
		t.Fatal(err)
	}
	if o.export == "" {
		o.export = exportTransform
	}
	o.instances = max(o.instances, 1)
	if o.timeout == 0 {
		o.timeout = 20 * time.Second
	}
	o.maxMemory = orInt64(o.maxMemory, 64<<20)
	o.maxOutput = orInt64(o.maxOutput, 1<<20)
	p, err := newSyncPool(o)
	if err == nil {
		t.Cleanup(p.close)
	}
	return p, err
}

func mustABIPool(t *testing.T, code []byte, o syncOpts) *syncPool {
	t.Helper()
	p, err := abiPool(t, code, o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const abiRecord = `{"namespace":"abi","op":"insert","key":{"map":{"id":{"int":1}}}}`

// TestSyncABIMisuse checks each misuse of a host function fails the call
// with what it did wrong, and the next call on a remade instance runs.
func TestSyncABIMisuse(t *testing.T) {
	for name, tc := range map[string]struct {
		body  func(d *abiData) []byte
		n     int
		input string
		opts  syncOpts
		want  string
	}{
		"input outside memory": {body: func(*abiData) []byte { return cat(callImport(impInput, -256, 100), []byte{opDrop}) },
			input: "abc", want: "a buffer outside the plugin's memory"},
		"output past max_output_bytes": {body: func(*abiData) []byte { return callImport(impOutput, 0, 10) },
			opts: syncOpts{maxOutput: 4}, want: "more than max_output_bytes (4)"},
		"output outside memory": {body: func(*abiData) []byte { return callImport(impOutput, -256, 10) },
			want: "a string outside the plugin's memory"},
		"emit going back": {n: 2, body: func(d *abiData) []byte {
			off, n := d.str(abiRecord)
			return cat(callImport(impRecordEmit, 1, off, n), callImport(impRecordEmit, 0, off, n))
		}, want: "record_emit went back, to record 0"},
		"emit of no record": {n: 1, body: func(d *abiData) []byte {
			off, n := d.str(`{"op":`)
			return callImport(impRecordEmit, 0, off, n)
		}, want: "record_emit: "},
		"fail out of range": {n: 1, body: func(d *abiData) []byte {
			off, n := d.str("no")
			return callImport(impFail, 5, off, n)
		}, want: "fail names record 5 of 1"},
		"more fails than records": {n: 1, body: func(d *abiData) []byte {
			off, n := d.str("no")
			return cat(callImport(impFail, 0, off, n), callImport(impFail, 0, off, n))
		}, want: "more fails than records"},
		"checkpoint too long": {body: func(*abiData) []byte { return callImport(impCheckpoint, 0, maxCheckpoint+1) },
			want: "a checkpoint longer than 64 KiB"},
		"checkpoint not JSON": {body: func(d *abiData) []byte {
			off, n := d.str(`{"after":`)
			return callImport(impCheckpoint, off, n)
		}, want: "a checkpoint that is not JSON"},
		"request not JSON": {opts: syncOpts{allow: []string{"example.com"}}, body: func(d *abiData) []byte {
			off, n := d.str("GET /")
			return cat(callImport(impHTTPRequest, off, n), []byte{opDrop})
		}, want: "http_request: "},
		"request not http": {opts: syncOpts{allow: []string{"example.com"}}, body: func(d *abiData) []byte {
			off, n := d.str(`{"url":"ftp://example.com/x"}`)
			return cat(callImport(impHTTPRequest, off, n), []byte{opDrop})
		}, want: `"ftp://example.com/x" is not an http or https URL`},
		"sleep in a transform": {body: func(*abiData) []byte { return callImport(impSleep, 1) },
			want: "only a source's or a sink's plugin may sleep"},
		"header in a transform": {body: func(d *abiData) []byte {
			off, n := d.str("x")
			return cat(callImport(impHTTPHeader, off, n, 0, 0), []byte{opDrop})
		}, want: "only a source's or a sink's plugin may make HTTP requests"},
	} {
		t.Run(name, func(t *testing.T) {
			d := &abiData{}
			body := cat(tc.body(d), i32(syncOK))
			p := mustABIPool(t, abiModule(exportTransform, d, body, nil), tc.opts)
			sc := &syncCall{input: []byte(tc.input), n: tc.n}
			if _, err := p.call(context.Background(), sc); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
			// The instance the call stopped is made again.
			if in := <-p.idle; in.mod != nil {
				t.Error("the failed call's instance was kept")
			} else {
				p.idle <- in
			}
			if _, err := p.call(context.Background(), &syncCall{input: []byte(tc.input), n: tc.n}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("again: %v", err)
			}
		})
	}
}

// TestSyncABIOutsideACall checks a sync host function called while the
// plugin starts, outside any call, fails it, and a plugin that is not one
// or that traps as it starts is not loaded.
func TestSyncABIOutsideACall(t *testing.T) {
	init := cat(callImport(impInput, 0, 0), []byte{opDrop})
	if _, err := abiPool(t, abiModule(exportTransform, nil, i32(0), init), syncOpts{}); err == nil || !strings.Contains(err.Error(), "outside a sync call") {
		t.Error(err)
	}
	if _, err := abiPool(t, abiModule(exportTransform, nil, i32(0), trap), syncOpts{}); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Error(err)
	}
	if _, err := abiPool(t, []byte("\x00asm but not really"), syncOpts{}); err == nil || !strings.Contains(err.Error(), "compiling the plugin") {
		t.Error(err)
	}
}

// TestSyncABIWithoutEngine checks a connector run without an engine has
// no HTTP client: its request fails, and says why, and there is no
// response nor field.
func TestSyncABIWithoutEngine(t *testing.T) {
	const why = "no HTTP client: the pipeline runs without an engine"
	d := &abiData{}
	req, reqN := d.str(`{"url":"http://example.com/"}`)
	name, nameN := d.str("Content-Type")
	request := cat(callImport(impHTTPRequest, req, reqN), []byte{opDrop})
	for _, tc := range []struct {
		name string
		body []byte
		want uint32
		out  string
	}{
		{"status", callImport(impHTTPRequest, req, reqN), math.MaxUint32, ""},
		{"error", cat(request, callImport(impHTTPResponse, 1, 4096, 256), callImport(impOutput, 4096, int32(len(why)))), uint32(len(why)), why},
		{"body", cat(request, callImport(impHTTPResponse, 0, 4096, 256)), math.MaxUint32, ""},
		{"field", cat(request, callImport(impHTTPHeader, name, nameN, 4096, 256)), math.MaxUint32, ""},
		{"no part", cat(request, callImport(impHTTPResponse, 7, 4096, 256)), math.MaxUint32, ""},
	} {
		p := mustABIPool(t, abiModule(exportWrite, d, tc.body, nil), syncOpts{export: exportWrite, allow: []string{"example.com"}})
		sc := &syncCall{}
		status, err := p.call(context.Background(), sc)
		if err != nil || status != tc.want || string(sc.output) != tc.out {
			t.Errorf("%s: %v %d %q", tc.name, err, status, sc.output)
		}
	}
}

// TestSyncABIHTTP checks a connector's request through the engine's
// client: a response's field read back, a request it cannot make, and one
// to a host that is not there, which says why.
func TestSyncABIHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fit", "yes")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := "http://" + ln.Addr().String() + "/"
	ln.Close()
	e := engine(t)
	u, _ := url.Parse(srv.URL)
	opts := syncOpts{export: exportWrite, allow: []string{u.Hostname()}, engine: e, maxResponse: 1 << 20, timeout: 20 * time.Second}

	d := &abiData{}
	req, reqN := d.str(`{"url":"` + srv.URL + `/x"}`)
	name, nameN := d.str("x-fit")
	body := cat(callImport(impHTTPRequest, req, reqN), []byte{opDrop},
		callImport(impHTTPHeader, name, nameN, 4096, 256), callImport(impOutput, 4096, 3),
		[]byte{opDrop}, callImport(impHTTPResponse, 0, 8192, 256), callImport(impOutput, 8192, 5))
	p := mustABIPool(t, abiModule(exportWrite, d, body, nil), opts)
	sc := &syncCall{}
	if status, err := p.call(context.Background(), sc); err != nil || status != 5 || string(sc.output) != "yeshello" {
		t.Errorf("%v %d %q", err, status, sc.output)
	}

	d = &abiData{}
	req, reqN = d.str(`{"method":"BAD METHOD","url":"` + srv.URL + `/"}`)
	p = mustABIPool(t, abiModule(exportWrite, d, callImport(impHTTPRequest, req, reqN), nil), opts)
	if _, err := p.call(context.Background(), &syncCall{}); err == nil || !strings.Contains(err.Error(), "invalid method") {
		t.Error(err)
	}

	d = &abiData{}
	req, reqN = d.str(`{"url":"` + gone + `"}`)
	body = cat(callImport(impHTTPRequest, req, reqN), []byte{opDrop}, callImport(impHTTPResponse, 1, 4096, 0))
	p = mustABIPool(t, abiModule(exportWrite, d, body, nil), opts)
	// The export returns the error's length, from http_response.
	sc = &syncCall{}
	status, err := p.call(context.Background(), sc)
	if err != nil || status == 0 || status == math.MaxUint32 || sc.httpErr == "" || int(status) != len(sc.httpErr) {
		t.Errorf("%v %d %q", err, status, sc.httpErr)
	}
}

// TestSyncABISleepAndLog checks a connector sleeps and logs within its
// call, and a plugin's lines past maxLogs a second are dropped.
func TestSyncABISleepAndLog(t *testing.T) {
	d := &abiData{}
	msg, msgN := d.str("a line of the plugin's")
	body := cat(callImport(impSleep, 1), callImport(impLog, 8, msg, msgN), i32(syncOK))
	p := mustABIPool(t, abiModule(exportWrite, d, body, nil), syncOpts{export: exportWrite, allow: []string{"example.com"}})
	if status, err := p.call(context.Background(), &syncCall{}); err != nil || status != syncOK {
		t.Fatal(status, err)
	}
	if p.logs.Load() != 1 || p.logSecond.Load() == 0 {
		t.Errorf("%d lines in second %d", p.logs.Load(), p.logSecond.Load())
	}
	p.logs.Store(maxLogs)
	p.logSecond.Store(time.Now().Unix())
	if _, err := p.call(context.Background(), &syncCall{}); err != nil {
		t.Fatal(err)
	}
	// One past the bound, unless the second turned and the count began
	// again.
	if n := p.logs.Load(); n != maxLogs+1 && n != 1 {
		t.Errorf("%d lines", n)
	}
}

// TestSyncABICancel checks a call waiting for an instance, or running,
// stops when its context ends, with the context's error.
func TestSyncABICancel(t *testing.T) {
	spin := cat([]byte{opLoop, blockEmpty, opBr, 0, opEnd}, i32(syncOK))
	p := mustABIPool(t, abiModule(exportWrite, nil, spin, nil), syncOpts{export: exportWrite})
	in := <-p.idle
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.call(ctx, &syncCall{}); !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	p.idle <- in
	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := p.call(ctx, &syncCall{}); !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	defer cancel()
}

func abiRecords(n int) []*fitsync.Record {
	var recs []*fitsync.Record
	for i := range n {
		recs = append(recs, fitsync.GenerateRecord("shop.orders", int64(i), 0, 1))
	}
	return recs
}

// TestSyncABIModules checks what the sync modules make of a plugin's
// status, failures and records.
func TestSyncABIModules(t *testing.T) {
	ctx := context.Background()
	// A sink's failures: the earliest record named, else the status.
	d := &abiData{}
	no, noN := d.str("refused")
	p := mustABIPool(t, abiModule(exportWrite, d, cat(callImport(impFail, 2, no, noN), callImport(impFail, 0, no, noN), i32(syncFailed)), nil), syncOpts{export: exportWrite})
	var be *fitsync.BatchError
	if err := (&SyncSink{pool: p}).Write(ctx, abiRecords(3)); !errors.As(err, &be) || be.Index != 0 || !strings.Contains(err.Error(), "wasm sink: refused") {
		t.Errorf("%v", err)
	}
	p = mustABIPool(t, abiModule(exportWrite, nil, i32(syncFailed), nil), syncOpts{export: exportWrite})
	if err := (&SyncSink{pool: p}).Write(ctx, abiRecords(2)); err == nil || err.Error() != "wasm sink: the plugin returned 2" {
		t.Error(err)
	}

	// A transform's status fails each record of its call; a record it
	// emits goes to Apply's emit.
	p = mustABIPool(t, abiModule(exportTransform, nil, i32(syncFailed), nil), syncOpts{})
	var fails []int
	tr := &SyncTransform{pool: p, BatchRecords: 2, Instances: 1}
	if err := tr.ApplyBatch(abiRecords(3), func(int, *fitsync.Record) { t.Error("emitted") }, func(i int, err error) {
		if err.Error() == "the plugin returned 2" {
			fails = append(fails, i)
		}
	}); err != nil || len(fails) != 3 {
		t.Errorf("%v %v", err, fails)
	}
	d = &abiData{}
	rec, recN := d.str(abiRecord)
	p = mustABIPool(t, abiModule(exportTransform, d, cat(callImport(impRecordEmit, 0, rec, recN), i32(syncOK)), nil), syncOpts{})
	var got []string
	if err := (&SyncTransform{pool: p, BatchRecords: 1, Instances: 1}).Apply(abiRecords(1)[0], func(r *fitsync.Record) { got = append(got, r.Namespace) }); err != nil || len(got) != 1 || got[0] != "abi" {
		t.Errorf("%v %v", err, got)
	}

	// A codec without an export says so; one whose encode traps fails.
	var none SyncCodec
	if err := none.Decode([]byte("x"), func(*fitsync.Record) {}); err == nil || !strings.Contains(err.Error(), exportDecode) {
		t.Error(err)
	}
	if b, err := none.Encode([]byte("#"), abiRecords(1)); err == nil || string(b) != "#" || !strings.Contains(err.Error(), exportEncode) {
		t.Error(b, err)
	}
	p = mustABIPool(t, abiModule(exportEncode, nil, trap, nil), syncOpts{export: exportEncode})
	if b, err := (&SyncCodec{encode: p}).Encode([]byte("#"), abiRecords(1)); err == nil || string(b) != "#" || !strings.HasPrefix(err.Error(), "wasm codec: ") {
		t.Error(b, err)
	}
}

// TestSyncABIUnwritableBatch checks a batch that cannot be written as
// JSON, for a plugin that takes it so, fails as a whole, before any call:
// a transform's every record, a sink's write and a codec's encode.
func TestSyncABIUnwritableBatch(t *testing.T) {
	bad := abiRecords(2)
	bad[1].Position = fitsync.Position("not JSON")
	p := mustABIPool(t, abiModule(exportTransform, nil, trap, nil), syncOpts{})
	var fails []int
	if err := (&SyncTransform{pool: p, BatchRecords: 8, Instances: 1}).ApplyBatch(bad, func(int, *fitsync.Record) { t.Error("emitted") }, func(i int, err error) {
		if strings.Contains(err.Error(), "record 1: position: ") {
			fails = append(fails, i)
		}
	}); err != nil || len(fails) != 2 {
		t.Errorf("%v %v", err, fails)
	}
	p = mustABIPool(t, abiModule(exportWrite, nil, trap, nil), syncOpts{export: exportWrite})
	if err := (&SyncSink{pool: p}).Write(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "record 1: position: ") {
		t.Error(err)
	}
	p = mustABIPool(t, abiModule(exportEncode, nil, trap, nil), syncOpts{export: exportEncode})
	if b, err := (&SyncCodec{encode: p}).Encode([]byte("#"), bad); err == nil || string(b) != "#" || !strings.Contains(err.Error(), "record 1: position: ") {
		t.Error(b, err)
	}
}

// TestSyncConnectorLoadErrors checks a source whose plugin is not there,
// and a sink whose allow_hosts names no host, are not loaded.
func TestSyncConnectorLoadErrors(t *testing.T) {
	if _, err := loadSync(t, fitsync.Sources, map[string]any{"path": filepath.Join(t.TempDir(), "nope.wasm")}); err == nil || !strings.Contains(err.Error(), "nope.wasm") {
		t.Error(err)
	}
	if _, err := loadSync(t, fitsync.Sinks, map[string]any{"path": "x.wasm", "allow_hosts": []string{"example.com", "*"}}); err == nil || !strings.Contains(err.Error(), `"*" names no host`) {
		t.Error(err)
	}
}

// TestSyncABISource checks a source plugin's failures end its run, a
// refused record ends it with Emit's error, and one with nothing to give
// waits poll_interval, until its context ends.
func TestSyncABISource(t *testing.T) {
	ctx := context.Background()
	src := func(body []byte, d *abiData) *SyncSource {
		return &SyncSource{pool: mustABIPool(t, abiModule(exportPoll, d, body, nil), syncOpts{export: exportPoll, allow: []string{}}),
			PollInterval: config.Duration(time.Hour)}
	}
	emit := func(*fitsync.Record) error { return nil }
	if err := src(trap, nil).Run(ctx, &fitsync.Run{Emit: emit}); err == nil || !strings.HasPrefix(err.Error(), "wasm source: ") || !strings.Contains(err.Error(), "unreachable") {
		t.Error(err)
	}
	d := &abiData{}
	boom, boomN := d.str("the API is down")
	if err := src(cat(callImport(impFail, -1, boom, boomN), i32(syncFailed)), d).Run(ctx, &fitsync.Run{Emit: emit}); err == nil || err.Error() != "wasm source: the API is down" {
		t.Error(err)
	}
	d = &abiData{}
	rec, recN := d.str(abiRecord)
	refused := errors.New("the pipeline stopped")
	if err := src(cat(callImport(impRecordEmit, 0, rec, recN), i32(syncOK)), d).Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
		if r.Source != "wasm" {
			t.Errorf("source %q", r.Source)
		}
		return refused
	}}); err != refused {
		t.Error(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := src(i32(syncOK), nil)
	done := make(chan error, 1)
	go func() { done <- s.Run(cctx, &fitsync.Run{Emit: emit}) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
}
