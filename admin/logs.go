package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// The Admin API's endpoints for the workbench's Logs page:
//
//	GET  /logs          every file output of fit's own log and the access
//	                    log, with its loggers, format, time zone, rotation
//	                    and the files on disk
//	POST /logs/rotate   rotate one output now: {"path"}
//	POST /logs/cleanup  remove what retention says, or report it with
//	                    dry_run: {"path", "dry_run"}
type logsBody struct {
	Outputs []logOutputBody `json:"outputs"`
}

// logOutputBody is one file output.
type logOutputBody struct {
	Path        string         `json:"path"`
	Loggers     []string       `json:"loggers"`
	AccessLog   bool           `json:"access_log,omitempty"` // the access log's file, not one of log's
	Format      string         `json:"format"`
	TimeZone    string         `json:"time_zone"`
	Rotate      *logRotateBody `json:"rotate"`
	Current     string         `json:"current"`
	CurrentSize int64          `json:"current_size"`
	Dropped     int64          `json:"dropped"`
	LastError   string         `json:"last_error,omitempty"`
	Files       []logFileBody  `json:"files"`
	TotalBytes  int64          `json:"total_bytes"`
}

// logRotateBody is an output's rotation as it applies, its defaults filled in.
type logRotateBody struct {
	MaxBytes        int64 `json:"max_bytes"`
	IntervalSeconds int64 `json:"interval_seconds"`
	MaxAgeSeconds   int64 `json:"max_age_seconds"`
	MaxFiles        int   `json:"max_files"`
	MaxTotalBytes   int64 `json:"max_total_bytes"`
	Compress        bool  `json:"compress"`
}

// logFileBody is one file of an output: the one being written, or a rotated
// one.
type logFileBody struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Current  bool      `json:"current"`
}

// logSink is what a sink says of itself, by cleaned path.
type logSink struct {
	files       []logs.FileInfo
	current     string
	currentSize int64
	dropped     int64
	lastErr     string
}

// logs answers GET /logs.
func (s *Server) logs(w http.ResponseWriter, _ *http.Request) {
	cfg := s.configs.CurrentConfig()
	if cfg == nil {
		writeJSON(w, http.StatusOK, logsBody{})
		return
	}
	writeJSON(w, http.StatusOK, s.configs.logsInfo(cfg))
}

// logsInfo returns the file outputs of cfg and the files on disk.
func (cs *Configs) logsInfo(cfg *config.Config) logsBody {
	sinks := map[string]logSink{}
	if log := cs.env.Log; log != nil {
		for _, info := range log.Files().List() {
			sinks[filepath.Clean(info.Path)] = logSink{
				files: info.Files, current: info.Current, currentSize: info.CurrentSize,
				dropped: info.Dropped, lastErr: info.LastError,
			}
		}
	}
	var body logsBody
	add := func(path string, loggers []string, format, zone string, rot *config.Rotate) {
		sink := sinks[filepath.Clean(path)]
		out := logOutputBody{
			Path: path, Loggers: loggers, Format: format, TimeZone: zone, Rotate: logRotateOf(rot),
			Current: sink.current, CurrentSize: sink.currentSize, Dropped: sink.dropped, LastError: sink.lastErr,
		}
		out.Files = make([]logFileBody, 0, len(sink.files))
		for _, f := range sink.files {
			out.Files = append(out.Files, logFileBody{Name: f.Name, Size: f.Size, Modified: f.Modified, Current: f.Current})
			out.TotalBytes += f.Size
		}
		body.Outputs = append(body.Outputs, out)
	}
	for _, out := range cfg.Log.FileOutputs() {
		format := ""
		for _, name := range out.Loggers {
			f := cfg.Log.FormatOf(name)
			if format == "" {
				format = f
			} else if format != f {
				format = "mixed"
			}
		}
		add(out.Path, out.Loggers, format, cfg.Log.TimeZoneName(), out.Rotate)
	}
	if a := cfg.AccessLog; a != nil && config.IsFileOutput(a.Output) {
		add(a.Output, []string{config.LoggerAccessLog}, a.Format, a.TimeZoneName(), a.Rotate)
		body.Outputs[len(body.Outputs)-1].AccessLog = true
	}
	return body
}

// logRotateOf returns the rotation as it applies, or nil when there is none.
func logRotateOf(r *config.Rotate) *logRotateBody {
	if r == nil {
		return nil
	}
	return &logRotateBody{
		MaxBytes:        r.MaxBytesOrDefault(),
		IntervalSeconds: int64(r.IntervalOrDefault() / time.Second),
		MaxAgeSeconds:   int64(r.MaxAgeOrDefault() / time.Second),
		MaxFiles:        r.MaxFilesOrDefault(),
		MaxTotalBytes:   r.MaxTotalBytesOrDefault(),
		Compress:        r.CompressOrDefault(),
	}
}

// logRequest is the body of POST /logs/rotate and POST /logs/cleanup.
type logRequest struct {
	Path   string `json:"path"`
	DryRun bool   `json:"dry_run"`
}

// readLogRequest reads the body both log operations take.
func (s *Server) readLogRequest(w http.ResponseWriter, r *http.Request) (logRequest, bool) {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return logRequest{}, false
	}
	var req logRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return logRequest{}, false
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "name the log file's path")
		return logRequest{}, false
	}
	return req, true
}

// logsRotate answers POST /logs/rotate: the file at path turns now.
func (s *Server) logsRotate(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readLogRequest(w, r)
	if !ok {
		return
	}
	files, err := s.configs.logFiles(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := files.Rotate(req.Path); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// removedFile is a rotated file a cleanup removed, or would remove.
type removedFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// logsCleanup answers POST /logs/cleanup: what retention would remove, or
// what it removed.
func (s *Server) logsCleanup(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readLogRequest(w, r)
	if !ok {
		return
	}
	files, err := s.configs.logFiles(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	removed, err := files.Cleanup(req.Path, req.DryRun)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]removedFile, 0, len(removed))
	for _, f := range removed {
		out = append(out, removedFile{Name: f.Name, Size: f.Size, Modified: f.ModTime})
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": out})
}

// errNoLogs is why a log file operation cannot run.
var errNoLogs = errors.New("fit runs no log file sinks")

// logFiles returns the sinks of the running config, after checking that
// path is one of its file outputs: no other file is listed or removed.
func (cs *Configs) logFiles(path string) (*logs.Files, error) {
	cfg := cs.CurrentConfig()
	if cfg == nil || cs.env.Log == nil || cs.env.Log.Files() == nil {
		return nil, errNoLogs
	}
	clean := filepath.Clean(path)
	for _, out := range cfg.Log.FileOutputs() {
		if filepath.Clean(out.Path) == clean {
			return cs.env.Log.Files(), nil
		}
	}
	if a := cfg.AccessLog; a != nil && config.IsFileOutput(a.Output) && filepath.Clean(a.Output) == clean {
		return cs.env.Log.Files(), nil
	}
	return nil, errors.New(path + " is not a log file of the running config: only the paths it writes are listed or removed")
}
