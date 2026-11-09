package btstream

import (
	"context"
	"errors"
	"io"
	stdhttp "net/http"
	"strconv"
	"sync/atomic"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/mediaauth"
)

// stream is a response a goroutine of the handler's pool reads and writes,
// one at a time: open, then pump, which ends when the client has enough
// queued and is started again by OnWritable. Only that chain touches the
// response and the reader; the client leaving and Drain only cancel ctx,
// which ends a read waiting for its piece and the waits before it (the
// torrent's hold and info). fib's Reader.Close interrupts a Read too (A83),
// but ctx is what every wait selects on, and the per-read timeout derives
// from it, so the stream closes its reader only as it finishes.
type stream struct {
	h    *BTStream
	c    *fibhttp.Context
	role *bittorrent.Role
	req  request

	ctx    context.Context
	cancel context.CancelFunc
	ended  atomic.Bool

	k        key
	acquired bool

	t         *fibbt.Torrent
	rd        *fibbt.Reader
	buf       []byte
	remaining int64
	begun     bool
	// status and header are the response's, sent with its first chunk.
	status int
	header stdhttp.Header
}

func newStream(h *BTStream, c *fibhttp.Context, role *bittorrent.Role, req request) *stream {
	s := &stream{h: h, c: c, role: role, req: req}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s
}

// open finds the torrent, adding it from its magnet link when asked and
// let, waits for its info, picks the file and the range, and starts
// streaming it. It runs on the stream's goroutine.
func (s *stream) open() {
	h := s.h
	t, added, ok := s.torrent()
	if !ok {
		return
	}
	// Held only once found, so that requests for torrents the role has
	// not got leave nothing behind; a linger's removal under way is
	// waited for, and a torrent it removed looked up, or added, again.
	s.k = key{role: s.role, id: s.req.id}
	if !acquire(s.ctx, s.k) {
		s.finish()
		return
	}
	s.acquired = true
	select {
	case <-t.Removed():
		if t, added, ok = s.torrent(); !ok {
			return
		}
	default:
	}
	s.t = t
	if !s.waitInfo(t) {
		return
	}
	f := pickFile(t.Files(), s.req)
	if f == nil {
		s.answer(stdhttp.StatusNotFound, "the torrent has no such file")
		return
	}
	if !s.focus(t, f, added) {
		return
	}

	size := f.Length()
	etag := `"` + s.req.id + "-" + strconv.Itoa(f.Index()) + `"`
	hdr := stdhttp.Header{
		"Content-Type":  {contentType(f.Path())},
		"Accept-Ranges": {"bytes"},
		"Etag":          {etag},
	}
	if h.CacheControl != "" {
		hdr["Cache-Control"] = []string{h.CacheControl}
	}
	if h.CORS != "" {
		hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
		hdr["Access-Control-Expose-Headers"] = []string{"Content-Range, Content-Length, Accept-Ranges, Etag"}
	}
	r := s.c.Request
	if noneMatch(r.Header.Get("If-None-Match"), etag) {
		s.reply(stdhttp.StatusNotModified, hdr, nil)
		return
	}
	rangeHeader := r.Header.Get("Range")
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		rangeHeader = "" // a validator of another version, or a date: the whole file
	}
	start, n, partial, satisfiable := parseRange(rangeHeader, size)
	if !satisfiable {
		hdr["Content-Range"] = []string{"bytes */" + strconv.FormatInt(size, 10)}
		hdr["Content-Type"] = []string{"text/plain; charset=utf-8"}
		s.reply(stdhttp.StatusRequestedRangeNotSatisfiable, hdr, []byte("the range is past the file's end\n"))
		return
	}
	s.status = stdhttp.StatusOK
	if partial {
		s.status = stdhttp.StatusPartialContent
		hdr["Content-Range"] = []string{"bytes " + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(start+n-1, 10) + "/" + strconv.FormatInt(size, 10)}
	}
	hdr["Content-Length"] = []string{strconv.FormatInt(n, 10)}
	s.header = hdr
	if r.Method == stdhttp.MethodHead || n == 0 {
		s.begin()
		_ = s.c.Finish()
		s.finish()
		return
	}
	s.rd = f.NewReader()
	s.rd.SetReadahead(h.readahead)
	if _, err := s.rd.Seek(start, io.SeekStart); err != nil {
		s.answer(stdhttp.StatusInternalServerError, err.Error())
		return
	}
	s.remaining = n
	s.buf = make([]byte, min(int64(chunkBytes), n))
	s.pump()
}

