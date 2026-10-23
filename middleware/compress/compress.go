// Package compress has the compress middleware, which compresses responses
// with zstd, brotli or gzip, as the request's Accept-Encoding allows, as
// they are written: a response that streams stays streamed, and what the
// handler flushes reaches the client, through fib's body filters.
package compress

import (
	"bytes"
	"io"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	fibhttp "github.com/lesismal/fib/http"
	fibcompress "github.com/lesismal/fib/middleware/compress"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".compress", New: func() any { return new(Compress) }, Doc: "Compresses responses with zstd, brotli or gzip as they are written."})
}

// The encodings, by their Content-Encoding names.
const (
	Zstd   = "zstd"
	Brotli = "br"
	Gzip   = "gzip"
)

// Defaults of Compress's params.
const (
	DefaultMinBytes    = 1024
	DefaultMaxInFlight = 1024
)

// Compress compresses response bodies. A body is compressed when the
// client accepts one of encodings, its status carries a body and is not 206,
// it has no Content-Encoding or Content-Range of its own, its Cache-Control
// does not say no-transform, its Content-Type is one that compresses (text,
// JSON, JavaScript, XML, SVG, fonts and the like, but not images, video or
// archives, which are compressed already), and its declared length, if it
// declares one, is at least min_bytes. Such a response says so in Vary, and
// a strong ETag it carries is made weak.
type Compress struct {
	Encodings   []string `json:"encodings,omitempty" enum:"zstd,br,gzip" default:"[\"zstd\",\"br\",\"gzip\"]" doc:"The encodings offered, the one the client prefers most chosen, and among those it prefers as much, the first here; zstd, br and gzip when left out."`
	Level       string   `json:"level,omitempty" enum:"fastest,default,better" default:"\"default\"" doc:"How hard the compressors work: fastest, default (gzip 5, zstd's default, brotli 4) or better (gzip 7, zstd's better, brotli 6); default when left out."`
	MinBytes    int      `json:"min_bytes,omitempty" renamed:"min_length" min:"0" default:"1024" doc:"The shortest declared body compressed; 1024 when left out. A body that declares no length, as one that streams, is compressed."`
	MaxInFlight int      `json:"max_in_flight,omitempty" min:"1" default:"1024" doc:"How many responses are compressed at once; past it, responses go uncompressed, so that the compressors' memory, a few hundred kilobytes each, is bounded. 1024 when left out."`

	encodings []string
	level     int // 0 fastest, 1 default, 2 better
	pools     map[string]*sync.Pool
	inFlight  atomic.Int64
}

// Provision checks the params and makes the compressors' pools.
func (cm *Compress) Provision(ctx *module.Context) error {
	cm.encodings = cm.Encodings
	if len(cm.encodings) == 0 {
		cm.encodings = []string{Zstd, Brotli, Gzip}
	}
	for i, e := range cm.encodings {
		if slices.Index(cm.encodings, e) < i {
			return ctx.Params.Field("encodings").Index(i).Errorf("%q is listed twice", e)
		}
	}
	switch cm.Level {
	case "fastest":
		cm.level = 0
	case "", "default":
		cm.level = 1
	case "better":
		cm.level = 2
	}
	if cm.MinBytes == 0 {
		cm.MinBytes = DefaultMinBytes
	}
	if cm.MaxInFlight == 0 {
		cm.MaxInFlight = DefaultMaxInFlight
	}
	cm.pools = map[string]*sync.Pool{}
	for _, e := range cm.encodings {
		cm.pools[e] = &sync.Pool{New: func() any { return cm.newEncoder(e) }}
	}
	return nil
}

// encoder is a compressor that writes into its own buffer.
type encoder struct {
	buf bytes.Buffer
	w   interface {
		io.WriteCloser
		Flush() error
	}
	reset func(io.Writer)
}

// The compressors' windows are kept small, so that each one under way costs
// a few hundred kilobytes, not megabytes.
const window = 256 << 10

func (cm *Compress) newEncoder(encoding string) *encoder {
	e := &encoder{}
	switch encoding {
	case Gzip:
		level := [...]int{gzip.BestSpeed, 5, 7}[cm.level]
		w, _ := gzip.NewWriterLevel(&e.buf, level)
		e.w, e.reset = w, w.Reset
	case Zstd:
		level := [...]zstd.EncoderLevel{zstd.SpeedFastest, zstd.SpeedDefault, zstd.SpeedBetterCompression}[cm.level]
		w, _ := zstd.NewWriter(&e.buf, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(window), zstd.WithLowerEncoderMem(true))
		e.w, e.reset = w, func(dst io.Writer) { w.Reset(dst) }
	case Brotli:
		quality := [...]int{1, 4, 6}[cm.level]
		w := brotli.NewWriterOptions(&e.buf, brotli.WriterOptions{Quality: quality, LGWin: 18})
		e.w, e.reset = w, w.Reset
	}
	return e
}

