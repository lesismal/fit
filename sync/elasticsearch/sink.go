package syncelastic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".elasticsearch", New: func() any { return new(Sink) },
		Doc: "Writes records to Elasticsearch (7 and 8) or OpenSearch (1 and 2), which it tells apart by the root endpoint, with the _bulk API: inserts and updates as index (or create) of the row as a JSON document, deletes as delete, each by an _id made from the record's key, into the index the namespace or a template with the commit time's date names. Items the cluster refuses for being busy (429) or failing (5xx) are sent again alone with a backoff, a 429 for as long as it lasts, so a busy cluster slows the pipeline rather than losing records; an item refused for good (a mapping error) fails its record, which on_error takes, those before it written."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 8 << 20
	// DefaultItemAttempts is how many times an item failing with a 5xx is
	// sent in one write before the write fails at it.
	DefaultItemAttempts = 5
	// MaxIDBytes is the longest _id the cluster takes.
	MaxIDBytes = 512
)

// The backoff between sending items again: doubling from the first to the
// most.
var (
	backoffFirst = 200 * time.Millisecond
	backoffMost  = 30 * time.Second
)

// Sink is the sink elasticsearch.
type Sink struct {
	URLs            []string        `json:"urls" fit:"required" doc:"The cluster's HTTP endpoints, as https://es1:9200, each a node or a load balancer: the one that answered last is used, and the next in turn when it fails to (a connection's error, a timeout, a 502, 503 or 504). fib's client does not follow redirects."`
	Username        string          `json:"username,omitempty" doc:"The user for basic authentication, with password_env."`
	PasswordEnv     string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	APIKeyEnv       string          `json:"api_key_env,omitempty" doc:"The environment variable holding an Elasticsearch API key as its create API key call returns it encoded (base64 of id:key), sent as Authorization: ApiKey, read when the pipeline loads. Not with username. OpenSearch has no API keys: it answers one with 401."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the cluster's is checked against, in place of the system's roots, for a CA of its own (Elasticsearch's http_ca.crt, OpenSearch's root-ca.pem). The check cannot be turned off."`
	CertFile        string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a cluster that asks for one; needs key_file."`
	KeyFile         string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the cluster's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Index           string          `json:"index,omitempty" default:"\"{namespace}\"" doc:"The index a record goes to: text with placeholders, {namespace} the record's namespace and any other a date of the record's commit time (the time it is written when the source gives none) in time_zone, written with yyyy, yy, MM, dd and HH and the separators '.', '-' and '_': logs-{yyyy.MM.dd}; {{ is a brace. Index names are lowercase; one the cluster refuses fails its record. {namespace} when left out."`
	TimeZone        string          `json:"time_zone,omitempty" doc:"The zone index's dates are in: UTC when left out, local, or an IANA name such as Asia/Shanghai."`
	Action          string          `json:"action,omitempty" enum:"index,create" default:"\"index\"" doc:"How inserts and updates are written: index replaces the document of the _id, so a replay converges; create writes it only when there is none, as a data stream needs, and a document already there (409) counts as written, so an update to it is lost. index when left out."`
	Routing         string          `json:"routing,omitempty" doc:"A field of the row (after, or before, or the key's for a delete) whose value routes the document to its shard; a record without it fails. None when left out."`
	Pipeline        string          `json:"pipeline,omitempty" doc:"The ingest pipeline inserts and updates go through. None when left out, or the index's default."`
	MaxRequestBytes int64           `json:"max_request_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes of one _bulk request; a batch larger is sent in several, in order, and a document larger fails its record. 8 MiB when left out or 0."`

	conn  *Conn
	index []indexPart
	zone  *time.Location
	query string
}

