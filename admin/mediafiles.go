package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/streaming/channels"
)

// The Admin API's endpoints for a media library's files (I139):
//
//	GET    /media/library/{name}/uploads       the uploads under way
//	POST   /media/library/{name}/uploads       begin one: {"path", "size"},
//	                                           path under upload_dir
//	GET    /media/library/{name}/uploads/{id}  one, its offset also in
//	                                           Upload-Offset (HEAD too)
//	PATCH  /media/library/{name}/uploads/{id}  write the body at
//	                                           Upload-Offset
//	DELETE /media/library/{name}/uploads/{id}  give it up
//	GET    /media/library/{name}/dirs?root=    the directories of a root
//	GET    /media/library/{name}/usage?item=   what plays an item: channels
//	                                           and playback sessions
//	POST   /media/library/{name}/rename        {"item", "name", "force"}
//	POST   /media/library/{name}/move          {"item", "dir", "force"}
//	POST   /media/library/{name}/delete        {"item", "force"}
//	GET    /media/library/{name}/sidecar?item= an item's sidecar, or a draft
//	PUT    /media/library/{name}/sidecar       {"item", "sidecar",
//	                                           "modified", "force"}
//
// A rename, move or delete of an item that a channel names or plays, or that
// a playback session plays, and a sidecar that changes such an item's ID,
// is refused 409 with what uses it, unless force is set. Each change, an
// upload begun, given up or finished included, is written to the
// assistant's audit log when the console runs one, as the console's user's.
// On the console, PATCH streams the body to the file pool as it arrives
// (console.go); here, the Admin API's own server reads it, within its
// read timeout, a minute.

// The headers of a resumable upload, as tus names them.
const (
	uploadOffsetHeader = "Upload-Offset"
	uploadLengthHeader = "Upload-Length"
)

// auditFunc writes a change of a media library's files to the assistant's
// audit log: who, the operation (media.rename and the like), its
// arguments, and how it ended.
type auditFunc func(user, op string, args any, err error)

// fileUser is who asks: the console's user, or admin-api.
func fileUser(r *http.Request) string {
	if u := r.Header.Get(ConsoleUserHeader); u != "" {
		return u
	}
	return "admin-api"
}

// auditFile writes a change to the audit log, when there is one.
func (s *Server) auditFile(r *http.Request, op string, args any, err error) {
	if s.audit != nil {
		s.audit(fileUser(r), op, args, err)
	}
}

// fileStatus is the status a change of files answers err with.
func fileStatus(err error) int {
	switch {
	case errors.Is(err, medialib.ErrNoItem), errors.Is(err, medialib.ErrNoUpload), errors.Is(err, medialib.ErrNoUploads),
		errors.Is(err, medialib.ErrRemoved):
		return http.StatusNotFound
	case errors.Is(err, medialib.ErrBadPath), errors.Is(err, medialib.ErrNotMedia), errors.Is(err, medialib.ErrUploadSize),
		errors.Is(err, medialib.ErrNoChange), errors.Is(err, medialib.ErrBadSidecar):
		return http.StatusBadRequest
	case errors.Is(err, medialib.ErrOutside):
		return http.StatusForbidden
	case errors.Is(err, medialib.ErrExists), errors.Is(err, medialib.ErrChanged), errors.Is(err, medialib.ErrOffset),
		errors.Is(err, medialib.ErrUploadBusy):
		return http.StatusConflict
	case errors.Is(err, medialib.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, medialib.ErrTooMany):
		return http.StatusTooManyRequests
	case errors.Is(err, medialib.ErrBusy):
		return http.StatusServiceUnavailable
	case errors.Is(err, medialib.ErrNoSpace):
		return http.StatusInsufficientStorage
	}
	return http.StatusInternalServerError
}

// writeFileError answers err, a change's.
func writeFileError(w http.ResponseWriter, err error) {
	status := fileStatus(err)
	if status == http.StatusServiceUnavailable || status == http.StatusConflict && errors.Is(err, medialib.ErrUploadBusy) {
		w.Header().Set("Retry-After", "1")
	}
	writeError(w, status, err.Error())
}

