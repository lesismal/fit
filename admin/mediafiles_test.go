package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// copyTree copies the directory from to a new one, and returns it.
func copyTree(t *testing.T, from string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(from, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		to := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// filesFixture is fit with a library of a copy of medialib's testdata,
// uploads to its uploads directory, and a channel naming two of its items.
type filesFixture struct {
	*fixture
	root string
	lib  *medialib.Library
}

func startFiles(t *testing.T, maxUpload int64) *filesFixture {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "medialib", "testdata", "lib"))
	if err != nil {
		t.Fatal(err)
	}
	root := copyTree(t, src)
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	f := &filesFixture{root: root, fixture: start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [{"name": "films", "roots": [`+q(root)+`], "upload_dir": `+q(filepath.Join(root, "uploads"))+`, "max_upload_bytes": `+strconv.FormatInt(maxUpload, 10)+`}],
  "streaming": {"channels": [{"name": "tv/late", "library": "films", "items": ["clips/late", "broken"], "schedule": [{"at": "20:00", "item": "films/x"}]}]},
  "routes": [{"handler": {"module": "respond", "params": {"body": "x"}}}]}`, &config.Admin{})}
	engine := f.configs.env.Engine
	if err := medialib.Apply(engine, f.configs.CurrentConfig().MediaLibrary, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { medialib.Remove(engine) })
	f.lib = medialib.Lookup(engine, "films")
	f.idle(t)
	return f
}

// idle waits until the library has scanned, and does not scan.
func (f *filesFixture) idle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		st := f.lib.Status()
		if st.Scans > 0 && !st.Scanning && !st.Pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still scanning: %+v", st)
		}
	}
}

// item waits for the scan that lists id, and returns it.
func (f *filesFixture) item(t *testing.T, id string) *medialib.Item {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if it := f.lib.Item(id); it != nil {
			return it
		}
		if time.Now().After(deadline) {
			t.Fatalf("no item %s", id)
		}
	}
}

// send sends method path with body to the Admin API, and returns the
// status, the decoded answer and the header.
func (f *fixture) send2(t *testing.T, method, path string, body io.Reader, header ...string) (int, map[string]any, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, f.api+path, body)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, got := f.do(t, req)
	var v map[string]any
	_ = json.Unmarshal([]byte(got), &v)
	return resp.StatusCode, v, resp.Header
}

func jsonBody(s string) io.Reader { return strings.NewReader(s) }

// TestMediaFiles renames, moves and deletes items, edits a sidecar, and
// uploads a file, cut short and resumed, through the Admin API.
func TestMediaFiles(t *testing.T) {
	f := startFiles(t, 1<<20)

	code, v, _ := f.send2(t, "GET", "/media/library/films/dirs", nil)
	if code != 200 || !strings.Contains(fmt.Sprint(v["dirs"]), "films/subs") || v["root"] != f.root {
		t.Fatalf("dirs: %d %v", code, v)
	}
	if code, _, _ := f.send2(t, "GET", "/media/library/films/dirs?root=/none", nil); code != 404 {
		t.Errorf("dirs of no root: %d", code)
	}

	// What uses an item: a channel's list, or its schedule.
	code, v, _ = f.send2(t, "GET", "/media/library/films/usage?item=clips/late", nil)
	if code != 200 || v["in_use"] != true || fmt.Sprint(v["channels"]) != "[tv/late]" || v["sessions_counted"] != false {
		t.Fatalf("usage: %d %v", code, v)
	}
	if code, v, _ := f.send2(t, "GET", "/media/library/films/usage?item=films/bbb", nil); code != 200 || v["in_use"] != false {
		t.Fatalf("usage of one unused: %d %v", code, v)
	}
	if code, _, _ := f.send2(t, "GET", "/media/library/films/usage?item=none", nil); code != 404 {
		t.Errorf("usage of none: %d", code)
	}
	// An item in use is not deleted, moved or renamed without force.
	for _, op := range []string{"delete", "move", "rename"} {
		code, v, _ := f.send2(t, "POST", "/media/library/films/"+op, jsonBody(`{"item": "clips/late", "dir": "x", "name": "y"}`))
		if code != 409 || v["usage"] == nil {
			t.Errorf("%s in use: %d %v", op, code, v)
		}
	}
	if code, v, _ := f.send2(t, "POST", "/media/library/films/move", jsonBody(`{"item": "clips/late", "dir": "old", "force": true}`)); code != 200 || v["id"] != "old/late" {
		t.Fatalf("move with force: %d %v", code, v)
	}

	// Rename: an item without a sidecar takes the ID of its new path.
	code, v, _ = f.send2(t, "POST", "/media/library/films/rename", jsonBody(`{"item": "films/bbb", "name": "bigbuck"}`))
	if code != 200 || v["id"] != "films/bigbuck" || v["op"] != "rename" {
		t.Fatalf("rename: %d %v", code, v)
	}
	if code, _, _ := f.send2(t, "GET", "/media/library/films/items/films/bbb", nil); code != 404 {
		t.Errorf("the old item: %d", code)
	}
	f.item(t, "films/bigbuck")
	for body, want := range map[string]int{
		`{"item": "films/bigbuck", "name": "../x"}`:    400,
		`{"item": "films/bigbuck", "name": "bigbuck"}`: 400,
		`{"item": "none", "name": "x"}`:                404,
		`{`:                                            400,
	} {
		if code, _, _ := f.send2(t, "POST", "/media/library/films/rename", jsonBody(body)); code != want {
			t.Errorf("rename %s: %d, want %d", body, code, want)
		}
	}
	if code, _, _ := f.send2(t, "POST", "/media/library/none/rename", jsonBody(`{}`)); code != 404 {
		t.Errorf("no library: %d", code)
	}

	// The sidecar: read, written from what was read, and refused when it
	// changed since.
	code, v, _ = f.send2(t, "GET", "/media/library/films/sidecar?item=sintel", nil)
	if code != 200 || v["exists"] != true || v["path"] != "films/sintel.json" {
		t.Fatalf("sidecar: %d %v", code, v)
	}
	sd := v["sidecar"].(map[string]any)
	sd["title"] = "Sintel, the film"
	body, _ := json.Marshal(map[string]any{"item": "sintel", "sidecar": sd, "modified": v["modified"]})
	if code, v, _ := f.send2(t, "PUT", "/media/library/films/sidecar", bytes.NewReader(body)); code != 200 || v["op"] != "sidecar" {
		t.Fatalf("put sidecar: %d %v", code, v)
	}
	if code, _, _ := f.send2(t, "PUT", "/media/library/films/sidecar", bytes.NewReader(body)); code != 404 && code != 409 {
		t.Errorf("a stale sidecar: %d", code)
	}
	if it := f.item(t, "sintel"); it.Title != "Sintel, the film" {
		t.Errorf("title: %q", it.Title)
	}
	f.idle(t)
	if code, _, _ := f.send2(t, "PUT", "/media/library/films/sidecar", bytes.NewReader(body)); code != 409 {
		t.Errorf("a stale sidecar: %d", code)
	}
	for body, want := range map[string]int{
		`{"item": "sintel"}`:                                400,
		`{"item": "none", "sidecar": {}}`:                   404,
		`{"item": "sintel", "sidecar": {"poster": "../x"}}`: 400,
		// A new ID of an item in use.
		`{"item": "broken", "sidecar": {"id": "broken2"}}`: 409,
	} {
		if code, _, _ := f.send2(t, "PUT", "/media/library/films/sidecar", jsonBody(body)); code != want {
			t.Errorf("put sidecar %s: %d, want %d", body, code, want)
		}
	}
	if code, _, _ := f.send2(t, "GET", "/media/library/films/sidecar?item=none", nil); code != 404 {
		t.Errorf("sidecar of none: %d", code)
	}

	// Delete: out of the catalog at once.
	if code, v, _ := f.send2(t, "POST", "/media/library/films/delete", jsonBody(`{"item": "films/bigbuck"}`)); code != 200 || len(v["files"].([]any)) != 6 {
		t.Fatalf("delete: %d %v", code, v)
	}
	if code, _, _ := f.send2(t, "GET", "/media/library/films/items/films/bigbuck", nil); code != 404 {
		t.Errorf("deleted: %d", code)
	}
	if _, err := os.Stat(filepath.Join(f.root, "films", "bigbuck.mp4")); err == nil {
		t.Error("a file of the item deleted is there")
	}
}

// TestMediaUploads uploads through the Admin API: begun, cut short,
// resumed from its offset, whole, and probed; past its bounds, refused.
func TestMediaUploads(t *testing.T) {
	f := startFiles(t, 1<<20)
	data, err := os.ReadFile(filepath.Join(f.root, "films", "bbb.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	code, v, h := f.send2(t, "POST", "/media/library/films/uploads", jsonBody(`{"path": "new/film.mp4", "size": `+strconv.Itoa(len(data))+`}`), ConsoleUserHeader, "ada")
	if code != 201 || v["path"] != "uploads/new/film.mp4" || v["user"] != "ada" || h.Get("Upload-Offset") != "0" || h.Get("Upload-Length") != strconv.Itoa(len(data)) {
		t.Fatalf("create: %d %v %v", code, v, h)
	}
	id := v["id"].(string)
	if h.Get("Location") != "/media/library/films/uploads/"+id {
		t.Errorf("Location: %q", h.Get("Location"))
	}
	up := "/media/library/films/uploads/" + id

	// Cut short: what arrived is written.
	half := len(data) / 2
	cut(t, f.admin.Addr().String(), "PATCH "+up, len(data), data[:half])
	waitUpload(t, func() (int, map[string]any) { c, v, _ := f.send2(t, "GET", up, nil); return c, v }, int64(half))
	req, _ := http.NewRequest("HEAD", f.api+up, nil)
	if resp, _ := f.do(t, req); resp.StatusCode != 200 || resp.Header.Get("Upload-Offset") != strconv.Itoa(half) {
		t.Fatalf("HEAD: %d %v", resp.StatusCode, resp.Header)
	}
	code, _, h = f.send2(t, "PATCH", up, bytes.NewReader(data[half:]), "Upload-Offset", "0", "Content-Type", "application/offset+octet-stream")
	if code != 409 || h.Get("Upload-Offset") != strconv.Itoa(half) {
		t.Errorf("at another offset: %d %v", code, h)
	}
	if code, _, _ := f.send2(t, "PATCH", up, bytes.NewReader(nil)); code != 400 {
		t.Errorf("without Upload-Offset: %d", code)
	}
	code, v, _ = f.send2(t, "PATCH", up, bytes.NewReader(data[half:]), "Upload-Offset", strconv.Itoa(half), "Content-Type", "application/offset+octet-stream")
	if code != 200 || v["complete"] != true {
		t.Fatalf("the rest: %d %v", code, v)
	}
	if it := f.item(t, "uploads/new/film"); it.Error != "" || len(it.Renditions) != 1 {
		t.Fatalf("the upload probed: %+v", it)
	}
	if code, _, _ := f.send2(t, "GET", up, nil); code != 404 {
		t.Errorf("the upload whole is still under way: %d", code)
	}

	// Past max_upload_bytes, refused with nothing written; a body longer
	// than the upload, refused and the upload given up.
	if code, _, _ := f.send2(t, "POST", "/media/library/films/uploads", jsonBody(`{"path": "big.mp4", "size": 1048577}`)); code != 413 {
		t.Errorf("too large: %d", code)
	}
	_, v, _ = f.send2(t, "POST", "/media/library/films/uploads", jsonBody(`{"path": "b.mp4", "size": 10}`))
	up = "/media/library/films/uploads/" + v["id"].(string)
	if code, _, _ := f.send2(t, "PATCH", up, bytes.NewReader(make([]byte, 11)), "Upload-Offset", "0"); code != 413 {
		t.Errorf("a body too long: %d", code)
	}
	if code, _, _ := f.send2(t, "GET", up, nil); code != 404 {
		t.Errorf("the upload refused is under way: %d", code)
	}
	if ents, _ := os.ReadDir(filepath.Join(f.root, "uploads")); len(ents) != 1 {
		t.Errorf("left in upload_dir: %v", ents)
	}
	// Given up.
	_, v, _ = f.send2(t, "POST", "/media/library/films/uploads", jsonBody(`{"path": "c.mp4", "size": 10}`))
	up = "/media/library/films/uploads/" + v["id"].(string)
	if code, v, _ := f.send2(t, "GET", "/media/library/films/uploads", nil); code != 200 || len(v["uploads"].([]any)) != 1 {
		t.Errorf("uploads: %d %v", code, v)
	}
	if code, _, _ := f.send2(t, "DELETE", up, nil); code != 200 {
		t.Errorf("cancel: %d", code)
	}
	if code, _, _ := f.send2(t, "DELETE", up, nil); code != 404 {
		t.Errorf("cancel twice: %d", code)
	}
	if code, _, _ := f.send2(t, "PATCH", up, bytes.NewReader(nil), "Upload-Offset", "0"); code != 404 {
		t.Errorf("a write to one given up: %d", code)
	}
	if code, _, _ := f.send2(t, "POST", "/media/library/films/uploads", jsonBody(`{"path": "x.txt", "size": 1}`)); code != 400 {
		t.Errorf("not media: %d", code)
	}
}

// cut sends a request line, as "PATCH /path", with a Content-Length of n
// and part of its body, the header fields in pairs after it, to addr, and
// closes the connection.
func cut(t *testing.T, addr, line string, n int, part []byte, header ...string) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s HTTP/1.1\r\nHost: %s\r\nUpload-Offset: 0\r\nContent-Type: application/offset+octet-stream\r\nContent-Length: %d\r\n", line, addr, n)
	for i := 0; i+1 < len(header); i += 2 {
		fmt.Fprintf(&b, "%s: %s\r\n", header[i], header[i+1])
	}
	b.WriteString("\r\n")
	if _, err := c.Write(append([]byte(b.String()), part...)); err != nil {
		t.Fatal(err)
	}
	// Let the server read what was sent before the connection goes.
	time.Sleep(50 * time.Millisecond)
	c.Close()
}

// waitUpload waits until the upload get reads is at offset, and not being
// written to.
func waitUpload(t *testing.T, get func() (int, map[string]any), offset int64) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		code, v := get()
		if code == 200 && v["busy"] == false && v["offset"] == float64(offset) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the upload: %d %v, want offset %d", code, v, offset)
		}
	}
}

// TestConsoleUploads uploads through the console, its body streamed to the
// library's pool: logged in and CSRF-checked, cut short and resumed, the
// changes in the assistant's audit log.
func TestConsoleUploads(t *testing.T) {
	f := startFiles(t, 1<<20)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	cf := &consoleFixture{fixture: f.fixture, now: time.Now()}
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	var err error
	cf.console, err = StartConsole(testEngine(t), c, &config.Admin{Assistant: &config.Assistant{}}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cf.console.Shutdown(context.Background()) })
	cf.url = strings.TrimSuffix(cf.console.URL(), "/")
	b := cf.browser(t)
	base := "/api/m/admin/media/library/films/uploads"

	// Not logged in: refused before the body is read.
	if code, _, _ := b.do("PATCH", base+"/x", "abc", "Upload-Offset", "0"); code != 401 {
		t.Errorf("not logged in: %d", code)
	}
	if b.login("ada", "pw") != 200 {
		t.Fatal("login")
	}
	data, err := os.ReadFile(filepath.Join(f.root, "films", "bbb.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := b.do("POST", base, `{"path": "c/film.mp4", "size": `+strconv.Itoa(len(data))+`}`)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var info medialib.UploadInfo
	_ = json.Unmarshal([]byte(body), &info)
	up := base + "/" + info.ID
	csrf := b.csrf
	b.csrf = ""
	if code, _, _ := b.do("PATCH", up, string(data), "Upload-Offset", "0"); code != 403 {
		t.Errorf("without the CSRF token: %d", code)
	}
	b.csrf = csrf

	// Cut short, then resumed.
	u, _ := url.Parse(cf.url)
	var cookies []string
	for _, ck := range b.c.Jar.Cookies(u) {
		cookies = append(cookies, ck.Name+"="+ck.Value)
	}
	half := len(data) / 3
	cut(t, u.Host, "PATCH "+up, len(data), data[:half], "Cookie", strings.Join(cookies, "; "), csrfHeader, csrf)
	waitUpload(t, func() (int, map[string]any) {
		code, body, _ := b.do("GET", up, "")
		var v map[string]any
		_ = json.Unmarshal([]byte(body), &v)
		return code, v
	}, int64(half))
	if code, _, h := b.do("PATCH", up, string(data[half:]), "Upload-Offset", "0"); code != 409 || h.Get("Upload-Offset") != strconv.Itoa(half) {
		t.Errorf("at another offset: %d %v", code, h)
	}
	if code, _, _ := b.do("PATCH", up, "", "Upload-Offset", "x"); code != 400 {
		t.Errorf("no offset: %d", code)
	}
	code, body, h := b.do("PATCH", up, string(data[half:]), "Upload-Offset", strconv.Itoa(half))
	if code != 200 || !strings.Contains(body, `"complete":true`) || h.Get("Upload-Offset") != strconv.Itoa(len(data)) {
		t.Fatalf("the rest: %d %s", code, body)
	}
	if it := f.item(t, "uploads/c/film"); it.Error != "" {
		t.Fatalf("probed: %+v", it)
	}

	// A body past the upload: 413, and the upload given up.
	_, body, _ = b.do("POST", base, `{"path": "d.mp4", "size": 10}`)
	_ = json.Unmarshal([]byte(body), &info)
	if code, _, _ := b.do("PATCH", base+"/"+info.ID, "01234567890", "Upload-Offset", "0"); code != 413 {
		t.Errorf("too long: %d", code)
	}
	if code, _, _ := b.do("GET", base+"/"+info.ID, ""); code != 404 {
		t.Errorf("given up: %d", code)
	}
	for path, want := range map[string]int{
		"/api/m/admin/media/library/none/uploads/x":  404,
		"/api/m/admin/media/library/films/uploads/x": 404,
		"/api/m/admin/media/library/films/other/x":   404,
	} {
		if code, _, _ := b.do("PATCH", path, "abc", "Upload-Offset", "0"); code != want {
			t.Errorf("PATCH %s: %d, want %d", path, code, want)
		}
	}
	// A change through the console's proxy.
	if code, body, _ := b.do("POST", "/api/m/admin/media/library/films/rename", `{"item": "sintel", "name": "durian"}`); code != 200 {
		t.Fatalf("rename: %d %s", code, body)
	}

	// The audit log: the upload begun and finished, the one refused, and
	// the rename, each as ada's, from the workbench.
	// An entry may be written once its answer has gone out: the log is
	// read until it holds them all, or the time is up.
	wants := []string{`"tool":"media.upload.begin"`, `"tool":"media.upload"`, `"tool":"media.rename"`, `"source":"workbench"`, `"user":"ada"`, `"outcome":"failed"`}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		code, body, _ = b.do("GET", "/api/m/ai/audit?kind=media&limit=100", "")
		if code != 200 {
			t.Fatalf("audit: %d %s", code, body)
		}
		missing := ""
		for _, want := range wants {
			if !strings.Contains(body, want) {
				missing = want
				break
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the audit log lacks %s: %s", missing, body)
		}
	}
	for _, p := range []string{"/media/library/x", "/media/library//uploads/x", "/media/library/a/uploads/", "/media/library/a/uploads/b/c", "/other"} {
		if _, _, ok := uploadPath(p); ok {
			t.Errorf("uploadPath(%q)", p)
		}
	}
}

// TestMediaFileUsage finds what plays an item: a channel playing it now,
// and a viewer's playback session; a delete is refused while they do.
func TestMediaFileUsage(t *testing.T) {
	f := startMedia(t)
	// The channel tv/films plays films/bbb.
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		u := f.call(t, "GET", "/media/library/films/usage?item=films/bbb", "", 200)
		if strings.Contains(fmt.Sprint(u["playing"]), "tv/films") && u["in_use"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage: %v", u)
		}
	}
	u := f.call(t, "GET", "/media/library/films/usage?item=sintel", "", 200)
	if u["in_use"] != false || u["sessions_counted"] != true || u["sessions"] != 0.0 {
		t.Fatalf("sintel: %v", u)
	}
	// A viewer plays sintel.
	pv := f.call(t, "POST", "/media/preview", `{"library": "films", "item": "sintel"}`, 200, ConsoleUserHeader, "ann")
	if got := play(t, pv["url"].(string)); got != 200 {
		t.Fatalf("play: %d", got)
	}
	u = f.call(t, "GET", "/media/library/films/usage?item=sintel", "", 200)
	if u["in_use"] != true || u["sessions"] != 1.0 {
		t.Fatalf("sintel played: %v", u)
	}
	got := f.call(t, "POST", "/media/library/films/delete", `{"item": "sintel"}`, 409)
	if got["usage"].(map[string]any)["sessions"] != 1.0 {
		t.Errorf("refused: %v", got)
	}
	// The library takes no uploads.
	f.call(t, "POST", "/media/library/films/uploads", `{"path": "a.mp4", "size": 1}`, 404)
}

// TestMediaUploadLarge writes a body of several pieces through the Admin
// API, each written on the pool while the next waits.
func TestMediaUploadLarge(t *testing.T) {
	f := startFiles(t, 8<<20)
	data := make([]byte, 3<<20+77)
	for i := range data {
		data[i] = byte(i * 7)
	}
	_, v, _ := f.send2(t, "POST", "/media/library/films/uploads", jsonBody(`{"path": "big.mp4", "size": `+strconv.Itoa(len(data))+`}`))
	up := "/media/library/films/uploads/" + v["id"].(string)
	code, v, _ := f.send2(t, "PATCH", up, bytes.NewReader(data), "Upload-Offset", "0")
	if code != 200 || v["complete"] != true {
		t.Fatalf("%d %v", code, v)
	}
	got, err := os.ReadFile(filepath.Join(f.root, "uploads", "big.mp4"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("the file: %d bytes, %v", len(got), err)
	}
	code, v, _ = f.send2(t, "GET", "/media/library", nil)
	lib := v["libraries"].([]any)[0].(map[string]any)
	if code != 200 || lib["upload"].(map[string]any)["dir"] != "uploads" || lib["uploads"] != 0.0 {
		t.Errorf("the library: %v", lib)
	}
	// The endpoints of no library, and bodies that do not decode.
	for _, c := range []struct{ method, path string }{
		{"GET", "/media/library/none/uploads"}, {"POST", "/media/library/none/uploads"}, {"GET", "/media/library/none/uploads/x"},
		{"PATCH", "/media/library/none/uploads/x"}, {"DELETE", "/media/library/none/uploads/x"}, {"GET", "/media/library/none/dirs"},
		{"GET", "/media/library/none/usage?item=x"}, {"GET", "/media/library/none/sidecar?item=x"}, {"PUT", "/media/library/none/sidecar"},
		{"POST", "/media/library/none/delete"},
	} {
		if code, _, _ := f.send2(t, c.method, c.path, jsonBody(`{}`)); code != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, code)
		}
	}
	for _, path := range []string{"/media/library/films/uploads", "/media/library/films/delete"} {
		if code, _, _ := f.send2(t, "POST", path, jsonBody(`{`)); code != 400 {
			t.Errorf("POST %s, bad JSON: %d", path, code)
		}
	}
	if code, _, _ := f.send2(t, "PUT", "/media/library/films/sidecar", jsonBody(`{`)); code != 400 {
		t.Errorf("PUT sidecar, bad JSON: %d", code)
	}
}

// TestFileStatus: what each error of a change answers.
func TestFileStatus(t *testing.T) {
	for err, want := range map[error]int{
		medialib.ErrNoItem: 404, medialib.ErrNoUpload: 404, medialib.ErrNoUploads: 404, medialib.ErrRemoved: 404,
		medialib.ErrBadPath: 400, medialib.ErrNotMedia: 400, medialib.ErrUploadSize: 400, medialib.ErrNoChange: 400, medialib.ErrBadSidecar: 400,
		medialib.ErrOutside: 403, medialib.ErrExists: 409, medialib.ErrChanged: 409, medialib.ErrOffset: 409, medialib.ErrUploadBusy: 409,
		medialib.ErrTooLarge: 413, medialib.ErrTooMany: 429, medialib.ErrBusy: 503, medialib.ErrNoSpace: 507, io.EOF: 500,
	} {
		if got := fileStatus(fmt.Errorf("x: %w", err)); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
	for _, err := range []error{medialib.ErrBusy, medialib.ErrUploadBusy} {
		w := httptestRecorder()
		writeFileError(w, err)
		if w.header.Get("Retry-After") != "1" {
			t.Errorf("%v: no Retry-After", err)
		}
	}
}

func httptestRecorder() *recorder { return &recorder{header: http.Header{}, status: http.StatusOK} }
