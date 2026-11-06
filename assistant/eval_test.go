package assistant

// The assistant's evaluation (I129): the skills migrate and security-review
// run end to end, as the panel runs them, on fixed sets of configs, each
// with what it should come to (testdata/eval):
//
//   - migrate/<case>: a config of nginx, Caddy or Traefik (source.*) and
//     case.json: requests and where each must go once the proposal the
//     skill makes is applied on base.json, and the items it must name as
//     not converted. The proposal must check as fit validate checks a
//     config, and is applied.
//   - review/<case>: a fit config with planted problems (fit.json) and
//     case.json: the certificates fit serves, and the findings the review
//     must make, by the member each is about.
//
// TestAssistantEval runs them against each case's recorded model answers
// (recorded.json), as CI does, without calling any model. With a provider
// in the environment it runs them against that model instead, and reports
// how many convert to a config that validates and keeps the routes, and
// how many reviews find their planted problems: make assistant-eval.
//
//	FIT_EVAL_KIND         the provider's kind, as admin.assistant's: anthropic, openai, ...
//	FIT_EVAL_MODEL        the model
//	FIT_EVAL_API_KEY      its key, when it takes one
//	FIT_EVAL_BASE_URL     its base URL, when not the kind's own
//	FIT_EVAL_API_VERSION  azure_openai's api-version
//	FIT_EVAL_CONTEXT      the model's window in tokens, when known
//	FIT_EVAL_CASES        a regular expression of the cases to run
//	FIT_EVAL_REPORT       a file to write the report to, as JSON
//	FIT_EVAL_RECORD=1     write each case's answers to its recorded.json

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	_ "github.com/lesismal/fit/modules/standard"
	fitruntime "github.com/lesismal/fit/runtime"
)

// The skills the evaluation runs, by the set's directory.
var evalSkills = map[string]string{"migrate": "migrate", "review": "security-review"}

// evalCase is a case of the set, as its case.json says.
type evalCase struct {
	Name  string `json:"-"`
	Set   string `json:"-"` // migrate or review
	Skill string `json:"-"`
	dir   string

	// Message is the person's message; a default for the set when empty.
	Message string `json:"message"`

	// From and Source are a migrate case's server and its config's file;
	// Routes where requests must go once its proposal is applied, and
	// Unconverted the items it must name as not converted, each one or
	// more words, "|" between those that would do.
	From        string      `json:"from"`
	Source      string      `json:"source"`
	Routes      []evalRoute `json:"routes"`
	Unconverted []string    `json:"unconverted"`

	// Config is a review case's config's file, Certificates those fit
	// serves, and Findings the planted problems.
	Config       string        `json:"config"`
	Certificates []evalCert    `json:"certificates"`
	Findings     []evalFinding `json:"findings"`
}

// evalRoute is a request, and where it must go.
type evalRoute struct {
	Port   int               `json:"port"`
	Host   string            `json:"host"`
	Path   string            `json:"path"`
	Remote string            `json:"remote"`
	Header map[string]string `json:"header"`
	Want   evalWant          `json:"want"`
}

// evalWant is what must take a request: a proxy to upstreams holding
// these addresses, a handler of this module, a redirect whose target holds
// this, a refusal (no route, or a respond of 401, 403 or 404), and text the
// route, with its upstreams, holds.
type evalWant struct {
	Upstream []string `json:"upstream"`
	Module   string   `json:"module"`
	Redirect string   `json:"redirect"`
	Denied   bool     `json:"denied"`
	Contains []string `json:"contains"`
}

// evalCert is a certificate the fake fit serves, Days from its expiry.
type evalCert struct {
	Listener string   `json:"listener"`
	Names    []string `json:"names"`
	Days     int      `json:"days"`
}

// evalFinding is a planted problem: found by a finding about one of
// Members, or inside one, or by one that names one of Mentions about a
// member under the same top-level member as Members' first.
type evalFinding struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"`
	Members  []string `json:"members"`
	Mentions []string `json:"mentions"`
}

