package syncs3

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".s3", New: func() any { return new(Source) },
		Doc: "Reads the objects under a prefix of an S3 or compatible bucket in key order, each as lines through a codec, gunzipping those whose key ends in .gz, in ranges of range_bytes; then, in a stream, lists again every poll_interval for keys past the last. Its position is the object's key and the offset past the line, so a restart resumes inside an object. Keys must grow as objects are added, as the s3 sink's do: an object added under a key before the last read is not seen, and an object is read once."})
}

// The defaults of the source's params.
const (
	DefaultPollInterval = 30 * time.Second
	DefaultRangeBytes   = 8 << 20
	DefaultMaxLineBytes = 1 << 20
	DefaultListPage     = 1000
)

// Source is the source s3.
type Source struct {
	Endpoint           string            `json:"endpoint,omitempty" doc:"The server, as https://minio.internal:9000 or https://s3.eu-west-1.amazonaws.com, with no path: an S3 compatible's address, or AWS's endpoint of region. https://s3.<region>.amazonaws.com when left out."`
	Region             string            `json:"region,omitempty" default:"\"us-east-1\"" doc:"The region requests are signed for: the bucket's on AWS, and what the server is set to elsewhere (MinIO's and most compatibles' us-east-1, R2's auto). us-east-1 when left out."`
	Bucket             string            `json:"bucket" fit:"required" doc:"The bucket. It is not made: it is the user's."`
	Addressing         string            `json:"addressing,omitempty" enum:"path,virtual" doc:"How the bucket is named in a request: path, as https://endpoint/bucket/key, which every compatible takes; virtual, as https://bucket.endpoint/key, as AWS prefers. virtual when endpoint is left out (AWS), path otherwise."`
	AccessKeyID        string            `json:"access_key_id,omitempty" doc:"The access key's ID, sent in each request's signature; with secret_access_key_env. Requests are not signed when both are left out, for a public bucket."`
	SecretAccessKeyEnv string            `json:"secret_access_key_env,omitempty" doc:"The environment variable holding access_key_id's secret, read when the pipeline loads."`
	SessionTokenEnv    string            `json:"session_token_env,omitempty" doc:"The environment variable holding a session token, for temporary credentials, sent as x-amz-security-token; none when left out. Temporary credentials are read once: they are not renewed while the pipeline runs."`
	CAFile             string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	ServerName         string            `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; the endpoint's host when left out."`
	Timeout            config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Prefix             string            `json:"prefix,omitempty" doc:"The objects read are those whose keys begin with it, as logs/2026/; every object of the bucket when left out. Keys ending in / (folders' markers) are skipped."`
	StartAfter         string            `json:"start_after,omitempty" doc:"Where a pipeline without a checkpoint starts: the objects whose keys come after it. The first object under prefix when left out."`
	Codec              *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a line: line when left out, a message record holding it as after's line; json reads it as JSON, csv as comma-separated values. A line the codec fails on is a message record of the line and the error."`
	Decompress         string            `json:"decompress,omitempty" enum:"auto,gzip,none" default:"\"auto\"" doc:"Which objects are gunzipped: auto, those whose keys end in .gz; gzip, every one; none, none. A gzipped object resumed after a restart is read again from its start, its lines up to the offset skipped. auto when left out."`
	Namespace          string            `json:"namespace,omitempty" doc:"The records' namespace. The object's key when left out."`
	PollInterval       config.Duration   `json:"poll_interval,omitempty" default:"\"30s\"" doc:"How long to wait, once every object listed is read, before listing again for keys past the last. 30s when left out or 0."`
	RangeBytes         int64             `json:"range_bytes,omitempty" min:"0" max:"67108864" default:"8388608" doc:"How many bytes of an object one request reads; an object is read in such ranges, so it never needs to be held whole. 8 MiB when left out or 0."`
	MaxLineBytes       int               `json:"max_line_bytes,omitempty" min:"0" max:"67108864" default:"1048576" doc:"The longest line, in bytes: a longer one is cut there, its rest skipped, and counted. 1 MiB when left out or 0."`

	conn  *Conn
	codec fitsync.Codec
	cut   atomic.Int64
}

// position is the source's: the object's key and the offset (of its bytes,
// gunzipped) past the last line read, and end once it is read to its end.
type position struct {
	Key    string `json:"key"`
	Offset int64  `json:"offset"`
	End    bool   `json:"end,omitempty"`
}

