package syncelastic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".elasticsearch", New: func() any { return new(Source) },
		Doc: "Reads the documents of an index pattern from Elasticsearch (7.12 and later) or OpenSearch (2.4 and later): a snapshot through a point in time and search_after, then, when cursor names a field (an update time, a sequence), the documents whose cursor has moved on, polled every poll_interval; the checkpoint is the cursor's last value and the _ids read at it. A record's namespace is the document's index, its key {\"_id\": ...} and its after the _source. Deletes are not seen: a soft-delete field, deleted_field, turns a document into a delete. In a job without a cursor, the snapshot is split into slices of the _id, read job.max_parallel at a time."})
}

// The defaults of the source's params.
const (
	DefaultPageSize       = 1000
	MaxPageSize           = 10000
	DefaultMaxPageBytes   = 8 << 20
	DefaultChunkDocuments = 100000
	// MaxSlices is the most chunks a job's snapshot is split into, the
	// cluster's default index.max_slices_per_scroll.
	MaxSlices           = 1024
	DefaultPollInterval = 5 * time.Second
	DefaultDelay        = 2 * time.Second
	DefaultKeepAlive    = 5 * time.Minute
)

// maxEqualIDs is the most _ids at one cursor value a position keeps; past
// them, the rest of that value is read through a point in time at once.
var maxEqualIDs = 1000