// indexPart is a piece of the index's template: text, the namespace, or a
// date layout of Go's.
type indexPart struct {
	text, layout string
	namespace    bool
}

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	conn, errs := NewConn(ctx, ConnParams{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv,
		APIKeyEnv: s.APIKeyEnv, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn = conn
	tmpl := s.Index
	if tmpl == "" {
		tmpl = "{namespace}"
	}
	var err error
	if s.index, err = parseIndex(tmpl); err != nil {
		errs = append(errs, ctx.Params.Field("index").Errorf("%v", err))
	}
	s.zone = time.UTC
	if s.TimeZone != "" {
		if s.zone, err = config.LoadTimeZone(s.TimeZone); err != nil {
			errs = append(errs, ctx.Params.Field("time_zone").Errorf("%v", err))
		}
	}
	q := url.Values{"filter_path": {"errors,items.*.status,items.*.error.type,items.*.error.reason"}}
	if s.Pipeline != "" {
		q.Set("pipeline", s.Pipeline)
	}
	s.query = q.Encode()
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: a key goes to one writer, and the cluster takes bulks
// at once.
func (s *Sink) ParallelWrites() {}

// Test asks the cluster's root endpoint, with the sink's credentials. It
// writes nothing, so whether they may write to the indices is not known
// before the first bulk.
func (s *Sink) Test(ctx context.Context) error { return s.conn.Test(ctx) }

// Cleanup closes the client.
func (s *Sink) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
}

var dateTokens = []struct{ java, golang string }{{"yyyy", "2006"}, {"yy", "06"}, {"MM", "01"}, {"dd", "02"}, {"HH", "15"}}

func parseIndex(t string) ([]indexPart, error) {
	var parts []indexPart
	var text strings.Builder
	for i := 0; i < len(t); i++ {
		switch {
		case strings.HasPrefix(t[i:], "{{"):
			text.WriteByte('{')
			i++
		case t[i] == '{':
			j := strings.IndexByte(t[i:], '}')
			if j < 0 {
				return nil, fmt.Errorf("a { at %d is not closed", i)
			}
			name := t[i+1 : i+j]
			p := indexPart{namespace: name == "namespace"}
			if !p.namespace {
				layout, err := dateLayout(name)
				if err != nil {
					return nil, err
				}
				p.layout = layout
			}
			parts = append(parts, indexPart{text: text.String()}, p)
			text.Reset()
			i += j
		default:
			text.WriteByte(t[i])
		}
	}
	return append(parts, indexPart{text: text.String()}), nil
}

// dateLayout turns a date written with yyyy, yy, MM, dd and HH into Go's.
func dateLayout(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '.' || c == '-' || c == '_':
			b.WriteByte(c)
			i++
		default:
			found := false
			for _, tok := range dateTokens {
				if strings.HasPrefix(s[i:], tok.java) {
					b.WriteString(tok.golang)
					i += len(tok.java)
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("{%s} is neither {namespace} nor a date of yyyy, yy, MM, dd, HH, '.', '-' and '_'", s)
			}
		}
	}
	if s == "" {
		return "", errors.New("{} names nothing")
	}
	return b.String(), nil
}

// indexOf returns the index r goes to.
func (s *Sink) indexOf(r *fitsync.Record, now time.Time) string {
	var b strings.Builder
	at := r.CommitTime
	if at.IsZero() {
		at = now
	}
	at = at.In(s.zone)
	for _, p := range s.index {
		switch {
		case p.namespace:
			b.WriteString(r.Namespace)
		case p.layout != "":
			b.WriteString(at.Format(p.layout))
		default:
			b.WriteString(p.text)
		}
	}
	return b.String()
}

// DocID returns the _id of a record's key: a single value's text, or a
// map's values in order, each with '\' and ':' escaped by a '\', joined by
// ':'. Strings, decimals and JSON are their text, numbers in decimal,
// times in RFC 3339, dates as 2006-01-02, bytes in base64, arrays and maps
// as plain JSON. A null key has none.
func DocID(key fitsync.Value) (string, bool) {
	switch key.Kind() {
	case fitsync.KindNull:
		return "", false
	case fitsync.KindMap:
		fs := key.Fields()
		if len(fs) == 1 {
			return idText(fs[0].Value), true
		}
		var b strings.Builder
		for i, f := range fs {
			if i > 0 {
				b.WriteByte(':')
			}
			t := idText(f.Value)
			if strings.ContainsAny(t, `\:`) {
				t = strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(t)
			}
			b.WriteString(t)
		}
		return b.String(), true
	}
	return idText(key), true
}

func idText(v fitsync.Value) string {
	switch v.Kind() {
	case fitsync.KindBytes:
		return base64.StdEncoding.EncodeToString([]byte(v.Text()))
	case fitsync.KindArray, fitsync.KindMap:
		b, err := fitsync.AppendPlain(nil, v)
		if err != nil {
			return fitsync.TextOf(v)
		}
		return string(b)
	}
	return fitsync.TextOf(v)
}

// item is a record as a _bulk item.
type item struct {
	// lines are its action line and document, each ending in a newline;
	// nil for a record the sink does not write (DDL).
	lines []byte
	// doc identifies the document, so that an item sent again takes the
	// later items of the same document with it; empty when the cluster
	// makes the _id.
	doc    string
	delete bool
	// err is why the record cannot be written, or failed, nil once written.
	err error
	// done says the record is written.
	done bool
}

