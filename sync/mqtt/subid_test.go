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

// subscriptions waits for n subscriptions of c, and returns them.
func subscriptions(t *testing.T, c *fakeClient, n int) []mqttwire.Subscription {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		subs := append([]mqttwire.Subscription(nil), c.subs...)
		c.mu.Unlock()
		if len(subs) >= n {
			return subs
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d subscriptions", len(subs))
		}
		time.Sleep(time.Millisecond)
	}
}

// positionOf is p, its run left out.
func positionOf(t *testing.T, p fitsync.Position) position {
	t.Helper()
	var at position
	if err := json.Unmarshal(p, &at); err != nil {
		t.Fatal(err)
	}
	at.Run = ""
	return at
}

// TestSourceSubscriptionIDs: in version 5, each filter of topics is
// subscribed with an identifier, the checkpoint's or a new one, and a
// message is acknowledged and not emitted when the identifiers it carries
// are all of filters dropped, whatever its topic. A message without
// identifiers, or of one fit did not give, is told by its topic, as is
// every message when the broker takes no identifiers.
func TestSourceSubscriptionIDs(t *testing.T) {
	n := newFakeNet()
	n.present = true
	src := source(t, n, map[string]any{"topics": []string{"a/#", "x/#"}})
	from, _ := json.Marshal(position{Run: "old", Seq: 9, Filters: []string{"a/#", "c/+", "x/y"}, IDs: map[string]int{"a/#": 3, "c/+": 4},
		NextID: 5})
	col := newCollect()
	r := col.run(false)
	r.From = from
	positions := make(chan fitsync.Position, 1)
	r.Positions = func(pos func() fitsync.Position) error {
		select {
		case positions <- pos():
		default:
		}
		return nil
	}
	cancel, done := started(src, r)
	c := n.last(t, 1)
	subs := subscriptions(t, c, 2)
	if !reflect.DeepEqual(subs, []mqttwire.Subscription{{Filter: "a/#", QoS: 1, RetainHandling: 1, ID: 3}, {Filter: "x/#", QoS: 1, RetainHandling: 1, ID: 5}}) {
		t.Fatalf("%+v", subs)
	}
	if u := c.unsubscribed(); !reflect.DeepEqual(u, []string{"c/+", "x/y"}) {
		t.Fatal(u)
	}
	// What the checkpoint does not hold is offered as a position-only
	// record: the dropped filters gone with their UNSUBACK.
	want := position{Filters: []string{"a/#", "x/#"}, IDs: map[string]int{"a/#": 3, "x/#": 5}, NextID: 6}
	if p := positionOf(t, <-positions); !reflect.DeepEqual(p, want) {
		t.Fatalf("%+v", p)
	}
	for _, m := range []*mqttwire.Message{
		{Topic: "c/1", QoS: 1, SubscriptionIDs: []int{4}},    // a dropped filter's
		{Topic: "a/1", QoS: 1, SubscriptionIDs: []int{4, 3}}, // a/#'s too
		{Topic: "q/1", QoS: 1, SubscriptionIDs: []int{5}},    // under a topic rewritten
		{Topic: "a/2", QoS: 1, SubscriptionIDs: []int{2}},    // given before, dropped since
		{Topic: "x/y", QoS: 1},                               // x/#'s too
		{Topic: "c/2", QoS: 1},                               // none: c/+'s alone
		{Topic: "c/3", QoS: 1, SubscriptionIDs: []int{9}},    // not fit's: c/+'s alone
		{Topic: "q/2", QoS: 1, SubscriptionIDs: []int{9}},    // not fit's, no filter dropped's
	} {
		c.msgs <- m
	}
	var last *fitsync.Record
	for _, ns := range []string{"a/1", "q/1", "x/y", "q/2"} {
		last = col.next(t)
		if last.Namespace != ns {
			t.Fatalf("%s, not %s", last.Namespace, ns)
		}
	}
	want.Seq = 8
	if p := positionOf(t, last.Position); !reflect.DeepEqual(p, want) {
		t.Fatalf("%+v", p)
	}
	src.Acknowledged(last.Position)
	waitAcks(t, c, 8)
	cancel()
	waitErr(t, done)

	// A broker that takes no identifiers: one SUBSCRIBE without them,
	// every message told by its topic, and the identifiers left out of
	// the positions, though none given is given again.
	n.mu.Lock()
	n.noSubIDs = true
	n.mu.Unlock()
	cancel, done = started(src, r)
	c = n.last(t, 2)
	subs = subscriptions(t, c, 2)
	if !reflect.DeepEqual(subs, []mqttwire.Subscription{{Filter: "a/#", QoS: 1, RetainHandling: 1}, {Filter: "x/#", QoS: 1, RetainHandling: 1}}) {
		t.Fatalf("%+v", subs)
	}
	c.msgs <- &mqttwire.Message{Topic: "c/1", QoS: 1, SubscriptionIDs: []int{3}}
	c.msgs <- &mqttwire.Message{Topic: "a/1", QoS: 1, SubscriptionIDs: []int{4}}
	last = col.next(t)
	if p := positionOf(t, last.Position); last.Namespace != "a/1" || !reflect.DeepEqual(p, position{Seq: 2, Filters: []string{"a/#", "x/#"}, NextID: 5}) {
		t.Fatalf("%s %+v", last.Namespace, p)
	}
	waitAcks(t, c, 1)
	cancel()
	waitErr(t, done)

	// A subscription refused fails the run.
	n.mu.Lock()
	n.noSubIDs, n.subErr = false, errors.New("not granted")
	n.mu.Unlock()
	if err := src.Run(context.Background(), r); err == nil || err.Error() != "not granted" {
		t.Fatal(err)
	}
}
