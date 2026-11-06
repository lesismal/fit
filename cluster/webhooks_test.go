package cluster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWebhookFormats: every format's body, the chat services' signatures,
// fit's own signature, a template of the alerts' lines, and a custom body.
func TestWebhookFormats(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	sink := newHookSink(t)
	var hooks []Webhook
	for _, f := range WebhookFormats {
		w := sink.hook(f, f)
		w.Secret = "s3cr3t"
		switch f {
		case FormatCustom:
			w.Template = `{"msg":"${text}","st":"${status}","n":${count},"by":"${manager}","list":${alerts}}`
		case FormatSlack:
			w.Template = "${severity}/${rule}/${kind}/${node}/${subject}/${on}/${status}/${id}/${since}: ${summary}"
		case FormatDingTalk:
			w.URL = sink.srv.URL + "/hook/dingtalk"
		}
		hooks = append(hooks, w)
	}
	plain := sink.hook("plain", FormatJSON)
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown}}, append(hooks, plain)...)
	now := clock.now()
	var states []State
	for i := 0; i < maxNotifyAlerts+5; i++ {
		states = append(states, down("m"+strconv.Itoa(i), now))
	}
	eval(m, now, &evalInput{states: states})
	got := sink.wait(t, len(WebhookFormats)+1)
	by := map[string]hookReq{}
	for _, r := range got {
		by[strings.TrimPrefix(r.Path, "/hook/")] = r
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := func(body []byte) string {
		mac := hmac.New(sha256.New, []byte("s3cr3t"))
		mac.Write([]byte(ts + "."))
		mac.Write(body)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	// fit's own JSON: bounded, signed.
	j := by[FormatJSON]
	p := j.payload(t)
	if len(p.Alerts) != maxNotifyAlerts || p.More != 5 || p.Source != "fit-cluster" || !strings.Contains(p.Text, "… and 35 more") {
		t.Fatalf("json: %d alerts, more %d, %q", len(p.Alerts), p.More, p.Text)
	}
	if j.Header.Get(HeaderTimestamp) != ts || j.Header.Get(HeaderSignature) != sig(j.Body) {
		t.Fatalf("signature: %v", j.Header)
	}
	if by["plain"].Header.Get(HeaderSignature) != "" || by["plain"].Query != "token=sekret-token" {
		t.Fatalf("plain: %v %q", by["plain"].Header, by["plain"].Query)
	}
	var slack map[string]string
	_ = json.Unmarshal(by[FormatSlack].Body, &slack)
	if !strings.Contains(slack["text"], "\ncritical/down/member_down/m0//m0/firing/") || by[FormatSlack].Header.Get(HeaderSignature) != sig(by[FormatSlack].Body) {
		t.Fatalf("slack: %s", by[FormatSlack].Body)
	}
	var discord map[string]string
	_ = json.Unmarshal(by[FormatDiscord].Body, &discord)
	if !strings.HasPrefix(discord["content"], "[FIRING] fit cluster on ") || len(discord["content"]) > 1910 {
		t.Fatalf("discord: %d bytes", len(discord["content"]))
	}
	var wecom struct {
		MsgType string `json:"msgtype"`
		Text    struct{ Content string }
	}
	_ = json.Unmarshal(by[FormatWeCom].Body, &wecom)
	if wecom.MsgType != "text" || !strings.Contains(wecom.Text.Content, "• [critical] down · m0: down since") {
		t.Fatalf("wecom: %s", by[FormatWeCom].Body)
	}
	// Feishu's signature is in its body.
	var feishu struct {
		MsgType   string `json:"msg_type"`
		Timestamp string
		Sign      string
		Content   struct{ Text string }
	}
	_ = json.Unmarshal(by[FormatFeishu].Body, &feishu)
	mac := hmac.New(sha256.New, []byte(ts+"\ns3cr3t"))
	if feishu.MsgType != "text" || feishu.Timestamp != ts || feishu.Sign != base64.StdEncoding.EncodeToString(mac.Sum(nil)) || feishu.Content.Text == "" {
		t.Fatalf("feishu: %s", by[FormatFeishu].Body)
	}
	if by[FormatFeishu].Header.Get(HeaderSignature) != "" {
		t.Fatal("feishu signed twice")
	}
	// DingTalk's in its URL.
	q, _ := url.ParseQuery(by[FormatDingTalk].Query)
	ms := strconv.FormatInt(now.UnixMilli(), 10)
	mac = hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write([]byte(ms + "\ns3cr3t"))
	if q.Get("timestamp") != ms || q.Get("sign") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("dingtalk: %s", by[FormatDingTalk].Query)
	}
	// The custom body.
	var custom struct {
		Msg, St, By string
		N           int
		List        []Alert
	}
	if err := json.Unmarshal(by[FormatCustom].Body, &custom); err != nil || custom.N != maxNotifyAlerts+5 || custom.St != AlertFiring ||
		custom.By != "mgr-1" || len(custom.List) != maxNotifyAlerts || !strings.Contains(custom.Msg, "[FIRING]") {
		t.Fatalf("custom: %s %v", by[FormatCustom].Body, err)
	}

	// A DingTalk URL with a query of its own gets the signature after it.
	s := &sender{m: m, hook: Webhook{Name: "d", URL: "https://oapi.example/robot/send?access_token=x", Format: FormatDingTalk, Secret: "k"}}
	target, _, _ := s.render(&notice{status: AlertFiring}, now)
	if !strings.HasPrefix(target, "https://oapi.example/robot/send?access_token=x&timestamp=") {
		t.Fatal(target)
	}
	// One alert is "alert", a test says so.
	if txt := noticeText(&Webhook{Name: "w", Format: FormatJSON}, &notice{status: AlertResolved, alerts: []Alert{{Rule: "r", Subject: "ro1"}}}, "h"); !strings.HasSuffix(strings.Split(txt, "\n")[0], ": 1 alert") || !strings.Contains(txt, "· ro1:") {
		t.Fatal(txt)
	}
	if txt := noticeText(&Webhook{Name: "w"}, &notice{test: true}, "h"); txt != "[TEST] fit cluster on h: a test of the webhook w" {
		t.Fatal(txt)
	}
}

