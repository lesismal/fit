package static

import (
	"bytes"
	"math/bits"
	stdhttp "net/http"
	"strings"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	fibcompress "github.com/lesismal/fib/middleware/compress"
)

// A file kept in memory is kept compressed too, with each of the handler's
// compress encodings a client asks for, made once on the file pool and
// served from then on to the clients that accept it, so that no request
// compresses it again. These are the bounds of what is compressed and kept.
const (
	// compressMinBytes is the smallest file compressed: one smaller goes
	// in one packet with its header either way.
	compressMinBytes = 1400
	// The levels: those that make the smallest copy at a cost paid once,
	// short of brotli's 10 and 11, many times slower than 9 for a few
	// hundredths smaller.
	gzipPackLevel   = gzip.BestCompression
	brotliPackLevel = 9
)

// The states of a compressed copy besides one made: being made, and not
// kept, for a file that compressed by less than a twentieth or that the
// cache had no room for. The entry's file being replaced makes a new entry,
// with none made.
var (
	packing  = &entry{}
	unpacked = &entry{}
)

// packable reports whether a file of data, whose header is h, is worth
// compressing: large enough, and of a type that compresses.
func packable(h stdhttp.Header, data []byte) bool {
	if len(data) < compressMinBytes {
		return false
	}
	mediaType, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType != "" && fibcompress.Compressible(mediaType)
}

// compressed returns what to serve r's client of e, the file at full kept
// in memory and to go out as v: e's copy compressed with the encoding of the
// handler's compress the client prefers, once made; or e itself, and the
// copy is made on the file pool for the requests after it. A twin, a ranged
// request and a file not worth compressing get e.
func (s *Static) compressed(r *stdhttp.Request, full string, e *entry, v variant) *entry {
	if e.packs == nil || v.encoding != "" {
		return e
	}
	if _, ok := r.Header["Range"]; ok {
		return e
	}
	accept := r.Header["Accept-Encoding"]
	if len(accept) == 0 {
		return e
	}
	best, bestQ := -1, 0.0
	for i, enc := range s.compress {
		if e.packs[i].Load() == unpacked {
			continue
		}
		if q := acceptQ(accept, enc.name); q > bestQ {
			best, bestQ = i, q
		}
	}
	if best < 0 {
		return e
	}
	slot := &e.packs[best]
	switch p := slot.Load(); p {
	case nil:
		if slot.CompareAndSwap(nil, packing) {
			s.pack(full, e, best)
		}
	case packing:
	default:
		return p
	}
	return e
}

// pack makes e's copy compressed with the handler's i-th compress encoding
// on the file pool, and keeps it in e if the cache still holds e and has
// room for it. A job the pool refuses or lets expire leaves the copy to be
// made for a later request.
func (s *Static) pack(full string, e *entry, i int) {
	slot := &e.packs[i]
	enc := s.compress[i]
	run := func() {
		data := packBytes(enc.name, e.data)
		if len(data) == 0 || len(data) > len(e.data)-len(e.data)/20 {
			slot.Store(unpacked)
			return
		}
		h := make(stdhttp.Header, len(e.header)+1)
		copyHeader(h, e.header)
		h["Content-Encoding"] = []string{enc.name}
		// The copy is another representation: its own tag, and no ranges,
		// which a ranged request is answered from the file as it is.
		h["Etag"] = []string{strings.TrimSuffix(e.header.Get("Etag"), `"`) + "-" + enc.name + `"`}
		delete(h, "Accept-Ranges")
		p := &entry{data: data, header: h, modTime: e.modTime, size: int64(len(data)), stamp: e.stamp}
		if !s.cache.keepPacked(full, e, p) {
			slot.Store(unpacked)
			return
		}
		slot.Store(p)
	}
	expire := func() { slot.CompareAndSwap(packing, nil) }
	if !s.pool.Submit(run, expire) {
		expire()
	}
}

// keepPacked counts p, a compressed copy of e, the file at full, in the
// cache's bytes, and reports whether it may be kept: whether the cache still
// holds e and has room for p.
func (c *cache) keepPacked(full string, e, p *entry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries[full] != e || c.bytes+p.size > c.maxBytes {
		return false
	}
	c.bytes += p.size
	e.size += p.size
	return true
}

// packBytes compresses data whole with encoding, or returns nil.
func packBytes(encoding string, data []byte) []byte {
	var b bytes.Buffer
	switch encoding {
	case "gzip":
		w, _ := gzip.NewWriterLevel(&b, gzipPackLevel)
		_, _ = w.Write(data)
		_ = w.Close()
	case "br":
		// A window no larger than the file, which bounds the encoder's
		// memory by it.
		w := brotli.NewWriterOptions(&b, brotli.WriterOptions{Quality: brotliPackLevel, LGWin: windowBits(len(data), 10, 24)})
		_, _ = w.Write(data)
		_ = w.Close()
	case "zstd":
		w, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(1<<windowBits(len(data), 10, 24)), zstd.WithLowerEncoderMem(true))
		data = w.EncodeAll(data, nil)
		_ = w.Close()
		return data
	}
	return b.Bytes()
}

// windowBits is the log2 of the smallest window, within [lo, hi] bits, that
// holds n bytes.
func windowBits(n, lo, hi int) int {
	return min(max(bits.Len(uint(n-1)), lo), hi)
}

// packSlots returns the slots of a file's compressed copies, one for each
// of the handler's compress encodings, or nil for a file not worth
// compressing or a handler that compresses none.
func packSlots(n int, h stdhttp.Header, data []byte) []atomic.Pointer[entry] {
	if n == 0 || !packable(h, data) {
		return nil
	}
	return make([]atomic.Pointer[entry], n)
}
