package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	stdhttp "net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	fitsync "github.com/lesismal/fit/sync"
)

// The sync ABI: the host module fit_sync, beside fit's, and the functions a
// sync plugin exports. Each export takes the length of its input, which the
// plugin reads with input, and returns syncOK, syncCaughtUp (a source with
// nothing more for now) or syncFailed. See docs/decisions/036-sync.md.
const (
	syncModule      = "fit_sync"
	exportTransform = "fit_sync_transform"
	exportDecode    = "fit_sync_decode"
	exportEncode    = "fit_sync_encode"
	exportPoll      = "fit_sync_poll"
	exportWrite     = "fit_sync_write"

	syncOK       = 0
	syncCaughtUp = 1
	syncFailed   = 2

	// maxCheckpoint bounds a checkpoint a source plugin sets.
	maxCheckpoint = 64 << 10
)

// instantiateSyncHost adds the host module fit_sync to rt:
//
//	input(buf, buf_len) i32  the call's input written to buf, up to buf_len
//	    bytes, and its length: the batch, the bytes to decode, or the
//	    checkpoint
//	record_emit(index, rec, rec_len)  a record, in canonical JSON, that the
//	    input's record index becomes (0 for decode and poll); indexes never
//	    go back
//	fail(index, msg, msg_len)  the input's record index failed, or, for
//	    -1, the call: msg says why
//	output(buf, buf_len)  bytes encode makes, appended
//	checkpoint(buf, buf_len)  the checkpoint, JSON, a poll moves to
//	http_request(req, req_len) i32  a connector's HTTP request, as JSON
//	    {"method", "url", "headers": {name: value}, "body"}, to a host its
//	    allow_hosts lets it reach; the status, or -1 when it failed
//	http_response(part, buf, buf_len) i32  the last response's body (part
//	    0), or the last request's error (part 1), as input writes them; -1
//	    when there is none
//	http_header(name, name_len, buf, buf_len) i32  the last response's
//	    field, or -1
//	sleep(ms)  a connector waits, within its call's timeout
//
// The plugin's config and its log are fit's own request_get(5) and log.
func instantiateSyncHost(ctx context.Context, rt wazero.Runtime) error {
	i32 := api.ValueTypeI32
	b := rt.NewHostModuleBuilder(syncModule)
	export := func(name string, fn api.GoModuleFunc, params, results []api.ValueType) {
		b.NewFunctionBuilder().WithGoModuleFunction(fn, params, results).Export(name)
	}
	export("input", syncInput, []api.ValueType{i32, i32}, []api.ValueType{i32})
	export("record_emit", recordEmit, []api.ValueType{i32, i32, i32}, nil)
	export("fail", failRecord, []api.ValueType{i32, i32, i32}, nil)
	export("output", syncOutput, []api.ValueType{i32, i32}, nil)
	export("checkpoint", syncCheckpoint, []api.ValueType{i32, i32}, nil)
	export("http_request", httpRequest, []api.ValueType{i32, i32}, []api.ValueType{i32})
	export("http_response", httpResponse, []api.ValueType{i32, i32, i32}, []api.ValueType{i32})
	export("http_header", httpHeader, []api.ValueType{i32, i32, i32, i32}, []api.ValueType{i32})
	export("sleep", syncSleep, []api.ValueType{i32}, nil)
	_, err := b.Instantiate(ctx)
	return err
}

type syncKey struct{}

// syncCall is a call to a sync plugin, as its host functions see it.
type syncCall struct {
	p     *syncPool
	input []byte
	// n is how many records the input holds, which record_emit and fail
	// may name; last the index record_emit named last.
	n, last int
	outs    []syncOut
	fails   []syncFail
	failMsg string
	output  []byte
	// bytes counts what the plugin handed back, which is bounded.
	bytes      int64
	checkpoint []byte
	// The last HTTP request's outcome.
	status  int
	header  stdhttp.Header
	body    []byte
	httpErr string
}

type syncOut struct {
	i   int
	rec *fitsync.Record
}

type syncFail struct {
	i   int
	msg string
}

// syncOf returns the sync call ctx is in, or nil.
func syncOf(ctx context.Context) *syncCall {
	sc, _ := ctx.Value(syncKey{}).(*syncCall)
	return sc
}

