//go:build !windows

package e2e

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
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
// and their statuses written back; an HTTPS listener with a Secret's
// certificate and, with the experimental channel's CRDs, a TCPRoute are
// served; a second replica joins, a change to the route is followed, and
// the route gone is no longer served.
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

	// fit writes back the statuses of its class, its Gateway and the route.
	waitStatus := func(what string, args ...string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			got := kubectl(t, kc, "", args...)
			if got == "True" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %q\nfit's stderr:\n%s", what, got, f.stderr.String())
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	waitStatus("the class accepted", "get", "gatewayclass", class, "-o", `jsonpath={.status.conditions[?(@.type=="Accepted")].status}`)
	waitStatus("the Gateway programmed", "-n", ns, "get", "gateway", "gw", "-o", `jsonpath={.status.conditions[?(@.type=="Programmed")].status}`)
	waitStatus("the route accepted", "-n", ns, "get", "httproute", "echo", "-o", `jsonpath={.status.parents[0].conditions[?(@.type=="Accepted")].status}`)

	// An HTTPS listener with a Secret's certificate takes the route too;
	// with the experimental channel's CRDs, a TCP listener and a TCPRoute
	// proxy connections to the Service as they are.
	cert, err := certtest.Write(t.TempDir(), "fit-e2e.example")
	if err != nil {
		t.Fatal(err)
	}
	kubectl(t, kc, "", "-n", ns, "create", "secret", "tls", "web-cert", "--cert="+cert.CertFile, "--key="+cert.KeyFile)
	httpsPort, tcpPort := gatewayPort(t), gatewayPort(t)
	listeners := fmt.Sprintf(`[{name: http, port: %d, protocol: HTTP}, {name: https, port: %d, protocol: HTTPS, tls: {certificateRefs: [{name: web-cert}]}}`, port, httpsPort)
	withTCP := exec.Command("kubectl", "--kubeconfig", kc, "get", "crd", "tcproutes.gateway.networking.k8s.io").Run() == nil
	if withTCP {
		listeners += fmt.Sprintf(`, {name: tcp, port: %d, protocol: TCP}`, tcpPort)
	}
	doc := `apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: gw, namespace: ` + ns + `}
spec:
  gatewayClassName: ` + class + `
  listeners: ` + listeners + `]
`
	if withTCP {
		doc += `---
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TCPRoute
metadata: {name: echo-tcp, namespace: ` + ns + `}
spec:
  parentRefs: [{name: gw, sectionName: tcp}]
  rules: [{backendRefs: [{name: echo, port: 80}]}]
`
	}
	kubectl(t, kc, doc, "apply", "-f", "-")
	reaches := func(what string, c *stdhttp.Client, url string) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for {
			resp, err := c.Get(url)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return
				}
				err = fmt.Errorf("status %d", resp.StatusCode)
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %v\nfit's stderr:\n%s", what, err, f.stderr.String())
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	https := &stdhttp.Client{Timeout: 5 * time.Second, Transport: &stdhttp.Transport{
		TLSClientConfig: &tls.Config{ServerName: "fit-e2e.example", RootCAs: certtest.Pool(cert)}}}
	reaches("HTTPS", https, fmt.Sprintf("https://127.0.0.1:%d/hostname", httpsPort))
	if withTCP {
		reaches("the TCPRoute", client(false), fmt.Sprintf("http://127.0.0.1:%d/hostname", tcpPort))
		waitStatus("the TCPRoute accepted", "-n", ns, "get", "tcproute", "echo-tcp", "-o", `jsonpath={.status.parents[0].conditions[?(@.type=="Accepted")].status}`)
	} else {
		t.Log("no TCPRoute CRD: the TCP listener is not tried")
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
