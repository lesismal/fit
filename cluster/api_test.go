package cluster

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestNodesAPI adds, reads, changes, tests and removes members through the
// console module, and checks that no credential is ever answered or
// logged.
func TestNodesAPI(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxNodes: 3})
	s := f.serve()
	plain, secure := newFakeMember(t, false), newFakeMember(t, true)
	var answers []string
	call := func(method, path, body string, want int, header ...string) string {
		t.Helper()
		code, got, _ := s.do(method, "/api/m/cluster"+path, body, header...)
		if code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, got)
		}
		answers = append(answers, got)
		return got
	}

	call("POST", "/nodes", js(consoleInput("a", plain.srv.URL)), 201)
	call("POST", "/nodes", js(consoleInput("a", plain.srv.URL)), 409)
	call("POST", "/nodes", js(tokenInput("b", secure.srv.URL)), 201)
	if got := call("POST", "/nodes", `{"name":"bad name","url":"x"}`, 400); !strings.Contains(got, `"field":"name"`) {
		t.Errorf("a bad name: %s", got)
	}
	call("POST", "/nodes", `{`, 400)
	call("POST", "/nodes", `{"name":"c","url":"`+strings.Repeat("x", MaxBodyBytes)+`"}`, 413)

	// The list masks the credentials, and carries its version.
	code, list, h := s.do("GET", "/api/m/cluster/nodes", "")
	if code != 200 || h.Get("ETag") == "" || !strings.Contains(list, `"password_set":true`) || !strings.Contains(list, `"token_set":true`) {
		t.Fatalf("%d %s %v", code, list, h)
	}
	answers = append(answers, list)
	etag := h.Get("ETag")

	// b is not trusted: its test shows the certificate to pin.
	f.waitKind("b", KindTLSUntrusted)
	var res TestResult
	if err := json.Unmarshal([]byte(call("POST", "/nodes/b/test", "", 200)), &res); err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error == nil || res.Error.Kind != KindTLSUntrusted || res.Certificate == nil || res.Certificate.Fingerprint == "" {
		t.Fatalf("%+v", res)
	}
	// Tested with the pin, not saved: it works.
	pinned := `{"url":"` + secure.srv.URL + `","access":"admin_api","tls":{"fingerprint":"` + res.Certificate.Fingerprint + `"}}`
	if err := json.Unmarshal([]byte(call("POST", "/nodes/b/test", pinned, 200)), &res); err != nil || !res.OK || res.Node == nil || res.Certificate.Verified != "pin" {
		t.Fatalf("%+v %v", res, err)
	}
	// A stale If-Match is refused; the right one saves the pin, the token
	// kept.
	call("PUT", "/nodes/b", pinned, 412, "If-Match", `"999"`)
	call("PUT", "/nodes/b", pinned, 200, "If-Match", etag)
	if st := f.waitUp("b"); st.TLS.Verified != "pin" {
		t.Errorf("%+v", st.TLS)
	}

	// A member's page; a rename keeps the password.
	if got := call("GET", "/nodes/a", "", 200); !strings.Contains(got, `"state"`) || !strings.Contains(got, `"password_set":true`) {
		t.Errorf("%s", got)
	}
	call("GET", "/nodes/zz", "", 404)
	call("PUT", "/nodes/a", `{"name":"a2","url":"`+plain.srv.URL+`","username":"`+fakeUser+`","labels":{"env":"prod"}}`, 200)
	call("PUT", "/nodes/zz", `{"url":"`+plain.srv.URL+`"}`, 404)
	call("PUT", "/nodes/a2", `{"name":"b","url":"`+plain.srv.URL+`","username":"u"}`, 409)
	call("PUT", "/nodes/a2", `{"url":"ftp://x","username":"u"}`, 400)
	call("PATCH", "/nodes/a2", `{}`, 405)
	call("GET", "/nodes/a2/other", "", 404)
	if st := f.waitUp("a2"); st.Labels["env"] != "prod" {
		t.Errorf("%+v", st)
	}

	// A draft tested before it is added; a draft that does not check.
	if err := json.Unmarshal([]byte(call("POST", "/test", js(consoleInput("new", plain.srv.URL)), 200)), &res); err != nil || !res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	wrong := consoleInput("new", plain.srv.URL)
	wrong.Password = "not-it"
	if err := json.Unmarshal([]byte(call("POST", "/test", js(wrong), 200)), &res); err != nil || res.OK || res.Error.Kind != KindAuth {
		t.Fatalf("%+v %v", res, err)
	}
	call("POST", "/test", `{"name":"x"}`, 400)
	call("POST", "/test", `{`, 400)
	call("POST", "/nodes/zz/test", "", 404)
	call("POST", "/nodes/b/test", `{`, 400)
	call("POST", "/nodes/b/test", `{"url":"http://example.com"}`, 400)

	// max_nodes; the state; the audit log; removal.
	call("POST", "/nodes", js(tokenInput("c", plain.srv.URL)), 201)
	call("POST", "/nodes", js(tokenInput("d", plain.srv.URL)), 409)
	var state stateBody
	if err := json.Unmarshal([]byte(call("GET", "/state", "", 200)), &state); err != nil || len(state.Nodes) != 3 || state.IntervalMs != 50 {
		t.Fatalf("%+v %v", state, err)
	}
	call("DELETE", "/nodes/c", "", 412, "If-Match", `"1"`)
	call("DELETE", "/nodes/c", "", 200)
	call("DELETE", "/nodes/c", "", 404)
	audit := call("GET", "/audit", "", 200)
	for _, want := range []string{ActionAdd, ActionUpdate, ActionRemove, `"user":"admin"`, "name, labels", "tls.fingerprint"} {
		if !strings.Contains(audit, want) {
			t.Errorf("the audit log lacks %q: %s", want, audit)
		}
	}

	// No answer and no log line holds a credential, nor a session's
	// cookie.
	for _, secret := range []string{fakePassword, fakeToken, "not-it", "csrf-s", "fit_console_2020"} {
		for _, a := range answers {
			if strings.Contains(a, secret) {
				t.Errorf("an answer holds %q: %s", secret, a)
			}
		}
		if strings.Contains(f.log.String(), secret) || strings.Contains(s.logs.String(), secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	if !strings.Contains(f.log.String(), `"admin" added the member "a"`) {
		t.Errorf("log: %s", f.log.String())
	}
	// The store holds them, and nothing else does.
	if sec := string(f.store.Section(Section)); !strings.Contains(sec, fakePassword) || !strings.Contains(sec, `"version":2`) {
		t.Errorf("the store: %s", sec)
	}
}

// TestTestsBounded refuses a test past MaxTests, and one whose store write
// fails is answered 500.
func TestTestsBounded(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	slow := newFakeMember(t, false)
	slow.set(func(f *fakeMember) { f.delay = 500 * time.Millisecond })
	for range MaxTests {
		f.m.tests <- struct{}{}
	}
	if code, body, _ := s.do("POST", "/api/m/cluster/test", js(tokenInput("x", slow.srv.URL))); code != 429 {
		t.Errorf("%d %s", code, body)
	}
	for range MaxTests {
		<-f.m.tests
	}
	f.store.mu.Lock()
	f.store.fail = errors.New("disk full")
	f.store.mu.Unlock()
	if code, body, _ := s.do("POST", "/api/m/cluster/nodes", js(tokenInput("x", slow.srv.URL))); code != 500 || !strings.Contains(body, "disk full") {
		t.Errorf("%d %s", code, body)
	}
}

// TestReadOnly leaves a document a newer fit wrote, or one that does not
// read, as it is.
func TestReadOnly(t *testing.T) {
	for _, sec := range []string{`{"version":99,"members":[{"name":"a","url":"http://127.0.0.1:1","access":"admin_api","token":"t"}]}`, `{"version":`} {
		f := &fixture{t: t, engine: testEngine(t), store: newMemStore(), log: &logBuf{}, set: config.Cluster{PollInterval: config.Duration(time.Hour)}}
		f.store.sections[Section] = []byte(sec)
		f.start()
		s := f.serve()
		if code, body, _ := s.do("POST", "/api/m/cluster/nodes", js(tokenInput("x", "http://127.0.0.1:1"))); code != 409 {
			t.Errorf("%s: %d %s", sec, code, body)
		}
		if code, body, _ := s.do("GET", "/api/m/cluster/nodes", ""); code != 200 || !strings.Contains(body, "read_only") {
			t.Errorf("%s: %d %s", sec, code, body)
		}
		if got := string(f.store.Section(Section)); got != sec {
			t.Errorf("written over: %s", got)
		}
	}
}

// TestInvalidKept polls no member the store keeps that does not check,
// and shows it as such.
func TestInvalidKept(t *testing.T) {
	f := &fixture{t: t, engine: testEngine(t), store: newMemStore(), log: &logBuf{}, set: config.Cluster{PollInterval: config.Duration(time.Hour)}}
	f.store.sections[Section] = []byte(`{"members":[{"name":"a","url":"ftp://x","access":"admin_api","token":"t"}]}`)
	f.start()
	if st := f.state("a"); st.Status != StatusError || st.Error == nil {
		t.Errorf("%+v", st)
	}
	if !strings.Contains(f.log.String(), "is not polled") {
		t.Errorf("%s", f.log.String())
	}
	// A document of no version is read as version 1, and written as the
	// version this fit writes.
	if _, err := f.m.Add("u", tokenInput("b", "http://127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
	if got := string(f.store.Section(Section)); !strings.Contains(got, `"version":2`) || !strings.Contains(got, `"groups":[]`) {
		t.Errorf("%s", got)
	}
}

func TestNewErrors(t *testing.T) {
	e := testEngine(t)
	for _, o := range []Options{{}, {Settings: &config.Cluster{}}, {Settings: &config.Cluster{}, Store: newMemStore()}} {
		if _, err := New(o); err == nil {
			t.Errorf("%+v", o)
		}
	}
	m, err := New(Options{Engine: e, Settings: &config.Cluster{}, Store: newMemStore()})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	m.Close()
	// After Close, a change is kept but polls nothing.
	if _, err := m.Add("u", tokenInput("a", "http://127.0.0.1:1")); err != nil {
		t.Fatal(err)
	}
}
