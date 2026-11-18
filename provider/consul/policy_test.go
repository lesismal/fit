package consul

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"

	_ "github.com/lesismal/fit/handler/proxy"
	_ "github.com/lesismal/fit/handler/static"
)

// TestTagPolicy translates services whose registrants point fit at its
// own Admin API, and whose tags would take the operator's host, outrank
// its routes and serve the host's files: what tags may do is bounded by
// default, and the addresses fit sends to are not local ones.
func TestTagPolicy(t *testing.T) {
	attack := []string{"fit.enable=true",
		"fit.routes.x.host=admin.example.com", "fit.routes.x.priority=2000000000",
		`fit.routes.x.handler={"module":"static","params":{"root":"/"}}`}
	services := map[string][]entry{
		"adm":   {inst{svc: "adm", id: "a1", node: "n1", addr: "127.0.0.1", port: 2019, tags: []string{"fit.enable=true", "fit.routes.r.host=a.apps.example.com"}}.entry()},
		"v6":    {inst{svc: "v6", id: "a2", node: "n1", addr: "::1", port: 2019, tags: []string{"fit.enable=true"}}.entry()},
		"short": {inst{svc: "short", id: "a3", node: "n1", addr: "127.1", port: 2019, tags: []string{"fit.enable=true"}}.entry()},
		"meta":  {inst{svc: "meta", id: "a4", node: "n1", addr: "169.254.169.254", port: 80, tags: []string{"fit.enable=true"}}.entry()},
		"loc":   {inst{svc: "loc", id: "a5", node: "n1", addr: "localhost", port: 2019, tags: []string{"fit.enable=true"}}.entry()},
		"evil":  {inst{svc: "evil", id: "e1", node: "n1", addr: "10.0.0.66", port: 80, tags: attack}.entry()},
	}
	part, problems := translate(services, &config.ConsulProvider{}, nil)
	if got := upstreams(t, part); !reflect.DeepEqual(got, map[string][]string{"evil": {"10.0.0.66:80"}}) || strings.Contains(string(part), `"routes"`) {
		t.Errorf("defaults: %s", part)
	}
	for _, want := range []string{
		"service adm, instance a1 on node n1: its address, 127.0.0.1, is a local one",
		"service v6, instance a2 on node n1: its address, ::1, is a local one",
		"service short, instance a3 on node n1: its address, 127.1, is a local one",
		"service meta, instance a4 on node n1: its address, 169.254.169.254, is a local one",
		"service loc, instance a5 on node n1: its address, localhost, is a local one",
		`service evil, instance e1 on node n1: route x: host "admin.example.com" is not among the provider's allowed_hosts`,
	} {
		if !hasProblem(problems, want) {
			t.Errorf("no problem %q in:\n%s", want, strings.Join(problems, "\n"))
		}
	}

	// With every host allowed, the priority and the handler are still
	// refused; allow_local_addresses takes the loopback.
	opts := &config.ConsulProvider{AllowedHosts: []string{"*"}, AllowLocalAddresses: true}
	part, problems = translate(map[string][]entry{"evil": services["evil"], "adm": services["adm"]}, opts, nil)
	if got := upstreams(t, part); !reflect.DeepEqual(got, map[string][]string{"adm": {"127.0.0.1:2019"}, "evil": {"10.0.0.66:80"}}) {
		t.Errorf("allow_local_addresses: %v", got)
	}
	if !hasProblem(problems, "service evil, instance e1 on node n1: route x: priority 2000000000 is above the provider's max_priority, 0") {
		t.Errorf("problems: %v", problems)
	}
	opts.MaxPriority = 2000000000
	if _, problems = translate(map[string][]entry{"evil": services["evil"]}, opts, nil); !hasProblem(problems,
		`service evil, instance e1 on node n1: route x: handler: module "static" is not one labels may use`) {
		t.Errorf("problems: %v", problems)
	}
}

func hasProblem(problems []string, prefix string) bool {
	for _, p := range problems {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
