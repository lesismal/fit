package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
	"github.com/lesismal/fit/config"
)

// startMediaChat starts an assistant on fakeConfigs over mediaBase and the
// fixed library, with settings extra, whose model answers turns.
func startMediaChat(t *testing.T, extra string, opts []func(*Options), turns ...fakeapi.Turn) *propFixture {
	t.Helper()
	cfgs := newFakeConfigs(mediaBase)
	sc := &script{turns: turns}
	f := startChat(t, extra, sc.next, append([]func(*Options){func(o *Options) { o.API = cfgs.handler() }}, opts...)...)
	return &propFixture{chatFixture: f, cfgs: cfgs, sc: sc}
}

// sendSubjects sets data.send_subjects.
func sendSubjects(o *Options) { o.Settings.Data.SendSubjects = true }

// mediaSecrets are what no request to a model may carry, the subjects too
// unless sent says they may.
func noMediaSecrets(t *testing.T, reqs []fakeapi.Request, sent bool) {
	t.Helper()
	noSecrets(t, reqs)
	bad := []string{mediaLibSecret, mediaAuthSecret, mediaStreamKey, mediaSig, "alice%40example.com", "sub=alice"}
	if !sent {
		bad = append(bad, mediaAlice, mediaBob, `"carol"`, `"carol"`)
	}
	for i, r := range reqs {
		for _, s := range bad {
			if strings.Contains(string(r.Raw), s) {
				t.Errorf("request %d carries %q", i, s)
			}
		}
	}
}

// setArgs puts what a placeholder stands for in the calls of turns.
func setArgs(turns []fakeapi.Turn, from, to string) {
	for i := range turns {
		for k := range turns[i].Calls {
			turns[i].Calls[k].Args = strings.ReplaceAll(turns[i].Calls[k].Args, from, to)
		}
	}
}

// TestMediaToolsRead: the media tools read the fixed library, its channel
// and the policy's playback; what reaches the model holds no secret of the
// config, no stream key, no signature nor playback URL, and the viewers'
// subjects only as hashes, which a subject's filter takes back.
func TestMediaToolsRead(t *testing.T) {
	turns := []fakeapi.Turn{
		{Calls: []fakeapi.Call{
			call("r1", "media_library_search", `{}`),
			call("r2", "media_library_search", `{"library": "films", "errors": true, "codec": "h264", "q": "b", "min_height": 0, "max_height": 2160, "limit": 10}`),
			call("r3", "media_item_get", `{"library": "films", "item": "films/broken", "sidecar": true}`),
			call("r4", "channels_status", `{}`),
			call("r5", "channels_status", `{"channel": "tv/films"}`),
			call("r6", "playback_sessions", `{}`),
			call("r7", "playback_sessions", `{"policy": "site", "resource": "/vod/films/films/bbb/"}`),
			call("r8", "config_get", `{"members": ["media_library", "media_auth", "streaming"]}`),
			call("r9", "playback_sessions", `{"subject": "{{alice}}", "limit": 1, "offset": 1}`),
		}},
		{Text: "films/broken is fragmented."},
	}
	f := startMediaChat(t, "", nil, turns...)
	alice := f.a.subjectOut(f.a.Settings(), mediaAlice)
	setArgs(turns, "{{alice}}", alice)
	s := f.chat(`{"message": "how is the library?"}`)
	if s.end == nil || s.end.Reason != ReasonAnswer {
		t.Fatalf("%+v %s", s.end, s.err)
	}
	res := s.of(EventToolResult)
	if len(res) != 9 {
		t.Fatalf("%+v", res)
	}
	for _, r := range res {
		if r.Error {
			t.Errorf("%s: %s", r.Name, r.Content)
		}
	}
	want := [][]string{
		{`"name":"films"`, `"errors":1`},
		{`films/broken`, RedactedURL, `"problem_list"`},
		{`"item":{`, `fragmented`, `"usage":{`, `"sidecar":{`, `"exists":false`},
		{`publish refused`, `"streaming_push":[{"name":"live/cam#1"`},
		{`"channel":{"name":"tv/films"`, `"position":42.5`},
		{`"policy":"site"`, `"session_limit":7`, `"key_names":["k1","k0"]`, `"rotatable_key_names":["k1"]`, `"subjects_hashed":true`,
			`"subject":"` + alice + `","playing":2,"at_max_sessions":true`, `"vod_cache"`, `"top_resources"`, `"key_names":["(the single secret)"]`},
		{`"matched":3`, `"id":"s1"`, `"id":"s3"`, `"id":"s4"`},
		{`"url":"[redacted]"`, `"secret":"[redacted]"`, `"library":"films"`},
		{`"matched":2`, `"id":"s2"`, `"limit":1`},
	}
	for i, w := range want {
		for _, x := range w {
			if !strings.Contains(res[i].Content, x) {
				t.Errorf("%s lacks %s: %s", res[i].Name, x, res[i].Content)
			}
		}
	}
	if strings.Contains(res[6].Content, `"id":"s2"`) || strings.Contains(res[8].Content, `"id":"s1"`) {
		t.Errorf("%s\n%s", res[6].Content, res[8].Content)
	}
	noMediaSecrets(t, f.model.Requests(), false)
}

