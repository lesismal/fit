package syncnats

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	natswire "github.com/lesismal/fit/sync/nats/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".nats", New: func() any { return new(Sink) },
		Doc: "Publishes records to NATS JetStream: each a message to the subject its namespace names (or subject's template), its data what the codec makes of it (raw when none is named), with a Nats-Msg-Id made from the record's origin (a Kafka offset, a Pulsar message ID, a stream sequence, a CDC transaction and place) or, without one, its position, and the record's op, namespace and key in the Fit-Op, Fit-Namespace and Fit-Key headers (as RabbitMQ's fit-op, fit-namespace and fit-key; the key's strings and bytes as they are, anything else as plain JSON), so that a batch written again within the stream's duplicate window (2 minutes by default) is stored once; a batch's messages are published together, max_in_flight at a time, and acknowledged by the stream before the batch is. With mode core, published to core NATS, confirmed only as far as the server reading them: at most once."})
}

// The defaults of the sink's params.
const (
	DefaultMaxInFlight = 256
	defaultSubject     = "{namespace}"
)

// Sink is the sink nats.
type Sink struct {
	Servers     []string          `json:"servers" fit:"required" doc:"The servers, host:port each; the others of the cluster are learnt from them, and the client connects to another when one fails."`
	Name        string            `json:"name,omitempty" default:"\"fit-sync\"" doc:"The connection's name, in the servers' monitoring. fit-sync when left out."`
	TLS         bool              `json:"tls,omitempty" doc:"Connect over TLS, the servers' certificates checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile      string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the servers' are checked against, in place of the system's roots; needs tls."`
	CertFile    string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, for servers that verify clients; needs tls and key_file."`
	KeyFile     string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName  string            `json:"server_name,omitempty" doc:"The name the servers' certificates must have; each server's host when left out."`
	TokenEnv    string            `json:"token_env,omitempty" doc:"The environment variable holding a token to authenticate with, read when the pipeline loads."`
	Username    string            `json:"username,omitempty" doc:"The user to authenticate as, with password_env."`
	PasswordEnv string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	NKeyFile    string            `json:"nkey_file,omitempty" doc:"A file holding a user's NKey seed (SU...), whose key signs the server's nonce."`
	CredsFile   string            `json:"creds_file,omitempty" doc:"A creds file, as nsc writes it, holding a user's JWT and NKey seed, for servers in operator mode."`
	Timeout     config.Duration   `json:"timeout,omitempty" default:"\"10s\"" doc:"How long connecting, and each publish's acknowledgement, may take. 10s when left out or 0."`
	Mode        string            `json:"mode,omitempty" enum:"jetstream,core" default:"\"jetstream\"" doc:"jetstream, each message acknowledged by the stream that takes its subject, deduplicated by its Nats-Msg-Id; core, published to core NATS, at most once. jetstream when left out."`
	Subject     string            `json:"subject,omitempty" default:"\"{namespace}\"" doc:"The subject a record goes to: text with {namespace}, the record's namespace (a route transform can map namespaces to subjects first). A record whose subject is empty or holds a wildcard fails. {namespace} when left out."`
	Codec       *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a message's data of a record: raw when left out, after's value field as it is, or the record's canonical JSON when it has none; json for the row as plain JSON (form plain) or the whole record (form record)."`
	MaxInFlight int               `json:"max_in_flight,omitempty" min:"0" max:"100000" default:"256" doc:"With mode jetstream, the most publishes waiting for the stream's acknowledgement at once. 256 when left out or 0."`

	cfg     natswire.Config
	timeout time.Duration
	codec   fitsync.Codec
	cl      holder
}

