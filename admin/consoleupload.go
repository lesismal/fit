package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant"
	"github.com/lesismal/fit/medialib"
)

// uploadChunkTimeout bounds how long the body of an upload's PATCH may take
// to arrive through the console, in place of the console's own read
// timeout: the workbench sends pieces of some megabytes.
const uploadChunkTimeout = 10 * time.Minute

// uploadPath splits the path of an upload's PATCH, under the admin module,
// /media/library/{name}/uploads/{id}, into the library's name and the
// upload's ID.
func uploadPath(p string) (name, id string, ok bool) {
	rest, ok := strings.CutPrefix(p, "/media/library/")
	if !ok {
		return "", "", false
	}
	name, rest, ok = strings.Cut(rest, "/")
	if !ok || name == "" {
		return "", "", false
	}
	id, ok = strings.CutPrefix(rest, "uploads/")
	return name, id, ok && id != "" && !strings.Contains(id, "/")
}

// uploadPatch is the console's Options.StreamBody: an upload's PATCH
// streams.
func uploadPatch(r *http.Request) bool {
	if r.Method != http.MethodPatch {
		return false
	}
	p, ok := strings.CutPrefix(r.URL.Path, "/api/m/"+AdminModule)
	_, _, up := uploadPath(p)
	return ok && up
}

// uploadChunk answers PATCH /api/media/library/{name}/uploads/{id} on the
// console: its body, which streams, written at Upload-Offset as it
// arrives, on the library's file pool, the body held while each piece is
// written. It never blocks the worker: the answer goes from the pool once
// the body has ended and its last piece is written.
func (cs *Console) uploadChunk(r *console.Request) {
	name, id, ok := uploadPath(strings.TrimPrefix(r.Path, "/api"))
	if !ok {
		_ = console.Error(r.Context, http.StatusNotFound, "no such endpoint")
		return
	}
	l := medialib.Lookup(cs.inner.configs.env.Engine, name)
	if l == nil {
		_ = console.Error(r.Context, http.StatusNotFound, "no media library named "+strconv.Quote(name)+" runs here")
		return
	}
	off, ok := uploadOffset(r.Request.Header)
	if !ok {
		_ = console.Error(r.Context, http.StatusBadRequest, "the request needs Upload-Offset, where the body goes in the file")
		return
	}
	c, err := l.WriteUpload(id, off)
	if err != nil {
		info, _ := l.Upload(id)
		answerFile(r.Context, info, err)
		return
	}
	user := r.User
	// A body declared longer than the upload gives it up before LimitBody
	// answers 413, as a client that reads the answer may look for the
	// upload at once, and finds it gone, as the API's PATCH has it.
	n := c.Remaining()
	over := r.Request.ContentLength > n
	if over {
		c.Abort(medialib.ErrTooLarge)
	}
	if err := r.Context.LimitBody(n); err != nil {
		// Answered 413: the upload is given up, here when the body is of
		// no declared length and already longer.
		if !over {
			c.Abort(medialib.ErrTooLarge)
		}
		cs.auditFile(user, "media.upload", map[string]any{"library": name, "id": id}, medialib.ErrTooLarge)
		return
	}
	_ = r.Context.SetReadDeadline(time.Now().Add(uploadChunkTimeout))
	// The answer goes once the last piece is written, after the body's
	// last call: the request is held until then.
	r.Context.Retain()
	done := func(info medialib.UploadInfo, err error) {
		defer r.Context.Release()
		args := map[string]any{"library": name, "id": info.ID, "path": info.Path, "size": info.Size}
		switch {
		case info.Complete && err == nil:
			cs.auditFile(user, "media.upload", args, nil)
		case errors.Is(err, medialib.ErrTooLarge), errors.Is(err, medialib.ErrNoSpace), errors.Is(err, medialib.ErrExists):
			cs.auditFile(user, "media.upload", args, err)
		}
		answerFile(r.Context, info, err)
	}
	r.Context.OnBody(func(data []byte, fin bool, err error) {
		c.Write(data, fin, err, r.Context.HoldBody, done)
	})
}

// setUploadHeaders sets an upload's offset and length in h.
func setUploadHeaders(h http.Header, info medialib.UploadInfo) {
	h.Set(uploadOffsetHeader, strconv.FormatInt(info.Offset, 10))
	h.Set(uploadLengthHeader, strconv.FormatInt(info.Size, 10))
}

// answerFile answers a console's upload write with info, or err, and the
// upload's offset and length while it is under way.
func answerFile(c *fibhttp.Context, info medialib.UploadInfo, err error) {
	if info.ID != "" && !errors.Is(err, medialib.ErrTooLarge) {
		setUploadHeaders(c.Header(), info)
	}
	if err != nil {
		status := fileStatus(err)
		if status == http.StatusServiceUnavailable || errors.Is(err, medialib.ErrUploadBusy) {
			c.Header().Set("Retry-After", "1")
		}
		_ = console.Error(c, status, err.Error())
		return
	}
	_ = console.WriteJSON(c, http.StatusOK, info)
}

// auditFile writes a change of a media library's files to the assistant's
// audit log, as the user's, from the workbench.
func (cs *Console) auditFile(user, op string, args any, err error) {
	if cs.ai == nil {
		return
	}
	data, _ := json.Marshal(args)
	e := assistant.AuditEntry{User: user, Source: assistant.SourceWorkbench, Kind: assistant.AuditMedia, Outcome: assistant.OutcomeApplied,
		Calls: []assistant.AuditCall{{Tool: op, Args: string(data), Error: err != nil}}}
	if err != nil {
		e.Outcome, e.Detail = assistant.OutcomeFailed, err.Error()
	}
	cs.ai.Audit(e)
}
