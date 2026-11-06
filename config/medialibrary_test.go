package config

import (
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

func TestMediaLibrary(t *testing.T) {
	head := `{"listeners": [{"name": "web", "address": ":8080"}], "media_library": `
	c, err := Load([]byte(head + `[{"name": "films", "roots": ["/srv/films", "/srv/shows"], "files": ["*.mp4", "films/**/*.m4a"],
		"exclude": ["**/extras/**"], "follow_symlinks": true, "scan_interval": "1h", "max_items": 10, "max_table_bytes": 1000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := c.MediaLibraryNamed("films")
	if m == nil || c.MediaLibraryNamed("none") != nil || m.Interval() != time.Hour || m.Items() != 10 || m.TableBytes() != 1000 ||
		m.Files() != DefaultMediaMaxFiles || m.CatalogBytes() != DefaultMediaMaxCatalogBytes || m.Probes() != DefaultMediaMaxProbes ||
		m.SidecarBytes() != DefaultMediaMaxSidecarBytes || m.SubtitleBytes() != DefaultMediaMaxSubtitleBytes ||
		m.Workers() != max(16, 4*goruntime.GOMAXPROCS(0)) || m.Queue() != DefaultMediaFileQueue || m.Timeout() != DefaultMediaFileTimeout {
		t.Fatalf("%+v", m)
	}
	if m.UploadBytes() != DefaultMediaMaxUploadBytes || m.Uploads() != DefaultMediaMaxUploads || m.FreeBytes() != DefaultMediaMinFreeBytes ||
		m.Expiry() != DefaultMediaUploadExpiry {
		t.Errorf("upload defaults: %+v", m)
	}
	for _, ok := range []string{`"/a"`, `"/a/up"`, `"/b/x/../up"`} {
		if _, err := Load([]byte(head + `[{"name": "a", "roots": ["/a", "/b"], "upload_dir": ` + ok + `, "max_uploads": 2, "upload_expiry": "1h"}]}`)); err != nil {
			t.Errorf("upload_dir %s: %v", ok, err)
		}
	}
	if m.Encryptable() || !(&MediaLibrary{SecretFile: "/s"}).Encryptable() || !(&MediaLibrary{Secret: "s"}).Encryptable() {
		t.Error("Encryptable")
	}
	if (&MediaLibrary{}).Interval() != DefaultMediaScanInterval || (&MediaLibrary{FileWorkers: 3}).Workers() != 3 {
		t.Error("defaults")
	}
	abs, _ := filepath.Abs("x")
	for _, bad := range []struct{ doc, want string }{
		{`[{"name": "-x", "roots": ["/a"]}]`, "not a valid name"},
		{`[{"name": "a", "roots": ["/a"]}, {"name": "a", "roots": ["/b"]}]`, `another media_library is already named "a"`},
		{`[{"name": "a", "roots": []}]`, "at least one root"},
		{`[{"name": "a"}]`, "roots"},
		{`[{"name": "a", "roots": [" ", "/a"]}]`, "a root may not be empty"},
		{`[{"name": "a", "roots": ["/a", "/a/"]}]`, "listed twice"},
		{`[{"name": "a", "roots": ["/a", "/a/b"]}]`, "lie one inside the other"},
		{`[{"name": "a", "roots": ["` + filepath.ToSlash(abs) + `/y", "x"]}]`, "lie one inside the other"},
		{`[{"name": "a", "roots": ["/a"], "files": ["["]}]`, "is not a pattern"},
		{`[{"name": "a", "roots": ["/a"], "exclude": ["/abs"]}]`, "is not a pattern"},
		{`[{"name": "a", "roots": ["/a"], "exclude": ["a//b"]}]`, "is not a pattern"},
		{`[{"name": "a", "roots": ["/a"], "exclude": [""]}]`, "is not a pattern"},
		{`[{"name": "a", "roots": ["/a"], "scan_interval": "1s"}]`, "at least 10s"},
		{`[{"name": "a", "roots": ["/a"], "max_sidecar_bytes": 2000000}]`, "greater than the maximum"},
		{`[{"name": "a", "roots": ["/a"], "secret": "0123456789abcdef", "secret_file": "/s"}]`, "one of the two"},
		{`[{"name": "a", "roots": ["/a"], "secret": "short"}]`, "use at least 16"},
		{`[{"name": "a", "roots": ["/a"], "upload_expiry": "1s"}]`, "at least 1m"},
		{`[{"name": "a", "roots": ["/a"], "upload_dir": "/b/up"}]`, "is not inside a root"},
		{`[{"name": "a", "roots": ["/a"], "upload_dir": "/ab"}]`, "is not inside a root"},
		{`[{"name": "a", "roots": ["/a"], "upload_dir": " "}]`, "is not inside a root"},
		{`[{"name": "a", "roots": ["/a"], "max_uploads": 2000}]`, "greater than the maximum"},
	} {
		_, err := Load([]byte(head + bad.doc + `}`))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	for _, p := range []string{"*.mp4", "**", "a/**/b", "[a-z]?.mp4"} {
		if !ValidMediaPathPattern(p) {
			t.Errorf("ValidMediaPathPattern(%q)", p)
		}
	}
	if within("/a", "/a") || within("/a", "/ab") || !within("/a", "/a/b") || within("/a/b", "/a") {
		t.Error("within")
	}
}
