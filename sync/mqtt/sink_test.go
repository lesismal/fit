package syncmqtt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

func row(fields ...fitsync.Field) fitsync.Value { return fitsync.Map(fields...) }

func TestSinkProvision(t *testing.T) {
	for _, p := range []map[string]any{
		{"topic": "{other}"}, {"topic": "a/+"}, {"topic": "{namespace}/#"}, {"topic": ""},
		{"version": "3.1.1", "message_expiry": "1m"}, {"version": "3.1.1", "content_type": "x"},
		{"version": "3.1.1", "user_properties": map[string]string{"a": "b"}},
		{"codec": map[string]any{"module": "nope"}}, {"servers": []string{"x"}},
	} {
		if p["servers"] == nil {
			p["servers"] = []string{"b:1"}
		}
		if _, err := load(t, fitsync.Sinks, "mqtt", p); err == nil && p["topic"] != "" {
			t.Errorf("%v loaded", p)
		}
	}
	s := mustLoad(t, fitsync.Sinks, "mqtt", map[string]any{"servers": []string{"b:1"}, "user_properties": map[string]string{"b": "2", "a": "1"},
		"max_inflight": 5, "qos": 2, "topic": "x/{namespace}/{key}"}).(*Sink)
	if !strings.HasPrefix(s.cfg.ClientID, "fit-sync-") || !s.cfg.CleanStart || s.cfg.MaxInflight != 5 || s.qos != 2 ||
		len(s.ups) != 2 || s.ups[0].Key != "a" {
		t.Fatalf("%+v %+v", s.cfg, s.ups)
	}
	s = mustLoad(t, fitsync.Sinks, "mqtt", map[string]any{"servers": []string{"b:1"}, "client_id": "me"}).(*Sink)
	if s.cfg.ClientID != "me" || s.cfg.MaxInflight != DefaultMaxInflight || s.qos != 1 {
		t.Fatalf("%+v", s.cfg)
	}
}

// TestSinkWrite publishes a batch: topics of the template, payloads of
// the plain form or the codec, deletes retained as empty messages.
func TestSinkWrite(t *testing.T) {
	n := newFakeNet()
	s := sink(t, n, map[string]any{"topic": "out/{namespace}/{key}", "retain": true, "qos": 2, "message_expiry": "90s",
		"content_type": "application/octet-stream", "user_properties": map[string]string{"by": "fit"}})
	batch := []*fitsync.Record{
		{Namespace: "a", Key: fitsync.String("k1"), Op: fitsync.OpMessage, After: row(fitsync.Field{Name: "payload", Value: fitsync.Bytes([]byte{1, 2})})},
		{Namespace: "a", Key: fitsync.Int(7), Op: fitsync.OpInsert, After: row(fitsync.Field{Name: "payload", Value: fitsync.String("text")},
			fitsync.Field{Name: "content_type", Value: fitsync.String("ignored")},
			fitsync.Field{Name: "user_properties", Value: row(fitsync.Field{Name: "u", Value: fitsync.Array(fitsync.String("1"), fitsync.Int(2))},
				fitsync.Field{Name: "v", Value: fitsync.String("x")})})},
		{Namespace: "b", Op: fitsync.OpUpdate, After: row(fitsync.Field{Name: "payload", Value: fitsync.Int(42)})},
		{Namespace: "b", Op: fitsync.OpDelete, Key: fitsync.String("gone"), Before: row(fitsync.Field{Name: "payload", Value: fitsync.Int(1)})},
		{Namespace: "c", Op: fitsync.OpInsert, After: row(fitsync.Field{Name: "x", Value: fitsync.Int(1)})},
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	c := n.last(t, 1)
	got := c.published()
	want := []struct{ topic, payload string }{{"out/a/k1", "\x01\x02"}, {"out/a/7", "text"}, {"out/b/", "42"}, {"out/b/gone", ""},
		{"out/c/", `{"namespace":"c","op":"insert","after":{"map":{"x":{"int":1}}}}`}}
	for i, w := range want {
		m := got[i]
		if m.Topic != w.topic || string(m.Payload) != w.payload || m.QoS != 2 || !m.Retain || m.MessageExpiry != 90*time.Second ||
			m.ContentType != "application/octet-stream" {
			t.Fatalf("%d: %+v", i, m)
		}
	}
	if ups := got[1].UserProperties; len(ups) != 4 || ups[1] != (mqttwire.UserProperty{Key: "u", Value: "1"}) || ups[2].Value != "2" || ups[3].Key != "v" {
		t.Fatal(ups)
	}
	if len(s.ups) != 1 || len(got[0].UserProperties) != 1 {
		t.Fatal("the params' user properties changed")
	}
	// Not retained, a delete is encoded; the content type of the row is
	// taken when the params set none.
	s = sink(t, n, map[string]any{})
	s.Write(context.Background(), []*fitsync.Record{{Namespace: "d", Op: fitsync.OpDelete,
		Before: row(fitsync.Field{Name: "payload", Value: fitsync.String("old")}, fitsync.Field{Name: "content_type", Value: fitsync.String("text/plain")})}})
	if m := n.last(t, 2).published()[0]; m.Topic != "d" || string(m.Payload) != "old" || m.Retain || m.QoS != 1 || m.ContentType != "text/plain" {
		t.Fatalf("%+v", m)
	}
	// A codec makes the payload.
	s.codec = testCodec{}
	if err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "e"}}); err != nil {
		t.Fatal(err)
	}
	if m := n.last(t, 2).published()[1]; string(m.Payload) != "e" {
		t.Fatalf("%q", m.Payload)
	}
	err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "e"}, {Namespace: "fail"}})
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	s.Cleanup()
	s.Cleanup()
}

