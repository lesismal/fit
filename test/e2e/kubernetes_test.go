//go:build !windows

package e2e

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// kubectl runs kubectl against kubeconfig, with stdin, failing the test on
// an error.
func kubectl(t *testing.T, kubeconfig, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("kubectl", append([]string{"--kubeconfig", kubeconfig}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gatewayPort returns a TCP port free on 127.0.0.1 now.
func gatewayPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestKubernetesGateway runs fit with the Kubernetes provider against a
// cluster, a kind cluster in CI, whose pods the host reaches: a Gateway of
// fit's GatewayClass and an HTTPRoute to a Deployment's Service are served,
// a second replica joins, a change to the route is followed, and the route
// gone is no longer served.
//
// It needs FIT_E2E_KUBECONFIG, the kubeconfig of a cluster with the Gateway
// API's CRDs installed, and kubectl; CI's "Kubernetes e2e (kind)" job makes
// both. Otherwise it is skipped.
func TestKubernetesGateway(t *testing.T) {
	kc := os.Getenv("FIT_E2E_KUBECONFIG")
	if kc == "" {
		t.Skip("FIT_E2E_KUBECONFIG is not set")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("no kubectl")
	}
	ns := fmt.Sprintf("fit-e2e-%d", os.Getpid())
	class := ns
	port := gatewayPort(t)
	kubectl(t, kc, "", "create", "namespace", ns)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "--kubeconfig", kc, "delete", "namespace", ns, "--wait=false").Run()
		_ = exec.Command("kubectl", "--kubeconfig", kc, "delete", "gatewayclass", class, "--wait=false").Run()
	})
	route := func(filters string) string {
		return `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: echo, namespace: ` + ns + `}
spec:
  parentRefs: [{name: gw}]
  rules:
  - matches: [{path: {type: PathPrefix, value: /}}]
    filters: [` + filters + `]
    backendRefs: [{name: echo, port: 80}]
`
	}
	kubectl(t, kc, `apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: `+class+`}
spec: {controllerName: lesismal.github.io/fit}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: echo, namespace: `+ns+`}
spec:
  replicas: 1
  selector: {matchLabels: {app: echo}}
  template:
    metadata: {labels: {app: echo}}
    spec:
      containers:
      - name: echo
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        args: [netexec, --http-port=8080]
        ports: [{containerPort: 8080}]
        readinessProbe: {httpGet: {path: /hostname, port: 8080}, periodSeconds: 1}
---
apiVersion: v1
kind: Service
metadata: {name: echo, namespace: `+ns+`}
spec:
  selector: {app: echo}
  ports: [{port: 80, targetPort: 8080}]
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: gw, namespace: `+ns+`}
spec:
  gatewayClassName: `+class+`
  listeners: [{name: http, port: `+fmt.Sprint(port)+`, protocol: HTTP}]
---
`+route(""), "apply", "-f", "-")
	kubectl(t, kc, "", "-n", ns, "rollout", "status", "deployment/echo", "--timeout=180s")

	f, addrs := startFit(t, `{
  "listeners": [{"name": "base", "address": "127.0.0.1:0"}],
  "admin": {"address": "127.0.0.1:0"},
  "providers": [{"kubernetes": {"kubeconfig": "`+kc+`", "listen_host": "127.0.0.1", "namespaces": ["`+ns+`"]}}]
}`)
	go func() {
		for range f.lines { // read what fit prints, so that it never waits on its stdout
		}
	}()
	url := fmt.Sprintf("http://127.0.0.1:%d/hostname", port)
	c := client(false)
	// answers returns the pods n requests reached, how many got no answer
	// or not 200, and the last answer's X-Fit-E2E field.
	answers := func(n int) (pods map[string]int, none int, field string) {
		pods = map[string]int{}
		for range n {
			resp, err := c.Get(url)
			if err != nil {
				none++
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			field = resp.Header.Get("X-Fit-E2E")
			if resp.StatusCode != 200 {
				none++
				continue
			}
			pods[strings.TrimSpace(string(body))]++
		}
		return pods, none, field
	}
	waitFor := func(what string, ok func(pods map[string]int, none int, field string) bool) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for {
			pods, none, field := answers(20)
			if ok(pods, none, field) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: pods %v, %d unanswered, X-Fit-E2E %q\nfit's stderr:\n%s", what, pods, none, field, f.stderr.String())
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	waitFor("one replica", func(pods map[string]int, none int, _ string) bool {
		return len(pods) == 1 && none == 0
	})
	effective := get(t, "http://"+addrs["admin"]+"/config/effective")
	if !strings.Contains(effective, `"source":"kubernetes"`) || !strings.Contains(effective, `"name":"`+ns+`.echo.80"`) {
		t.Fatalf("/config/effective: %s", effective)
	}

	kubectl(t, kc, "", "-n", ns, "scale", "deployment/echo", "--replicas=2")
	kubectl(t, kc, "", "-n", ns, "rollout", "status", "deployment/echo", "--timeout=180s")
	waitFor("two replicas", func(pods map[string]int, none int, _ string) bool {
		return len(pods) == 2 && none == 0
	})

	kubectl(t, kc, route(`{type: ResponseHeaderModifier, responseHeaderModifier: {add: [{name: X-Fit-E2E, value: "yes"}]}}`), "apply", "-f", "-")
	waitFor("the route changed", func(_ map[string]int, none int, field string) bool {
		return none == 0 && field == "yes"
	})

	kubectl(t, kc, "", "-n", ns, "delete", "httproute", "echo")
	waitFor("the route gone", func(pods map[string]int, none int, _ string) bool {
		return none == 20
	})
}