// torrent returns the torrent of the request, added as a temporary one
// from its magnet link when the role has not got it, and whether this
// stream added it. ok false says the request is answered.
func (s *stream) torrent() (t *fibbt.Torrent, added, ok bool) {
	h := s.h
	t, err := s.role.Torrent(s.req.id)
	if errors.Is(err, bittorrent.ErrNotFound) && s.req.magnet != "" {
		if err := s.allowAdd(); err != nil {
			if s.ctx.Err() != nil {
				s.finish()
				return nil, false, false
			}
			s.refuse(err)
			return nil, false, false
		}
		_, err = s.role.AddTemporary(bittorrent.AddRequest{Magnet: s.req.magnet}, h.maxTemp)
		added = err == nil
		if errors.Is(err, bittorrent.ErrExists) {
			err = nil // another request added it first
		}
		if err == nil {
			t, err = s.role.Torrent(s.req.id)
		}
	}
	switch {
	case err == nil:
		return t, added, true
	case errors.Is(err, bittorrent.ErrNotFound):
		s.answer(stdhttp.StatusNotFound, "the bittorrent.client role has no such torrent")
	case errors.Is(err, bittorrent.ErrTooManyTemporary):
		s.answer(stdhttp.StatusServiceUnavailable, "max_temporary torrents added by magnet link are held already", "Retry-After", "30")
	case errors.Is(err, bittorrent.ErrTooMany):
		s.answer(stdhttp.StatusServiceUnavailable, "the bittorrent.client role holds max_torrents torrents already", "Retry-After", "30")
	case errors.Is(err, bittorrent.ErrNotRunning):
		s.answer(stdhttp.StatusServiceUnavailable, "the bittorrent.client role does not run", "Retry-After", "5")
	case errors.Is(err, bittorrent.ErrBadRequest):
		s.answer(stdhttp.StatusBadRequest, err.Error())
	default:
		s.answer(stdhttp.StatusInternalServerError, err.Error())
	}
	return nil, false, false
}

