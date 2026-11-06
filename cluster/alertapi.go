package cluster

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/lesismal/fit/app/console"
)

// alertConfigBody is GET /api/alerts/config's answer: the rules, the
// webhooks (without their URLs' paths and their secrets, with their
// statuses), the silences, the latest first, and what the pages offer.
type alertConfigBody struct {
	Version    string        `json:"version"`
	ReadOnly   string        `json:"read_only,omitempty"`
	Rules      []AlertRule   `json:"rules"`
	Webhooks   []WebhookView `json:"webhooks"`
	Silences   []Silence     `json:"silences"`
	Kinds      []string      `json:"kinds"`
	Formats    []string      `json:"formats"`
	Severities []string      `json:"severities"`
	Bounds     alertBounds   `json:"bounds"`
}

type alertBounds struct {
	MaxRules    int   `json:"max_rules"`
	MaxWebhooks int   `json:"max_webhooks"`
	MaxSilences int   `json:"max_silences"`
	MaxHistory  int   `json:"max_history"`
	MaxQueue    int   `json:"max_queue"`
	TickMs      int64 `json:"tick_ms"`
}

// AlertConfig returns the rules, webhooks and silences as GET
// /api/alerts/config answers them.
func (m *Manager) AlertConfig() alertConfigBody {
	al := &m.al
	al.mu.Lock()
	b := alertConfigBody{Version: alertsTag(al.rev), ReadOnly: al.readOnly, Rules: []AlertRule{}, Webhooks: []WebhookView{},
		Silences: slices.Clone(al.cfg.silences), Kinds: AlertKinds, Formats: WebhookFormats, Severities: Severities,
		Bounds: alertBounds{MaxRules: MaxAlertRules, MaxWebhooks: MaxWebhooks, MaxSilences: MaxSilences, MaxHistory: MaxAlertHistory,
			MaxQueue: MaxWebhookQueue, TickMs: alertTick.Milliseconds()}}
	for _, r := range al.cfg.rules {
		b.Rules = append(b.Rules, r.clone())
	}
	type viewOf struct {
		v WebhookView
		s *sender
	}
	var views []viewOf
	for _, w := range al.cfg.webhooks {
		views = append(views, viewOf{w.view(), al.senders[w.Name]})
	}
	al.mu.Unlock()
	for _, x := range views {
		if x.s != nil {
			x.v.Status = x.s.status()
		}
		b.Webhooks = append(b.Webhooks, x.v)
	}
	if b.Silences == nil {
		b.Silences = []Silence{}
	}
	slices.SortStableFunc(b.Silences, func(a, c Silence) int { return c.Ends.Compare(a.Ends) })
	return b
}

func alertsTag(rev uint64) string { return "a" + strconv.FormatUint(rev, 10) }

func (m *Manager) alertsETag() string {
	m.al.mu.Lock()
	defer m.al.mu.Unlock()
	return strconv.Quote(alertsTag(m.al.rev))
}

func (m *Manager) alertRoutes(api *console.API) {
	api.Handle("GET", "/api/alerts", func(r *console.Request) { _ = r.JSON(200, m.Alerts()) })
	api.Handle("GET", "/api/alerts/summary", func(r *console.Request) { _ = r.JSON(200, m.AlertSummary()) })
	api.Handle("GET", "/api/alerts/config", func(r *console.Request) {
		b := m.AlertConfig()
		r.Header().Set("ETag", strconv.Quote(b.Version))
		_ = r.JSON(200, b)
	})
	api.Handle("POST", "/api/alerts/rules", func(r *console.Request) {
		var in AlertRule
		if !m.alertBody(r, &in) {
			return
		}
		user := r.User
		r.Go(func() {
			out, err := m.AddAlertRule(user, &in)
			m.answerAlerts(r, 201, out, err)
		})
	})
	api.HandlePrefix("", "/api/alerts/rules/", m.ruleRoute)
	api.Handle("POST", "/api/alerts/webhooks", func(r *console.Request) {
		var in WebhookInput
		if !m.alertBody(r, &in) {
			return
		}
		user := r.User
		r.Go(func() {
			out, err := m.AddWebhook(user, &in)
			m.answerAlerts(r, 201, out, err)
		})
	})
	api.HandlePrefix("", "/api/alerts/webhooks/", m.webhookRoute)
	api.Handle("POST", "/api/alerts/silences", func(r *console.Request) {
		var in SilenceInput
		if !m.alertBody(r, &in) {
			return
		}
		user := r.User
		r.Go(func() {
			out, err := m.AddSilence(user, &in)
			m.answerAlerts(r, 201, out, err)
		})
	})
	api.HandlePrefix("", "/api/alerts/silences/", m.silenceRoute)
	api.Handle("GET", "/api/drift", func(r *console.Request) {
		m.mu.Lock()
		ro := m.readOnly
		m.mu.Unlock()
		_ = r.JSON(200, map[string]any{"interval_ms": m.interval.Milliseconds(), "read_only": ro, "nodes": m.DriftStates()})
	})
	api.Handle("POST", "/api/drift/fix", func(r *console.Request) {
		var in struct {
			Nodes []string `json:"nodes"`
		}
		if !bodyBounded(r) || !r.Decode(&in) {
			return
		}
		user := r.User
		r.Go(func() {
			ro, err := m.FixDrift(user, in.Nodes)
			m.answerRollout(r, 202, ro, err)
		})
	})
}

