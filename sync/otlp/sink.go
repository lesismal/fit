// Package syncotlp is sync's OpenTelemetry: the sink otlp_logs, which
// exports records as OTLP log records over HTTP (/v1/logs), protobuf or
// JSON, through fib's asynchronous client, to an OpenTelemetry Collector
// or any OTLP receiver. Its resource is the one fit's own telemetry export
// sends (service.name, service.version, observe's), with attributes of the
// config's; each record's attributes and body are mapped from it. See
// docs/sync.md.
package syncotlp

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	stdhttp "net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/httpconn"
	"github.com/lesismal/fit/sync/internal/pb"
	"github.com/lesismal/fit/sync/internal/rectmpl"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".otlp_logs", New: func() any { return new(Sink) },
		Doc: "Exports records as OpenTelemetry log records over OTLP/HTTP (/v1/logs), protobuf or JSON, gzipped, to an OpenTelemetry Collector or any OTLP receiver. A record's body is the line its codec makes, or its row as a map; its time comes from timestamp_field or its commit time, its severity from severity_field; its attributes and the resource's are templates of its fields, records of one resource exported together under it, with service.name and service.version as fit's own telemetry sends them. Requests are bounded by max_request_bytes and sent again with a backoff on 429 and 5xx; one the receiver refuses (400) is split until the record it refuses is found, which fails, on_error taking it, those before it exported."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 4 << 20
	DefaultServiceName     = "fit"
	logsPath               = "/v1/logs"
)

// warnf logs a warning of the sink's; the tests watch it.
var warnf = func(format string, args ...any) { logs.Named(config.LoggerSync).Warnf(format, args...) }

// Sink is the sink otlp_logs.
type Sink struct {
	URLs            []string          `json:"urls" fit:"required" doc:"The receiver's OTLP/HTTP address, as http://collector:4318, with or without the path /v1/logs, which goes after it when it is not there. The one that answered last is used, and the next in turn when one fails to (a connection's error, a timeout, a 502, 503 or 504)."`
	Username        string            `json:"username,omitempty" doc:"The user for basic authentication, with password_env; none when left out."`
	PasswordEnv     string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	BearerTokenEnv  string            `json:"bearer_token_env,omitempty" doc:"The environment variable holding a token sent as Authorization: Bearer, in place of username."`
	HeadersFile     string            `json:"headers_file,omitempty" doc:"A file of headers sent with every export, one \"Name: value\" a line, # starting a comment, as telemetry's headers_file, for a vendor's API key kept out of the config. Read when the pipeline loads."`
	CAFile          string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the receiver's is checked against over https, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	CertFile        string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a receiver that asks for one; needs key_file."`
	KeyFile         string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string            `json:"server_name,omitempty" doc:"The name the receiver's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout         config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one export may take, from connecting to the end of its answer. 60s when left out or 0."`
	Encoding        string            `json:"encoding,omitempty" enum:"protobuf,json" default:"\"protobuf\"" doc:"How an export is sent: protobuf (application/x-protobuf) or JSON (application/json), OTLP/HTTP's two. protobuf when left out."`
	Compression     string            `json:"compression,omitempty" enum:"gzip,none" default:"\"gzip\"" doc:"How an export's body is compressed: gzip, or none. gzip when left out."`
	ServiceName     string            `json:"service_name,omitempty" default:"\"fit\"" doc:"The resource's service.name. fit when left out, as fit's telemetry has it."`
	Resource        map[string]string `json:"resource,omitempty" doc:"More attributes of the resource, the name to a template of the value: text with {name}, a field of the row (after, or before when after is null), and {$namespace}, {$op}, {$source} and {$key}, the record's; {{ is a brace. Records whose resources come out the same are exported under one. Empty values are left out. service.name and service.version are the sink's. None when left out."`
	Attributes      map[string]string `json:"attributes,omitempty" doc:"Each log record's attributes, the name to a template as resource has them; empty values are left out. {\"fit.namespace\": \"{$namespace}\", \"fit.op\": \"{$op}\"} when left out."`
	Body            string            `json:"body,omitempty" enum:"line,row" default:"\"line\"" doc:"What a log record's body is: line, the text its codec makes; row, the record's row as a map of its fields, typed (ints, floats, booleans, bytes, arrays and maps as OTLP has them; times, dates and decimals as text). line when left out."`
	Codec           *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a record's line, with body line: line when left out, which writes after's line field, or the record's canonical JSON when it has none; json writes the row as a JSON object."`
	TimestampField  string            `json:"timestamp_field,omitempty" doc:"A field of the row whose value is the log record's time: a time, an integer of Unix seconds, milliseconds, microseconds or nanoseconds, or RFC 3339 text. The record's commit time when left out or when a record lacks it, and none when it has neither (its observed time is the export's)."`
	SeverityField   string            `json:"severity_field,omitempty" doc:"A field of the row whose value is the severity: text (trace, debug, info, warn or warning, error, fatal or critical, any case), kept as the severity text and numbered as OTLP numbers them, or a number from 1 to 24. None when left out."`
	TraceIDField    string            `json:"trace_id_field,omitempty" doc:"A field of the row holding a trace's ID as 32 hex digits, which the log record is tied to, with span_id_field. None when left out."`
	SpanIDField     string            `json:"span_id_field,omitempty" doc:"A field of the row holding a span's ID as 16 hex digits. None when left out."`
	MaxRequestBytes int64             `json:"max_request_bytes,omitempty" min:"0" default:"4194304" doc:"The most bytes of one export's log records, before compression; a batch larger is exported in several, in order, and a record larger fails. The Collector's own bound is its max_request_body_size, 20 MiB by default. 4 MiB when left out or 0."`

	conn     *httpconn.Conn
	path     string
	codec    fitsync.Codec
	resource []attr
	attrs    []attr
}

