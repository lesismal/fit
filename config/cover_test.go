package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestPositionPastTheEnd checks that an offset past the source's end is
// placed at its end, and that a decoder error of no known kind is placed
// where the decoder stopped: what the parser's own errors never reach.
func TestPositionPastTheEnd(t *testing.T) {
	s := &source{data: []byte("{\n  \"a\": 1\n}")}
	if line, col := s.pos(100); line != 3 || col != 2 {
		t.Errorf("past the end: %d:%d, want 3:2", line, col)
	}
	data := []byte(`{"a": 1}`)
	p := &parser{src: &source{data: data}, dec: json.NewDecoder(bytes.NewReader(data))}
	err := p.syntaxError(errors.New("broken"))
	if err.Line != 1 || err.Col != 1 || !strings.Contains(err.Msg, "invalid JSON: broken") {
		t.Errorf("%+v", err)
	}
}

// TestAccessLogZoneUnset checks an access log left out writes local times.
func TestAccessLogZoneUnset(t *testing.T) {
	var l *AccessLog
	if loc, err := l.TimeZoneLocation(); loc != time.Local || err != nil {
		t.Errorf("TimeZoneLocation() = %v, %v; want local", loc, err)
	}
	if got := l.TimeZoneName(); got != TimeZoneLocal {
		t.Errorf("TimeZoneName() = %q, want %q", got, TimeZoneLocal)
	}
}

// TestStreamingWebRTCOnProxyPort checks the streaming WebRTC role may not
// share the port of a WebRTC proxy that runs with no media role.
func TestStreamingWebRTCOnProxyPort(t *testing.T) {
	_, err := Load([]byte(`{"webrtc": {"proxy": {"listen": ":8190"}}, "streaming": {"webrtc": {"listen": "0.0.0.0:8190"}}}`))
	if err == nil || !strings.Contains(err.Error(), "webrtc.proxy listens on 0.0.0.0:8190 already") {
		t.Errorf("Load: %v, want the proxy's port taken", err)
	}
}

// TestAdminOffLoopbackNoToken checks an Admin API off loopback with TLS but
// no token is told to set the one thing it lacks.
func TestAdminOffLoopbackNoToken(t *testing.T) {
	_, err := Load([]byte(`{"listeners": [{"name": "web", "address": ":80"}], "admin": {"address": "10.0.0.1:2019", "cert_file": "c", "key_file": "k"}}`))
	if err == nil || !strings.Contains(err.Error(), "or set token_file: off loopback") {
		t.Errorf("Load: %v, want token_file named alone", err)
	}
}