// await calls a method that calls back, and returns what it gave: the
// Admin API's handlers run on goroutines of their own, or the console's
// pool, never a fib worker, and wait for the library's pool.
func await[T any](call func(func(T, error))) (T, error) {
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	call(func(v T, err error) { ch <- res{v, err} })
	r := <-ch
	return r.v, r.err
}

// uploadInfo writes an upload's offset and length as headers.
func uploadHeaders(w http.ResponseWriter, info medialib.UploadInfo) {
	w.Header().Set(uploadOffsetHeader, strconv.FormatInt(info.Offset, 10))
	w.Header().Set(uploadLengthHeader, strconv.FormatInt(info.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
}

// listUploads answers GET /media/library/{name}/uploads.
func (s *Server) listUploads(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploads": l.Uploads()})
}

// createUpload answers POST /media/library/{name}/uploads: 201 with the
// upload and its Location.
func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	var in struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if !s.readJSON(w, r, &in) {
		return
	}
	info, err := await(func(fn func(medialib.UploadInfo, error)) {
		l.CreateUpload(medialib.UploadSpec{Path: in.Path, Size: in.Size, User: fileUser(r)}, fn)
	})
	args := map[string]any{"library": l.Name(), "path": in.Path, "size": in.Size}
	if err != nil {
		s.auditFile(r, "media.upload.begin", args, err)
		writeFileError(w, err)
		return
	}
	args["id"], args["to"] = info.ID, info.Path
	s.auditFile(r, "media.upload.begin", args, nil)
	uploadHeaders(w, info)
	w.Header().Set("Location", "/media/library/"+l.Name()+"/uploads/"+info.ID)
	writeJSON(w, http.StatusCreated, info)
}

// getUpload answers GET and HEAD /media/library/{name}/uploads/{id}.
func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	info, ok := l.Upload(r.PathValue("id"))
	if !ok {
		writeFileError(w, medialib.ErrNoUpload)
		return
	}
	uploadHeaders(w, info)
	writeJSON(w, http.StatusOK, info)
}

