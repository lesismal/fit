package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
)

// The Admin API's endpoints for the sync role's pipelines (I188), which fit
// sync calls:
//
//	GET    /sync/pipelines                    every pipeline's state: {"pipelines": [...]}
//	GET    /sync/pipelines/{name}             one pipeline's: its state, checkpoint,
//	                                          queue, counts, throughput, lag and errors
//	POST   /sync/pipelines/{name}             an action: {"action": "pause"},
//	                                          {"action": "resume"}, or {"action":
//	                                          "snapshot", "confirm": true}, the
//	                                          source read again from its start
//	GET    /sync/pipelines/{name}/checkpoint  the checkpoint file: {"checkpoint": ...}
//	PUT    /sync/pipelines/{name}/checkpoint  set it: {"position", "confirm"}
//	DELETE /sync/pipelines/{name}/checkpoint  reset it: ?confirm=1
//	POST   /sync/pipelines/{name}/test        test its connectors' connections
//	POST   /sync/pipelines/{name}/dry-run     read its first records through its
//	                                          transforms: {"limit", "timeout",
//	                                          "from": "checkpoint" | "start"}
//	POST   /sync/test                         the same for a pipeline's config not
//	POST   /sync/dry-run                      applied: {"pipeline": {...}, ...}
//
// A change of the checkpoint that would skip or replay records (setting
// another position, a reset or a snapshot while there is a checkpoint) is
// answered 409 unless it says confirm. An action is not kept in the
// config: a pipeline paused stays paused across reloads that keep it, and
// runs again when fit starts again. A dry run writes nothing and saves no
// position.

// syncRuntime is the sync role's runtime, or nil once it has answered 404.
func (s *Server) syncRuntime(w http.ResponseWriter) *fitsync.Runtime {
	rt := fitsync.Lookup(s.configs.env.Engine)
	if rt == nil {
		writeError(w, http.StatusNotFound, "the sync role does not run here")
	}
	return rt
}

// syncError answers err, a sync control's: a pipeline not running, a
// change not confirmed, or else the checkpoint file that could not be read
// or written.
func syncError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, fitsync.ErrNoPipeline):
		status = http.StatusNotFound
	case errors.Is(err, fitsync.ErrConfirm):
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

// readSyncBody decodes the request's JSON body into v; an empty body leaves
// v as it is.
func (s *Server) readSyncBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return false
	}
	return true
}

// syncPipelines answers GET /sync/pipelines.
func (s *Server) syncPipelines(w http.ResponseWriter, _ *http.Request) {
	if rt := s.syncRuntime(w); rt != nil {
		writeJSON(w, http.StatusOK, map[string]any{"pipelines": rt.Status()})
	}
}

