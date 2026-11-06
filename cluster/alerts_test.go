package cluster

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestAlertRuleCheck checks each field of a rule, and fills its defaults.
func TestAlertRuleCheck(t *testing.T) {
	for _, c := range []struct {
		r     AlertRule
		field string
	}{
		{AlertRule{Name: "", Kind: AlertDrift}, "name"},
		{AlertRule{Name: "a b", Kind: AlertDrift}, "name"},
		{AlertRule{Name: "a", Kind: "nope"}, "kind"},
		{AlertRule{Name: "a", Kind: AlertDrift, Severity: "loud"}, "severity"},
		{AlertRule{Name: "a", Kind: AlertDrift, ForMs: -1}, "for_ms"},
		{AlertRule{Name: "a", Kind: AlertDrift, ForMs: 25 * 3_600_000}, "for_ms"},
		{AlertRule{Name: "a", Kind: AlertDrift, RepeatMs: 1000}, "repeat_ms"},
		{AlertRule{Name: "a", Kind: AlertCertExpiry, Days: 400}, "days"},
		{AlertRule{Name: "a", Kind: AlertBackendsDown, Threshold: -1}, "threshold"},
		{AlertRule{Name: "a", Kind: AlertDrift, Source: "x"}, "source"},
		{AlertRule{Name: "a", Kind: AlertReloadFailed, ResolveAfterMs: 1000}, "resolve_after_ms"},
		{AlertRule{Name: "a", Kind: AlertDrift, Webhooks: make([]string, MaxWebhooks+1)}, "webhooks"},
	} {
		var fe *FieldError
		if err := c.r.check(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%+v: %v", c.r, err)
		}
	}
	r := AlertRule{Name: "a", Kind: AlertCertExpiry, Threshold: 3, Source: "x", ResolveAfterMs: 5, Webhooks: []string{"b", "a", "b"}}
	if err := r.check(); err != nil || r.Days != DefaultCertDays || r.Threshold != 0 || r.Source != "" || r.ResolveAfterMs != 0 ||
		r.Severity != SeverityWarning || strings.Join(r.Webhooks, ",") != "a,b" {
		t.Fatalf("%+v %v", r, err)
	}
	r = AlertRule{Name: "a", Kind: AlertRolloutFailed}
	if err := r.check(); err != nil || r.Severity != SeverityCritical || r.Webhooks == nil {
		t.Fatalf("%+v %v", r, err)
	}
	r = AlertRule{Name: "a", Kind: AlertBackendsDown}
	if err := r.check(); err != nil || r.Threshold != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, d := range defaultRules() {
		if d.RepeatMs != DefaultRepeat.Milliseconds() || d.Severity == "" {
			t.Fatalf("%+v", d)
		}
	}
}

