package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

// A counter counts the records at the sink: lines in the files written,
// documents in the index, messages in the topic, rows in the table.
type counter interface {
	count(ctx context.Context) (int64, error)
	close()
}

// fileCounter counts the lines of the files in a directory, reading each
// only from where it last stopped; a file that shrank is read again.
type fileCounter struct {
	dir  string
	seen map[string]*fileCount
	buf  []byte
}

type fileCount struct {
	off, lines int64
}

func (f *fileCounter) count(context.Context) (int64, error) {
	if f.seen == nil {
		f.seen = map[string]*fileCount{}
		f.buf = make([]byte, 1<<20)
	}
	ents, err := os.ReadDir(f.dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		fc := f.seen[e.Name()]
		if fc == nil {
			fc = &fileCount{}
			f.seen[e.Name()] = fc
		}
		if err := f.read(filepath.Join(f.dir, e.Name()), fc); err != nil {
			return 0, err
		}
		total += fc.lines
	}
	return total, nil
}

func (f *fileCounter) read(path string, fc *fileCount) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	if st.Size() < fc.off {
		*fc = fileCount{}
	}
	if _, err := fh.Seek(fc.off, io.SeekStart); err != nil {
		return err
	}
	for {
		n, err := fh.Read(f.buf)
		fc.off += int64(n)
		fc.lines += int64(bytes.Count(f.buf[:n], []byte{'\n'}))
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (f *fileCounter) close() {}

// esCounter counts an index's documents: those searchable, so after its
// refresh, every second by default.
type esCounter struct {
	client *http.Client
	url    string
}

func (e *esCounter) count(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return 0, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s: %s", e.url, resp.Status, bytes.TrimSpace(body))
	}
	var r struct {
		Count *int64 `json:"count"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Count == nil {
		return 0, fmt.Errorf("%s: no count in %q", e.url, body)
	}
	return *r.Count, nil
}

func (e *esCounter) close() {}

// lokiCounter counts the lines Loki took: its distributors'
// loki_distributor_lines_received_total, of every tenant, from its
// metrics, which costs Loki nothing a query of the lines would.
type lokiCounter struct {
	client *http.Client
	url    string
}

func (l *lokiCounter) count(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", l.url, resp.Status)
	}
	var total float64
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "loki_distributor_lines_received_total") {
			continue
		}
		var v float64
		if _, err := fmt.Sscan(line[strings.LastIndexByte(line, ' ')+1:], &v); err != nil {
			return 0, fmt.Errorf("%s: %q: %v", l.url, line, err)
		}
		total += v
	}
	return int64(total), nil
}

func (l *lokiCounter) close() {}

// kafkaCounter counts a topic's messages: the sum of its partitions' next
// offsets, the movers writing no transactions. A topic not made yet has
// none.
type kafkaCounter struct {
	client *kafkawire.Client
	topic  string
}

func (k *kafkaCounter) count(ctx context.Context) (int64, error) {
	n, err := k.client.Partitions(ctx, k.topic)
	if errors.Is(err, kafkawire.ErrUnknownTopicOrPartition) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	qs := make([]kafkawire.OffsetQuery, n)
	for i := range qs {
		qs[i] = kafkawire.OffsetQuery{Topic: k.topic, Partition: int32(i), Timestamp: kafkawire.OffsetLatest}
	}
	res, err := k.client.ListOffsets(ctx, false, qs...)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, r := range res {
		total += r.Offset
	}
	return total, nil
}

func (k *kafkaCounter) close() { _ = k.client.Close() }

// canalCounter counts the rows of a topic Canal writes, whose messages
// (its flat messages) hold a transaction's rows of a table each: it reads
// every message, once, and adds up their data's rows.
type canalCounter struct {
	client *kafkawire.Client
	topic  string
	next   []int64
	rows   int64
}

func (k *canalCounter) count(ctx context.Context) (int64, error) {
	n, err := k.client.Partitions(ctx, k.topic)
	if errors.Is(err, kafkawire.ErrUnknownTopicOrPartition) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for len(k.next) < int(n) {
		k.next = append(k.next, 0)
	}
	// Read until every partition is at the high watermark it gave.
	for {
		parts := make([]kafkawire.FetchPartition, len(k.next))
		for i, off := range k.next {
			parts[i] = kafkawire.FetchPartition{Topic: k.topic, Partition: int32(i), Offset: off}
		}
		res, err := k.client.Fetch(ctx, kafkawire.FetchOptions{MaxWait: 10 * time.Millisecond}, parts...)
		if err != nil {
			return 0, err
		}
		behind := false
		for i, r := range res {
			for _, rec := range r.Records {
				var m struct {
					Data []json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(rec.Value, &m); err != nil {
					return 0, fmt.Errorf("%s/%d at %d: %v", k.topic, i, rec.Offset, err)
				}
				k.rows += int64(len(m.Data))
			}
			k.next[i] = r.NextOffset
			if r.NextOffset < r.HighWatermark {
				behind = true
			}
		}
		if !behind {
			return k.rows, nil
		}
	}
}

func (k *canalCounter) close() { _ = k.client.Close() }

// influxCounter counts the points of the measurement reading in InfluxDB
// 2, through its v1 query API (the bucket a database of its name):
// count(id) over the readings' times, every point having an id. A point is
// its series and its time, and each reading has a time of its own, so a
// reading written twice is one point: no extras show.
type influxCounter struct {
	client *http.Client
	url    string
	token  string
}

// influxCount is the query: every reading's time is in its first days
// after readingsEpoch.
var influxCount = "SELECT count(id) FROM reading WHERE time >= '" + readingsEpoch.Format(time.RFC3339) +
	"' AND time < '" + readingsEpoch.AddDate(0, 0, 30).Format(time.RFC3339) + "'"

func (c *influxCounter) count(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return 0, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Token "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("influxdb: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var r struct {
		Results []struct {
			Error  string `json:"error"`
			Series []struct {
				Values [][]any `json:"values"`
			} `json:"series"`
		} `json:"results"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&r); err != nil || len(r.Results) != 1 {
		return 0, fmt.Errorf("influxdb: no result in %q", body)
	}
	if e := r.Results[0].Error; e != "" {
		return 0, fmt.Errorf("influxdb: %s", e)
	}
	// No series: no point yet.
	if len(r.Results[0].Series) == 0 {
		return 0, nil
	}
	v := r.Results[0].Series[0].Values
	if len(v) != 1 || len(v[0]) != 2 {
		return 0, fmt.Errorf("influxdb: no count in %q", body)
	}
	n, ok := v[0][1].(json.Number)
	if !ok {
		return 0, fmt.Errorf("influxdb: no count in %q", body)
	}
	return n.Int64()
}