// Source is the source elasticsearch.
type Source struct {
	URLs           []string         `json:"urls" fit:"required" doc:"The cluster's HTTP endpoints, as https://es1:9200, each a node or a load balancer: the one that answered last is used, and the next in turn when it fails to (a connection's error, a timeout, a 502, 503 or 504)."`
	Username       string           `json:"username,omitempty" doc:"The user for basic authentication, with password_env."`
	PasswordEnv    string           `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	APIKeyEnv      string           `json:"api_key_env,omitempty" doc:"The environment variable holding an Elasticsearch API key as its create API key call returns it encoded (base64 of id:key), sent as Authorization: ApiKey, read when the pipeline loads. Not with username. OpenSearch has no API keys."`
	CAFile         string           `json:"ca_file,omitempty" doc:"A PEM file of the certificates the cluster's is checked against, in place of the system's roots. The check cannot be turned off."`
	CertFile       string           `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a cluster that asks for one; needs key_file."`
	KeyFile        string           `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName     string           `json:"server_name,omitempty" doc:"The name the cluster's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout        config.Duration  `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Index          string           `json:"index" fit:"required" doc:"The indices read: a name, an alias, a pattern such as logs-*, or several joined by commas; a record's namespace is the index its document is in."`
	Cursor         string           `json:"cursor,omitempty" doc:"A field every document written or changed gets a later value of (an update time, a sequence number), of type date, date_nanos, a number or keyword: after the snapshot, documents whose cursor reaches past the last value read are polled for, ordered by it. Documents without the field are read by the snapshot only. None when left out: the snapshot is read and the source ends."`
	Snapshot       string           `json:"snapshot,omitempty" enum:"pit,cursor,none" default:"\"pit\"" doc:"How the documents there are at the start are read: pit, every document, through a point in time, then the cursor from its value then; cursor, only those with the cursor, in its order, from its lowest value, resumed where a restart left off; none, no snapshot, the cursor from its value now. cursor and none need cursor. pit when left out."`
	DeletedField   string           `json:"deleted_field,omitempty" doc:"A top-level field marking a document deleted: a document with it set, to anything but null or false, is read as a delete of its _id, with the document as before. Deletes themselves are not seen by the source. None when left out."`
	PollInterval   config.Duration  `json:"poll_interval,omitempty" default:"\"5s\"" doc:"How long to wait to poll again once a poll has read everything there was. 5s when left out or 0."`
	Delay          *config.Duration `json:"delay,omitempty" default:"\"2s\"" doc:"For a date or date_nanos cursor, how far behind the cluster's clock polls stay: documents whose cursor is later than now less delay are read only once it has passed, so that a document written with a time but searchable only after the index refreshes (1s by default) is not passed over. Date math counts whole seconds: a fraction is rounded up. 0 reads up to now. 2s when left out."`
	PageSize       int              `json:"page_size,omitempty" min:"0" max:"10000" default:"1000" doc:"The most documents one search returns. 1000 when left out or 0."`
	MaxPageBytes   int64            `json:"max_page_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes a search's answer should hold: past them, the next searches ask for half as many documents, and twice as many again, up to page_size, once answers are under a quarter of it. An answer past 64 MiB is refused and asked again for half. 8 MiB when left out or 0."`
	KeepAlive      config.Duration  `json:"keep_alive,omitempty" default:"\"5m\"" doc:"How long the cluster keeps a point in time, or a job's scroll, between two searches of it. A snapshot stalled longer than this (its sinks held by the queue) finds it gone and starts again. 5m when left out or 0."`
	ChunkDocuments int64            `json:"chunk_documents,omitempty" min:"0" default:"100000" doc:"In a job (mode once) without a cursor, the documents a chunk is reckoned to hold: the snapshot is split into as many slices of the _id as the count of documents needs, at most 1024, read job.max_parallel at a time, each through a scroll of its own. 100000 when left out or 0."`

	conn  *Conn
	ctype string
	size  atomic.Int64
}

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until Run.
func (s *Source) Provision(ctx *module.Context) error {
	conn, errs := NewConn(ctx, ConnParams{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv,
		APIKeyEnv: s.APIKeyEnv, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn = conn
	if !indexName.MatchString(s.Index) {
		errs = append(errs, ctx.Params.Field("index").Errorf("%q is not an index name or pattern", s.Index))
	}
	if s.Snapshot != "" && s.Snapshot != "pit" && s.Cursor == "" {
		errs = append(errs, ctx.Params.Field("snapshot").Errorf("snapshot %q needs a cursor", s.Snapshot))
	}
	if strings.ContainsAny(s.Cursor, " ,*/") || strings.HasPrefix(s.Cursor, "_") {
		errs = append(errs, ctx.Params.Field("cursor").Errorf("%q is not a field of the documents", s.Cursor))
	}
	s.size.Store(int64(s.pageSize()))
	if len(errs) == 0 {
		return nil
	}
	return errs
}

var indexName = regexp.MustCompile(`^[^_/\\?"<>| #][^/\\?"<>| #]*$`)

// Cleanup closes the client.
func (s *Source) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Source) pageSize() int {
	if s.PageSize <= 0 {
		return DefaultPageSize
	}
	return s.PageSize
}

func (s *Source) maxPageBytes() int {
	if s.MaxPageBytes <= 0 {
		return DefaultMaxPageBytes
	}
	return int(s.MaxPageBytes)
}

func orDefault(d config.Duration, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d.Std()
}

// position is the source's checkpoint. Done says the snapshot of a source
// without a cursor is read; otherwise Cursor is the cursor's last value
// read, as the cluster sorts it (none: from its lowest), IDs the documents
// read at it, and Past says every document at it is read.
type position struct {
	Done   bool            `json:"done,omitempty"`
	Cursor json.RawMessage `json:"cursor,omitempty"`
	IDs    []string        `json:"ids,omitempty"`
	Past   bool            `json:"past,omitempty"`
}

func (p *position) marshal() fitsync.Position {
	b, _ := json.Marshal(p)
	return b
}

// hit is a document a search returns.
type hit struct {
	Index   string            `json:"_index"`
	ID      string            `json:"_id"`
	Version int64             `json:"_version"`
	Source  json.RawMessage   `json:"_source"`
	Sort    []json.RawMessage `json:"sort"`
}

type searchResult struct {
	PitID    string `json:"pit_id"`
	ScrollID string `json:"_scroll_id"`
	TimedOut bool   `json:"timed_out"`
	Shards   struct {
		Failed   int               `json:"failed"`
		Failures []json.RawMessage `json:"failures"`
	} `json:"_shards"`
	Hits struct {
		Hits []hit `json:"hits"`
	} `json:"hits"`
	// size is how many documents the search asked for.
	size int
}

// errPITGone is a search of a point in time the cluster no longer has.
var errPITGone = errors.New("the point in time or scroll is gone: it outlived keep_alive between two searches, or the cluster restarted")

// Run reads the snapshot, then polls by the cursor, from r.From.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var pos *position
	if r.From != nil {
		pos = new(position)
		if err := json.Unmarshal(r.From, pos); err != nil {
			return fmt.Errorf("elasticsearch: the checkpoint %s: %w", r.From, err)
		}
		if pos.Done {
			return nil
		}
	}
	if s.Cursor != "" {
		if err := s.detect(ctx); err != nil {
			return err
		}
	}
	if pos == nil {
		start := &position{Done: s.Cursor == ""}
		if s.Cursor != "" && s.Snapshot != "cursor" {
			// The cursor's highest value before the snapshot starts: what
			// changes during it, polling reads again.
			var err error
			if start.Cursor, err = s.highest(ctx); err != nil {
				return err
			}
		}
		if s.Snapshot == "" || s.Snapshot == "pit" {
			var held *fitsync.Record
			err := s.scan(ctx, nil, nil, func(h *hit) error {
				rec, err := s.record(h, fitsync.OpInsert)
				if err != nil {
					return err
				}
				if held != nil {
					if err := r.Emit(held); err != nil {
						return err
					}
				}
				held = rec
				return nil
			})
			if err != nil {
				return err
			}
			if held != nil {
				held.Position = start.marshal()
				if err := r.Emit(held); err != nil {
					return err
				}
			}
			if s.Cursor == "" {
				return nil
			}
		}
		pos = start
	}
	for {
		caught, err := s.poll(ctx, pos, r.Emit)
		if err != nil {
			return err
		}
		if !caught {
			continue
		}
		if r.Once {
			return nil
		}
		if !sleep(ctx, orDefault(s.PollInterval, DefaultPollInterval)) {
			return ctx.Err()
		}
	}
}

