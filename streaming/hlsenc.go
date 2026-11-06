package streaming

import (
	"errors"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/media/hls"

	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/streaming/channels"
)

// Encrypted HLS of channels (encrypt, I136). A channel publishes its
// packets clear; fib's HLS Muxer encrypts what it cuts of it (fib request
// A69), its segments and its Low-Latency parts, AES-128 (METHOD=AES-128)
// under keys of the channel's library, a key every key_rotation segments,
// and names them in its media playlists; the hls handler gives each Muxer
// of an encrypted channel its Encryption as it starts, and serves the keys
// to whoever its auth lets play the channel:
//
//   - a key, key-<session>-<epoch>.key beside the segments, is derived from
//     the library's secret (medialib.Keys, KeyChannel, of <stream>/<session>
//     and the epoch), as the Muxer asks for it, on the library's file pool,
//     where the Muxer's encryption runs too.
//   - a key is served for a session the stream's Muxer runs, and an epoch no
//     later than that of the segment it cuts, so that a viewer cannot fetch
//     the keys of segments not yet cut; by a handler with auth alone, since
//     without it anyone could take the keys.
//   - a Muxer started under another encryption than the channel's now (a
//     reload turned encrypt on or off, or changed its library, secret or
//     key_rotation) is ended at the next playlist request, and starts again
//     under the new one, so that a channel is never served clear past the
//     reload that encrypts it.
//
// <session> is the Muxer's, which a restart of the stream's segmenting
// changes, so that its keys and IVs never repeat.

// errNoKeys is the error of an encrypted channel whose library, or its
// keys, are not loaded: fib's handler answers its playlist 503.
var errNoKeys = errors.New("streaming: the channel's media library, and its keys, are not loaded")

// encLibs are the libraries an hls handler's channels are encrypted with,
// found once and kept until a reload removes them.
type encLibs struct{ m sync.Map } // name -> *medialib.Library

// library returns the library name of the handler's engine, or nil.
func (sh *hlsShared) library(name string) *medialib.Library {
	if v, ok := sh.libs.m.Load(name); ok {
		if l := v.(*medialib.Library); !l.Removed() {
			return l
		}
	}
	l := medialib.Lookup(sh.key.engine, name)
	if l != nil {
		sh.libs.m.Store(name, l)
	}
	return l
}

// encStart is an encryption a Muxer of a stream starts with: the channel's,
// under keys of an ID.
type encStart struct {
	enc  channels.Encryption
	keys string
}

// want returns the encryption a Muxer of stream starts with now, and false
// for none.
func (sh *hlsShared) want(stream string) (encStart, bool) {
	enc, ok := channels.Lookup(sh.key.engine).Encryption(stream)
	if !ok {
		return encStart{}, false
	}
	keys := ""
	if l := sh.library(enc.Library); l != nil {
		keys = l.Keys().ID()
	}
	return encStart{enc: enc, keys: keys}, true
}

// encrypt is fib's HandlerConfig.Encrypt: the Encryption of a Muxer of the
// stream at path as it starts, nil for a stream that is not a channel with
// encrypt, errNoKeys (503) when the channel's library or its keys are not
// loaded. fib's handler calls it with its lock held: it calls no Handler.
func (sh *hlsShared) encrypt(path string) (*hls.Encryption, error) {
	enc, ok := channels.Lookup(sh.key.engine).Encryption(path)
	if !ok {
		return nil, nil
	}
	l := sh.library(enc.Library)
	var keys *medialib.Keys
	if l != nil {
		keys = l.Keys()
	}
	if keys == nil {
		return nil, errNoKeys
	}
	sh.started.Store(path, encStart{enc: enc, keys: keys.ID()})
	return &hls.Encryption{
		Rotation: enc.Rotation,
		Key: func(session string, epoch uint64) ([16]byte, string, error) {
			return keys.Key(medialib.KeyChannel, path+"/"+session, epoch), keyName(session, epoch), nil
		},
		// On the library's pool, as the segments it reads are; a job that
		// waited past file_timeout runs at once on a goroutine of its own,
		// since the Muxer lists nothing until it has run.
		Run: func(job func()) bool { return l.Submit(job, func() { go job() }) },
	}, nil
}

