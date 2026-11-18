package docker

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"

	_ "github.com/lesismal/fit/handler/proxy"
	_ "github.com/lesismal/fit/handler/respond"
	_ "github.com/lesismal/fit/handler/static"
	_ "github.com/lesismal/fit/middleware/headers"
	_ "github.com/lesismal/fit/middleware/limit"
)

// TestLabelPolicy translates the labels of a container that would take the
// operator's host, outrank its routes and serve the host's files, and of
// one on the host's network that would reach fit's Admin API: what labels
// may do is bounded, by default, and each route left out is reported, the
// rest of the container still taken.
func TestLabelPolicy(t *testing.T) {
	attack := ctr("evil", map[string]string{
		"fit.enable":                  "true",
		"fit.routes.x.host":           "admin.example.com",
		"fit.routes.x.priority":       "2000000000",
		"fit.routes.x.handler":        `{"module":"static","params":{"root":"/"}}`,
		"fit.routes.files.host":       "evil.apps.example.com",
		"fit.routes.files.handler":    `{"module":"static","params":{"root":"/"}}`,
		"fit.routes.top.host":         "top.apps.example.com",
		"fit.routes.top.priority":     "2000000000",
		"fit.routes.other.host":       "o.apps.example.com",
		"fit.routes.other.handler":    `{"module":"proxy","params":{"upstream":"operators-admin"}}`,
		"fit.routes.mirror.host":      "m.apps.example.com",
		"fit.routes.mirror.handler":   `{"module":"proxy","params":{"upstream":"evil","mirror":{"upstream":"operators-admin"}}}`,
		"fit.routes.crlf.host":        "c.apps.example.com",
		"fit.routes.crlf.middleware":  `[{"module":"headers","params":{"request":{"set":{"X-A":"b\r\nX-Injected: 1"}}}}]`,
		"fit.routes.cache.host":       "k.apps.example.com",
		"fit.routes.cache.middleware": `[{"module":"cache","params":{}}]`,
		"fit.routes.any.path":         "/",
		"fit.routes.ok.host":          "evil.apps.example.com",
		"fit.routes.ok.middleware":    `[{"module":"headers","params":{"response":{"set":{"X-Served-By":"evil"}}}}]`,
	}, "10.0.0.66", 80)
	admin := ctr("adm", map[string]string{"fit.enable": "true", "fit.port": "2019", "fit.routes.a.host": "a.apps.example.com"}, "", 80)
	admin.HostConfig.NetworkMode = "host"

	// The defaults: no host may be matched, so no route is taken; the
	// container on the host's network is not.
	part, problems := translate([]container{attack, admin}, &config.DockerProvider{}, nil)
	if got := decoded(t, part); got["routes"] != nil || len(got["upstreams"].([]any)) != 1 {
		t.Errorf("defaults: %s", part)
	}
	for _, want := range []string{
		`container adm: its address, 127.0.0.1:2019, is a local one`,
		`container evil: route x: host "admin.example.com" is not among the provider's allowed_hosts; it is left out`,
		`container evil: route any: it matches every host, which takes "*" in the provider's allowed_hosts; it is left out`,
	} {
		if !hasPrefix(problems, want) {
			t.Errorf("no problem %q in:\n%s", want, strings.Join(problems, "\n"))
		}
	}

	// The hosts below apps.example.com: only ok is taken; the rest break
	// the policy or would have the part refused.
	opts := &config.DockerProvider{AllowedHosts: []string{"*.apps.example.com"}}
	part, problems = translate([]container{attack}, opts, nil)
	got := decoded(t, part)
	routes, _ := got["routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["name"] != "evil.ok" {
		t.Errorf("allowed_hosts: %s", part)
	}
	for _, want := range []string{
		`container evil: route x: host "admin.example.com" is not among the provider's allowed_hosts`,
		`container evil: route top: priority 2000000000 is above the provider's max_priority, 0`,
		`container evil: route files: handler: module "static" is not one labels may use`,
		`container evil: route other: handler: params.upstream: there is no upstream named "operators-admin"`,
		`container evil: route mirror: handler: params.mirror.upstream: there is no upstream named "operators-admin"`,
		`container evil: route crlf: middleware 0: params.request: the value of X-A holds a line break or NUL`,
		`container evil: route cache: middleware 0: module "cache" is not one labels may use`,
	} {
		if !hasPrefix(problems, want) {
			t.Errorf("no problem %q in:\n%s", want, strings.Join(problems, "\n"))
		}
	}
	if _, err := config.Load(withListener(part)); err != nil {
		t.Errorf("the part is refused: %v\n%s", err, part)
	}

	// Widened: static may be used, and priorities up to 10; the host's
	// loopback is taken.
	opts = &config.DockerProvider{AllowedHosts: []string{"*"}, AllowedModules: []string{"static"}, MaxPriority: 10, AllowLocalAddresses: true, AllowFilePaths: true}
	part, _ = translate([]container{attack, admin}, opts, nil)
	var names []string
	for _, r := range decoded(t, part)["routes"].([]any) {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	if want := []string{"adm.a", "evil.any", "evil.files", "evil.ok"}; !reflect.DeepEqual(names, want) {
		t.Errorf("widened: %v, want %v", names, want)
	}
}

func hasPrefix(problems []string, want string) bool {
	for _, p := range problems {
		if strings.HasPrefix(p, want) {
			return true
		}
	}
	return false
}

// withListener returns part with a listener, for config.Load.
func withListener(part []byte) []byte {
	return []byte(`{"listeners": [{"name": "web", "address": ":8080"}], ` + strings.TrimPrefix(string(part), "{"))
}
