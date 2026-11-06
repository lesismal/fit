package cluster

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestReads reads members' node, status, versions and check through the
// manager, by console and by token, and checks what is refused: other
// reads and methods, members there are none of, reads a member does not
// list, too many at once, and members that cannot be read, their problems
// without a credential.
func TestReads(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	plain, tok := newFakeMember(t, false), newFakeMember(t, false)
	f.add(consoleInput("a", plain.srv.URL))
	f.add(tokenInput("b", tok.srv.URL))
	f.waitUp("a")
	f.waitUp("b")
	read := func(name, what string, want int) string {
		t.Helper()
		code, body, h := s.do("GET", "/api/m/cluster/nodes/"+name+"/read/"+what, "")
		if code != want {
			t.Fatalf("%s %s: %d %s", name, what, code, body)
		}
		if want == 200 && h.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s: %s", name, what, h.Get("Content-Type"))
		}
		if strings.Contains(body, fakePassword) || strings.Contains(body, fakeToken) {
			t.Errorf("%s %s: a credential in %s", name, what, body)
		}
		return body
	}
	for _, name := range []string{"a", "b"} {
		for _, what := range ReadNames() {
			body := read(name, what, 200)
			if !json.Valid([]byte(body)) {
				t.Errorf("%s %s: %s", name, what, body)
			}
		}
		if got := read(name, "status", 200); !strings.Contains(got, `"listeners"`) || !strings.Contains(got, `"backends"`) {
			t.Errorf("%s", got)
		}
		if got := read(name, "versions", 200); !strings.Contains(got, `"current":7`) {
			t.Errorf("%s", got)
		}
	}

	// A console's session that ended is logged in again.
	plain.restart()
	read("a", "check", 200)

	// What is refused.
	read("a", "config", 404)
	read("zz", "status", 404)
	if code, body, h := s.do("POST", "/api/m/cluster/nodes/a/read/status", "{}"); code != 405 || h.Get("Allow") != "GET" {
		t.Errorf("%d %s", code, body)
	}
	for range MaxReads {
		f.m.reads <- struct{}{}
	}
	read("a", "status", 429)
	for range MaxReads {
		<-f.m.reads
	}

	// A member that does not list a read is not asked for it.
	bare := newFakeMember(t, false)
	bare.set(func(f *fakeMember) { f.caps = []string{"node"} })
	f.add(tokenInput("bare", bare.srv.URL))
	f.waitUp("bare")
	before := bare.requests.Load()
	if got := read("bare", "versions", 502); !strings.Contains(got, `"kind":"unsupported"`) {
		t.Errorf("%s", got)
	}
	if bare.requests.Load() != before {
		t.Error("the member was asked for what it does not list")
	}

	// A member's refusal and a member down are problems, the credentials
	// taken out of their messages.
	tok.set(func(f *fakeMember) { f.nodeStatus = 500 })
	if got := read("b", "node", 502); !strings.Contains(got, `"kind":"http_status"`) || !strings.Contains(got, "••••") {
		t.Errorf("%s", got)
	}
	f.add(consoleInput("down", downURL(t)))
	if got := read("down", "status", 502); !strings.Contains(got, `"kind":"unreachable"`) {
		t.Errorf("%s", got)
	}
	plain.set(func(f *fakeMember) { f.expired = true })
	plain.restart()
	if got := read("a", "status", 502); !strings.Contains(got, `"kind":"auth_failed"`) {
		t.Errorf("%s", got)
	}
	// A login refused waits its backoff: the next read is answered the
	// same without a login.
	logins, _, _ := plain.counts()
	read("a", "status", 502)
	if l, _, _ := plain.counts(); l != logins {
		t.Errorf("logged in %d times more", l-logins)
	}
}

// TestReadLogsIn reads a console member that has no session: the read logs
// in first, and the session is the node's after it.
func TestReadLogsIn(t *testing.T) {
	f := newFixture(t, config.Cluster{PollInterval: config.Duration(time.Hour)})
	s := f.serve()
	m := newFakeMember(t, false)
	f.add(consoleInput("a", m.srv.URL))
	f.waitUp("a")
	f.m.mu.Lock()
	n := f.m.nodes["a"]
	f.m.mu.Unlock()
	n.dropSession(n.currentSession())
	logins, _, _ := m.counts()
	if code, body, _ := s.do("GET", "/api/m/cluster/nodes/a/read/node", ""); code != 200 || !strings.Contains(body, `"id"`) {
		t.Fatalf("%d %s", code, body)
	}
	if l, _, _ := m.counts(); l != logins+1 || n.currentSession() == nil {
		t.Errorf("logins %d → %d, session %v", logins, l, n.currentSession())
	}
}