// restartStale ends the Muxers of stream when they were started under
// another encryption than its channel's now, so that fib's handler starts
// them again under it: called for a playlist request, before fib's.
func (sh *hlsShared) restartStale(fh *hls.Handler, stream string) {
	want, encrypted := sh.want(stream)
	v, started := sh.started.Load(stream)
	if started == encrypted && (!started || v.(encStart) == want) {
		return
	}
	var muxers []*hls.Muxer
	for _, f := range []hls.Format{hls.FMP4, hls.MPEGTS} {
		if m := fh.FormatMuxer(stream, f.Name); m != nil {
			muxers = append(muxers, m)
		}
	}
	// A Muxer fib's handler shows was started, its encryption stored,
	// before: what is stored now tells whether a request just started
	// it under what is wanted.
	v, started = sh.started.Load(stream)
	if started == encrypted && (!started || v.(encStart) == want) {
		return
	}
	if started {
		sh.started.CompareAndDelete(stream, v)
	}
	for _, m := range muxers {
		m.Close()
	}
}

// keyName is the name of key epoch of session.
func keyName(session string, epoch uint64) string {
	return "key-" + session + "-" + strconv.FormatUint(epoch, 10) + ".key"
}

// parseKey parses a key's name, key-<session>-<epoch>.key.
func parseKey(name string) (session string, epoch uint64, ok bool) {
	rest, ok1 := strings.CutPrefix(name, "key-")
	rest, ok2 := strings.CutSuffix(rest, ".key")
	i := strings.LastIndexByte(rest, '-')
	if !ok1 || !ok2 || i <= 0 {
		return "", 0, false
	}
	epoch, err := strconv.ParseUint(rest[i+1:], 10, 64)
	return rest[:i], epoch, err == nil
}

// serveKey serves name of stream when it is a key of a channel that
// encrypts its HLS, and reports whether it did.
func (h *HLS) serveKey(c *fibhttp.Context, fh *hls.Handler, stream, name string) bool {
	session, epoch, ok := parseKey(name)
	if !ok {
		return false
	}
	enc, encrypted := channels.Lookup(h.b.engine).Encryption(stream)
	if !encrypted {
		return false
	}
	if h.b.auth == nil {
		// Everyone could take the key: the handler serves none.
		h.respond(c, stdhttp.StatusForbidden, "the keys of an encrypted channel are served only by an hls handler with auth", "")
		return true
	}
	var keys *medialib.Keys
	if l := h.shared.library(enc.Library); l != nil {
		keys = l.Keys()
	}
	if keys == nil {
		h.respond(c, stdhttp.StatusServiceUnavailable, errNoKeys.Error(), "1")
		return true
	}
	// Of a session the stream's Muxer runs, and not past the segment it
	// cuts: a playlist has named no later key.
	m := fh.SessionMuxer(stream, session)
	if m == nil {
		h.respond(c, stdhttp.StatusNotFound, "no such key", "")
		return true
	}
	if cur, ok := m.KeyEpoch(); !ok || epoch > cur {
		h.respond(c, stdhttp.StatusNotFound, "no such key", "")
		return true
	}
	key := keys.Key(medialib.KeyChannel, stream+"/"+session, epoch)
	hdr := stdhttp.Header{"Content-Type": {"application/octet-stream"}, "Cache-Control": {"private, no-store"}}
	if h.CORS != "" {
		hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: key[:]})
	return true
}

// respond answers with status and a plain text message, with the handler's
// CORS, and Retry-After when retry is set.
func (h *HLS) respond(c *fibhttp.Context, status int, msg, retry string) {
	hdr := stdhttp.Header{}
	if h.CORS != "" {
		hdr["Access-Control-Allow-Origin"] = []string{h.CORS}
	}
	if retry != "" {
		hdr["Retry-After"] = []string{retry}
	}
	respond(c, status, msg, hdr)
}