// Chunks splits a job's snapshot into slices of the _id, by the count of
// documents; none with a cursor, whose job reads as a stream does, so that
// the next job reads only what is new.
func (s *Source) Chunks(ctx context.Context) ([]fitsync.Chunk, error) {
	if s.Cursor != "" {
		return nil, nil
	}
	n, err := s.count(ctx)
	if err != nil {
		return nil, err
	}
	per := s.ChunkDocuments
	if per <= 0 {
		per = DefaultChunkDocuments
	}
	k := min(max((n+per-1)/per, 1), MaxSlices)
	plan := make([]fitsync.Chunk, k)
	for i := range k {
		rows := n / k
		if i < n%k {
			rows++
		}
		plan[i] = fitsync.Chunk{Namespace: s.Index, Lo: fmt.Appendf(nil, `{"slice":%d,"of":%d}`, i, k), Rows: rows}
	}
	return plan, nil
}

// ReadChunk reads a slice through a scroll of its own: Elasticsearch
// refuses to slice a point in time by the _id, and slices it otherwise by
// ranges of Lucene's document numbers, which another point in time does not
// share, while a scroll's slices by the _id's hash hold the same documents
// whenever the job reads them. A chunk interrupted is read again from its
// start: from is not used.
func (s *Source) ReadChunk(ctx context.Context, c fitsync.Chunk, _ fitsync.Position, emit func(*fitsync.Record) error) error {
	var sl struct{ Slice, Of int }
	if err := json.Unmarshal(c.Lo, &sl); err != nil || sl.Of < 1 || sl.Slice < 0 || sl.Slice >= sl.Of {
		return fmt.Errorf("elasticsearch: the chunk %s is not a slice", c.Lo)
	}
	var slice []int
	if sl.Of > 1 {
		slice = []int{sl.Slice, sl.Of}
	}
	keep := fmt.Sprintf("%dms", orDefault(s.KeepAlive, DefaultKeepAlive).Milliseconds())
	body := map[string]any{"size": s.size.Load(), "version": true, "sort": []string{"_doc"}}
	if slice != nil {
		body["slice"] = map[string]any{"field": "_id", "id": slice[0], "max": slice[1]}
	}
	res, err := s.search(ctx, "/"+s.Index+"/_search", "scroll="+keep, body)
	id := ""
	defer func() { s.clearScroll(ctx, id) }()
	for err == nil {
		if res.ScrollID != "" {
			id = res.ScrollID
		}
		hits := res.Hits.Hits
		for i := range hits {
			rec, err := s.record(&hits[i], fitsync.OpInsert)
			if err != nil {
				return err
			}
			if err := emit(rec); err != nil {
				return err
			}
		}
		if len(hits) == 0 || id == "" {
			return nil
		}
		res, err = s.search(ctx, "/_search/scroll", "", map[string]any{"scroll": keep, "scroll_id": id})
	}
	return err
}