func (c *influxCounter) close() {}

// sqlCounter counts a table's rows.
type sqlCounter struct {
	c   execer
	sql string
}

func (s *sqlCounter) count(ctx context.Context) (int64, error) { return s.c.count(ctx, s.sql) }
func (s *sqlCounter) close()                                   { s.c.close() }

// openCounter opens the scenario's sink for counting.
func openCounter(ctx context.Context, o *options) (counter, error) {
	switch o.sc.sink {
	case "files":
		return &fileCounter{dir: o.outDir}, nil
	case "loki":
		return &lokiCounter{client: &http.Client{Timeout: 10 * time.Second}, url: strings.TrimRight(o.loki, "/") + "/metrics"}, nil
	case "es":
		return &esCounter{client: &http.Client{Timeout: 10 * time.Second},
			url: strings.TrimRight(o.es, "/") + "/" + o.esIndex + "/_count"}, nil
	case "influxdb":
		return &influxCounter{client: &http.Client{Timeout: 10 * time.Second}, token: o.influxdbToken,
			url: strings.TrimRight(o.influxdb, "/") + "/query?" + url.Values{"db": {o.influxdbBucket}, "q": {influxCount}}.Encode()}, nil
	case "kafka":
		c, err := kafkawire.New(kafkawire.Config{Brokers: []string{o.kafka}, ClientID: "syncbench",
			MetadataMaxAge: time.Second, RetryFor: time.Second})
		if err != nil {
			return nil, err
		}
		if o.kafkaFormat == "canal" {
			return &canalCounter{client: c, topic: o.kafkaTopic}, nil
		}
		return &kafkaCounter{client: c, topic: o.kafkaTopic}, nil
	}
	c, err := mysqlDial(ctx, o.target, o.mysqlUser, o.mysqlPassword)
	if err != nil {
		return nil, err
	}
	return &sqlCounter{c: c, sql: "SELECT COUNT(*) FROM shop.items"}, nil
}
