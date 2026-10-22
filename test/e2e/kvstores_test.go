//go:build !windows

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The images of the servers the providers are tested against.
const (
	consulImage = "hashicorp/consul:1.21.5"
	etcdImage   = "quay.io/coreos/etcd:v3.6.5"
)

// startServer runs image in Docker with args, its port published on
// 127.0.0.1, and returns the address it is published on; the container is
// removed when the test ends. It skips the test without Docker.
func startServer(t *testing.T, image string, port int, args ...string) string {
	t.Helper()
	return startServerWith(t, image, port, nil, args...)
}

// startServerWith is startServer with docker run's options opts, as
// volumes.
func startServerWith(t *testing.T, image string, port int, opts []string, args ...string) string {
	t.Helper()
	if os.Getenv("FIT_E2E_DOCKER") == "0" {
		t.Skip("FIT_E2E_DOCKER=0")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	if exec.Command("docker", "image", "inspect", image).Run() != nil {
		docker(t, "pull", image)
	}
	run := append([]string{"run", "-d", "--rm", "-p", fmt.Sprintf("127.0.0.1::%d", port)}, opts...)
	id := docker(t, append(append(run, image), args...)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	addr := docker(t, "port", id, fmt.Sprintf("%d/tcp", port))
	if i := strings.IndexByte(addr, '\n'); i >= 0 {
		addr = addr[:i]
	}
	return addr
}

// backend serves body on 127.0.0.1, and returns its port.
func backend(t *testing.T, body string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// call sends method to url with body, failing the test unless the answer
// is a 2xx; it returns the answer's body.
func call(t *testing.T, method, url, body string, header ...string) string {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %s: %s", method, url, resp.Status, out)
	}
	return string(out)
}

// waitServer waits until ok says the server at url is ready.
func waitServer(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// answers waits until the bodies of 10 GETs of url, by count, are want, an
// empty body standing for a 404; it fails the test after 30s.
func answers(t *testing.T, f *fit, url string, want map[string]int, what string) {
	t.Helper()
	c := client(false)
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := map[string]int{}
		for range 10 {
			resp, err := c.Get(url)
			if err != nil {
				got["error"]++
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch resp.StatusCode {
			case 200:
				got[string(body)]++
			case 404:
				got[""]++
			default:
				got[resp.Status]++
			}
		}
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: got %v, want %v\nfit's stderr:\n%s", what, got, want, f.stderr.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestConsulProviders runs fit with the Consul catalog and KV providers
// against a Consul agent in dev mode, and follows a service's instances as
// they are registered, fail their checks and leave, and a key as it is
// written and deleted. Nothing is read on a timer: the refresh interval is
// left at 30s, so each change comes through a blocking query.
func TestConsulProviders(t *testing.T) {
	consul := "http://" + startServer(t, consulImage, 8500, "agent", "-dev", "-client", "0.0.0.0")
	waitServer(t, "Consul", func() bool {
		resp, err := http.Get(consul + "/v1/status/leader")
		if err != nil {
			return false
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode == 200 && len(bytes.Trim(body, `"`+"\n")) > 0
	})
	register := func(id, body string) {
		port := backend(t, body)
		call(t, "PUT", consul+"/v1/agent/service/register", fmt.Sprintf(`{
  "ID": %q, "Name": "web", "Address": "127.0.0.1", "Port": %d,
  "Tags": ["fit.enable=true", "fit.routes.main.path=/*"],
  "Check": {"TTL": "10m", "Status": "passing"}
}`, id, port))
	}
	register("web-a", "a")

	f, addrs := startFit(t, fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "admin": {"address": "127.0.0.1:0"},
  "providers": [{"consul": {"endpoint": %q}}, {"consul_kv": {"endpoint": %q}}]
}`, consul, consul))
	go func() {
		for range f.lines {
		}
	}()
	web := "http://" + addrs["web"]
	answers(t, f, web+"/", map[string]int{"a": 10}, "the first instance")
	effective := get(t, "http://"+addrs["admin"]+"/config/effective")
	if !strings.Contains(effective, `"source":"consul"`) || !strings.Contains(effective, `"name":"web.main"`) {
		t.Fatalf("/config/effective: %s", effective)
	}

	register("web-b", "b")
	answers(t, f, web+"/", map[string]int{"a": 5, "b": 5}, "both instances")
	call(t, "PUT", consul+"/v1/agent/check/fail/service:web-a", "")
	answers(t, f, web+"/", map[string]int{"b": 10}, "the instance that passes")
	call(t, "PUT", consul+"/v1/agent/service/deregister/web-b", "")
	answers(t, f, web+"/", map[string]int{"": 10}, "no instance")

	call(t, "PUT", consul+"/v1/kv/fit/kv", `{"routes": [{"name": "kv", "match": {"path": ["/kv"]}, "handler": {"module": "respond", "params": {"body": "from consul"}}}]}`)
	answers(t, f, web+"/kv", map[string]int{"from consul": 10}, "a route from a key")
	call(t, "DELETE", consul+"/v1/kv/fit/kv", "")
	answers(t, f, web+"/kv", map[string]int{"": 10}, "the key deleted")
}

// TestEtcdProvider runs fit with the etcd provider against etcd with its
// authentication on, and follows a key as it is written, changed and
// deleted, through a watch: the refresh interval is left at 30s.
func TestEtcdProvider(t *testing.T) {
	etcd := "http://" + startServer(t, etcdImage, 2379, "etcd",
		"--listen-client-urls", "http://0.0.0.0:2379", "--advertise-client-urls", "http://127.0.0.1:2379")
	waitServer(t, "etcd", func() bool {
		resp, err := http.Post(etcd+"/v3/kv/range", "application/json", strings.NewReader(`{"key": "AA=="}`))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	b64 := func(s string) string {
		v, _ := json.Marshal([]byte(s))
		return string(v)
	}
	call(t, "POST", etcd+"/v3/auth/user/add", `{"name": "root", "password": "s3cret"}`)
	call(t, "POST", etcd+"/v3/auth/user/grant", `{"user": "root", "role": "root"}`)
	call(t, "POST", etcd+"/v3/auth/enable", `{}`)
	var auth struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(call(t, "POST", etcd+"/v3/auth/authenticate", `{"name": "root", "password": "s3cret"}`)), &auth); err != nil || auth.Token == "" {
		t.Fatalf("authenticate: %v %q", err, auth.Token)
	}
	put := func(key, value string) {
		call(t, "POST", etcd+"/v3/kv/put", fmt.Sprintf(`{"key": %s, "value": %s}`, b64(key), b64(value)), "Authorization", auth.Token)
	}
	respond := func(body string) string {
		return `{"routes": [{"name": "etcd", "match": {"path": ["/etcd"]}, "handler": {"module": "respond", "params": {"body": "` + body + `"}}}]}`
	}
	put("fit/route", respond("one"))

	password := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(password, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, addrs := startFit(t, fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "admin": {"address": "127.0.0.1:0"},
  "providers": [{"etcd": {"endpoints": [%q], "username": "root", "password_file": %q}}]
}`, etcd, password))
	go func() {
		for range f.lines {
		}
	}()
	url := "http://" + addrs["web"] + "/etcd"
	answers(t, f, url, map[string]int{"one": 10}, "a route from a key")
	effective := get(t, "http://"+addrs["admin"]+"/config/effective")
	if !strings.Contains(effective, `"source":"etcd"`) {
		t.Fatalf("/config/effective: %s", effective)
	}
	put("fit/route", respond("two"))
	answers(t, f, url, map[string]int{"two": 10}, "the key changed")
	call(t, "POST", etcd+"/v3/kv/deleterange", fmt.Sprintf(`{"key": %s}`, b64("fit/route")), "Authorization", auth.Token)
	answers(t, f, url, map[string]int{"": 10}, "the key deleted")
}
