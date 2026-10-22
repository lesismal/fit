//go:build !windows

package e2e

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// dockerImage serves the test's containers: nginx, which the benchmark
// harness uses too, answering each its own host name.
const dockerImage = "nginx:1.31.4"

// docker runs the docker command, failing the test on an error.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// replicas starts and stops the replicas of one service, web, of a Compose
// project: with docker compose where it is installed, or else with docker
// run, labelling the containers as Compose does, which is all the provider
// sees of Compose.
type replicas struct {
	t       *testing.T
	project string
	file    string // the Compose file, when Compose is used
	n       int
}

const webLabels = `fit.enable: "true"
      fit.port: "80"
      fit.routes.main.path: "/*"`

func newReplicas(t *testing.T) *replicas {
	r := &replicas{t: t, project: fmt.Sprintf("fite2e%d", os.Getpid())}
	if exec.Command("docker", "compose", "version").Run() == nil {
		r.file = filepath.Join(t.TempDir(), "compose.yaml")
		compose := `services:
  web:
    image: ` + dockerImage + `
    command: ["sh", "-c", "echo $$HOSTNAME > /usr/share/nginx/html/index.html && exec nginx -g 'daemon off;'"]
    labels:
      ` + webLabels + `
`
		if err := os.WriteFile(r.file, []byte(compose), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("using docker compose")
	} else {
		t.Log("docker compose is not installed: using docker run with Compose's labels")
	}
	t.Cleanup(r.down)
	return r
}

func (r *replicas) name(i int) string { return fmt.Sprintf("%s-web-%d", r.project, i) }

// scale runs n replicas in all, starting the missing ones.
func (r *replicas) scale(n int) {
	r.t.Helper()
	if r.file != "" {
		docker(r.t, "compose", "-p", r.project, "-f", r.file, "up", "-d", "--scale", fmt.Sprintf("web=%d", n))
		r.n = n
		return
	}
	for ; r.n < n; r.n++ {
		docker(r.t, "run", "-d", "--name", r.name(r.n+1),
			"--label", "com.docker.compose.project="+r.project, "--label", "com.docker.compose.service=web",
			"--label", "fit.enable=true", "--label", "fit.port=80", "--label", "fit.routes.main.path=/*",
			dockerImage, "sh", "-c", `echo $HOSTNAME > /usr/share/nginx/html/index.html && exec nginx -g 'daemon off;'`)
	}
}

// stop stops replica i, leaving it there, stopped.
func (r *replicas) stop(i int) {
	r.t.Helper()
	docker(r.t, "stop", "-t", "1", r.name(i))
}

// host returns the host name replica i answers with.
func (r *replicas) host(i int) string {
	r.t.Helper()
	return docker(r.t, "inspect", "-f", "{{.Config.Hostname}}", r.name(i))
}

func (r *replicas) down() {
	if r.file != "" {
		_ = exec.Command("docker", "compose", "-p", r.project, "-f", r.file, "down", "-t", "1").Run()
		return
	}
	for i := 1; i <= r.n; i++ {
		_ = exec.Command("docker", "rm", "-f", r.name(i)).Run()
	}
}

// TestDockerProvider runs fit with the Docker provider and follows a
// Compose service's containers as they start and stop: one replica served,
// a second one added to its upstream, the first stopped and dropped, and
// the route gone with the last.
func TestDockerProvider(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the containers' addresses are reached from the host on Linux only")
	}
	if os.Getenv("FIT_E2E_DOCKER") == "0" {
		t.Skip("FIT_E2E_DOCKER=0")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	if exec.Command("docker", "image", "inspect", dockerImage).Run() != nil {
		docker(t, "pull", dockerImage)
	}
	r := newReplicas(t)
	r.scale(1)

	f, addrs := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "admin": {"address": "127.0.0.1:0"},
  "providers": [{"docker": {"refresh_interval": "1s"}}]
}`)
	go func() {
		for range f.lines { // read what fit prints, so that it never waits on its stdout
		}
	}()
	web := "http://" + addrs["web"] + "/"
	c := client(false)
	// answers returns the replicas' host names that n requests got, and
	// how many got none.
	answers := func(n int) (map[string]int, int) {
		hosts, none := map[string]int{}, 0
		for range n {
			resp, err := c.Get(web)
			if err != nil {
				none++
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				none++
				continue
			}
			hosts[strings.TrimSpace(string(body))]++
		}
		return hosts, none
	}
	waitFor := func(what string, ok func(hosts map[string]int, none int) bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			hosts, none := answers(20)
			if ok(hosts, none) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: answered by %v, %d unanswered\nfit's stderr:\n%s", what, hosts, none, f.stderr.String())
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	first := r.host(1)
	waitFor("the first replica", func(hosts map[string]int, none int) bool {
		return hosts[first] == 20
	})
	effective := get(t, "http://"+addrs["admin"]+"/config/effective")
	if !strings.Contains(effective, `"source":"docker"`) || !strings.Contains(effective, `"name":"`+r.project+`-web.main"`) {
		t.Fatalf("/config/effective: %s", effective)
	}

	r.scale(2)
	second := r.host(2)
	waitFor("both replicas", func(hosts map[string]int, none int) bool {
		return hosts[first] > 0 && hosts[second] > 0 && none == 0
	})

	r.stop(1)
	waitFor("the second replica alone", func(hosts map[string]int, none int) bool {
		return hosts[second] == 20
	})

	r.stop(2)
	waitFor("no route", func(hosts map[string]int, none int) bool {
		return none == 20
	})
	if resp, err := c.Get(web); err != nil || resp.StatusCode != 404 {
		t.Fatalf("with no replicas: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
}
