// Package fastcgi has the fastcgi handler, which answers requests from a
// FastCGI application, as PHP-FPM is: the request's CGI parameters, PHP's
// among them, and its body go to the application, and the response it
// writes comes back, both streamed and neither held whole.
//
// The client is fit's own, over fib connections dialed through fib's
// engine, so nothing waits on a worker: one request at a time on each
// connection, idle ones kept for the next, their number bounded, and a
// request that finds none free waits in a bounded queue. Every exchange has
// its timeouts: connect, the response's header, the response going idle,
// and the whole.
package fastcgi

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/filepool"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".fastcgi", New: func() any { return new(FastCGI) }, Doc: "Answers requests from a FastCGI application, as PHP-FPM is, over a bounded pool of connections, streaming both ways."})
}

// Defaults of FastCGI's params.
const (
	DefaultMaxConns       = 64
	DefaultMaxIdleConns   = 16
	DefaultMaxPending     = 1024
	DefaultMaxHeaderBytes = 64 << 10
	defaultConnect        = 5 * time.Second
	defaultHeader         = 60 * time.Second
	defaultIdle           = 60 * time.Second
	defaultTotal          = 10 * time.Minute
	defaultIdleConn       = 60 * time.Second
	// watermark is how much may wait to be sent on a connection, or to the
	// client, before the side sending it is held back.
	watermark = 64 << 10
)

// FastCGI answers requests from a FastCGI application.
type FastCGI struct {
	Address               string            `json:"address" fit:"required" doc:"Where the application listens: a host and port, as \"127.0.0.1:9000\", or \"unix/\" and a socket's path, as \"unix//run/php/php-fpm.sock\"."`
	Root                  string            `json:"root" fit:"required" doc:"The document root, which SCRIPT_FILENAME, DOCUMENT_ROOT and PATH_TRANSLATED are made from, as the application sees it."`
	SplitPath             []string          `json:"split_path,omitempty" default:"[\".php\"]" doc:"What ends a script's name in a path, the rest being PATH_INFO: \"/index.php/a/b\" runs /index.php with PATH_INFO /a/b. [\".php\"] when left out."`
	Index                 string            `json:"index,omitempty" default:"\"index.php\"" doc:"The script a path ending in / runs; index.php when left out."`
	TryFiles              []string          `json:"try_files,omitempty" doc:"Paths tried in turn, as nginx's try_files, for the script to run: {path} stands for the request's path, and the first whose script exists under root runs; [\"{path}\", \"{path}/index.php\", \"/index.php\"] sends what is no script of its own to the front controller, as most PHP applications want. Looked up on a pool of goroutines of its own. Left out, the path runs as it is."`
	Env                   map[string]string `json:"env,omitempty" doc:"More parameters for the application, set after fit's own."`
	MaxConns              int               `json:"max_conns,omitempty" min:"0" default:"64" doc:"The most connections to the application, each carrying one request at a time. 64 when left out or 0."`
	MaxIdleConns          int               `json:"max_idle_conns,omitempty" min:"0" default:"16" doc:"The most idle connections kept for later requests. 16 when left out or 0."`
	MaxPending            int               `json:"max_pending,omitempty" min:"0" default:"1024" doc:"How many requests may wait for a connection when max_conns are busy; past it a request is answered 503. 1024 when left out or 0."`
	ConnectTimeout        config.Duration   `json:"connect_timeout,omitempty" default:"\"5s\"" doc:"How long connecting may take; 5s when left out. A request that times out connecting is answered 504."`
	ResponseHeaderTimeout config.Duration   `json:"response_header_timeout,omitempty" default:"\"60s\"" doc:"How long the application may take to begin its response; 60s when left out."`
	ResponseIdleTimeout   config.Duration   `json:"response_idle_timeout,omitempty" default:"\"60s\"" doc:"How long the application may go silent in the middle of a response; 60s when left out."`
	Timeout               config.Duration   `json:"timeout,omitempty" default:"\"10m\"" doc:"The most a whole exchange may take; 10m when left out."`
	IdleTimeout           config.Duration   `json:"idle_timeout,omitempty" default:"\"60s\"" doc:"How long an idle connection is kept; 60s when left out."`
	MaxHeaderBytes        int               `json:"max_header_bytes,omitempty" min:"0" default:"65536" doc:"The most bytes the response's CGI header may take; past it the request is answered 502. 64 KiB when left out or 0."`

	pool  *pool
	graph module.Holder
	stall time.Duration
}

