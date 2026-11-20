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

// Defaults of Compress's params. A body of fewer than DefaultMinBytes goes
// in one TCP segment of a 1500-byte MTU with its header, compressed or not,
// so compressing it saves no packet and costs several times what sending it
// does.
const (
	DefaultMinBytes    = 1400
	DefaultMaxInFlight = 1024
)

// The levels of each encoding the named levels stand for: fastest, default
// and better, as indexes. zstd's are on its own scale of 1 to 22, which the
// encoder runs at four speeds: 1-2 its fastest, 3-5 its default, 6-9 its
// better and 10-22 its best.
var (
	gzipLevels   = [...]int{1, 5, 7}
	brotliLevels = [...]int{1, 4, 6}
	zstdLevels   = [...]int{1, 3, 6}
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
	Encodings   []string `json:"encodings,omitempty" enum:"zstd,br,gzip" default:"[\"zstd\",\"gzip\",\"br\"]" doc:"The encodings offered, the one the client prefers most chosen, and among those it prefers as much, the first here; zstd, gzip and br when left out, gzip ahead of br since at the levels a response is compressed at as it goes, brotli is slower than gzip for about the same size."`
	Level       string   `json:"level,omitempty" enum:"fastest,default,better" default:"\"fastest\"" doc:"How hard the compressors work: fastest (gzip 1, brotli 1, zstd 1), default (gzip 5, brotli 4, zstd 3) or better (gzip 7, brotli 6, zstd 6); fastest when left out. gzip_level, brotli_level and zstd_level set one encoding's level by number instead."`
	GzipLevel   int      `json:"gzip_level,omitempty" min:"1" max:"9" doc:"gzip's level, 1 to 9, in place of the one level gives it: 1 is fastest, 5 default, 7 better and 9 the smallest."`
	BrotliLevel int      `json:"brotli_level,omitempty" min:"1" max:"11" doc:"brotli's quality, 1 to 11, in place of the one level gives it: 1 is fastest, 4 default, 6 better and 11 the smallest, many times slower."`
	ZstdLevel   int      `json:"zstd_level,omitempty" min:"1" max:"22" doc:"zstd's level, 1 to 22, in place of the one level gives it: 1 is fastest, 3 default, 6 better and 10 the smallest. The encoder has four speeds, which the levels fall into: 1-2, 3-5, 6-9 and 10-22."`
	MinBytes    int      `json:"min_bytes,omitempty" renamed:"min_length" min:"0" default:"1400" doc:"The shortest declared body compressed; 1400 when left out, below which a response goes in one packet either way. A body that declares no length, as one that streams, is compressed."`
	MaxInFlight int      `json:"max_in_flight,omitempty" min:"1" default:"1024" doc:"How many responses are compressed at once; past it, responses go uncompressed, so that the compressors' memory, a few hundred kilobytes each, is bounded. 1024 when left out."`

	encodings []string
	// The levels the encoders are made with.
	gzipLevel, brotliLevel, zstdLevel int
	pools                             map[string]*sync.Pool
	inFlight                          atomic.Int64
}

// Provision checks the params and makes the compressors' pools.
func (cm *Compress) Provision(ctx *module.Context) error {
	cm.encodings = cm.Encodings
	if len(cm.encodings) == 0 {
		cm.encodings = []string{Zstd, Gzip, Brotli}
	}
	for i, e := range cm.encodings {
		if slices.Index(cm.encodings, e) < i {
			return ctx.Params.Field("encodings").Index(i).Errorf("%q is listed twice", e)
		}
	}
	level := 0
	switch cm.Level {
	case "", "fastest":
	case "default":
		level = 1
	case "better":
		level = 2
	}
	cm.gzipLevel = levelOf(cm.GzipLevel, gzipLevels[level])
	cm.brotliLevel = levelOf(cm.BrotliLevel, brotliLevels[level])
	cm.zstdLevel = levelOf(cm.ZstdLevel, zstdLevels[level])
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

// levelOf is an encoding's level: set, or the one the named level gives
// it, named, when set is 0. The schema keeps set within the encoding's range.
func levelOf(set, named int) int {
	if set == 0 {
		return named
	}
	return set
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
		w, _ := gzip.NewWriterLevel(&e.buf, cm.gzipLevel)
		e.w, e.reset = w, w.Reset
	case Zstd:
		w, _ := zstd.NewWriter(&e.buf, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(cm.zstdLevel)),
			zstd.WithEncoderConcurrency(1), zstd.WithWindowSize(window), zstd.WithLowerEncoderMem(true))
		e.w, e.reset = w, func(dst io.Writer) { w.Reset(dst) }
	case Brotli:
		w := brotli.NewWriterOptions(&e.buf, brotli.WriterOptions{Quality: cm.brotliLevel, LGWin: 18})
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
// time, and ends it with Close and then Release, once what Close returned is
// sent or copied, or with Abort when the response ends without its body
// ending, as one whose client goes first does.
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

// Close ends the body. What is left is in the compressor's buffer, which
// Release gives back once fib has sent or copied it.
func (f *filter) Close() ([]byte, error) {
	f.enc.buf.Reset()
	err := f.enc.w.Close()
	return f.enc.buf.Bytes(), err
}

// Release gives the compressor and the response's place back, after Close.
func (f *filter) Release() { f.Abort(nil) }

// Abort gives the compressor and the response's place back.
func (f *filter) Abort(error) {
	f.enc.buf.Reset()
	f.pool.Put(f.enc)
	f.enc = nil
	f.cm.inFlight.Add(-1)
}

// InFlight returns how many responses are being compressed.
func (cm *Compress) InFlight() int { return int(cm.inFlight.Load()) }