// cancelUpload answers DELETE /media/library/{name}/uploads/{id}.
func (s *Server) cancelUpload(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	id := r.PathValue("id")
	info, _ := l.Upload(id)
	_, err := await(func(fn func(struct{}, error)) { l.CancelUpload(id, func(err error) { fn(struct{}{}, err) }) })
	s.auditFile(r, "media.upload.cancel", map[string]any{"library": l.Name(), "id": id, "path": info.Path}, err)
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

// uploadOffset reads Upload-Offset, answering 400 when it is not a number.
func uploadOffset(h http.Header) (int64, bool) {
	n, err := strconv.ParseInt(h.Get(uploadOffsetHeader), 10, 64)
	return n, err == nil && n >= 0
}

// patchUpload answers PATCH /media/library/{name}/uploads/{id}: the body
// written at Upload-Offset, read as it arrives, each piece written on the
// library's pool before the next is read.
func (s *Server) patchUpload(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	off, ok := uploadOffset(r.Header)
	if !ok {
		writeError(w, http.StatusBadRequest, "the request needs Upload-Offset, where the body goes in the file")
		return
	}
	c, err := l.WriteUpload(r.PathValue("id"), off)
	if err != nil {
		if info, ok := l.Upload(r.PathValue("id")); ok {
			uploadHeaders(w, info)
		}
		writeFileError(w, err)
		return
	}
	res := make(chan error, 1)
	var last medialib.UploadInfo
	done := func(info medialib.UploadInfo, err error) { last = info; res <- err }
	held := make(chan struct{}, 1)
	hold := func(h bool) {
		if h {
			held <- struct{}{}
		} else {
			<-held
		}
	}
	if r.ContentLength > c.Remaining() {
		c.Abort(medialib.ErrTooLarge)
		err = medialib.ErrTooLarge
	} else {
		err = s.readUpload(r.Body, c, hold, held, done, res)
	}
	s.finishUpload(w, r, l.Name(), last, err)
}

// readUpload reads body into c, waiting while c holds it, and returns how
// c ended.
func (s *Server) readUpload(body io.Reader, c *medialib.Chunk, hold func(bool), held chan struct{}, done func(medialib.UploadInfo, error), res chan error) error {
	buf := make([]byte, 64<<10)
	for {
		n, rerr := body.Read(buf)
		fin := rerr == io.EOF
		var fail error
		if rerr != nil && !fin {
			fail = rerr
		}
		c.Write(buf[:n], fin, fail, hold, done)
		if fin || fail != nil {
			return <-res
		}
		// Wait while the piece is written.
		select {
		case held <- struct{}{}:
			<-held
		case err := <-res:
			return err
		}
	}
}

// finishUpload answers the end of a write, and writes an upload finished,
// or given up, to the audit log.
func (s *Server) finishUpload(w http.ResponseWriter, r *http.Request, library string, info medialib.UploadInfo, err error) {
	args := map[string]any{"library": library, "id": info.ID, "path": info.Path, "size": info.Size}
	switch {
	case info.Complete && err == nil:
		s.auditFile(r, "media.upload", args, nil)
	case errors.Is(err, medialib.ErrTooLarge), errors.Is(err, medialib.ErrNoSpace), errors.Is(err, medialib.ErrExists):
		s.auditFile(r, "media.upload", args, err)
	}
	if err != nil {
		if info.ID != "" && !errors.Is(err, medialib.ErrTooLarge) {
			uploadHeaders(w, info)
		}
		writeFileError(w, err)
		return
	}
	uploadHeaders(w, info)
	writeJSON(w, http.StatusOK, info)
}

// libraryDirs answers GET /media/library/{name}/dirs?root=: the root's
// directories, the first root's without root.
func (s *Server) libraryDirs(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	root := r.URL.Query().Get("root")
	if root == "" {
		root = l.Roots()[0]
	}
	dirs, err := await(func(fn func([]string, error)) { l.Dirs(root, fn) })
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"root": root, "roots": l.Roots(), "dirs": dirs, "max": medialib.MaxDirs})
}

// itemUsage is what uses an item: the channels whose list or schedule names
// it, those playing it now or next, and its playback sessions.
type itemUsage struct {
	Library string `json:"library"`
	Item    string `json:"item"`
	// Channels are those of the running config that name the item, and
	// Playing those playing it now or reading it to play next.
	Channels []string `json:"channels"`
	Playing  []string `json:"playing"`
	// Sessions counts the playback sessions playing it; Counted is false
	// when no vod route of the library signs URLs, whose sessions alone are
	// known.
	Sessions int  `json:"sessions"`
	Counted  bool `json:"sessions_counted"`
	// InUse is true when any of these is.
	InUse bool `json:"in_use"`
}

// usage finds what uses the item id of library. It reads the running
// config: call it off fib's workers.
func (s *Server) usage(library, id string) itemUsage {
	u := itemUsage{Library: library, Item: id, Channels: []string{}, Playing: []string{}}
	v := s.configs.mediaView()
	if st := v.cfg.Streaming; st != nil {
		for _, ch := range st.Channels {
			if ch.Library != library {
				continue
			}
			named := slices.Contains(ch.Items, id) || slices.ContainsFunc(ch.Schedule, func(sl config.ChannelSlot) bool { return sl.Item == id })
			if named {
				u.Channels = append(u.Channels, ch.Name)
			}
		}
	}
	engine := s.configs.env.Engine
	if m := channels.Lookup(engine); m != nil {
		for _, ch := range m.Channels() {
			if ch.Library == library && (ch.Now != nil && ch.Now.Item == id || ch.Next != nil && ch.Next.Item == id) {
				u.Playing = append(u.Playing, ch.Name)
			}
		}
	}
	for _, rt := range v.routes {
		if rt.module != "vod" || rt.library != library {
			continue
		}
		p := mediaauth.Lookup(engine, rt.auth)
		if p == nil || !p.URLSigning() {
			continue
		}
		u.Counted = true
		res := rt.prefix + id + "/"
		for _, ss := range p.Sessions() {
			if !ss.Ended && ss.Resource == res {
				u.Sessions++
			}
		}
	}
	u.InUse = len(u.Channels) > 0 || len(u.Playing) > 0 || u.Sessions > 0
	return u
}

