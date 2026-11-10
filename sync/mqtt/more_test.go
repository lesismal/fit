package syncmqtt

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// subscribed waits for c's SUBSCRIBE, and returns its filters.
func subscribed(t *testing.T, c *fakeClient) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		var fs []string
		for _, s := range c.subs {
			fs = append(fs, s.Filter)
		}
		c.mu.Unlock()
		if fs != nil {
			return fs
		}
		if time.Now().After(deadline) {
			t.Fatal("no SUBSCRIBE")
		}
		time.Sleep(time.Millisecond)
	}
}

func filtersOf(t *testing.T, p fitsync.Position) []string {
	t.Helper()
	var at position
	if err := json.Unmarshal(p, &at); err != nil {
		t.Fatal(err)
	}
	return at.Filters
}

// TestSourceUnsubscribes: the filters the checkpoint names and topics no
// longer does are unsubscribed from, in a session resumed; their messages
// queued before are acknowledged and not emitted, unless a filter still
// named matches them too. The positions carry the filters subscribed.
func TestSourceUnsubscribes(t *testing.T) {
	n := newFakeNet()
	n.present = true
	src := source(t, n, map[string]any{"topics": []string{"a/#", "x/#"}})
	from, _ := json.Marshal(position{Run: "old", Seq: 9, Filters: []string{"a/#", "c/+", "x/y"}})
	col := newCollect()
	r := col.run(false)
	r.From = from
	r.Start = func(fitsync.Position) error { t.Error("a start saved over a checkpoint"); return nil }
	cancel, done := started(src, r)
	c := n.last(t, 1)
	if fs := subscribed(t, c); !reflect.DeepEqual(fs, []string{"a/#", "x/#"}) {
		t.Fatal(fs)
	}
	if u := c.unsubscribed(); !reflect.DeepEqual(u, []string{"c/+", "x/y"}) {
		t.Fatal(u)
	}
	c.msgs <- &mqttwire.Message{Topic: "c/1", QoS: 1}
	c.msgs <- &mqttwire.Message{Topic: "x/y", QoS: 1}
	rec := col.next(t)
	if rec.Namespace != "x/y" || !reflect.DeepEqual(filtersOf(t, rec.Position), []string{"a/#", "x/#"}) {
		t.Fatalf("%s %s", rec.Namespace, rec.Position)
	}
	if a := waitAcks(t, c, 1); a[0].Topic != "c/1" {
		t.Fatal(a[0].Topic)
	}
	src.Acknowledged(rec.Position)
	waitAcks(t, c, 2)
	cancel()
	waitErr(t, done)

	// Not resumed: nothing to unsubscribe from.
	n.present = false
	_, done = started(src, r)
	c = n.last(t, 2)
	subscribed(t, c)
	c.msgs <- &mqttwire.Message{Topic: "c/1"}
	if rec := col.next(t); rec.Namespace != "c/1" || !reflect.DeepEqual(filtersOf(t, rec.Position), []string{"a/#", "x/#"}) {
		t.Fatal(rec.Namespace)
	}
	if len(c.unsubscribed()) != 0 {
		t.Fatal(c.unsubscribed())
	}
	c.lose(errors.New("gone"))
	waitErr(t, done)

	// A refused UNSUBSCRIBE fails the run.
	n.present, n.unsubErr = true, errors.New("not allowed")
	if err := src.Run(context.Background(), r); err == nil || err.Error() != "not allowed" {
		t.Fatal(err)
	}
}

// TestSourceSavesStart: a run without a checkpoint saves its filters as
// one at once; a start not saved fails the run.
func TestSourceSavesStart(t *testing.T) {
	n := newFakeNet()
	src := source(t, n, map[string]any{"topics": []string{"a", "b/#"}})
	col := newCollect()
	r := col.run(false)
	saved := make(chan fitsync.Position, 1)
	r.Start = func(p fitsync.Position) error { saved <- p; return nil }
	cancel, done := started(src, r)
	if fs := filtersOf(t, <-saved); !reflect.DeepEqual(fs, []string{"a", "b/#"}) {
		t.Fatal(fs)
	}
	cancel()
	waitErr(t, done)
	r.Start = func(fitsync.Position) error { return errors.New("disk full") }
	if err := src.Run(context.Background(), r); err == nil || err.Error() != "disk full" {
		t.Fatal(err)
	}
	if !n.last(t, 2).isClosed() {
		t.Fatal("not closed")
	}
}