// evalRecord is a case's recorded model answers, in order: each answer's
// text and the tools it called. "{{source}}" in a call's arguments stands
// for the case's source.
type evalRecord struct {
	Model string     `json:"model"`
	Turns []evalTurn `json:"turns"`
}

type evalTurn struct {
	Text  string     `json:"text,omitempty"`
	Calls []evalCall `json:"calls,omitempty"`
}

type evalCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// evalResult is what a case came to.
type evalResult struct {
	Case  string `json:"case"`
	Skill string `json:"skill"`
	// Proposal says whether the chat left a proposal, Valid whether it
	// checks, and Applied whether it was applied.
	Proposal bool `json:"proposal"`
	Valid    bool `json:"valid"`
	Applied  bool `json:"applied"`
	// Routes and RoutesKept count a migration's requests and those that go
	// where they must; Unconverted and Named its items not converted and
	// those the proposal names.
	Routes      int `json:"routes,omitempty"`
	RoutesKept  int `json:"routes_kept,omitempty"`
	Unconverted int `json:"unconverted,omitempty"`
	Named       int `json:"named,omitempty"`
	// Planted and Found count a review's planted problems and those its
	// findings name; Findings is how many it made.
	Planted  int `json:"planted,omitempty"`
	Found    int `json:"found,omitempty"`
	Findings int `json:"findings,omitempty"`
	// Calls are the tools called, Failed those that failed, Tokens the
	// tokens the model took, and Seconds how long the case ran.
	Calls  int `json:"calls"`
	Failed int `json:"failed"`
	// Errors are the failed calls' results, which a model may mend.
	Errors   []string `json:"errors,omitempty"`
	Tokens   int64    `json:"tokens"`
	Seconds  float64  `json:"seconds"`
	Problems []string `json:"problems,omitempty"`
}

// Passed reports whether the case came to what it should.
func (r *evalResult) Passed() bool { return len(r.Problems) == 0 }

// evalModel is the model the set runs against, from the environment; nil
// runs it against the recorded answers.
type evalModel struct {
	Kind, Model, BaseURL, APIVersion string
	Context                          int
	Record                           bool
}

func evalModelFromEnv(t *testing.T) *evalModel {
	kind := os.Getenv("FIT_EVAL_KIND")
	if kind == "" {
		return nil
	}
	m := &evalModel{Kind: kind, Model: os.Getenv("FIT_EVAL_MODEL"), BaseURL: os.Getenv("FIT_EVAL_BASE_URL"),
		APIVersion: os.Getenv("FIT_EVAL_API_VERSION"), Record: os.Getenv("FIT_EVAL_RECORD") == "1"}
	if m.Model == "" {
		t.Fatal("FIT_EVAL_MODEL names the model FIT_EVAL_KIND's provider is called with")
	}
	if kind == "llm_proxy" {
		t.Fatal("FIT_EVAL_KIND: llm_proxy needs a running fit's llm.proxy; use openai_compatible at its address")
	}
	if s := os.Getenv("FIT_EVAL_CONTEXT"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("FIT_EVAL_CONTEXT: %v", err)
		}
		m.Context = n
	}
	return m
}

// settings returns admin.assistant's settings for the model; extra is
// what every run sets beside the provider.
func (m *evalModel) settings(extra string) string {
	p := map[string]any{"name": "eval", "kind": m.Kind, "api_key_env": "FIT_EVAL_API_KEY",
		"models": []map[string]any{{"name": m.Model, "context": m.Context}}}
	if m.BaseURL != "" {
		p["base_url"] = m.BaseURL
	}
	if m.APIVersion != "" {
		p["api_version"] = m.APIVersion
	}
	prov, _ := json.Marshal([]any{p})
	return `{"providers": ` + string(prov) + `, "default_model": {"provider": "eval", "model": ` + jsonString(m.Model) + `}, ` + extra + `}`
}

