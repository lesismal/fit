package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
)

// The cluster tools (I153) run on a manager of other fits (admin.cluster,
// I141): they read its members, templates, rollouts, metrics and
// certificates, and propose a change across members as a rollout plan
// (rollout_propose, drift_fix_propose) or an operation (cluster_op_propose),
// which a person starts on the Cluster pages with that page's own confirm:
// none of them is a step of the conversation's proposal, and the panel's
// Apply never starts one. The read tools that read the Admin API take a
// member argument there, which reads that member instead, through the
// manager's session with it. Every call acts as the user, whose roles on
// the manager the manager checks (I148), beside the assistant's level,
// which the registry checks and the roles cap (Options.LevelCap): the
// narrower wins. Nothing of a member's credentials reaches a tool's
// result: the manager answers none, and the result is redacted as every
// tool's is.

// Cluster is a manager of other fits as the cluster tools reach it:
// package cluster's Manager.Assistant(). Each method acts as user, checks
// the user's roles, and answers JSON.
type Cluster interface {
	Members(user, group, selector, status string) ([]byte, error)
	Templates(user, name string) ([]byte, error)
	Rollouts(user, id string) ([]byte, error)
	Metrics(ctx context.Context, user, group string, nodes []string) ([]byte, error)
	Certs(ctx context.Context, user, group string, nodes []string, within int) ([]byte, error)
	Logs(ctx context.Context, user, group string, nodes []string, logger, level, contains string, limit int) ([]byte, error)
	PlanRollout(ctx context.Context, user string, req []byte) ([]byte, error)
	CheckOp(user string, req []byte) ([]byte, error)
	FixDrift(ctx context.Context, user string, nodes []string) ([]byte, error)
	MemberCall(ctx context.Context, user, member, method, path string, body []byte) (status int, header http.Header, data []byte, note string, err error)
}

// GroupCluster is the cluster tools' group.
const GroupCluster = "cluster"

// The cluster's propose tools.
const (
	ToolRolloutPropose   = "rollout_propose"
	ToolClusterOpPropose = "cluster_op_propose"
	ToolDriftFixPropose  = "drift_fix_propose"
)

// clusterPropose reports whether name is a cluster propose tool's.
func clusterPropose(name string) bool {
	return name == ToolRolloutPropose || name == ToolClusterOpPropose || name == ToolDriftFixPropose
}

// The bounds of the cluster tools' waits, inside the chat's toolTimeout:
// a read across members, a plan's members read and dry run, a member's
// read.
var (
	clusterReadWait = 20 * time.Second
	clusterPlanWait = 20 * time.Second
	memberReadWait  = 20 * time.Second
)

// memberTools are the read tools that take a member argument on a
// manager: those that read the Admin API.
var memberTools = []string{"config_get", "versions_diff", "status_get", "metrics_query", "upstream_health", "consistency_check",
	"certs_list", "cache_zones", "media_library_search", "media_item_get", "channels_status", "playback_sessions",
	"sync_pipelines", "sync_dead_letters", "bt_torrents", "bt_torrent", "bt_tracker_stats", "bt_rss"}

// clusterSkills are the built-in skills offered on a manager only.
var clusterSkills = []string{"fleet-health", "rollout-planner", "cluster-drift"}

// errNoCluster is a cluster tool on a fit that manages no other.
var errNoCluster = errors.New("this fit manages no other fits (admin.cluster)")

const (
	nodesSchema = `"nodes":{"type":"array","items":{"type":"string"},"description":"members by name; the group's, or every member, when left out"}`
	groupSchema = `"group":{"type":"string","description":"a group's name"}`
)

