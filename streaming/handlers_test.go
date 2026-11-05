package streaming

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// The handlers refuse a prefix that is not a path, a policy that is not
// configured, a stream that is not a stream's path and a part duration out
// of range.
func TestHandlerParams(t *testing.T) {
	up, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	for params, want := range map[string]string{
		`"module": "httpflv", "params": {"prefix": "flv/"}`:                     `"flv/" does not start with /`,
		`"module": "httpflv", "params": {"auth": "nobody"}`:                     `nobody`,
		`"module": "hls", "params": {"auth": "nobody"}`:                         `nobody`,
		`"module": "hls", "params": {"part_duration": "50ms"}`:                  `50ms is not between 100ms and 5s`,
		`"module": "stream_whip", "params": {"stream": "live/../cam"}`:          `"live/../cam" is not a stream's path`,
		`"module": "stream_whep", "params": {"prefix": "/whep/", "auth": "no"}`: `"no"`,
	} {
		c, err := config.Load([]byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		  "routes": [{"handler": {` + params + `}}]}`))
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		_, err = runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics(), Engine: up})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", params, want, err)
		}
	}
}

// A path the handlers do not serve is 404: outside the prefix, without a
// stream or a playlist's name, or a stream that is not one; a WHIP OPTIONS
// of the endpoint reaches fib's server, which answers it.
func TestHandlerPaths(t *testing.T) {
	f := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "streaming": {"webrtc": {"listen": "127.0.0.1:0"}},
  "routes": [
    {"match": {"path": ["/flv/*", "/other.flv"]}, "handler": {"module": "httpflv", "params": {"prefix": "/flv"}}},
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "format": "ts"}}},
    {"match": {"path": ["/whip", "/whip/*", "/elsewhere"]}, "handler": {"module": "stream_whip", "params": {"prefix": "/whip"}}}
  ]
}`)
	for path, want := range map[string]int{
		"/other.flv":      404,
		"/flv/live/x.mp4": 404,
		"/hls/cam":        404,
		"/hls/../x/a.ts":  404,
		"/elsewhere":      404,
		"/whip":           404,
	} {
		if resp, _ := get(t, f.base+path, 0, nil); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	req, _ := http.NewRequest("OPTIONS", f.base+"/whip/live/cam", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 || !strings.Contains(resp.Header.Get("Allow")+resp.Header.Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("OPTIONS of the endpoint: %d %v", resp.StatusCode, resp.Header)
	}
}