// clearScroll clears a scroll, even once ctx has ended.
func (s *Source) clearScroll(ctx context.Context, id string) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"scroll_id": []string{id}})
	_, _ = s.conn.Do(ctx, stdhttp.MethodDelete, "/_search/scroll", "", b, "application/json")
}

// record makes a document's record.
func (s *Source) record(h *hit, op fitsync.Op) (*fitsync.Record, error) {
	doc := fitsync.Map()
	if len(h.Source) > 0 {
		var err error
		if doc, err = fitsync.ParsePlain(h.Source); err != nil {
			return nil, fmt.Errorf("elasticsearch: the document %s/%s: %w", h.Index, h.ID, err)
		}
	}
	r := &fitsync.Record{Source: "elasticsearch", Namespace: h.Index, Op: op,
		Key: fitsync.Map(fitsync.Field{Name: "_id", Value: fitsync.String(h.ID)}), After: doc}
	if op != fitsync.OpInsert && h.Version == 1 {
		r.Op = fitsync.OpInsert
	}
	if s.DeletedField != "" {
		if v, ok := doc.Get(s.DeletedField); ok && !v.IsNull() && !(v.Kind() == fitsync.KindBool && !v.AsBool()) {
			r.Op, r.Before, r.After = fitsync.OpDelete, doc, fitsync.Null()
		}
	}
	if op != fitsync.OpInsert && len(h.Sort) > 0 {
		r.CommitTime = s.cursorTime(h.Sort[0])
	}
	return r, nil
}

// cursorTime is a date cursor's value as a time; zero for other cursors.
func (s *Source) cursorTime(v json.RawMessage) time.Time {
	n, err := strconv.ParseInt(string(v), 10, 64)
	switch {
	case err != nil:
	case s.ctype == "date":
		return time.UnixMilli(n).UTC()
	case s.ctype == "date_nanos":
		return time.Unix(0, n).UTC()
	}
	return time.Time{}
}

// The cursor's types the source takes.
var cursorTypes = map[string]bool{"date": true, "date_nanos": true, "long": true, "integer": true, "short": true, "byte": true,
	"double": true, "float": true, "half_float": true, "scaled_float": true, "unsigned_long": true, "keyword": true}

