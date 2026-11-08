package listener

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// refused reports whether a request to url fails without a response, as
// it does when the listener closes the connection at accept.
func refused(t *testing.T, url string) bool {
	t.Helper()
	c := client(false)
	c.Transport.(*stdhttp.Transport).DisableKeepAlives = true
	resp, err := c.Get(url)
	if err != nil {
		return true
	}
	resp.Body.Close()
	return false
}

func status(t *testing.T, url string) int {
	t.Helper()
	c := client(false)
	c.Transport.(*stdhttp.Transport).DisableKeepAlives = true
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// startEngine serves doc with an engine for its upstreams to dial on.
func startEngine(t *testing.T, doc string) *Server {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := Start(rt)
	if err != nil {
		rt.Close()
		stop()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); rt.Close(); stop() })
	return s
}

func accessDoc(global, listener, route string) string {
	field := func(name, v string) string {
		if v == "" {
			return ""
		}
		return fmt.Sprintf(`, %q: %s`, name, v)
	}
	return `{"schema_version": 1` + field("access", global) + `,
  "listeners": [{"name": "web", "address": "127.0.0.1:0"` + field("access", listener) + `}],
  "routes": [
    {"match": {"path": ["/open"]}, "handler": {"module": "respond", "params": {"body": "open"}}` + field("access", route) + `},
    {"handler": {"module": "respond", "params": {"body": "hello"}}}
  ]}`
}

