package node

import (
	"bytes"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
)

// blockedOnPusher reports whether a goroutine waits for a mutex inside the pusher.
func blockedOnPusher() bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for _, g := range bytes.Split(buf, []byte("\n\n")) {
		if bytes.Contains(g, []byte("[sync.Mutex.Lock")) && bytes.Contains(g, []byte("(*pusher).")) {
			return true
		}
	}
	return false
}

// An Info and a push racing: the first one to take its snapshot is held
// between its snapshot and its Seq while the node changes and the other one
// runs. The info with the higher Seq, the one a hub keeps, must carry the
// change: a push skips a state it already pushed, so a hub keeping an older
// state under a higher Seq would never be corrected.
func TestInfoPushOrder(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, then string // "info" or "push"
	}{
		{"info then push", "info", "push"},
		{"push then info", "push", "info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delay := pushDelay
			t.Cleanup(func() { pushDelay = delay })
			pushDelay = 1 << 62 // the loop never pushes; the test does
			n := startNode(t, &config.LLMNode{}, nil)
			p := n.push

			run := func(which string) model.NodeInfo {
				if which == "info" {
					return n.Info()
				}
				info, ok := p.next()
				if !ok {
					t.Error("push skipped")
				}
				return info
			}

			held, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			p.snapped = func() {
				if calls.Add(1) == 1 {
					close(held)
					<-release
				}
			}
			firstDone := make(chan model.NodeInfo, 1)
			go func() { firstDone <- run(tc.first) }()
			<-held

			n.mu.Lock()
			n.description = "changed"
			n.mu.Unlock()
			thenDone := make(chan model.NodeInfo, 1)
			go func() { thenDone <- run(tc.then) }()
			// Let the second one finish, or wait until it cannot go on.
			for done := false; !done; {
				select {
				case info := <-thenDone:
					thenDone <- info
					done = true
				default:
					done = blockedOnPusher()
					runtime.Gosched()
				}
			}
			close(release)
			a, b := <-firstDone, <-thenDone

			newest := a
			if b.Seq > a.Seq {
				newest = b
			}
			if a.Seq == b.Seq || newest.Description != "changed" {
				t.Fatalf("%s: seq %d desc %q, %s: seq %d desc %q: the highest Seq must carry the change",
					tc.first, a.Seq, a.Description, tc.then, b.Seq, b.Description)
			}
		})
	}
}