// Provision checks the params, reads the secrets and loads the codec;
// nothing is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	conn, errs := NewConn(ctx, ConnParams{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Addressing: s.Addressing,
		AccessKeyID: s.AccessKeyID, SecretAccessKeyEnv: s.SecretAccessKeyEnv, SessionTokenEnv: s.SessionTokenEnv,
		CAFile: s.CAFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn = conn
	if len(errs) > 0 {
		return errs
	}
	c, err := loadCodec(ctx, s.Codec)
	s.codec = c
	return err
}

// Test lists one key under prefix, as the source lists them: the endpoint
// answers, the bucket is there, and the credentials may list it.
func (s *Source) Test(ctx context.Context) error {
	_, _, err := s.conn.List(ctx, s.Prefix, "", "", 1)
	return err
}

// Cleanup closes the client and lets the codec go.
func (s *Source) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
	module.Cleanup(s.codec)
}

// LinesCut returns how many lines were cut at max_line_bytes.
func (s *Source) LinesCut() int64 { return s.cut.Load() }

func (s *Source) pollInterval() time.Duration {
	if s.PollInterval <= 0 {
		return DefaultPollInterval
	}
	return s.PollInterval.Std()
}

func (s *Source) rangeBytes() int64 {
	if s.RangeBytes <= 0 {
		return DefaultRangeBytes
	}
	return s.RangeBytes
}

func (s *Source) maxLine() int {
	if s.MaxLineBytes <= 0 {
		return DefaultMaxLineBytes
	}
	return s.MaxLineBytes
}

func (s *Source) gzipped(key string) bool {
	switch s.Decompress {
	case "gzip":
		return true
	case "none":
		return false
	}
	return strings.HasSuffix(key, ".gz")
}

