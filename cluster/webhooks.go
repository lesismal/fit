package cluster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// How alerts are sent: each webhook has a queue of its own, bounded by
// MaxWebhookQueue, the oldest notification dropped past it, and sends one
// notification at a time on fib's asynchronous client, bounded by the
// webhook's timeout; the next goes from the callback of the one before. No
// one waits for a webhook: the evaluator only queues, and a webhook that
// does not answer holds up nothing but its own queue. A notification is
// not sent again when it fails: a firing alert is sent again at its repeat
// interval, and the webhook's status says what went wrong.

// The formats of a webhook, Webhook.Format.
const (
	// FormatJSON is fit's own JSON: the alerts, their states and times,
	// and a text; signed with X-Fit-Signature when the webhook has a
	// secret.
	FormatJSON = "json"
	// FormatSlack is {"text"}: Slack's incoming webhooks, and Mattermost's
	// and Rocket.Chat's, which take the same.
	FormatSlack = "slack"
	// FormatDiscord is {"content"}.
	FormatDiscord = "discord"
	// FormatFeishu is Feishu's (Lark's) bot: a text message, signed by
	// its secret in the body.
	FormatFeishu = "feishu"
	// FormatDingTalk is DingTalk's robot: a text message, signed by its
	// secret in the URL's query.
	FormatDingTalk = "dingtalk"
	// FormatWeCom is WeCom's (WeChat Work's) group robot: a text message.
	FormatWeCom = "wecom"
	// FormatCustom is a body of the webhook's own: Template, JSON with
	// ${text}, ${status}, ${count}, ${manager} and ${alerts}.
	FormatCustom = "custom"
)

// WebhookFormats are the formats, in the order the pages offer them.
var WebhookFormats = []string{FormatJSON, FormatSlack, FormatDiscord, FormatFeishu, FormatDingTalk, FormatWeCom, FormatCustom}

// The bounds of a notification.
const (
	// maxNotifyAlerts bounds the alerts a notification's JSON lists, and
	// maxTextAlerts those its text does: the rest are counted.
	maxNotifyAlerts = 50
	maxTextAlerts   = 20
	// maxWebhookAnswer bounds a webhook's answer.
	maxWebhookAnswer = 64 << 10
)

// The headers a signed notification carries.
const (
	HeaderSignature = "X-Fit-Signature"
	HeaderTimestamp = "X-Fit-Timestamp"
)

// webhookURL checks a webhook's URL: https, or http to a loopback address,
// without a user or a fragment; its query may hold a chat service's token.
// The URL is never in the error, as it may hold one.
func webhookURL(raw, format string) error {
	if raw == "" || len(raw) > MaxURLBytes {
		return fieldErr("url", "the URL is 1 to %d bytes", MaxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return fieldErr("url", "the URL is not an http or https URL without a user or a fragment")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopback(u.Hostname()):
	case u.Scheme == "http":
		return fieldErr("url", "the URL is http to a host that is not a loopback address: use https, so that the alerts and the URL's token do not cross a network in the clear")
	default:
		return fieldErr("url", "the URL is not an http or https URL")
	}
	return nil
}

// urlHost is a URL's scheme and host, which is all of it the API answers.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// redactURL takes a webhook's URL out of msg, but for its scheme and host.
func redactURL(msg, raw string) string {
	if raw == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, raw, urlHost(raw)+"/…")
	if u, err := url.Parse(raw); err == nil {
		if rest := strings.TrimPrefix(raw, urlHost(raw)); len(rest) > 1 {
			msg = strings.ReplaceAll(msg, rest, "/…")
		}
		if u.RawQuery != "" {
			msg = strings.ReplaceAll(msg, u.RawQuery, "…")
		}
	}
	return msg
}

// notice is a notification for a webhook: alerts that fired (or are sent
// again), alerts that resolved, or a test; done, when not nil, is told how
// it went.
type notice struct {
	status string
	alerts []Alert
	test   bool
	done   func(WebhookResult)
}

func (n *notice) finish(res WebhookResult) {
	if n.done != nil {
		n.done(res)
	}
}

// WebhookResult is how a notification went.
type WebhookResult struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	TookMs int64  `json:"took_ms"`
}