// build makes r's item.
func (s *Sink) build(r *fitsync.Record, now time.Time) item {
	switch r.Op {
	case fitsync.OpDDL:
		return item{done: true}
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpDelete, fitsync.OpMessage:
	default:
		return item{err: fitsync.Permanent(fmt.Errorf("a record of operation %s", r.Op))}
	}
	id, hasID := DocID(r.Key)
	if !hasID && (r.Op == fitsync.OpUpdate || r.Op == fitsync.OpDelete) {
		return item{err: fitsync.Permanent(fmt.Errorf("a %s has no key to make the _id of", r.Op))}
	}
	if len(id) > MaxIDBytes {
		return item{err: fitsync.Permanent(fmt.Errorf("the _id is %d bytes, more than %d", len(id), MaxIDBytes))}
	}
	index := s.indexOf(r, now)
	row := r.After
	if row.IsNull() {
		row = r.Before
	}
	var routing string
	if s.Routing != "" {
		v, ok := row.Get(s.Routing)
		if !ok || v.IsNull() {
			v, ok = r.Key.Get(s.Routing)
		}
		if !ok || v.IsNull() {
			return item{err: fitsync.Permanent(fmt.Errorf("the record has no %s to route by", s.Routing))}
		}
		routing = idText(v)
	}
	action := s.Action
	if action == "" {
		action = "index"
	}
	it := item{delete: r.Op == fitsync.OpDelete}
	if it.delete {
		action = "delete"
	}
	meta := map[string]string{"_index": index}
	if hasID {
		meta["_id"] = id
		it.doc = index + "\x00" + routing + "\x00" + id
	}
	if routing != "" {
		meta["routing"] = routing
	}
	head, _ := json.Marshal(map[string]any{action: meta})
	it.lines = append(head, '\n')
	if it.delete {
		return it
	}
	if row.Kind() != fitsync.KindMap {
		return item{err: fitsync.Permanent(fmt.Errorf("a %s's row is a %s, not a map to make a document of", r.Op, row.Kind()))}
	}
	var err error
	if it.lines, err = fitsync.AppendPlain(it.lines, row); err != nil {
		return item{err: fitsync.Permanent(err)}
	}
	it.lines = append(it.lines, '\n')
	if int64(len(it.lines)) > s.maxRequest() {
		return item{err: fitsync.Permanent(fmt.Errorf("the document is %d bytes, more than max_request_bytes", len(it.lines)))}
	}
	return it
}

func (s *Sink) maxRequest() int64 {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return s.MaxRequestBytes
}

// errBusy is a request or an item the cluster was too busy for (429).
var errBusy = errors.New("the cluster is busy (429)")

// Write sends the batch's records in _bulk requests, in order. An item the
// cluster refuses for being busy (429) or failing (5xx) is sent again with
// a backoff, taking with it the later items of the same document so that
// they land after it: a 429 for as long as it lasts, a 5xx DefaultItemAttempts
// times. It returns nil once every record is written, or a BatchError at the
// first one that is not: those before it are written, and those after it
// that were are written again by the runtime, which sends the batch again
// from that record, in order, so no later write of a document is undone.
// A record refused for good (a mapping error, a bad index name) is a
// Permanent error, which on_error takes at once.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	if _, err := s.conn.Info(ctx); err != nil {
		return err
	}
	now := time.Now()
	items := make([]item, len(batch))
	var pending []int
	for i, r := range batch {
		items[i] = s.build(r, now)
		if items[i].err == nil && !items[i].done {
			pending = append(pending, i)
		}
	}
	for round := 0; len(pending) > 0; round++ {
		retry, busy, err := s.send(ctx, items, pending)
		if err != nil {
			return failAt(items, pending, err)
		}
		if len(retry) == 0 || !busy && round+1 >= DefaultItemAttempts || refusedFirst(items, retry) {
			break
		}
		if !sleep(ctx, backoff(round)) {
			return ctx.Err()
		}
		pending = withLater(items, retry, pending)
	}
	for i := range items {
		if !items[i].done {
			return &fitsync.BatchError{Index: i, Err: items[i].err}
		}
	}
	return nil
}

// refusedFirst reports whether a record refused for good comes before every
// item to send again: the write fails there, and the runtime sends the
// rest again after on_error has taken it.
func refusedFirst(items []item, retry []int) bool {
	for i := range items {
		if !items[i].done {
			return i < retry[0]
		}
	}
	return false
}

