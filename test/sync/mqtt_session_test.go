package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
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
		send("b/3", "b-third")
		send("a/3", "a-third")
		wait("a-third")
		rt.Close(context.Background())

		// A filter added in a run that gets no message, then dropped: the
		// position-only records of the two runs save each filter set, so
		// the last run's session no longer holds it.
		quiet := func(topics ...string) {
			rt := run(topics...)
			time.Sleep(time.Second)
			rt.Close(context.Background())
		}
		quiet(name+"/a/#", name+"/c/#")
		quiet(name + "/a/#")
		rt = run(name + "/a/#")
		send("c/1", "c-last")
		send("a/4", "a-last")
		wait("a-last")
		rt.Close(context.Background())

		// A message queued for a filter since dropped, under a topic a
		// filter added in its place matches: in version 5 it carries the
		// dropped filter's subscription identifier alone, and is not
		// written; in 3.1.1, told by its topic, it is.
		rt = run(name+"/a/#", name+"/d/#")
		for deadline := time.Now().Add(time.Minute); payloads(store)["d-first"] == 0; time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("d/# never subscribed")
			}
			send("d/0", "d-first")
		}
		rt.Close(context.Background())
		send("d/1", "d-away")
		rt = run(name+"/a/#", name+"/+/1")
		defer rt.Close(context.Background())
		send("a/5", "a-fifth")
		wait("a-fifth")
		time.Sleep(500 * time.Millisecond)
		want := map[string]int{"b-away": 0, "b-after": 0, "b-third": 0, "c-last": 0, "d-away": 0}
		if version == "3.1.1" {
			want["d-away"] = 1
		}
		for p, n := range want {
			if got := payloads(store)[p]; got != n {
				t.Fatalf("%s written %d times, not %d", p, got, n)
			}
		}
	})
}

// TestMQTTSubscriptionIDs: both brokers put the identifiers of the
// subscriptions a message matches on it, on those the session kept while
// its client was away too, as the source's version 5 needs. Both send a
// message that two subscriptions match twice, each copy with one
// subscription's identifier, as MQTT 5 lets them (3.3.4): the copy of a
// filter dropped is not emitted.
func TestMQTTSubscriptionIDs(t *testing.T) {
	eachMQTT(t, func(t *testing.T, b mqttBroker, version string) {
		if version != "5" {
			t.Skip("version 5's")
		}
		ctx := hangGuard(t)
		name := mqttName(t)
		cfg := mqttwire.Config{Address: b.addr, ClientID: name + "-sub", SessionExpiry: time.Minute}
		sub := mqttDial(t, cfg)
		if !sub.SubscriptionIDsAvailable() {
			t.Fatal("no subscription identifiers")
		}
		for _, s := range []mqttwire.Subscription{{Filter: name + "/a/#", QoS: 1, ID: 7}, {Filter: name + "/a/+", QoS: 1, ID: 9},
			{Filter: name + "/b", QoS: 1}} {
			if _, err := sub.Subscribe(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		pub := mqttDial(t, mqttwire.Config{Address: b.addr, ClientID: name + "-pub", CleanStart: true})
		// next takes the copies of a message, one per identifier.
		next := func(c *mqttwire.Client, topic string, ids ...int) {
			t.Helper()
			var got []int
			for len(got) < max(len(ids), 1) {
				select {
				case m := <-c.Messages():
					c.Ack(m)
					if m.Topic != name+"/"+topic || len(m.SubscriptionIDs) > 1 {
						t.Fatalf("%s %v, not %s %v", m.Topic, m.SubscriptionIDs, topic, ids)
					}
					got = append(got, m.SubscriptionIDs...)
					if len(ids) == 0 {
						got = append(got, 0)
					}
				case <-time.After(30 * time.Second):
					t.Fatalf("no %s", topic)
				}
			}
			if slices.Sort(got); len(ids) > 0 && !slices.Equal(got, ids) {
				t.Fatalf("%s %v, not %v", topic, got, ids)
			}
		}
		for _, topic := range []string{"a/1", "b"} {
			if err := pub.Publish(ctx, &mqttwire.Message{Topic: name + "/" + topic, QoS: 1}); err != nil {
				t.Fatal(err)
			}
		}
		next(sub, "a/1", 7, 9)
		next(sub, "b")
		sub.Close()
		if err := pub.Publish(ctx, &mqttwire.Message{Topic: name + "/a/2", QoS: 1}); err != nil {
			t.Fatal(err)
		}
		next(mqttDial(t, cfg), "a/2", 7, 9)
	})
}

// TestMQTTRewrite: EMQX's rewrite rule (test/sync/mqtt.sh) moves the
// filters subscribed and the topics published under fit-rewrite/ to
// fit-rewritten/, so the source's messages come under topics its filter
// does not match, and match a filter dropped from topics: in version 5,
// their subscription identifiers say they are the filter's of topics, and
// they are written, both those queued before the UNSUBACK and those after.
func TestMQTTRewrite(t *testing.T) {
	addr := mqttEnv(t, "FIT_SYNC_MQTT_EMQX")
	ctx := hangGuard(t)
	name := mqttName(t)
	from, to := "fit-rewrite/"+name, "fit-rewritten/"+name
	pub := mqttDial(t, mqttwire.Config{Address: addr, ClientID: name + "-pub", CleanStart: true})
	sub := mqttDial(t, mqttwire.Config{Address: addr, ClientID: name + "-sub", CleanStart: true})
	if _, err := sub.Subscribe(ctx, mqttwire.Subscription{Filter: from + "/#", QoS: 1, ID: 42}); err != nil {
		t.Fatal(err)
	}
	if err := pub.Publish(ctx, &mqttwire.Message{Topic: from + "/x", QoS: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-sub.Messages():
		if m.Topic != to+"/x" || !slices.Equal(m.SubscriptionIDs, []int{42}) {
			t.Fatalf("%s %v: not rewritten", m.Topic, m.SubscriptionIDs)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no message")
	}

	store := name
	fitsync.MemoryStore(store).Reset()
	dir := t.TempDir()
	run := func(topics ...string) *fitsync.Runtime {
		params := map[string]any{"servers": []string{addr}, "client_id": name, "topics": topics}
		return startSync(t, mqttSourceConfig(t, dir, store, params, nil), nil)
	}
	send := func(topic, payload string) {
		t.Helper()
		if err := pub.Publish(ctx, &mqttwire.Message{Topic: topic, QoS: 1, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	rt := run(from+"/#", to+"/+")
	mqttWarm(t, pub, from, store)
	rt.Close(context.Background())
	send(from+"/1", "away")
	rt = run(from + "/#")
	defer rt.Close(context.Background())
	send(from+"/2", "after")
	deadline := time.Now().Add(time.Minute)
	for got := payloads(store); got["away"] != 1 || got["after"] != 1; got = payloads(store) {
		if time.Now().After(deadline) {
			t.Fatalf("written: %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