// WebhookStatus is how a webhook's notifications went since the manager
// started.
type WebhookStatus struct {
	Sent       uint64    `json:"sent"`
	Failed     uint64    `json:"failed"`
	Dropped    uint64    `json:"dropped"`
	Queued     int       `json:"queued"`
	LastAt     time.Time `json:"last_at,omitzero"`
	LastOK     time.Time `json:"last_ok,omitzero"`
	LastStatus int       `json:"last_status,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	LastTookMs int64     `json:"last_took_ms,omitempty"`
}

// sender is a webhook's queue and connections.
type sender struct {
	m      *Manager
	hook   Webhook
	client *fibhttp.Client

	mu     sync.Mutex
	queue  []*notice
	busy   bool
	closed bool
	st     WebhookStatus
}

func (m *Manager) newSender(w Webhook) *sender {
	t := w.timeout()
	cc := fibhttp.DefaultClientConfig()
	cc.Timeout, cc.ResponseHeaderTimeout, cc.DialTimeout = t, t, min(t, maxDialTimeout)
	cc.MaxConnsPerHost, cc.MaxPendingPerHost, cc.MaxIdleConnsPerHost = 1, 2, 1
	cc.IdleConnTimeout = idleConnTimeout
	cc.MaxResponseHeaderBytes = maxHeaderBytes
	cc.MaxResponseBodyBytes = maxWebhookAnswer
	cc.ExpectContinueTimeout = 0
	return &sender{m: m, hook: w, client: fibhttp.NewClient(m.o.Engine, cc)}
}

// syncSenders makes the senders those of the webhooks: one unchanged keeps
// its queue and its status; one changed or removed is closed.
func (m *Manager) syncSenders() {
	al := &m.al
	al.mu.Lock()
	keep := map[string]*sender{}
	var gone []*sender
	for _, w := range al.cfg.webhooks {
		if s := al.senders[w.Name]; s != nil && s.hook == w {
			keep[w.Name] = s
			delete(al.senders, w.Name)
			continue
		}
		keep[w.Name] = m.newSender(w)
	}
	for _, s := range al.senders {
		gone = append(gone, s)
	}
	al.senders = keep
	al.mu.Unlock()
	for _, s := range gone {
		s.close()
	}
}

// enqueue queues n, dropping the oldest notification past MaxWebhookQueue,
// and sends it when nothing is under way. It never blocks.
func (s *sender) enqueue(n *notice) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		n.finish(WebhookResult{Error: "the webhook was changed or removed"})
		return
	}
	var dropped *notice
	if len(s.queue) >= MaxWebhookQueue {
		dropped, s.queue = s.queue[0], s.queue[1:]
		s.st.Dropped++
	}
	s.queue = append(s.queue, n)
	next := s.nextLocked()
	s.mu.Unlock()
	if dropped != nil {
		s.m.logf("cluster: the webhook %q has %d notifications waiting: the oldest was dropped", s.hook.Name, MaxWebhookQueue)
		dropped.finish(WebhookResult{Error: "dropped: the webhook had too many notifications waiting"})
	}
	if next != nil {
		s.send(next)
	}
}

// nextLocked takes the next notification to send when none is under way.
// s.mu is held.
func (s *sender) nextLocked() *notice {
	if s.busy || len(s.queue) == 0 {
		return nil
	}
	n := s.queue[0]
	s.queue = s.queue[1:]
	s.busy = true
	return n
}

// send sends n, and the next notification once it is over.
func (s *sender) send(n *notice) {
	start := time.Now()
	target, header, body := s.render(n, s.m.now().UTC())
	req, err := stdhttp.NewRequest("POST", target, nil)
	if err != nil {
		s.finish(n, WebhookResult{Error: err.Error()}, start)
		return
	}
	req.Header = header
	req.Body, req.ContentLength = fibhttp.NewBufferedBody(body), int64(len(body))
	s.client.Start(req, func(resp *fibhttp.ClientResponse, err error) {
		if err != nil {
			s.finish(n, WebhookResult{Error: err.Error()}, start)
			return
		}
		// The answer arrived whole, within maxWebhookAnswer.
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		res := WebhookResult{Status: resp.StatusCode, OK: resp.StatusCode >= 200 && resp.StatusCode <= 299}
		if !res.OK {
			res.Error = "the webhook answered " + strconv.Itoa(resp.StatusCode) + ": " + clip(strings.TrimSpace(string(data)), 200)
		} else if msg := chatRefusal(s.hook.Format, data); msg != "" {
			res.OK, res.Error = false, "the chat service refused it: "+clip(msg, 200)
		}
		s.finish(n, res, start)
	})
}

// chatRefusal returns why a chat service that answers 200 to every request
// refused this one, by the errcode or code of its answer; "" when it did
// not.
func chatRefusal(format string, data []byte) string {
	if format != FormatFeishu && format != FormatDingTalk && format != FormatWeCom {
		return ""
	}
	var b struct {
		ErrCode *int   `json:"errcode"`
		Code    *int   `json:"code"`
		ErrMsg  string `json:"errmsg"`
		Msg     string `json:"msg"`
	}
	_ = json.Unmarshal(data, &b)
	switch {
	case b.ErrCode != nil && *b.ErrCode != 0:
		return strconv.Itoa(*b.ErrCode) + " " + b.ErrMsg
	case b.Code != nil && *b.Code != 0:
		return strconv.Itoa(*b.Code) + " " + b.Msg
	}
	return ""
}

// finish records how n went, tells it, and sends the next.
func (s *sender) finish(n *notice, res WebhookResult, start time.Time) {
	res.TookMs = time.Since(start).Milliseconds()
	res.Error = redactURL(res.Error, s.hook.URL)
	if s.hook.Secret != "" {
		res.Error = strings.ReplaceAll(res.Error, s.hook.Secret, "••••")
	}
	now := s.m.now().UTC()
	s.mu.Lock()
	st := &s.st
	st.LastAt, st.LastStatus, st.LastError, st.LastTookMs = now, res.Status, res.Error, res.TookMs
	if res.OK {
		st.Sent++
		st.LastOK = now
	} else {
		st.Failed++
	}
	s.busy = false
	next := s.nextLocked()
	s.mu.Unlock()
	if !res.OK {
		s.m.logf("cluster: the webhook %q was not sent a %s notification: %s", s.hook.Name, n.status, res.Error)
	}
	n.finish(res)
	if next != nil {
		s.send(next)
	}
}

// close drops what waits and closes the connections; a notification under
// way hears its call cancelled.
func (s *sender) close() {
	s.mu.Lock()
	s.closed = true
	q := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, n := range q {
		n.finish(WebhookResult{Error: "the webhook was changed or removed"})
	}
	s.client.Close()
}

// status returns the webhook's status.
func (s *sender) status() WebhookStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.Queued = len(s.queue)
	if s.busy {
		st.Queued++
	}
	return st
}

// payload is a notification in FormatJSON.
type payload struct {
	Version     int       `json:"version"`
	Source      string    `json:"source"`
	Manager     string    `json:"manager"`
	ManagerHost string    `json:"manager_host"`
	Status      string    `json:"status"`
	Test        bool      `json:"test,omitempty"`
	Sent        time.Time `json:"sent"`
	Text        string    `json:"text"`
	Alerts      []Alert   `json:"alerts"`
	More        int       `json:"more,omitempty"`
}

// noticeVars are what a custom body's ${...} are replaced with.
type noticeVars struct {
	text, status, manager string
	count                 int
	alerts                []byte
}

// sampleNotice is what a custom template is checked with.
func sampleNotice() noticeVars {
	return noticeVars{text: "[FIRING] \"fit\"\n• a", status: AlertFiring, manager: "m", count: 1, alerts: []byte(`[{"rule":"r"}]`)}
}

// customBody is tpl with its ${...} replaced: the strings escaped as in a
// JSON string, ${count} a number and ${alerts} a JSON array.
func customBody(tpl string, v noticeVars) []byte {
	esc := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b[1 : len(b)-1])
	}
	r := strings.NewReplacer("${text}", esc(v.text), "${status}", esc(v.status), "${manager}", esc(v.manager),
		"${count}", strconv.Itoa(v.count), "${alerts}", string(v.alerts))
	return []byte(r.Replace(tpl))
}

// render returns where n goes, its headers and its body, in the webhook's
// format, signed when it has a secret.
func (s *sender) render(n *notice, now time.Time) (string, stdhttp.Header, []byte) {
	w := &s.hook
	host := hostname()
	status := n.status
	if n.test {
		status = "test"
	}
	text := noticeText(w, n, host)
	list := n.alerts
	more := 0
	if len(list) > maxNotifyAlerts {
		list, more = list[:maxNotifyAlerts], len(list)-maxNotifyAlerts
	}
	if list == nil {
		list = []Alert{}
	}
	h := stdhttp.Header{"Content-Type": {"application/json"}, "User-Agent": {"fit-cluster-manager"}}
	target := w.URL
	ts := now.Unix()
	var body []byte
	switch w.Format {
	case FormatSlack:
		body, _ = json.Marshal(map[string]string{"text": clip(text, 3800)})
	case FormatDiscord:
		body, _ = json.Marshal(map[string]string{"content": clip(text, 1900)})
	case FormatWeCom:
		body, _ = json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": clip(text, 2000)}})
	case FormatFeishu:
		b := map[string]any{"msg_type": "text", "content": map[string]string{"text": clip(text, 3800)}}
		if w.Secret != "" {
			// Feishu's: the HMAC-SHA256, keyed by the timestamp, a line
			// feed and the secret, of nothing.
			mac := hmac.New(sha256.New, []byte(strconv.FormatInt(ts, 10)+"\n"+w.Secret))
			b["timestamp"], b["sign"] = strconv.FormatInt(ts, 10), base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		body, _ = json.Marshal(b)
	case FormatDingTalk:
		body, _ = json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": clip(text, 3800)}})
		if w.Secret != "" {
			// DingTalk's: the HMAC-SHA256, keyed by the secret, of the
			// timestamp in milliseconds, a line feed and the secret.
			ms := strconv.FormatInt(now.UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(w.Secret))
			mac.Write([]byte(ms + "\n" + w.Secret))
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + "timestamp=" + ms + "&sign=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		}
	case FormatCustom:
		alerts, _ := json.Marshal(list)
		body = customBody(w.Template, noticeVars{text: text, status: status, manager: s.m.o.NodeID, count: len(n.alerts), alerts: alerts})
	default:
		body, _ = json.Marshal(payload{Version: 1, Source: "fit-cluster", Manager: s.m.o.NodeID, ManagerHost: host, Status: status, Test: n.test,
			Sent: now, Text: text, Alerts: list, More: more})
	}
	if w.Secret != "" && w.Format != FormatFeishu && w.Format != FormatDingTalk {
		// fit's own: the HMAC-SHA256, keyed by the secret, of the
		// timestamp, a dot and the body.
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
		mac.Write(body)
		h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
		h.Set(HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return target, h, body
}

// noticeText is a notification's text: a line saying what it is, and a
// line each alert, as the webhook's template has it for a chat service.
func noticeText(w *Webhook, n *notice, host string) string {
	var b strings.Builder
	switch {
	case n.test:
		fmt.Fprintf(&b, "[TEST] fit cluster on %s: a test of the webhook %s", host, w.Name)
	default:
		fmt.Fprintf(&b, "[%s] fit cluster on %s: %d alert", strings.ToUpper(n.status), host, len(n.alerts))
		if len(n.alerts) != 1 {
			b.WriteString("s")
		}
	}
	tpl := w.Template
	if w.Format == FormatJSON || w.Format == FormatCustom {
		tpl = ""
	}
	for i, a := range n.alerts {
		if i == maxTextAlerts {
			fmt.Fprintf(&b, "\n… and %d more", len(n.alerts)-maxTextAlerts)
			break
		}
		b.WriteString("\n")
		b.WriteString(alertLine(tpl, &a))
	}
	return b.String()
}

// alertLine is an alert's line: tpl with its ${...} replaced, or fit's own
// when tpl is empty.
func alertLine(tpl string, a *Alert) string {
	on := a.Node
	if on == "" {
		on = a.Subject
	}
	if tpl == "" {
		if on != "" {
			on = " · " + on
		}
		return "• [" + a.Severity + "] " + a.Rule + on + ": " + a.Summary
	}
	return strings.NewReplacer("${status}", a.State, "${rule}", a.Rule, "${kind}", a.Kind, "${severity}", a.Severity,
		"${node}", a.Node, "${subject}", a.Subject, "${on}", on, "${summary}", a.Summary,
		"${since}", a.Since.UTC().Format(time.RFC3339), "${id}", a.ID).Replace(tpl)
}

// TestWebhook sends the webhook name a test notification, as user, and
// tells done how it went, from a fib worker or the caller's goroutine. It
// writes the audit log: off a fib worker.
func (m *Manager) TestWebhook(user, name string, done func(WebhookResult)) error {
	m.al.mu.Lock()
	s := m.al.senders[name]
	m.al.mu.Unlock()
	if s == nil {
		return errNoAlert("no webhook is named " + strconv.Quote(name))
	}
	if err := m.appendAudit([]AuditEntry{{At: m.now().UTC(), User: user, Action: ActionAlertWebhookTest, Detail: name}}); err != nil {
		m.logf("cluster: the audit entry of %s was not written: %v", ActionAlertWebhookTest, err)
	}
	now := m.now().UTC()
	sample := Alert{ID: "test", Rule: "test", Kind: AlertMemberDown, Severity: SeverityInfo, State: AlertFiring,
		Summary: "a test of the webhook " + name + ", sent by " + user, Since: now, Fired: now}
	s.enqueue(&notice{status: AlertFiring, test: true, alerts: []Alert{sample}, done: done})
	return nil
}
