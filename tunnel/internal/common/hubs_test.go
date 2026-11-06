package common

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

func TestEndpointAddress(t *testing.T) {
	for _, c := range []struct {
		e    EndpointReq
		want string
	}{
		{EndpointReq{Protocol: config.TunnelWS, Address: " "}, ""},
		{EndpointReq{Protocol: config.TunnelWS, Address: "wss://h:1/x"}, "wss://h:1/x"},
		{EndpointReq{Protocol: config.TunnelWS, Address: "h:1"}, "ws://h:1" + config.DefaultTunnelWSPath},
		{EndpointReq{Protocol: config.TunnelWS, Address: "h:1/tun"}, "ws://h:1/tun"},
		{EndpointReq{Protocol: config.TunnelQUIC, Address: "h:1"}, "quic://h:1"},
		{EndpointReq{Protocol: config.TunnelTLS, Address: "h:1"}, "tls://h:1"},
		{EndpointReq{Protocol: config.TunnelWSS, Address: "h:1"}, "wss://h:1" + config.DefaultTunnelWSPath},
		{EndpointReq{Protocol: config.TunnelWSS, Address: "h:1/tun"}, "wss://h:1/tun"},
		{EndpointReq{Protocol: "tcp", Address: " h:1 "}, "h:1"},
	} {
		if got, err := c.e.address(); err != nil || got != c.want {
			t.Errorf("%+v: %q, %v", c.e, got, err)
		}
	}
	// The review's attacks: a fragment or a query that would have changed
	// how the hub's certificate is checked, and a user; and an address the
	// config refuses otherwise.
	for _, e := range []EndpointReq{
		{Protocol: config.TunnelTLS, Address: "tls://hub:7000#?pin=" + strings.Repeat("0", 64) + "&"},
		{Protocol: config.TunnelTLS, Address: "hub:443#?x="},
		{Protocol: config.TunnelQUIC, Address: "hub:7000#?insecure=1&"},
		{Protocol: config.TunnelWSS, Address: "hub:443/tunnel#?ca=/tmp/evil.pem&"},
		{Protocol: config.TunnelWSS, Address: "wss://hub:443/tunnel?pin=" + strings.Repeat("0", 64)},
		{Protocol: config.TunnelTLS, Address: "user@hub:7000"},
		{Protocol: "tcp", Address: "hub:7000#x"},
		{Protocol: "tcp", Address: "hub"},
	} {
		if got, err := e.address(); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "is not") && !strings.Contains(err.Error(), "pin or ca_file") {
			t.Errorf("%+v: %q, %v", e, got, err)
		}
	}
}

// The pages cannot write an endpoint whose fragment would have overridden
// the hub's pin.
func TestEditHubRefusesFragment(t *testing.T) {
	m := Member{}
	ep := []EndpointReq{{Protocol: config.TunnelTLS, Address: "hub:7000#?pin=" + strings.Repeat("0", 64) + "&", Enabled: true}}
	err := EditHub(m, "", HubReq{Endpoints: ep, Password: "pw", Pin: strings.Repeat("1", 64)}, false)
	if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "fragment") {
		t.Fatalf("%v", err)
	}
	if _, ok := m["hubs"]; ok {
		t.Fatalf("written: %v", m)
	}
}

