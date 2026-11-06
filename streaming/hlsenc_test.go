package streaming

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/medialib"
)

const (
	encSecret  = "the library's secret of sixteen bytes"
	otherToken = "other-token-0123456789"
)

// encDoc is a config of a library with a secret, a channel live/sec whose
// params sec adds to (its encryption), and a clear one, live/tv; an hls
// route, one of Low-Latency HLS, and one without auth; and a policy whose
// token other plays live/other alone.
func encDoc(t *testing.T, sec string) string {
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
    "channels": [{"name": "live/sec", "library": "lib", "items": ["films/bbb"]` + sec + `},
                 {"name": "live/tv", "library": "lib", "items": ["films/bbb"]}]
  },
  "routes": [
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "live", "target_duration": "1s", "cors": "*"}}},
    {"match": {"path": ["/ll/*"]}, "handler": {"module": "hls", "params": {"prefix": "/ll/", "auth": "live", "target_duration": "1s", "part_duration": "250ms"}}},
    {"match": {"path": ["/open/*"]}, "handler": {"module": "hls", "params": {"prefix": "/open/", "target_duration": "1s"}}}
  ]
}`
}

// encrypted is encDoc's sec of an encrypted channel, a key every rotation
// segments.
func encrypted(rotation int) string {
	return `, "encrypt": true, "key_rotation": ` + strconv.Itoa(rotation)
}

var (
	keyTag   = regexp.MustCompile(`#EXT-X-KEY:METHOD=AES-128,URI="(key-[^"?]+)\?token=` + viewToken + `"`)
	partTag  = regexp.MustCompile(`#EXT-X-PART:[^\n]*URI="(part-([^-"]+)-(\d+)-\d+\.m4s)[^"]*"`)
	segName  = regexp.MustCompile(`^seg-([^-]+)-(\d+)\.(m4s|ts)$`)
	anyKey   = regexp.MustCompile(`key-[^"?]+\.key`)
	boxTypes = []string{"moof", "styp", "prft", "sidx"}
)

// clearMedia reports whether data is a clear fMP4 segment or part (a box
// it begins with), or MPEG-TS (packets of 188 bytes, each a sync byte).
func clearMedia(data []byte, ts bool) bool {
	if ts {
		return len(data) >= 376 && len(data)%188 == 0 && data[0] == 0x47 && data[188] == 0x47
	}
	if len(data) < 8 {
		return false
	}
	for _, b := range boxTypes {
		if string(data[4:8]) == b {
			return true
		}
	}
	return false
}