// alertBody decodes a change's body, bounded, and checks its If-Match
// against the alerts' ETag; it answers when it fails.
func (m *Manager) alertBody(r *console.Request, v any) bool {
	if !bodyBounded(r) || !r.Decode(v) {
		return false
	}
	return m.alertsMatch(r)
}

// alertsMatch reports whether the request's If-Match, when it sends one, is
// the alerts' ETag; it answers 412 when not.
func (m *Manager) alertsMatch(r *console.Request) bool {
	want := r.Request.Header.Get("If-Match")
	if want == "" || want == "*" || want == m.alertsETag() {
		return true
	}
	_ = console.Error(r.Context, 412, "the alerts' rules, webhooks or silences changed since the page read them; read them again")
	return false
}

// answerAlerts answers a change of the alerts: its result with the alerts'
// ETag, or its error at its status.
func (m *Manager) answerAlerts(r *console.Request, status int, v any, err error) {
	if err == nil {
		r.Header().Set("ETag", m.alertsETag())
		_ = r.JSON(status, v)
		return
	}
	if errors.As(err, new(errNoAlert)) {
		_ = console.Error(r.Context, 404, err.Error())
		return
	}
	m.answer(r, status, v, err)
}

// itemRoute splits /api/alerts/<what>/<name>[/<action>].
func itemRoute(r *console.Request, prefix string) (string, string) {
	name, action, _ := strings.Cut(strings.TrimPrefix(r.Path, prefix), "/")
	return name, action
}

func notAllowed(r *console.Request, allow string) {
	r.Header().Set("Allow", allow)
	_ = console.Error(r.Context, 405, "the method is not allowed")
}

// ruleRoute answers /api/alerts/rules/<name>: PUT and DELETE.
func (m *Manager) ruleRoute(r *console.Request) {
	name, action := itemRoute(r, "/api/alerts/rules/")
	user := r.User
	switch {
	case action != "":
		_ = console.Error(r.Context, 404, "no such endpoint")
	case r.Request.Method == "PUT":
		var in AlertRule
		if !m.alertBody(r, &in) {
			return
		}
		r.Go(func() {
			out, err := m.UpdateAlertRule(user, name, &in)
			m.answerAlerts(r, 200, out, err)
		})
	case r.Request.Method == "DELETE":
		if !m.alertsMatch(r) {
			return
		}
		r.Go(func() {
			err := m.RemoveAlertRule(user, name)
			m.answerAlerts(r, 200, map[string]string{"removed": name}, err)
		})
	default:
		notAllowed(r, "PUT, DELETE")
	}
}

// webhookRoute answers /api/alerts/webhooks/<name>: PUT, DELETE, and
// POST <name>/test.
func (m *Manager) webhookRoute(r *console.Request) {
	name, action := itemRoute(r, "/api/alerts/webhooks/")
	user := r.User
	method := r.Request.Method
	switch {
	case action == "test" && method == "POST":
		// The answer waits for the webhook, past the task: the request is
		// held until then, and let go here when the task never ran.
		r.Context.Retain()
		var queued atomic.Bool
		r.GoThen(func() {
			err := m.TestWebhook(user, name, func(res WebhookResult) {
				defer r.Context.Release()
				_ = r.JSON(200, res)
			})
			if err != nil {
				m.answerAlerts(r, 0, nil, err)
				return
			}
			queued.Store(true)
		}, func() {
			if !queued.Load() {
				r.Context.Release()
			}
		})
	case action != "":
		_ = console.Error(r.Context, 404, "no such endpoint")
	case method == "PUT":
		var in WebhookInput
		if !m.alertBody(r, &in) {
			return
		}
		r.Go(func() {
			out, err := m.UpdateWebhook(user, name, &in)
			m.answerAlerts(r, 200, out, err)
		})
	case method == "DELETE":
		if !m.alertsMatch(r) {
			return
		}
		r.Go(func() {
			err := m.RemoveWebhook(user, name)
			m.answerAlerts(r, 200, map[string]string{"removed": name}, err)
		})
	default:
		notAllowed(r, "PUT, DELETE")
	}
}

// silenceRoute answers DELETE /api/alerts/silences/<id>, which ends it.
func (m *Manager) silenceRoute(r *console.Request) {
	id, action := itemRoute(r, "/api/alerts/silences/")
	user := r.User
	switch {
	case action != "":
		_ = console.Error(r.Context, 404, "no such endpoint")
	case r.Request.Method == "DELETE":
		if !m.alertsMatch(r) {
			return
		}
		r.Go(func() {
			out, err := m.ExpireSilence(user, id)
			m.answerAlerts(r, 200, out, err)
		})
	default:
		notAllowed(r, "DELETE")
	}
}