func hubs(t *testing.T, m Member) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := m.Get("hubs", &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEditHub(t *testing.T) {
	m := Member{}
	ep := []EndpointReq{{Protocol: "tcp", Address: "h:1", Enabled: true}, {Protocol: config.TunnelQUIC, Address: "h:2"}}
	if err := EditHub(m, "", HubReq{Name: " a ", Endpoints: ep, Password: "pw", BodyPassword: "bp", PoolSize: 2, Pin: " pin ", QUICInsecure: true}, false); err != nil {
		t.Fatal(err)
	}
	// A hub named by its first address, beside it.
	if err := EditHub(m, "", HubReq{Endpoints: []EndpointReq{{Protocol: "tcp", Address: "h:9", Enabled: true}}, Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	got := hubs(t, m)
	if len(got) != 2 || got[0]["name"] != "a" || got[0]["body_password"] != "bp" || got[0]["pin"] != "pin" ||
		got[0]["quic_insecure"] != true || got[0]["pool_size"] != float64(2) {
		t.Fatalf("%v", got)
	}
	if eps := got[0]["endpoints"].([]any); len(eps) != 2 || eps[1].(map[string]any)["disabled"] != true || eps[1].(map[string]any)["address"] != "quic://h:2" {
		t.Fatalf("endpoints %v", eps)
	}

	// A change keeps the passwords left empty, and what the pages do not
	// show; clearBody drops the body password. A quic_pin, pin's old name,
	// gives way to pin.
	var raw []json.RawMessage
	_ = m.Get("hubs", &raw)
	var first map[string]any
	_ = json.Unmarshal(raw[0], &first)
	first["dial_timeout"] = "3s"
	first["ca_file"] = "/etc/ca.pem"
	first["quic_pin"] = "old"
	raw[0], _ = json.Marshal(first)
	_ = m.Set("hubs", raw)
	if err := EditHub(m, "a", HubReq{Name: "a", Endpoints: ep[:1]}, false); err != nil {
		t.Fatal(err)
	}
	got = hubs(t, m)
	if got[0]["password"] != "pw" || got[0]["body_password"] != "bp" || got[0]["dial_timeout"] != "3s" || got[0]["pool_size"] != nil ||
		got[0]["ca_file"] != "/etc/ca.pem" || got[0]["quic_pin"] != nil {
		t.Fatalf("kept %v", got[0])
	}
	// The pages of an earlier release send the pin as quic_pin.
	if err := EditHub(m, "a", HubReq{Name: "a", Endpoints: ep[:1], QUICPin: "old"}, false); err != nil {
		t.Fatal(err)
	}
	if got = hubs(t, m); got[0]["pin"] != "old" {
		t.Fatalf("quic_pin: %v", got[0])
	}
	if err := EditHub(m, "a", HubReq{Name: "b", Endpoints: ep[:1]}, true); err != nil {
		t.Fatal(err)
	}
	if got = hubs(t, m); got[0]["name"] != "b" || got[0]["body_password"] != nil {
		t.Fatalf("renamed %v", got[0])
	}
	// A token replaces the password, and the password the token.
	if err := EditHub(m, "b", HubReq{Name: "b", Endpoints: ep[:1], Token: " tok "}, false); err != nil {
		t.Fatal(err)
	}
	if got = hubs(t, m); got[0]["token"] != "tok" || got[0]["password"] != nil {
		t.Fatalf("token %v", got[0])
	}
	if err := EditHub(m, "b", HubReq{Name: "b", Endpoints: ep[:1], Password: "pw2"}, false); err != nil {
		t.Fatal(err)
	}
	if got = hubs(t, m); got[0]["token"] != nil || got[0]["password"] != "pw2" {
		t.Fatalf("password %v", got[0])
	}

	for _, c := range []struct {
		name string
		req  HubReq
		err  string
	}{
		{"nope", HubReq{Endpoints: ep, Password: "pw"}, "no hub named nope"},
		{"", HubReq{Endpoints: []EndpointReq{{Address: " "}}, Password: "pw"}, "needs an address"},
		{"", HubReq{Endpoints: ep}, "needs the token it issued for this member, or its password"},
		{"", HubReq{Name: "b", Endpoints: ep, Password: "pw"}, "a hub named b exists"},
		{"b", HubReq{Endpoints: []EndpointReq{{Address: "h:9"}}}, "a hub named h:9 exists"},
	} {
		if err := EditHub(m, c.name, c.req, false); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%q %+v: %v", c.name, c.req, err)
		}
	}

	if err := EditHub(Member{"hubs": json.RawMessage(`{}`)}, "", HubReq{}, false); err == nil || errors.Is(err, ErrBadRequest) {
		t.Fatalf("hubs not a list: %v", err)
	}
}

func TestDeleteHub(t *testing.T) {
	m := Member{"hubs": json.RawMessage(`[{"name":"a"},{"endpoints":[{"address":"h:1"}]}]`)}
	if err := DeleteHub(m, "nope"); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	if err := DeleteHub(m, "h:1"); err != nil {
		t.Fatal(err)
	}
	if got := hubs(t, m); len(got) != 1 || got[0]["name"] != "a" {
		t.Fatalf("%v", got)
	}
	if err := DeleteHub(m, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["hubs"]; ok {
		t.Fatal("an empty list was kept")
	}
	if err := DeleteHub(Member{"hubs": json.RawMessage(`1`)}, "a"); err == nil {
		t.Fatal("hubs not a list")
	}
}