func (m *evalModel) String() string {
	if m == nil {
		return "the recorded answers"
	}
	return m.Kind + "/" + m.Model
}

// evalExtra are the settings every run has: the user applies, and the
// skills have room for a config's worth of calls and answer.
const evalExtra = `"default_level": "apply", "params": {"max_output_tokens": 16384},
	"limits": {"max_tool_calls": 40, "max_tool_result_bytes": 65536, "max_tokens_per_day": 50000000}`

// evalDir is the set's directory.
const evalDir = "testdata/eval"

// loadEvalCases reads the set, the cases FIT_EVAL_CASES matches.
func loadEvalCases(t *testing.T) []*evalCase {
	t.Helper()
	var only *regexp.Regexp
	if s := os.Getenv("FIT_EVAL_CASES"); s != "" {
		only = regexp.MustCompile(s)
	}
	var out []*evalCase
	for _, set := range []string{"migrate", "review"} {
		files, _ := filepath.Glob(filepath.Join(evalDir, set, "*", "case.json"))
		slices.Sort(files)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			c := &evalCase{Set: set, Skill: evalSkills[set], dir: filepath.Dir(f)}
			if err := json.Unmarshal(data, &c); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			c.Name = set + "/" + filepath.Base(c.dir)
			if only == nil || only.MatchString(c.Name) {
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no case of the set is chosen")
	}
	return out
}

func (c *evalCase) read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// message returns the person's message.
func (c *evalCase) message(t *testing.T) string {
	switch {
	case c.Message != "" && c.Set == "migrate":
		return c.Message + "\n\n```\n" + string(c.read(t, c.Source)) + "```"
	case c.Message != "":
		return c.Message
	case c.Set == "migrate":
		return "Move this " + c.From + " config to fit, as one proposal:\n\n```\n" + string(c.read(t, c.Source)) + "```"
	}
	return "Review this fit's config for security problems, and propose a fix for each finding you can."
}

// turns returns the case's recorded answers as the fake model's turns.
func (c *evalCase) turns(t *testing.T) []fakeapi.Turn {
	t.Helper()
	var rec evalRecord
	if err := json.Unmarshal(c.read(t, "recorded.json"), &rec); err != nil {
		t.Fatalf("%s: recorded.json: %v", c.Name, err)
	}
	var source string
	if c.Source != "" {
		source = jsonString(string(c.read(t, c.Source)))
	}
	out := make([]fakeapi.Turn, len(rec.Turns))
	n := 0
	for i, tu := range rec.Turns {
		out[i] = fakeapi.Turn{Text: tu.Text, Prompt: 1000, Completion: 100}
		for _, cl := range tu.Calls {
			n++
			args := strings.ReplaceAll(string(cl.Args), `"{{source}}"`, source)
			out[i].Calls = append(out[i].Calls, fakeapi.Call{ID: "call_" + strconv.Itoa(n), Name: cl.Name, Args: args})
		}
	}
	return out
}

// TestAssistantEval runs the set: against the recorded answers, where
// every case must come to what it should, or against the model the
// environment names, and reports what each came to.
func TestAssistantEval(t *testing.T) {
	m := evalModelFromEnv(t)
	cases := loadEvalCases(t)
	cert, err := certtest.Write(t.TempDir(), "eval.example")
	if err != nil {
		t.Fatal(err)
	}
	var results []*evalResult
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			r := runEvalCase(t, m, c, cert)
			results = append(results, r)
			for _, e := range r.Errors {
				t.Log("a call failed: " + e)
			}
			for _, p := range r.Problems {
				t.Error(p)
			}
		})
	}
	report := evalReport(m, results)
	t.Log("\n" + report)
	if path := os.Getenv("FIT_EVAL_REPORT"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"model": m.String(), "at": time.Now().UTC().Format(time.RFC3339), "results": results}, "", "  ")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Error(err)
		}
	}
}

