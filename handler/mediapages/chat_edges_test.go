package mediapages

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// The chat's requests that name no member, a file's name and type past their
// bounds, and a file the chat has no room for.
func TestChatRequestEdges(t *testing.T) {
	old := chatFileBytes
	chatFileBytes = 1500
	t.Cleanup(func() { chatFileBytes = old })

	f := startFit(t, conf(`{"auth": "live", "max_file_bytes": 4096}`))
	h := hubOf(t, f)
	ann := joinKey(t, f, "room1", "ann", bearer)
	api := f.base + "/media/api/rooms/room1/"

	// No member, or a key left out, is no such member.
	for _, hdr := range []map[string]string{bearer, chatAuth(bearer, "ann", ""), chatAuth(bearer, "not a name", ann)} {
		if resp, body := do(t, "POST", api+"chat", `{"text": "hi"}`, hdr); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no such member") {
			t.Fatalf("a chat message from %v: %d %s", hdr, resp.StatusCode, body)
		}
		if resp, body := do(t, "POST", api+"files?file=a.txt", "x", hdr); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no such member") {
			t.Fatalf("a file from %v: %d %s", hdr, resp.StatusCode, body)
		}
	}

	// A name too long to come back short enough is refused before it is
	// sanitized.
	if resp, _ := postFile(t, f, "room1", "ann", ann, strings.Repeat("n", 4*maxChatFileName+1), "text/plain", "x", bearer); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a file name past four times the bound: %d", resp.StatusCode)
	}

	// A file larger than the hub keeps in all, with no file to evict, is
	// 413 and stores nothing.
	if resp, body := do(t, "POST", api+"files?file=big.bin", strings.Repeat("b", 1600), chatAuth(bearer, "ann", ann)); resp.StatusCode != http.StatusRequestEntityTooLarge ||
		!strings.Contains(body, "no room for the file") {
		t.Fatalf("a file past the hub's bound: %d %s", resp.StatusCode, body)
	}
	if got := fileBytesOf(t, h); got != 0 {
		t.Fatalf("after a refused file: %d bytes", got)
	}

	// A Content-Type past its bound is no type: the file is octet-stream.
	resp, m := postFile(t, f, "room1", "ann", ann, "a.txt", "text/plain; x="+strings.Repeat("y", maxChatFileType), "hello", bearer)
	if resp.StatusCode != 200 || m.File == nil || m.File.Type != "application/octet-stream" {
		t.Fatalf("a long Content-Type: %d %+v", resp.StatusCode, m)
	}
	if resp, body := do(t, "GET", filesURL(f, "room1", m.ID), "", bearer); resp.StatusCode != 200 || body != "hello" ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("its download: %d %v %q", resp.StatusCode, resp.Header, body)
	}

}

// Eviction finds a room's oldest file behind its text messages, and a file
// pushed out of the history by later messages takes its bytes with it.
func TestChatFileEvictionBehindText(t *testing.T) {
	old := chatFileBytes
	chatFileBytes = 1500
	t.Cleanup(func() { chatFileBytes = old })

	f := startFit(t, conf(`{"auth": "live"}`))
	h := hubOf(t, f)
	ann := joinKey(t, f, "room1", "ann", bearer)
	if resp, _ := postChat(t, f, "room1", "ann", ann, "first, a text", bearer); resp.StatusCode != 200 {
		t.Fatalf("a text: %d", resp.StatusCode)
	}
	_, a := postFile(t, f, "room1", "ann", ann, "a.bin", "", strings.Repeat("a", 1000), bearer)
	resp, b := postFile(t, f, "room1", "ann", ann, "b.bin", "", strings.Repeat("b", 1000), bearer)
	if resp.StatusCode != 200 || fileBytesOf(t, h) != 1000 {
		t.Fatalf("the second file: %d, %d bytes", resp.StatusCode, fileBytesOf(t, h))
	}
	if resp, _ := do(t, "GET", filesURL(f, "room1", a.ID), "", bearer); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the evicted file: %d", resp.StatusCode)
	}
	// A file larger than all the room the chat has is refused without
	// evicting the ones there.
	if resp, _ := postFile(t, f, "room1", "ann", ann, "c.bin", "", strings.Repeat("c", 1600), bearer); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a file past the chat's room: %d", resp.StatusCode)
	}
	if resp, body := do(t, "GET", filesURL(f, "room1", b.ID), "", bearer); resp.StatusCode != 200 || body != strings.Repeat("b", 1000) {
		t.Fatalf("the file there after a refusal: %d", resp.StatusCode)
	}

	// chatHistory text messages push b out of the room's history.
	for i := range chatHistory {
		if resp, _ := postChat(t, f, "room1", "ann", ann, fmt.Sprintf("m%d", i), bearer); resp.StatusCode != 200 {
			t.Fatalf("message %d: %d", i, resp.StatusCode)
		}
	}
	if got := fileBytesOf(t, h); got != 0 {
		t.Fatalf("after the file left the history: %d bytes", got)
	}
	if resp, _ := do(t, "GET", filesURL(f, "room1", b.ID), "", bearer); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the file out of the history: %d", resp.StatusCode)
	}
}