// TestSinkErrors: records that cannot be published, publishes refused,
// a connection lost and made again.
func TestSinkErrors(t *testing.T) {
	n := newFakeNet()
	s := sink(t, n, map[string]any{})
	ok := &fitsync.Record{Namespace: "ok", After: row(fitsync.Field{Name: "payload", Value: fitsync.String("p")})}
	check := func(batch []*fitsync.Record, index int, perm bool) {
		t.Helper()
		err := s.Write(context.Background(), batch)
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != index || fitsync.IsPermanent(err) != perm {
			t.Fatalf("%v", err)
		}
	}
	check([]*fitsync.Record{ok, {Namespace: "a/+"}}, 1, true)
	check([]*fitsync.Record{ok, ok, {Namespace: ""}}, 2, true)
	check([]*fitsync.Record{{Namespace: "j", After: row(fitsync.Field{Name: "x", Value: fitsync.JSON([]byte("{bad"))})}}, 0, true)
	n.sendErr = func(m *mqttwire.Message) error {
		switch m.Topic {
		case "big":
			return mqttwire.ErrPacketTooLarge
		case "busy":
			return errors.New("busy")
		}
		return nil
	}
	n.waitErr = func(m *mqttwire.Message) error {
		switch m.Topic {
		case "refused":
			return &mqttwire.ReasonError{Packet: "PUBACK", Code: 0x90}
		case "quota":
			return &mqttwire.ReasonError{Packet: "PUBACK", Code: 0x97}
		}
		return nil
	}
	check([]*fitsync.Record{ok, {Namespace: "big"}}, 1, true)
	check([]*fitsync.Record{ok, {Namespace: "busy"}}, 1, false)
	check([]*fitsync.Record{ok, {Namespace: "refused"}, ok}, 1, true)
	check([]*fitsync.Record{ok, {Namespace: "quota"}}, 1, false)
	// The first failure is the batch's: a wait before a record not made.
	check([]*fitsync.Record{ok, {Namespace: "quota"}, {Namespace: "+"}}, 1, false)
	if !permanent(mqttwire.ErrNotSupported) || permanent(errors.New("x")) {
		t.Fatal("permanent")
	}
	// A connection lost is made again at the next write.
	c := n.last(t, 1)
	c.lose(errors.New("gone"))
	if err := s.Write(context.Background(), []*fitsync.Record{ok}); err != nil {
		t.Fatal(err)
	}
	if !c.isClosed() || len(n.last(t, 2).published()) != 1 {
		t.Fatal("not made again")
	}
	n.last(t, 2).lose(errors.New("gone"))
	n.dialErr["b:1883"] = errors.New("refused")
	if err := s.Write(context.Background(), []*fitsync.Record{ok}); err == nil || err.Error() != "refused" {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err == nil {
		t.Fatal("tested")
	}
	delete(n.dialErr, "b:1883")
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := n.last(t, 3); !strings.HasPrefix(c.cfg.ClientID, "fit-test-") || !c.isClosed() {
		t.Fatal(c.cfg.ClientID)
	}
}