// evalReport says what each case came to, and the counts.
func evalReport(m *evalModel, results []*evalResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The assistant's evaluation, against %s:\n", m)
	var migrates, migrated, reviews, reviewed int
	for _, r := range results {
		state := "ok"
		if !r.Passed() {
			state = "FAILED: " + strings.Join(r.Problems, "; ")
		}
		switch r.Skill {
		case "migrate":
			migrates++
			if r.Passed() {
				migrated++
			}
			fmt.Fprintf(&b, "  %-26s valid %-5v applied %-5v routes %d/%d  unconverted named %d/%d  calls %d (%d failed)  %d tokens  %.1fs  %s\n",
				r.Case, r.Valid, r.Applied, r.RoutesKept, r.Routes, r.Named, r.Unconverted, r.Calls, r.Failed, r.Tokens, r.Seconds, state)
		default:
			reviews++
			if r.Passed() {
				reviewed++
			}
			fmt.Fprintf(&b, "  %-26s planted found %d/%d  findings %d  proposal %v (valid %v)  calls %d (%d failed)  %d tokens  %.1fs  %s\n",
				r.Case, r.Found, r.Planted, r.Findings, r.Proposal, r.Valid, r.Calls, r.Failed, r.Tokens, r.Seconds, state)
		}
	}
	fmt.Fprintf(&b, "migrate: %d of %d convert to a config that validates and keeps the routes, their unconverted items named\n", migrated, migrates)
	fmt.Fprintf(&b, "security-review: %d of %d find the planted problems of their config\n", reviewed, reviews)
	return b.String()
}

// runEvalCase runs a case: the chat, as the panel would send it, then the
// checks of what it left.
func runEvalCase(t *testing.T, m *evalModel, c *evalCase, cert *certtest.Cert) *evalResult {
	t.Helper()
	start := time.Now()
	r := &evalResult{Case: c.Name, Skill: c.Skill}
	base := c.read(t, "../../base.json")
	if c.Set == "review" {
		base = c.read(t, c.Config)
	}
	cfgs := newFakeConfigs(string(base))
	opts := func(o *Options) {
		o.API = evalAPI(cfgs, c)
		o.Schema, _ = os.ReadFile("../schema/fit.schema.json")
		o.Validate = func(data []byte) error { return evalValidate(data, cert) }
		o.Config = func() *config.Config {
			_, data := cfgs.current()
			cfg, _ := config.Load(data)
			return cfg
		}
	}
	var f *fixture
	if m == nil {
		sc := &script{turns: c.turns(t)}
		f = startChat(t, evalExtra, sc.next, opts).fixture
	} else {
		f = startWith(t, loadConfig(t, m.settings(evalExtra)), &memStore{}, opts)
	}

	body := map[string]any{"message": c.message(t), "skill": c.Skill}
	if c.From != "" {
		body["inputs"] = map[string]string{"from": c.From}
	}
	conv := ""
	for round := 0; ; round++ {
		raw, _ := json.Marshal(body)
		s := f.chat(string(raw))
		for _, e := range s.events {
			switch e.Type {
			case EventToolCall:
				r.Calls++
			case EventToolResult:
				if e.Error {
					r.Failed++
					r.Errors = append(r.Errors, e.Name+": "+clip(e.Content, 300))
				}
			case EventUsage:
				r.Tokens += int64(e.Usage.InputTokens + e.Usage.OutputTokens)
			}
		}
		if s.end == nil {
			r.Problems = append(r.Problems, fmt.Sprintf("the chat failed: %d %s%s", s.status, s.err, s.body))
			break
		}
		conv = s.end.Conversation
		asks := s.of(EventAsk)
		if s.end.Reason != ReasonAsk || len(asks) == 0 || round == 3 {
			if s.end.Reason != ReasonAnswer {
				r.Problems = append(r.Problems, "the chat ended with "+s.end.Reason)
			}
			break
		}
		// A question is answered with its first option.
		ask := asks[len(asks)-1]
		body = map[string]any{"conversation": conv, "answer": map[string]string{"id": ask.ID, "choice": ask.Options[0]}}
	}
	if conv != "" {
		if c.Set == "migrate" {
			evalMigration(t, f, cfgs, c, r, conv, cert)
		} else {
			evalReview(t, f, c, r, conv)
		}
		if m != nil && m.Record {
			recordEval(t, f, c, m, conv)
		}
	}
	r.Seconds = time.Since(start).Seconds()
	return r
}

