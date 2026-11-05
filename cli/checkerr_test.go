package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestCheckAdminNotReadable refuses a bootstrap config whose admin has a
// field of the wrong type.
func TestCheckAdminNotReadable(t *testing.T) {
	cfg := writeCheckBoot(t, `{"address": 2019}`)
	if code, _, stderr := runFitCheck(t, "", "-c", cfg); code != 2 || !strings.Contains(stderr, "fit check: admin: json: cannot unmarshal number") {
		t.Errorf("%d %q", code, stderr)
	}
}

// TestCheckAPIUnspecifiedIPv6 reaches an Admin API on every IPv6 address
// on IPv6 loopback.
func TestCheckAPIUnspecifiedIPv6(t *testing.T) {
	api, err := newCheckAPI(config.Admin{Address: "[::]:2019"}, checkTLS{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if api.baseURL != "http://[::1]:2019" {
		t.Errorf("base URL %q", api.baseURL)
	}
}

// TestCheckBadAnswers reports an answer that is not the JSON asked for, and
// a refusal with no body by its status text.
func TestCheckBadAnswers(t *testing.T) {
	notJSON := newCheckFake(t, "", checkAnswer(1))
	notJSON.getCode, notJSON.getBody = http.StatusOK, "<html>"
	if code, _, stderr := runFitCheck(t, "", "-c", notJSON.boot(t, "")); code != 2 || !strings.Contains(stderr, "the Admin API's answer is not a check result") {
		t.Errorf("check: %d %q", code, stderr)
	}

	empty := newCheckFake(t, "", checkAnswer(1))
	empty.getCode = http.StatusBadGateway
	if code, _, stderr := runFitCheck(t, "", "-c", empty.boot(t, "")); code != 2 || stderr != "fit check: the Admin API answered 502: Bad Gateway\n" {
		t.Errorf("empty refusal: %d %q", code, stderr)
	}

	applied := newCheckFake(t, "", driftCheck())
	applied.postCode, applied.postBody = http.StatusOK, "applied"
	if code, _, stderr := runFitCheck(t, "1\n", "-c", applied.boot(t, ""), "-fix"); code != 1 || !strings.Contains(stderr, "the Admin API's answer is not an applied config") {
		t.Errorf("reconcile: %d %q", code, stderr)
	}
}

// TestCheckJSONNoNewline ends the JSON printed with a line break when the
// Admin API's answer has none.
func TestCheckJSONNoNewline(t *testing.T) {
	f := newCheckFake(t, "", checkAnswer(1))
	data, err := json.Marshal(checkAnswer(4))
	if err != nil {
		t.Fatal(err)
	}
	f.getCode, f.getBody = http.StatusOK, string(data)
	code, out, stderr := runFitCheck(t, "", "-c", f.boot(t, ""), "-json")
	if code != 0 || stderr != "" || out != string(data)+"\n" {
		t.Errorf("%d %q %q", code, out, stderr)
	}
}

// TestCheckRequestErrors: a base URL no request can be made to, and an
// answer cut short of the length it gave.
func TestCheckRequestErrors(t *testing.T) {
	bad := &checkAPI{client: &http.Client{}, baseURL: "http://bad host"}
	if _, _, err := bad.check(); err == nil || !strings.Contains(err.Error(), "invalid character") {
		t.Errorf("bad URL: %v", err)
	}

	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"version":`))
	}))
	t.Cleanup(cut.Close)
	api := &checkAPI{client: &http.Client{Timeout: 5 * time.Second}, baseURL: cut.URL}
	defer api.close()
	if _, _, err := api.check(); err == nil || !strings.Contains(err.Error(), "reading the Admin API's answer") {
		t.Errorf("cut answer: %v", err)
	}
}

// failAfterFake is a fake Admin API whose answers after the first check
// fail: POST /config/reconcile has its connection closed, when cutPost is
// set, and a second check answers 500.
func failAfterFake(t *testing.T, cutPost bool) *checkFake {
	t.Helper()
	f := &checkFake{checks: []*checkResult{driftCheck()}}
	var gets atomic.Int32
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && cutPost:
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		case r.Method == http.MethodGet && gets.Add(1) > 1:
			http.Error(w, "checking failed", http.StatusInternalServerError)
		default:
			f.serve(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// TestCheckFixFailures: the fix cannot be sent, and the check after it
// fails.
func TestCheckFixFailures(t *testing.T) {
	cut := failAfterFake(t, true)
	if code, _, stderr := runFitCheck(t, "1\n", "-c", cut.boot(t, ""), "-fix"); code != 1 || !strings.Contains(stderr, "fit check: Post ") {
		t.Errorf("reconcile cut: %d %q", code, stderr)
	}

	again := failAfterFake(t, false)
	code, out, stderr := runFitCheck(t, "1\n", "-c", again.boot(t, ""), "-fix")
	if code != 2 || !strings.Contains(out, "applied config version 8") || stderr != "fit check: checking again: the Admin API answered 500: checking failed\n" {
		t.Errorf("check again: %d %q %q", code, out, stderr)
	}
}

// TestCheckFixInput: standard input that cannot be read stops fit check,
// and input that ends on an answer that is not a fix's number skips it.
func TestCheckFixInput(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	cfg := f.boot(t, "")
	defer func(r io.Reader) { stdin = r }(stdin)
	stdin = failingReader{}
	var out, errOut bytes.Buffer
	if code := run([]string{"check", "-c", cfg, "-fix"}, &out, &errOut); code != 2 || errOut.String() != "fit check: broken pipe\n" {
		t.Errorf("read error: %d %q", code, errOut.String())
	}

	code, _, stderr := runFitCheck(t, "9", "-c", cfg, "-fix")
	if code != 1 || stderr != "fit check: no fix was chosen: the config was not changed\n" {
		t.Errorf("EOF: %d %q", code, stderr)
	}
	if _, posts, _, _ := f.sent(); posts != 0 {
		t.Errorf("posts: %d", posts)
	}
}
