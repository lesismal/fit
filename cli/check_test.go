package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkAt is the time the fake checks say they ran at, with the offset the
// Admin API's JSON keeps.
var checkAt = time.Date(2026, 11, 4, 20, 0, 0, 0, time.FixedZone("", 8*60*60))

// checkAnswer is an answer that finds no difference, of the config at
// version.
func checkAnswer(version uint64) *checkResult {
	return &checkResult{
		Version:   version,
		ETag:      `"v` + strconv.FormatUint(version, 10) + `"`,
		CheckedAt: checkAt,
	}
}

// driftCheck is an answer with one drift difference and one info one.
func driftCheck() *checkResult {
	res := checkAnswer(7)
	res.Summary = checkSummary{Drift: 1, Info: 1}
	res.Differences = []difference{
		{
			ID:       "setting:log",
			Category: "settings",
			Kind:     "drift",
			Name:     "log",
			Detail:   "the config sets log.level=debug, but the running service uses the value fit started with",
			Config:   json.RawMessage(`{"level":"debug"}`),
			Running:  json.RawMessage(`{"level":"info"}`),
			Fixes: []fix{{
				Action: "replace",
				Label:  "revert the config to the value in force",
				Patch:  &configPatch{Op: "replace", Path: "/log"},
			}},
		},
		{
			ID:       "tls:web:auto",
			Category: "tls",
			Kind:     "info",
			Name:     "web",
			Detail:   "the certificate for web is being obtained by ACME",
		},
	}
	return res
}

// checkFake is a fake Admin API: the answers it gives to GET /config/check,
// in order, and what it kept of a POST /config/reconcile.
type checkFake struct {
	mu      sync.Mutex
	checks  []*checkResult
	next    int
	gets    int
	posts   int
	ifMatch string
	fixes   []checkFix

	token    string
	warnings []string
	getCode  int    // answered to GET /config/check instead of a check
	getBody  string // its body, when getCode is set
	postCode int    // answered to POST /config/reconcile instead of applying
	postBody string // its body, when postCode is set

	srv *httptest.Server
}

func newCheckFake(t *testing.T, token string, checks ...*checkResult) *checkFake {
	t.Helper()
	f := &checkFake{token: token, checks: checks}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *checkFake) serve(w http.ResponseWriter, r *http.Request) {
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fit admin"`)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"a valid bearer token is needed"}`+"\n")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/config/check":
		f.serveCheck(w)
	case r.Method == http.MethodPost && r.URL.Path == "/config/reconcile":
		f.serveReconcile(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *checkFake) serveCheck(w http.ResponseWriter) {
	f.mu.Lock()
	f.gets++
	code, body := f.getCode, f.getBody
	i := f.next
	if i < len(f.checks)-1 {
		f.next++
	}
	res := f.checks[i]
	f.mu.Unlock()
	if code != 0 {
		w.WriteHeader(code)
		fmt.Fprint(w, body)
		return
	}
	data, _ := json.Marshal(res)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(data, '\n'))
}