// evalAPI is the fake Admin API of a case: fakeConfigs, with the status
// and the effective config of what it holds.
func evalAPI(cfgs *fakeConfigs, c *evalCase) http.Handler {
	next := cfgs.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, data := cfgs.current()
		switch {
		case r.Method == "GET" && r.URL.Path == "/config/effective":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"version":%d,"config":%s,"sources":[]}`, n, data)
		case r.Method == "GET" && r.URL.Path == "/status":
			var cfg struct {
				Listeners []struct {
					Name    string `json:"name"`
					Address string `json:"address"`
				} `json:"listeners"`
			}
			_ = json.Unmarshal(data, &cfg)
			var certs []map[string]any
			for _, ct := range c.Certificates {
				certs = append(certs, map[string]any{"listener": ct.Listener, "names": ct.Names, "issuer": "R11",
					"not_after": time.Now().Add(time.Duration(ct.Days)*24*time.Hour + time.Hour).UTC().Format(time.RFC3339)})
			}
			st, _ := json.Marshal(map[string]any{"version": n, "applied": "2026-11-06T00:00:00Z", "source": "file",
				"listeners": cfg.Listeners, "connections": map[string]int{"open": 3}, "upstreams": []any{}, "certificates": certs})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(st)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// evalValidate checks a config's JSON as fit validate does, loaded and
// compiled, with the files it names that are not here, certificates and
// keys on the machine the config came from, standing for cert's.
func evalValidate(data []byte, cert *certtest.Cert) error {
	g, err := evalCompile(data, cert)
	if err == nil {
		g.Release()
	}
	return err
}

// evalCompile loads and compiles a config as evalValidate does.
func evalCompile(data []byte, cert *certtest.Cert) (*fitruntime.Graph, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	data, _ = json.Marshal(localFiles(v, "", cert))
	c, err := config.Load(data)
	if err != nil {
		return nil, err
	}
	return fitruntime.Compile(c, fitruntime.Env{})
}

// localFiles returns v with each file a member *_file names that is not
// here replaced with cert's: its key for a key's member, its certificate
// for the rest; and each root directory that is not, with cert's.
func localFiles(v any, key string, cert *certtest.Cert) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = localFiles(e, k, cert)
		}
	case []any:
		for i, e := range x {
			x[i] = localFiles(e, key, cert)
		}
	case string:
		if !strings.HasSuffix(key, "_file") && !strings.HasSuffix(key, "_files") && key != "root" {
			return x
		}
		if _, err := os.Stat(x); err == nil {
			return x
		}
		if key == "root" {
			return filepath.Dir(cert.CertFile)
		}
		if strings.Contains(key, "key") {
			return cert.KeyFile
		}
		return cert.CertFile
	}
	return v
}

// evalView returns the conversation's proposal as the panel reads it.
func evalView(f *fixture, conv string) (*ProposalView, string) {
	status, body := f.do("GET", "/api/m/ai/proposals/"+conv, "")
	var out struct {
		Proposal *ProposalView `json:"proposal"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if status != 200 {
		return nil, body
	}
	return out.Proposal, body
}