// TestWillAndAuthParams: the will and enhanced authentication of both
// connectors, and what they refuse.
func TestWillAndAuthParams(t *testing.T) {
	t.Setenv("FIT_MQTT_TEST_PW", "secret")
	base := func(extra map[string]any) map[string]any {
		p := map[string]any{"servers": []string{"b:1883"}, "client_id": "c", "topics": []string{"a/#"}}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	long := string(make([]byte, 70000))
	for _, p := range []map[string]any{
		base(map[string]any{"will": map[string]any{"topic": "a/+"}}),
		base(map[string]any{"will": map[string]any{"topic": "a", "payload": long}}),
		base(map[string]any{"will": map[string]any{"topic": "a", "qos": 3}}),
		base(map[string]any{"version": "3.1.1", "will": map[string]any{"topic": "a", "delay": "1s"}}),
		base(map[string]any{"version": "3.1.1", "will": map[string]any{"topic": "a", "message_expiry": "1s"}}),
		base(map[string]any{"version": "3.1.1", "will": map[string]any{"topic": "a", "content_type": "c"}}),
		base(map[string]any{"version": "3.1.1", "will": map[string]any{"topic": "a", "user_properties": map[string]string{"k": "v"}}}),
		base(map[string]any{"auth_method": "SCRAM-MD5", "username": "u", "password_env": "FIT_MQTT_TEST_PW"}),
		base(map[string]any{"auth_method": "SCRAM-SHA-256", "username": "u"}),
		base(map[string]any{"auth_method": "SCRAM-SHA-256", "password_env": "FIT_MQTT_TEST_PW"}),
		base(map[string]any{"auth_method": "SCRAM-SHA-256", "username": "u", "password_env": "FIT_MQTT_TEST_PW", "version": "3.1.1"}),
	} {
		if _, err := load(t, fitsync.Sources, "mqtt", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	will := map[string]any{"topic": "fit/gone", "payload": "bye", "qos": 1, "retain": true, "delay": "1500ms", "message_expiry": "1m",
		"content_type": "text/plain", "user_properties": map[string]string{"b": "2", "a": "1"}}
	src := mustLoad(t, fitsync.Sources, "mqtt", base(map[string]any{"will": will, "auth_method": "SCRAM-SHA-512", "username": "u",
		"password_env": "FIT_MQTT_TEST_PW"})).(*Source)
	want := &mqttwire.Will{Message: mqttwire.Message{Topic: "fit/gone", Payload: []byte("bye"), QoS: 1, Retain: true, MessageExpiry: time.Minute,
		ContentType: "text/plain", UserProperties: []mqttwire.UserProperty{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}}, Delay: 1500 * time.Millisecond}
	c := src.cfg
	if !reflect.DeepEqual(c.Will, want) || c.Auth == nil || c.Auth.Method() != mqttwire.SCRAMSHA512 || c.Password != "" || c.Username != "u" {
		t.Fatalf("%+v", c)
	}
	snk := mustLoad(t, fitsync.Sinks, "mqtt", map[string]any{"servers": []string{"b:1883"}, "version": "3.1.1",
		"will": map[string]any{"topic": "fit/sink"}}).(*Sink)
	if snk.cfg.Will == nil || snk.cfg.Will.Topic != "fit/sink" || snk.cfg.Auth != nil {
		t.Fatalf("%+v", snk.cfg)
	}
	if _, err := load(t, fitsync.Sinks, "mqtt", map[string]any{"servers": []string{"b:1883"}, "auth_method": "SCRAM-SHA-1"}); err == nil {
		t.Fatal("SCRAM without a user loaded")
	}
}
