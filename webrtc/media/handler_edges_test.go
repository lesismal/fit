package media

import (
	"net/http"
	"strings"
	"testing"
)

// Without webrtc.media, a session's URL has no session to end: its DELETE
// is 404, and an offer 503.
func TestHandlerWithoutRole(t *testing.T) {
	f := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "whip"}}]
}`)
	if code := request(t, "DELETE", f.base+"/whip/s/"+strings.Repeat("ab", 16)); code != http.StatusNotFound {
		t.Fatalf("DELETE of a session: %d", code)
	}
	if resp, _ := post(t, f.base+"/whip/s", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("an offer: %d", resp.StatusCode)
	}
}

// webrtc_proxy refuses a policy that is not configured.
func TestProxyHandlerUnknownAuth(t *testing.T) {
	err := compileErr(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "media", "addresses": ["127.0.0.1:1"]}],
  "routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "webrtc_proxy", "params": {"upstream": "media", "auth": "nobody", "action": "publish"}}}]}`)
	if err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Fatalf("an unknown policy: %v", err)
	}
}