// TestChatRefusalsAndRedaction: a chat service's 200 with an error code
// fails the notification; a webhook's URL and secret never reach its
// status or the log.
func TestChatRefusalsAndRedaction(t *testing.T) {
	for _, c := range []struct {
		format, body, want string
	}{
		{FormatDingTalk, `{"errcode":310000,"errmsg":"sign not match"}`, "310000 sign not match"},
		{FormatWeCom, `{"errcode":0,"errmsg":"ok"}`, ""},
		{FormatFeishu, `{"code":19021,"msg":"sign match fail"}`, "19021 sign match fail"},
		{FormatFeishu, `{"code":0}`, ""},
		{FormatSlack, `{"errcode":1}`, ""},
		{FormatFeishu, `ok`, ""},
	} {
		if got := chatRefusal(c.format, []byte(c.body)); got != c.want {
			t.Errorf("%s %s: %q", c.format, c.body, got)
		}
	}
	raw := "https://hooks.example/services/T0/B0/xyz?key=abc"
	msg := redactURL(`Post "`+raw+`": refused; path /services/T0/B0/xyz?key=abc; key=abc`, raw)
	if strings.Contains(msg, "xyz") || strings.Contains(msg, "abc") || !strings.Contains(msg, "https://hooks.example/…") {
		t.Fatal(msg)
	}
	if redactURL("x", "") != "x" || urlHost("%zz") != "" {
		t.Fatal("redact")
	}

	clock := newClock()
	m, _, log := bareManager(t, clock.now)
	sink := newHookSink(t)
	sink.set(func(h *hookSink) { h.answer = `{"errcode":300001,"errmsg":"token s3cr3t-value is not valid"}` })
	w := sink.hook("dt", FormatDingTalk)
	w.Secret = "s3cr3t-value"
	bad := sink.hook("bad", FormatJSON)
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown}}, w, bad)
	eval(m, clock.now(), &evalInput{states: []State{down("a", clock.now())}})
	st := waitSent(t, m, "dt", 1)
	if st.Failed != 1 || !strings.Contains(st.LastError, "300001") || strings.Contains(st.LastError, "s3cr3t-value") || st.LastStatus != 200 {
		t.Fatalf("%+v", st)
	}
	waitSent(t, m, "bad", 1)
	sink.set(func(h *hookSink) { h.status, h.answer = 500, "no "+w.URL })
	m.al.mu.Lock()
	m.al.active[alertKey("down", "a", "")].Sends = 0
	m.al.mu.Unlock()
	eval(m, clock.add(time.Second), &evalInput{states: []State{down("a", clock.now())}})
	st = waitSent(t, m, "bad", 2)
	if st.Failed != 1 || st.LastStatus != 500 || !strings.Contains(st.LastError, "the webhook answered 500") || strings.Contains(st.LastError, "sekret-token") {
		t.Fatalf("%+v", st)
	}
	if strings.Contains(log.String(), "sekret-token") || strings.Contains(log.String(), "s3cr3t-value") {
		t.Fatal(log.String())
	}
}