// Two routes that serve one prefix share its rooms, whose bounds are the
// config's last route's: a message the other route's body limit takes is
// still held to the rooms' max_message_bytes.
func TestChatSharedHubBound(t *testing.T) {
	route := func(host string, max int) string {
		return fmt.Sprintf(`{"match": {"host": [%q], "path": ["/media", "/media/*"]}, "handler": {"module": "media_pages", "params": {"max_message_bytes": %d}}}`, host, max)
	}
	// The rooms take the bounds of whichever route is provisioned last: the
	// order that leaves them the small route's is the one under test.
	var f *fit
	for _, routes := range []string{route("big.test", 4096) + ", " + route("small.test", 256), route("small.test", 256) + ", " + route("big.test", 4096)} {
		f = startFit(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [`+routes+`]}`)
		if hubOf(t, f).set.Load().maxMessage == 256 {
			break
		}
	}
	if got := hubOf(t, f).set.Load().maxMessage; got != 256 {
		t.Fatalf("the rooms' bound is %d in either order", got)
	}
	post := func(path, body string, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", f.base+path, strings.NewReader(body))
		req.Host = "big.test"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: waitTimeout}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := post("/media/api/rooms/r/join", `{"name": "ann"}`, nil)
	var v struct{ Key string }
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &v) != nil || v.Key == "" {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
	resp, body = post("/media/api/rooms/r/chat", `{"text": "`+strings.Repeat("t", 1000)+`"}`, chatAuth(nil, "ann", v.Key))
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "longer than max_message_bytes") {
		t.Fatalf("a text past the rooms' bound: %d %s", resp.StatusCode, body)
	}
}

// A user whose grant is too long for a token logs in with the right password
// but gets no token: 500.
func TestLoginGrantTooLong(t *testing.T) {
	patterns := make([]string, 20)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("%q", fmt.Sprintf("room%02d-", i)+strings.Repeat("p", 240))
	}
	f := startFit(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["/media", "/media/*"]}, "handler": {"module": "media_pages", "params": {"auth": "wide", "login": true}}}],
  "media_auth": [{"name": "wide", "users": [{"name": "wes", "password": "wes-password", "play": [`+strings.Join(patterns, ",")+`]}]}]}`)
	resp, body := do(t, "POST", f.base+"/media/api/login", `{"user": "wes", "password": "wes-password"}`, nil)
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "too long for a token") || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("a login whose grant is too long: %d %v %s", resp.StatusCode, resp.Header, body)
	}
}

// pagesHolder provisions the Pages it holds as they are set, past the bounds
// a config's schema puts on them, as a Go caller that builds Pages may.
type pagesHolder struct{ p *Pages }

func (h *pagesHolder) Provision(ctx *module.Context) error { return h.p.Provision(ctx) }
func (h *pagesHolder) ServeHTTP(*fibhttp.Context)          {}

var pagesSeq atomic.Int64

// Provision checks max_file_bytes itself, for Pages the schema did not check.
func TestProvisionFileBound(t *testing.T) {
	for _, n := range []int64{MinMaxFileBytes - 1, MaxMaxFileBytes + 1} {
		id := fmt.Sprintf("test_media_pages_bounded_%d", pagesSeq.Add(1))
		p := &Pages{MaxFileBytes: n}
		module.Register(module.Info{ID: module.Handlers + "." + id, New: func() any { return &pagesHolder{p: p} }})
		c, err := config.Load([]byte(`{"listeners": [{"name": "w", "address": ":0"}], "routes": [{"handler": {"module": "` + id + `", "params": {}}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := module.Load(module.Handlers, c.Routes[0].Handler, &module.Env{}); err == nil || !strings.Contains(err.Error(), "a file is at least 1024 and at most 33554432 bytes") {
			t.Fatalf("max_file_bytes %d: %v", n, err)
		}
	}
}

// rawPost sends a chunked POST of chunks, then tail, with hdr, on a
// connection of its own, and returns what the server answered before it
// closed it.
func rawPost(t *testing.T, f *fit, path, hdr string, chunks []string, tail string) string {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(waitTimeout))
	var b strings.Builder
	b.WriteString("POST " + path + " HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n" + hdr + "\r\n")
	for _, c := range chunks {
		fmt.Fprintf(&b, "%x\r\n%s\r\n", len(c), c)
	}
	b.WriteString(tail)
	// The server may answer and close before it has read everything.
	_, _ = conn.Write([]byte(b.String()))
	out, _ := io.ReadAll(conn)
	return string(out)
}

// A body past the listener's stream threshold reaches the room API as it
// arrives: past max_message_bytes it is 413 and the rest is let go, and one
// whose chunked framing breaks fails and stores nothing.
func TestStreamedBodies(t *testing.T) {
	f := startFit(t, conf(`{"max_message_bytes": 65536, "max_file_bytes": 1048576}`))
	h := hubOf(t, f)
	big := strings.Repeat("n", 40<<10)
	if out := rawPost(t, f, "/media/api/rooms/r/join", "", []string{`{"name": "` + big, big, big}, "0\r\n\r\n"); !strings.HasPrefix(out, "HTTP/1.1 413") {
		t.Fatalf("a streamed join past the limit: %q", out)
	}
	ann := joinKey(t, f, "r", "ann", nil)
	out := rawPost(t, f, "/media/api/rooms/r/files?file=a.bin", "X-Fit-Member: ann\r\nX-Fit-Room-Key: "+ann+"\r\n",
		[]string{strings.Repeat("f", 70<<10)}, "zz\r\n")
	if strings.Contains(out, "200 OK") {
		t.Fatalf("a broken upload was answered: %q", out)
	}
	if got := fileBytesOf(t, h); got != 0 {
		t.Fatalf("a broken upload stored %d bytes", got)
	}
}