// attr is an attribute's name and template.
type attr struct {
	name string
	tmpl *rectmpl.Template
}

// Provision checks the params, loads the codec and reads the secrets, the
// headers and the TLS files; nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	header := map[string]string{}
	if s.HeadersFile != "" {
		data, err := os.ReadFile(s.HeadersFile)
		var h stdhttp.Header
		if err == nil {
			h, err = config.ParseHeaders(string(data))
		}
		if err != nil {
			errs = append(errs, ctx.Params.Field("headers_file").Errorf("%s: %v", s.HeadersFile, err))
		}
		for k := range h {
			header[k] = h.Get(k)
		}
	}
	conn, cerrs := httpconn.New(ctx, httpconn.Params{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv, BearerTokenEnv: s.BearerTokenEnv,
		CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout, Header: header, AllowPath: true})
	s.conn = conn
	errs = append(errs, cerrs...)
	// A URL's path may end in /v1/logs, which is then not added: all or
	// none of them.
	ends := 0
	for _, u := range s.URLs {
		if strings.HasSuffix(strings.TrimSuffix(u, "/"), logsPath) {
			ends++
		}
	}
	switch ends {
	case 0:
		s.path = logsPath
	case len(s.URLs):
	default:
		errs = append(errs, ctx.Params.Field("urls").Errorf("some URLs end in /v1/logs and some do not"))
	}
	if s.Encoding == "" {
		s.Encoding = "protobuf"
	}
	if s.Compression == "" {
		s.Compression = "gzip"
	}
	if s.ServiceName == "" {
		s.ServiceName = DefaultServiceName
	}
	if s.Body == "" {
		s.Body = "line"
	}
	attrs := s.Attributes
	if len(attrs) == 0 {
		attrs = map[string]string{"fit.namespace": "{$namespace}", "fit.op": "{$op}"}
	}
	parse := func(field string, m map[string]string) []attr {
		var out []attr
		for name, t := range m {
			if name == "" {
				errs = append(errs, ctx.Params.Field(field).Errorf("an attribute without a name"))
				continue
			}
			if field == "resource" && (name == "service.name" || name == "service.version") {
				errs = append(errs, ctx.Params.Field(field).Errorf("%s is the sink's: service_name sets service.name", name))
				continue
			}
			tmpl, err := rectmpl.Parse(t)
			if err != nil {
				errs = append(errs, ctx.Params.Field(field).Errorf("%s: %v", name, err))
				continue
			}
			out = append(out, attr{name: name, tmpl: tmpl})
		}
		slices.SortFunc(out, func(a, b attr) int { return cmp.Compare(a.name, b.name) })
		return out
	}
	s.resource = parse("resource", s.Resource)
	s.attrs = parse("attributes", attrs)
	if s.Codec != nil && s.Body == "row" {
		errs = append(errs, ctx.Params.Field("codec").Errorf("a codec makes a line: body row has none"))
	}
	if len(errs) > 0 {
		return errs
	}
	codec, err := loadCodec(ctx, s.Codec)
	s.codec = codec
	return err
}

