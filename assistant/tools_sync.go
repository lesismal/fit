package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/lesismal/fit/config"
)

// The sync tools (I190) read the sync role's pipelines (I188, I189)
// through the Admin API in process, as the workbench's Sync pages do: each
// pipeline's state, counters, lag and errors, its config, its connection
// tests, a dry run of its first records and its dead letters. None changes
// a pipeline: a pause, a resume, a snapshot, a checkpoint's change or a
// replay is the person's, on the pages. The records a dry run reads and a
// dead letter holds are rows of the person's data: unless
// data.send_records allows them, the model is shown each value as its kind
// only, so that it sees namespaces, operations, fields and errors, never
// what the fields hold.

// The bounds of the sync tools' own work.
const (
	// defaultSyncLetters and maxSyncLetters bound sync_dead_letters' page.
	defaultSyncLetters = 20
	maxSyncLetters     = 100
	// defaultSyncDryRun and maxSyncDryRun bound sync_dry_run's records.
	defaultSyncDryRun = 5
	maxSyncDryRun     = 20
	// syncDryRunWait is how long a dry run waits for its records.
	syncDryRunWait = "5s"
	// valuesHidden says why a record's values are shown as kinds.
	valuesHidden = "each value is shown as its kind only: data.send_records is off"
)

// syncTools returns the sync tools.
func syncTools() []*Tool {
	return []*Tool{
		{Name: "sync_pipelines", Group: GroupSync, Level: config.AssistantRead,
			Description: "The sync role's pipelines (I188): without pipeline, each one's state (running, paused, retrying, finished, stopped, scheduled, killed), its last error, its counts of records read, acknowledged and failed, throughput, lag (seconds from commit to acknowledgement, and the source's behind its server) and dead letters kept; with pipeline, its whole status (checkpoint, queue against its bounds, each sink's records, batches, retries and errors, a job's progress) and its config as it runs.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pipeline":{"type":"string","description":"one pipeline by name; all in short when left out"}}}`),
			Run:         toolSyncPipelines},
		{Name: "sync_test", Group: GroupSync, Level: config.AssistantRead,
			Description: "Tests the connections of a running pipeline's source, sinks and dead_letter sink, as Test connection on the Sync pages does: each connector's result, tested false for one that has no test. Writes no record.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pipeline":{"type":"string"}},"required":["pipeline"]}`),
			Run:         toolSyncTest},
		{Name: "sync_dry_run", Group: GroupSync, Level: config.AssistantRead,
			Description: "Reads a running pipeline's first records through its transforms, as Dry run on the Sync pages does, writing nothing and saving no position: each source record and what the transforms made of it, or the transform's error, and whether the source ended, timed out or failed. from is checkpoint (the default) or start. Values show as their kinds unless data.send_records is set.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pipeline":{"type":"string"},"limit":{"type":"integer","description":"the most records, 5 when left out, at most 20"},"from":{"type":"string","enum":["checkpoint","start"]}},"required":["pipeline"]}`),
			Run:         toolSyncDryRun},
		{Name: "sync_dead_letters", Group: GroupSync, Level: config.AssistantRead,
			Description: "A pipeline's dead letters, oldest first, when its dead_letter sink is dead_letters (I189): each one's ID, when it was saved, its error, where it failed (sink <name> or transform <n>), its attempts and its record. Values show as their kinds unless data.send_records is set. Replaying or dropping them is the person's, on the Sync pages.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pipeline":{"type":"string"},"after":{"type":"integer","description":"only letters after this ID"},"limit":{"type":"integer","description":"the most letters, 20 when left out, at most 100"}},"required":["pipeline"]}`),
			Run:         toolSyncDeadLetters},
	}
}

// recordsSent reports whether s lets records' values reach the model.
func recordsSent(s *config.Assistant) bool { return s != nil && s.Data != nil && s.Data.SendRecords }

// pipelinePath returns the Admin API's path of pipeline.
func pipelinePath(pipeline string) (string, error) {
	if pipeline == "" {
		return "", errors.New("pipeline is a pipeline's name, as sync_pipelines lists them")
	}
	return "/sync/pipelines/" + url.PathEscape(pipeline), nil
}

