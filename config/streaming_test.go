package config

import (
	"strings"
	"testing"
	"time"
)

func TestStreaming(t *testing.T) {
	good := `{"media_auth": [{"name": "live", "public": ["play"]}], "streaming": {
		"max_streams": 8, "rtmp": {"listen": ":1935", "auth": "live", "idle_timeout": "5s"},
		"rtsp": {"listen": ":8554", "auth_methods": ["digest", "basic"], "realm": "cams"},
		"srt": {"listen": ":8890", "passphrase": "0123456789", "latency": "200ms"},
		"webrtc": {"listen": ":8188", "ice_servers": [{"urls": ["stun:stun.example.com"]}]}}}`
	c, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Streaming
	if !c.HasApps() || s.Streams() != 8 || s.Players() != DefaultStreamMaxPlayers || s.GOPCache() != DefaultStreamGOPCacheBytes ||
		s.RTMP.Idle() != 5*time.Second || s.RTMP.Handshake() != DefaultRTMPHandshakeTimeout || s.RTMP.Conns() != DefaultStreamMaxConns ||
		s.RTMP.MessageBytes() != DefaultRTMPMaxMessageBytes || s.RTSP.RealmName() != "cams" || len(s.RTSP.Methods()) != 2 ||
		s.RTSP.Session() != DefaultRTSPSessionTimeout || s.RTSP.Idle() != DefaultRTSPIdleTimeout || s.RTSP.Conns() != DefaultStreamMaxConns ||
		s.SRT.LatencyOr() != 200*time.Millisecond || s.SRT.PeerIdle() != DefaultSRTPeerIdleTimeout || s.SRT.Conns() != DefaultStreamMaxConns ||
		s.WebRTC.Sessions() != DefaultStreamWebRTCMaxSessions || s.WebRTC.Connect() != DefaultStreamWebRTCConnectTimeout ||
		s.WebRTC.OfferBytes() != DefaultStreamWebRTCMaxOfferBytes {
		t.Fatalf("%+v", s)
	}
	if r := (&StreamRTSP{}); r.RealmName() != DefaultRTSPRealm || r.Methods()[0] != "digest" {
		t.Fatal("rtsp defaults")
	}
	doc := func(streaming string) string {
		return `{"media_auth": [{"name": "live", "public": ["play"]}], "webrtc": {"media": {"listen": ":8189"}}, "streaming": ` + streaming + `}`
	}
	for _, bad := range []struct{ doc, want string }{
		{doc(`{}`), "nothing takes streams in"},
		{doc(`{"rtmp": {"listen": ":1935", "auth": "nope"}}`), `no media_auth policy named "nope"`},
		{doc(`{"rtsp": {"listen": ":8554", "auth": "nope"}}`), `no media_auth policy named "nope"`},
		{doc(`{"srt": {"listen": ":8890", "auth": "nope"}}`), `no media_auth policy named "nope"`},
		{doc(`{"rtmp": {"listen": "1935"}}`), "not a host and port"},
		{doc(`{"rtmp": {"listen": ":1935"}, "rtsp": {"listen": ":1935"}}`), "rtmp listens on :1935 already"},
		{doc(`{"srt": {"listen": ":9000"}, "webrtc": {"listen": ":9000"}}`), "srt listens on :9000 already"},
		{doc(`{"webrtc": {"listen": "0.0.0.0:8189"}}`), "webrtc.media listens on 0.0.0.0:8189 already"},
		{doc(`{"rtsp": {"listen": ":8554", "auth_methods": ["digest", "digest"]}}`), "listed twice"},
		{doc(`{"rtsp": {"listen": ":8554", "auth_methods": ["ntlm"]}}`), "ntlm"},
		{doc(`{"rtsp": {"listen": ":8554", "realm": "a\"b"}}`), "double quote"},
		{doc(`{"srt": {"listen": ":8890", "passphrase": "short"}}`), "10 to 79"},
		{doc(`{"webrtc": {"listen": ":8188", "public_ips": ["nope"]}}`), "nope"},
		{doc(`{"webrtc": {"listen": ":8188", "ice_servers": [{"urls": ["http://x"]}]}}`), "not a stun:"},
		{doc(`{"webrtc": {"listen": ":8188", "max_offer_bytes": 100000}}`), "65536"},
		{doc(`{"rtmp": {}}`), "listen"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	if !samePort(":1", "127.0.0.1:1") || samePort(":0", ":0") || samePort("a:1", "b:1") || samePort("x", ":1") {
		t.Fatal("samePort")
	}
}
