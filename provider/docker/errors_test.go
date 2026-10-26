package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider"
)

func TestKind(t *testing.T) {
	k, ok := provider.LookupKind("docker")
	if !ok {
		t.Fatal("kind docker is not registered")
	}
	p, watching := k("dk", &config.Provider{Docker: &config.DockerProvider{Endpoint: "tcp://127.0.0.1:2375"}}, nil)
	if p.Name() != "dk" || watching != "watching Docker at tcp:127.0.0.1:2375" {
		t.Errorf("%q, %q", p.Name(), watching)
	}
}

func TestTranslateEdges(t *testing.T) {
	// No name but its ID, and not running.
	stopped := ctr("x", map[string]string{"fit.enable": "true"}, "10.0.0.1", 80)
	stopped.Names, stopped.State = nil, "exited"
	// A name the config does not take.
	badName := ctr("badname", map[string]string{"fit.enable": "true", "fit.name": "no way"}, "10.0.0.2", 80)
	// On the network it names, but without an address there.
	noAddr := ctr("noaddr", map[string]string{"fit.enable": "true", "fit.network": "net"}, "10.0.0.3", 80)
	noAddr.NetworkSettings.Networks["net"] = struct {
		IPAddress         string `json:"IPAddress"`
		GlobalIPv6Address string `json:"GlobalIPv6Address"`
	}{}
	part, problems := translate([]container{stopped, badName, noAddr}, &config.DockerProvider{})
	want := []string{
		`container badname: "no way" is not a valid name for an upstream: set fit.name`,
		`container noaddr: it has no address on network "net"`,
	}
	if string(part) != "{}" || !reflect.DeepEqual(problems, want) {
		t.Errorf("part %s, problems:\n%s", part, strings.Join(problems, "\n"))
	}

	// With published ports: one on "::", a service without a port of its
	// own, and one whose port is not published, which leaves the
	// container out.
	pub := ctr("pub", map[string]string{
		"fit.enable": "true", "fit.port": "80",
		"fit.services.a.listeners": "tcp",
	}, "10.0.0.4", 80)
	pub.Ports[0].PublicPort, pub.Ports[0].IP = 8080, "::"
	unpub := ctr("unpub", map[string]string{
		"fit.enable": "true", "fit.port": "80",
		"fit.services.b.listeners": "tcp", "fit.services.b.port": "81",
	}, "10.0.0.5", 80)
	unpub.Ports[0].PublicPort = 8081
	part, problems = translate([]container{pub, unpub}, &config.DockerProvider{UsePublishedPorts: true})
	if !strings.Contains(string(part), `"[::1]:8080"`) {
		t.Errorf("the port published on :: is not on ::1:\n%s", part)
	}
	if strings.Contains(string(part), "8081") || strings.Contains(string(part), "unpub") {
		t.Errorf("a container with a service not published was taken, or left an upstream:\n%s", part)
	}
	if !reflect.DeepEqual(problems, []string{"container unpub: service b: port 81 is not published on the host"}) {
		t.Errorf("problems: %q", problems)
	}
}

func TestClientFails(t *testing.T) {
	big := strings.Repeat(" ", maxListBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("filters") {
		case `{"x":["json"]}`:
			w.Write([]byte("nope"))
		case `{"x":["big"]}`:
			w.Write([]byte(big))
		case `{"x":["cut"]}`:
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("[{}"))
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(&config.DockerProvider{Endpoint: "tcp://" + srv.Listener.Addr().String()})
	if c.base != "http://"+srv.Listener.Addr().String() {
		t.Fatalf("base: %q", c.base)
	}
	for _, tc := range []struct{ filter, want string }{
		{"json", "GET /containers/json: invalid character"},
		{"big", "the list is larger than"},
		{"cut", "GET /containers/json: unexpected EOF"},
	} {
		_, err := c.list(context.Background(), map[string][]string{"x": {tc.filter}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.filter, err, tc.want)
		}
	}
	c.base = "::bad"
	if _, err := c.list(context.Background(), nil); err == nil {
		t.Error("a bad base: no error")
	}
	if p := New("dk", &config.DockerProvider{}, nil); p.Name() != "dk" {
		t.Errorf("Name: %q", p.Name())
	}
}

// TestProviderListFails checks that a listing that fails after an event,
// or at a refresh, ends the watch, which is reported and tried again.
func TestProviderListFails(t *testing.T) {
	for _, byEvent := range []bool{true, false} {
		t.Run(fmt.Sprintf("event=%v", byEvent), func(t *testing.T) {
			shorten(t)
			d := newDaemon(t)
			d.set(ctr("web", map[string]string{"fit.enable": "true"}, "10.0.0.2", 80))
			refresh := time.Hour
			if !byEvent {
				refresh = 30 * time.Millisecond
			}
			var mu sync.Mutex
			var lines []string
			p := New("dk", &config.DockerProvider{Endpoint: "unix://" + d.socket, RefreshInterval: config.Duration(refresh)}, logs.Func(func(format string, args ...any) {
				mu.Lock()
				lines = append(lines, fmt.Sprintf(format, args...))
				mu.Unlock()
			}))
			// Parts sent at a refresh before the daemon fails are let
			// through.
			got := &parts{ch: make(chan []byte, 1024)}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				p.Run(ctx, got.update)
			}()
			got.next(t)
			d.mu.Lock()
			d.fail = true
			d.mu.Unlock()
			if byEvent {
				d.events <- `{"Type":"container","Action":"start"}`
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				mu.Lock()
				all := strings.Join(lines, "\n")
				mu.Unlock()
				if strings.Contains(all, "500 Internal Server Error") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("lines:\n%s", all)
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			<-done
		})
	}
}