// TestChannelEncrypted plays an encrypted channel over HLS, encrypted by
// fib's Muxer: its media playlists name a key every two segments, after
// any init section; each segment, fMP4 and MPEG-TS, decrypts with its key
// and IV into clear media, and alone is not; a key is refused without
// credentials, to another stream's token, past the segment being cut, and
// of a session the stream does not run; Low-Latency HLS's parts are listed
// and encrypted too; a handler without auth serves no key; and a clear
// channel stays clear.
func TestChannelEncrypted(t *testing.T) {
	f := startFit(t, encDoc(t, encrypted(2)))
	f.waitLive(t, "live/sec")
	tok := "?token=" + viewToken
	keys, err := medialib.NewKeys("lib", encSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"", "&format=ts"} {
		ts := format != ""
		var media string
		waitFor(t, "four segments"+format, func() bool {
			resp, body := get(t, f.base+"/hls/live/sec/media.m3u8"+tok+format, 1<<16, nil)
			media = string(body)
			return resp.StatusCode == 200 && strings.Count(media, "#EXTINF") >= 4
		})
		tags := keyTag.FindAllStringSubmatch(media, -1)
		if len(tags) < 2 || tags[0][1] == tags[1][1] {
			t.Fatalf("the keys of\n%s", media)
		}
		if m := strings.Index(media, "#EXT-X-MAP"); m >= 0 && m > strings.Index(media, "#EXT-X-KEY") {
			t.Fatalf("the init section after a key:\n%s", media)
		}
		var key []byte
		for _, l := range strings.Split(media, "\n") {
			if m := keyTag.FindStringSubmatch(l); m != nil {
				resp, body := get(t, f.base+"/hls/live/sec/"+m[1]+tok, 64, nil)
				if resp.StatusCode != 200 || len(body) != 16 || resp.Header.Get("Cache-Control") != "private, no-store" ||
					resp.Header.Get("Access-Control-Allow-Origin") != "*" {
					t.Fatalf("the key %s: %d %v", m[1], resp.StatusCode, resp.Header)
				}
				key = body
				continue
			}
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			seg, _, _ := strings.Cut(l, "?")
			m := segName.FindStringSubmatch(seg)
			if m == nil {
				t.Fatalf("a segment %q", l)
			}
			msn, _ := strconv.ParseUint(m[2], 10, 64)
			want := keys.Key(medialib.KeyChannel, "live/sec/"+m[1], msn/2)
			if !bytes.Equal(key, want[:]) {
				t.Fatalf("%s under the key %x, not %x", seg, key, want)
			}
			resp, enc := get(t, f.base+"/hls/live/sec/"+l, 64<<20, nil)
			if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("%s: %d", seg, resp.StatusCode)
			}
			if clearMedia(enc, ts) {
				t.Fatalf("%s is clear", seg)
			}
			if got := decrypt(t, key, msn, enc); !clearMedia(got, ts) {
				t.Fatalf("%s does not decrypt into clear media: % x", seg, got[:min(16, len(got))])
			}
		}
		// Refused: without credentials, to another stream's token, past the
		// segment being cut, and of a session not run.
		m := tags[0][1]
		if resp, _ := get(t, f.base+"/hls/live/sec/"+m, 64, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("a key without credentials: %d", resp.StatusCode)
		}
		if resp, _ := get(t, f.base+"/hls/live/sec/"+m+"?token="+otherToken, 64, nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a key to another stream's token: %d", resp.StatusCode)
		}
		session, epoch, ok := parseKey(m)
		if !ok {
			t.Fatalf("the key's name %q", m)
		}
		if resp, _ := get(t, f.base+"/hls/live/sec/"+keyName(session, epoch+1000)+tok, 64, nil); resp.StatusCode != 404 {
			t.Fatalf("a key of the future: %d", resp.StatusCode)
		}
		if resp, _ := get(t, f.base+"/hls/live/sec/"+keyName("nosuch", 0)+tok, 64, nil); resp.StatusCode != 404 {
			t.Fatalf("a key of another session: %d", resp.StatusCode)
		}
	}
	if resp, _ := get(t, f.base+"/hls/live/sec/seg-nosuch-0.m4s"+tok, 64, nil); resp.StatusCode != 404 {
		t.Fatalf("a segment not there: %d", resp.StatusCode)
	}
	// A key's name of a clear stream is fib's: not found.
	if resp, _ := get(t, f.base+"/hls/live/tv/key-x-0.key"+tok, 64, nil); resp.StatusCode != 404 {
		t.Fatalf("a key of a clear channel: %d", resp.StatusCode)
	}

	// Low-Latency HLS: parts named and served, each encrypted on its own
	// with its segment's key and IV.
	var ll string
	waitFor(t, "Low-Latency parts", func() bool {
		resp, body := get(t, f.base+"/ll/live/sec/media.m3u8"+tok, 1<<16, nil)
		ll = string(body)
		return resp.StatusCode == 200 && strings.Count(ll, "#EXTINF") >= 2 && partTag.MatchString(ll)
	})
	if !strings.Contains(ll, "#EXT-X-KEY") || strings.Index(ll, "#EXT-X-KEY") > strings.Index(ll, "#EXT-X-PART:") {
		t.Fatalf("the Low-Latency playlist:\n%s", ll)
	}
	parts := partTag.FindAllStringSubmatch(ll, -1)
	p := parts[len(parts)-1]
	msn, _ := strconv.ParseUint(p[3], 10, 64)
	resp, enc := get(t, f.base+"/ll/live/sec/"+p[1]+tok, 16<<20, nil)
	if resp.StatusCode != 200 || clearMedia(enc, false) {
		t.Fatalf("the part %s: %d, clear %t", p[1], resp.StatusCode, clearMedia(enc, false))
	}
	resp, key := get(t, f.base+"/ll/live/sec/"+keyName(p[2], msn/2)+tok, 64, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the part's key: %d", resp.StatusCode)
	}
	if got := decrypt(t, key, msn, enc); !clearMedia(got, false) {
		t.Fatalf("the part %s does not decrypt into clear media", p[1])
	}

	// A handler without auth serves no key, and its segments are
	// encrypted all the same.
	var open string
	waitFor(t, "the open handler's playlist", func() bool {
		resp, body := get(t, f.base+"/open/live/sec/media.m3u8", 1<<16, nil)
		open = string(body)
		return resp.StatusCode == 200 && strings.Contains(open, "#EXTINF")
	})
	k := anyKey.FindString(open)
	if resp, _ := get(t, f.base+"/open/live/sec/"+k, 64, nil); k == "" || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a key %q of a handler without auth: %d", k, resp.StatusCode)
	}

	// The clear channel stays clear.
	waitFor(t, "the clear channel", func() bool {
		resp, body := get(t, f.base+"/hls/live/tv/media.m3u8"+tok, 1<<16, nil)
		return resp.StatusCode == 200 && strings.Contains(string(body), "#EXTINF") && !strings.Contains(string(body), "EXT-X-KEY")
	})
}