// Provision checks the params and makes the connection pool.
func (h *FastCGI) Provision(ctx *module.Context) error {
	network, addr := "tcp", h.Address
	if p, ok := strings.CutPrefix(h.Address, "unix/"); ok {
		network, addr = "unix", p
	} else if _, _, err := net.SplitHostPort(addr); err != nil {
		return ctx.Errorf("address", "%q is neither a host and port nor unix/ and a path", h.Address)
	}
	if !filepath.IsAbs(h.Root) {
		return ctx.Errorf("root", "%q is not an absolute path", h.Root)
	}
	if len(h.SplitPath) == 0 {
		h.SplitPath = []string{".php"}
	}
	for i, s := range h.SplitPath {
		h.SplitPath[i] = strings.ToLower(s)
	}
	if h.Index == "" {
		h.Index = "index.php"
	}
	for i, t := range h.TryFiles {
		if !strings.HasPrefix(t, "/") && !strings.HasPrefix(t, "{path}") {
			return ctx.Params.Field("try_files").Index(i).Errorf("%q starts with neither / nor {path}", t)
		}
	}
	or := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	or(&h.MaxConns, DefaultMaxConns)
	or(&h.MaxIdleConns, DefaultMaxIdleConns)
	or(&h.MaxPending, DefaultMaxPending)
	or(&h.MaxHeaderBytes, DefaultMaxHeaderBytes)
	dur := func(v *config.Duration, d time.Duration) {
		if *v <= 0 {
			*v = config.Duration(d)
		}
	}
	dur(&h.ConnectTimeout, defaultConnect)
	dur(&h.ResponseHeaderTimeout, defaultHeader)
	dur(&h.ResponseIdleTimeout, defaultIdle)
	dur(&h.Timeout, defaultTotal)
	dur(&h.IdleTimeout, defaultIdleConn)
	h.graph, h.stall = ctx.Graph(), ctx.WriteStallTimeout()
	engine := ctx.Engine()
	if engine == nil {
		// Compiled only to be checked, as fit validate does, and as
		// upstreams are: there is nothing to dial with, nor any request.
		return nil
	}
	h.pool = &pool{engine: engine, network: network, addr: addr, connect: h.ConnectTimeout.Std(), idle: h.IdleTimeout.Std(),
		maxConns: h.MaxConns, maxIdle: h.MaxIdleConns, maxPending: h.MaxPending}
	return nil
}

// Cleanup closes the idle connections.
func (h *FastCGI) Cleanup() {
	if h.pool != nil {
		h.pool.close(false)
	}
}

// Drain ends the exchanges still under way, closing their connections.
func (h *FastCGI) Drain() {
	if h.pool != nil {
		h.pool.close(true)
	}
}

var (
	lookupsOnce sync.Once
	lookups     *filepool.Pool
)

// lookupPool is where try_files looks at the disk: 4 workers a CPU, at
// least 16, as the static handler's.
func lookupPool() *filepool.Pool {
	lookupsOnce.Do(func() {
		lookups = filepool.New(max(16, 4*goruntime.GOMAXPROCS(0)), 4096, 10*time.Second)
	})
	return lookups
}

