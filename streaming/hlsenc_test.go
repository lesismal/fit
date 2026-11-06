package streaming

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/streaming/channels"
)

// TestEncryptedNotLoaded answers 503 for an encrypted channel whose
// library, and keys, are not loaded, and leaves init sections and names
// it does not know to fib.
func TestEncryptedNotLoaded(t *testing.T) {
	h := &HLS{CORS: "*", shared: &hlsShared{enc: newEncCache(1 << 20)}}
	h.b.engine = new(fib.Engine)
	enc := channels.Encryption{Library: "none", Rotation: 1}
	served := true
	rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		served = h.serveEncrypted(c, nil, "live/x", "seg-s1-3.m4s", enc)
	}), httptest.NewRequest("GET", "/hls/live/x/seg-s1-3.m4s", nil))
	if !served || rec.Response.StatusCode != 503 || rec.Response.Header.Get("Retry-After") != "1" || rec.Response.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%t %d %v", served, rec.Response.StatusCode, rec.Response.Header)
	}
	for _, name := range []string{"init-s1-1.mp4", "index.m3u8", "media.m3u8"} {
		fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
			if h.serveEncrypted(c, nil, "live/x", name, enc) {
				t.Errorf("%s served", name)
			}
			respond(c, 204, "", nil)
		}), httptest.NewRequest("GET", "/hls/live/x/"+name, nil))
	}
}

const (
	encSecret  = "the library's secret of sixteen bytes"
	otherToken = "other-token-0123456789"
)