func toolSyncPipelines(c *Call) (string, error) {
	var args struct {
		Pipeline string `json:"pipeline"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if args.Pipeline == "" {
		var all struct {
			Pipelines []map[string]json.RawMessage `json:"pipelines"`
		}
		if err := c.a.apiGet(c.Context, "/sync/pipelines", &all); err != nil {
			return "", err
		}
		// The short form: what tells a pipeline's health at a glance.
		short := make([]map[string]json.RawMessage, 0, len(all.Pipelines))
		for _, p := range all.Pipelines {
			s := map[string]json.RawMessage{}
			for _, k := range []string{"name", "state", "paused", "error", "error_at", "errors", "read", "acknowledged", "failed",
				"throughput", "lag_seconds", "source_lag", "next"} {
				if v, ok := p[k]; ok {
					s[k] = v
				}
			}
			var dl struct {
				Store *struct {
					Kept int `json:"kept"`
				} `json:"store"`
			}
			if json.Unmarshal(p["dead_letters"], &dl) == nil && dl.Store != nil {
				s["dead_letters_kept"] = json.RawMessage(strconv.Itoa(dl.Store.Kept))
			}
			short = append(short, s)
		}
		return jsonText(map[string]any{"pipelines": short}), nil
	}
	path, _ := pipelinePath(args.Pipeline) // not "": that is the list
	var st json.RawMessage
	if err := c.a.apiGet(c.Context, path, &st); err != nil {
		return "", err
	}
	out := map[string]any{"status": st}
	if cfg := c.localConfig(); cfg != nil && cfg.Sync != nil {
		for i := range cfg.Sync.Pipelines {
			if p := &cfg.Sync.Pipelines[i]; p.Name == args.Pipeline {
				out["config"] = p
			}
		}
	}
	return jsonText(out), nil
}

func toolSyncTest(c *Call) (string, error) {
	var args struct {
		Pipeline string `json:"pipeline"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	path, err := pipelinePath(args.Pipeline)
	if err != nil {
		return "", err
	}
	body, err := c.a.apiJSON(c.Context, "POST", path+"/test", "", nil)
	if err != nil {
		return "", err
	}
	return compactText(body), nil
}

func toolSyncDryRun(c *Call) (string, error) {
	var args struct {
		Pipeline string `json:"pipeline"`
		Limit    int    `json:"limit"`
		From     string `json:"from"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	path, err := pipelinePath(args.Pipeline)
	if err != nil {
		return "", err
	}
	_, limit, err := pageArgs(0, args.Limit, defaultSyncDryRun, maxSyncDryRun)
	if err != nil {
		return "", err
	}
	if args.From != "" && args.From != "checkpoint" && args.From != "start" {
		return "", fmt.Errorf("from is checkpoint or start, not %q", args.From)
	}
	req, _ := json.Marshal(map[string]any{"limit": limit, "timeout": syncDryRunWait, "from": args.From})
	body, err := c.a.apiJSON(c.Context, "POST", path+"/dry-run", "application/json", req)
	if err != nil {
		return "", err
	}
	if recordsSent(c.Settings) {
		return compactText(body), nil
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(body, &d); err != nil {
		return "", fmt.Errorf("the dry run's answer is not what it should be: %v", err)
	}
	var recs []struct {
		In  json.RawMessage   `json:"in"`
		Out []json.RawMessage `json:"out"`
		Err string            `json:"error,omitempty"`
	}
	_ = json.Unmarshal(d["records"], &recs)
	for i := range recs {
		recs[i].In = hideValues(recs[i].In)
		for k := range recs[i].Out {
			recs[i].Out[k] = hideValues(recs[i].Out[k])
		}
	}
	d["records"], _ = json.Marshal(recs)
	d["values"], _ = json.Marshal(valuesHidden)
	return jsonText(d), nil
}

func toolSyncDeadLetters(c *Call) (string, error) {
	var args struct {
		Pipeline string `json:"pipeline"`
		After    uint64 `json:"after"`
		Limit    int    `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	path, err := pipelinePath(args.Pipeline)
	if err != nil {
		return "", err
	}
	_, limit, err := pageArgs(0, args.Limit, defaultSyncLetters, maxSyncLetters)
	if err != nil {
		return "", err
	}
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if args.After > 0 {
		q.Set("after", strconv.FormatUint(args.After, 10))
	}
	body, err := c.a.apiJSON(c.Context, "GET", path+"/dead-letters?"+q.Encode(), "", nil)
	if err != nil {
		return "", err
	}
	if recordsSent(c.Settings) {
		return compactText(body), nil
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(body, &d); err != nil {
		return "", fmt.Errorf("the dead letters' answer is not what it should be: %v", err)
	}
	var letters []map[string]json.RawMessage
	_ = json.Unmarshal(d["letters"], &letters)
	for _, l := range letters {
		if r, ok := l["record"]; ok {
			l["record"] = hideValues(r)
		}
	}
	d["letters"], _ = json.Marshal(letters)
	d["values"], _ = json.Marshal(valuesHidden)
	return jsonText(d), nil
}

// compactText returns body compacted, or as it is when it is not JSON.
func compactText(body []byte) string {
	var b bytes.Buffer
	if json.Compact(&b, body) != nil {
		return string(body)
	}
	return b.String()
}

// recordParts are the members of a record in its canonical JSON that hold
// values; the others (source, namespace, op, commit_time, tx, position)
// say where it comes from, and stay.
var recordParts = map[string]bool{"key": true, "before": true, "after": true}

// hideValues returns rec, a record in its canonical JSON, with each value
// of its key, before and after replaced by its kind: "(string)",
// "(int)", a map's members by name. A document that is not a record's is
// hidden whole, by the same rule.
func hideValues(rec json.RawMessage) json.RawMessage {
	d := json.NewDecoder(bytes.NewReader(rec))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil {
		return json.RawMessage(`"(not JSON)"`)
	}
	m, ok := v.(map[string]any)
	isRecord := ok
	for k := range m {
		switch k {
		case "source", "namespace", "op", "commit_time", "tx", "position", "key", "before", "after":
		default:
			isRecord = false
		}
	}
	if !isRecord {
		v = kindOf(v)
	} else {
		for k, x := range m {
			if recordParts[k] {
				m[k] = kindOf(x)
			}
		}
	}
	b, _ := json.Marshal(v)
	return b
}

// canonicalKinds are the kinds a value's canonical JSON names in an object
// of one member, sync/json.go's.
var canonicalKinds = map[string]bool{"int": true, "uint": true, "float": true, "decimal": true, "bytes": true, "time": true,
	"date": true, "json": true}

// kindOf returns v, a value in its canonical JSON (or plain JSON), with
// every leaf replaced by its kind.
func kindOf(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool:
		return "(bool)"
	case json.Number:
		return "(number)"
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = kindOf(x[i])
		}
		return out
	case map[string]any:
		if len(x) == 1 {
			for k, inner := range x {
				if canonicalKinds[k] {
					return "(" + k + ")"
				}
				if mm, ok := inner.(map[string]any); ok && k == "map" {
					return map[string]any{"map": kindMembers(mm)}
				}
			}
		}
		return kindMembers(x)
	default:
		return "(string)"
	}
}

// kindMembers returns the members of m, each value as its kind.
func kindMembers(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, inner := range m {
		out[k] = kindOf(inner)
	}
	return out
}