// clusterTools returns the cluster tools, which run on cl; nil cl makes
// them for the registry's tests, and they fail.
func clusterTools(cl Cluster) []*Tool {
	need := func(run func(c *Call, cl Cluster) (string, error)) func(c *Call) (string, error) {
		return func(c *Call) (string, error) {
			if cl == nil {
				return "", errNoCluster
			}
			return run(c, cl)
		}
	}
	return []*Tool{
		{Name: "cluster_members", Group: GroupCluster, Level: config.AssistantRead,
			Description: "The members this manager manages, as its poller last read them: each one's status (up, down, error, unknown) and since when, its last error, latency, fit and fib versions, uptime, config version, consistency drift, sync with its templates, nearest certificate expiry and days left, connections, reloads, upstream backends down, and alerts firing on it; filtered by group, label selector (k=v, k!=v, k, !k) or status, with the counts by status.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + groupSchema + `,"selector":{"type":"string","description":"a label selector, as region=eu,tier!=edge"},"status":{"type":"string","enum":["up","down","error","unknown"]}}}`),
			Run: need(func(c *Call, cl Cluster) (string, error) {
				var a struct{ Group, Selector, Status string }
				if err := c.Decode(&a); err != nil {
					return "", err
				}
				return c.clusterJSON(cl.Members(c.User, a.Group, a.Selector, a.Status))
			})},
		{Name: "cluster_templates", Group: GroupCluster, Level: config.AssistantRead,
			Description: "The templates: config a group's members share. Each one's parts (top-level members, and routes, upstreams, listeners and services by name), variables, groups, members and how many are in sync, out of sync, in conflict or in error; with template, that one's config (secrets masked as [secret]) and each member's parts as last read, with why one is out of sync or in conflict.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"template":{"type":"string","description":"one template, whole"}}}`),
			Run: need(func(c *Call, cl Cluster) (string, error) {
				var a struct{ Template string }
				if err := c.Decode(&a); err != nil {
					return "", err
				}
				return c.clusterJSON(cl.Templates(c.User, a.Template))
			})},
		{Name: "cluster_rollouts", Group: GroupCluster, Level: config.AssistantRead,
			Description: "Rollouts planned, under way and past, the latest first, batch by batch with each member's status, versions, gate and error, and the latest operations on many members with each one's outcome; with rollout, that one whole, with each member's changes and dry run.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"rollout":{"type":"string","description":"a rollout's ID"}}}`),
			Run: need(func(c *Call, cl Cluster) (string, error) {
				var a struct{ Rollout string }
				if err := c.Decode(&a); err != nil {
					return "", err
				}
				return c.clusterJSON(cl.Rollouts(c.User, a.Rollout))
			})},
		{Name: "cluster_metrics", Group: GroupCluster, Level: config.AssistantRead,
			Description: "Each member's requests by status class, connections open and refused, reloads and upstream backends down, read now from the members, and their sums by group (counters since each member started); and the manager's series of the last polls, per member and group: requests, 4xx and 5xx a second, connections, reloads. A member slow or down is reported, not waited for.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + groupSchema + `,` + nodesSchema + `}}`),
			Run: need(func(c *Call, cl Cluster) (string, error) {
				var a struct {
					Group string
					Nodes []string
				}
				if err := c.Decode(&a); err != nil {
					return "", err
				}
				ctx, cancel := context.WithTimeout(c.Context, clusterReadWait)
				defer cancel()
				return c.clusterJSON(cl.Metrics(ctx, c.User, a.Group, a.Nodes))
			})},
		{Name: "cluster_certs", Group: GroupCluster, Level: config.AssistantRead,
			Description: "The certificate inventory: every certificate the members serve, with its member, listener, names, issuer, serial, expiry, days left and the member's status, the one that expires first first, as each member's latest poll read them; within_days keeps those expiring within that many days.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + groupSchema + `,` + nodesSchema + `,"within_days":{"type":"integer","description":"only certificates expiring within these days"}}}`),
			Run: need(func(c *Call, cl Cluster) (string, error) {
				var a struct {
					Group      string
					Nodes      []string
					WithinDays int `json:"within_days"`
				}
				if err := c.Decode(&a); err != nil {
					return "", err
				}
				ctx, cancel := context.WithTimeout(c.Context, clusterReadWait)
				defer cancel()
				return c.clusterJSON(cl.Certs(ctx, c.User, a.Group, a.Nodes, a.WithinDays))
			})},
		{Name: "cluster_log_tail", Group: GroupCluster, Level: config.AssistantRead,
			Description: "The latest lines of members' own logs, merged by time, oldest first: of nodes or a group's members (16 at most), by logger, least level and text, limit lines a member (50 when left out). Each member's outcome is said; one slow or down does not hold the others. Secrets are redacted.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` + groupSchema + `,"nodes":{"type":"array","items":{"type":"string"},"description":"members by name; the group's when left out"},` +
				`"logger":{"type":"string"},"level":{"type":"string","enum":["debug","info","warn","error"]},"contains":{"type":"string"},"limit":{"type":"integer","description":"lines a member, 50 when left out"}}}`),
			Enabled: func(s *config.Assistant) (bool, string) {
				return s.Data != nil && s.Data.SendLogs, "data.send_logs is off"
			},
			Run: need(func(c *Call, cl Cluster) (string, error) {
				var a struct {
					Group, Logger, Level, Contains string
					Nodes                          []string
					Limit                          int
				}
				if err := c.Decode(&a); err != nil {
					return "", err
				}
				ctx, cancel := context.WithTimeout(c.Context, clusterReadWait)
				defer cancel()
				return c.clusterJSON(cl.Logs(ctx, c.User, a.Group, a.Nodes, a.Logger, a.Level, a.Contains, a.Limit))
			})},
		{Name: ToolRolloutPropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Plan a change across members as a rollout: each member's diff, checked by the member's own dry run, nothing applied. kind template rolls out a template as it is, or, with config, a draft of its new config, saved as the template when a person starts the rollout (a cluster-wide admin's); sync brings members in sync with their templates; patch puts config (top-level members whole, named routes, upstreams, listeners and services by name) on nodes or a group's members as they are, for config no template owns (an admin's of each member); copy copies paths from the member from. Secrets stay [redacted], which keeps each one's own. strategy suggests how a person runs it. The answer is the plan with its link: a person starts it on the Rollouts page, with its confirm.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"kind":{"type":"string","enum":["template","sync","patch","copy"]},"template":{"type":"string"},` + groupSchema + `,` + nodesSchema +
				`,"from":{"type":"string","description":"copy: the member copied from"},"paths":{"type":"array","items":{"type":"string"},"description":"copy: the parts, as log or routes[api]"},"config":{"type":"object","description":"patch: the parts put on the members; template: the template's whole new config"},"description":{"type":"string"},` +
				`"strategy":{"type":"object","description":"suggested: mode all, rolling or canary; batch_size or batch_percent; canary (members first); pause_ms; on_failure batch or all; gate {wait_ms, max_5xx_ratio, window_ms, min_requests}"}},"required":["summary","kind"]}`),
			Run: need(toolRolloutPropose)},
		{Name: ToolClusterOpPropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Propose an operation on many members: reload (apply the running config again), rollback (to the previous kept version), check (the consistency check), purge (cache zones by keys, prefixes, tags or all) or test (reach each one). It is checked, not started: the answer links the operations page, filled in, where a person starts it with its confirm.",
			Parameters: json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"op":{"type":"string","enum":["reload","rollback","check","purge","test"]},` + groupSchema +
				`,"members":{"type":"array","items":{"type":"string"}},"purge":{"type":"object","properties":{"zone":{"type":"string"},"keys":{"type":"array","items":{"type":"string"}},"prefixes":{"type":"array","items":{"type":"string"}},"tags":{"type":"array","items":{"type":"string"}},"all":{"type":"boolean"}}}},"required":["summary","op"]}`),
			Run: need(toolClusterOpPropose)},
		{Name: ToolDriftFixPropose, Group: GroupChange, Level: config.AssistantPropose,
			Description: "Propose the fix of members' drift: those out of sync with their templates, or in conflict, in a sync rollout's plan (each member's diff and dry run, as the Drift page's Fix plans it); those whose own consistency check finds differences in a reload, checked as an operation. Nothing starts: the answer links where a person does.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"nodes":{"type":"array","items":{"type":"string"},"minItems":1}},"required":["summary","nodes"]}`),
			Run:         need(toolDriftFixPropose)},
	}
}