// TestMediaSubjectsSent: with data.send_subjects, the subjects reach the
// model as they are, and a subject's filter goes to the Admin API.
func TestMediaSubjectsSent(t *testing.T) {
	f := startMediaChat(t, "", []func(*Options){sendSubjects},
		fakeapi.Turn{Calls: []fakeapi.Call{call("r1", "playback_sessions", `{"policy": "site", "subject": "`+mediaBob+`"}`)}},
		fakeapi.Turn{Text: "bob plays bbb."})
	s := f.chat(`{"message": "what does bob play?"}`)
	res := s.of(EventToolResult)
	if len(res) != 1 || res[0].Error || !strings.Contains(res[0].Content, `"subject":"`+mediaBob+`"`) || strings.Contains(res[0].Content, mediaAlice) ||
		!strings.Contains(res[0].Content, `"subjects_hashed":false`) {
		t.Fatalf("%+v", res)
	}
	noMediaSecrets(t, f.model.Requests(), true)
	// A session is named by its subject as it is, or by its hash.
	if r := f.tool("", ToolMediaSessionEndPropose, `{"summary": "End bob's", "policy": "site", "sessions": [{"subject": "`+mediaBob+`", "id": "s3"}]}`); r.Error ||
		!strings.Contains(r.Text, mediaBob) {
		t.Errorf("%+v", r)
	}
}

// applyMedia confirms the proposal v shows, with its media tag.
func (f *propFixture) applyMedia(conv string, v *ProposalView) (int, applyMediaWire) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"id": v.ID, "revision": v.Revision, "etag": v.ETag, "user": v.User, "media_tag": v.MediaTag})
	status, out := f.do("POST", "/api/m/ai/proposals/"+conv+"/apply", string(body))
	var res applyMediaWire
	_ = json.Unmarshal([]byte(out), &res)
	res.raw = out
	return status, res
}

type applyMediaWire struct {
	Outcome  string        `json:"outcome"`
	Version  uint64        `json:"version"`
	Media    []mediaResult `json:"media"`
	Proposal *ProposalView `json:"proposal"`
	Error    string        `json:"error"`
	raw      string
}

