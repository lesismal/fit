package assistant

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// The bounds of the read tools' own work, beside the result's bound.
const (
	// maxLogLines bounds log_tail's and access_log_sample's lines.
	maxLogLines = 500
	// accessLogTailBytes is how much of the access log's file
	// access_log_sample reads, from its end.
	accessLogTailBytes = 512 << 10
	// maxDiffEntries bounds versions_diff's changes.
	maxDiffEntries = 400
	// maxMetricLines bounds metrics_query's samples.
	maxMetricLines = 1000
	// maxToolTextBytes bounds the text a tool is handed to validate or convert.
	maxToolTextBytes = 1 << 20
	// maxAskOptions bounds ask_user's buttons.
	maxAskOptions = 8
)

// readTools returns the read tools of the plan's registry that this
// iteration builds, in the registry's order.
func readTools() []*Tool {
	return []*Tool{
		{Name: "config_get", Group: GroupConfig, Level: config.AssistantRead,
			Description: "Read fit's config: the base config (as written and kept) or the effective one (with what providers added), whole or only some top-level members (one it does not set is null), as JSON, fit's .conf or YAML. Secrets are redacted and the assistant's, the console's users and the Admin API's own settings are left out.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"source":{"type":"string","enum":["base","effective"],"description":"base (default) or effective"},"members":{"type":"array","items":{"type":"string"},"description":"top-level members to read, as routes or upstreams; all when left out"},"format":{"type":"string","enum":["json","conf","yaml"],"description":"json (default), conf or yaml"}}}`),
			Run:         toolConfigGet},
		{Name: "schema_describe", Group: GroupConfig, Level: config.AssistantRead,
			Description: "Describe a member of fit's config from its schema: what it is, its type, values, default and its members. path is dotted, as routes.handle or admin.console; empty for the top level.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"dotted path of the member, as listeners.address"},"lang":{"type":"string","enum":["en","zh"],"description":"the descriptions' language: en (default) or zh"}}}`),
			Run:         toolSchemaDescribe},
		{Name: "config_validate", Group: GroupConfig, Level: config.AssistantRead,
			Description: "Check a config as `fit validate` does, without applying it: either text (a whole config in conf, yaml or json), or patch, a JSON object of top-level members put over the running base config. Returns valid, or each error with its path, line and column.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"a whole config"},"format":{"type":"string","enum":["conf","yaml","json"],"description":"the text's format; json when left out"},"patch":{"type":"object","description":"top-level members that replace the running config's"}}}`),
			Run:         toolConfigValidate},
		{Name: "versions_diff", Group: GroupConfig, Level: config.AssistantRead,
			Description: "List the config versions fit keeps, or, with from (and to, the current one when left out), what changed between two of them, member by member.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"from":{"type":"integer","description":"the older version"},"to":{"type":"integer","description":"the newer version; the current one when left out"}}}`),
			Run:         toolVersionsDiff},
		{Name: "status_get", Group: GroupRuntime, Level: config.AssistantRead,
			Description: "What fit runs now: the config version and when it was applied, the listeners bound, connections, upstreams in short, the roles running, the engine's limits and the uptime.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run:         toolStatusGet},
		{Name: "metrics_query", Group: GroupRuntime, Level: config.AssistantRead,
			Description: "fit's metrics as they stand now (counters since start, gauges and histograms), filtered: by the metric's name, and the route, upstream or listener label. Histograms' buckets only when asked.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"text the metric's name holds, as http_requests or upstream"},"route":{"type":"string"},"upstream":{"type":"string"},"listener":{"type":"string"},"buckets":{"type":"boolean","description":"include histogram buckets"},"limit":{"type":"integer","description":"the most samples, 200 when left out"}}}`),
			Run:         toolMetricsQuery},
		{Name: "upstream_health", Group: GroupRuntime, Level: config.AssistantRead,
			Description: "The upstream pools and each backend's health: up or down, probed down, requests in flight, succeeded and failed; with the pool's health check settings.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"upstream":{"type":"string","description":"one pool's name; all when left out"}}}`),
			Run:         toolUpstreamHealth},
		{Name: "consistency_check", Group: GroupRuntime, Level: config.AssistantRead,
			Description: "Compare what fit runs with the config it runs from, as the Consistency page does: each difference with its category and the fix offered.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run:         toolConsistencyCheck},
		{Name: "certs_list", Group: GroupRuntime, Level: config.AssistantRead,
			Description: "The certificates served: listener, names, issuer, validity and days left; with the acme and auto_https settings.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run:         toolCertsList},
		{Name: "cache_zones", Group: GroupRuntime, Level: config.AssistantRead,
			Description: "The response cache's zones: their sizes, entries and hit rates.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run:         toolCacheZones},
		{Name: "log_tail", Group: GroupLogs, Level: config.AssistantRead,
			Description: "The latest lines of fit's own log, from a bounded ring in memory, oldest first: by logger (server, tls, provider, engine and the like), least level and text. Secrets are redacted.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"logger":{"type":"string"},"level":{"type":"string","enum":["debug","info","warn","error"]},"contains":{"type":"string"},"limit":{"type":"integer","description":"the most lines, 100 when left out, at most 500"}}}`),
			Enabled: func(s *config.Assistant) (bool, string) {
				return s.Data != nil && s.Data.SendLogs, "data.send_logs is off"
			},
			Run: toolLogTail},
		{Name: "access_log_sample", Group: GroupLogs, Level: config.AssistantRead,
			Description: "Recent lines of the access log's file matching a route, a status, a client address or text, newest last. Secrets are redacted.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"route":{"type":"string"},"status":{"type":"integer"},"client":{"type":"string"},"contains":{"type":"string"},"limit":{"type":"integer","description":"the most lines, 50 when left out, at most 500"}}}`),
			Enabled: func(s *config.Assistant) (bool, string) {
				return s.Data != nil && s.Data.SendAccessLog, "data.send_access_log is off"
			},
			Run: toolAccessLogSample},
		{Name: "adapt_convert", Group: GroupConvert, Level: config.AssistantRead,
			Description: "Convert nginx or Caddyfile text to fit's config as `fit adapt` does: the JSON, and what it could not convert.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"from":{"type":"string","enum":["nginx","caddyfile"]},"text":{"type":"string"}},"required":["from","text"]}`),
			Run:         toolAdaptConvert},
		{Name: "ask_user", Group: GroupAgent, Level: config.AssistantRead, Kind: KindAsk,
			Description: "Ask the user a question when a change lacks what it needs (an address, a name) or a choice is theirs, rather than guessing. The options are shown as buttons; the one chosen is the result.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"question":{"type":"string"},"options":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":8}},"required":["question","options"]}`)},
	}
}

// jsonText returns v as compact JSON.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// configJSON returns the running config as JSON, the base one or the
// effective one.
func (c *Call) configJSON(source string) ([]byte, error) {
	if source == "effective" {
		body, err := c.a.apiJSON(c.Context, "GET", "/config/effective", "", nil)
		if err != nil {
			return nil, err
		}
		var e struct {
			Config json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal(body, &e); err != nil || e.Config == nil {
			return nil, errors.New("the effective config is not JSON")
		}
		return e.Config, nil
	}
	return c.a.apiJSON(c.Context, "GET", "/config", "", nil)
}

// members returns the top-level members names lists of obj, a JSON object,
// in obj's order, all of them when names is empty; and the names obj does
// not set, absent.
func members(obj []byte, names []string) (_ []byte, absent []string, _ error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errNotJSON
	}
	var b bytes.Buffer
	b.WriteByte('{')
	found := map[string]bool{}
	for dec.More() {
		k, _ := dec.Token()
		key, _ := k.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, errNotJSON
		}
		if len(names) > 0 && !slices.Contains(names, key) {
			continue
		}
		found[key] = true
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(key)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	for _, n := range names {
		if !found[n] && !slices.Contains(absent, n) {
			absent = append(absent, n)
		}
	}
	return b.Bytes(), absent, nil
}

func toolConfigGet(c *Call) (string, error) {
	var args struct {
		Source  string   `json:"source"`
		Members []string `json:"members"`
		Format  string   `json:"format"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	switch args.Source {
	case "", "base", "effective":
	default:
		return "", fmt.Errorf("source is base or effective, not %q", args.Source)
	}
	data, err := c.configJSON(args.Source)
	if err != nil {
		return "", err
	}
	red, err := c.a.redactor(c.Settings).redactJSON(data, hiddenMembers)
	if err != nil {
		return "", errors.New("the config is not JSON")
	}
	red, absent, err := members(red, args.Members)
	if err != nil {
		return "", err
	}
	// A member asked for that the config does not set is null in JSON,
	// and said in a comment in the other formats: not an error, since the
	// next step is often to add it.
	switch args.Format {
	case "", "json":
		for _, n := range absent {
			kb, _ := json.Marshal(n)
			if len(red) > 2 {
				kb = append([]byte{','}, kb...)
			}
			red = append(append(red[:len(red)-1], kb...), []byte(":null}")...)
		}
		return string(red), nil
	case "conf", "yaml":
		text, err := c.a.apiJSON(c.Context, "POST", "/config/render?format="+args.Format, "application/json", red)
		if err != nil {
			return "", err
		}
		if len(absent) > 0 {
			text = append(text, []byte("\n# The config sets no "+strings.Join(absent, ", ")+".\n")...)
		}
		return string(text), nil
	}
	return "", fmt.Errorf("format is json, conf or yaml, not %q", args.Format)
}

// schemaNode is a member of the schema, resolved.
type schemaNode map[string]any

func toolSchemaDescribe(c *Call) (string, error) {
	var args struct {
		Path string `json:"path"`
		Lang string `json:"lang"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if c.a.o.Schema == nil {
		return "", errors.New("the schema is not loaded here")
	}
	var root schemaNode
	if err := json.Unmarshal(c.a.o.Schema, &root); err != nil {
		return "", errors.New("the schema is not JSON")
	}
	defs, _ := root["$defs"].(map[string]any)
	var resolve func(n schemaNode) schemaNode
	resolve = func(n schemaNode) schemaNode {
		for range 8 { // a $ref to a $ref, at most
			ref, _ := n["$ref"].(string)
			if ref == "" {
				break
			}
			d, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
			if d == nil {
				break
			}
			merged := schemaNode{}
			for k, v := range d {
				merged[k] = v
			}
			for k, v := range n {
				if k != "$ref" {
					merged[k] = v
				}
			}
			n = merged
		}
		if items, ok := n["items"].(map[string]any); ok && n["properties"] == nil {
			it := resolve(items)
			if it["properties"] != nil {
				n = schemaNode{"type": n["type"], "description": n["description"], "items_type": it["type"], "properties": it["properties"], "required": it["required"]}
			}
		}
		return n
	}
	n := resolve(root)
	if args.Path != "" {
		for _, part := range strings.Split(args.Path, ".") {
			props, _ := n["properties"].(map[string]any)
			next, _ := props[part].(map[string]any)
			if next == nil {
				return "", fmt.Errorf("the schema has no member %q at %q", part, args.Path)
			}
			n = resolve(next)
		}
	}
	zh := func(s string) string { return s }
	if args.Lang == "zh" && c.a.o.SchemaZh != nil {
		if m := c.a.o.SchemaZh(); m != nil {
			zh = func(s string) string {
				if t := m[s]; t != "" {
					return t
				}
				return s
			}
		}
	}
	out := map[string]any{"path": args.Path}
	for _, k := range []string{"type", "items_type", "enum", "default", "examples", "minimum", "maximum", "required", "deprecated"} {
		if v, ok := n[k]; ok {
			out[k] = v
		}
	}
	if d, ok := n["description"].(string); ok {
		out["description"] = zh(d)
	}
	if props, ok := n["properties"].(map[string]any); ok {
		names := make([]string, 0, len(props))
		for k := range props {
			names = append(names, k)
		}
		sort.Strings(names)
		list := make([]map[string]any, 0, len(names))
		for _, k := range names {
			p := resolve(props[k].(map[string]any))
			e := map[string]any{"name": k}
			if t, ok := p["type"]; ok {
				e["type"] = t
			}
			if d, ok := p["description"].(string); ok {
				e["description"] = clip(zh(d), 300)
			}
			list = append(list, e)
		}
		out["members"] = list
	}
	return jsonText(out), nil
}

// validationError is an error of a config checked, where it is.
type validationError struct {
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
	Msg  string `json:"msg"`
}

func toolConfigValidate(c *Call) (string, error) {
	var args struct {
		Text   string          `json:"text"`
		Format string          `json:"format"`
		Patch  json.RawMessage `json:"patch"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if (args.Text == "") == (len(args.Patch) == 0) {
		return "", errors.New("give either text or patch")
	}
	if len(args.Text) > maxToolTextBytes || len(args.Patch) > maxToolTextBytes {
		return "", fmt.Errorf("the config is larger than %d bytes", maxToolTextBytes)
	}
	var res *adapter.Result
	var err error
	if args.Text != "" {
		name := ""
		switch args.Format {
		case "conf":
			name = "fit"
		case "yaml":
			name = "yaml"
		case "", "json":
		default:
			return "", fmt.Errorf("format is conf, yaml or json, not %q", args.Format)
		}
		res, err = adapter.Adapt(name, []byte(args.Text))
	} else {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(args.Patch, &patch); err != nil {
			return "", errors.New("patch is not a JSON object")
		}
		base, err := c.configJSON("base")
		if err != nil {
			return "", err
		}
		var cur map[string]json.RawMessage
		if err := json.Unmarshal(base, &cur); err != nil {
			return "", errors.New("the running config is not a JSON object")
		}
		for k, v := range patch {
			if string(v) == "null" {
				delete(cur, k)
			} else {
				cur[k] = v
			}
		}
		data, _ := json.Marshal(cur)
		res = &adapter.Result{JSON: data}
	}
	if err == nil {
		err = res.MapErrors(c.a.validate(res.JSON))
	}
	out := struct {
		Valid    bool              `json:"valid"`
		Errors   []validationError `json:"errors,omitempty"`
		Warnings []string          `json:"warnings,omitempty"`
	}{Valid: err == nil}
	if res != nil {
		out.Warnings = res.Warnings
	}
	if err != nil {
		errs := config.AsErrors(err)
		if len(errs) == 0 {
			out.Errors = []validationError{{Msg: err.Error()}}
		}
		for i, e := range errs {
			if i == 50 {
				out.Errors = append(out.Errors, validationError{Msg: fmt.Sprintf("and %d more", len(errs)-50)})
				break
			}
			out.Errors = append(out.Errors, validationError{Path: e.Path, Line: e.Line, Col: e.Col, Msg: e.Msg})
		}
	}
	return jsonText(out), nil
}

// validate checks a config's JSON as fit validate does: loaded, and
// compiled when the assistant was given how.
func (a *Assistant) validate(data []byte) error {
	if a.o.Validate != nil {
		return a.o.Validate(data)
	}
	_, err := config.Load(data)
	return err
}

// change is one difference between two configs.
type change struct {
	Path string          `json:"path"`
	Op   string          `json:"op"` // added, removed, changed
	From json.RawMessage `json:"from,omitempty"`
	To   json.RawMessage `json:"to,omitempty"`
}

// diffJSON appends to out the changes from a to b at path, decoded JSON.
func diffJSON(path string, a, b any, out *[]change) {
	if len(*out) > maxDiffEntries {
		return
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if len(*out) > maxDiffEntries {
				return
			}
			sub := k
			if path != "" {
				sub = path + "." + k
			}
			av, ain := am[k]
			bv, bin := bm[k]
			switch {
			case !ain:
				*out = append(*out, change{Path: sub, Op: "added", To: raw(bv)})
			case !bin:
				*out = append(*out, change{Path: sub, Op: "removed", From: raw(av)})
			default:
				diffJSON(sub, av, bv, out)
			}
		}
		return
	}
	al, aok := a.([]any)
	bl, bok := b.([]any)
	if aok && bok && len(al) == len(bl) {
		for i := range al {
			diffJSON(path+"["+strconv.Itoa(i)+"]", al[i], bl[i], out)
		}
		return
	}
	if string(raw(a)) != string(raw(b)) {
		*out = append(*out, change{Path: path, Op: "changed", From: raw(a), To: raw(b)})
	}
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func toolVersionsDiff(c *Call) (string, error) {
	var args struct {
		From uint64 `json:"from"`
		To   uint64 `json:"to"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	list, err := c.a.apiJSON(c.Context, "GET", "/versions", "", nil)
	if err != nil {
		return "", err
	}
	if args.From == 0 {
		return string(list), nil
	}
	if args.To == 0 {
		var vs struct {
			Current uint64 `json:"current"`
		}
		_ = json.Unmarshal(list, &vs)
		args.To = vs.Current
	}
	red := c.a.redactor(c.Settings)
	get := func(n uint64) (any, error) {
		data, err := c.a.apiJSON(c.Context, "GET", "/versions/"+strconv.FormatUint(n, 10), "", nil)
		if err != nil {
			return nil, err
		}
		if data, err = red.redactJSON(data, hiddenMembers); err != nil {
			return nil, errors.New("the version is not JSON")
		}
		var v any
		_ = json.Unmarshal(data, &v)
		return v, nil
	}
	from, err := get(args.From)
	if err != nil {
		return "", err
	}
	to, err := get(args.To)
	if err != nil {
		return "", err
	}
	var changes []change
	diffJSON("", from, to, &changes)
	out := map[string]any{"from": args.From, "to": args.To, "changes": changes}
	if changes == nil {
		out["changes"] = []change{}
	}
	if len(changes) > maxDiffEntries {
		out["changes"], out["more"] = changes[:maxDiffEntries], true
	}
	return jsonText(out), nil
}

// status returns GET /status's answer, decoded.
func (c *Call) status() (map[string]any, error) {
	body, err := c.a.apiJSON(c.Context, "GET", "/status", "", nil)
	if err != nil {
		return nil, err
	}
	var st map[string]any
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, errors.New("the status is not JSON")
	}
	return st, nil
}

// roleMembers are the config's members that start roles.
var roleMembers = []string{"llm.hub", "llm.node", "llm.proxy", "tunnel.hub", "tunnel.node", "tunnel.proxy", "webrtc.turn", "webrtc.media", "webrtc.proxy", "streaming"}

func toolStatusGet(c *Call) (string, error) {
	st, err := c.status()
	if err != nil {
		return "", err
	}
	if ups, ok := st["upstreams"].([]any); ok {
		short := make([]map[string]any, 0, len(ups))
		for _, u := range ups {
			m, _ := u.(map[string]any)
			bs, _ := m["backends"].([]any)
			up := 0
			for _, b := range bs {
				if bm, _ := b.(map[string]any); bm["up"] == true {
					up++
				}
			}
			short = append(short, map[string]any{"name": m["name"], "backends": len(bs), "up": up, "in_flight": m["in_flight"]})
		}
		st["upstreams"] = short
	}
	delete(st, "certificates")
	cfg := c.a.o.Config()
	var roles []string
	if cfg != nil {
		var top map[string]map[string]json.RawMessage
		data, _ := json.Marshal(cfg)
		_ = json.Unmarshal(data, &top)
		for _, r := range roleMembers {
			group, name, _ := strings.Cut(r, ".")
			if name == "" && top[group] != nil || top[group][name] != nil {
				roles = append(roles, r)
			}
		}
		st["engine"] = map[string]any{"max_conns": cfg.Limits.Conns(), "max_pending_bytes": cfg.Limits.PendingBytes(), "max_write_queue_bytes": cfg.Limits.QueueBytes()}
	}
	st["roles"] = roles
	st["uptime_seconds"] = int64(time.Since(c.a.started) / time.Second)
	data, _ := json.Marshal(st)
	red, err := c.a.redactor(c.Settings).redactJSON(data, nil)
	return string(red), err
}

// metricLine is a sample of the Prometheus text format: its name and
// labels.
func metricLine(line string) (name string, labels map[string]string) {
	end := strings.IndexAny(line, "{ ")
	if end < 0 {
		return line, nil
	}
	name = line[:end]
	labels = map[string]string{}
	if line[end] == '{' {
		rest := line[end+1:]
		for rest != "" && rest[0] != '}' {
			eq := strings.IndexByte(rest, '=')
			if eq < 0 || eq+1 >= len(rest) || rest[eq+1] != '"' {
				break
			}
			k := strings.TrimLeft(rest[:eq], ",")
			v, n := unquoteLabel(rest[eq+1:])
			labels[k] = v
			rest = rest[eq+1+n:]
		}
	}
	return name, labels
}

// unquoteLabel reads a quoted label value at s's start, returning it and
// how many bytes it took, with the comma after it.
func unquoteLabel(s string) (string, int) {
	var b strings.Builder
	i := 1
	for ; i < len(s) && s[i] != '"'; i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	i++
	if i < len(s) && s[i] == ',' {
		i++
	}
	return b.String(), i
}

func toolMetricsQuery(c *Call) (string, error) {
	var args struct {
		Name     string `json:"name"`
		Route    string `json:"route"`
		Upstream string `json:"upstream"`
		Listener string `json:"listener"`
		Buckets  bool   `json:"buckets"`
		Limit    int    `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if args.Limit <= 0 {
		args.Limit = 200
	}
	args.Limit = min(args.Limit, maxMetricLines)
	text, err := c.a.apiJSON(c.Context, "GET", "/metrics", "", nil)
	if err != nil {
		return "", err
	}
	var out []string
	more := 0
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels := metricLine(line)
		switch {
		case args.Name != "" && !strings.Contains(name, args.Name),
			!args.Buckets && strings.HasSuffix(name, "_bucket"),
			args.Route != "" && labels["route"] != args.Route,
			args.Upstream != "" && labels["upstream"] != args.Upstream,
			args.Listener != "" && labels["listener"] != args.Listener:
			continue
		}
		if len(out) >= args.Limit {
			more++
			continue
		}
		out = append(out, line)
	}
	res := strings.Join(out, "\n")
	if len(out) == 0 {
		res = "no metric matches"
	}
	if more > 0 {
		res += fmt.Sprintf("\n(%d more samples match; narrow the query)", more)
	}
	return res, nil
}

func toolUpstreamHealth(c *Call) (string, error) {
	var args struct {
		Upstream string `json:"upstream"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	st, err := c.status()
	if err != nil {
		return "", err
	}
	ups, _ := st["upstreams"].([]any)
	settings := map[string]any{}
	if cfg := c.a.o.Config(); cfg != nil {
		for _, u := range cfg.Upstreams {
			settings[u.Name] = u
		}
	}
	var out []map[string]any
	for _, u := range ups {
		m, _ := u.(map[string]any)
		name, _ := m["name"].(string)
		if args.Upstream != "" && name != args.Upstream {
			continue
		}
		if s := settings[name]; s != nil {
			m["settings"] = s
		}
		out = append(out, m)
	}
	if args.Upstream != "" && len(out) == 0 {
		return "", fmt.Errorf("no upstream is named %q", args.Upstream)
	}
	data, _ := json.Marshal(map[string]any{"upstreams": out})
	red, err := c.a.redactor(c.Settings).redactJSON(data, nil)
	return string(red), err
}

func toolConsistencyCheck(c *Call) (string, error) {
	body, err := c.a.apiJSON(c.Context, "GET", "/config/check", "", nil)
	if err != nil {
		return "", err
	}
	red, err := c.a.redactor(c.Settings).redactJSON(body, hiddenMembers)
	return string(red), err
}

func toolCertsList(c *Call) (string, error) {
	st, err := c.status()
	if err != nil {
		return "", err
	}
	certs, _ := st["certificates"].([]any)
	for _, x := range certs {
		m, _ := x.(map[string]any)
		if s, _ := m["not_after"].(string); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				m["days_left"] = int(time.Until(t).Hours() / 24)
			}
		}
	}
	out := map[string]any{"certificates": certs}
	if cfg := c.a.o.Config(); cfg != nil {
		out["acme"], out["auto_https"] = cfg.ACME, cfg.AutoHTTPS
	}
	data, _ := json.Marshal(out)
	red, err := c.a.redactor(c.Settings).redactJSON(data, nil)
	return string(red), err
}

func toolCacheZones(c *Call) (string, error) {
	body, err := c.a.apiJSON(c.Context, "GET", "/cache", "", nil)
	if err != nil {
		return "", err
	}
	red, err := c.a.redactor(c.Settings).redactJSON(body, nil)
	return string(red), err
}

func toolLogTail(c *Call) (string, error) {
	var args struct {
		Logger   string `json:"logger"`
		Level    string `json:"level"`
		Contains string `json:"contains"`
		Limit    int    `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if args.Level != "" && !slices.Contains(config.Levels, args.Level) {
		return "", fmt.Errorf("level is debug, info, warn or error, not %q", args.Level)
	}
	if c.a.o.Logs == nil {
		return "", errors.New("fit's log keeps no lines in memory here")
	}
	recs := c.a.o.Logs(logs.Filter{Logger: args.Logger, Level: args.Level, Contains: args.Contains, Limit: min(max(args.Limit, 0), maxLogLines)})
	if len(recs) == 0 {
		return "no line matches", nil
	}
	var b strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&b, "%s %s %s: %s\n", r.Time.UTC().Format(time.RFC3339), r.Level, r.Logger, r.Message)
	}
	return b.String(), nil
}

func toolAccessLogSample(c *Call) (string, error) {
	var args struct {
		Route    string `json:"route"`
		Status   int    `json:"status"`
		Client   string `json:"client"`
		Contains string `json:"contains"`
		Limit    int    `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	args.Limit = min(args.Limit, maxLogLines)
	cfg := c.a.o.Config()
	if cfg == nil || cfg.AccessLog == nil || !config.IsFileOutput(cfg.AccessLog.Output) {
		return "", errors.New("the access log is not written to a file here, so there are no lines to read")
	}
	tail, err := readTail(cfg.AccessLog.Output, accessLogTailBytes)
	if err != nil {
		return "", fmt.Errorf("reading the access log: %v", err)
	}
	status := ""
	if args.Status != 0 {
		status = strconv.Itoa(args.Status)
	}
	var lines []string
	for _, line := range strings.Split(string(tail), "\n") {
		switch {
		case strings.TrimSpace(line) == "",
			args.Route != "" && !strings.Contains(line, args.Route),
			status != "" && !containsWord(line, status),
			args.Client != "" && !strings.Contains(line, args.Client),
			args.Contains != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(args.Contains)):
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > args.Limit {
		lines = lines[len(lines)-args.Limit:]
	}
	if len(lines) == 0 {
		return "no line matches", nil
	}
	return strings.Join(lines, "\n"), nil
}

// containsWord reports whether s holds w apart from digits around it, as a
// status in a line.
func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || s[j-1] < '0' || s[j-1] > '9'
		after := j+len(w) == len(s) || s[j+len(w)] < '0' || s[j+len(w)] > '9'
		if before && after {
			return true
		}
		i = j + 1
	}
}

// readTail returns the last n bytes of the file at path, from the start of
// a line.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(fi.Size()-n, 0)
	data, err := io.ReadAll(io.NewSectionReader(f, off, fi.Size()-off))
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data, nil
}

func toolAdaptConvert(c *Call) (string, error) {
	var args struct {
		From string `json:"from"`
		Text string `json:"text"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if args.From != "nginx" && args.From != "caddyfile" {
		return "", fmt.Errorf("from is nginx or caddyfile, not %q", args.From)
	}
	if args.Text == "" || len(args.Text) > maxToolTextBytes {
		return "", fmt.Errorf("text is 1 to %d bytes", maxToolTextBytes)
	}
	res, err := adapter.Adapt(args.From, []byte(args.Text))
	if err != nil {
		return "", err
	}
	out := struct {
		Config   json.RawMessage `json:"config"`
		Warnings []string        `json:"warnings"`
	}{Config: res.JSON, Warnings: res.Warnings}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	data, _ := json.Marshal(out)
	red, err := c.a.redactor(c.Settings).redactJSON(data, nil)
	return string(red), err
}