// detect learns the cursor's type from the indices' mappings: one type
// across them, or none while no index maps it.
func (s *Source) detect(ctx context.Context) error {
	resp, err := s.call(ctx, stdhttp.MethodGet, "/"+s.Index+"/_mapping/field/"+s.Cursor, "", nil)
	if err != nil {
		return err
	}
	if resp.Status == stdhttp.StatusNotFound {
		return nil
	}
	if resp.Status != stdhttp.StatusOK {
		return fmt.Errorf("elasticsearch: the cursor's mapping: %d %s", resp.Status, clip(resp.Body))
	}
	var m map[string]struct {
		Mappings map[string]struct {
			Mapping map[string]struct {
				Type string `json:"type"`
			} `json:"mapping"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(resp.Body, &m); err != nil {
		return fmt.Errorf("elasticsearch: the cursor's mapping: %w", err)
	}
	t := ""
	for index, im := range m {
		for _, f := range im.Mappings {
			for _, leaf := range f.Mapping {
				if !cursorTypes[leaf.Type] {
					return fmt.Errorf("elasticsearch: the cursor %s is a %s in %s: a date, date_nanos, a number or keyword is needed", s.Cursor, leaf.Type, index)
				}
				if t != "" && t != leaf.Type {
					return fmt.Errorf("elasticsearch: the cursor %s is a %s in one index and a %s in %s", s.Cursor, t, leaf.Type, index)
				}
				t = leaf.Type
			}
		}
	}
	s.ctype = t
	return nil
}

func (s *Source) dated() bool { return s.ctype == "date" || s.ctype == "date_nanos" }

// sortBy is the cursor's sort clause.
func (s *Source) sortBy(order string) []any {
	t := s.ctype
	if t == "" {
		t = "long"
	}
	return []any{map[string]any{s.Cursor: map[string]any{"order": order, "unmapped_type": t}}}
}

// bound is a range of the cursor's value v, with v as each of ops' bound:
// dates as epoch milliseconds, date_nanos with their fraction.
func (s *Source) bound(v json.RawMessage, ops ...string) map[string]any {
	var b any = v
	r := map[string]any{}
	if s.dated() {
		r["format"] = "epoch_millis"
		if n, err := strconv.ParseInt(string(v), 10, 64); err == nil && s.ctype == "date_nanos" {
			b = fmt.Sprintf("%d.%06d", n/1e6, n%1e6) // date_nanos start at 1970
		}
	}
	for _, op := range ops {
		r[op] = b
	}
	return map[string]any{"range": map[string]any{s.Cursor: r}}
}

// filters are the clauses every cursor search has: the field there, and,
// for a date, not later than delay before now.
func (s *Source) filters() []any {
	f := []any{map[string]any{"exists": map[string]any{"field": s.Cursor}}}
	d := DefaultDelay
	if s.Delay != nil {
		d = s.Delay.Std()
	}
	if s.dated() && d > 0 {
		f = append(f, map[string]any{"range": map[string]any{s.Cursor: map[string]any{"lte": fmt.Sprintf("now-%ds", (d+time.Second-1)/time.Second)}}})
	}
	return f
}

// highest returns the cursor's highest value, nil when no document has it.
func (s *Source) highest(ctx context.Context) (json.RawMessage, error) {
	res, err := s.search(ctx, "/"+s.Index+"/_search", "", map[string]any{"size": 1, "_source": false, "track_total_hits": false,
		"sort": s.sortBy("desc"), "query": map[string]any{"bool": map[string]any{"filter": s.filters()}}})
	if err != nil || len(res.Hits.Hits) == 0 {
		return nil, err
	}
	if h := res.Hits.Hits[0]; len(h.Sort) > 0 {
		return h.Sort[0], nil
	}
	return nil, nil
}

// poll reads a page of documents past pos, and moves pos past them. It
// reports whether it read everything there was.
func (s *Source) poll(ctx context.Context, pos *position, emit func(*fitsync.Record) error) (bool, error) {
	if s.ctype == "" {
		if err := s.detect(ctx); err != nil {
			return false, err
		}
	}
	if !pos.Past && len(pos.IDs) >= maxEqualIDs {
		return false, s.drain(ctx, pos, emit)
	}
	filter := s.filters()
	if pos.Cursor != nil {
		op := "gte"
		if pos.Past {
			op = "gt"
		}
		filter = append(filter, s.bound(pos.Cursor, op))
	}
	q := map[string]any{"filter": filter}
	if len(pos.IDs) > 0 && !pos.Past {
		q["must_not"] = []any{map[string]any{"ids": map[string]any{"values": pos.IDs}}}
	}
	res, err := s.search(ctx, "/"+s.Index+"/_search", "", map[string]any{"size": s.size.Load(), "version": true, "track_total_hits": false,
		"sort": s.sortBy("asc"), "query": map[string]any{"bool": q}})
	if err != nil {
		return false, err
	}
	var recs []*fitsync.Record
	full := false
	for i := range res.Hits.Hits {
		h := &res.Hits.Hits[i]
		if len(h.Sort) == 0 {
			return false, errors.New("elasticsearch: a search answered a document without its sort values")
		}
		if v := h.Sort[0]; !pos.Past && bytes.Equal(v, pos.Cursor) {
			if len(pos.IDs) >= maxEqualIDs {
				full = true
				break
			}
			pos.IDs = append(pos.IDs, h.ID)
		} else {
			pos.Cursor, pos.IDs, pos.Past = v, []string{h.ID}, false
		}
		rec, err := s.record(h, fitsync.OpUpdate)
		if err != nil {
			return false, err
		}
		recs = append(recs, rec)
	}
	if len(recs) > 0 {
		recs[len(recs)-1].Position = pos.marshal()
	}
	for _, rec := range recs {
		if err := emit(rec); err != nil {
			return false, err
		}
	}
	return !full && len(res.Hits.Hits) < res.size, nil
}

// drain reads every document at the cursor's value pos holds that pos has
// not, through a point in time, since more share it than a position keeps.
func (s *Source) drain(ctx context.Context, pos *position, emit func(*fitsync.Record) error) error {
	q := map[string]any{"bool": map[string]any{"filter": []any{s.bound(pos.Cursor, "gte", "lte")}, "must_not": []any{map[string]any{"ids": map[string]any{"values": pos.IDs}}}}}
	var held *fitsync.Record
	err := s.scan(ctx, q, nil, func(h *hit) error {
		rec, err := s.record(h, fitsync.OpUpdate)
		if err != nil {
			return err
		}
		rec.CommitTime = s.cursorTime(pos.Cursor)
		if held != nil {
			if err := emit(held); err != nil {
				return err
			}
		}
		held = rec
		return nil
	})
	if err != nil {
		return err
	}
	pos.IDs, pos.Past = nil, true
	if held != nil {
		held.Position = pos.marshal()
		return emit(held)
	}
	return nil
}

// count returns how many documents the indices hold.
func (s *Source) count(ctx context.Context) (int64, error) {
	resp, err := s.call(ctx, stdhttp.MethodGet, "/"+s.Index+"/_count", "", nil)
	if err != nil {
		return 0, err
	}
	if resp.Status == stdhttp.StatusNotFound {
		return 0, nil
	}
	var c struct {
		Count int64 `json:"count"`
	}
	if resp.Status != stdhttp.StatusOK || json.Unmarshal(resp.Body, &c) != nil {
		return 0, fmt.Errorf("elasticsearch: counting %s: %d %s", s.Index, resp.Status, clip(resp.Body))
	}
	return c.Count, nil
}

// scan reads the documents query matches (all when nil), in slice
// [id, max] of the _id when slice is set, through a point in time, in pages,
// handing each to each in order; the point in time is closed at the end.
func (s *Source) scan(ctx context.Context, query any, slice []int, each func(*hit) error) error {
	info, err := s.conn.Info(ctx)
	if err != nil {
		return err
	}
	if err := pitSupported(info); err != nil {
		return err
	}
	keep := fmt.Sprintf("%dms", orDefault(s.KeepAlive, DefaultKeepAlive).Milliseconds())
	id, err := s.openPIT(ctx, info, keep)
	if err != nil || id == "" {
		return err
	}
	defer func() { s.closePIT(ctx, info, id) }()
	tie := "_shard_doc"
	if info.OpenSearch() {
		tie = "_id"
	}
	var after []json.RawMessage
	for {
		body := map[string]any{"size": s.size.Load(), "pit": map[string]any{"id": id, "keep_alive": keep}, "version": true,
			"track_total_hits": false, "sort": []any{map[string]any{tie: "asc"}}}
		if after != nil {
			body["search_after"] = after
		}
		if query != nil {
			body["query"] = query
		}
		if slice != nil {
			body["slice"] = map[string]any{"field": "_id", "id": slice[0], "max": slice[1]}
		}
		res, err := s.search(ctx, "/_search", "", body)
		if err != nil {
			return err
		}
		if res.PitID != "" {
			id = res.PitID
		}
		hits := res.Hits.Hits
		for i := range hits {
			if err := each(&hits[i]); err != nil {
				return err
			}
		}
		if len(hits) == 0 {
			return nil
		}
		if after = hits[len(hits)-1].Sort; len(after) == 0 {
			return errors.New("elasticsearch: a search answered a document without its sort values")
		}
	}
}

// pitSupported refuses a cluster without points in time and _shard_doc.
func pitSupported(info *Info) error {
	var major, minor int
	_, _ = fmt.Sscanf(info.Version, "%d.%d", &major, &minor)
	want := [2]int{7, 12}
	if info.OpenSearch() {
		want = [2]int{2, 4}
	}
	if major < want[0] || major == want[0] && minor < want[1] {
		return fmt.Errorf("elasticsearch: %s %s has no point in time to read a snapshot through: %d.%d or later is needed", info.Distribution, info.Version, want[0], want[1])
	}
	return nil
}

// openPIT opens a point in time of the indices; "" when they hold nothing
// to open one of.
func (s *Source) openPIT(ctx context.Context, info *Info, keep string) (string, error) {
	path := "/" + s.Index + "/_pit"
	if info.OpenSearch() {
		path = "/" + s.Index + "/_search/point_in_time"
	}
	resp, err := s.conn.Do(ctx, stdhttp.MethodPost, path, "keep_alive="+keep, nil, "")
	if err != nil {
		return "", err
	}
	var r struct {
		ID    string `json:"id"`
		PitID string `json:"pit_id"`
	}
	if resp.Status == stdhttp.StatusOK && json.Unmarshal(resp.Body, &r) == nil && r.ID+r.PitID != "" {
		return r.ID + r.PitID, nil
	}
	// OpenSearch refuses a point in time of a pattern matching no index.
	if n, cerr := s.count(ctx); cerr == nil && n == 0 {
		return "", nil
	}
	return "", fmt.Errorf("elasticsearch: opening a point in time of %s: %d %s", s.Index, resp.Status, clip(resp.Body))
}

// closePIT closes a point in time, even once ctx has ended; one left open
// by a failure lapses after keep_alive.
func (s *Source) closePIT(ctx context.Context, info *Info, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	path, body := "/_pit", map[string]any{"id": id}
	if info.OpenSearch() {
		path, body = "/_search/point_in_time", map[string]any{"pit_id": []string{id}}
	}
	b, _ := json.Marshal(body)
	_, _ = s.conn.Do(ctx, stdhttp.MethodDelete, path, "", b, "application/json")
}

// call sends a request, waiting out the cluster's 429s for as long as they
// last, as the sink does.
func (s *Source) call(ctx context.Context, method, path, query string, body []byte) (*Response, error) {
	ct := ""
	if body != nil {
		ct = "application/json"
	}
	for round := 0; ; round++ {
		resp, err := s.conn.Do(ctx, method, path, query, body, ct)
		if err != nil || resp.Status != stdhttp.StatusTooManyRequests {
			return resp, err
		}
		if !sleep(ctx, backoff(round)) {
			return nil, ctx.Err()
		}
	}
}

// search runs a search, asking for fewer documents while answers run past
// max_page_bytes, and more again once they are well under it.
// A scroll's next page has the size of its first: an answer past the client's
// bound fails it, and halves the size of the searches after.
func (s *Source) search(ctx context.Context, path, query string, body map[string]any) (*searchResult, error) {
	for {
		// The shared page size adapts; a search of a fixed size does not.
		size, adapt := body["size"].(int64)
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		resp, err := s.call(ctx, stdhttp.MethodPost, path, query, b)
		if errors.Is(err, fibhttp.ErrResponseBodyTooLarge) {
			if !adapt {
				s.size.Store(max(s.size.Load()/2, 1))
			} else if size > 1 {
				s.size.Store(size / 2)
				body["size"] = size / 2
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if resp.Status == stdhttp.StatusNotFound && bytes.Contains(resp.Body, []byte("search_context_missing_exception")) {
			return nil, fmt.Errorf("elasticsearch: %w", errPITGone)
		}
		if resp.Status != stdhttp.StatusOK {
			return nil, fmt.Errorf("elasticsearch: a search of %s answered %d: %s", s.Index, resp.Status, clip(resp.Body))
		}
		if limit := s.maxPageBytes(); adapt {
			switch {
			case len(resp.Body) > limit && size > 1:
				s.size.Store(size / 2)
			case len(resp.Body) < limit/4 && size < int64(s.pageSize()):
				s.size.Store(min(size*2, int64(s.pageSize())))
			}
		}
		res := &searchResult{size: int(size)}
		if err := json.Unmarshal(resp.Body, res); err != nil {
			return nil, fmt.Errorf("elasticsearch: a search's answer: %w", err)
		}
		if res.TimedOut || res.Shards.Failed > 0 {
			// A page missing a shard's documents would leave a gap.
			return nil, fmt.Errorf("elasticsearch: a search of %s failed on %d shards (timed out: %v): %s", s.Index, res.Shards.Failed, res.TimedOut, clip(bytes.Join(rawJoin(res.Shards.Failures), []byte(", "))))
		}
		return res, nil
	}
}

func rawJoin(rs []json.RawMessage) [][]byte {
	out := make([][]byte, len(rs))
	for i, r := range rs {
		out[i] = r
	}
	return out
}
