package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// endingSource is a source whose watch ends at once, without an error.
type endingSource struct{}

func (endingSource) Open(context.Context) (func(), error) { return func() {}, nil }

func (endingSource) Watch(_ context.Context, ready, _ func()) error {
	ready()
	return nil
}

func (endingSource) Read(context.Context) ([]byte, []string, error) { return []byte("p"), nil, nil }

func waitLog(t *testing.T, l *logs, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(l.all(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("logs lack %q:\n%s", want, l.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFollowerWatchEnds(t *testing.T) {
	shorten(t)
	var l logs
	f := &Follower{Name: "p", Interval: time.Hour, Logf: l.logf, Source: endingSource{}}
	background(t, f.Run, func([]byte) {})
	waitLog(t, &l, "provider p: watch: the watch ended; trying again")
}

// TestFollowerReadFailsAfterChange checks that a read after a change that
// fails starts the watch over.
func TestFollowerReadFailsAfterChange(t *testing.T) {
	shorten(t)
	src := &fakeSource{part: "p", watching: make(chan *fakeWatch, 16)}
	var l logs
	f := &Follower{Name: "p", Interval: time.Hour, Logf: l.logf, Source: src}
	parts := make(chan []byte, 16)
	background(t, f.Run, func(p []byte) { parts <- p })
	if p := next(t, parts); p != "p" {
		t.Fatalf("part: %q", p)
	}
	w := <-src.watching
	src.mu.Lock()
	src.readErr = errors.New("read failed")
	src.mu.Unlock()
	w.changed()
	waitLog(t, &l, "provider p: read failed; trying again")
}