// encDoc is a config of a library with a secret, an encrypted channel
// live/sec, a key every rotation segments, and a clear one, live/tv; an hls
// route, and one of Low-Latency HLS; and a policy whose token other plays
// live/other alone.
func encDoc(t *testing.T, rotation int) string {
	root, err := filepath.Abs("../medialib/testdata/lib")
	if err != nil {
		t.Fatal(err)
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": [{"name": "live", "tokens": [{"name": "view", "token": "` + viewToken + `", "play": ["live/*"]},
    {"name": "other", "token": "` + otherToken + `", "play": ["live/other"]}]}],
  "media_library": [{"name": "lib", "roots": [` + fmt.Sprintf("%q", root) + `], "secret": "` + encSecret + `"}],
  "streaming": {
    "channels": [{"name": "live/sec", "library": "lib", "items": ["films/bbb"], "encrypt": true, "key_rotation": ` + strconv.Itoa(rotation) + `},
                 {"name": "live/tv", "library": "lib", "items": ["films/bbb"]}]
  },
  "routes": [
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "live", "target_duration": "1s", "cors": "*"}}},
    {"match": {"path": ["/ll/*"]}, "handler": {"module": "hls", "params": {"prefix": "/ll/", "auth": "live", "target_duration": "1s", "part_duration": "250ms"}}},
    {"match": {"path": ["/open/*"]}, "handler": {"module": "hls", "params": {"prefix": "/open/", "target_duration": "1s"}}}
  ]
}`
}

// hlsModule returns the running config's hls handler of prefix.
func (f *fit) hlsModule(t *testing.T, prefix string) *HLS {
	t.Helper()
	g := f.rt.Current()
	defer g.Release()
	for _, m := range g.Modules() {
		if h, ok := m.(*HLS); ok && h.b.prefix == prefix {
			return h
		}
	}
	t.Fatalf("no hls handler of %s", prefix)
	return nil
}

var keyTag = regexp.MustCompile(`#EXT-X-KEY:METHOD=AES-128,URI="(key-[^"?]+)\?token=` + viewToken + `"`)

// TestChannelEncrypted plays an encrypted channel over HLS: its media
// playlists name a key every two segments, after any init section, and
// without Low-Latency HLS's tags; each segment, fMP4 and MPEG-TS, decrypts
// with its key into the Muxer's own, and alone does not; a key is refused
// without credentials, to another stream's token, past the next segment,
// and of a session the stream does not run; parts are refused; and a clear
// channel stays clear.
func TestChannelEncrypted(t *testing.T) {
	f := startFit(t, encDoc(t, 2))
	f.waitLive(t, "live/sec")
	tok := "?token=" + viewToken
	keys, err := medialib.NewKeys("lib", encSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"", "&format=ts"} {
		var media string
		waitFor(t, "four segments"+format, func() bool {
			resp, body := get(t, f.base+"/hls/live/sec/media.m3u8"+tok+format, 1<<16, nil)
			media = string(body)
			return resp.StatusCode == 200 && strings.Count(media, "#EXTINF") >= 4
		})
		tags := keyTag.FindAllStringSubmatch(media, -1)
		if len(tags) < 2 {
			t.Fatalf("the keys of\n%s", media)
		}
		if m := strings.Index(media, "#EXT-X-MAP"); m >= 0 && m > strings.Index(media, "#EXT-X-KEY") {
			t.Fatalf("the init section after a key:\n%s", media)
		}
		fh := f.hlsModule(t, "/hls/").shared.handler(f.role().Hub())
		name := "fmp4"
		if format != "" {
			name = "ts"
		}
		mux := fh.FormatMuxer("live/sec", name)
		var key []byte
		lines := strings.Split(media, "\n")
		for _, l := range lines {
			if m := keyTag.FindStringSubmatch(l); m != nil {
				resp, body := get(t, f.base+"/hls/live/sec/"+m[1]+tok, 64, nil)
				if resp.StatusCode != 200 || len(body) != 16 || resp.Header.Get("Cache-Control") != "private, no-store" {
					t.Fatalf("the key %s: %d %v", m[1], resp.StatusCode, resp.Header)
				}
				key = body
				continue
			}
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			seg, _, _ := strings.Cut(l, "?")
			fn, ok := parseFile(seg)
			if !ok || fn.kind != "seg" {
				t.Fatalf("a segment %q", l)
			}
			want := keys.Key(medialib.KeyChannel, "live/sec/"+fn.session, fn.n/2)
			if !bytes.Equal(key, want[:]) {
				t.Fatalf("%s under the key %x, not %x", seg, key, want)
			}
			resp, enc := get(t, f.base+"/hls/live/sec/"+l, 64<<20, nil)
			plain, ok := mux.File(seg)
			if resp.StatusCode != 200 || !ok || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("%s: %d, in the Muxer %t", seg, resp.StatusCode, ok)
			}
			if bytes.Equal(enc[:16], plain[:16]) {
				t.Fatalf("%s is clear", seg)
			}
			if got := decryptSegment(t, key, fn.n, enc); !bytes.Equal(got, plain) {
				t.Fatalf("%s decrypts into %d bytes, not the Muxer's %d", seg, len(got), len(plain))
			}
		}
		// Refused: without credentials, to another stream's token, past the
		// next segment, and of a session not run.
		m := tags[0][1]
		if resp, _ := get(t, f.base+"/hls/live/sec/"+m, 64, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a key without credentials: %d", resp.StatusCode)
		}
		if resp, _ := get(t, f.base+"/hls/live/sec/"+m+"?token="+otherToken, 64, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a key to another stream's token: %d", resp.StatusCode)
		}
		fn, _ := parseFile(m)
		if resp, _ := get(t, f.base+"/hls/live/sec/"+keyName(fn.session, fn.n+1000)+tok, 64, nil); resp.StatusCode != 404 {
			t.Fatalf("a key of the future: %d", resp.StatusCode)
		}
		if resp, _ := get(t, f.base+"/hls/live/sec/"+keyName("nosuch", 0)+tok, 64, nil); resp.StatusCode != 404 {
			t.Fatalf("a key of another session: %d", resp.StatusCode)
		}
	}
	if resp, _ := get(t, f.base+"/hls/live/sec/seg-nosuch-0.m4s"+tok, 64, nil); resp.StatusCode != 404 {
		t.Fatalf("a segment not there: %d", resp.StatusCode)
	}

	// Low-Latency HLS: no parts named, nor served; the key tags all the same.
	var ll string
	waitFor(t, "Low-Latency segments", func() bool {
		resp, body := get(t, f.base+"/ll/live/sec/media.m3u8"+tok, 1<<16, nil)
		ll = string(body)
		return resp.StatusCode == 200 && strings.Count(ll, "#EXTINF") >= 2
	})
	if strings.Contains(ll, "#EXT-X-PART") || strings.Contains(ll, "PRELOAD-HINT") || !strings.Contains(ll, "#EXT-X-KEY") {
		t.Fatalf("the Low-Latency playlist:\n%s", ll)
	}
	seg := regexp.MustCompile(`seg-([^-]+)-(\d+)`).FindStringSubmatch(ll)
	if resp, _ := get(t, f.base+"/ll/live/sec/part-"+seg[1]+"-"+seg[2]+"-0.m4s"+tok, 64, nil); resp.StatusCode != 404 {
		t.Fatalf("a part: %d", resp.StatusCode)
	}

	// A handler without auth serves no key.
	var open string
	waitFor(t, "the open handler's playlist", func() bool {
		resp, body := get(t, f.base+"/open/live/sec/media.m3u8", 1<<16, nil)
		open = string(body)
		return resp.StatusCode == 200 && strings.Contains(open, "#EXTINF")
	})
	k := regexp.MustCompile(`key-[^"]+\.key`).FindString(open)
	if resp, _ := get(t, f.base+"/open/live/sec/"+k, 64, nil); k == "" || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a key %q of a handler without auth: %d", k, resp.StatusCode)
	}

	// The clear channel stays clear.
	waitFor(t, "the clear channel", func() bool {
		resp, body := get(t, f.base+"/hls/live/tv/media.m3u8"+tok, 1<<16, nil)
		return resp.StatusCode == 200 && strings.Contains(string(body), "#EXTINF") && !strings.Contains(string(body), "EXT-X-KEY")
	})
	if b := f.hlsModule(t, "/hls/").shared.enc.bytes(); b <= 0 || b > 32<<20 {
		t.Fatalf("the cache holds %d bytes", b)
	}
}

// decryptSegment is a player's AES-128 of segment n under key.
func decryptSegment(t *testing.T, key []byte, n uint64, data []byte) []byte {
	t.Helper()
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		t.Fatalf("an encrypted segment of %d bytes", len(data))
	}
	block, _ := aes.NewCipher(key)
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], n)
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, data)
	return out[:len(out)-int(out[len(out)-1])]
}

// TestWithKeys rotates keys at their epochs, clears them before an init
// section that follows one, and leaves a multivariant playlist as it is.
func TestWithKeys(t *testing.T) {
	pl := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES\n#EXT-X-PART-INF:PART-TARGET=0.25\n#EXT-X-MEDIA-SEQUENCE:3\n" +
		"#EXT-X-MAP:URI=\"init-s1-1.mp4\"\n#EXTINF:1.000,\nseg-s1-3.m4s\n#EXTINF:1.000,\nseg-s1-4.m4s\n" +
		"#EXT-X-DISCONTINUITY\n#EXT-X-MAP:URI=\"init-s1-2.mp4\"\n#EXT-X-PART:DURATION=0.250,URI=\"part-s1-5-0.m4s\"\n#EXTINF:1.000,\nseg-s1-5.m4s\n#EXTINF:1.000,\nseg-s1-6.m4s\n" +
		"#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"part-s1-7-0.m4s\"\n"
	want := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:3\n" +
		"#EXT-X-MAP:URI=\"init-s1-1.mp4\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"key-s1-1.key\"\n#EXTINF:1.000,\nseg-s1-3.m4s\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key-s1-2.key\"\n#EXTINF:1.000,\nseg-s1-4.m4s\n" +
		"#EXT-X-DISCONTINUITY\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-MAP:URI=\"init-s1-2.mp4\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"key-s1-2.key\"\n#EXTINF:1.000,\nseg-s1-5.m4s\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key-s1-3.key\"\n#EXTINF:1.000,\nseg-s1-6.m4s\n"
	if got := string(withKeys([]byte(pl), 2)); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	master := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n"
	if got := string(withKeys([]byte(master), 2)); got != master {
		t.Fatalf("the multivariant playlist: %s", got)
	}
	for _, bad := range []string{"seg", "seg-1", "seg-s-x.m4s", "nope-s-1.m4s", "seg--1.m4s", "seg-s.m4s"} {
		if _, ok := parseFile(bad); ok {
			t.Errorf("%q parses", bad)
		}
	}
	if f, ok := parseFile("part-s9-12-3.m4s"); !ok || f.kind != "part" || f.n != 12 || f.session != "s9" {
		t.Errorf("a part: %+v", f)
	}
}

// TestEncCache keeps a segment once for every request that waits for it,
// within its bound, and forgets one that failed.
func TestEncCache(t *testing.T) {
	c := newEncCache(4096)
	var built int
	var pending func([]byte, error)
	got := make([][]byte, 0, 3)
	for range 3 {
		c.get(encKey{name: "a"}, 1000, func(done func([]byte, error)) { built++; pending = done }, func(b []byte, err error) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, b)
		})
	}
	pending(make([]byte, 1000), nil)
	if built != 1 || len(got) != 3 {
		t.Fatalf("built %d times, %d answers", built, len(got))
	}
	c.get(encKey{name: "a"}, 1000, func(func([]byte, error)) { t.Fatal("built again") }, func([]byte, error) {})
	// Too large for the cache.
	c.get(encKey{name: "big"}, 8192, func(func([]byte, error)) { t.Fatal("built") }, func(_ []byte, err error) {
		if err != errEncFull {
			t.Fatalf("%v", err)
		}
	})
	// Others push the first out; a failure keeps nothing.
	for _, n := range []string{"b", "c", "d"} {
		c.get(encKey{name: n}, 1000, func(done func([]byte, error)) { done(make([]byte, 1000), nil) }, func([]byte, error) {})
	}
	if c.bytes() > 4096 {
		t.Fatalf("%d bytes", c.bytes())
	}
	before := c.bytes()
	c.get(encKey{name: "e"}, 100, func(done func([]byte, error)) { done(nil, medialib.ErrBusy) }, func([]byte, error) {})
	if c.bytes() != before {
		t.Fatalf("a failure holds %d bytes", c.bytes()-before)
	}
	// Past maxEncWaiters.
	c.get(encKey{name: "w"}, 10, func(func([]byte, error)) {}, func([]byte, error) {})
	for range maxEncWaiters - 1 {
		c.get(encKey{name: "w"}, 10, nil, func([]byte, error) {})
	}
	var refused error
	c.get(encKey{name: "w"}, 10, nil, func(_ []byte, err error) { refused = err })
	if refused != errEncWaiters {
		t.Fatalf("%v", refused)
	}
}