// decrypt is a player's AES-128 of segment or part msn under key.
func decrypt(t *testing.T, key []byte, msn uint64, data []byte) []byte {
	t.Helper()
	if len(key) != 16 || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		t.Fatalf("a key of %d bytes, an encrypted segment of %d bytes", len(key), len(data))
	}
	block, _ := aes.NewCipher(key)
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], msn)
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize {
		t.Fatalf("the padding %d", pad)
	}
	return out[:len(out)-pad]
}

// sessionOf returns the session of the first segment a media playlist
// names, and whether it names a key.
func sessionOf(pl string) (string, bool) {
	for _, l := range strings.Split(pl, "\n") {
		seg, _, _ := strings.Cut(l, "?")
		if m := segName.FindStringSubmatch(seg); m != nil {
			return m[1], strings.Contains(pl, "#EXT-X-KEY:METHOD=AES-128")
		}
	}
	return "", false
}

// TestEncryptedReload restarts a channel's segmenting when a reload changes
// its encryption, so that it is never served clear past the reload that
// encrypts it, nor under the keys of another rotation; and answers 503 for
// its playlists and keys once its library is not loaded.
func TestEncryptedReload(t *testing.T) {
	f := startFit(t, encDoc(t, ""))
	f.waitLive(t, "live/sec")
	tok := "?token=" + viewToken
	playlist := func(what string, encrypted bool) string {
		t.Helper()
		var session string
		waitFor(t, what, func() bool {
			resp, body := get(t, f.base+"/hls/live/sec/media.m3u8"+tok, 1<<16, nil)
			s, keyed := sessionOf(string(body))
			session = s
			return resp.StatusCode == 200 && s != "" && keyed == encrypted
		})
		return session
	}
	clear := playlist("the clear playlist", false)
	f.reload(t, encDoc(t, encrypted(2)))
	enc := playlist("the encrypted playlist", true)
	if enc == clear {
		t.Fatalf("the session %s carried on", enc)
	}
	f.reload(t, encDoc(t, encrypted(3)))
	rotated := playlist("the playlist of the new rotation", true)
	if rotated == enc {
		t.Fatalf("the session %s carried on under another rotation", rotated)
	}
	if again := playlist("the same playlist", true); again != rotated {
		t.Fatalf("the session %s restarted as %s with nothing changed", rotated, again)
	}
	f.reload(t, encDoc(t, ""))
	if again := playlist("the playlist cleared", false); again == rotated {
		t.Fatalf("the session %s carried on cleared", again)
	}

	// The library unloaded: its playlists and keys are 503, never clear.
	f.reload(t, encDoc(t, encrypted(2)))
	session := playlist("the encrypted playlist again", true)
	if err := medialib.Apply(f.up, nil, nil); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(t, f.base+"/hls/live/sec/media.m3u8"+tok, 1<<16, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a playlist without the library: %d", resp.StatusCode)
	}
	resp, _ := get(t, f.base+"/hls/live/sec/"+keyName(session, 0)+tok, 64, nil)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("a key without the library: %d", resp.StatusCode)
	}
}

// TestEncryptNotAChannel gives a stream that is not an encrypted channel no
// Encryption, and parses keys' names.
func TestEncryptNotAChannel(t *testing.T) {
	sh := &hlsShared{key: hlsKey{engine: new(fib.Engine)}}
	if enc, err := sh.encrypt("live/x"); enc != nil || err != nil {
		t.Fatalf("%v %v", enc, err)
	}
	if _, ok := sh.want("live/x"); ok {
		t.Fatal("an encryption wanted")
	}
	if sh.library("lib") != nil {
		t.Fatal("a library of no engine")
	}
	for _, bad := range []string{"key", "key-1.key", "key-s-x.key", "nope-s-1.key", "key--1.key", "key-s-1.bin", "key-s-1"} {
		if _, _, ok := parseKey(bad); ok {
			t.Errorf("%q parses", bad)
		}
	}
	if s, e, ok := parseKey(keyName("s9a", 12)); !ok || s != "s9a" || e != 12 {
		t.Errorf("%s %d %t", s, e, ok)
	}
}