// ServeHTTP sends the request to the application, after try_files has
// found its script when there are any.
func (h *FastCGI) ServeHTTP(c *fibhttp.Context) {
	c.Retain()
	h.graph.Retain()
	x := &exchange{h: h, c: c}
	if len(h.TryFiles) == 0 {
		x.script, x.pathInfo = h.split(c.Request.URL.Path)
		x.start()
		return
	}
	reqPath := c.Request.URL.Path
	if !lookupPool().Submit(func() {
		script, info, ok := h.try(reqPath)
		if !ok {
			x.answer(stdhttp.StatusNotFound)
			return
		}
		x.script, x.pathInfo = script, info
		x.start()
	}, func() { x.answer(stdhttp.StatusServiceUnavailable) }) {
		x.answer(stdhttp.StatusServiceUnavailable)
	}
}

// split splits a path into the script it names and its path info.
func (h *FastCGI) split(p string) (script, info string) {
	lower := strings.ToLower(p)
	for _, ext := range h.SplitPath {
		for from := 0; ; {
			i := strings.Index(lower[from:], ext)
			if i < 0 {
				break
			}
			end := from + i + len(ext)
			if end == len(p) || p[end] == '/' {
				return p[:end], p[end:]
			}
			from = end
		}
	}
	if strings.HasSuffix(p, "/") {
		return p + h.Index, ""
	}
	return p, ""
}

// try returns the script of the first of try_files whose script is a file
// under root. It runs on the lookup pool.
func (h *FastCGI) try(reqPath string) (script, info string, ok bool) {
	for _, t := range h.TryFiles {
		candidate := path.Clean("/" + strings.ReplaceAll(t, "{path}", reqPath))
		if strings.HasSuffix(t, "/") || strings.HasSuffix(reqPath, "/") && strings.HasSuffix(t, "{path}") {
			candidate += "/"
		}
		s, i := h.split(candidate)
		fi, err := os.Stat(filepath.Join(h.Root, filepath.FromSlash(path.Clean(s))))
		if err == nil && fi.Mode().IsRegular() {
			return s, i, true
		}
	}
	return "", "", false
}

// exchange is a request on its way through the application.
type exchange struct {
	h              *FastCGI
	c              *fibhttp.Context
	script         string
	pathInfo       string
	conn           *conn
	mu             sync.Mutex
	started, done  bool
	bodyDone       bool
	head           []byte
	timer, overall *time.Timer
	declared       bool
	// held says the connection's reads are held for the client.
	held bool
}

// start takes a connection for the request.
func (x *exchange) start() {
	x.c.OnCancel(func(error) { x.fail(errClientGone) })
	x.mu.Lock()
	x.overall = time.AfterFunc(x.h.Timeout.Std(), func() { x.fail(errTimeout) })
	x.timer = time.AfterFunc(x.h.ConnectTimeout.Std()+x.h.ResponseHeaderTimeout.Std(), func() { x.fail(errTimeout) })
	x.mu.Unlock()
	x.h.pool.get(x.connected)
}

var (
	errClientGone = errors.New("fastcgi: the client went away")
	errTimeout    = errors.New("fastcgi: timed out")
)

// connected sends the request on the connection it got.
func (x *exchange) connected(c *conn, err error) {
	if err != nil {
		switch {
		case errors.Is(err, errTooMany), errors.Is(err, errPoolClosed):
			x.answer(stdhttp.StatusServiceUnavailable)
		default:
			x.failStatus(err, statusOf(err))
		}
		return
	}
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		c.done(true)
		return
	}
	x.conn = c
	x.timer.Reset(x.h.ResponseHeaderTimeout.Std())
	x.mu.Unlock()
	c.start(x)
	r := x.c.Request
	b := appendBeginRequest(make([]byte, 0, 4096))
	b = appendRecord(b, typeParams, x.params())
	b = appendRecord(b, typeParams, nil)
	noBody := r.ContentLength == 0 || r.Method == stdhttp.MethodHead
	if noBody {
		b = appendRecord(b, typeStdin, nil)
		x.mu.Lock()
		x.bodyDone = true
		x.mu.Unlock()
	}
	if err := c.send(b); err != nil {
		x.fail(err)
		return
	}
	if !noBody {
		x.c.OnBody(x.body)
	}
}