// Provision checks the params and reads the secrets and TLS files;
// nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	cfg, t, errs := wireConfig(ctx, connParams{Servers: s.Servers, Name: s.Name, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, TokenEnv: s.TokenEnv, Username: s.Username, PasswordEnv: s.PasswordEnv, NKeyFile: s.NKeyFile, CredsFile: s.CredsFile, Timeout: s.Timeout})
	s.cfg, s.timeout = cfg, t
	if s.Subject != "" {
		if rest := strings.ReplaceAll(s.Subject, defaultSubject, "x"); strings.ContainsAny(rest, "{}") || !validSubject(rest) {
			errs = append(errs, ctx.Params.Field("subject").Errorf("%q is not a subject, or text with {namespace}", s.Subject))
		}
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Cleanup closes the client and lets the codec go.
func (s *Sink) Cleanup() {
	s.cl.close()
	module.Cleanup(s.codec)
}

// Test connects, and has the server answer a PING.
func (s *Sink) Test(ctx context.Context) error {
	h := holder{cfg: s.cfg}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	c, err := h.get(ctx)
	if err != nil {
		return err
	}
	defer h.close()
	return c.Flush(ctx)
}

func (s *Sink) subjectOf(r *fitsync.Record) string {
	if s.Subject == "" {
		return r.Namespace
	}
	return strings.ReplaceAll(s.Subject, defaultSubject, r.Namespace)
}

// message makes r a message: its subject, headers and data. The headers
// are Fit-Op, Fit-Namespace and, when r has a key, Fit-Key, as the
// RabbitMQ sink writes them; a value a header line cannot hold as it is
// (a CR or LF, or space at either end) is left out.
func (s *Sink) message(r *fitsync.Record) (string, natswire.Header, []byte, error) {
	subj := s.subjectOf(r)
	if !validSubject(subj) {
		return "", nil, nil, fitsync.Permanent(fmt.Errorf("nats: %q is not a subject to publish to", subj))
	}
	data, err := s.codec.Encode(nil, []*fitsync.Record{r})
	if err != nil {
		return "", nil, nil, fitsync.Permanent(fmt.Errorf("nats: the codec: %w", err))
	}
	h := natswire.Header{opHeader: {r.Op.String()}}
	if headerSafe(r.Namespace) {
		h[namespaceHeader] = []string{r.Namespace}
	}
	if k, ok := keyText(r.Key); ok && headerSafe(k) {
		h[keyHeader] = []string{k}
	}
	return subj, h, bytes.TrimSuffix(data, []byte{'\n'}), nil
}

// keyText is a key as the Fit-Key header has it, the same as RabbitMQ's
// fit-key: strings and bytes as they are, anything else as plain JSON.
func keyText(k fitsync.Value) (string, bool) {
	switch k.Kind() {
	case fitsync.KindNull:
		return "", false
	case fitsync.KindString, fitsync.KindBytes:
		return k.Text(), true
	}
	var plain fitsync.JSONCodec
	b, _ := plain.Encode(nil, []*fitsync.Record{{After: k}}) // a key's values all encode
	return string(bytes.TrimSuffix(b, []byte{'\n'})), true
}

// headerSafe reports whether v is read back the same from a header line:
// no line breaks, and nothing the reader trims.
func headerSafe(v string) bool {
	return !strings.ContainsAny(v, "\r\n") && strings.TrimSpace(v) == v
}

// Write publishes the batch, and returns once the stream has acknowledged
// each message, or, in core, the server has read them.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	s.cl.cfg = s.cfg
	dctx, cancel := context.WithTimeout(ctx, s.timeout)
	c, err := s.cl.get(dctx)
	cancel()
	if err != nil {
		return err
	}
	if s.Mode == "core" {
		return s.writeCore(ctx, c, batch)
	}
	ids := fitsync.RecordIDs(batch)
	most := orDefault(s.MaxInFlight, DefaultMaxInFlight)
	type inFlight struct {
		index int
		pa    *natswire.PendingAck
	}
	var flight []inFlight
	defer func() {
		for _, f := range flight {
			f.pa.Cancel()
		}
	}()
	// wait waits for the oldest publish's acknowledgement.
	wait := func() error {
		f := flight[0]
		flight = flight[1:]
		wctx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		if _, err := f.pa.Wait(wctx); err != nil {
			return &fitsync.BatchError{Index: f.index, Err: err}
		}
		return nil
	}
	for i, r := range batch {
		subj, h, data, err := s.message(r)
		if err == nil {
			h["Nats-Msg-Id"] = []string{ids[i]}
			var pa *natswire.PendingAck
			if pa, err = c.PublishJS(subj, h, data); err == nil {
				flight = append(flight, inFlight{i, pa})
			} else if errors.Is(err, natswire.ErrMaxPayload) {
				err = fitsync.Permanent(err)
			}
		}
		if err != nil {
			// Those before it are written once acknowledged.
			for len(flight) > 0 {
				if werr := wait(); werr != nil {
					return werr
				}
			}
			return &fitsync.BatchError{Index: i, Err: err}
		}
		if len(flight) >= most {
			if err := wait(); err != nil {
				return err
			}
		}
	}
	for len(flight) > 0 {
		if err := wait(); err != nil {
			return err
		}
	}
	return nil
}

// writeCore publishes the batch to core NATS and waits for the server's
// PONG after it.
func (s *Sink) writeCore(ctx context.Context, c *natswire.Client, batch []*fitsync.Record) error {
	for i, r := range batch {
		subj, h, data, err := s.message(r)
		if err == nil {
			if err = c.Publish(subj, "", h, data); errors.Is(err, natswire.ErrMaxPayload) {
				err = fitsync.Permanent(err)
			}
		}
		if err != nil {
			if ferr := s.flush(ctx, c); ferr != nil {
				return ferr
			}
			return &fitsync.BatchError{Index: i, Err: err}
		}
	}
	return s.flush(ctx, c)
}

func (s *Sink) flush(ctx context.Context, c *natswire.Client) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return c.Flush(ctx)
}