// TestWebhookCheck checks a webhook's fields, its URL never in an error,
// and what an edit keeps.
func TestWebhookCheck(t *testing.T) {
	now := time.Now()
	ok := WebhookInput{Name: "w", URL: "https://hooks.example/T/B/secret-part?x=1"}
	for _, c := range []struct {
		in    WebhookInput
		field string
	}{
		{WebhookInput{Name: "", URL: ok.URL}, "name"},
		{WebhookInput{Name: "w", URL: ok.URL, Format: "irc"}, "format"},
		{WebhookInput{Name: "w", URL: ok.URL, Secret: "a\nb"}, "secret"},
		{WebhookInput{Name: "w", URL: ok.URL, Template: strings.Repeat("x", MaxWebhookTemplate+1)}, "template"},
		{WebhookInput{Name: "w", URL: ok.URL, Template: "\xff"}, "template"},
		{WebhookInput{Name: "w", URL: ok.URL, TimeoutMs: 500}, "timeout_ms"},
		{WebhookInput{Name: "w", URL: ok.URL, Format: FormatCustom}, "template"},
		{WebhookInput{Name: "w", URL: ok.URL, Format: FormatCustom, Template: `{"a":${text}}`}, "template"},
		{WebhookInput{Name: "w", URL: ""}, "url"},
		{WebhookInput{Name: "w", URL: "https://u:p@hooks.example/secret-part"}, "url"},
		{WebhookInput{Name: "w", URL: "http://hooks.example/secret-part"}, "url"},
		{WebhookInput{Name: "w", URL: "ftp://hooks.example/secret-part"}, "url"},
		{WebhookInput{Name: "w", URL: "https://hooks.example/secret-part#f"}, "url"},
	} {
		_, err := c.in.webhook(nil, now)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != c.field || strings.Contains(err.Error(), "secret-part") {
			t.Errorf("%+v: %v", c.in, err)
		}
	}
	w, err := ok.webhook(nil, now)
	if err != nil || w.Format != FormatJSON || w.TimeoutMs != DefaultWebhookTimeout.Milliseconds() || !w.SendResolved {
		t.Fatalf("%+v %v", w, err)
	}
	w.Secret = "kept"
	no := false
	edit := WebhookInput{Name: "w", SendResolved: &no, Format: FormatCustom, Template: `{"t":"${text}","n":${count},"a":${alerts}}`}
	e, err := edit.webhook(&w, now.Add(time.Hour))
	if err != nil || e.URL != w.URL || e.Secret != "kept" || e.SendResolved || !e.Created.Equal(w.Created) {
		t.Fatalf("%+v %v", e, err)
	}
	edit.ClearSecret = true
	if e, _ = edit.webhook(&w, now); e.Secret != "" {
		t.Fatal("the secret was not cleared")
	}
	v := w.view()
	if v.URLHost != "https://hooks.example" || !v.SecretSet {
		t.Fatalf("%+v", v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "secret-part") || strings.Contains(string(b), "kept") {
		t.Fatal(string(b))
	}
}

// TestSilenceInput checks a silence's fields and times.
func TestSilenceInput(t *testing.T) {
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in    SilenceInput
		field string
	}{
		{SilenceInput{}, "rule"},
		{SilenceInput{Rule: "a b", DurationMs: 1}, "rule"},
		{SilenceInput{Node: "a b", DurationMs: 1}, "node"},
		{SilenceInput{Rule: "r", Comment: "a\x01", DurationMs: 1}, "comment"},
		{SilenceInput{Rule: "r"}, "ends"},
		{SilenceInput{Rule: "r", Ends: now.Add(-time.Hour)}, "ends"},
		{SilenceInput{Rule: "r", Starts: now.Add(time.Hour), Ends: now.Add(time.Minute)}, "ends"},
		{SilenceInput{Rule: "r", DurationMs: (31 * 24 * time.Hour).Milliseconds()}, "ends"},
	} {
		var fe *FieldError
		if _, err := c.in.silence("u", now); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%+v: %v", c.in, err)
		}
	}
	s, err := (&SilenceInput{Rule: "r", Node: "a", Comment: " upgrade ", DurationMs: 60_000}).silence("ann", now)
	if err != nil || !s.Starts.Equal(now) || !s.Ends.Equal(now.Add(time.Minute)) || s.Comment != "upgrade" || s.CreatedBy != "ann" || s.ID == "" {
		t.Fatalf("%+v %v", s, err)
	}
	if !s.active(now) || s.active(s.Ends) || !s.matches(&Alert{Rule: "r", Node: "a"}) || s.matches(&Alert{Rule: "r", Node: "b"}) {
		t.Fatal("matching")
	}
}