// Run reads the objects past the checkpoint, the one it is inside first,
// and, in a stream, lists again for more.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	last := s.StartAfter
	if r.From != nil {
		var p position
		if err := json.Unmarshal(r.From, &p); err != nil || p.Key == "" {
			return fmt.Errorf("s3: the checkpoint %s is not the source's position", r.From)
		}
		last = p.Key
		if !p.End {
			if err := s.read(ctx, r, Object{Key: p.Key}, p.Offset); err != nil {
				return err
			}
		}
	}
	for {
		token := ""
		for {
			objs, next, err := s.conn.List(ctx, s.Prefix, last, token, DefaultListPage)
			if err != nil {
				return fmt.Errorf("s3: listing %s: %w", s.Prefix, err)
			}
			for _, o := range objs {
				if o.Key <= last {
					continue // a server that ignores start-after
				}
				if !strings.HasSuffix(o.Key, "/") {
					if err := s.read(ctx, r, o, 0); err != nil {
						return err
					}
				}
				last = o.Key
			}
			if next == "" {
				break
			}
			token = next
		}
		if r.Once {
			return nil
		}
		t := time.NewTimer(s.pollInterval())
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// ranges reads an object's bytes from off in ranges, as an io.Reader,
// while its ETag stays the one first seen.
type ranges struct {
	ctx   context.Context
	conn  *Conn
	key   string
	etag  string
	off   int64
	size  int64 // -1 until the first answer says
	n     int64
	buf   []byte
	mod   time.Time
	first bool
	gone  bool
}

func (g *ranges) Read(p []byte) (int, error) {
	for len(g.buf) == 0 {
		if g.size >= 0 && g.off >= g.size {
			return 0, io.EOF
		}
		part, err := g.conn.Get(g.ctx, g.key, g.etag, g.off, g.n)
		if err != nil {
			var e *Error
			if g.first && errors.As(err, &e) && e.Status == stdhttp.StatusNotFound {
				g.gone = true
				return 0, io.EOF
			}
			return 0, err
		}
		if g.first {
			g.first = false
			if g.mod.IsZero() {
				g.mod = part.LastModified
			}
			if g.etag == "" {
				g.etag = part.ETag
			}
		}
		g.size = part.Size
		if len(part.Data) == 0 {
			if g.off < g.size {
				return 0, fmt.Errorf("the range at %d of %d is empty", g.off, g.size)
			}
			return 0, io.EOF
		}
		g.buf = part.Data
		g.off += int64(len(part.Data))
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

// read reads object o from off, its offset in the object's bytes (gunzipped).
func (s *Source) read(ctx context.Context, r *fitsync.Run, o Object, off int64) error {
	gz := s.gzipped(o.Key)
	g := &ranges{ctx: ctx, conn: s.conn, key: o.Key, etag: o.ETag, size: -1, n: s.rangeBytes(), first: true, mod: o.LastModified}
	if !gz {
		g.off = off
	}
	var src io.Reader = g
	if gz {
		zr, err := gzip.NewReader(g)
		if err == io.EOF {
			// Empty, or gone.
			s.warnGone(g)
			return nil
		}
		if err != nil {
			return fmt.Errorf("s3: %s: %w", o.Key, err)
		}
		if _, err := io.CopyN(io.Discard, zr, off); err != nil {
			return fmt.Errorf("s3: %s: skipping to %d: %w", o.Key, off, err)
		}
		src = zr
	}
	ns := s.Namespace
	if ns == "" {
		ns = o.Key
	}
	lr := &lineReader{rd: bufio.NewReaderSize(src, 64<<10), max: s.maxLine(), off: off}
	var held *fitsync.Record
	for {
		line, start, cut, err := lr.next()
		if err != nil && err != io.EOF {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("s3: reading %s: %w", o.Key, err)
		}
		if line == nil {
			break
		}
		off = lr.off
		if cut {
			s.cut.Add(1)
			logs.Named(config.LoggerSync).Warnf("sync s3: a line of %s at %d is longer than %d bytes: cut", o.Key, start, s.maxLine())
		}
		var recs []*fitsync.Record
		if derr := s.codec.Decode(line, func(rec *fitsync.Record) { recs = append(recs, rec) }); derr != nil {
			recs = []*fitsync.Record{{Op: fitsync.OpMessage, After: fitsync.Map(
				fitsync.Field{Name: "line", Value: fitsync.String(string(line))},
				fitsync.Field{Name: "error", Value: fitsync.String(derr.Error())})}}
		}
		for i, rec := range recs {
			rec.Source, rec.Namespace, rec.Position, rec.Tx, rec.CommitTime = "s3", ns, nil, fitsync.Tx{}, g.mod
			if rec.Key.IsNull() {
				fs := []fitsync.Field{{Name: "object", Value: fitsync.String(o.Key)}, {Name: "offset", Value: fitsync.Int(start)}}
				if i > 0 {
					fs = append(fs, fitsync.Field{Name: "n", Value: fitsync.Int(int64(i))})
				}
				rec.Key = fitsync.Map(fs...)
			}
			if held != nil {
				if err := r.Emit(held); err != nil {
					return err
				}
			}
			held = rec
		}
		if len(recs) > 0 {
			held.Position, _ = json.Marshal(position{Key: o.Key, Offset: off})
		}
		if err == io.EOF {
			break
		}
	}
	s.warnGone(g)
	if held != nil {
		held.Position, _ = json.Marshal(position{Key: o.Key, Offset: off, End: true})
		return r.Emit(held)
	}
	return nil
}

// warnGone logs an object deleted between its listing and its reading.
func (s *Source) warnGone(g *ranges) {
	if g.gone {
		logs.Named(config.LoggerSync).Warnf("sync s3: %s is gone, skipped", g.key)
	}
}

// lineReader reads lines, cutting those past max.
type lineReader struct {
	rd  *bufio.Reader
	max int
	off int64 // past the last line returned
	buf []byte
}

// next returns the next line without its newline and a carriage return
// before it (a last one without a newline too), where it starts, and
// whether it was cut; nil at the end. The line is valid until the next
// call. err is io.EOF with the last line, or with none.
func (l *lineReader) next() ([]byte, int64, bool, error) {
	start := l.off
	if l.buf == nil {
		l.buf = make([]byte, 0, 256)
	}
	l.buf = l.buf[:0]
	total := 0
	for {
		chunk, err := l.rd.ReadSlice('\n')
		l.off += int64(len(chunk))
		content := chunk
		if err == nil {
			content = chunk[:len(chunk)-1]
		}
		total += len(content)
		if room := l.max - len(l.buf); room > 0 {
			l.buf = append(l.buf, content[:min(len(content), room)]...)
		}
		switch err {
		case nil:
			return bytes.TrimSuffix(l.buf, []byte{'\r'}), start, total > l.max, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if l.off == start {
				return nil, start, false, io.EOF
			}
			return bytes.TrimSuffix(l.buf, []byte{'\r'}), start, total > l.max, io.EOF
		default:
			return nil, start, false, err
		}
	}
}
