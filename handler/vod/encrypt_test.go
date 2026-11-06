package vod

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/medialib"
	"github.com/lesismal/fit/runtime"
)

// libSecret is the secret of the encrypted tests' library.
const libSecret = "the library's secret, 16 bytes or more"

// encrypted is the library's params of a secret.
const encrypted = `, "secret": "` + libSecret + `"`

// decrypt is a player's AES-128 of segment n under key.
func decrypt(t *testing.T, key []byte, n uint64, data []byte) []byte {
	t.Helper()
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		t.Fatalf("an encrypted segment of %d bytes", len(data))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	var iv [16]byte
	binary.BigEndian.PutUint64(iv[8:], n)
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		t.Fatalf("segment %d: bad padding %d", n, pad)
	}
	return out[:len(out)-pad]
}

// TestEncrypt plays an encrypted item through a signed URL: its media
// playlists name the key after the init section, the key is served to the
// grant alone, and its segments, fMP4 and MPEG-TS, decrypt with it into the
// clear handler's own; while the segments alone do not decode, a key is
// refused without a grant or with another item's, and the files are not
// served.
func TestEncrypt(t *testing.T) {
	root := copyLib(t)
	clear := startVOD(t, root, "", `{"library": "films"}`, "")
	f := startVOD(t, root, encrypted, `{"library": "films", "auth": "site", "encrypt": true, "cache_control": "public, max-age=60"}`, signedConf(`{}`))
	viewer := mediaauth.Viewer{Subject: "viewer-1", Session: "s1"}
	u := f.sign(t, "index.m3u8", viewer, time.Now().Add(time.Hour), "")
	master := string(f.mustGet(t, u, 200))
	if strings.Contains(master, "EXT-X-KEY") {
		t.Fatalf("the master names a key:\n%s", master)
	}
	grant := strings.SplitN(uris(master)[0], "?", 2)[1]

	keys, err := medialib.NewKeys("films", libSecret)
	if err != nil {
		t.Fatal(err)
	}
	want := keys.Key(medialib.KeyItem, "films/movie", 0)
	resp, key := f.get(t, movie+"aes.key?"+grant)
	if resp.StatusCode != 200 || !bytes.Equal(key, want[:]) || resp.Header.Get("Cache-Control") != "private, no-store" ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("the key: %d %v %x", resp.StatusCode, resp.Header, key)
	}
	other := keys.Key(medialib.KeyItem, "clips/hevc", 0)
	if bytes.Equal(other[:], key) {
		t.Fatal("two items share a key")
	}

	for _, c := range []string{"fmp4", "ts"} {
		media := string(f.mustGet(t, movie+"v0."+c+".m3u8?"+grant, 200))
		k := strings.Index(media, `#EXT-X-KEY:METHOD=AES-128,URI="aes.key?`+grant+`"`)
		if k < 0 || strings.Count(media, "#EXT-X-KEY") != 1 || strings.Index(media, "#EXT-X-MAP") > k || strings.Index(media, "#EXTINF") < k {
			t.Fatalf("the %s playlist:\n%s", c, media)
		}
		ext := ".m4s"
		if c == "ts" {
			ext = ".ts"
		}
		for _, n := range []uint64{0, 1} {
			name := "v0." + string(rune('0'+n)) + ext
			resp, enc := f.get(t, movie+name+"?"+grant)
			plain := clear.mustGet(t, movie+name, 200)
			if resp.StatusCode != 200 || bytes.Equal(enc[:16], plain[:16]) {
				t.Fatalf("%s: %d, its first bytes clear", name, resp.StatusCode)
			}
			if got := decrypt(t, key, n, enc); !bytes.Equal(got, plain) {
				t.Fatalf("%s decrypts into %d bytes, not the clear %d", name, len(got), len(plain))
			}
			// Under another IV it does not decrypt into the segment.
			if bytes.Equal(enc, plain) || resp.Header.Get("Cache-Control") != "public, max-age=60" {
				t.Fatalf("%s: %v", name, resp.Header)
			}
			clearResp, _ := clear.get(t, movie+name)
			if resp.Header.Get("Etag") == "" || resp.Header.Get("Etag") == clearResp.Header.Get("Etag") {
				t.Fatalf("%s: the ETag %q of the clear one", name, resp.Header.Get("Etag"))
			}
		}
	}
	// The init section and subtitles are clear.
	if init := f.mustGet(t, movie+"v0.init.mp4?"+grant, 200); string(init[4:8]) != "ftyp" {
		t.Fatalf("the init section: %q", init[:8])
	}

	// The key without a grant, with a grant for another item, and of an
	// item not encrypted.
	if resp, _ := f.get(t, movie+"aes.key"); resp.StatusCode != 401 {
		t.Fatalf("the key without a grant: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/vod/films/clips/hevc/aes.key?"+grant); resp.StatusCode != 403 {
		t.Fatalf("another item's key: %d", resp.StatusCode)
	}
	if resp, _ := clear.get(t, movie+"aes.key"); resp.StatusCode != 404 {
		t.Fatalf("a clear item's key: %d", resp.StatusCode)
	}
	// The files are not served; the poster is.
	if resp, _ := f.get(t, movie+"v0.mp4?"+grant); resp.StatusCode != 404 {
		t.Fatalf("an encrypted item's file: %d", resp.StatusCode)
	}
}