func TestAccessRefusedAtAccept(t *testing.T) {
	for _, tt := range []struct {
		name, global, listener string
		refused                bool
	}{
		{"none", "", "", false},
		{"global deny", `{"deny": ["127.0.0.1"]}`, "", true},
		{"global allow elsewhere", `{"allow": ["10.0.0.0/8"]}`, "", true},
		{"global allow", `{"allow": ["127.0.0.0/8"]}`, "", false},
		{"listener overrides global deny", `{"deny": ["127.0.0.0/8"]}`, `{"allow": ["127.0.0.1"]}`, false},
		{"listener deny overrides global allow", `{"allow": ["127.0.0.0/8"]}`, `{"deny": ["127.0.0.1"]}`, true},
		{"listener no say, global decides", `{"deny": ["127.0.0.1"]}`, `{"deny": ["10.0.0.1"]}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, s := start(t, accessDoc(tt.global, tt.listener, ""))
			url := "http://" + s.Addrs()[0].Addr.String() + "/"
			if got := refused(t, url); got != tt.refused {
				t.Errorf("refused = %v, want %v", got, tt.refused)
			}
		})
	}
}

func TestAccessRoute(t *testing.T) {
	// The route's own access decides for its requests; the others go by
	// the listener's and the global, checked per request now.
	_, s := start(t, accessDoc(`{"deny": ["127.0.0.0/8"]}`, "", `{"allow": ["127.0.0.1"]}`))
	base := "http://" + s.Addrs()[0].Addr.String()
	if got := status(t, base+"/open"); got != 200 {
		t.Errorf("/open: %d, want 200", got)
	}
	if got := status(t, base+"/other"); got != 403 {
		t.Errorf("/other: %d, want 403", got)
	}

	_, s = start(t, accessDoc(`{"allow": ["127.0.0.0/8"]}`, "", `{"deny": ["127.0.0.1"]}`))
	base = "http://" + s.Addrs()[0].Addr.String()
	if got := status(t, base+"/open"); got != 403 {
		t.Errorf("/open denied by its route: %d, want 403", got)
	}
	if got := status(t, base+"/other"); got != 200 {
		t.Errorf("/other: %d, want 200", got)
	}
}

func TestAccessFollowsReload(t *testing.T) {
	rt, s := start(t, accessDoc("", "", ""))
	_ = rt
	url := "http://" + s.Addrs()[0].Addr.String() + "/"
	if refused(t, url) {
		t.Fatal("refused before any access")
	}
	address := s.Addrs()[0].Addr.String()
	reload := func(doc string) {
		t.Helper()
		if _, _, err := s.Reload(compile(t, doc)); err != nil {
			t.Fatal(err)
		}
	}
	reload(accessDoc(`{"deny": ["127.0.0.1"]}`, "", ""))
	if s.Addrs()[0].Addr.String() != address {
		t.Fatal("the reload bound the listener anew")
	}
	if !refused(t, url) {
		t.Error("served after a reload denied it")
	}
	// A route of its own: decided per request now, so the accept serves it.
	reload(accessDoc(`{"deny": ["127.0.0.1"]}`, "", `{"allow": ["127.0.0.1"]}`))
	if got := status(t, url+"open"); got != 200 {
		t.Errorf("/open: %d, want 200", got)
	}
	if got := status(t, url); got != 403 {
		t.Errorf("/: %d, want 403", got)
	}
	reload(accessDoc("", "", ""))
	if got := status(t, url); got != 200 {
		t.Errorf("after access is gone: %d", got)
	}
}

func TestAccessProxyProtocol(t *testing.T) {
	_, s := start(t, `{
  "access": {"deny": ["192.0.2.1"]},
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "proxy_protocol": {"trusted": ["127.0.0.1"]}}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}]}`)
	addr := s.Addrs()[0].Addr.String()
	for client, want := range map[string]string{"192.0.2.1": "403", "192.0.2.2": "200"} {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "PROXY TCP4 %s 127.0.0.1 40000 80\r\nGET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", client)
		line, err := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err != nil {
			t.Fatalf("%s: %v", client, err)
		}
		if !strings.Contains(line, " "+want+" ") {
			t.Errorf("%s: %q, want %s", client, line, want)
		}
	}
}

func TestAccessService(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		for {
			c, err := up.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	doc := func(global, service, protocols string) string {
		return fmt.Sprintf(`{"access": %s,
  "listeners": [{"name": "echo", "address": "127.0.0.1:0", "protocols": [%s]}],
  "upstreams": [{"name": "echo", "addresses": [%q]}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "hello"}}}],
  "services": [{"name": "echo", "listeners": ["echo"], "upstream": "echo"%s}]}`, global, protocols, up.Addr().String(), service)
	}
	echoes := func(addr string) bool {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			return false
		}
		buf := make([]byte, 4)
		_, err = io.ReadFull(conn, buf)
		return err == nil && string(buf) == "ping"
	}
	const tcp, mixed = `"tcp"`, `"http/1.1", "tcp"`
	for _, tt := range []struct {
		name, global, service, protocols string
		echoes                           bool
		// http is the status an HTTP request gets on a mixed listener.
		http int
	}{
		{"global deny", `{"deny": ["127.0.0.1"]}`, "", tcp, false, 0},
		{"service overrides", `{"deny": ["127.0.0.1"]}`, `, "access": {"allow": ["127.0.0.1"]}`, tcp, true, 0},
		{"service deny", `{}`, `, "access": {"deny": ["127.0.0.0/8"]}`, tcp, false, 0},
		// A listener that tells protocols apart decides after it has.
		{"mixed service overrides", `{"deny": ["127.0.0.1"]}`, `, "access": {"allow": ["127.0.0.1"]}`, mixed, true, 403},
		{"mixed service deny", `{}`, `, "access": {"deny": ["127.0.0.1"]}`, mixed, false, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := startEngine(t, doc(tt.global, tt.service, tt.protocols))
			addr := s.Addrs()[0].Addr.String()
			if got := echoes(addr); got != tt.echoes {
				t.Errorf("echoes = %v, want %v", got, tt.echoes)
			}
			if tt.http != 0 {
				if got := status(t, "http://"+addr+"/"); got != tt.http {
					t.Errorf("HTTP: %d, want %d", got, tt.http)
				}
			}
		})
	}
}
