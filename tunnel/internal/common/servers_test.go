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
		{EndpointReq{Protocol: "tcp", Address: " h:1 "}, "h:1"},
	} {
		if got := c.e.address(); got != c.want {
			t.Errorf("%+v: %q", c.e, got)
		}
	}
}

func servers(t *testing.T, m Member) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := m.Get("servers", &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEditServer(t *testing.T) {
	m := Member{}
	ep := []EndpointReq{{Protocol: "tcp", Address: "h:1", Enabled: true}, {Protocol: config.TunnelQUIC, Address: "h:2"}}
	if err := EditServer(m, "", ServerReq{Name: " a ", Endpoints: ep, Password: "pw", BodyPassword: "bp", PoolSize: 2, QUICPin: " pin ", QUICInsecure: true}, false); err != nil {
		t.Fatal(err)
	}
	// A server named by its first address, beside it.
	if err := EditServer(m, "", ServerReq{Endpoints: []EndpointReq{{Protocol: "tcp", Address: "h:9", Enabled: true}}, Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	got := servers(t, m)
	if len(got) != 2 || got[0]["name"] != "a" || got[0]["body_password"] != "bp" || got[0]["quic_pin"] != "pin" ||
		got[0]["quic_insecure"] != true || got[0]["pool_size"] != float64(2) {
		t.Fatalf("%v", got)
	}
	if eps := got[0]["endpoints"].([]any); len(eps) != 2 || eps[1].(map[string]any)["disabled"] != true || eps[1].(map[string]any)["address"] != "quic://h:2" {
		t.Fatalf("endpoints %v", eps)
	}

	// A change keeps the passwords left empty, and what the pages do not
	// show; clearBody drops the body password.
	var raw []json.RawMessage
	_ = m.Get("servers", &raw)
	var first map[string]any
	_ = json.Unmarshal(raw[0], &first)
	first["dial_timeout"] = "3s"
	raw[0], _ = json.Marshal(first)
	_ = m.Set("servers", raw)
	if err := EditServer(m, "a", ServerReq{Name: "a", Endpoints: ep[:1]}, false); err != nil {
		t.Fatal(err)
	}
	got = servers(t, m)
	if got[0]["password"] != "pw" || got[0]["body_password"] != "bp" || got[0]["dial_timeout"] != "3s" || got[0]["pool_size"] != nil {
		t.Fatalf("kept %v", got[0])
	}
	if err := EditServer(m, "a", ServerReq{Name: "b", Endpoints: ep[:1]}, true); err != nil {
		t.Fatal(err)
	}
	if got = servers(t, m); got[0]["name"] != "b" || got[0]["body_password"] != nil {
		t.Fatalf("renamed %v", got[0])
	}

	for _, c := range []struct {
		name string
		req  ServerReq
		err  string
	}{
		{"nope", ServerReq{Endpoints: ep, Password: "pw"}, "no server named nope"},
		{"", ServerReq{Endpoints: []EndpointReq{{Address: " "}}, Password: "pw"}, "needs an address"},
		{"", ServerReq{Endpoints: ep}, "needs a password"},
		{"", ServerReq{Name: "b", Endpoints: ep, Password: "pw"}, "a server named b exists"},
		{"b", ServerReq{Endpoints: []EndpointReq{{Address: "h:9"}}}, "a server named h:9 exists"},
	} {
		if err := EditServer(m, c.name, c.req, false); !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%q %+v: %v", c.name, c.req, err)
		}
	}

	if err := EditServer(Member{"servers": json.RawMessage(`{}`)}, "", ServerReq{}, false); err == nil || errors.Is(err, ErrBadRequest) {
		t.Fatalf("servers not a list: %v", err)
	}
}

func TestDeleteServer(t *testing.T) {
	m := Member{"servers": json.RawMessage(`[{"name":"a"},{"endpoints":[{"address":"h:1"}]}]`)}
	if err := DeleteServer(m, "nope"); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	if err := DeleteServer(m, "h:1"); err != nil {
		t.Fatal(err)
	}
	if got := servers(t, m); len(got) != 1 || got[0]["name"] != "a" {
		t.Fatalf("%v", got)
	}
	if err := DeleteServer(m, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["servers"]; ok {
		t.Fatal("an empty list was kept")
	}
	if err := DeleteServer(Member{"servers": json.RawMessage(`1`)}, "a"); err == nil {
		t.Fatal("servers not a list")
	}
}
