package config

import (
	"fmt"
	"strings"
	"testing"
)

// bindKind is a part of the config fit listens for: its name in an error,
// its transport, and how to set its address on a config.
type bindKind struct {
	name    string
	network string
	set     func(c *Config, address string)
}

func llmOf(c *Config) *LLM {
	if c.LLM == nil {
		c.LLM = &LLM{Hub: &LLMHub{}, Node: &LLMNode{}, Proxy: &LLMProxy{}}
	}
	return c.LLM
}

func tunnelOf(c *Config) *Tunnel {
	if c.Tunnel == nil {
		c.Tunnel = &Tunnel{Hub: &TunnelHub{}, Node: &TunnelNode{}, Proxy: &TunnelProxy{}}
	}
	return c.Tunnel
}

func streamingOf(c *Config) *Streaming {
	if c.Streaming == nil {
		c.Streaming = &Streaming{}
	}
	return c.Streaming
}

func webrtcOf(c *Config) *WebRTC {
	if c.WebRTC == nil {
		c.WebRTC = &WebRTC{TURN: &TURNServer{}}
	}
	return c.WebRTC
}

func adminOf(c *Config) *Admin {
	if c.Admin == nil {
		// Its own address out of the way of the others'.
		c.Admin = &Admin{Address: "127.0.0.1:0"}
	}
	return c.Admin
}

// bindKinds is every kind of address fit listens on.
var bindKinds = []bindKind{
	{`listener "tcp"`, "tcp", func(c *Config, a string) {
		c.Listeners = append(c.Listeners, Listener{Name: "tcp", Address: a, Protocols: []string{ProtocolHTTP1}})
	}},
	{`listener "udp"`, "udp", func(c *Config, a string) {
		c.Listeners = append(c.Listeners, Listener{Name: "udp", Address: a, Protocols: []string{ProtocolUDP}})
	}},
	{"admin", "tcp", func(c *Config, a string) { adminOf(c).Address = a }},
	{"admin.console", "tcp", func(c *Config, a string) { adminOf(c).Console = &Console{Address: a} }},
	{"llm.hub.console", "tcp", func(c *Config, a string) { llmOf(c).Hub.Console = &RoleConsole{Address: a} }},
	{"llm.hub.listen", "tcp", func(c *Config, a string) { llmOf(c).Hub.Listen = &LLMListen{Address: a} }},
	{"llm.node.console", "tcp", func(c *Config, a string) { llmOf(c).Node.Console = &RoleConsole{Address: a} }},
	{"llm.node.listen", "tcp", func(c *Config, a string) { llmOf(c).Node.Listen = &LLMListen{Address: a} }},
	{"llm.proxy.console", "tcp", func(c *Config, a string) { llmOf(c).Proxy.Console = &RoleConsole{Address: a} }},
	{"llm.proxy.listen", "tcp", func(c *Config, a string) { llmOf(c).Proxy.Listen = &LLMListen{Address: a} }},
	{"llm.proxy.api", "tcp", func(c *Config, a string) { llmOf(c).Proxy.API = &LLMProxyAPI{Address: a} }},
	{"tunnel.hub.console", "tcp", func(c *Config, a string) { tunnelOf(c).Hub.Console = &RoleConsole{Address: a} }},
	{"tunnel.hub.listeners[0]", "tcp", func(c *Config, a string) {
		h := tunnelOf(c).Hub
		h.Listeners = append(h.Listeners, TunnelListener{Protocol: TunnelWSS, Address: a})
	}},
	{"tunnel.hub.listeners[0]", "udp", func(c *Config, a string) {
		h := tunnelOf(c).Hub
		h.Listeners = append(h.Listeners, TunnelListener{Protocol: TunnelQUIC, Address: a})
	}},
	{"tunnel.hub.console_socket", "tcp", func(c *Config, a string) {
		tunnelOf(c).Hub.ConsoleSocket = &TunnelConsoleSocket{Address: a}
	}},
	{"tunnel.node.console", "tcp", func(c *Config, a string) { tunnelOf(c).Node.Console = &RoleConsole{Address: a} }},
	{"tunnel.proxy.console", "tcp", func(c *Config, a string) { tunnelOf(c).Proxy.Console = &RoleConsole{Address: a} }},
	{"tunnel.proxy.forwards[0]", "tcp", func(c *Config, a string) {
		p := tunnelOf(c).Proxy
		p.Forwards = append(p.Forwards, TunnelForward{Listen: a})
	}},
	{"tunnel.proxy.web_proxies[0]", "tcp", func(c *Config, a string) {
		p := tunnelOf(c).Proxy
		p.WebProxies = append(p.WebProxies, TunnelWebProxy{Listen: a})
	}},
	{"streaming.rtmp", "tcp", func(c *Config, a string) { streamingOf(c).RTMP = &StreamRTMP{Listen: a} }},
	{"streaming.rtsp", "tcp", func(c *Config, a string) { streamingOf(c).RTSP = &StreamRTSP{Listen: a} }},
	{"streaming.srt", "udp", func(c *Config, a string) { streamingOf(c).SRT = &StreamSRT{Listen: a} }},
	{"streaming.webrtc", "udp", func(c *Config, a string) { streamingOf(c).WebRTC = &StreamWebRTC{Listen: a} }},
	{"webrtc.turn.listen[0]", "udp", func(c *Config, a string) {
		t := webrtcOf(c).TURN
		t.Listen = append(t.Listen, a)
	}},
	{"webrtc.turn.listen_tcp[0]", "tcp", func(c *Config, a string) {
		t := webrtcOf(c).TURN
		t.ListenTCP = append(t.ListenTCP, a)
	}},
	{"webrtc.turn.listen_tls[0]", "tcp", func(c *Config, a string) {
		t := webrtcOf(c).TURN
		t.ListenTLS = append(t.ListenTLS, a)
	}},
	{"webrtc.media", "udp", func(c *Config, a string) { webrtcOf(c).Media = &WebRTCMedia{Listen: a} }},
	{"webrtc.proxy", "udp", func(c *Config, a string) { webrtcOf(c).Proxy = &WebRTCProxy{Listen: a} }},
}

