package assistant

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

// offered returns the tools a request to the model offers, sorted.
func offered(r fakeapi.Request) []string {
	var out []string
	list, _ := r.Body["tools"].([]any)
	for _, t := range list {
		name, _ := fakeapi.Get(t, "function", "name").(string)
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// skillRuns are what each built-in skill does in TestBuiltinSkillsRun: a
// read call, and the change it proposes when it proposes one, on propBase.
var skillRuns = map[string][2]string{
	"explain":               {`{"members": ["routes"]}`, ""},
	"route-builder":         {`{"members": ["routes", "upstreams", "listeners"]}`, `{"summary": "Answer /healthz", "ops": [{"op": "add", "path": "/routes/0", "value": {"name": "health", "match": {"path": ["/healthz"]}, "handler": {"module": "respond", "params": {"body": "ok"}}}}]}`},
	"reverse-proxy":         {`{"members": ["upstreams"]}`, `{"summary": "A second backend, health checked", "members": {"upstreams": [{"name": "app", "addresses": ["10.0.0.7:9000", "10.0.0.8:9000"], "max_fails": 3, "fail_timeout": "10s", "health_check": {"path": "/healthz", "interval": "5s"}}]}}`},
	"tls-acme":              {`{"members": ["listeners"]}`, `{"summary": "An ACME account", "members": {"acme": {"email": "ops@example.com"}}}`},
	"rate-limit-auth":       {`{"members": ["routes"]}`, `{"summary": "Limit /api", "ops": [{"op": "add", "path": "/routes/0/middleware/0", "value": {"module": "rate_limit", "params": {"rate": 10, "burst": 20}}}]}`},
	"cache-tuning":          {`{"members": ["routes"]}`, `{"summary": "Purge the zone", "zone": "main", "all": true}`},
	"diagnose":              {`{"members": ["routes", "upstreams"]}`, ""},
	"drift-explain":         {`{}`, ""},
	"log-analysis":          {`{"level": "warn"}`, ""},
	"security-review":       {`{}`, `{"summary": "No internal addresses through app", "ops": [{"op": "add", "path": "/upstreams/0/deny_internal", "value": true}]}`},
	"migrate":               {`{"from": "nginx", "text": "http { server { listen 8080; location / { proxy_pass http://10.0.0.1:9000; } } }"}`, `{"summary": "The converted site", "ops": [{"op": "add", "path": "/routes/-", "value": {"name": "site", "match": {"path": ["/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}}}]}`},
	"media-setup":           {`{"members": ["listeners", "routes"]}`, `{"summary": "Serve the HLS output", "ops": [{"op": "add", "path": "/routes/0", "value": {"name": "live", "match": {"path": ["/live/*"]}, "handler": {"module": "static", "params": {"root": "/var/lib/fit/hls"}}}}]}`},
	"llm-mesh-setup":        {`{"members": ["listeners"]}`, `{"summary": "Route the mesh's API", "ops": [{"op": "add", "path": "/routes/0", "value": {"name": "llm", "match": {"path": ["/v1/*"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}}}]}`},
	"tunnel-setup":          {`{"members": ["listeners"]}`, `{"summary": "Route the tunnel's web service", "ops": [{"op": "add", "path": "/routes/0", "value": {"name": "home", "match": {"host": ["home.example.com"]}, "handler": {"module": "proxy", "params": {"upstream": "app"}}}}]}`},
	"vod-setup":             {`{"members": ["media_library", "routes"]}`, `{"summary": "Serve the films on demand", "members": {"media_library": [{"name": "films", "roots": ["/srv/media/films"]}]}, "ops": [{"op": "add", "path": "/routes/0", "value": {"name": "vod", "match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": {"library": "films"}}}}]}`},
	"channel-scheduler":     {`{"members": ["streaming"]}`, `{"summary": "Loop the films on tv/films", "members": {"media_library": [{"name": "films", "roots": ["/srv/media/films"]}], "streaming": {"channels": [{"name": "tv/films", "library": "films", "items": ["films/bbb", "films/news"]}]}}}`},
	"library-health":        {`{"library": "films", "errors": true}`, `{"summary": "Keep the broken file aside", "library": "films", "item": "films/broken", "op": "rename", "name": "broken-old.mp4"}`},
	"playback-troubleshoot": {`{}`, `{"summary": "End bob's old player", "policy": "site", "sessions": [{"subject": "{{bob}}", "id": "s3"}]}`},
	"sync-pipeline":         {`{"members": ["sync"]}`, ""},
}

// skillProposes is the propose tool a skill's run calls, when not
// config_propose.
var skillProposes = map[string]string{"cache-tuning": ToolPurgePropose, "library-health": ToolMediaFilePropose,
	"playback-troubleshoot": ToolMediaSessionEndPropose}

// TestBuiltinSkillsRun: each built-in skill, run by /name with its first
// example, does its task on a fixed config against a recorded model: its
// prompt in the system prompt, its tools offered (narrowed to its own and
// the user's level, without the catalog), its read call answered, and its
// proposal, when it makes one, valid; the audit log names it.
func TestBuiltinSkillsRun(t *testing.T) {
	list, err := builtinSkills()
	if err != nil {
		t.Fatal(err)
	}
	for _, sk := range list {
		if slices.Contains(clusterSkills, sk.Name) {
			continue // a manager's: TestClusterSkillsRun
		}
		t.Run(sk.Name, func(t *testing.T) {
			run, ok := skillRuns[sk.Name]
			if !ok {
				t.Fatal("no run for the skill")
			}
			turns := []fakeapi.Turn{{Calls: []fakeapi.Call{call("r1", sk.Tools[0], run[0])}}}
			propose := ""
			if run[1] != "" {
				propose = ToolConfigPropose
				if p, ok := skillProposes[sk.Name]; ok {
					propose = p
				}
				turns = append(turns, fakeapi.Turn{Calls: []fakeapi.Call{call("p1", propose, run[1])}})
			}
			turns = append(turns, fakeapi.Turn{Text: "Done: " + sk.Title})
			f := startProposals(t, `"default_level": "propose"`, turns...)
			// A subject as playback_sessions shows it.
			for i := range turns {
				for k := range turns[i].Calls {
					turns[i].Calls[k].Args = strings.ReplaceAll(turns[i].Calls[k].Args, "{{bob}}", f.a.subjectOut(f.a.Settings(), mediaBob))
				}
			}
			body, _ := json.Marshal(map[string]string{"message": "/" + sk.Name + " " + sk.Examples[0]})
			s := f.chat(string(body))
			if s.end == nil || s.end.Reason != ReasonAnswer {
				t.Fatalf("%+v %s %d %s", s.end, s.err, s.status, s.body)
			}
			if st := s.of(EventStart); st[0].Skill != sk.Name {
				t.Errorf("start: %+v", st[0])
			}
			for _, r := range s.of(EventToolResult) {
				if r.Error {
					t.Errorf("%s failed: %s", r.Name, r.Content)
				}
			}
			reqs := f.model.Requests()
			if len(reqs) != len(turns) {
				t.Fatalf("%d requests", len(reqs))
			}
			sys := fakeapi.System(reqs[0])
			if !strings.Contains(sys, "The current skill is "+sk.Name) || !strings.Contains(sys, sk.Prompt[:60]) ||
				strings.Contains(sys, "Skills you may load") || fakeapi.LastUser(reqs[0]) != sk.Examples[0] {
				t.Errorf("%s\n%s", sys, fakeapi.LastUser(reqs[0]))
			}
			want := []string{"ask_user"}
			for _, st := range f.a.skillTools(sk, "propose", f.a.Settings()) {
				// The page tools only with a page that edits the config, which this chat has not.
				if st.OK && f.a.registry.Tool(st.Name).Kind != KindPage {
					want = append(want, st.Name)
				}
			}
			sort.Strings(want)
			if got := offered(reqs[0]); !slices.Equal(got, want) {
				t.Errorf("offered %v, want %v", got, want)
			}
			conv := s.end.Conversation
			if propose != "" {
				if _, v, body := f.view(conv); v == nil || !v.Valid {
					t.Errorf("the proposal: %s", body)
				}
			}
			if e := f.audit(""); len(e) == 0 || e[0].Skill != sk.Name || e[0].Kind != AuditChat {
				t.Errorf("%+v", e)
			}
			if c := f.conversation(conv); c.Messages[0].Content != sk.Examples[0] {
				t.Errorf("%+v", c.Messages[0])
			}
			noSecrets(t, reqs)
		})
	}
}