func (f *checkFake) serveReconcile(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Fixes []checkFix `json:"fixes"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.posts++
	f.ifMatch = r.Header.Get("If-Match")
	f.fixes = req.Fixes
	code, postBody, warnings := f.postCode, f.postBody, f.warnings
	f.mu.Unlock()
	if code != 0 {
		w.WriteHeader(code)
		fmt.Fprint(w, postBody)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		http.Error(w, "the body must be sent as application/json", http.StatusUnsupportedMediaType)
		return
	}
	data, _ := json.Marshal(checkApplied{Version: 8, ETag: `"v8"`, Warnings: warnings})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(data, '\n'))
}

// sent returns what the fake kept of the requests fit check made.
func (f *checkFake) sent() (gets, posts int, ifMatch string, fixes []checkFix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets, f.posts, f.ifMatch, f.fixes
}

// boot writes a bootstrap config whose admin is the fake's address, reading
// a token from tokenFile when it is not empty.
func (f *checkFake) boot(t *testing.T, tokenFile string) string {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	admin := `{"address": ` + js(u.Host)
	if tokenFile != "" {
		admin += `, "token_file": ` + js(tokenFile)
	}
	admin += `}`
	return writeCheckBoot(t, admin)
}

// writeCheckBoot writes a bootstrap config with admin as its admin.
func writeCheckBoot(t *testing.T, admin string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, path, `{"admin": `+admin+`}`)
	return path
}

// runFitCheck runs fit check with in as standard input and returns its exit
// code and its outputs.
func runFitCheck(t *testing.T, in string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	defer func(r io.Reader) { stdin = r }(stdin)
	stdin = strings.NewReader(in)
	var out, errOut bytes.Buffer
	code = run(append([]string{"check"}, args...), &out, &errOut)
	return code, plain(out.String()), plain(errOut.String())
}

// TestCheckUsage lists fit check in fit's help.
func TestCheckUsage(t *testing.T) {
	if code, out, stderr := runFit("help"); code != 0 || !strings.Contains(out, "check      ask a running fit") || stderr != "" {
		t.Errorf("help: %d\n%q\n%q", code, out, stderr)
	}
}

// TestCheckDrift checks the listing of a check that found differences, and
// that drift is exit code 1.
func TestCheckDrift(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	code, out, stderr := runFitCheck(t, "", "-c", f.boot(t, ""))
	want := `fit check: config version 7, checked at 2026-11-04T20:00:00+08:00
drift: 1, info: 1
[1] drift settings/log
    the config sets log.level=debug, but the running service uses the value fit started with
    config:  {"level":"debug"}
    running: {"level":"info"}
    fixes:   1 replace: revert the config to the value in force
[2] info tls/web
    the certificate for web is being obtained by ACME
`
	if code != 1 || out != want || stderr != "" {
		t.Fatalf("%d\n%q\n%q\nwant\n%q", code, out, stderr, want)
	}
}

// TestCheckJSON checks that -json prints the answer as the Admin API sent it,
// and still reports drift by its exit code.
func TestCheckJSON(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	code, out, stderr := runFitCheck(t, "", "-c", f.boot(t, ""), "-json")
	data, err := json.Marshal(driftCheck())
	if err != nil {
		t.Fatal(err)
	}
	if want := string(data) + "\n"; code != 1 || out != want || stderr != "" {
		t.Fatalf("%d\n%q\nwant\n%q", code, out, want)
	}
}

// TestCheckAgrees checks a config with no drift at all: exit code 0, and no
// prompt even with -fix.
func TestCheckAgrees(t *testing.T) {
	f := newCheckFake(t, "", checkAnswer(3))
	code, out, stderr := runFitCheck(t, "", "-c", f.boot(t, ""), "-fix")
	want := "fit check: config version 3, checked at 2026-11-04T20:00:00+08:00\ndrift: 0, info: 0\n"
	if code != 0 || out != want || stderr != "" {
		t.Fatalf("%d\n%q\n%q\nwant\n%q", code, out, stderr, want)
	}
	if gets, posts, _, _ := f.sent(); gets != 1 || posts != 0 {
		t.Errorf("requests: %d, %d", gets, posts)
	}
}

// TestCheckInfoOnly checks that info differences alone are exit code 0.
func TestCheckInfoOnly(t *testing.T) {
	res := checkAnswer(3)
	res.Summary.Info = 1
	res.Differences = []difference{{ID: "listener:web:port", Category: "listeners", Kind: "info", Name: "web", Detail: "a port of 0 is a dynamic port"}}
	f := newCheckFake(t, "", res)
	code, out, stderr := runFitCheck(t, "", "-c", f.boot(t, ""))
	if code != 0 || !strings.Contains(out, "[1] info listeners/web") || stderr != "" {
		t.Fatalf("%d\n%q\n%q", code, out, stderr)
	}
}

// TestCheckFix chooses a fix for the one drift difference, checks what
// was sent and with which If-Match, and checks again after applying it.
func TestCheckFix(t *testing.T) {
	f := newCheckFake(t, "", driftCheck(), checkAnswer(8))
	f.warnings = []string{"log.level takes effect when fit restarts"}
	code, out, stderr := runFitCheck(t, "1\n", "-c", f.boot(t, ""), "-fix")
	if code != 0 {
		t.Fatalf("%d\n%q\n%q", code, out, stderr)
	}
	gets, posts, ifMatch, fixes := f.sent()
	if gets != 2 || posts != 1 || ifMatch != `"v7"` || len(fixes) != 1 || fixes[0] != (checkFix{ID: "setting:log", Action: "replace"}) {
		t.Errorf("requests: get %d, post %d, if-match %q, fixes %v", gets, posts, ifMatch, fixes)
	}
	for _, want := range []string{
		"fix [1] setting:log — choose 1) replace: revert the config to the value in force (replace /log), s) skip [s]: ",
		"fit check: applied config version 8\n",
		"fit check: config version 8, checked at 2026-11-04T20:00:00+08:00\ndrift: 0, info: 0\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if want := "fit check: warning: log.level takes effect when fit restarts\n"; stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
}

// TestCheckFixSkip skips every difference: nothing is sent, and the drift is
// still there.
func TestCheckFixSkip(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	code, out, stderr := runFitCheck(t, "\n", "-c", f.boot(t, ""), "-fix")
	if code != 1 || !strings.Contains(out, "s) skip [s]: ") {
		t.Fatalf("%d\n%q", code, out)
	}
	if want := "fit check: no fix was chosen: the config was not changed\n"; stderr != want {
		t.Errorf("stderr %q, want %q", stderr, want)
	}
	if gets, posts, _, _ := f.sent(); gets != 1 || posts != 0 {
		t.Errorf("requests: %d, %d", gets, posts)
	}
}

// TestCheckFixReprompt asks again after an answer that is no fix, and takes
// the one given then.
func TestCheckFixReprompt(t *testing.T) {
	f := newCheckFake(t, "", driftCheck(), checkAnswer(8))
	code, out, stderr := runFitCheck(t, "9\nx\n1\n", "-c", f.boot(t, ""), "-fix")
	if code != 0 || !strings.Contains(out, "choose a number from 1 to 1, or s to skip\n") {
		t.Fatalf("%d\n%q\n%q", code, out, stderr)
	}
	if _, posts, _, fixes := f.sent(); posts != 1 || len(fixes) != 1 {
		t.Errorf("posts %d, fixes %v", posts, fixes)
	}
}

// TestCheckFixChanged applies a fix with an ETag of a config that is no
// longer the running one: nothing is applied, and the check is not run again.
func TestCheckFixChanged(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	f.postCode = http.StatusPreconditionFailed
	f.postBody = `{"error":"the running config changed"}` + "\n"
	code, _, stderr := runFitCheck(t, "1\n", "-c", f.boot(t, ""), "-fix")
	if code != 1 || !strings.Contains(stderr, "run fit check again") {
		t.Fatalf("%d %q", code, stderr)
	}
	if gets, posts, _, _ := f.sent(); gets != 1 || posts != 1 {
		t.Errorf("requests: %d, %d", gets, posts)
	}
}

// TestCheckFixRefused reports the Admin API's error when it refuses a fix.
func TestCheckFixRefused(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	f.postCode = http.StatusBadRequest
	f.postBody = `{"error":"the difference \"setting:log\" is not there any more: check again"}` + "\n"
	code, _, stderr := runFitCheck(t, "1\n", "-c", f.boot(t, ""), "-fix")
	if code != 1 || !strings.Contains(stderr, "is not there any more") {
		t.Fatalf("%d %q", code, stderr)
	}
}

// TestCheckHTTPError reports an Admin API that could not check, with the
// error it sent or, without one, its body.
func TestCheckHTTPError(t *testing.T) {
	f := newCheckFake(t, "", driftCheck())
	f.getCode = http.StatusInternalServerError
	f.getBody = `{"error":"checking failed"}` + "\n"
	code, _, stderr := runFitCheck(t, "", "-c", f.boot(t, ""))
	if want := "fit check: the Admin API answered 500: checking failed\n"; code != 2 || stderr != want {
		t.Fatalf("%d %q, want %q", code, stderr, want)
	}
	plain := newCheckFake(t, "", driftCheck())
	plain.getCode = http.StatusServiceUnavailable
	plain.getBody = "it is starting\n"
	code, _, stderr = runFitCheck(t, "", "-c", plain.boot(t, ""))
	if want := "fit check: the Admin API answered 503: it is starting\n"; code != 2 || stderr != want {
		t.Errorf("%d %q, want %q", code, stderr, want)
	}
}

// TestPrintCheckFallbacks lists a difference whose name the Admin API left
// out by its ID, and a value that is not JSON as it came.
func TestPrintCheckFallbacks(t *testing.T) {
	res := checkAnswer(1)
	res.Summary.Drift = 1
	res.Differences = []difference{{
		ID:       "role:llm.hub",
		Category: "roles",
		Kind:     "drift",
		Config:   json.RawMessage("not json"),
		Fixes:    []fix{{Action: "delete"}},
	}}
	var b bytes.Buffer
	printCheck(&b, res)
	want := `[1] drift roles/role:llm.hub
    config:  not json
    fixes:   1 delete: 
`
	if !strings.Contains(b.String(), want) {
		t.Errorf("listing:\n%s\nwant\n%s", b.String(), want)
	}
}

// TestCheckFixNoFix skips a drift difference that offers no fix at all.
func TestCheckFixNoFix(t *testing.T) {
	res := checkAnswer(7)
	res.Summary.Drift = 1
	res.Differences = []difference{{ID: "role:llm.hub", Category: "roles", Kind: "drift", Name: "llm.hub", Detail: "the role is not running"}}
	f := newCheckFake(t, "", res)
	code, _, stderr := runFitCheck(t, "", "-c", f.boot(t, ""), "-fix")
	if code != 1 || !strings.Contains(stderr, "no fix was chosen") {
		t.Fatalf("%d %q", code, stderr)
	}
	if gets, posts, _, _ := f.sent(); gets != 1 || posts != 0 {
		t.Errorf("requests: %d, %d", gets, posts)
	}
}

// TestCheckToken sends the token admin.token_file holds.
func TestCheckToken(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	writeFile(t, token, "s3cret\n")
	f := newCheckFake(t, "s3cret", driftCheck())
	if code, _, stderr := runFitCheck(t, "", "-c", f.boot(t, token)); code != 1 {
		t.Fatalf("%d %q", code, stderr)
	}

	// A token file that does not open, is empty, or holds another token.
	if code, _, stderr := runFitCheck(t, "", "-c", f.boot(t, filepath.Join(t.TempDir(), "missing"))); code != 2 || !strings.Contains(stderr, "admin.token_file") {
		t.Errorf("missing: %d %q", code, stderr)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	writeFile(t, empty, " \n")
	if code, _, stderr := runFitCheck(t, "", "-c", f.boot(t, empty)); code != 2 || !strings.Contains(stderr, "is empty") {
		t.Errorf("empty: %d %q", code, stderr)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeFile(t, other, "other")
	if code, _, stderr := runFitCheck(t, "", "-c", f.boot(t, other)); code != 2 || !strings.Contains(stderr, "valid bearer token") {
		t.Errorf("other: %d %q", code, stderr)
	}
}

// TestCheckFixTogether chooses one fix for each of two differences, one of
// them the second fix offered, and sends them together.
func TestCheckFixTogether(t *testing.T) {
	res := checkAnswer(7)
	res.Summary.Drift = 2
	res.Differences = []difference{
		{
			ID: "setting:log", Category: "settings", Kind: "drift", Name: "log", Detail: "the config sets another log level",
			Fixes: []fix{{Action: "replace", Label: "revert the config to the value in force", Patch: &configPatch{Op: "replace", Path: "/log"}}},
		},
		{
			ID: "role:llm.hub", Category: "roles", Kind: "drift", Name: "llm.hub", Detail: "the config sets a role that is not running",
			Fixes: []fix{
				{Action: "delete", Label: "remove it from the config", Patch: &configPatch{Op: "delete", Path: "/llm/hub"}},
				{Action: "reapply", Label: "apply the config again to retry starting it"},
			},
		},
	}
	f := newCheckFake(t, "", res, checkAnswer(8))
	code, _, stderr := runFitCheck(t, "1\n2\n", "-c", f.boot(t, ""), "-fix")
	if code != 0 {
		t.Fatalf("%d %q", code, stderr)
	}
	want := []checkFix{{ID: "setting:log", Action: "replace"}, {ID: "role:llm.hub", Action: "reapply"}}
	if _, posts, _, fixes := f.sent(); posts != 1 || len(fixes) != 2 || fixes[0] != want[0] || fixes[1] != want[1] {
		t.Errorf("posts %d, fixes %v, want %v", posts, fixes, want)
	}
}

// TestCheckTimeout gives up on an Admin API that does not answer in time.
func TestCheckTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	t.Cleanup(slow.Close)
	u, err := url.Parse(slow.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := writeCheckBoot(t, `{"address": `+js(u.Host)+`}`)
	if code, _, stderr := runFitCheck(t, "", "-c", cfg, "-timeout", "20ms"); code != 2 || !strings.Contains(stderr, "exceeded") {
		t.Errorf("timeout: %d %q", code, stderr)
	}
}

// TestCheckErrors covers the bootstrap configs and the arguments fit check
// refuses.
func TestCheckErrors(t *testing.T) {
	// No admin: no Admin API to reach.
	dir := t.TempDir()
	noAdmin := filepath.Join(dir, "fit.json")
	writeFile(t, noAdmin, `{"listeners": [{"name": "web", "address": ":0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	want := "fit check: the config sets no admin: fit check reaches a running fit through its Admin API\n"
	if code, _, stderr := runFitCheck(t, "", "-c", noAdmin); code != 2 || stderr != want {
		t.Errorf("no admin: %d %q, want %q", code, stderr, want)
	}

	// A config that does not load, a file that is not there, and bad
	// arguments.
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, `{"admin": {"address": "127.0.0.1:2019"`+"\n")
	if code, _, stderr := runFitCheck(t, "", "-c", bad); code != 2 || !strings.Contains(stderr, "not JSON") {
		t.Errorf("bad config: %d %q", code, stderr)
	}
	if code, _, stderr := runFitCheck(t, "", "-c", filepath.Join(dir, "missing.json")); code != 2 || !strings.Contains(stderr, "missing.json") {
		t.Errorf("missing file: %d %q", code, stderr)
	}
	if code, _, stderr := runFitCheck(t, "", "-bogus"); code != 2 || !strings.Contains(stderr, "-bogus") {
		t.Errorf("bad flag: %d %q", code, stderr)
	}
	if code, _, stderr := runFitCheck(t, "", "extra"); code != 2 || !strings.Contains(stderr, `unexpected argument "extra"`) {
		t.Errorf("extra argument: %d %q", code, stderr)
	}
	if code, _, stderr := runFitCheck(t, "", "-json", "-fix"); code != 2 || !strings.Contains(stderr, "give one of them") {
		t.Errorf("-json -fix: %d %q", code, stderr)
	}

	// An Admin API that is not listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := writeCheckBoot(t, `{"address": `+js(addr)+`}`)
	if code, _, stderr := runFitCheck(t, "", "-c", cfg); code != 2 || !strings.Contains(stderr, "connection refused") {
		t.Errorf("not listening: %d %q", code, stderr)
	}
}

// TestCheckUnix reaches the Admin API over the Unix socket the bootstrap
// config's admin.address names.
func TestCheckUnix(t *testing.T) {
	// A short directory: a Unix socket's path is limited to about a hundred
	// bytes, and a test's own temporary directory is longer than that here.
	dir, err := os.MkdirTemp("", "fitcheck")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "admin.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &checkFake{checks: []*checkResult{checkAnswer(1)}}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	cfg := writeCheckBoot(t, `{"address": `+js("unix/"+socket)+`}`)
	code, out, stderr := runFitCheck(t, "", "-c", cfg)
	if code != 0 || !strings.Contains(out, "drift: 0, info: 0") || stderr != "" {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	if gets, _, _, _ := f.sent(); gets != 1 {
		t.Errorf("requests: %d", gets)
	}
}

// TestCheckAdminWithConsoleDurations: the bootstrap file's admin holds
// console settings whose JSON is fit's own (durations written as strings);
// fit check reads only the address and token file, so it still reaches the
// Admin API.
func TestCheckAdminWithConsoleDurations(t *testing.T) {
	f := newCheckFake(t, "", checkAnswer(2))
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	boot := writeCheckBoot(t, `{"address": `+js(u.Host)+`,
		"console": {"address": "127.0.0.1:0", "session_timeout": "12h", "session_lifetime": "1h",
			"users": [{"username": "ada", "password_hash": "$2a$04$abcdefghijklmnopqrstuv"}]}}`)
	code, out, stderr := runFitCheck(t, "", "-c", boot)
	if code != 0 || stderr != "" || !strings.Contains(out, "drift: 0, info: 0") {
		t.Fatalf("%d\n%q\n%q", code, out, stderr)
	}
}