// clusterJSON returns a cluster's answer as a tool's result, redacted.
func (c *Call) clusterJSON(data []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	red, err := c.a.redactor(c.Settings).redactJSON(data, nil)
	if err != nil {
		return "", errors.New("the manager's answer is not JSON")
	}
	return string(red), nil
}

// proposed tells the caller of the link of what a cluster propose tool
// made, and returns its result.
func (c *Call) clusterPlan(data []byte, err error) (string, error) {
	out, err := c.clusterJSON(data, err)
	if err != nil {
		return "", err
	}
	var head struct {
		Link string `json:"link"`
	}
	_ = json.Unmarshal([]byte(out), &head)
	if head.Link != "" && c.linked != nil {
		c.linked(head.Link)
	}
	return out, nil
}

// stale refuses a propose call given up on past its timeout: it plans
// nothing.
func (c *Call) stale() error {
	if c.expired != nil && c.expired.Load() {
		return errors.New("the call was given up on: nothing was planned")
	}
	return nil
}

func toolRolloutPropose(c *Call, cl Cluster) (string, error) {
	var a struct {
		Summary     string          `json:"summary"`
		Kind        string          `json:"kind"`
		Template    string          `json:"template"`
		Group       string          `json:"group"`
		Nodes       []string        `json:"nodes"`
		From        string          `json:"from"`
		Paths       []string        `json:"paths"`
		Config      json.RawMessage `json:"config"`
		Description string          `json:"description"`
		Strategy    json.RawMessage `json:"strategy"`
	}
	if err := c.Decode(&a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Summary) == "" {
		return "", errors.New("summary says what the rollout does, for the person who starts it")
	}
	req := map[string]any{"kind": a.Kind}
	for k, v := range map[string]string{"template": a.Template, "group": a.Group, "from": a.From} {
		if v != "" {
			req[k] = v
		}
	}
	if len(a.Nodes) > 0 {
		req["nodes"] = a.Nodes
	}
	if len(a.Paths) > 0 {
		req["paths"] = a.Paths
	}
	desc := strings.TrimSpace(a.Description)
	if desc == "" {
		desc = strings.TrimSpace(a.Summary)
	}
	req["description"] = clip(desc, 500)
	if len(a.Config) > 0 && string(a.Config) != "null" {
		cfg, err := masks(a.Config)
		if err != nil {
			return "", err
		}
		req["config"] = json.RawMessage(cfg)
	}
	if len(a.Strategy) > 0 && string(a.Strategy) != "null" {
		req["strategy"] = a.Strategy
	}
	body, _ := json.Marshal(req) // strings and checked JSON
	if err := c.stale(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(c.Context, clusterPlanWait)
	defer cancel()
	return c.clusterPlan(cl.PlanRollout(ctx, c.User, body))
}

// masks checks a config the assistant proposes for members: it writes no
// secret, and each secret it copied as [redacted] goes as the manager's
// mask, which keeps the secret there (a template's, or each member's).
func masks(cfg json.RawMessage) ([]byte, error) {
	tree, err := decodeTree(cfg)
	if err != nil {
		return nil, errors.New("config is not JSON")
	}
	if _, ok := tree.(*jobject); !ok {
		return nil, errors.New("config is a JSON object of top-level members")
	}
	if err := guardDraft(nil, tree, false); err != nil {
		return nil, errors.New(strings.Replace(err.Error(), "copy it as page_get shows it ([redacted]) or leave it out, and the person sets it on the page",
			"copy it as it was read ([redacted]), which keeps each one's own, or leave it out; a person sets a new secret on the Templates page or the member's own", 1))
	}
	var mask func(tokens []string, v any, secret bool) any
	mask = func(tokens []string, v any, secret bool) any {
		switch t := v.(type) {
		case *jobject:
			for _, k := range t.keys {
				sub := append(tokens[:len(tokens):len(tokens)], k)
				t.vals[k] = mask(sub, t.vals[k], secret || secretPath(sub))
			}
		case []any:
			for i := range t {
				t[i] = mask(tokens, t[i], secret)
			}
		case string:
			if secret && isRedacted(t) {
				return "[secret]"
			}
		}
		return v
	}
	return encodeTree(mask(nil, tree, false)), nil
}

func toolClusterOpPropose(c *Call, cl Cluster) (string, error) {
	var a struct {
		Summary string          `json:"summary"`
		Op      string          `json:"op"`
		Group   string          `json:"group"`
		Members []string        `json:"members"`
		Purge   json.RawMessage `json:"purge"`
	}
	if err := c.Decode(&a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Summary) == "" {
		return "", errors.New("summary says what the operation does, for the person who starts it")
	}
	req := map[string]any{"op": a.Op}
	if a.Group != "" {
		req["group"] = a.Group
	}
	if len(a.Members) > 0 {
		req["members"] = a.Members
	}
	if len(a.Purge) > 0 && string(a.Purge) != "null" {
		req["purge"] = a.Purge
	}
	body, _ := json.Marshal(req)
	return c.clusterPlan(cl.CheckOp(c.User, body))
}

func toolDriftFixPropose(c *Call, cl Cluster) (string, error) {
	var a struct {
		Summary string   `json:"summary"`
		Nodes   []string `json:"nodes"`
	}
	if err := c.Decode(&a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Summary) == "" {
		return "", errors.New("summary says what the fix does, for the person who starts it")
	}
	if err := c.stale(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(c.Context, clusterPlanWait)
	defer cancel()
	return c.clusterPlan(cl.FixDrift(ctx, c.User, a.Nodes))
}

// ---- The member argument ----

// memberKey keys the member a read tool's call reads, in its context.
type memberKey struct{}

// memberTarget is the member a call reads, as whom, and what the manager
// noted of it.
type memberTarget struct {
	user, name string
	mu         sync.Mutex
	note       string
}

// withMember returns t taking a member argument: a call that names one
// reads the Admin API of that member, through the manager, instead of
// this fit's; what this fit alone knows (its uptime, its running config's
// settings) is then left out.
func withMember(t *Tool) *Tool {
	c := *t
	c.Description = t.Description + " On this manager, member reads it on that member instead, through the manager."
	c.Parameters = withMemberParam(t.Parameters)
	run := t.Run
	c.Run = func(call *Call) (string, error) {
		var a struct {
			Member string `json:"member"`
		}
		if json.Unmarshal(call.Args, &a) != nil || a.Member == "" {
			return run(call)
		}
		mt := &memberTarget{user: call.User, name: a.Member}
		mc := *call
		mc.Context = context.WithValue(call.Context, memberKey{}, mt)
		out, err := run(&mc)
		if err != nil {
			return "", err
		}
		mt.mu.Lock()
		note := mt.note
		mt.mu.Unlock()
		if json.Valid([]byte(out)) {
			w := map[string]any{"member": a.Member, "result": json.RawMessage(out)}
			if note != "" {
				w["note"] = note
			}
			return jsonText(w), nil
		}
		head := "member " + a.Member
		if note != "" {
			head += " (" + note + ")"
		}
		return head + ":\n" + out, nil
	}
	return &c
}

// withMemberParam adds member to a tool's parameters.
func withMemberParam(params json.RawMessage) json.RawMessage {
	var p map[string]any
	_ = json.Unmarshal(params, &p) // checked by the registry
	props, _ := p["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["member"] = map[string]any{"type": "string", "description": "a member of this manager's, by name: read on it instead of here"}
	p["properties"] = props
	b, _ := json.Marshal(p)
	return b
}

// onMember returns the member a call reads, "" for this fit.
func (c *Call) onMember() string {
	if t, _ := c.Context.Value(memberKey{}).(*memberTarget); t != nil {
		return t.name
	}
	return ""
}

// localConfig returns this fit's running config, or nil while the call
// reads a member, whose config a tool reads through the API.
func (c *Call) localConfig() *config.Config {
	if c.onMember() != "" {
		return nil
	}
	return c.a.o.Config()
}

// memberAPI calls the member t's Admin API through the manager, as a read
// tool's API call: a read only, bounded by memberReadWait.
func (a *Assistant) memberAPI(ctx context.Context, t *memberTarget, method, path string, body []byte) (*apiAnswer, error) {
	if a.o.Cluster == nil {
		return nil, errNoCluster
	}
	ctx, cancel := context.WithTimeout(ctx, memberReadWait)
	defer cancel()
	status, header, data, note, err := a.o.Cluster.MemberCall(ctx, t.user, t.name, method, path, body)
	if note != "" {
		t.mu.Lock()
		t.note = note
		t.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	if header == nil {
		header = http.Header{}
	}
	return &apiAnswer{status: status, header: header, body: bytes.Clone(data)}, nil
}

// clusterNote is what the system prompt says on a manager.
func clusterNote() string {
	return fmt.Sprintf("This fit is a manager of other fits (a cluster). The cluster tools read its members; the read tools that take member (%s) read that member instead. "+
		"A change across members is proposed with %s, %s or %s: each makes a rollout plan or a checked operation that a person starts on the Cluster pages, never applied here; give the person its link.",
		strings.Join(memberTools[:6], ", ")+", …", ToolRolloutPropose, ToolClusterOpPropose, ToolDriftFixPropose)
}