// evalMigration checks what a migration left: its proposal valid, the
// items not converted named, applied, and the requests going where they
// must on the config it made.
func evalMigration(t *testing.T, f *fixture, cfgs *fakeConfigs, c *evalCase, r *evalResult, conv string, cert *certtest.Cert) {
	t.Helper()
	r.Routes, r.Unconverted = len(c.Routes), len(c.Unconverted)
	v, body := evalView(f, conv)
	if v == nil {
		r.Problems = append(r.Problems, "no proposal: "+clip(body, 300))
		return
	}
	r.Proposal, r.Valid = true, v.Valid
	if !v.Valid {
		r.Problems = append(r.Problems, "the proposal does not check: "+jsonText(v.Errors)+" "+jsonText(v.Steps))
	}
	for _, want := range c.Unconverted {
		if named(v.Unconverted, want) {
			r.Named++
		} else {
			r.Problems = append(r.Problems, fmt.Sprintf("the proposal does not name %q as not converted: %q", want, v.Unconverted))
		}
	}
	if !v.Valid {
		return
	}
	raw, _ := json.Marshal(map[string]any{"id": v.ID, "revision": v.Revision, "etag": v.ETag, "user": v.User})
	status, out := f.do("POST", "/api/m/ai/proposals/"+v.Conversation+"/apply", string(raw))
	if status != 200 {
		r.Problems = append(r.Problems, fmt.Sprintf("the apply: %d %s", status, clip(out, 300)))
		return
	}
	r.Applied = true
	_, data := cfgs.current()
	g, err := evalCompile(data, cert)
	if err != nil {
		r.Problems = append(r.Problems, "the config applied does not compile: "+err.Error())
		return
	}
	defer g.Release()
	var cfg map[string]any
	_ = json.Unmarshal(data, &cfg)
	for _, rt := range c.Routes {
		if err := checkRoute(g, cfg, rt); err != nil {
			r.Problems = append(r.Problems, err.Error())
		} else {
			r.RoutesKept++
		}
	}
}

// named reports whether one of items names want: holds one of its words,
// "|" between them, in any case.
func named(items []string, want string) bool {
	for _, it := range items {
		for _, w := range strings.Split(want, "|") {
			if strings.Contains(strings.ToLower(it), strings.ToLower(strings.TrimSpace(w))) {
				return true
			}
		}
	}
	return false
}

// evalReview checks what a review found: each planted problem named by a
// finding with a fix, and its proposal, when it made one, valid.
func evalReview(t *testing.T, f *fixture, c *evalCase, r *evalResult, conv string) {
	t.Helper()
	r.Planted = len(c.Findings)
	answer := ""
	for _, m := range f.conversation(conv).Messages {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) != "" {
			answer = m.Content
		}
	}
	found := parseFindings(answer)
	r.Findings = len(found)
	for _, p := range c.Findings {
		if slices.ContainsFunc(found, func(fd finding) bool { return fd.fix && p.matches(fd) }) {
			r.Found++
		} else {
			r.Problems = append(r.Problems, fmt.Sprintf("no finding with a fix names %s (%s)", p.ID, strings.Join(p.Members, ", ")))
		}
	}
	if v, _ := evalView(f, conv); v != nil {
		r.Proposal, r.Valid = true, v.Valid
		if !v.Valid {
			r.Problems = append(r.Problems, "the proposal does not check: "+jsonText(v.Errors))
		}
	}
}

// recordEval writes the conversation's answers to the case's
// recorded.json, the case's source put back as "{{source}}".
func recordEval(t *testing.T, f *fixture, c *evalCase, m *evalModel, conv string) {
	t.Helper()
	rec := evalRecord{Model: m.String()}
	var source string
	if c.Source != "" {
		source = jsonString(string(c.read(t, c.Source)))
	}
	for _, msg := range f.conversation(conv).Messages {
		if msg.Role != "assistant" || msg.Error != "" {
			continue
		}
		tu := evalTurn{Text: msg.Content}
		for _, tc := range msg.ToolCalls {
			args := string(tc.Arguments)
			if source != "" {
				args = strings.ReplaceAll(args, source, `"{{source}}"`)
			}
			tu.Calls = append(tu.Calls, evalCall{Name: tc.Name, Args: json.RawMessage(args)})
		}
		rec.Turns = append(rec.Turns, tu)
	}
	data, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(c.dir, "recorded.json"), append(data, '\n'), 0o644); err != nil {
		t.Error(err)
	}
}