// TestMediaFileProposal: a delete of a file a channel plays is proposed,
// and the step, the proposal's view and the confirm name the channel and
// the viewers; it is applied only on the confirm of what was shown, as
// the person who applies it, forcing what they were warned of; an item
// that comes into use after the review is offered again with its new
// warning; a change the library refuses at the last moment is the step's
// error, the rest applied.
func TestMediaFileProposal(t *testing.T) {
	turns := []fakeapi.Turn{
		{Calls: []fakeapi.Call{call("p1", ToolMediaFilePropose, `{"summary": "Delete Big Buck Bunny", "library": "films", "item": "films/bbb", "op": "delete"}`)}},
		{Text: "The channel tv/films plays it now; deleting it stops it."},
	}
	f := startMediaChat(t, `"default_level": "apply"`, nil, turns...)
	s := f.chat(`{"message": "delete films/bbb"}`)
	if s.end == nil {
		t.Fatal(s.err)
	}
	res := s.of(EventToolResult)
	if len(res) != 1 || res[0].Error || !strings.Contains(res[0].Content, "the channel tv/films plays it now or next, and names it in its list or schedule") ||
		!strings.Contains(res[0].Content, "2 viewers play it") || !strings.Contains(res[0].Content, `"in_use":true`) {
		t.Fatalf("%+v", res)
	}
	if len(f.cfgs.media.changed()) != 0 {
		t.Fatal("proposing deleted something")
	}
	conv := s.end.Conversation
	_, v, body := f.view(conv)
	if v == nil || !v.Valid || v.Kind != "" || len(v.Media) != 1 || v.MediaTag == "" || v.Media[0].Usage == nil || !v.Media[0].Usage.InUse ||
		!strings.Contains(v.Media[0].Warning, "tv/films") || v.Stale {
		t.Fatalf("%s", body)
	}
	// A confirm that did not see the warning is offered again.
	stale := *v
	stale.MediaTag = ""
	if st, out := f.applyMedia(conv, &stale); st != 412 || out.Outcome != OutcomeStale || !strings.Contains(out.Error, "what uses the files") ||
		out.Proposal == nil || out.Proposal.Revision != v.Revision+1 {
		t.Fatalf("%d %s", st, out.raw)
	}
	_, v, _ = f.view(conv)
	st, out := f.applyMedia(conv, v)
	if st != 200 || out.Outcome != OutcomeApplied || len(out.Media) != 1 || out.Media[0].Error != "" || !strings.Contains(string(out.Media[0].Result), `"op":"delete"`) {
		t.Fatalf("%d %s", st, out.raw)
	}
	if got := f.cfgs.media.changed(); !slices.Equal(got, []string{"POST /media/library/films/delete ada"}) || !strings.Contains(f.cfgs.media.lastBody(), `"force":true`) {
		t.Fatalf("%v %s", got, f.cfgs.media.lastBody())
	}
	if len(f.cfgs.changed()) != 0 {
		t.Errorf("the config changed: %v", f.cfgs.changed())
	}
	if e := f.audit(""); e[0].Kind != AuditApply || e[0].Outcome != OutcomeApplied || e[0].Proposal == nil || e[0].Proposal.Steps[0].Tool != ToolMediaFilePropose {
		t.Errorf("%+v", e[0])
	}

	// An item not in use when reviewed, in use when applied.
	f.sc.mu.Lock()
	f.sc.turns, f.sc.n = []fakeapi.Turn{
		{Calls: []fakeapi.Call{call("p2", ToolMediaFilePropose, `{"summary": "Rename the broken film", "library": "films", "item": "films/broken", "op": "rename", "name": "broken-old.mp4"}`)}},
		{Text: "Proposed."}}, 0
	f.sc.mu.Unlock()
	s = f.chat(`{"conversation": "` + conv + `", "message": "rename films/broken"}`)
	if r := s.of(EventToolResult); len(r) != 1 || r[0].Error || strings.Contains(r[0].Content, "warning") {
		t.Fatalf("%+v", r)
	}
	_, v, _ = f.view(conv)
	if v.Media[0].Warning != "" || v.MediaTag == "" {
		t.Fatalf("%+v", v.Media)
	}
	f.cfgs.media.setUsage("films/broken", ItemUsage{Channels: []string{"tv/late"}, Playing: []string{}, Counted: true, InUse: true})
	st, out = f.applyMedia(conv, v)
	if st != 412 || out.Proposal == nil || !strings.Contains(out.Proposal.Media[0].Warning, "the channel tv/late names it") {
		t.Fatalf("%d %s", st, out.raw)
	}
	if st, out = f.applyMedia(conv, out.Proposal); st != 200 || out.Outcome != OutcomeApplied {
		t.Fatalf("%d %s", st, out.raw)
	}
	if got := f.cfgs.media.changed(); len(got) != 2 || got[1] != "POST /media/library/films/rename ada" ||
		!strings.Contains(f.cfgs.media.lastBody(), `"name":"broken-old.mp4"`) || !strings.Contains(f.cfgs.media.lastBody(), `"force":true`) {
		t.Fatalf("%v %s", got, f.cfgs.media.lastBody())
	}

	// The library refuses it at the last moment: the step fails, and says why.
	f.sc.mu.Lock()
	f.sc.turns, f.sc.n = []fakeapi.Turn{
		{Calls: []fakeapi.Call{call("p3", ToolMediaFilePropose, `{"summary": "Move the news", "library": "films", "item": "films/news", "op": "move", "dir": "archive"}`)}},
		{Text: "Proposed."}}, 0
	f.sc.mu.Unlock()
	f.chat(`{"conversation": "` + conv + `", "message": "move the news to archive"}`)
	f.cfgs.media.failWith("POST /media/library/films/move", 409)
	_, v, _ = f.view(conv)
	st, out = f.applyMedia(conv, v)
	if st != 200 || out.Outcome != OutcomePartial || !strings.Contains(out.Media[0].Error, "came into use after the proposal was reviewed") {
		t.Fatalf("%d %s", st, out.raw)
	}
	if e := f.audit(""); e[0].Outcome != OutcomePartial || !strings.Contains(e[0].Detail, "came into use") {
		t.Errorf("%+v", e[0])
	}
	if !strings.Contains(f.logText(), "failed: the item came into use") {
		t.Error(f.logText())
	}
	noMediaSecrets(t, f.model.Requests(), false)
}