// TestAlertConfigChanges adds, changes and removes rules, webhooks and
// silences; each is written to the store and the audit log; a webhook's
// rename follows into its rules; one a rule names cannot go; the bounds.
func TestAlertConfigChanges(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	m := f.m
	if _, err := m.AddGroup("tester", &GroupInput{Name: "eu", Selector: "region=eu"}); err != nil {
		t.Fatal(err)
	}
	cfg := m.AlertConfig()
	if len(cfg.Rules) != len(defaultRules()) || cfg.ReadOnly != "" {
		t.Fatalf("%+v", cfg)
	}
	if _, err := m.AddWebhook("ann", &WebhookInput{Name: "ops", URL: "https://hooks.example/T/B/tok"}); err != nil {
		t.Fatal(err)
	}
	r, err := m.AddAlertRule("ann", &AlertRule{Name: "eu-down", Kind: AlertMemberDown, Group: "eu", Webhooks: []string{"ops"}, Disabled: true})
	if err != nil || r.Created.IsZero() {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range []struct {
		r    AlertRule
		want string
	}{
		{AlertRule{Name: "eu-down", Kind: AlertMemberDown}, "already named"},
		{AlertRule{Name: "x", Kind: AlertMemberDown, Group: "nope"}, "no group"},
		{AlertRule{Name: "x", Kind: AlertMemberDown, Webhooks: []string{"nope"}}, "no webhook"},
		{AlertRule{Name: "x y", Kind: AlertMemberDown}, "not a name"},
	} {
		if _, err := m.AddAlertRule("ann", &c.r); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.r, err)
		}
	}
	// A rename of the webhook follows into the rule.
	if _, err := m.UpdateWebhook("ann", "ops", &WebhookInput{Name: "oncall", Format: FormatSlack}); err != nil {
		t.Fatal(err)
	}
	cfg = m.AlertConfig()
	if i := slicesIndex(cfg.Rules, "eu-down"); i < 0 || strings.Join(cfg.Rules[i].Webhooks, ",") != "oncall" {
		t.Fatalf("%+v", cfg.Rules)
	}
	if cfg.Webhooks[0].URLHost != "https://hooks.example" || cfg.Webhooks[0].Format != FormatSlack {
		t.Fatalf("%+v", cfg.Webhooks)
	}
	if _, err := m.UpdateWebhook("ann", "none", &WebhookInput{}); err == nil {
		t.Fatal("updated a webhook there is none of")
	}
	if _, err := m.UpdateWebhook("ann", "oncall", &WebhookInput{Format: "x"}); err == nil {
		t.Fatal("a bad webhook")
	}
	if _, err := m.AddWebhook("ann", &WebhookInput{Name: "other", URL: "https://b.example/"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateWebhook("ann", "other", &WebhookInput{Name: "oncall"}); err == nil {
		t.Fatal("renamed onto another")
	}
	if _, err := m.AddWebhook("ann", &WebhookInput{Name: "other", URL: "https://b.example/"}); err == nil {
		t.Fatal("added twice")
	}
	if _, err := m.AddWebhook("ann", &WebhookInput{Name: "bad"}); err == nil {
		t.Fatal("a webhook without a URL")
	}
	if err := m.RemoveWebhook("ann", "oncall"); err == nil || !strings.Contains(err.Error(), "eu-down") {
		t.Fatalf("removed a webhook a rule names: %v", err)
	}
	if err := m.RemoveWebhook("ann", "none"); err == nil {
		t.Fatal("removed a webhook there is none of")
	}
	if err := m.RemoveWebhook("ann", "other"); err != nil {
		t.Fatal(err)
	}
	// Rules changed, renamed, removed.
	if _, err := m.UpdateAlertRule("ann", "eu-down", &AlertRule{Kind: AlertMemberDown, ForMs: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateAlertRule("ann", "eu-down", &AlertRule{Name: "drift", Kind: AlertMemberDown}); err == nil {
		t.Fatal("renamed onto another")
	}
	if _, err := m.UpdateAlertRule("ann", "eu-down", &AlertRule{Kind: "x"}); err == nil {
		t.Fatal("a bad rule")
	}
	if _, err := m.UpdateAlertRule("ann", "eu-down", &AlertRule{Kind: AlertMemberDown, Group: "x"}); err == nil {
		t.Fatal("a group there is none of")
	}
	if _, err := m.UpdateAlertRule("ann", "none", &AlertRule{Kind: AlertMemberDown}); err == nil {
		t.Fatal("updated a rule there is none of")
	}
	if _, err := m.UpdateAlertRule("ann", "eu-down", &AlertRule{Name: "down-eu", Kind: AlertMemberDown}); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveAlertRule("ann", "down-eu"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveAlertRule("ann", "down-eu"); err == nil {
		t.Fatal("removed twice")
	}
	// Silences.
	s, err := m.AddSilence("ann", &SilenceInput{Node: "a", DurationMs: 3_600_000, Comment: "maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddSilence("ann", &SilenceInput{}); err == nil {
		t.Fatal("a silence of nothing")
	}
	later, err := m.AddSilence("ann", &SilenceInput{Rule: "drift", Starts: time.Now().Add(time.Hour), DurationMs: 60_000})
	if err != nil {
		t.Fatal(err)
	}
	if e, err := m.ExpireSilence("ann", s.ID); err != nil || e.Ends.After(time.Now()) {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err := m.ExpireSilence("ann", later.ID); err != nil || e.Starts.After(e.Ends) {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := m.ExpireSilence("ann", "none"); err == nil {
		t.Fatal("expired a silence there is none of")
	}
	// Kept in the store, the URL and the secret with them; in the audit
	// log by name only.
	var d alertsDoc
	if err := json.Unmarshal(f.store.Section(AlertsSection), &d); err != nil || len(d.Webhooks) != 1 || d.Webhooks[0].URL != "https://hooks.example/T/B/tok" || len(d.Silences) != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	var actions []string
	for _, e := range m.Audit() {
		actions = append(actions, e.Action)
		if strings.Contains(e.Detail, "/T/B/tok") {
			t.Fatalf("the URL is in the audit log: %+v", e)
		}
	}
	for _, a := range []string{ActionAlertWebhookAdd, ActionAlertRuleAdd, ActionAlertWebhookUpdate, ActionAlertWebhookRemove, ActionAlertRuleUpdate,
		ActionAlertRuleRemove, ActionAlertSilenceAdd, ActionAlertSilenceExpire} {
		if !strings.Contains(strings.Join(actions, " "), a) {
			t.Errorf("no %s in %v", a, actions)
		}
	}
	if strings.Contains(f.log.String(), "/T/B/tok") {
		t.Fatal(f.log.String())
	}
	// The store refusing a write refuses the change.
	f.store.mu.Lock()
	f.store.fail = errors.New("disk full")
	f.store.mu.Unlock()
	if _, err := m.AddAlertRule("ann", &AlertRule{Name: "z", Kind: AlertDrift}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("%v", err)
	}
	f.store.mu.Lock()
	f.store.fail = nil
	f.store.mu.Unlock()
}

func slicesIndex(rules []AlertRule, name string) int {
	for i, r := range rules {
		if r.Name == name {
			return i
		}
	}
	return -1
}

// TestAlertBoundsOfConfig: the most rules, webhooks and silences.
func TestAlertBoundsOfConfig(t *testing.T) {
	clock := newClock()
	m, _, _ := bareManager(t, clock.now)
	m.al.mu.Lock()
	m.al.cfg.rules = nil
	m.al.mu.Unlock()
	for i := 0; i < MaxAlertRules; i++ {
		if _, err := m.AddAlertRule("u", &AlertRule{Name: "r" + itoa(i), Kind: AlertDrift}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddAlertRule("u", &AlertRule{Name: "more", Kind: AlertDrift}); err == nil {
		t.Fatal("past the most rules")
	}
	for i := 0; i < MaxWebhooks; i++ {
		if _, err := m.AddWebhook("u", &WebhookInput{Name: "w" + itoa(i), URL: "https://h.example/"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddWebhook("u", &WebhookInput{Name: "more", URL: "https://h.example/"}); err == nil {
		t.Fatal("past the most webhooks")
	}
	for i := 0; i < MaxSilences; i++ {
		if _, err := m.AddSilence("u", &SilenceInput{Rule: "r", DurationMs: 60_000}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.AddSilence("u", &SilenceInput{Rule: "r", DurationMs: 60_000}); err == nil {
		t.Fatal("past the most silences")
	}
	// One that ended makes room; those ended a week ago go.
	clock.add(2 * time.Minute)
	m.al.mu.Lock()
	m.al.cfg.silences[0].Ends = clock.now().Add(-8 * 24 * time.Hour)
	m.al.mu.Unlock()
	if _, err := m.AddSilence("u", &SilenceInput{Rule: "r", DurationMs: 60_000}); err != nil {
		t.Fatal(err)
	}
	if n := len(m.AlertConfig().Silences); n != MaxSilences {
		t.Fatalf("%d silences", n)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// reload has a bare manager read its store's section again.
func reload(m *Manager) {
	m.loadAlerts()
	close(m.al.done)
}

// TestAlertsKept: the alerts firing and past, the rules, webhooks and
// silences outlive a restart; a section of a newer fit, or one that is not
// the alerts', is read-only; a rule kept that does not check is dropped.
func TestAlertsKept(t *testing.T) {
	clock := newClock()
	m, st, log := bareManager(t, clock.now)
	setRules(m, []AlertRule{{Name: "down", Kind: AlertMemberDown}})
	eval(m, clock.now(), &evalInput{states: []State{down("a", clock.now()), down("b", clock.now())}})
	eval(m, clock.add(time.Second), &evalInput{states: []State{down("a", clock.now()), up("b", clock.now())}})
	m.saveAlerts()
	data := st.Section(AlertsSection)
	m2, st2, _ := bareManager(t, clock.now)
	if err := st2.SetSection(AlertsSection, data); err != nil {
		t.Fatal(err)
	}
	reload(m2)
	if v := m2.Alerts(); len(v.Active) != 1 || v.Active[0].Node != "a" || len(v.History) != 1 || v.History[0].Node != "b" {
		t.Fatalf("%+v", v)
	}
	// The store failing: logged, and written later.
	st.mu.Lock()
	st.fail = errors.New("disk full")
	st.mu.Unlock()
	eval(m, clock.add(time.Second), &evalInput{states: []State{up("a", clock.now())}})
	m.saveAlerts()
	if !strings.Contains(log.String(), "the alerts were not saved: disk full") {
		t.Fatal(log.String())
	}
	st.mu.Lock()
	st.fail = nil
	st.mu.Unlock()
	m.saveAlerts()
	var d alertsDoc
	_ = json.Unmarshal(st.Section(AlertsSection), &d)
	if len(d.Active) != 0 || len(d.History) != 2 {
		t.Fatalf("%+v", d)
	}
	// Newer, broken, a rule that does not check.
	for _, c := range []struct{ data, want string }{
		{`{"version":9,"rules":[{"name":"x","kind":"drift"}]}`, "newer fit"},
		{`[1]`, "is not the alerts' document"},
		{`{"version":1,"rules":[{"name":"x","kind":"nope"}]}`, "is not used"},
	} {
		m3, st3, log3 := bareManager(t, clock.now)
		_ = st3.SetSection(AlertsSection, []byte(c.data))
		reload(m3)
		if !strings.Contains(log3.String(), c.want) {
			t.Errorf("%s: %s", c.data, log3.String())
		}
		if c.want != "is not used" {
			if _, err := m3.AddAlertRule("u", &AlertRule{Name: "y", Kind: AlertDrift}); !errors.As(err, new(errReadOnly)) {
				t.Errorf("%s: %v", c.data, err)
			}
			m3.al.mu.Lock()
			m3.al.dirty = true
			m3.al.mu.Unlock()
			m3.saveAlerts()
			if string(st3.Section(AlertsSection)) != c.data {
				t.Errorf("%s was written over", c.data)
			}
		}
	}
}
