package listener

import (
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// freePort returns a TCP port nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestAutoHTTPSRedirect serves a config that writes no listeners: HTTPS by
// default makes an https listener and an http one, which redirects every
// request to it with 308, keeping the host, the target and the method.
func TestAutoHTTPSRedirect(t *testing.T) {
	httpPort, httpsPort := freePort(t), freePort(t)
	_, s := start(t, fmt.Sprintf(`{"auto_https": {"http_port": %d, "https_port": %d},
  "routes": [{"match": {"host": ["a.example"]}, "handler": {"module": "respond", "params": {"body": "ok"}}}]}`, httpPort, httpsPort))
	var names []string
	for _, a := range s.Addrs() {
		names = append(names, a.Name+" "+strconv.Itoa(a.Addr.(*net.TCPAddr).Port))
	}
	if want := fmt.Sprintf("https %d,http %d", httpsPort, httpPort); strings.Join(names, ",") != want {
		t.Fatalf("listeners %v, want %s", names, want)
	}
	c := &stdhttp.Client{Timeout: 5 * time.Second, CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error {
		return stdhttp.ErrUseLastResponse
	}}
	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	for _, tt := range []struct {
		method, host, target, want string
	}{
		{"GET", "a.example", "/x?y=1", fmt.Sprintf("https://a.example:%d/x?y=1", httpsPort)},
		{"POST", "A.example:" + strconv.Itoa(httpPort), "/form", fmt.Sprintf("https://A.example:%d/form", httpsPort)},
		{"GET", "[::1]", "/", fmt.Sprintf("https://[::1]:%d/", httpsPort)},
		{"GET", "other.example", "/.well-known/acme-challenge/none", fmt.Sprintf("https://other.example:%d/.well-known/acme-challenge/none", httpsPort)},
	} {
		req, _ := stdhttp.NewRequest(tt.method, base+tt.target, strings.NewReader("body"))
		req.Host = tt.host
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != stdhttp.StatusPermanentRedirect || resp.Header.Get("Location") != tt.want {
			t.Errorf("%s %s%s: %d %q, want %q", tt.method, tt.host, tt.target, resp.StatusCode, resp.Header.Get("Location"), tt.want)
		}
	}
}