func mustSync(ctx context.Context) *syncCall {
	sc := syncOf(ctx)
	if sc == nil {
		panic(errABI("a sync host function called outside a sync call"))
	}
	return sc
}

// writeBytes writes b into mod's memory at buf, up to n bytes, and returns
// b's length.
func writeBytes(mod api.Module, buf, n uint32, b []byte) int32 {
	if len(b) > 0 && n > 0 {
		if !mod.Memory().Write(buf, b[:min(len(b), int(n))]) {
			panic(errABI("a buffer outside the plugin's memory"))
		}
	}
	return int32(len(b))
}

// take returns n bytes of the plugin's memory at ptr, counted against what
// a call may hand back.
func (sc *syncCall) take(mod api.Module, ptr, n uint32) []byte {
	if sc.bytes += int64(n); sc.bytes > sc.p.opts.maxOutput {
		panic(errABI(fmt.Sprintf("the plugin handed back more than max_output_bytes (%d)", sc.p.opts.maxOutput)))
	}
	b, ok := mod.Memory().Read(ptr, n)
	if !ok {
		panic(errABI("a string outside the plugin's memory"))
	}
	return b
}

func syncInput(ctx context.Context, mod api.Module, stack []uint64) {
	sc := mustSync(ctx)
	stack[0] = api.EncodeI32(writeBytes(mod, api.DecodeU32(stack[0]), api.DecodeU32(stack[1]), sc.input))
}