// allowAdd checks that the request may add its torrent: publish on its
// stream, with a bcrypt check waited for here, on the stream's goroutine.
func (s *stream) allowAdd() error {
	p, stream := s.h.auth, "bt/"+s.req.id
	_, err := p.Allow(mediaauth.Publish, stream, s.req.creds)
	if !errors.Is(err, mediaauth.ErrPending) {
		return err
	}
	ch := make(chan error, 1)
	p.Check(mediaauth.Publish, stream, s.req.creds, func(_ string, err error) { ch <- err })
	select {
	case err = <-ch:
		return err
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// refuse answers a request auth refused to add a torrent.
func (s *stream) refuse(err error) {
	status := mediaauth.Status(err)
	if status == stdhttp.StatusUnauthorized {
		s.answer(status, "adding a torrent needs publish on bt/"+s.req.id+": "+err.Error(), "Www-Authenticate", `Bearer realm="fit"`)
		return
	}
	s.answer(status, "adding a torrent needs publish on bt/"+s.req.id+": "+err.Error())
}

// waitInfo waits for the torrent's info, within info_timeout, and reports
// whether it came; otherwise the request is answered.
func (s *stream) waitInfo(t *fibbt.Torrent) bool {
	select {
	case <-t.GotInfo():
		return true
	default:
	}
	timer := time.NewTimer(s.h.infoTimeout)
	defer timer.Stop()
	select {
	case <-t.GotInfo():
		return true
	case <-timer.C:
		s.answer(stdhttp.StatusGatewayTimeout, "the torrent's info did not arrive from peers within info_timeout")
	case <-t.Removed():
		s.answer(stdhttp.StatusServiceUnavailable, "the torrent was removed", "Retry-After", "1")
	case <-s.ctx.Done():
		s.finish()
	}
	return false
}

// focus has a temporary torrent download the file f too, and, when this
// stream added it, that file alone, and holds the temporary torrents to
// max_temporary_bytes: past it a torrent just added is removed, and a file
// raised is set back, and the request answered 507. It reports whether the
// stream goes on.
func (s *stream) focus(t *fibbt.Torrent, f *fibbt.File, added bool) bool {
	st, err := s.role.Get(s.req.id)
	if err != nil || !st.Temporary {
		return true
	}
	info := t.Info()
	if info == nil {
		return true
	}
	prios := make([]int, len(info.FileList()))
	for _, x := range st.Files {
		if x.Index >= 0 && x.Index < len(prios) {
			prios[x.Index] = x.Priority
		}
	}
	before := append([]int(nil), prios...)
	if added {
		clear(prios)
	}
	if prios[f.Index()] == fibbt.PriorityNone {
		prios[f.Index()] = fibbt.PriorityNormal
	}
	if !equal(prios, before) {
		if err := s.role.SetFilePriorities(s.req.id, prios); err != nil {
			s.answer(stdhttp.StatusInternalServerError, err.Error())
			return false
		}
	}
	if s.role.TemporaryBytes() <= s.h.maxTempBytes {
		return true
	}
	if added {
		_, _ = s.role.RemoveTemporary(s.req.id)
	} else if !equal(prios, before) {
		_ = s.role.SetFilePriorities(s.req.id, before)
	}
	s.answer(stdhttp.StatusInsufficientStorage, "the temporary torrents would want more than max_temporary_bytes")
	return false
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pump reads the range a chunk at a time and writes it, until it is all
// sent, the client has enough queued (then OnWritable starts pump again),
// or the stream ends.
func (s *stream) pump() {
	for {
		if s.ctx.Err() != nil {
			s.finish()
			return
		}
		p := s.buf[:min(int64(len(s.buf)), s.remaining)]
		rctx, cancel := context.WithTimeout(s.ctx, s.h.readTimeout)
		s.rd.SetContext(rctx)
		n, err := s.rd.Read(p)
		cancel()
		if err == nil && n == 0 {
			err = io.ErrNoProgress
		}
		if err != nil {
			s.readFailed(err)
			return
		}
		if !s.begun {
			s.begin()
		}
		above, err := s.c.WriteNoWait(p[:n])
		if err == nil {
			err = s.c.FlushError()
		}
		if err != nil {
			// The client went, or the write stall timeout passed.
			s.finish()
			return
		}
		s.remaining -= int64(n)
		s.h.sent.Add(int64(n))
		if s.remaining == 0 {
			_ = s.c.Finish()
			s.finish()
			return
		}
		if above {
			_ = s.c.SetWriteDeadline(time.Now().Add(s.h.stall))
			if s.c.OnWritable(s.writable) {
				return
			}
			_ = s.c.SetWriteDeadline(time.Time{})
		}
	}
}

// writable goes on with the stream once the client has taken half of what
// it had queued, or ends it when the client went or the stall timeout
// passed. fib may call it on a worker: the reads go on a goroutine.
func (s *stream) writable(err error) {
	if err != nil {
		s.finish()
		return
	}
	_ = s.c.SetWriteDeadline(time.Time{})
	go s.pump()
}

// readFailed ends the stream after a read failed: a response not begun is
// answered, one begun cut short, so that the client sees it broke off.
func (s *stream) readFailed(err error) {
	if errors.Is(err, context.Canceled) || s.ctx.Err() != nil {
		// The client went, or the config was swapped out and drained.
		if s.begun {
			_ = s.c.Finish()
		} else if s.c.Err() == nil {
			s.h.respond(s.c, stdhttp.StatusServiceUnavailable, "the stream was ended", "Retry-After", "1")
		}
		s.finish()
		return
	}
	if s.begun {
		_ = s.c.Finish()
		s.finish()
		return
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		s.answer(stdhttp.StatusGatewayTimeout, "the pieces asked for did not arrive within read_timeout")
	case s.removed():
		s.answer(stdhttp.StatusServiceUnavailable, "the torrent was removed", "Retry-After", "1")
	default:
		s.answer(stdhttp.StatusInternalServerError, "reading the torrent: "+err.Error())
	}
}

// removed reports whether the torrent was removed from the client.
func (s *stream) removed() bool {
	select {
	case <-s.t.Removed():
		return true
	default:
		return false
	}
}

// begin sends the response's header, its write deadline lifted: each wait
// for the client is bounded by the stall timeout instead.
func (s *stream) begin() {
	s.begun = true
	_ = s.c.SetWriteDeadline(time.Time{})
	h := s.c.Header()
	for k, v := range s.header {
		h[k] = v
	}
	s.c.WriteHeader(s.status)
}

// reply answers with a whole response and ends the stream.
func (s *stream) reply(status int, hdr stdhttp.Header, body []byte) {
	if s.c.Err() == nil {
		_ = s.c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: body})
	}
	s.finish()
}

// answer answers with status and a message, and ends the stream.
func (s *stream) answer(status int, msg string, extra ...string) {
	if s.c.Err() == nil {
		s.h.respond(s.c, status, msg, extra...)
	}
	s.finish()
}

// finish ends the stream, once: its reader closed, its slot and its hold
// on the torrent given back, and the request and the config let go.
func (s *stream) finish() {
	if !s.ended.CompareAndSwap(false, true) {
		return
	}
	s.cancel()
	if s.rd != nil {
		_ = s.rd.Close()
	}
	if s.acquired {
		release(s.k, s.h.ttl)
	}
	s.h.done(s)
	s.c.Release()
	s.h.graph.Release()
}

// pickFile returns the file req asks for of files, nil when there is none:
// the largest; the one of the path; or, for a path of digits no file has,
// the one of that index.
func pickFile(files []*fibbt.File, req request) *fibbt.File {
	if req.largest {
		var best *fibbt.File
		for _, f := range files {
			if best == nil || f.Length() > best.Length() {
				best = f
			}
		}
		return best
	}
	for _, f := range files {
		if f.Path() == req.sel {
			return f
		}
	}
	if len(req.sel) == 0 || len(req.sel) > 9 {
		return nil
	}
	n := 0
	for i := 0; i < len(req.sel); i++ {
		c := req.sel[i]
		if c < '0' || c > '9' {
			return nil
		}
		n = n*10 + int(c-'0')
	}
	for _, f := range files {
		if f.Index() == n {
			return f
		}
	}
	return nil
}