// bindErrors returns what checkBinds reports of c.
func bindErrors(c *Config) []string {
	var out []string
	c.checkBinds(func(_ Value, format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) })
	return out
}

// TestBindsEveryKindClashes checks every two kinds of address fit listens on
// that share a transport clash on one address, whichever comes first, with
// an error naming the first; and that a TCP one and a UDP one do not.
func TestBindsEveryKindClashes(t *testing.T) {
	for i, a := range bindKinds {
		for j, b := range bindKinds {
			if i == j && (a.name == "admin" || strings.HasSuffix(a.name, "console") ||
				strings.HasPrefix(a.name, "llm.") || strings.HasPrefix(a.name, "streaming.") ||
				a.name == "webrtc.media" || a.name == "webrtc.proxy" || a.name == "tunnel.hub.console_socket") {
				// A kind there is only one of.
				continue
			}
			var c Config
			a.set(&c, ":7000")
			b.set(&c, "127.0.0.1:7000")
			errs := bindErrors(&c)
			if a.network != b.network {
				if len(errs) != 0 {
					t.Errorf("%s (%s) and %s (%s): %q, want no clash", a.name, a.network, b.name, b.network, errs)
				}
				continue
			}
			if len(errs) != 1 {
				t.Errorf("%s and %s on one %s address: %q, want one error", a.name, b.name, a.network, errs)
				continue
			}
			// The kinds are listed in the order binds lists them, so the
			// earlier of the two is named, at the later one.
			first, second := a, b
			if j < i {
				first, second = b, a
			}
			addr := ":7000"
			if j < i {
				addr = "127.0.0.1:7000"
			}
			want := first.name + " listens on " + strings.ToUpper(a.network) + " " + addr + " already"
			if !strings.HasPrefix(errs[0], want) {
				t.Errorf("%s then %s: %q, want it to start %q", first.name, second.name, errs[0], want)
			}
		}
	}
}

// TestBindsNormalized checks the forms of one address clash, and those of
// others do not.
func TestBindsNormalized(t *testing.T) {
	listeners := func(addrs ...string) *Config {
		c := &Config{}
		for i, a := range addrs {
			c.Listeners = append(c.Listeners, Listener{Name: fmt.Sprint(i), Address: a, Protocols: []string{ProtocolHTTP1}})
		}
		return c
	}
	for _, tc := range []struct {
		a, b string
		want string // what the error says past the address, or "-" for no error
	}{
		{":8080", ":8080", ";"},
		{":8080", "0.0.0.0:8080", ", the same address as 0.0.0.0:8080;"},
		{":8080", "[::]:8080", ", the same address as [::]:8080;"},
		{"0.0.0.0:8080", "[::]:8080", ", the same address as [::]:8080;"},
		{"[::ffff:0.0.0.0]:8080", ":8080", ", the same address as :8080;"},
		{"localhost:8080", "127.0.0.1:8080", ", the same address as 127.0.0.1:8080;"},
		{"LocalHost:8080", "localhost:8080", ", the same address as localhost:8080;"},
		{"[::ffff:127.0.0.1]:8080", "127.0.0.1:8080", ", the same address as 127.0.0.1:8080;"},
		{"127.0.0.1:8080", "127.0.0.1:08080", ", the same address as 127.0.0.1:08080;"},
		{"[fe80::1%eth0]:8080", "[FE80::1%eth0]:8080", ", the same address as [FE80::1%eth0]:8080;"},
		{":8080", "127.0.0.1:8080", ", every address, 127.0.0.1:8080 among them;"},
		{"192.0.2.1:8080", "0.0.0.0:8080", ", which 0.0.0.0:8080, every address, takes in;"},
		{"Example.COM:8080", "example.com:8080", ", the same address as example.com:8080;"},
		{"127.0.0.1:8080", "[::1]:8080", "-"},
		{"127.0.0.1:8080", "127.0.0.2:8080", "-"},
		{"a.example:8080", "b.example:8080", "-"},
		{":8080", ":8081", "-"},
		{":0", ":0", "-"},
		{"127.0.0.1:0", "localhost:0", "-"},
		{"8080", "8080", "-"},
		{":http", ":http", "-"},
	} {
		errs := bindErrors(listeners(tc.a, tc.b))
		if tc.want == "-" {
			if len(errs) != 0 {
				t.Errorf("%s and %s: %q, want no clash", tc.a, tc.b, errs)
			}
			continue
		}
		want := `listener "0" listens on TCP ` + tc.a + " already" + tc.want
		if len(errs) != 1 || !strings.HasPrefix(errs[0], want) {
			t.Errorf("%s and %s: %q, want one starting %q", tc.a, tc.b, errs, want)
		}
	}
}

