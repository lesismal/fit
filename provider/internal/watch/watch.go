// Package watch is the loop the providers that follow a service's API
// share: it keeps a watch on the source going and reads it whole when it
// changes. It runs in the control plane, on goroutines of its own.
package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lesismal/fit/logs"
)

// Timings, variables for tests to shorten.
var (
	// Debounce is how long a follower waits after a change for more,
	// before reading the source again.
	Debounce = 200 * time.Millisecond
	// MinBackoff and MaxBackoff bound the wait before trying the source
	// again after it failed.
	MinBackoff = time.Second
	MaxBackoff = 30 * time.Second
)

// A Source is what a Follower follows.
type Source interface {
	// Open readies a connection to the source for a watch and the reads
	// that go with it, as a client with its certificates loaded; close
	// ends it.
	Open(ctx context.Context) (close func(), err error)
	// Watch watches the source until ctx ends or the watch fails, which
	// it returns. It calls ready once the watch is in place, so that no
	// change after a read that follows is missed, and changed after each
	// change from then on; neither blocks.
	Watch(ctx context.Context, ready, changed func()) error
	// Read reads the source whole and returns the part it makes, and
	// why it left out what it did.
	Read(ctx context.Context) (part []byte, problems []string, err error)
}

// Follower follows a source and sends the parts it makes.
type Follower struct {
	Name     string        // the provider's
	Interval time.Duration // how often the source is read whether or not it changed
	Log      *logs.Logger  // hears what the follower reports
	Source   Source

	last     []byte          // the part sent last
	problems map[string]bool // the problems reported at the last read
}

// Run follows the source until ctx ends, calling update with the part it
// makes: at once, after each change, and every Interval whether or not it
// changed, so that a part refused because of the rest of the config is
// tried again. When the source cannot be reached, the last part stays, and
// the source is tried again, from MinBackoff to MaxBackoff apart.
func (f *Follower) Run(ctx context.Context, update func([]byte)) {
	backoff := MinBackoff
	for {
		read, err := f.follow(ctx, update)
		if ctx.Err() != nil {
			return
		}
		if read {
			backoff = MinBackoff
		}
		f.Log.Warnf("provider %s: %v; trying again in %s", f.Name, err, backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(2*backoff, MaxBackoff)
	}
}

// follow watches the source, and reads it once the watch is in place,
// after changes and every Interval, until the source fails or ctx ends.
// It reports whether a read succeeded.
func (f *Follower) follow(ctx context.Context, update func([]byte)) (read bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closeSource, err := f.Source.Open(ctx)
	if err != nil {
		return false, err
	}
	defer closeSource()
	ready, changed := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	failed, watched := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(watched)
		failed <- f.Source.Watch(ctx, func() { once.Do(func() { close(ready) }) }, func() {
			select {
			case changed <- struct{}{}:
			default: // one is pending already
			}
		})
	}()
	defer func() {
		cancel()
		<-watched // the watch is done with the source before it closes
	}()
	ended := func(err error) error {
		if err == nil {
			err = errors.New("the watch ended")
		}
		return fmt.Errorf("watch: %w", err)
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case err := <-failed:
		return false, ended(err)
	case <-ready:
	}
	if err := f.refresh(ctx, update, true); err != nil {
		return false, err
	}
	tick := time.NewTicker(f.Interval)
	defer tick.Stop()
	var wait *time.Timer
	var fire <-chan time.Time
	defer func() {
		if wait != nil {
			wait.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-failed:
			return true, ended(err)
		case <-changed:
			if fire == nil {
				wait = time.NewTimer(Debounce)
				fire = wait.C
			}
		case <-fire:
			fire = nil
			if err := f.refresh(ctx, update, false); err != nil {
				return true, err
			}
		case <-tick.C:
			if err := f.refresh(ctx, update, true); err != nil {
				return true, err
			}
		}
	}
}

// refresh reads the source and sends the part it makes, when it changed
// or always is set, reporting each problem once while it lasts.
func (f *Follower) refresh(ctx context.Context, update func([]byte), always bool) error {
	part, problems, err := f.Source.Read(ctx)
	if err != nil {
		return err
	}
	reported := make(map[string]bool, len(problems))
	for _, msg := range problems {
		if !f.problems[msg] {
			f.Log.Warnf("provider %s: %s", f.Name, msg)
		}
		reported[msg] = true
	}
	f.problems = reported
	if always || !bytes.Equal(part, f.last) {
		f.last = part
		update(part)
	}
	return nil
}