func loadCodec(ctx *module.Context, ref *config.ModuleRef) (fitsync.Codec, error) {
	if ref == nil {
		return &fitsync.LineCodec{}, nil
	}
	v, err := ctx.Load(fitsync.Codecs, *ref)
	if err != nil {
		return nil, err
	}
	c, ok := v.(fitsync.Codec)
	if !ok {
		module.Cleanup(v)
		return nil, ref.At.Field("module").Errorf("%s is not a codec", ref.Module)
	}
	return c, nil
}

// Test exports an empty request, which a receiver answers without storing
// anything: it is there, speaks OTLP/HTTP and takes the credentials.
func (s *Sink) Test(ctx context.Context) error {
	return s.export(ctx, nil)
}

// Cleanup closes the client and lets the codec go.
func (s *Sink) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
	module.Cleanup(s.codec)
}

func (s *Sink) maxRequest() int64 {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return s.MaxRequestBytes
}

// logRecord is a record as the sink exports it: its resource's key, and
// its LogRecord's bytes.
type logRecord struct {
	resource string   // the resource's attributes, as a key
	rattrs   []string // its names and values
	data     []byte   // the LogRecord, protobuf; JSON with encoding json
	err      error
}

// severities number the severity texts, as OTLP's SeverityNumber does.
var severities = map[string]int64{"trace": 1, "debug": 5, "info": 9, "information": 9, "notice": 10, "warn": 13, "warning": 13,
	"error": 17, "err": 17, "fatal": 21, "critical": 21, "crit": 21, "alert": 22, "emergency": 23, "emerg": 23}

// build makes r's log record.
func (s *Sink) build(r *fitsync.Record) logRecord {
	var lr logRecord
	row := rectmpl.Row(r)
	var key strings.Builder
	for _, a := range s.resource {
		if v := a.tmpl.Render(r); v != "" {
			lr.rattrs = append(lr.rattrs, a.name, v)
			key.WriteString(a.name + "\x00" + v + "\x00")
		}
	}
	lr.resource = key.String()
	var ts time.Time
	if s.TimestampField != "" {
		if v, ok := row.Get(s.TimestampField); ok {
			ts, _ = rectmpl.TimeOf(v)
		}
	}
	if ts.IsZero() {
		ts = r.CommitTime
	}
	var sevText string
	var sevNum int64
	if s.SeverityField != "" {
		if v, ok := row.Get(s.SeverityField); ok && !v.IsNull() {
			switch v.Kind() {
			case fitsync.KindInt, fitsync.KindUint:
				if n := v.AsInt(); n >= 1 && n <= 24 {
					sevNum = n
				}
			default:
				sevText = fitsync.TextOf(v)
				sevNum = severities[strings.ToLower(strings.TrimSpace(sevText))]
			}
		}
	}
	var traceID, spanID []byte
	ids := func(field string, n int) ([]byte, error) {
		if field == "" {
			return nil, nil
		}
		v, ok := row.Get(field)
		if !ok || v.IsNull() || v.Text() == "" {
			return nil, nil
		}
		id, err := hex.DecodeString(v.Text())
		if err != nil || len(id) != n {
			return nil, fmt.Errorf("%s is %q, not %d hex digits", field, v.Text(), 2*n)
		}
		return id, nil
	}
	var err error
	if traceID, err = ids(s.TraceIDField, 16); err == nil {
		spanID, err = ids(s.SpanIDField, 8)
	}
	if err != nil {
		return logRecord{err: fitsync.Permanent(err)}
	}
	var body fitsync.Value
	if s.Body == "row" {
		body = row
	} else {
		b, err := s.codec.Encode(nil, []*fitsync.Record{r})
		if err != nil {
			return logRecord{err: fitsync.Permanent(err)}
		}
		body = fitsync.String(strings.TrimSuffix(string(b), "\n"))
	}
	var attrs []string
	for _, a := range s.attrs {
		if v := a.tmpl.Render(r); v != "" {
			attrs = append(attrs, a.name, v)
		}
	}
	if s.Encoding == "json" {
		lr.data, err = appendJSONRecord(nil, ts, sevNum, sevText, body, attrs, traceID, spanID)
	} else {
		lr.data, err = appendProtoRecord(nil, ts, sevNum, sevText, body, attrs, traceID, spanID)
	}
	if err != nil {
		return logRecord{err: fitsync.Permanent(err)}
	}
	if int64(len(lr.data)) > s.maxRequest() {
		return logRecord{err: fitsync.Permanent(fmt.Errorf("the log record is %d bytes, more than max_request_bytes", len(lr.data)))}
	}
	return lr
}

