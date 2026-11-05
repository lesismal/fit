package fitconf

import "testing"

// mptcp is a .conf directive like any other boolean field: bare for true, or
// with true or false after it, everywhere a listener is configured.
func TestMPTCPDirectives(t *testing.T) {
	for _, c := range []struct{ name, fit, json string }{
		{"listener", "listener web :8080 { mptcp }\n",
			`{"listeners": [{"name": "web", "address": ":8080", "mptcp": true}]}`},
		{"listener with false", "listener web :8080 { mptcp false }\n",
			`{"listeners": [{"name": "web", "address": ":8080", "mptcp": false}]}`},
		{"admin and console", "admin { address 127.0.0.1:2019; mptcp; console { address 127.0.0.1:2020; mptcp } }\n",
			`{"admin": {"address": "127.0.0.1:2019", "mptcp": true, "console": {"address": "127.0.0.1:2020", "mptcp": true}}}`},
		{"streaming", "streaming { rtmp { listen :1935; mptcp }; rtsp { listen :8554; mptcp } }\n",
			`{"streaming": {"rtmp": {"listen": ":1935", "mptcp": true}, "rtsp": {"listen": ":8554", "mptcp": true}}}`},
		{"turn", `webrtc { turn { listen :3478; listen_tcp :3478; mptcp } }` + "\n",
			`{"webrtc": {"turn": {"listen": [":3478"], "listen_tcp": [":3478"], "mptcp": true}}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := Adapt([]byte(c.fit))
			if err != nil {
				t.Fatal(err)
			}
			if !same(t, r.JSON, []byte(c.json)) {
				t.Fatalf("got %s, want %s", r.JSON, c.json)
			}
		})
	}
}