// body sends the request's body on as it arrives, holding it while the
// connection has more than the watermark unsent.
func (x *exchange) body(data []byte, fin bool, err error) {
	if err != nil {
		x.failStatus(err, stdhttp.StatusBadRequest)
		return
	}
	x.mu.Lock()
	c := x.conn
	done := x.done
	x.mu.Unlock()
	if done || c == nil {
		return
	}
	var b []byte
	if len(data) > 0 {
		b = appendRecord(b, typeStdin, data)
	}
	if fin {
		b = appendRecord(b, typeStdin, nil)
	}
	if len(b) > 0 {
		if err := c.send(b); err != nil {
			x.fail(err)
			return
		}
	}
	if fin {
		x.mu.Lock()
		x.bodyDone = true
		x.mu.Unlock()
		return
	}
	fc := c.fib()
	x.c.HoldBody(true)
	if !fc.NotifyOutput(watermark, func(error) { x.c.HoldBody(false) }) {
		x.c.HoldBody(false)
	}
}

// record takes a record the application sent. It runs on the connection's
// worker.
func (x *exchange) record(r record) error {
	if r.id != requestID {
		return errProtocol
	}
	switch r.t {
	case typeStdout:
		return x.stdout(r.content)
	case typeStderr:
		logStderr(x.h.Address, r.content)
	case typeEndRequest:
		if len(r.content) < 8 {
			return errProtocol
		}
		x.end(r.content[4] == 0)
	}
	return nil
}

// stdout takes a part of the CGI response: its header, until the blank
// line after it, then its body.
func (x *exchange) stdout(p []byte) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.done || len(p) == 0 {
		return nil
	}
	x.timer.Reset(x.h.ResponseIdleTimeout.Std())
	if !x.started {
		x.head = append(x.head, p...)
		end, sep := bytes.Index(x.head, []byte("\r\n\r\n")), 4
		if lf := bytes.Index(x.head, []byte("\n\n")); lf >= 0 && (end < 0 || lf < end) {
			end, sep = lf, 2
		}
		if end < 0 {
			if len(x.head) > x.h.MaxHeaderBytes {
				return errors.New("fastcgi: the response's header is too large")
			}
			return nil
		}
		if err := x.begin(x.head[:end]); err != nil {
			return err
		}
		p = x.head[end+sep:]
		x.head = nil
		if len(p) == 0 {
			return nil
		}
	}
	return x.write(p)
}

// begin writes the response's status and header from the CGI header. x.mu
// is held.
func (x *exchange) begin(head []byte) error {
	status := 0
	h := x.c.Header()
	location := false
	for line := range strings.SplitSeq(string(head), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return errors.New("fastcgi: a header line with no colon")
		}
		name, value = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name)), strings.TrimSpace(value)
		switch name {
		case "Status":
			code, _, _ := strings.Cut(value, " ")
			n, err := strconv.Atoi(code)
			if err != nil || n < 100 || n > 999 {
				return fmt.Errorf("fastcgi: a status %q", value)
			}
			status = n
		case "Connection", "Transfer-Encoding", "Keep-Alive", "Upgrade":
		default:
			if name == "Location" {
				location = true
			}
			if name == "Content-Length" {
				x.declared = true
			}
			h[name] = append(h[name], value)
		}
	}
	if status == 0 {
		status = stdhttp.StatusOK
		if location {
			status = stdhttp.StatusFound
		}
	}
	x.started = true
	x.c.WriteHeader(status)
	return nil
}

// write sends a part of the body to the client, and holds the connection's
// reads while the client has more than the watermark queued. x.mu is held.
func (x *exchange) write(p []byte) error {
	above, err := x.c.WriteNoWait(p)
	if err == nil {
		err = x.c.FlushError()
	}
	if err != nil {
		return errClientGone
	}
	if above && !x.held {
		// Records already read may still come; one hold stands for them
		// all.
		x.held = true
		x.conn.fib().HoldReads(true)
		if err := x.c.SetWriteDeadline(time.Now().Add(x.h.stall)); err != nil && !errors.Is(err, stdhttp.ErrNotSupported) {
			return errClientGone
		}
		if !x.c.OnWritable(x.writable) {
			x.resume()
		}
	}
	return nil
}