// tool calls the tool name as ada's MCP token of the level propose, in her
// conversation conv ("" for a new one).
func (f *propFixture) tool(conv, name, args string) ExternalResult {
	f.t.Helper()
	return f.a.CallTool(context.Background(), External{User: "ada", Level: config.AssistantPropose, Source: SourceMCP, Token: "t", Conversation: conv},
		name, json.RawMessage(args))
}

// TestMediaProposeChecks: each media step is checked on what runs now, and
// one that is wrong is refused, saying why, and not added.
func TestMediaProposeChecks(t *testing.T) {
	f := startMediaChat(t, `"default_level": "apply"`, nil)
	alice := f.a.subjectOut(f.a.Settings(), mediaAlice)
	carol := f.a.subjectOut(f.a.Settings(), "carol")
	sum := `"summary": "x", `
	for _, c := range []struct{ tool, args, err string }{
		{ToolMediaScanPropose, `{` + sum + `"library": ""}`, "library is the library's name"},
		{ToolMediaScanPropose, `{` + sum + `"library": "music"}`, `no media library named "music"`},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "site", "sessions": []}`, "sessions are 1 to 100"},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "", "sessions": [{"subject": "a", "id": "s1"}]}`, "policy is a media_auth policy's name"},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "gone", "sessions": [{"subject": "a", "id": "s1"}]}`, `no media_auth policy named "gone"`},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "staff", "sessions": [{"subject": "a", "id": "s1"}]}`, "has no url_signing"},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "site", "sessions": [{"subject": "a", "id": "s1"}, {"subject": "a", "id": "s1"}]}`, "each once"},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "site", "sessions": [{"subject": "` + carol + `", "id": "s4"}]}`, "none of the sessions plays now"},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "site", "sessions": [{"subject": "` + mediaAlice + `", "id": "s1"}]}`, "none of the sessions plays now"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site", "key": "k0"}`, `the key "k0" of site is written in the config`},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site", "all": true}`, "is written in the config"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site", "key": "k1", "all": true}`, "not both"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site"}`, "has a keyring: name the key"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site", "key": "k9"}`, `has no key "k9"`},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "staff", "all": true}`, "has a single secret"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "inline"}`, "is written in the config"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "nope"}`, "no media_auth policy"},
		{ToolMediaFilePropose, `{` + sum + `"library": "", "item": "films/bbb", "op": "delete"}`, "library is the library's name"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "../etc", "op": "delete"}`, "is not an item's ID"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/bbb", "op": "copy"}`, "op is rename, move, delete or sidecar"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/bbb", "op": "rename", "name": "a/b.mp4"}`, "without a directory"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/bbb", "op": "move", "dir": "` + strings.Repeat("d", 1100) + `"}`, "dir is a directory"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/bbb", "op": "sidecar", "sidecar": {"title": "x", "rating": 5}}`, "sidecar is the item's sidecar"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/bbb", "op": "sidecar"}`, "sidecar is the item's sidecar"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/bbb", "op": "sidecar", "sidecar": {"id": "a b"}}`, "is not an item's ID"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/none", "op": "delete"}`, `no item "films/none"`},
	} {
		if r := f.tool("", c.tool, c.args); !r.Error || !strings.Contains(r.Text, c.err) || !strings.Contains(r.Text, "the step was not added") {
			t.Errorf("%s %s: %+v", c.tool, c.args, r)
		}
	}

	// A step on what it acts on already, in the same proposal.
	r := f.tool("", ToolMediaScanPropose, `{`+sum+`"library": "films"}`)
	conv := r.Conversation
	for _, c := range []struct{ tool, args, err string }{
		{ToolMediaScanPropose, `{` + sum + `"library": "films"}`, "scans films already"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site", "key": "k1"}`, ""},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "site", "key": "k1"}`, "rotates a key of site already"},
		{ToolMediaKeyRotatePropose, `{` + sum + `"policy": "staff"}`, ""},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "site", "sessions": [{"subject": "` + alice + `", "id": "s1"}, {"subject": "` + carol + `", "id": "s4"}]}`, ""},
		{ToolMediaSessionEndPropose, `{` + sum + `"policy": "site", "sessions": [{"subject": "` + alice + `", "id": "s2"}]}`, "ends sessions of site already"},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/news", "op": "sidecar", "sidecar": {"id": "films/news", "title": "The News"}}`, ""},
		{ToolMediaFilePropose, `{` + sum + `"library": "films", "item": "films/news", "op": "delete"}`, "changes the files of films/news already"},
	} {
		r := f.tool(conv, c.tool, c.args)
		if c.err == "" && r.Error || c.err != "" && (!r.Error || !strings.Contains(r.Text, c.err)) {
			t.Errorf("%s %s: %+v", c.tool, c.args, r)
		}
	}
	// The model is shown the sessions' subjects hashed, and what it left
	// out.
	_, v, body := f.view(conv)
	if v == nil || !v.Valid || len(v.Media) != 5 || v.Media[3].Warning == "" || len(v.Media[3].Sessions) != 1 || v.Media[3].Sessions[0].Subject != mediaAlice ||
		!strings.Contains(v.Media[4].Diff, `-  "title": "News"`) || !strings.Contains(v.Media[4].Diff, `+  "title": "The News"`) {
		t.Fatalf("%s", body)
	}
	if r := f.tool(conv, ToolMediaScanPropose, `{`+sum+`"library": "music"}`); !r.Error || strings.Contains(r.Text, mediaAlice) {
		t.Errorf("%+v", r)
	}
	// An item without a sidecar is diffed from the draft of its files' names.
	if r := f.tool("", ToolMediaFilePropose, `{`+sum+`"library": "films", "item": "films/bbb", "op": "sidecar", "sidecar": {"title": "BBB"}}`); r.Error ||
		!strings.Contains(r.Text, "a draft of the files' names") {
		t.Errorf("%+v", r)
	}
	// The single secret is named as playback_sessions shows it.
	if r := f.tool("", ToolMediaKeyRotatePropose, `{`+sum+`"policy": "staff", "key": "`+singleSecret+`"}`); r.Error {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.failWith("GET /media/library/films/sidecar", 500)
	if r := f.tool("", ToolMediaFilePropose, `{`+sum+`"library": "films", "item": "films/bbb", "op": "sidecar", "sidecar": {}}`); !r.Error {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.failWith("GET /media/library/films/usage", 500)
	if r := f.tool("", ToolMediaFilePropose, `{`+sum+`"library": "films", "item": "films/bbb", "op": "delete"}`); !r.Error {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.failWith("GET /media/auth", 500)
	f.cfgs.media.failWith("GET /media/auth/site/sessions", 500)
	if r := f.tool("", ToolMediaKeyRotatePropose, `{`+sum+`"policy": "site"}`); !r.Error {
		t.Errorf("%+v", r)
	}
}

// TestMediaSessionsFailed: a session step whose sessions cannot be read is
// refused.
func TestMediaSessionsFailed(t *testing.T) {
	f := startMediaChat(t, `"default_level": "apply"`, nil)
	f.cfgs.media.failWith("GET /media/auth/site/sessions", 500)
	if r := f.tool("", ToolMediaSessionEndPropose, `{"summary": "x", "policy": "site", "sessions": [{"subject": "a", "id": "s1"}]}`); !r.Error {
		t.Errorf("%+v", r)
	}
	if r := f.tool("", "playback_sessions", `{"policy": "site"}`); !r.Error {
		t.Errorf("%+v", r)
	}
}

// TestMediaApplyAll: a proposal of a config change and a step of each
// media tool is applied on the confirm: the config first, as one change,
// then the media steps in their order, as the person, each with what it
// was checked on: the subjects as they are, the sidecar's time; a step
// that fails leaves the others applied, and the outcome partial.
func TestMediaApplyAll(t *testing.T) {
	f := startMediaChat(t, `"default_level": "apply"`, nil)
	a := f.a.Settings()
	alice, bob := f.a.subjectOut(a, mediaAlice), f.a.subjectOut(a, mediaBob)
	r := f.tool("", ToolConfigPropose, `{"summary": "Another backend", "members": {"upstreams": [{"name": "app", "addresses": ["10.0.0.7:9000", "10.0.0.8:9000"]}]}}`)
	conv := r.Conversation
	for _, c := range [][2]string{
		{ToolMediaScanPropose, `{"summary": "Scan films", "library": "films"}`},
		{ToolMediaSessionEndPropose, `{"summary": "End two players", "policy": "site", "sessions": [{"subject": "` + alice + `", "id": "s1"}, {"subject": "` + bob + `", "id": "s3"}]}`},
		{ToolMediaKeyRotatePropose, `{"summary": "Rotate k1", "policy": "site", "key": "k1"}`},
		{ToolMediaFilePropose, `{"summary": "Retitle the news", "library": "films", "item": "films/news", "op": "sidecar", "sidecar": {"id": "films/news", "title": "The News"}}`},
	} {
		r := f.tool(conv, c[0], c[1])
		if r.Error || r.Conversation != conv {
			t.Fatalf("%s: %+v", c[0], r)
		}
		if c[0] == ToolMediaSessionEndPropose && (strings.Contains(r.Text, mediaAlice) || !strings.Contains(r.Text, alice)) {
			t.Errorf("the model is shown %s", r.Text)
		}
	}
	_, v, body := f.view(conv)
	if v == nil || !v.Valid || v.Kind != KindPatch || len(v.Media) != 4 || v.MediaTag == "" {
		t.Fatalf("%s", body)
	}
	st, out := f.applyMedia(conv, v)
	if st != 200 || out.Outcome != OutcomeApplied || out.Version != 2 || len(out.Media) != 4 {
		t.Fatalf("%d %s", st, out.raw)
	}
	if got := f.cfgs.changed(); len(got) != 1 || !strings.HasPrefix(got[0], "PUT /config/text") {
		t.Errorf("%v", got)
	}
	want := []string{"POST /media/library/films/rescan ada", "POST /media/auth/site/sessions/end ada", "POST /media/auth/site/sessions/end ada",
		"POST /media/auth/site/rotate ada", "PUT /media/library/films/sidecar ada"}
	f.cfgs.media.mu.Lock()
	changes, bodies := slices.Clone(f.cfgs.media.changes), slices.Clone(f.cfgs.media.bodies)
	f.cfgs.media.mu.Unlock()
	if !slices.Equal(changes, want) || !strings.Contains(bodies[1], mediaAlice) || !strings.Contains(bodies[2], mediaBob) ||
		!strings.Contains(bodies[3], `"key":"k1"`) || !strings.Contains(bodies[4], `"modified":"2026-11-01T00:00:00Z"`) || !strings.Contains(bodies[4], `"force":true`) {
		t.Errorf("%v %v", changes, bodies)
	}
	if !strings.Contains(string(out.Media[1].Result), `"ended":2`) || !strings.Contains(string(out.Media[2].Result), `"rotated":["k1"]`) {
		t.Errorf("%s", out.raw)
	}

	// Each fails its own way, the outcome partial.
	r = f.tool("", ToolMediaScanPropose, `{"summary": "Scan films", "library": "films"}`)
	conv = r.Conversation
	f.tool(conv, ToolMediaSessionEndPropose, `{"summary": "End", "policy": "site", "sessions": [{"subject": "`+alice+`", "id": "s2"}]}`)
	f.tool(conv, ToolMediaKeyRotatePropose, `{"summary": "Rotate", "policy": "staff"}`)
	f.cfgs.media.failWith("POST /media/library/films/rescan", 500)
	f.cfgs.media.failWith("POST /media/auth/site/sessions/end", 404)
	f.cfgs.media.failWith("POST /media/auth/staff/rotate", 409)
	_, v, body = f.view(conv)
	if v == nil || v.MediaTag != "" || len(v.Media) != 3 {
		t.Fatalf("%s", body)
	}
	st, out = f.applyMedia(conv, v)
	if st != 200 || out.Outcome != OutcomePartial || out.Media[0].Error != "scripted failure" || out.Media[1].Error != "" ||
		!strings.Contains(string(out.Media[1].Result), `"ended_already":1`) || out.Media[2].Error != "the item is in use" {
		t.Fatalf("%d %s", st, out.raw)
	}
	r = f.tool("", ToolMediaSessionEndPropose, `{"summary": "End", "policy": "site", "sessions": [{"subject": "`+alice+`", "id": "s2"}]}`)
	f.cfgs.media.failWith("POST /media/auth/site/sessions/end", 500)
	_, v, _ = f.view(r.Conversation)
	if st, out = f.applyMedia(r.Conversation, v); st != 200 || out.Outcome != OutcomePartial || out.Media[0].Error != "scripted failure" {
		t.Fatalf("%d %s", st, out.raw)
	}
}

// TestMediaReadEdges: the read tools' arguments are checked, and what the
// Admin API refuses is the call's error.
func TestMediaReadEdges(t *testing.T) {
	f := startMediaChat(t, `"default_level": "apply"`, nil)
	for _, c := range [][3]string{
		{"media_library_search", `{"library": "films", "min_height": -1}`, "min_height is 0 or more"},
		{"media_library_search", `{"library": "films", "offset": -1}`, "offset is 0 or more"},
		{"media_library_search", `{"library": "films", "limit": 1000}`, "limit is 1 to 100"},
		{"media_library_search", `{"library": "music"}`, "no media library named"},
		{"media_library_search", `{"library": 7}`, "the arguments are not what the tool takes"},
		{"media_item_get", `{"library": "", "item": "a"}`, "library is the library's name"},
		{"media_item_get", `{"library": "films", "item": "/a"}`, "is not an item's ID"},
		{"media_item_get", `{"library": "films", "item": "films/none"}`, "no item"},
		{"media_item_get", `{"item": 1}`, "the arguments are not"},
		{"channels_status", `{"channel": "tv/none"}`, "no channel named"},
		{"channels_status", `{"channel": 1}`, "the arguments are not"},
		{"playback_sessions", `{"policy": "nope"}`, `no media_auth policy named "nope"`},
		{"playback_sessions", `{"limit": 500}`, "limit is 1 to 200"},
		{"playback_sessions", `{"policy": 1}`, "the arguments are not"},
	} {
		if r := f.tool("", c[0], c[1]); !r.Error || !strings.Contains(r.Text, c[2]) {
			t.Errorf("%s %s: %+v", c[0], c[1], r)
		}
	}
	// What a part of the answer could not read is said in its place.
	f.cfgs.media.failWith("GET /media/library/films/usage", 500)
	f.cfgs.media.failWith("GET /media/library/films/sidecar", 500)
	if r := f.tool("", "media_item_get", `{"library": "films", "item": "films/bbb", "sidecar": true}`); r.Error ||
		!strings.Contains(r.Text, `"usage_error"`) || !strings.Contains(r.Text, `"sidecar_error"`) {
		t.Errorf("%+v", r)
	}
	// A read past the sessions it reads says so.
	sessionsPage, maxSessionsScan = 1, 2
	t.Cleanup(func() { sessionsPage, maxSessionsScan = 1000, 20000 })
	if r := f.tool("", "playback_sessions", `{"policy": "site"}`); r.Error || !strings.Contains(r.Text, "the policy has 4 sessions, and the 2 first by subject were read") {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.failWith("GET /streaming/channels", 404)
	if r := f.tool("", "channels_status", `{}`); !r.Error {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.failWith("GET /media/library", 500)
	if r := f.tool("", "media_library_search", `{}`); !r.Error {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.mu.Lock()
	f.cfgs.media.noPolicies = true
	f.cfgs.media.mu.Unlock()
	if r := f.tool("", "playback_sessions", `{}`); r.Error || !strings.Contains(r.Text, "no media_auth policy runs here") {
		t.Errorf("%+v", r)
	}
	f.cfgs.media.failWith("GET /media/auth", 500)
	if r := f.tool("", "playback_sessions", `{}`); !r.Error {
		t.Errorf("%+v", r)
	}
	// A JSON answer that is not what it should be.
	if err := f.a.apiGet(context.Background(), "/media/library/films/items/films/bbb", &[]int{}); err == nil || !strings.Contains(err.Error(), "not what it should be") {
		t.Error(err)
	}
}

// TestMediaPoliciesNamed: with several policies that sign URLs and none
// named, playback_sessions lists them and asks for one.
func TestMediaPoliciesNamed(t *testing.T) {
	f := startMediaChat(t, "", nil)
	r := f.tool("", "playback_sessions", `{"policy": ""}`)
	if r.Error || !strings.Contains(r.Text, `"policy":"site"`) {
		t.Fatalf("%+v", r)
	}
	f.cfgs.media.mu.Lock()
	f.cfgs.media.twoSigning = true
	f.cfgs.media.mu.Unlock()
	if r := f.tool("", "playback_sessions", `{}`); r.Error || !strings.Contains(r.Text, "name a policy to list its sessions: site, inline") {
		t.Errorf("%+v", r)
	}
	// A policy named is listed whole; one that does not sign has no
	// sessions to list.
	if r := f.tool("", "playback_sessions", `{"policy": "staff"}`); !r.Error {
		t.Errorf("%+v", r)
	}
}

// TestMediaRedaction: a playback URL, as text, as JSON writes it or as a
// path, a signature and a grant's cookie are redacted wherever they are;
// a push target's url is a secret of the config, refused to a proposal
// and to a page's draft.
func TestMediaRedaction(t *testing.T) {
	r := newRedactor(nil, nil)
	for _, c := range []struct{ in, gone, keep string }{
		{"GET /vod/films/a/index.m3u8?exp=1&sid=x&sub=alice&sig=abcdef HTTP/1.1", "alice", "GET " + RedactedURL + " HTTP/1.1"},
		{`{"url":"https://h.example/vod/a/v0.m3u8?exp=1&sub=bob&sig=xyz123"}`, "bob", `{"url":"` + RedactedURL + `"}`},
		{"v0.12.m4s?exp=1&g=1&sig=qq77 failed", "qq77", RedactedURL + " failed"},
		{"Cookie: fit_media_grant=exp%3D1%26sig%3Dabc9", "abc9", "fit_media_grant=" + Redacted},
		{"the query exp=1&sig=zz99 was refused", "zz99", "&sig=" + Redacted},
		{"a signature is checked", "", "a signature is checked"},
	} {
		got := r.text(c.in)
		if c.gone != "" && strings.Contains(got, c.gone) || !strings.Contains(got, c.keep) {
			t.Errorf("%s: %s", c.in, got)
		}
	}
	for _, c := range []struct {
		tokens []string
		want   bool
	}{
		{[]string{"streaming", "push", "0", "url"}, true},
		{[]string{"streaming", "channels", "1", "push", "0", "url"}, true},
		{[]string{"routes", "0", "url"}, false},
		{[]string{"url"}, false},
		{[]string{"media_auth", "0", "secret"}, true},
		{nil, false},
	} {
		if got := secretPath(c.tokens); got != c.want {
			t.Errorf("%v: %v", c.tokens, got)
		}
	}
	tree, _ := decodeTree([]byte(`{"channels": [{"push": [{"url": "rtmp://evil/key"}]}]}`))
	if err := guardDraft([]string{"streaming"}, tree, false); err == nil || !strings.Contains(err.Error(), "/streaming/channels/0/push/0/url") {
		t.Error(err)
	}
	tree, _ = decodeTree([]byte(`{"channels": [{"push": [{"url": "[redacted]"}]}]}`))
	if err := guardDraft([]string{"streaming"}, tree, false); err != nil {
		t.Error(err)
	}

	f := startMediaChat(t, `"default_level": "apply"`, nil)
	if r := f.tool("", ToolConfigPropose, `{"summary": "x", "ops": [{"op": "replace", "path": "/streaming/channels/0/push/0/url", "value": "rtmp://evil.example/live/k"}]}`); !r.Error ||
		!strings.Contains(r.Text, "holds a secret") {
		t.Errorf("%+v", r)
	}
	if r := f.tool("", ToolConfigPropose, `{"summary": "Name the push", "ops": [{"op": "replace", "path": "/streaming/channels/0/push/0", "value": {"name": "youtube", "url": "[redacted]"}}]}`); r.Error {
		t.Errorf("%+v", r)
	} else if _, v, _ := f.view(r.Conversation); v == nil || !v.Valid || strings.Contains(v.Diff["json"], mediaStreamKey) {
		t.Errorf("%+v", v)
	}
}

// TestMediaHelpers: what the media tools make of answers that are not
// JSON, and an apply without the Admin API.
func TestMediaHelpers(t *testing.T) {
	if got := indentJSON([]byte("not json")); got != "not json\n" {
		t.Errorf("%q", got)
	}
	a := &Assistant{o: Options{API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "plain text") })}}
	if got, err := a.apiText(context.Background(), "/x"); err != nil || got != "plain text" {
		t.Errorf("%q %v", got, err)
	}
	a = &Assistant{}
	if res := a.applyMedia(context.Background(), "ada", MediaAction{Step: 1, Op: MediaScan, Library: "films"}); res.Error != errNoAPI.Error() {
		t.Errorf("%+v", res)
	}
}

// TestMediaToolsServed: the media tools are served to outside agents, read
// ones to a read token, the propose ones to a propose token alone.
func TestMediaToolsServed(t *testing.T) {
	f := startMediaChat(t, "", nil)
	names := func(level string) []string {
		var out []string
		for _, t := range f.a.ExternalTools(level) {
			out = append(out, t.Name)
		}
		return out
	}
	read, propose := names(config.AssistantRead), names(config.AssistantPropose)
	for _, n := range []string{"media_library_search", "media_item_get", "channels_status", "playback_sessions"} {
		if !slices.Contains(read, n) {
			t.Errorf("read lacks %s", n)
		}
	}
	for _, n := range []string{ToolMediaScanPropose, ToolMediaSessionEndPropose, ToolMediaKeyRotatePropose, ToolMediaFilePropose} {
		if slices.Contains(read, n) || !slices.Contains(propose, n) {
			t.Errorf("%s: %v %v", n, read, propose)
		}
	}
	if tools := f.a.Registry().Tools(); tools[len(tools)-1].Group == GroupMedia {
		t.Error("the media group sorts last")
	}
}