// syncPipeline answers GET /sync/pipelines/{name}.
func (s *Server) syncPipeline(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	st, err := rt.Pipeline(r.PathValue("name"))
	if err != nil {
		syncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// syncActionBody is the body of POST /sync/pipelines/{name}.
type syncActionBody struct {
	Action  string `json:"action"`
	Confirm bool   `json:"confirm"`
}

// syncAction answers POST /sync/pipelines/{name}.
func (s *Server) syncAction(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	var a syncActionBody
	if !s.readSyncBody(w, r, &a) {
		return
	}
	name := r.PathValue("name")
	var st fitsync.Status
	var err error
	switch a.Action {
	case "pause":
		st, err = rt.Pause(name)
	case "resume":
		st, err = rt.Resume(name)
	case "snapshot":
		st, err = rt.Snapshot(name, a.Confirm)
	default:
		writeError(w, http.StatusBadRequest, "the action is pause, resume or snapshot, not "+strconv.Quote(a.Action))
		return
	}
	if err != nil {
		syncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// checkpointBody is a checkpoint's answer: the file, null when none was
// saved yet.
type checkpointBody struct {
	Checkpoint *fitsync.Checkpoint `json:"checkpoint"`
}

// syncCheckpoint answers GET /sync/pipelines/{name}/checkpoint.
func (s *Server) syncCheckpoint(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	c, err := rt.Checkpoint(r.PathValue("name"))
	if err != nil {
		syncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkpointBody{c})
}

// setCheckpointBody is the body of PUT /sync/pipelines/{name}/checkpoint.
type setCheckpointBody struct {
	Position json.RawMessage `json:"position"`
	Confirm  bool            `json:"confirm"`
}

// syncSetCheckpoint answers PUT /sync/pipelines/{name}/checkpoint.
func (s *Server) syncSetCheckpoint(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	var b setCheckpointBody
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	if err := json.Unmarshal(data, &b); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return
	}
	c, err := rt.SetCheckpoint(r.PathValue("name"), b.Position, b.Confirm)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, checkpointBody{c})
	case errors.Is(err, fitsync.ErrNoPipeline), errors.Is(err, fitsync.ErrConfirm):
		syncError(w, err)
	default:
		// The position is what is wrong.
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// syncResetCheckpoint answers DELETE /sync/pipelines/{name}/checkpoint.
func (s *Server) syncResetCheckpoint(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	confirm, _ := strconv.ParseBool(r.URL.Query().Get("confirm"))
	st, err := rt.ResetCheckpoint(r.PathValue("name"), confirm)
	if err != nil {
		syncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// syncTestRunning answers POST /sync/pipelines/{name}/test.
func (s *Server) syncTestRunning(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	res, err := rt.Test(r.Context(), r.PathValue("name"))
	if err != nil {
		syncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, testBody(res))
}

// testBody is a connection test's answer: ok when every connector tested
// passed.
func testBody(res []fitsync.TestResult) map[string]any {
	return map[string]any{"ok": fitsync.Passed(res), "results": res}
}

// dryRunBody is the body of a dry run.
type dryRunBody struct {
	// Pipeline is the config of the pipeline, for POST /sync/dry-run and
	// POST /sync/test.
	Pipeline json.RawMessage `json:"pipeline,omitempty"`
	Limit    int             `json:"limit,omitempty"`
	// Timeout is a duration, as 5s.
	Timeout string `json:"timeout,omitempty"`
	// From is checkpoint, the default, or start.
	From string `json:"from,omitempty"`
}

func (b *dryRunBody) options(w http.ResponseWriter) (fitsync.DryRunOptions, bool) {
	o := fitsync.DryRunOptions{Limit: b.Limit}
	if b.Timeout != "" {
		d, err := time.ParseDuration(b.Timeout)
		if err != nil || d <= 0 || d > fitsync.MaxDryRunTimeout {
			writeError(w, http.StatusBadRequest, "timeout is a duration up to "+fitsync.MaxDryRunTimeout.String()+", as 5s, not "+strconv.Quote(b.Timeout))
			return o, false
		}
		o.Timeout = d
	}
	switch b.From {
	case "", "checkpoint":
	case "start":
		o.FromStart = true
	default:
		writeError(w, http.StatusBadRequest, "from is checkpoint or start, not "+strconv.Quote(b.From))
		return o, false
	}
	if b.Limit < 0 || b.Limit > fitsync.MaxDryRunRecords {
		writeError(w, http.StatusBadRequest, "limit is from 1 to "+strconv.Itoa(fitsync.MaxDryRunRecords))
		return o, false
	}
	return o, true
}

// syncDryRunRunning answers POST /sync/pipelines/{name}/dry-run.
func (s *Server) syncDryRunRunning(w http.ResponseWriter, r *http.Request) {
	rt := s.syncRuntime(w)
	if rt == nil {
		return
	}
	var b dryRunBody
	if !s.readSyncBody(w, r, &b) {
		return
	}
	o, ok := b.options(w)
	if !ok {
		return
	}
	d, err := rt.DryRun(r.Context(), r.PathValue("name"), o)
	if err != nil {
		syncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// postedPipeline decodes the pipeline a body names, or answers 400.
func postedPipeline(w http.ResponseWriter, b *dryRunBody) *config.SyncPipeline {
	if len(b.Pipeline) == 0 {
		writeError(w, http.StatusBadRequest, "name the pipeline's config: {\"pipeline\": {...}}")
		return nil
	}
	p, err := fitsync.ParsePipeline(b.Pipeline)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil
	}
	return p
}

// syncTestConfig answers POST /sync/test: the connections of a pipeline's
// config, which need not be applied, tested with its modules loaded anew.
func (s *Server) syncTestConfig(w http.ResponseWriter, r *http.Request) {
	var b dryRunBody
	if !s.readSyncBody(w, r, &b) {
		return
	}
	p := postedPipeline(w, &b)
	if p == nil {
		return
	}
	res, err := fitsync.TestPipeline(r.Context(), p, s.configs.env.Engine)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, testBody(res))
}

// syncDryRunConfig answers POST /sync/dry-run: a dry run of a pipeline's
// config, which need not be applied, from its source's start.
func (s *Server) syncDryRunConfig(w http.ResponseWriter, r *http.Request) {
	var b dryRunBody
	if !s.readSyncBody(w, r, &b) {
		return
	}
	p := postedPipeline(w, &b)
	if p == nil {
		return
	}
	if b.From == "checkpoint" {
		writeError(w, http.StatusBadRequest, "a pipeline's config has no checkpoint: it is read from its start")
		return
	}
	b.From = "start"
	o, ok := b.options(w)
	if !ok {
		return
	}
	d, err := fitsync.DryRunPipeline(r.Context(), p, nil, s.configs.env.Engine, o)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}