// writable resumes the application's response once the client has room,
// or ends the exchange when the client went or the stall timeout passed.
func (x *exchange) writable(err error) {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	if err != nil {
		x.mu.Unlock()
		x.fail(errClientGone)
		return
	}
	x.resume()
	x.mu.Unlock()
}

// resume lifts the hold write took. x.mu is held.
func (x *exchange) resume() {
	x.held = false
	_ = x.c.SetWriteDeadline(time.Time{})
	x.conn.fib().HoldReads(false)
}

// end finishes the response once the application ended the request. A
// connection whose request ended as FastCGI says, its body all sent, is
// kept for another.
func (x *exchange) end(complete bool) {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	if !x.started {
		if err := x.begin(x.head); err != nil {
			x.mu.Unlock()
			x.failStatus(err, stdhttp.StatusBadGateway)
			return
		}
	}
	x.done = true
	reuse := complete && x.bodyDone
	x.stopTimers()
	c := x.conn
	x.mu.Unlock()
	_ = x.c.Finish()
	c.done(reuse)
	x.release()
}

// fail ends the exchange for err: answered with a status of err's when the
// response has not begun, cut short when it has.
func (x *exchange) fail(err error) {
	x.failStatus(err, statusOf(err))
}

func statusOf(err error) int {
	var ne net.Error
	if errors.Is(err, errTimeout) || errors.As(err, &ne) && ne.Timeout() {
		return stdhttp.StatusGatewayTimeout
	}
	return stdhttp.StatusBadGateway
}

func (x *exchange) failStatus(err error, status int) {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	x.done = true
	x.stopTimers()
	c, started := x.conn, x.started
	x.mu.Unlock()
	if c != nil {
		c.close()
		c.done(false)
	}
	if !errors.Is(err, errClientGone) {
		if !started {
			_ = x.c.Respond(status, "text/plain; charset=utf-8", []byte(strconv.Itoa(status)+" "+stdhttp.StatusText(status)+"\n"))
		} else {
			x.broken()
		}
	}
	x.release()
}

// broken cuts short a response that failed after it began, so the client
// can tell, as the proxy does.
func (x *exchange) broken() {
	switch {
	case x.declared:
		_ = x.c.Finish()
	case x.c.Request.ProtoMajor == 1 && x.c.Conn != nil:
		_ = x.c.Conn.Close()
	default:
		x.c.Header().Set(stdhttp.TrailerPrefix+"Fit-Error", "the FastCGI application's response broke off")
		_ = x.c.Finish()
	}
}

// answer answers a request that never reached the application.
func (x *exchange) answer(status int) {
	x.mu.Lock()
	if x.done {
		x.mu.Unlock()
		return
	}
	x.done = true
	x.stopTimers()
	x.mu.Unlock()
	_ = x.c.Respond(status, "text/plain; charset=utf-8", []byte(strconv.Itoa(status)+" "+stdhttp.StatusText(status)+"\n"))
	x.release()
}

// stopTimers stops the exchange's timers. x.mu is held.
func (x *exchange) stopTimers() {
	if x.timer != nil {
		x.timer.Stop()
	}
	if x.overall != nil {
		x.overall.Stop()
	}
}

func (x *exchange) release() {
	x.c.Release()
	x.h.graph.Release()
}

// stderrLines counts the lines of the applications' standard error logged
// this second; past maxStderrLines they are dropped.
var (
	stderrLines  atomic.Int64
	stderrSecond atomic.Int64
)

const maxStderrLines = 100

func logStderr(addr string, p []byte) {
	if now := time.Now().Unix(); stderrSecond.Swap(now) != now {
		stderrLines.Store(0)
	}
	if stderrLines.Add(1) > maxStderrLines {
		return
	}
	msg := strings.TrimSpace(string(p[:min(len(p), 1024)]))
	log.Printf("fit: fastcgi %s: %s", addr, msg)
}