// Wrap returns next behind the middleware.
func (cm *Compress) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		accept := c.Request.Header["Accept-Encoding"]
		head := c.Request.Method == stdhttp.MethodHead
		c.OnBodyFilter(func(status int, h stdhttp.Header) fibhttp.BodyFilter {
			if !cm.eligible(status, h) {
				return nil
			}
			addVary(h)
			if head {
				return nil
			}
			encoding := negotiate(accept, cm.encodings)
			if encoding == "" {
				return nil
			}
			if cm.inFlight.Add(1) > int64(cm.MaxInFlight) {
				cm.inFlight.Add(-1)
				return nil
			}
			f := &filter{cm: cm, pool: cm.pools[encoding]}
			f.enc = f.pool.Get().(*encoder)
			f.enc.buf.Reset()
			f.enc.reset(&f.enc.buf)
			h["Content-Encoding"] = []string{encoding}
			if tag := h.Get("Etag"); tag != "" && !strings.HasPrefix(tag, "W/") {
				h["Etag"] = []string{"W/" + tag}
			}
			return f
		})
		next.ServeHTTP(c)
	})
}

// eligible reports whether a response of status and h may be compressed.
func (cm *Compress) eligible(status int, h stdhttp.Header) bool {
	if status < 200 || status == stdhttp.StatusNoContent || status == stdhttp.StatusNotModified ||
		status == stdhttp.StatusPartialContent {
		return false
	}
	if e := h.Get("Content-Encoding"); e != "" && !strings.EqualFold(e, "identity") {
		return false
	}
	if _, ok := h["Content-Range"]; ok {
		return false
	}
	for _, v := range h["Cache-Control"] {
		if strings.Contains(strings.ToLower(v), "no-transform") {
			return false
		}
	}
	if v := h.Get("Content-Length"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n < int64(cm.MinBytes) {
			return false
		}
	}
	mediaType, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType != "" && fibcompress.Compressible(mediaType)
}

// addVary adds Accept-Encoding to h's Vary, unless it is there.
func addVary(h stdhttp.Header) {
	for _, v := range h["Vary"] {
		for name := range strings.SplitSeq(v, ",") {
			if name = strings.TrimSpace(name); name == "*" || strings.EqualFold(name, "Accept-Encoding") {
				return
			}
		}
	}
	h["Vary"] = append(h["Vary"], "Accept-Encoding")
}

// negotiate picks the encoding of offered the Accept-Encoding fields prefer
// most, the first of offered among those they prefer as much, or "" when
// they accept none of them.
func negotiate(fields []string, offered []string) string {
	q := map[string]float64{}
	anyQ := -1.0
	for _, field := range fields {
		for member := range strings.SplitSeq(field, ",") {
			name, params, _ := strings.Cut(member, ";")
			name = strings.ToLower(strings.TrimSpace(name))
			weight := 1.0
			for param := range strings.SplitSeq(params, ";") {
				if key, value, ok := strings.Cut(strings.TrimSpace(param), "="); ok && strings.EqualFold(strings.TrimSpace(key), "q") {
					if v, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
						weight = v
					}
				}
			}
			switch name {
			case "*":
				anyQ = weight
			case "x-gzip":
				q[Gzip] = weight
			case "":
			default:
				q[name] = weight
			}
		}
	}
	best, bestQ := "", 0.0
	for _, e := range offered {
		w, ok := q[e]
		if !ok {
			w = anyQ
		}
		if w > bestQ {
			best, bestQ = e, w
		}
	}
	return best
}

// filter compresses one response's body. fib calls its methods one at a
// time, and ends it with Close, or with Abort when the response ends without
// its body ending, as one whose client goes first does.
type filter struct {
	cm   *Compress
	pool *sync.Pool
	enc  *encoder
}

func (f *filter) Filter(p []byte) ([]byte, error) {
	f.enc.buf.Reset()
	_, err := f.enc.w.Write(p)
	return f.enc.buf.Bytes(), err
}

func (f *filter) Flush() ([]byte, error) {
	f.enc.buf.Reset()
	err := f.enc.w.Flush()
	return f.enc.buf.Bytes(), err
}

// Close ends the body and gives the compressor back; what is left is
// copied, since the compressor's buffer goes back with it.
func (f *filter) Close() ([]byte, error) {
	f.enc.buf.Reset()
	err := f.enc.w.Close()
	tail := bytes.Clone(f.enc.buf.Bytes())
	f.Abort(nil)
	return tail, err
}

// Abort gives the compressor and the response's place back.
func (f *filter) Abort(error) {
	f.enc.buf.Reset()
	f.pool.Put(f.enc)
	f.enc = nil
	f.cm.inFlight.Add(-1)
}

// InFlight returns how many responses are being compressed.
func (cm *Compress) InFlight() int { return int(cm.inFlight.Load()) }