// itemUsage answers GET /media/library/{name}/usage?item=.
func (s *Server) itemUsage(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	id := r.URL.Query().Get("item")
	if l.Item(id) == nil {
		writeFileError(w, medialib.ErrNoItem)
		return
	}
	writeJSON(w, http.StatusOK, s.usage(l.Name(), id))
}

// fileRequest is the body of a change of an item's files.
type fileRequest struct {
	Item  string `json:"item"`
	Name  string `json:"name,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// inUse answers 409 with what uses the item, and reports true, when it is
// used and force is not set.
func (s *Server) inUse(w http.ResponseWriter, library, id string, force bool) bool {
	if force {
		return false
	}
	u := s.usage(library, id)
	if !u.InUse {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "the item is in use: a channel names or plays it, or a viewer plays it; send force to change it all the same", "usage": u})
	return true
}

// changeItem answers POST /media/library/{name}/{rename,move,delete}.
func (s *Server) changeItem(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := s.library(w, r)
		if l == nil {
			return
		}
		var in fileRequest
		if !s.readJSON(w, r, &in) {
			return
		}
		if l.Item(in.Item) == nil {
			writeFileError(w, medialib.ErrNoItem)
			return
		}
		if s.inUse(w, l.Name(), in.Item, in.Force) {
			return
		}
		c, err := await(func(fn func(*medialib.Change, error)) {
			switch op {
			case medialib.OpRename:
				l.RenameItem(in.Item, in.Name, fn)
			case medialib.OpMove:
				l.MoveItem(in.Item, in.Dir, fn)
			default:
				l.DeleteItem(in.Item, fn)
			}
		})
		args := map[string]any{"library": l.Name(), "item": in.Item}
		switch op {
		case medialib.OpRename:
			args["name"] = in.Name
		case medialib.OpMove:
			args["dir"] = in.Dir
		}
		if in.Force {
			args["force"] = true
		}
		s.auditFile(r, "media."+op, args, err)
		if err != nil {
			if c != nil {
				// A delete that stopped part way: what it removed.
				writeJSON(w, fileStatus(err), map[string]any{"error": err.Error(), "change": c})
				return
			}
			writeFileError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// getSidecar answers GET /media/library/{name}/sidecar?item=.
func (s *Server) getSidecar(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	v, err := await(func(fn func(*medialib.SidecarView, error)) { l.ReadSidecar(r.URL.Query().Get("item"), fn) })
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putSidecar answers PUT /media/library/{name}/sidecar.
func (s *Server) putSidecar(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	var in struct {
		Item     string            `json:"item"`
		Sidecar  *medialib.Sidecar `json:"sidecar"`
		Modified time.Time         `json:"modified"`
		Force    bool              `json:"force"`
	}
	if !s.readJSON(w, r, &in) {
		return
	}
	it := l.Item(in.Item)
	switch {
	case it == nil:
		writeFileError(w, medialib.ErrNoItem)
		return
	case in.Sidecar == nil:
		writeError(w, http.StatusBadRequest, `the body names the item and its sidecar: {"item", "sidecar"}`)
		return
	}
	// A new ID is a new item to what names the old one.
	if id := cmpOr(in.Sidecar.ID, medialib.PathID(it.Path)); id != it.ID && s.inUse(w, l.Name(), in.Item, in.Force) {
		return
	}
	c, err := await(func(fn func(*medialib.Change, error)) { l.WriteSidecar(in.Item, in.Sidecar, in.Modified, fn) })
	data, _ := json.Marshal(in.Sidecar)
	s.auditFile(r, "media.sidecar", map[string]any{"library": l.Name(), "item": in.Item, "sidecar": string(data)}, err)
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
