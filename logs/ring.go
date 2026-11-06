package logs

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The bounds of the ring of recent messages a log keeps.
const (
	// RingSize is how many messages the ring keeps; one more removes the
	// oldest.
	RingSize = 1000
	// RingMessageBytes bounds a message as the ring keeps it, its
	// attributes included; a longer one is cut.
	RingMessageBytes = 2048
)

// Record is a message the log wrote, as its ring keeps it.
type Record struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Logger  string    `json:"logger"`
	Message string    `json:"message"`
}

// ring keeps the last RingSize messages the log wrote, for the workbench's
// assistant to read (its tool log_tail): a fixed array, written in a
// circle under a lock, so that it never grows.
type ring struct {
	mu   sync.Mutex
	recs [RingSize]Record
	next int // where the next record goes
	n    int // how many are kept
}

func (r *ring) add(rec Record) {
	r.mu.Lock()
	r.recs[r.next] = rec
	r.next = (r.next + 1) % RingSize
	if r.n < RingSize {
		r.n++
	}
	r.mu.Unlock()
}

// Filter says which of the ring's messages Recent returns.
type Filter struct {
	// Logger, when set, is the only logger whose messages are returned.
	Logger string
	// Level is the least level returned, as the config names it: debug,
	// info, warn or error; debug and up when "".
	Level string
	// Contains, when set, is text the message must hold, case ignored.
	Contains string
	// Limit is the most returned, the latest; 100 when zero, and never
	// more than RingSize.
	Limit int
}

// Recent returns the latest messages of the log's ring that f lets
// through, oldest first.
func (r *Root) Recent(f Filter) []Record {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, RingSize)
	contains := strings.ToLower(f.Contains)
	least := levelRank(f.Level)
	g := &r.ring
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Record
	for i := 0; i < g.n && len(out) < limit; i++ {
		rec := g.recs[(g.next-1-i+2*RingSize)%RingSize]
		if f.Logger != "" && rec.Logger != f.Logger || levelRank(rec.Level) < least {
			continue
		}
		if contains != "" && !strings.Contains(strings.ToLower(rec.Message), contains) {
			continue
		}
		out = append(out, rec)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// levelName returns a level's name as the config writes it.
func levelName(l slog.Level) string {
	switch {
	case l >= LevelError:
		return "error"
	case l >= LevelWarn:
		return "warn"
	case l >= LevelInfo:
		return "info"
	}
	return "debug"
}

// levelRank orders the levels' names.
func levelRank(name string) int {
	switch name {
	case "error":
		return 3
	case "warn":
		return 2
	case "info":
		return 1
	}
	return 0
}

// ringHandler hands each record its inner handler writes to the log's
// ring as well, with the logger's name and the attributes it carries.
type ringHandler struct {
	inner  slog.Handler
	ring   *ring
	logger string
	attrs  string // the attributes WithAttrs added, as " k=v"
}

func (h ringHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h ringHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)
	var b strings.Builder
	b.WriteString(r.Message)
	b.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, a)
		return b.Len() < RingMessageBytes
	})
	h.ring.add(Record{Time: r.Time, Level: levelName(r.Level), Logger: h.logger, Message: clipUTF8(b.String(), RingMessageBytes)})
	return err
}

func (h ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		appendAttr(&b, a)
	}
	return ringHandler{inner: h.inner.WithAttrs(attrs), ring: h.ring, logger: h.logger, attrs: clipUTF8(b.String(), RingMessageBytes)}
}

func (h ringHandler) WithGroup(name string) slog.Handler {
	return ringHandler{inner: h.inner.WithGroup(name), ring: h.ring, logger: h.logger, attrs: h.attrs}
}

func appendAttr(b *strings.Builder, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	b.WriteByte(' ')
	b.WriteString(a.Key)
	b.WriteByte('=')
	b.WriteString(a.Value.Resolve().String())
}

// clipUTF8 returns s cut to at most n bytes, on a rune's boundary.
func clipUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
