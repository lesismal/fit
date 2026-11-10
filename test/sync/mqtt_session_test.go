package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// mqttProxy forwards connections to a broker, and cuts them all at once:
// a connection lost, as the broker sees it.
type mqttProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newMQTTProxy(t *testing.T, broker string) *mqttProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &mqttProxy{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", broker)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, b)
			p.mu.Unlock()
			go func() { io.Copy(b, c); b.Close() }()
			go func() { io.Copy(c, b); c.Close() }()
		}
	}()
	t.Cleanup(func() { ln.Close(); p.cut() })
	return p
}

func (p *mqttProxy) addr() string { return p.ln.Addr().String() }

func (p *mqttProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

// TestMQTTWill: the sink's will reaches a subscriber once its connection
// is lost, with its payload and, in version 5, its properties; a sink
// closed by fit leaves none.
func TestMQTTWill(t *testing.T) {
	eachMQTT(t, func(t *testing.T, b mqttBroker, version string) {
		name := mqttName(t)
		ctx := hangGuard(t)
		sub := mqttDial(t, mqttwire.Config{Address: b.addr, Version: mqttVersion(version), ClientID: name + "-sub", CleanStart: true})
		if _, err := sub.Subscribe(ctx, mqttwire.Subscription{Filter: name + "/will/#", QoS: 1}); err != nil {
			t.Fatal(err)
		}
		proxy := newMQTTProxy(t, b.addr)
		will := map[string]any{"topic": name + "/will/lost", "payload": "fit is gone", "qos": 1}
		if version == "5" {
			will["content_type"], will["user_properties"] = "text/plain", map[string]string{"pipeline": "p"}
		}
		load := func(topic string) fitsync.Sink {
			will["topic"] = name + "/will/" + topic
			m, err := module.Load(fitsync.Sinks, mqttRef(t, map[string]any{"servers": []string{proxy.addr()}, "version": version,
				"topic": name + "/data", "will": will}), nil)
			if err != nil {
				t.Fatal(err)
			}
			snk := m.(fitsync.Sink)
			if err := snk.Write(ctx, []*fitsync.Record{{Op: fitsync.OpInsert, Namespace: "n", After: fitsync.Map(
				fitsync.Field{Name: "payload", Value: fitsync.String("x")})}}); err != nil {
				t.Fatal(err)
			}
			return snk
		}
		snk := load("lost")
		defer module.Cleanup(snk)
		proxy.cut()
		select {
		case m := <-sub.Messages():
			sub.Ack(m)
			if m.Topic != name+"/will/lost" || string(m.Payload) != "fit is gone" ||
				version == "5" && (m.ContentType != "text/plain" || len(m.UserProperties) != 1 || m.UserProperties[0].Value != "p") {
				t.Fatalf("%+v", m)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("no will")
		}
		module.Cleanup(load("closed"))
		select {
		case m := <-sub.Messages():
			t.Fatalf("a will of a sink closed: %+v", m)
		case <-time.After(time.Second):
		}
	})
}

// TestMQTTUnsubscribe: a topic filter dropped from the config between runs
// is unsubscribed from: its messages queued while fit was away, and those
// published after, are not written, and a third run, whose checkpoint
// names it no more, gets none of them either, as the session no longer
// holds it.
func TestMQTTUnsubscribe(t *testing.T) {
	eachMQTT(t, func(t *testing.T, b mqttBroker, version string) {
		name := mqttName(t)
		store := name
		fitsync.MemoryStore(store).Reset()
		dir := t.TempDir()
		pub := mqttDial(t, mqttwire.Config{Address: b.addr, Version: mqttVersion(version), ClientID: name + "-pub", CleanStart: true})
		send := func(topic, payload string) {
			t.Helper()
			if err := pub.Publish(hangGuard(t), &mqttwire.Message{Topic: name + "/" + topic, QoS: 1, Payload: []byte(payload)}); err != nil {
				t.Fatal(err)
			}
		}
		wait := func(payload string) {
			t.Helper()
			deadline := time.Now().Add(time.Minute)
			for payloads(store)[payload] == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("%s never written", payload)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		run := func(topics ...string) *fitsync.Runtime {
			params := map[string]any{"servers": []string{b.addr}, "client_id": name, "version": version, "topics": topics}
			return startSync(t, mqttSourceConfig(t, dir, store, params, nil), nil)
		}
		rt := run(name+"/a/#", name+"/b/#")
		mqttWarm(t, pub, name+"/a", store)
		send("b/1", "b-first")
		wait("b-first")
		rt.Close(context.Background())
		send("b/1", "b-away")
		send("a/1", "a-away")

		rt = run(name + "/a/#")
		wait("a-away")
		send("b/2", "b-after")
		send("a/2", "a-after")
		wait("a-after")
		rt.Close(context.Background())

		rt = run(name + "/a/#")
		defer rt.Close(context.Background())
		send("b/3", "b-third")
		send("a/3", "a-third")
		wait("a-third")
		time.Sleep(500 * time.Millisecond)
		for _, p := range []string{"b-away", "b-after", "b-third"} {
			if n := payloads(store)[p]; n != 0 {
				t.Fatalf("%s written %d times", p, n)
			}
		}
	})
}

// TestMQTTSCRAM: MQTT 5's enhanced authentication by SCRAM-SHA-256 with
// EMQX's SCRAM authenticator: the wire client connects, re-authenticates
// and publishes; a wrong password and another method are refused; the
// source reads with it. EMQX 5.8.6 takes a re-authentication's whole
// exchange, its last step judged ok in its log, and then sends no AUTH
// of success: its channel waits on kicking its own session, and the
// connection ends 5s later (session_stepdown_request_timeout). The test
// takes that as EMQX's, and logs it.
func TestMQTTSCRAM(t *testing.T) {
	addr := mqttEnv(t, "FIT_SYNC_MQTT_EMQX_SCRAM")
	user, password := mqttEnv(t, "FIT_SYNC_MQTT_USER"), mqttEnv(t, "FIT_SYNC_MQTT_PASSWORD")
	ctx := hangGuard(t)
	name := mqttName(t)
	auth, _ := mqttwire.NewSCRAM(mqttwire.SCRAMSHA256, user, password)
	cfg := mqttwire.Config{Address: addr, ClientID: name + "-pub", CleanStart: true, Username: user, Auth: auth}
	pub := mqttDial(t, cfg)
	if err := pub.Reauthenticate(ctx); err != nil {
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("re-authenticate: %v", err)
		}
		t.Logf("EMQX dropped the connection re-authenticated: %v", err)
		// A client ID of its own: EMQX refuses the old one for a while
		// (0x85).
		cfg.ClientID += "-again"
		pub = mqttDial(t, cfg)
	}
	if err := pub.Publish(ctx, &mqttwire.Message{Topic: name + "/x", QoS: 1}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ method, password string }{{mqttwire.SCRAMSHA256, "wrong"}, {mqttwire.SCRAMSHA512, password}} {
		bad, _ := mqttwire.NewSCRAM(a.method, user, a.password)
		_, err := mqttwire.Dial(ctx, mqttwire.Config{Address: addr, ClientID: name + "-bad", CleanStart: true, Auth: bad})
		var ce *mqttwire.ConnectError
		if !errors.As(err, &ce) || !ce.Auth() {
			t.Fatalf("%s %s: %v", a.method, a.password, err)
		}
		t.Logf("%s refused: %v", a.method, err)
	}
	if _, err := mqttwire.Dial(ctx, mqttwire.Config{Address: addr, ClientID: name + "-anon", CleanStart: true}); err == nil {
		t.Fatal("an anonymous client taken")
	}
	t.Setenv("FIT_TEST_SYNC_MQTT_PASSWORD", password)
	store := name
	fitsync.MemoryStore(store).Reset()
	params := map[string]any{"servers": []string{addr}, "client_id": name, "topics": []string{name + "/#"},
		"username": user, "password_env": "FIT_TEST_SYNC_MQTT_PASSWORD", "auth_method": "SCRAM-SHA-256"}
	rt := startSync(t, mqttSourceConfig(t, t.TempDir(), store, params, nil), nil)
	defer rt.Close(context.Background())
	mqttWarm(t, pub, name, store)
	mqttPublish(t, pub, name, 1, 0, 50)
	waitPayloads(t, store, 50)
}

// mqttRef is a module ref of params.
func mqttRef(t *testing.T, params map[string]any) config.ModuleRef {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	return config.ModuleRef{Module: "mqtt", Params: v}
}