// Write exports the batch in order, a request of up to max_request_bytes
// at a time. It returns nil once every record is exported, or a BatchError
// at the first one that is not: those before it are exported.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	recs := make([]logRecord, len(batch))
	for i, r := range batch {
		recs[i] = s.build(r)
	}
	limit := s.maxRequest()
	for i := 0; i < len(recs); {
		if recs[i].err != nil {
			return &fitsync.BatchError{Index: i, Err: recs[i].err}
		}
		j, size := i, int64(0)
		for j < len(recs) && recs[j].err == nil {
			if j > i && size+int64(len(recs[j].data)) > limit {
				break
			}
			size += int64(len(recs[j].data))
			j++
		}
		if bad, err := s.exportRun(ctx, recs[i:j]); err != nil {
			return &fitsync.BatchError{Index: i + bad, Err: err}
		}
		i = j
	}
	return nil
}

// exportRun exports a run; one the receiver refuses (400, 413) is split
// in two, each half exported, until the one record it refuses is found. It
// returns the index, in the run, of the record that failed, those before
// it exported.
func (s *Sink) exportRun(ctx context.Context, recs []logRecord) (int, error) {
	err := s.export(ctx, recs)
	if err == nil {
		return 0, nil
	}
	var se *httpconn.StatusError
	if !errors.As(err, &se) || se.Status != stdhttp.StatusBadRequest && se.Status != stdhttp.StatusRequestEntityTooLarge {
		return 0, err
	}
	if len(recs) == 1 {
		return 0, fitsync.Permanent(err)
	}
	h := len(recs) / 2
	if bad, err := s.exportRun(ctx, recs[:h]); err != nil {
		return bad, err
	}
	bad, err := s.exportRun(ctx, recs[h:])
	if err != nil {
		return h + bad, err
	}
	return 0, nil
}

// group returns recs by resource, the resources in the order they first
// come, each one's records in order.
func group(recs []logRecord) [][]*logRecord {
	var out [][]*logRecord
	at := map[string]int{}
	for i := range recs {
		r := &recs[i]
		k, ok := at[r.resource]
		if !ok {
			k = len(out)
			at[r.resource] = k
			out = append(out, nil)
		}
		out[k] = append(out[k], r)
	}
	return out
}

// export sends one request of recs.
func (s *Sink) export(ctx context.Context, recs []logRecord) error {
	var body []byte
	h := stdhttp.Header{}
	if s.Encoding == "json" {
		h.Set("Content-Type", "application/json")
		body = s.appendJSONRequest(nil, group(recs))
	} else {
		h.Set("Content-Type", "application/x-protobuf")
		body = s.appendProtoRequest(nil, group(recs))
	}
	if s.Compression == "gzip" {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = buf.Bytes()
		h.Set("Content-Encoding", "gzip")
	}
	resp, err := s.conn.DoRetry(ctx, &httpconn.Request{Method: stdhttp.MethodPost, Path: s.path, Header: h, Body: body}, 0)
	if err != nil {
		return err
	}
	if resp.Status/100 != 2 {
		return &httpconn.StatusError{Status: resp.Status, Body: string(resp.Body)}
	}
	if n, msg := partialSuccess(resp.Header.Get("Content-Type"), resp.Body); n > 0 {
		warnf("sync otlp_logs: the receiver rejected %d of %d log records: %s", n, len(recs), msg)
	}
	return nil
}