// TestWebhookQueue: a webhook that does not answer holds up only its own
// queue, bounded, the oldest dropped; it times out; a closed webhook drops
// what waits; an unchanged webhook keeps its sender, a changed one gets a
// new one.
func TestWebhookQueue(t *testing.T) {
	clock := newClock()
	m, _, log := bareManager(t, clock.now)
	slow, fast := newHookSink(t), newHookSink(t)
	slow.set(func(h *hookSink) { h.hold = make(chan struct{}) })
	w := slow.hook("slow", FormatJSON)
	w.TimeoutMs = 1000
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown, RepeatMs: 60_000, Webhooks: []string{"fast"}}}, w, fast.hook("fast", FormatJSON))
	s := senderOf(m, "slow")
	results := make(chan WebhookResult, MaxWebhookQueue+4)
	start := time.Now()
	for i := 0; i < MaxWebhookQueue+3; i++ {
		s.enqueue(&notice{status: AlertFiring, done: func(r WebhookResult) { results <- r }})
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("queueing waited for the webhook")
	}
	// The evaluator's own sends go on to the other webhook meanwhile.
	eval(m, clock.now(), &evalInput{states: []State{down("a", clock.now())}})
	fast.wait(t, 1)
	st := s.status()
	// One under way, MaxWebhookQueue waiting, the two oldest dropped.
	if st.Dropped != 2 || st.Queued != MaxWebhookQueue+1 {
		t.Fatalf("%+v", st)
	}
	for i := 0; i < 2; i++ {
		if r := <-results; !strings.Contains(r.Error, "dropped") {
			t.Fatalf("%+v", r)
		}
	}
	// The one under way times out.
	r := <-results
	if r.OK || r.Error == "" || r.TookMs < 900 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(log.String(), "the oldest was dropped") {
		t.Fatal(log.String())
	}
	// Unchanged: the same sender; changed: a new one, the old closed,
	// what waited dropped.
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown}}, w, fast.hook("fast", FormatJSON))
	if senderOf(m, "slow") != s {
		t.Fatal("an unchanged webhook lost its sender")
	}
	w.TimeoutMs = 2000
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown}}, w)
	if senderOf(m, "slow") == s || senderOf(m, "fast") != nil {
		t.Fatal("a changed webhook kept its sender")
	}
	waitFor(t, "the queue dropped", func() bool { return len(results) >= MaxWebhookQueue })
	s.enqueue(&notice{status: AlertFiring, done: func(r WebhookResult) { results <- r }})
	for len(results) > 0 {
		if r := <-results; r.OK || !strings.Contains(r.Error, "changed or removed") && !strings.Contains(r.Error, "cancel") && r.Error == "" {
			t.Fatalf("%+v", r)
		}
	}
	slow.release()
	// A request that cannot be made fails at once.
	bad := &sender{m: m, hook: Webhook{Name: "x", URL: "http://[::1]:namedport/", Format: FormatJSON}, client: senderOf(m, "slow").client}
	done := make(chan WebhookResult, 1)
	bad.enqueue(&notice{status: AlertFiring, done: func(r WebhookResult) { done <- r }})
	if r := <-done; r.OK || r.Error == "" {
		t.Fatalf("%+v", r)
	}
}

// TestTestWebhook sends a test, and reports a webhook there is none of.
func TestTestWebhook(t *testing.T) {
	clock := newClock()
	m, st, _ := bareManager(t, clock.now)
	m.readOnly = "the store is read-only"
	sink := newHookSink(t)
	setRules(m, nil, sink.hook("ops", FormatJSON))
	done := make(chan WebhookResult, 1)
	if err := m.TestWebhook("ann", "ops", func(r WebhookResult) { done <- r }); err != nil {
		t.Fatal(err)
	}
	if r := <-done; !r.OK || r.Status != 200 {
		t.Fatalf("%+v", r)
	}
	p := sink.wait(t, 1)[0].payload(t)
	if !p.Test || p.Status != "test" || !strings.Contains(p.Alerts[0].Summary, "sent by ann") || !strings.Contains(p.Text, "\n• [info] test: a test") {
		t.Fatalf("%+v", p)
	}
	if err := m.TestWebhook("ann", "none", nil); err == nil {
		t.Fatal("a webhook there is none of was tested")
	}
	_ = st
}