// TestEncryptTokens serves the key to a token that may play the item, the
// grant passed on as its playlists pass it.
func TestEncryptTokens(t *testing.T) {
	f := startVOD(t, copyLib(t), encrypted, `{"library": "films", "auth": "site", "encrypt": true, "container": "ts"}`, authConf)
	media := string(f.mustGet(t, movie+"v0.ts.m3u8?token="+viewToken, 200))
	if !strings.Contains(media, `URI="aes.key?token=`+viewToken+`"`) {
		t.Fatalf("the playlist:\n%s", media)
	}
	if key := f.mustGet(t, movie+"aes.key", 200, "Authorization", "Bearer "+viewToken); len(key) != 16 {
		t.Fatalf("the key: %x", key)
	}
	if resp, _ := f.get(t, movie+"aes.key", "Authorization", "Bearer nope"); resp.StatusCode != 403 && resp.StatusCode != 401 {
		t.Fatalf("a bad token: %d", resp.StatusCode)
	}
}

// TestEncryptSecretChanged makes new keys of a new secret, and a new cache
// with new ETags, on a reload.
func TestEncryptSecretChanged(t *testing.T) {
	root := copyLib(t)
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("the first secret of the library\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := `, "secret_file": ` + quote(file)
	params := `{"library": "films", "auth": "site", "encrypt": true}`
	f := startVOD(t, root, extra, params, authConf)
	bearer := []string{"Authorization", "Bearer " + viewToken}
	key1 := f.mustGet(t, movie+"aes.key", 200, bearer...)
	resp1, seg1 := f.get(t, movie+"v0.0.m4s", bearer...)
	if err := os.WriteFile(file, []byte("the second secret of the library"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.swap(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [`+libConf(root, extra)+`],
  "media_auth": `+authConf+`,
  "routes": [{"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": `+params+`}}]
}`)
	key2 := f.mustGet(t, movie+"aes.key", 200, bearer...)
	resp2, seg2 := f.get(t, movie+"v0.0.m4s", bearer...)
	if bytes.Equal(key1, key2) || bytes.Equal(seg1, seg2) || resp1.Header.Get("Etag") == resp2.Header.Get("Etag") {
		t.Fatalf("the same key, segment or ETag after the secret changed: %x %x", key1, key2)
	}
	if !bytes.Equal(decrypt(t, key1, 0, seg1), decrypt(t, key2, 0, seg2)) {
		t.Fatal("the segment is another")
	}
	if lk := f.lib.Keys(); lk == nil || lk.Key(medialib.KeyItem, "films/movie", 0) != [16]byte(key2) {
		t.Fatal("the library's keys are not the handler's")
	}
}

// TestEncryptProvision refuses encrypt without auth or a secret, and a
// secret that cannot be read or is too short.
func TestEncryptProvision(t *testing.T) {
	root := copyLib(t)
	short := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(short, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ lib, params, want string }{
		{encrypted, `{"library": "films", "encrypt": true}`, "encrypt needs auth"},
		{"", `{"library": "films", "auth": "site", "encrypt": true}`, "encrypt needs a key"},
		{`, "secret_file": "/nonexistent/secret"`, `{"library": "films", "auth": "site", "encrypt": true}`, "secret_file"},
		{`, "secret_file": ` + quote(short), `{"library": "films", "auth": "site", "encrypt": true}`, "at least 16"},
	} {
		c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": ":0"}],
  "media_library": [` + libConf(root, tc.lib) + `],
  "media_auth": ` + authConf + `,
  "routes": [{"handler": {"module": "vod", "params": ` + tc.params + `}}]}`))
		if err == nil {
			var g *runtime.Graph
			if g, err = runtime.Compile(c, runtime.Env{}); err == nil {
				g.Release()
			}
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: %v, want %q", tc.lib, tc.params, err, tc.want)
		}
	}
}