// failAt returns err as the batch's failure at its first record not
// written: those before it were.
func failAt(items []item, pending []int, err error) error {
	for i := range items {
		if !items[i].done {
			if items[i].err != nil && !contains(pending, i) {
				err = items[i].err
			}
			return &fitsync.BatchError{Index: i, Err: err}
		}
	}
	return err
}

func contains(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// withLater returns the items to send again, in order: those that failed
// and every later item of the same documents among those sent, so that a
// document's last write is still its last.
func withLater(items []item, retry, sent []int) []int {
	again := map[string]int{} // document -> the first index sent again
	next := map[int]bool{}
	for _, i := range retry {
		next[i] = true
		if d := items[i].doc; d != "" {
			if j, ok := again[d]; !ok || i < j {
				again[d] = i
			}
		}
	}
	var out []int
	for _, i := range sent {
		d := items[i].doc
		if j, ok := again[d]; next[i] || d != "" && ok && i > j {
			items[i].done = false
			out = append(out, i)
		}
	}
	return out
}

// send sends the pending items in _bulk requests of at most
// max_request_bytes, in order, marking each done or failed. It returns the
// items to send again (429 or 5xx) and whether any was a 429, or an error
// when a request failed whole, with the items before its first sent.
func (s *Sink) send(ctx context.Context, items []item, pending []int) (retry []int, busy bool, err error) {
	limit := s.maxRequest()
	for start := 0; start < len(pending); {
		end, size := start, int64(0)
		for end < len(pending) && (end == start || size+int64(len(items[pending[end]].lines)) <= limit) {
			size += int64(len(items[pending[end]].lines))
			end++
		}
		part := pending[start:end]
		body := make([]byte, 0, size)
		for _, i := range part {
			body = append(body, items[i].lines...)
		}
		results, err := s.bulk(ctx, body, len(part))
		if err != nil {
			return nil, false, err
		}
		for k, i := range part {
			st, e := results[k].Status, results[k].Error
			switch {
			case st >= 200 && st < 300, st == stdhttp.StatusNotFound && items[i].delete,
				st == stdhttp.StatusConflict && s.Action == "create" && !items[i].delete:
				items[i].done, items[i].err = true, nil
			case st == stdhttp.StatusTooManyRequests:
				items[i].err, busy = errBusy, true
				retry = append(retry, i)
			case st >= 500:
				items[i].err = fmt.Errorf("the cluster answered %d: %s", st, e)
				retry = append(retry, i)
			default:
				items[i].err = fitsync.Permanent(fmt.Errorf("the cluster refused it (%d): %s", st, e))
			}
		}
		start = end
	}
	return retry, busy, nil
}

// itemResult is a _bulk item's answer.
type itemResult struct {
	Status int
	Error  string
}

// bulk sends one _bulk request, again with a backoff for as long as the
// cluster answers 429, and returns its n items' results.
func (s *Sink) bulk(ctx context.Context, body []byte, n int) ([]itemResult, error) {
	for round := 0; ; round++ {
		resp, err := s.conn.Do(ctx, stdhttp.MethodPost, "/_bulk", s.query, body, "application/x-ndjson")
		if err != nil {
			return nil, err
		}
		if resp.Status == stdhttp.StatusTooManyRequests {
			logs.Named(config.LoggerSync).Debugf("sync elasticsearch: the cluster is busy (429), the bulk waits")
			if !sleep(ctx, backoff(round)) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.Status != stdhttp.StatusOK {
			err := fmt.Errorf("_bulk answered %d: %s", resp.Status, clip(resp.Body))
			if resp.Status == stdhttp.StatusRequestEntityTooLarge {
				err = fmt.Errorf("%w: lower max_request_bytes", err)
			}
			return nil, err
		}
		return parseBulk(resp.Body, n)
	}
}

func parseBulk(body []byte, n int) ([]itemResult, error) {
	var r struct {
		Errors bool                        `json:"errors"`
		Items  []map[string]itemResultJSON `json:"items"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("_bulk's answer: %v: %s", err, clip(body))
	}
	if len(r.Items) != n {
		return nil, fmt.Errorf("_bulk answered %d items for %d", len(r.Items), n)
	}
	out := make([]itemResult, n)
	for i, m := range r.Items {
		for _, v := range m {
			out[i].Status = v.Status
			if v.Error != nil {
				out[i].Error = v.Error.Type + ": " + v.Error.Reason
			}
		}
	}
	return out, nil
}

type itemResultJSON struct {
	Status int `json:"status"`
	Error  *struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

// backoff is the wait before round's next try.
func backoff(round int) time.Duration {
	d := backoffFirst << min(round, 20)
	return min(d, backoffMost)
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