// partialSuccess reads an ExportLogsServiceResponse's partial success:
// the log records rejected and why.
func partialSuccess(contentType string, body []byte) (int64, string) {
	if len(body) == 0 {
		return 0, ""
	}
	if strings.HasPrefix(contentType, "application/json") {
		var r struct {
			PartialSuccess struct {
				Rejected json.Number `json:"rejectedLogRecords"`
				Message  string      `json:"errorMessage"`
			} `json:"partialSuccess"`
		}
		if json.Unmarshal(body, &r) != nil {
			return 0, ""
		}
		n, _ := r.PartialSuccess.Rejected.Int64()
		return n, r.PartialSuccess.Message
	}
	var n int64
	var msg string
	for len(body) > 0 {
		f, rest, err := pb.Next(body)
		if err != nil {
			return 0, ""
		}
		body = rest
		if f.Num != 1 || f.Wire != pb.Bytes {
			continue
		}
		for in := f.Bytes; len(in) > 0; {
			g, rest, err := pb.Next(in)
			if err != nil {
				return 0, ""
			}
			in = rest
			switch {
			case g.Num == 1 && g.Wire == pb.Varint:
				n = int64(g.Value)
			case g.Num == 2 && g.Wire == pb.Bytes:
				msg = string(g.Bytes)
			}
		}
	}
	return n, msg
}

// The protobuf of OTLP's logs: ExportLogsServiceRequest's resource_logs 1;
// ResourceLogs' resource 1 and scope_logs 2; Resource's attributes 1;
// ScopeLogs' scope 1 and log_records 2; InstrumentationScope's name 1 and
// version 2; LogRecord's time_unix_nano 1 (fixed64), severity_number 2,
// severity_text 3, body 5, attributes 6, trace_id 9, span_id 10,
// observed_time_unix_nano 11 (fixed64); KeyValue's key 1 and value 2;
// AnyValue's string 1, bool 2, int 3, double 4, array 5, kvlist 6, bytes
// 7; ArrayValue's and KeyValueList's values 1.

func (s *Sink) appendProtoRequest(b []byte, groups [][]*logRecord) []byte {
	for _, g := range groups {
		b = pb.AppendMessage(b, 1, func(b []byte) []byte {
			b = pb.AppendMessage(b, 1, func(b []byte) []byte {
				b = appendProtoKV(b, 1, "service.name", fitsync.String(s.ServiceName))
				b = appendProtoKV(b, 1, "service.version", fitsync.String(observe.Version))
				for k := 0; k+1 < len(g[0].rattrs); k += 2 {
					b = appendProtoKV(b, 1, g[0].rattrs[k], fitsync.String(g[0].rattrs[k+1]))
				}
				return b
			})
			return pb.AppendMessage(b, 2, func(b []byte) []byte {
				b = pb.AppendMessage(b, 1, func(b []byte) []byte {
					b = pb.AppendString(b, 1, "fit")
					return pb.AppendString(b, 2, observe.Version)
				})
				for _, r := range g {
					b = pb.AppendBytes(b, 2, r.data)
				}
				return b
			})
		})
	}
	return b
}

func appendProtoRecord(b []byte, ts time.Time, sevNum int64, sevText string, body fitsync.Value, attrs []string, traceID, spanID []byte) ([]byte, error) {
	if !ts.IsZero() {
		b = pb.AppendFixed64(b, 1, uint64(max(ts.UnixNano(), 0)))
	}
	b = pb.AppendInt(b, 2, sevNum)
	b = pb.AppendString(b, 3, sevText)
	var err error
	b = pb.AppendMessage(b, 5, func(b []byte) []byte {
		b, err = appendProtoAny(b, body)
		return b
	})
	if err != nil {
		return nil, err
	}
	for k := 0; k+1 < len(attrs); k += 2 {
		b = appendProtoKV(b, 6, attrs[k], fitsync.String(attrs[k+1]))
	}
	if traceID != nil {
		b = pb.AppendBytes(b, 9, traceID)
	}
	if spanID != nil {
		b = pb.AppendBytes(b, 10, spanID)
	}
	return pb.AppendFixed64(b, 11, uint64(time.Now().UnixNano())), nil
}