func recordEmit(ctx context.Context, mod api.Module, stack []uint64) {
	sc := mustSync(ctx)
	i := int(api.DecodeI32(stack[0]))
	if i < 0 || i >= sc.n {
		panic(errABI(fmt.Sprintf("record_emit names record %d of %d", i, sc.n)))
	}
	if i < sc.last {
		panic(errABI("record_emit went back, to record " + fmt.Sprint(i)))
	}
	sc.last = i
	rec, err := fitsync.ParseRecord(sc.take(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	if err != nil {
		panic(errABI("record_emit: " + err.Error()))
	}
	sc.outs = append(sc.outs, syncOut{i, rec})
}

func failRecord(ctx context.Context, mod api.Module, stack []uint64) {
	sc := mustSync(ctx)
	i := int(api.DecodeI32(stack[0]))
	msg := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
	switch {
	case i == -1:
		sc.failMsg = msg
	case i < 0 || i >= sc.n:
		panic(errABI(fmt.Sprintf("fail names record %d of %d", i, sc.n)))
	case len(sc.fails) == sc.n:
		panic(errABI("more fails than records"))
	default:
		sc.fails = append(sc.fails, syncFail{i, msg})
	}
}

func syncOutput(ctx context.Context, mod api.Module, stack []uint64) {
	sc := mustSync(ctx)
	sc.output = append(sc.output, sc.take(mod, api.DecodeU32(stack[0]), api.DecodeU32(stack[1]))...)
}

func syncCheckpoint(ctx context.Context, mod api.Module, stack []uint64) {
	sc := mustSync(ctx)
	n := api.DecodeU32(stack[1])
	if n > maxCheckpoint {
		panic(errABI("a checkpoint longer than 64 KiB"))
	}
	b := read(mod, api.DecodeU32(stack[0]), n)
	if !json.Valid(b) {
		panic(errABI("a checkpoint that is not JSON"))
	}
	sc.checkpoint = append(sc.checkpoint[:0], b...)
}

// connector returns the call of a connector plugin, which may reach the
// outside, and fails the call of any other.
func connector(ctx context.Context, what string) *syncCall {
	sc := mustSync(ctx)
	if sc.p.opts.allow == nil {
		panic(errABI("only a source's or a sink's plugin may " + what))
	}
	return sc
}

// httpReq is what http_request takes.
type httpReq struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type httpResult struct {
	status int
	header stdhttp.Header
	body   []byte
	err    error
}

func httpRequest(ctx context.Context, mod api.Module, stack []uint64) {
	sc := connector(ctx, "make HTTP requests")
	var hr httpReq
	if err := json.Unmarshal(read(mod, api.DecodeU32(stack[0]), api.DecodeU32(stack[1])), &hr); err != nil {
		panic(errABI("http_request: " + err.Error()))
	}
	if hr.Method == "" {
		hr.Method = stdhttp.MethodGet
	}
	u, err := url.Parse(hr.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		panic(errABI(fmt.Sprintf("http_request: %q is not an http or https URL", hr.URL)))
	}
	if !sc.p.allowed(u) {
		panic(errABI(fmt.Sprintf("http_request: the host %s is not in allow_hosts", u.Host)))
	}
	sc.status, sc.header, sc.body, sc.httpErr = 0, nil, nil, ""
	if sc.p.client == nil {
		sc.httpErr = "no HTTP client: the pipeline runs without an engine"
		stack[0] = api.EncodeI32(-1)
		return
	}
	var body io.Reader
	if hr.Body != "" {
		body = strings.NewReader(hr.Body)
	}
	req, err := stdhttp.NewRequestWithContext(ctx, hr.Method, u.String(), body)
	if err != nil {
		panic(errABI("http_request: " + err.Error()))
	}
	for k, v := range hr.Headers {
		req.Header.Set(k, v)
	}
	done := make(chan httpResult, 1)
	sc.p.client.Do(req, func(resp *fibhttp.ClientResponse, err error) {
		if err != nil {
			done <- httpResult{err: err}
			return
		}
		b, ok := resp.BodyBytes()
		if !ok {
			done <- httpResult{err: errors.New("a streamed body")}
			return
		}
		done <- httpResult{status: resp.StatusCode, header: resp.Header, body: b}
	})
	var res httpResult
	select {
	case res = <-done:
	case <-ctx.Done():
		panic(errABI("the call's timeout passed waiting for an HTTP response"))
	}
	if res.err != nil {
		sc.httpErr = res.err.Error()
		stack[0] = api.EncodeI32(-1)
		return
	}
	sc.status, sc.header, sc.body = res.status, res.header, res.body
	stack[0] = api.EncodeI32(int32(res.status))
}

func httpResponse(ctx context.Context, mod api.Module, stack []uint64) {
	sc := connector(ctx, "make HTTP requests")
	buf, n := api.DecodeU32(stack[1]), api.DecodeU32(stack[2])
	result := int32(-1)
	switch api.DecodeU32(stack[0]) {
	case 0:
		if sc.status != 0 {
			result = writeBytes(mod, buf, n, sc.body)
		}
	case 1:
		if sc.httpErr != "" {
			result = write(mod, buf, n, sc.httpErr)
		}
	}
	stack[0] = api.EncodeI32(result)
}

func httpHeader(ctx context.Context, mod api.Module, stack []uint64) {
	sc := connector(ctx, "make HTTP requests")
	name := textproto.CanonicalMIMEHeaderKey(readField(mod, api.DecodeU32(stack[0]), api.DecodeU32(stack[1])))
	result := int32(-1)
	if v, ok := sc.header[name]; ok && len(v) > 0 {
		result = write(mod, api.DecodeU32(stack[2]), api.DecodeU32(stack[3]), v[0])
	}
	stack[0] = api.EncodeI32(result)
}

func syncSleep(ctx context.Context, _ api.Module, stack []uint64) {
	connector(ctx, "sleep")
	t := time.NewTimer(time.Duration(api.DecodeU32(stack[0])) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// syncLog writes a sync plugin's line, as logMessage does a request's.
func syncLog(sc *syncCall, level slog.Level, msg string) {
	p := sc.p
	now := time.Now().Unix()
	if s := p.logSecond.Load(); s != now && p.logSecond.CompareAndSwap(s, now) {
		p.logs.Store(0)
	}
	if p.logs.Add(1) <= maxLogs {
		pluginLog.Logf(level, "wasm sync plugin %s: %s", p.opts.path, msg)
	}
}

// syncOpts are what a sync plugin's pool is made with.
type syncOpts struct {
	path, config string
	export       string
	instances    int
	timeout      time.Duration
	maxMemory    int64
	maxOutput    int64
	// allow are the hosts a connector may reach; nil for a plugin that may
	// reach none, and sleep neither.
	allow       []string
	maxResponse int64
	engine      *fib.Engine
}

// syncPool runs a sync plugin's calls, each on an instance of its own taken
// from idle, made when first needed and again after a call fails. Callers
// are the pipeline's goroutines, never a fib worker, and wait for an
// instance; a call is bounded by the timeout and the instance's memory.
type syncPool struct {
	opts      syncOpts
	plugin    *plugin
	idle      chan *syncInstance
	client    *fibhttp.Client
	logs      atomic.Int64
	logSecond atomic.Int64
}

type syncInstance struct {
	mod   api.Module
	fn    api.Function
	stack [1]uint64
}

func newSyncPool(o syncOpts) (*syncPool, error) {
	code, err := os.ReadFile(o.path)
	if err != nil {
		return nil, err
	}
	pl, err := loadPlugin(code, o.maxMemory)
	if err != nil {
		return nil, err
	}
	if !pl.exports(o.export) {
		pl.close()
		return nil, fmt.Errorf("the plugin does not export %s", o.export)
	}
	p := &syncPool{opts: o, plugin: pl, idle: make(chan *syncInstance, o.instances)}
	first := &syncInstance{}
	if err := p.instantiate(first); err != nil {
		pl.close()
		return nil, err
	}
	p.idle <- first
	for range o.instances - 1 {
		p.idle <- &syncInstance{}
	}
	if len(o.allow) > 0 && o.engine != nil {
		cc := fibhttp.DefaultClientConfig()
		cc.Timeout, cc.ResponseHeaderTimeout = o.timeout, o.timeout
		cc.DialTimeout = min(o.timeout, 10*time.Second)
		cc.MaxConnsPerHost, cc.MaxIdleConnsPerHost = 4, 4
		cc.MaxResponseBodyBytes = o.maxResponse
		p.client = fibhttp.NewClient(o.engine, cc)
	}
	return p, nil
}

func (p *syncPool) instantiate(in *syncInstance) error {
	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize").
		WithStdout(io.Discard).WithStderr(io.Discard).WithSysWalltime().WithSysNanotime()
	mod, err := p.plugin.runtime.InstantiateModule(context.Background(), p.plugin.compiled, cfg)
	if err != nil {
		return err
	}
	in.mod, in.fn = mod, mod.ExportedFunction(p.opts.export)
	return nil
}

// close stops the instances and gives the plugin back; no call may be
// under way.
func (p *syncPool) close() {
	for range p.opts.instances {
		in := <-p.idle
		if in.mod != nil {
			_ = in.mod.Close(context.Background())
		}
	}
	if p.client != nil {
		p.client.Close()
	}
	p.plugin.close()
}

// call runs the plugin's export for sc on an idle instance, waiting for
// one while ctx lets it, and returns what the export returned. A call that
// fails, traps, asks for more memory than allowed or runs past the timeout
// is an error, and its instance is made again for the next call.
func (p *syncPool) call(ctx context.Context, sc *syncCall) (uint32, error) {
	var in *syncInstance
	select {
	case in = <-p.idle:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { p.idle <- in }()
	if in.mod == nil {
		if err := p.instantiate(in); err != nil {
			return 0, err
		}
	}
	sc.p = p
	cctx, cancel := context.WithTimeout(context.WithValue(ctx, syncKey{}, sc), p.opts.timeout)
	defer cancel()
	in.stack[0] = uint64(len(sc.input))
	err := in.fn.CallWithStack(cctx, in.stack[:])
	if err != nil {
		_ = in.mod.Close(context.Background())
		in.mod = nil
		if errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return 0, fmt.Errorf("the plugin ran past its timeout of %v", p.opts.timeout)
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		msg, _, _ := strings.Cut(err.Error(), "\n")
		return 0, errors.New(msg)
	}
	return uint32(in.stack[0]), nil
}

// failure is what a call that returned status says of itself.
func (sc *syncCall) failure(status uint32) error {
	if sc.failMsg != "" {
		return errors.New(sc.failMsg)
	}
	return fmt.Errorf("the plugin returned %d", status)
}

// allowed reports whether allow_hosts lets the plugin reach u: an entry
// names a host, any port, or host:port, or *.domain for its subdomains.
func (p *syncPool) allowed(u *url.URL) bool {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	for _, a := range p.opts.allow {
		a = strings.ToLower(a)
		h, ap, err := net.SplitHostPort(a)
		if err != nil {
			h, ap = strings.Trim(a, "[]"), ""
		}
		if ap != "" && ap != port {
			continue
		}
		if h == host || strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:]) {
			return true
		}
	}
	return false
}
