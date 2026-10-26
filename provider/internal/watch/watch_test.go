package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/logs"
)

// fakeSource is a source whose watch the test drives.
type fakeSource struct {
	mu       sync.Mutex
	part     string
	problems []string
	readErr  error
	openErr  error
	opens    int
	reads    int
	watching chan *fakeWatch // each watch, once it is in place
}

type fakeWatch struct {
	changed func()
	fail    chan error
}

func (s *fakeSource) Open(context.Context) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	if s.openErr != nil {
		return nil, s.openErr
	}
	return func() {}, nil
}

func (s *fakeSource) Watch(ctx context.Context, ready, changed func()) error {
	w := &fakeWatch{changed: changed, fail: make(chan error, 1)}
	ready()
	s.watching <- w
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-w.fail:
		return err
	}
}

func (s *fakeSource) Read(context.Context) ([]byte, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.readErr != nil {
		return nil, nil, s.readErr
	}
	return []byte(s.part), s.problems, nil
}

func (s *fakeSource) set(part string, problems ...string) {
	s.mu.Lock()
	s.part, s.problems = part, problems
	s.mu.Unlock()
}

func (s *fakeSource) count() (opens, reads int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens, s.reads
}

// background runs run until the test ends, and waits for it to return.
func background(t *testing.T, run func(context.Context, func([]byte)), update func([]byte)) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, update)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func shorten(t *testing.T) {
	d, lo, hi := Debounce, MinBackoff, MaxBackoff
	Debounce, MinBackoff, MaxBackoff = 20*time.Millisecond, 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { Debounce, MinBackoff, MaxBackoff = d, lo, hi })
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logLines) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func next(t *testing.T, ch <-chan []byte) string {
	t.Helper()
	select {
	case p := <-ch:
		return string(p)
	case <-time.After(10 * time.Second):
		t.Fatal("no part")
		return ""
	}
}

func TestFollower(t *testing.T) {
	shorten(t)
	src := &fakeSource{part: "one", problems: []string{"bad key"}, watching: make(chan *fakeWatch, 4)}
	var l logLines
	f := &Follower{Name: "p", Interval: time.Hour, Log: logs.Func(l.logf), Source: src}
	parts := make(chan []byte, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx, func(p []byte) { parts <- p })
	}()

	if p := next(t, parts); p != "one" {
		t.Fatalf("at the start: %q", p)
	}
	w := <-src.watching
	// Several changes make one read, and a part.
	src.set("two", "bad key")
	_, before := src.count()
	for range 5 {
		w.changed()
	}
	if p := next(t, parts); p != "two" {
		t.Fatalf("after changes: %q", p)
	}
	if _, after := src.count(); after != before+1 {
		t.Errorf("five changes made %d reads", after-before)
	}
	// A change that changes nothing sends nothing.
	w.changed()
	select {
	case p := <-parts:
		t.Fatalf("an unchanged read sent %q", p)
	case <-time.After(100 * time.Millisecond):
	}

	// The watch fails: the follower reports it, watches again and sends
	// the part at once, unchanged as it is.
	w.fail <- errors.New("lost")
	if p := next(t, parts); p != "two" {
		t.Fatalf("after the watch came back: %q", p)
	}
	w = <-src.watching

	// The source fails to open: tried again until it opens.
	src.mu.Lock()
	src.openErr = errors.New("refused")
	src.mu.Unlock()
	w.fail <- errors.New("lost again")
	time.Sleep(100 * time.Millisecond)
	src.mu.Lock()
	src.openErr = nil
	src.part = "three"
	src.mu.Unlock()
	if p := next(t, parts); p != "three" {
		t.Fatalf("after the source came back: %q", p)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	all := l.all()
	if strings.Count(all, "provider p: bad key") != 1 {
		t.Errorf("a lasting problem is reported once:\n%s", all)
	}
	for _, want := range []string{"provider p: watch: lost; trying again", "provider p: refused; trying again"} {
		if !strings.Contains(all, want) {
			t.Errorf("logs lack %q:\n%s", want, all)
		}
	}
}

// TestFollowerInterval checks that the part is sent every Interval whether
// or not it changed, and that a failed read starts the watch over.
func TestFollowerInterval(t *testing.T) {
	shorten(t)
	src := &fakeSource{part: "p", watching: make(chan *fakeWatch, 16)}
	var l logLines
	f := &Follower{Name: "p", Interval: 20 * time.Millisecond, Log: logs.Func(l.logf), Source: src}
	parts := make(chan []byte, 16)
	background(t, f.Run, func(p []byte) { parts <- p })
	for range 4 {
		if p := next(t, parts); p != "p" {
			t.Fatalf("part: %q", p)
		}
	}
	src.mu.Lock()
	src.readErr = errors.New("read failed")
	src.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if opens, _ := src.count(); opens >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watch was not started over:\n%s", l.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(l.all(), "provider p: read failed; trying again") {
		t.Errorf("logs:\n%s", l.all())
	}
}