func appendProtoKV(b []byte, field int, key string, v fitsync.Value) []byte {
	return pb.AppendMessage(b, field, func(b []byte) []byte {
		b = pb.AppendString(b, 1, key)
		return pb.AppendMessage(b, 2, func(b []byte) []byte {
			b, _ = appendProtoAny(b, v) // a string, which does not fail
			return b
		})
	})
}

// appendProtoAny appends v as an AnyValue's member.
func appendProtoAny(b []byte, v fitsync.Value) ([]byte, error) {
	var err error
	switch v.Kind() {
	case fitsync.KindNull:
		return b, nil
	case fitsync.KindString:
		return pb.AppendBytes(b, 1, []byte(v.Text())), nil
	case fitsync.KindBool:
		return append(pb.AppendTag(b, 2, pb.Varint), boolByte(v.AsBool())), nil
	case fitsync.KindInt:
		return pb.AppendVarint(pb.AppendTag(b, 3, pb.Varint), uint64(v.AsInt())), nil
	case fitsync.KindUint:
		if u := v.AsUint(); u <= math.MaxInt64 {
			return pb.AppendVarint(pb.AppendTag(b, 3, pb.Varint), u), nil
		}
		return pb.AppendBytes(b, 1, []byte(fitsync.TextOf(v))), nil
	case fitsync.KindFloat:
		return binaryDouble(b, v.AsFloat()), nil
	case fitsync.KindBytes:
		return pb.AppendBytes(b, 7, []byte(v.Text())), nil
	case fitsync.KindArray:
		b = pb.AppendMessage(b, 5, func(b []byte) []byte {
			for _, e := range v.Elems() {
				b = pb.AppendMessage(b, 1, func(b []byte) []byte {
					if err == nil {
						b, err = appendProtoAny(b, e)
					}
					return b
				})
			}
			return b
		})
		return b, err
	case fitsync.KindMap:
		b = pb.AppendMessage(b, 6, func(b []byte) []byte {
			for _, f := range v.Fields() {
				b = pb.AppendMessage(b, 1, func(b []byte) []byte {
					b = pb.AppendString(b, 1, f.Name)
					return pb.AppendMessage(b, 2, func(b []byte) []byte {
						if err == nil {
							b, err = appendProtoAny(b, f.Value)
						}
						return b
					})
				})
			}
			return b
		})
		return b, err
	}
	// Decimals, times, dates and JSON as text.
	return pb.AppendBytes(b, 1, []byte(fitsync.TextOf(v))), nil
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// binaryDouble appends a double member, 0 included.
func binaryDouble(b []byte, f float64) []byte {
	b = pb.AppendTag(b, 4, pb.Fixed64)
	u := math.Float64bits(f)
	return append(b, byte(u), byte(u>>8), byte(u>>16), byte(u>>24), byte(u>>32), byte(u>>40), byte(u>>48), byte(u>>56))
}

// The JSON of OTLP's logs, as OTLP/HTTP's JSON encoding has it: field
// names in lowerCamelCase, 64-bit integers as strings, IDs in hex.

func (s *Sink) appendJSONRequest(b []byte, groups [][]*logRecord) []byte {
	b = append(b, `{"resourceLogs":[`...)
	for i, g := range groups {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"resource":{"attributes":[`...)
		b = appendJSONKV(b, "service.name", fitsync.String(s.ServiceName))
		b = append(b, ',')
		b = appendJSONKV(b, "service.version", fitsync.String(observe.Version))
		for k := 0; k+1 < len(g[0].rattrs); k += 2 {
			b = append(b, ',')
			b = appendJSONKV(b, g[0].rattrs[k], fitsync.String(g[0].rattrs[k+1]))
		}
		b = append(b, `]},"scopeLogs":[{"scope":{"name":"fit","version":`...)
		b = fitsync.AppendJSONString(b, observe.Version)
		b = append(b, `},"logRecords":[`...)
		for n, r := range g {
			if n > 0 {
				b = append(b, ',')
			}
			b = append(b, r.data...)
		}
		b = append(b, "]}]}"...)
	}
	return append(b, "]}"...)
}

func appendJSONRecord(b []byte, ts time.Time, sevNum int64, sevText string, body fitsync.Value, attrs []string, traceID, spanID []byte) ([]byte, error) {
	b = append(b, `{"observedTimeUnixNano":"`...)
	b = strconv.AppendInt(b, time.Now().UnixNano(), 10)
	b = append(b, '"')
	if !ts.IsZero() {
		b = append(b, `,"timeUnixNano":"`...)
		b = strconv.AppendInt(b, max(ts.UnixNano(), 0), 10)
		b = append(b, '"')
	}
	if sevNum != 0 {
		b = append(b, `,"severityNumber":`...)
		b = strconv.AppendInt(b, sevNum, 10)
	}
	if sevText != "" {
		b = append(b, `,"severityText":`...)
		b = fitsync.AppendJSONString(b, sevText)
	}
	b = append(b, `,"body":`...)
	var err error
	if b, err = appendJSONAny(b, body); err != nil {
		return nil, err
	}
	b = append(b, `,"attributes":[`...)
	for k := 0; k+1 < len(attrs); k += 2 {
		if k > 0 {
			b = append(b, ',')
		}
		b = appendJSONKV(b, attrs[k], fitsync.String(attrs[k+1]))
	}
	b = append(b, ']')
	if traceID != nil {
		b = append(b, `,"traceId":"`...)
		b = hex.AppendEncode(b, traceID)
		b = append(b, '"')
	}
	if spanID != nil {
		b = append(b, `,"spanId":"`...)
		b = hex.AppendEncode(b, spanID)
		b = append(b, '"')
	}
	return append(b, '}'), nil
}

func appendJSONKV(b []byte, key string, v fitsync.Value) []byte {
	b = append(b, `{"key":`...)
	b = fitsync.AppendJSONString(b, key)
	b = append(b, `,"value":`...)
	b, _ = appendJSONAny(b, v) // a string, which does not fail
	return append(b, '}')
}

// appendJSONAny appends v as an AnyValue in JSON.
func appendJSONAny(b []byte, v fitsync.Value) ([]byte, error) {
	var err error
	switch v.Kind() {
	case fitsync.KindNull:
		return append(b, "{}"...), nil
	case fitsync.KindString:
		b = append(b, `{"stringValue":`...)
		b = fitsync.AppendJSONString(b, v.Text())
	case fitsync.KindBool:
		b = append(b, `{"boolValue":`...)
		b = strconv.AppendBool(b, v.AsBool())
	case fitsync.KindInt:
		b = append(b, `{"intValue":"`...)
		b = strconv.AppendInt(b, v.AsInt(), 10)
		b = append(b, '"')
	case fitsync.KindUint:
		if u := v.AsUint(); u <= math.MaxInt64 {
			b = append(b, `{"intValue":"`...)
			b = strconv.AppendUint(b, u, 10)
			b = append(b, '"')
		} else {
			b = append(b, `{"stringValue":"`...)
			b = strconv.AppendUint(b, u, 10)
			b = append(b, '"')
		}
	case fitsync.KindFloat:
		f := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is not a JSON number", f)
		}
		b = append(b, `{"doubleValue":`...)
		b = strconv.AppendFloat(b, f, 'g', -1, 64)
	case fitsync.KindBytes:
		p, _ := fitsync.AppendPlain(nil, v) // base64, as OTLP's JSON has bytes
		b = append(b, `{"bytesValue":`...)
		b = append(b, p...)
	case fitsync.KindArray:
		b = append(b, `{"arrayValue":{"values":[`...)
		for i, e := range v.Elems() {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendJSONAny(b, e); err != nil {
				return nil, err
			}
		}
		b = append(b, "]}"...)
	case fitsync.KindMap:
		b = append(b, `{"kvlistValue":{"values":[`...)
		for i, f := range v.Fields() {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"key":`...)
			b = fitsync.AppendJSONString(b, f.Name)
			b = append(b, `,"value":`...)
			if b, err = appendJSONAny(b, f.Value); err != nil {
				return nil, err
			}
			b = append(b, '}')
		}
		b = append(b, "]}"...)
	default:
		b = append(b, `{"stringValue":`...)
		b = fitsync.AppendJSONString(b, fitsync.TextOf(v))
	}
	return append(b, '}'), nil
}
