package vod

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInteropEncrypted is TestInterop's check of encrypt: ffmpeg plays
// every item of the generated library, both containers, its segments
// encrypted and its key fetched with the token its playlists hand on, every
// frame of the file; while ffprobe reads nothing of a segment fetched
// alone, after its init section or not, and the key is refused without the
// token. Like TestInterop, it runs only with FIT_MEDIA_INTEROP set.
func TestInteropEncrypted(t *testing.T) {
	requireFFmpeg(t)
	root := t.TempDir()
	script, err := filepath.Abs("../../test/docker/vod/genlib.sh")
	if err != nil {
		t.Fatal(err)
	}
	run(t, false, "sh", script, root)
	f := startVOD(t, root, encrypted, `{"library": "films", "auth": "site", "encrypt": true}`,
		`[{"name": "site", "tokens": [{"name": "viewer", "token": "`+viewToken+`", "play": ["vod/films/*"]}]}]`)
	t.Log(strings.SplitN(run(t, true, "ffmpeg", "-hide_banner", "-version"), "\n", 2)[0])
	work := t.TempDir()
	tok := "?token=" + viewToken
	for _, it := range f.lib.Catalog().Items {
		if it.Error != "" {
			t.Fatalf("%s: %s", it.ID, it.Error)
		}
		base := f.base + "/vod/films/" + it.ID + "/"
		for _, c := range []string{"fmp4", "ts"} {
			t.Run(it.ID+"/"+c, func(t *testing.T) {
				if resp, _ := f.get(t, "/vod/films/"+it.ID+"/index."+c+".m3u8"+tok); resp.StatusCode == 404 {
					t.Skip("not carried in " + c)
				}
				run(t, true, "ffmpeg", "-hide_banner", "-v", "error", "-i", base+"index."+c+".m3u8"+tok, "-map", "0:v?", "-map", "0:a?", "-f", "null", "-")
				for _, ref := range append(refs(it.Renditions, "v"), refs(it.Audio, "a")...) {
					media := base + ref.ref + "." + c + ".m3u8" + tok
					src := filepath.Join(ref.root, filepath.FromSlash(ref.path))
					if _, frame := probeStreams(t, src); frame > 0 {
						want, _ := countFrames(t, true, "-i", src)
						if got, _ := countFrames(t, true, "-i", media); got != want {
							t.Errorf("%s: %d frames played, the file has %d", ref.ref, got, want)
						}
					}
					undecodable(t, f, work, it.ID, ref.ref, c)
				}
			})
		}
		if resp, _ := f.get(t, "/vod/films/"+it.ID+"/aes.key"); resp.StatusCode != 401 {
			t.Errorf("%s: the key without the token: %d", it.ID, resp.StatusCode)
		}
	}
}

// undecodable checks that ffprobe reads no packet of each segment of a
// media playlist fetched alone, after its initialization section for fMP4,
// as a copy of the segments would be read without the key.
func undecodable(t *testing.T, f *fit, work, id, ref, c string) {
	t.Helper()
	base := "/vod/films/" + id + "/"
	tok := "?token=" + viewToken
	uris, _, initURI := segmentURIs(t, f.mustGet(t, base+ref+"."+c+".m3u8"+tok, 200))
	var init []byte
	if initURI != "" {
		init = f.mustGet(t, base+strings.SplitN(initURI, "?", 2)[0]+tok, 200)
	}
	for n, u := range uris {
		seg := f.mustGet(t, base+strings.SplitN(u, "?", 2)[0]+tok, 200)
		p := filepath.Join(work, strings.ReplaceAll(id, "/", "_")+"-"+ref+"-enc."+c)
		if err := os.WriteFile(p, append(append([]byte{}, init...), seg...), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		var out bytes.Buffer
		cmd := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-show_entries", "packet=codec_type", "-of", "csv=p=0", p)
		cmd.Stdout = &out
		err := cmd.Run()
		cancel()
		if err == nil && strings.TrimSpace(out.String()) != "" {
			t.Errorf("%s.%s segment %d alone: ffprobe reads %d packets", ref, c, n, strings.Count(out.String(), "\n"))
		}
	}
	t.Logf("%s.%s: %d segments undecodable alone", ref, c, len(uris))
}
