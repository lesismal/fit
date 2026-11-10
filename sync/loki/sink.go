// Package syncloki is sync's Grafana Loki: the sink loki, which pushes
// records as log lines to Loki's push API (/loki/api/v1/push), as protobuf
// compressed with snappy or as JSON, through fib's asynchronous client, in
// streams whose labels come from templates of the record's configured
// fields only, so that a source cannot raise the streams' cardinality. See
// docs/sync.md.
package syncloki

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/tmpl"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".loki", New: func() any { return new(Sink) },
		Doc: "Pushes records to Grafana Loki (2.9 and 3) as log lines, with its push API: protobuf compressed with snappy, or JSON. Each record is a line its codec makes, at the time timestamp_field or its commit time says, in the stream its labels' templates make of the record's configured fields, so that the streams are as many as those fields' values; structured metadata (Loki 3) the same way. Requests are bounded by max_request_bytes, sent again with a backoff on 429 and 5xx; one Loki refuses (400, an entry too old or a line too long) is split until the record it refuses is found, which fails, on_error taking it, those before it pushed. Loki keeps one copy of an entry pushed again with the same stream, time and line, so a batch written again after a crash is stored once when its records have times."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 1 << 20
	pushPath               = "/loki/api/v1/push"
)

// Sink is the sink loki.
type Sink struct {
	URLs               []string          `json:"urls" fit:"required" doc:"Loki's HTTP address, as http://loki:3100, or a gateway's with the path before /loki (https://logs.example.com/tenant-a): /loki/api/v1/push goes after it. The one that answered last is used, and the next in turn when one fails to (a connection's error, a timeout, a 502, 503 or 504)."`
	Username           string            `json:"username,omitempty" doc:"The user for basic authentication, as a gateway asks for; none when left out."`
	PasswordEnv        string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	BearerTokenEnv     string            `json:"bearer_token_env,omitempty" doc:"The environment variable holding a token sent as Authorization: Bearer, in place of username."`
	CAFile             string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against over https, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	CertFile           string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile            string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName         string            `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout            config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Tenant             string            `json:"tenant,omitempty" doc:"The tenant the lines go to, sent as X-Scope-OrgID, for a Loki with auth_enabled; none when left out."`
	TenantField        string            `json:"tenant_field,omitempty" doc:"A field of the row whose value is a record's tenant, in place of tenant for the records that have it: a batch is pushed in a request per tenant."`
	Encoding           string            `json:"encoding,omitempty" enum:"protobuf,json" default:"\"protobuf\"" doc:"How a push is sent: protobuf, compressed with snappy, as Promtail and Alloy send it; json, Loki's JSON push. protobuf when left out."`
	Labels             map[string]string `json:"labels,omitempty" doc:"The stream's labels, the label's name to a template of its value: text with {name}, a field of the row (after, or before when after is null), and {$namespace}, {$op}, {$source} and {$key}, the record's; {{ is a brace. A label whose value comes out empty is left out. Name only fields of few values here: each set of values is a stream of Loki's. {\"job\": \"fit\", \"namespace\": \"{$namespace}\"} when left out."`
	StructuredMetadata map[string]string `json:"structured_metadata,omitempty" doc:"Structured metadata of each line, the name to a template as labels has them, for values of many kinds (a trace ID, a user) that are not to be labels: Loki 3 with allow_structured_metadata (the default from 3.0, on schema v13). Empty values are left out. None when left out."`
	Codec              *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a record's line: line when left out, which writes after's line field, or the record's canonical JSON when it has none; json writes the row as a JSON object, which Loki's json parser reads."`
	TimestampField     string            `json:"timestamp_field,omitempty" doc:"A field of the row whose value is the line's time: a time, an integer of Unix seconds, milliseconds, microseconds or nanoseconds, or RFC 3339 text. The record's commit time when left out or when a record lacks it, and the time of the write when it has neither, a nanosecond apart for each record of a batch."`
	MaxRequestBytes    int64             `json:"max_request_bytes,omitempty" min:"0" default:"1048576" doc:"The most bytes of the lines of one push, before compression; a batch larger is pushed in several, in order, and a line larger fails its record. Loki's own bound is its grpc_server_max_recv_msg_size, 4 MiB by default. 1 MiB when left out or 0."`

	client *fibrest.Client
	codec  fitsync.Codec
	labels []label
	meta   []label
}

// label is a label's or a metadata's name and template.
type label struct {
	name string
	tmpl *tmpl.Record
}

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Provision checks the params, loads the codec and reads the secrets and
// TLS files; nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	client, errs := fibrest.Connect(ctx, fibrest.Connection{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv, BearerTokenEnv: s.BearerTokenEnv,
		CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.client = client
	if s.Encoding == "" {
		s.Encoding = "protobuf"
	}
	labels := s.Labels
	if len(labels) == 0 {
		labels = map[string]string{"job": "fit", "namespace": "{$namespace}"}
	}
	parse := func(field string, m map[string]string) []label {
		var out []label
		for name, t := range m {
			if !labelName.MatchString(name) || strings.HasPrefix(name, "__") {
				errs = append(errs, ctx.Params.Field(field).Errorf("%q is not a label's name: letters, digits and _, not starting with a digit or __", name))
				continue
			}
			tp, err := tmpl.ParseRecord(t)
			if err != nil {
				errs = append(errs, ctx.Params.Field(field).Errorf("%s: %v", name, err))
				continue
			}
			out = append(out, label{name: name, tmpl: tp})
		}
		slices.SortFunc(out, func(a, b label) int { return cmp.Compare(a.name, b.name) })
		return out
	}
	s.labels = parse("labels", labels)
	s.meta = parse("structured_metadata", s.StructuredMetadata)
	if strings.ContainsAny(s.Tenant, "\r\n|") {
		errs = append(errs, ctx.Params.Field("tenant").Errorf("a tenant has no line breaks or |"))
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

// Test pushes a stream without entries, as the tenant, with the sink's
// credentials: Loki (or a gateway before it) answers, takes them and the
// encoding, and stores nothing. Credentials that may push but not query,
// as a hosted Loki's write-only tokens, pass. (A push without streams is
// refused, 422.)
func (s *Sink) Test(ctx context.Context) error {
	return s.send(ctx, new(scratch), s.Tenant, []stream{{labels: `{job="fit"}`, pairs: []string{"job", "fit"}}}, nil)
}

// Cleanup closes the client and lets the codec go.
func (s *Sink) Cleanup() {
	if s.client != nil {
		s.client.Close()
	}
	module.Cleanup(s.codec)
}

func (s *Sink) maxRequest() int64 {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return s.MaxRequestBytes
}