// TestBindsOthers checks the addresses that share no port: the Admin API on
// a Unix socket, TCP and UDP on one port, HTTP and HTTP/3 on one listener,
// and a TCP listener beside an h3 one, which listens on both.
func TestBindsOthers(t *testing.T) {
	c := &Config{
		Admin: &Admin{Address: AdminUnixPrefix + "/run/fit.sock"},
		Listeners: []Listener{
			{Name: "web", Address: ":443", Protocols: []string{ProtocolHTTP1, ProtocolH2, ProtocolH3}},
			{Name: "dns", Address: ":53", Protocols: []string{ProtocolUDP}},
			{Name: "dns-tcp", Address: ":53", Protocols: []string{ProtocolTCP}},
		},
		Streaming: &Streaming{RTMP: &StreamRTMP{Listen: ":1935"}, SRT: &StreamSRT{Listen: ":1935"}},
	}
	if errs := bindErrors(c); len(errs) != 0 {
		t.Fatalf("%q, want none", errs)
	}
	c.Listeners = append(c.Listeners, Listener{Name: "quic", Address: "0.0.0.0:443", Protocols: []string{ProtocolUDP}})
	if errs := bindErrors(c); len(errs) != 1 || !strings.HasPrefix(errs[0], `listener "web" listens on UDP :443 already, the same address as 0.0.0.0:443;`) {
		t.Fatalf("%q, want the h3 port taken", errs)
	}

	// The Admin API and the console at their defaults.
	c = &Config{Admin: &Admin{Console: &Console{}}, Listeners: []Listener{{Name: "web", Address: "localhost:2020"}}}
	if errs := bindErrors(c); len(errs) != 1 || !strings.HasPrefix(errs[0], `listener "web" listens on TCP localhost:2020 already, the same address as 127.0.0.1:2020;`) {
		t.Fatalf("%q, want the console's default address taken", errs)
	}
	c = &Config{Admin: &Admin{}, LLM: &LLM{Hub: &LLMHub{Console: &RoleConsole{Address: "127.0.0.1:2019"}}}}
	if errs := bindErrors(c); len(errs) != 1 || !strings.HasPrefix(errs[0], `admin listens on TCP 127.0.0.1:2019 already;`) {
		t.Fatalf("%q, want the Admin API's default address taken", errs)
	}
}

// TestBindsLoaded checks a clash is reported by Load, at the later of the
// two, the HTTPS by default listeners among them.
func TestBindsLoaded(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`{"listeners": [{"name": "web", "address": ":8080"}], "tunnel": {"proxy": {"web_proxies": [{"listen": "0.0.0.0:8080", "url": "ws://h:1/", "password": "p"}],
			"console": {"address": "127.0.0.1:8081", "users": [{"username": "u", "password_hash": "$2a$10$abcdefghijklmnopqrstuuJ0RIYlS8m3wXV9z5mMn0D0Hn4N6h1zu"}]}}}}`,
			`tunnel.proxy.web_proxies[0].listen: listener "web" listens on TCP :8080 already, the same address as 0.0.0.0:8080`},
		{`{"routes": [{"match": {"host": ["a.example"]}, "handler": {"module": "respond"}}], "streaming": {"rtmp": {"listen": "0.0.0.0:443"}}}`,
			`streaming.rtmp.listen: listener "https" listens on TCP :443 already, the same address as 0.0.0.0:443`},
	} {
		_, err := Load([]byte(tc.doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s:\n%v\nwant %q", tc.doc, err, tc.want)
		}
	}
}
