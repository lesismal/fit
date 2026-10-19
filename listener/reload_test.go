package listener

import (
	"fmt"
	"net"
	stdhttp "net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
)

func compile(t *testing.T, doc string) *runtime.Graph {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// reloadDoc is a config whose listeners are given, each "name address
// protocol...", and whose one route answers body.
func reloadDoc(body string, listeners ...string) string {
	var ls []string
	for _, l := range listeners {
		f := strings.Fields(l)
		ls = append(ls, fmt.Sprintf(`{"name": %q, "address": %q, "protocols": ["%s"]}`, f[0], f[1], strings.Join(f[2:], `", "`)))
	}
	return `{"listeners": [` + strings.Join(ls, ", ") + `],
  "routes": [{"handler": {"module": "respond", "params": {"body": "` + body + `"}}}]}`
}

func addrOf(s *Server, name string) string {
	for _, a := range s.Addrs() {
		if a.Name == name {
			return a.Addr.String()
		}
	}
	return ""
}

func names(addrs []Addr) []string {
	var ns []string
	for _, a := range addrs {
		ns = append(ns, a.Name)
	}
	return ns
}

func TestReload(t *testing.T) {
	rt, s := start(t, reloadDoc("v1", "a 127.0.0.1:0 http/1.1", "b 127.0.0.1:0 http/1.1", "gone 127.0.0.1:0 http/1.1"))
	s.SetDrainTimeout(time.Second)
	a, b, gone := addrOf(s, "a"), addrOf(s, "b"), addrOf(s, "gone")
	// A keep-alive connection to a, open across the reload.
	keep := client(false)
	if _, body := get(t, keep, "http://"+a+"/", ""); body != "v1" {
		t.Fatalf("before: %q", body)
	}

	added, removed, err := s.Reload(compile(t, reloadDoc("v2",
		"a 127.0.0.1:0 http/1.1", "b 127.0.0.1:0 http/1.1 h2c", "c 127.0.0.1:0 http/1.1")))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(added), []string{"b", "c"}) || !slices.Equal(names(removed), []string{"b", "gone"}) {
		t.Fatalf("added %v, removed %v", names(added), names(removed))
	}
	if got := names(s.Addrs()); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("listeners now %v", got)
	}
	if addrOf(s, "a") != a {
		t.Fatalf("a moved from %s to %s", a, addrOf(s, "a"))
	}
	if _, body := get(t, keep, "http://"+a+"/", ""); body != "v2" {
		t.Fatalf("a after: %q", body)
	}
	if _, body := get(t, client(true), "http://"+addrOf(s, "b")+"/", ""); body != "v2" {
		t.Fatalf("b over h2c after: %q", body)
	}
	if _, body := get(t, client(false), "http://"+addrOf(s, "c")+"/", ""); body != "v2" {
		t.Fatalf("c: %q", body)
	}
	// The removed listeners are drained in the background.
	for _, old := range []string{b, gone} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			conn, err := net.DialTimeout("tcp", old, time.Second)
			if err != nil {
				break
			}
			conn.Close()
			if time.Now().After(deadline) {
				t.Fatalf("%s still takes connections", old)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if g := rt.Current(); g.Version != 2 {
		t.Errorf("version %d", g.Version)
		g.Release()
	} else {
		g.Release()
	}
}

func TestReloadFails(t *testing.T) {
	rt, s := start(t, reloadDoc("v1", "a 127.0.0.1:0 http/1.1"))
	a := addrOf(s, "a")
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	// fresh is bound before busy fails, and must be unbound again.
	_, _, err = s.Reload(compile(t, reloadDoc("v2", "a 127.0.0.1:0 http/1.1", "fresh 127.0.0.1:0 http/1.1",
		"busy "+taken.Addr().String()+" http/1.1")))
	if err == nil || !strings.Contains(err.Error(), `listener "busy"`) {
		t.Fatalf("error %v", err)
	}
	if got := names(s.Addrs()); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("listeners now %v", got)
	}
	if _, body := get(t, client(false), "http://"+a+"/", ""); body != "v1" {
		t.Fatalf("a after a failed reload: %q", body)
	}
	g := rt.Current()
	defer g.Release()
	if g.Version != 1 {
		t.Fatalf("version %d", g.Version)
	}
}

func TestReloadSameAddress(t *testing.T) {
	// A listener on a fixed port that changes its protocols is bound anew on
	// the same address, beside the old one, which then drains.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	_, s := start(t, reloadDoc("v1", "a "+addr+" http/1.1"))
	if _, _, err := s.Reload(compile(t, reloadDoc("v2", "a "+addr+" h2c"))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client(true).Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == stdhttp.StatusOK && resp.ProtoMajor == 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("h2c on %s after the reload: %v %v", addr, resp, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.Addrs(); len(got) != 1 || got[0].Addr.String() != addr || !slices.Equal(got[0].Protocols, []string{"h2c"}) {
		t.Fatalf("listeners now %+v", got)
	}
}
