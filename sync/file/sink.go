package syncfile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// chunkBytes is about the most one write to the file holds; a write is
// whole lines, so that the file rotates between lines.
const chunkBytes = 64 << 10

// Sink is the sink file.
type Sink struct {
	Path     string            `json:"path" fit:"required" doc:"The file the lines are written to, appended to when it is there. One sink writes a path: neither another file sink nor fit's log or access log may write it."`
	Codec    *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a line of a record: line when left out, which writes after's line field, or the record's canonical JSON when it has none; json writes it as JSON, csv as comma-separated values."`
	Template string            `json:"template,omitempty" doc:"A line made of text and placeholders, in place of codec: {name} is the row's field name (after, or before when after is null) as text, and {$namespace}, {$op}, {$key}, {$commit_time} and {$record} (its canonical JSON) are the record's; {{ is a brace. A newline ends each line."`
	Rotate   *config.Rotate    `json:"rotate,omitempty" doc:"Rotates and keeps the file as a log file is: by size and time, removing old files by age, count and total size, and compressing them. The file grows without bound when left out."`
	TimeZone string            `json:"time_zone,omitempty" doc:"The zone rotate's interval turns in: local (left out), utc, or an IANA name such as Asia/Shanghai."`

	codec fitsync.Codec
	tmpl  []part
	zone  *time.Location

	mu    sync.Mutex
	file  *logs.File
	files *logs.Files
}

// part is a template's text, or a placeholder.
type part struct {
	text  string
	field string // a row's field, or $ and the record's part
}

// The record's parts a template may name.
var recordParts = map[string]bool{"$namespace": true, "$op": true, "$key": true, "$commit_time": true, "$record": true}

// Provision checks the params and loads the codec; the file is opened at
// the first write, so that fit validate opens none.
func (s *Sink) Provision(ctx *module.Context) error {
	if strings.TrimSpace(s.Path) == "" {
		return ctx.Errorf("path", "the path is empty")
	}
	if err := config.CheckRotate(s.Rotate, ctx.Params.Field("rotate")); err != nil {
		return err
	}
	zone, err := config.LoadTimeZone(s.TimeZone)
	if err != nil {
		return ctx.Errorf("time_zone", "%v", err)
	}
	s.zone = zone
	if s.Template != "" {
		if s.Codec != nil {
			return ctx.Errorf("template", "a template and a codec cannot both make the lines")
		}
		if s.tmpl, err = parseTemplate(s.Template); err != nil {
			return ctx.Errorf("template", "%v", err)
		}
		return nil
	}
	s.codec, err = loadCodec(ctx, s.Codec)
	return err
}

func parseTemplate(t string) ([]part, error) {
	var parts []part
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
			if name == "" || strings.HasPrefix(name, "$") && !recordParts[name] {
				return nil, fmt.Errorf("{%s} names nothing: a field, or one of $namespace, $op, $key, $commit_time and $record", name)
			}
			parts = append(parts, part{text: text.String()}, part{field: name})
			text.Reset()
			i += j
		default:
			text.WriteByte(t[i])
		}
	}
	return append(parts, part{text: text.String()}), nil
}

// appendTemplate appends r as the template makes it, and a newline.
func (s *Sink) appendTemplate(b []byte, r *fitsync.Record) ([]byte, error) {
	row := r.After
	if row.IsNull() {
		row = r.Before
	}
	for _, p := range s.tmpl {
		var err error
		switch p.field {
		case "":
			b = append(b, p.text...)
		case "$namespace":
			b = append(b, r.Namespace...)
		case "$op":
			b = append(b, r.Op.String()...)
		case "$key":
			b = append(b, fitsync.TextOf(r.Key)...)
		case "$commit_time":
			if !r.CommitTime.IsZero() {
				b = r.CommitTime.AppendFormat(b, time.RFC3339Nano)
			}
		case "$record":
			b, err = r.AppendJSON(b)
		default:
			v, _ := row.Get(p.field)
			b = append(b, fitsync.TextOf(v)...)
		}
		if err != nil {
			return b, err
		}
	}
	return append(b, '\n'), nil
}

// The paths the file sinks write, one sink each.
var (
	pathsMu sync.Mutex
	paths   = map[string]*Sink{}
)

// open opens the file at the first write, through the process's log files.
func (s *Sink) open() (*logs.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		return s.file, nil
	}
	key := filepath.Clean(s.Path)
	pathsMu.Lock()
	defer pathsMu.Unlock()
	if o := paths[key]; o != nil && o != s {
		return nil, fmt.Errorf("file: another sync file sink writes %s", s.Path)
	}
	files := logs.DefaultFiles()
	if files.File(key) != nil {
		return nil, fmt.Errorf("file: fit's log writes %s: a sync file sink needs a file of its own", s.Path)
	}
	f, err := files.Open(key, s.Rotate, func() *time.Location { return s.zone })
	if err != nil {
		return nil, err
	}
	paths[key] = s
	s.file, s.files = f, files
	return f, nil
}

// Write appends the batch's lines and returns once they are in the file,
// synced.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	f, err := s.open()
	if err != nil {
		return err
	}
	var chunks [][]byte
	var cur []byte
	var failed *fitsync.BatchError
	for i, r := range batch {
		before := cur
		if s.tmpl != nil {
			cur, err = s.appendTemplate(cur, r)
		} else {
			cur, err = s.codec.Encode(cur, batch[i:i+1])
		}
		if err != nil {
			// What came before it is written, and it is the one that failed.
			cur, failed = before, &fitsync.BatchError{Index: i, Err: err}
			break
		}
		if len(cur) >= chunkBytes {
			chunks, cur = append(chunks, cur), nil
		}
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	if err := f.WriteSync(ctx, chunks); err != nil {
		return err
	}
	if failed != nil {
		return failed
	}
	return nil
}

// Cleanup closes the file, once what was written is in it, and lets the
// codec go.
func (s *Sink) Cleanup() {
	s.mu.Lock()
	f, files := s.file, s.files
	s.file = nil
	s.mu.Unlock()
	if f != nil {
		pathsMu.Lock()
		delete(paths, filepath.Clean(s.Path))
		pathsMu.Unlock()
		if err := files.Release(f); err != nil {
			logs.Named(config.LoggerSync).Warnf("sync file: closing %s: %v", s.Path, err)
		}
	}
	module.Cleanup(s.codec)
}
