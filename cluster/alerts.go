package cluster

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Drift and alerts (I147). Drift is checked on every poll: a member's
// consistency check comes with the poll itself, and a member a template is
// attached to is read against its templates once its config's ETag changed
// since its sync state was taken (alerteval.go). A drift is fixed by a
// rollout of the members, which a person plans and starts (drift.go): no
// fix is ever applied unasked. Alert rules watch the members' states, their
// drift, the rollouts and the certificates; an alert fires once its
// condition has lasted the rule's time, is sent to webhooks (webhooks.go),
// again every repeat interval while it fires and is not silenced, and
// resolves once its condition is gone. The rules, the webhooks with their
// URLs and secrets, the silences, the alerts firing and the past ones are
// kept in the store's section AlertsSection, encrypted with the rest of the
// store, never in an export; a webhook's URL and secret are never answered
// or logged. Everything is bounded, and nothing here waits on a member or
// a webhook: the evaluator is a goroutine of its own, and the calls go out
// on fib's asynchronous client.

// AlertsSection is the store's section the alerts are kept in, beside the
// manager's document, so that a fit that predates them keeps them as it
// finds them.
const AlertsSection = "cluster.alerts"

// AlertsVersion is the version of the section this fit writes; a section
// of a later one is read for its rules and never written over.
const AlertsVersion = 1

// The kinds of alert rules, AlertRule.Kind.
const (
	// AlertMemberDown: a member down, or failing otherwise (its
	// credentials, its certificate), since its state changed.
	AlertMemberDown = "member_down"
	// AlertDrift: a member out of sync with its templates or in conflict
	// with them, or whose consistency check finds differences.
	AlertDrift = "drift"
	// AlertRolloutFailed: a rollout that ended failed, until it is rolled
	// back or ResolveAfterMs has passed since.
	AlertRolloutFailed = "rollout_failed"
	// AlertCertExpiry: a member's certificate that expires within Days.
	AlertCertExpiry = "cert_expiry"
	// AlertBackendsDown: at least Threshold of a member's upstream
	// backends down.
	AlertBackendsDown = "backends_down"
	// AlertReloadFailed: a member whose latest reload failed, until one
	// succeeds or ResolveAfterMs has passed since.
	AlertReloadFailed = "reload_failed"
)

// AlertKinds are the kinds of rules, in the order the pages offer them.
var AlertKinds = []string{AlertMemberDown, AlertDrift, AlertRolloutFailed, AlertCertExpiry, AlertBackendsDown, AlertReloadFailed}

// The severities of a rule.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Severities are the severities, the least first.
var Severities = []string{SeverityInfo, SeverityWarning, SeverityCritical}

// What a drift rule watches, AlertRule.Source.
const (
	DriftBoth        = ""
	DriftTemplates   = "templates"
	DriftConsistency = "consistency"
)

// The states of an alert.
const (
	// AlertPending: its condition holds, and has not lasted the rule's
	// time yet.
	AlertPending = "pending"
	// AlertFiring: its condition has lasted the rule's time.
	AlertFiring = "firing"
	// AlertResolved: its condition is gone, or its rule.
	AlertResolved = "resolved"
)

// The bounds of alerts.
const (
	MaxAlertRules   = 64
	MaxWebhooks     = 16
	MaxSilences     = 256
	MaxActiveAlerts = 2048
	MaxAlertHistory = 500
	// MaxWebhookQueue bounds the notifications waiting for a webhook; the
	// oldest is dropped past it.
	MaxWebhookQueue = 64
	// MaxWebhookTemplate bounds a webhook's template.
	MaxWebhookTemplate = 4 << 10
	// MaxWebhookSecret bounds a webhook's secret.
	MaxWebhookSecret = 1024
	maxAlertText     = 512
	maxSilenceSpan   = 30 * 24 * time.Hour
	// silenceKept is how long a silence that ended is kept, shown as past.
	silenceKept = 7 * 24 * time.Hour
)

// The defaults and bounds of a rule's times.
const (
	DefaultRepeat       = 4 * time.Hour
	DefaultCertDays     = 14
	maxAlertFor         = 24 * time.Hour
	minRepeat           = time.Minute
	maxRepeat           = 7 * 24 * time.Hour
	minResolveAfter     = time.Minute
	maxResolveAfter     = 30 * 24 * time.Hour
	maxCertDays         = 365
	maxBackendThreshold = 10000
)

// AlertRule is a rule: what it watches (Kind), on which members (those of
// Group, or every member), how long its condition must last before it
// fires (ForMs), how often a firing alert is sent again (RepeatMs, 0 for
// never), and where it is sent (Webhooks, every webhook when empty).
type AlertRule struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Disabled bool   `json:"disabled,omitempty"`
	Group    string `json:"group,omitempty"`
	Severity string `json:"severity"`
	ForMs    int64  `json:"for_ms"`
	RepeatMs int64  `json:"repeat_ms"`
	// ResolveAfterMs ends a rollout_failed or reload_failed alert that
	// long after the failure, 0 for never.
	ResolveAfterMs int64 `json:"resolve_after_ms,omitempty"`
	// Days is a cert_expiry rule's, Threshold a backends_down rule's, and
	// Source what a drift rule watches.
	Days      int       `json:"days,omitempty"`
	Threshold int       `json:"threshold,omitempty"`
	Source    string    `json:"source,omitempty"`
	Webhooks  []string  `json:"webhooks"`
	Created   time.Time `json:"created,omitzero"`
	Updated   time.Time `json:"updated,omitzero"`
}

// defaultRules are the rules of a manager that has none kept yet.
func defaultRules() []AlertRule {
	ms := func(d time.Duration) int64 { return d.Milliseconds() }
	rules := []AlertRule{
		{Name: "member-down", Kind: AlertMemberDown, ForMs: ms(time.Minute)},
		{Name: "drift", Kind: AlertDrift, ForMs: ms(5 * time.Minute)},
		{Name: "rollout-failed", Kind: AlertRolloutFailed, ResolveAfterMs: ms(24 * time.Hour)},
		{Name: "certificate-expiry", Kind: AlertCertExpiry, Days: DefaultCertDays},
		{Name: "backends-down", Kind: AlertBackendsDown, Threshold: 1, ForMs: ms(2 * time.Minute)},
		{Name: "reloads-failing", Kind: AlertReloadFailed, ResolveAfterMs: ms(time.Hour)},
	}
	for i := range rules {
		rules[i].RepeatMs = ms(DefaultRepeat)
		_ = rules[i].check()
	}
	return rules
}

func (r AlertRule) clone() AlertRule {
	r.Webhooks = slices.Clone(r.Webhooks)
	return r
}

func (r *AlertRule) forDur() time.Duration    { return time.Duration(r.ForMs) * time.Millisecond }
func (r *AlertRule) repeatDur() time.Duration { return time.Duration(r.RepeatMs) * time.Millisecond }
func (r *AlertRule) resolveAfterDur() time.Duration {
	return time.Duration(r.ResolveAfterMs) * time.Millisecond
}

// check fills in r's defaults and checks it, alone; its group and webhooks
// are checked against the manager's by checkRefs.
func (r *AlertRule) check() error {
	if r.Name == "" || len(r.Name) > MaxNameBytes || !namePattern.MatchString(r.Name) {
		return fieldErr("name", "%q is not a name: 1 to %d letters, digits, '_', '.' and '-', starting with a letter or digit", r.Name, MaxNameBytes)
	}
	if !slices.Contains(AlertKinds, r.Kind) {
		return fieldErr("kind", "%q is not a kind of rule: %s", r.Kind, strings.Join(AlertKinds, ", "))
	}
	if r.Severity == "" {
		r.Severity = SeverityWarning
		if r.Kind == AlertMemberDown || r.Kind == AlertRolloutFailed {
			r.Severity = SeverityCritical
		}
	}
	if !slices.Contains(Severities, r.Severity) {
		return fieldErr("severity", "%q is not a severity: %s", r.Severity, strings.Join(Severities, ", "))
	}
	if r.ForMs < 0 || r.forDur() > maxAlertFor {
		return fieldErr("for_ms", "a rule's time is 0 to %s", maxAlertFor)
	}
	if r.RepeatMs != 0 && (r.repeatDur() < minRepeat || r.repeatDur() > maxRepeat) {
		return fieldErr("repeat_ms", "a repeat interval is 0 (never) or %s to %s", minRepeat, maxRepeat)
	}
	r.Days, r.Threshold, r.Source = keepIf(r.Kind == AlertCertExpiry, r.Days), keepIf(r.Kind == AlertBackendsDown, r.Threshold), keepIf(r.Kind == AlertDrift, r.Source)
	if r.Kind != AlertRolloutFailed && r.Kind != AlertReloadFailed {
		r.ResolveAfterMs = 0
	}
	switch r.Kind {
	case AlertCertExpiry:
		if r.Days == 0 {
			r.Days = DefaultCertDays
		}
		if r.Days < 1 || r.Days > maxCertDays {
			return fieldErr("days", "a certificate's days are 1 to %d", maxCertDays)
		}
	case AlertBackendsDown:
		if r.Threshold == 0 {
			r.Threshold = 1
		}
		if r.Threshold < 1 || r.Threshold > maxBackendThreshold {
			return fieldErr("threshold", "the backends down are 1 to %d", maxBackendThreshold)
		}
	case AlertDrift:
		if r.Source != DriftBoth && r.Source != DriftTemplates && r.Source != DriftConsistency {
			return fieldErr("source", "%q is not what a drift rule watches: %s, %s, or empty for both", r.Source, DriftTemplates, DriftConsistency)
		}
	default:
		if r.ResolveAfterMs != 0 && (r.resolveAfterDur() < minResolveAfter || r.resolveAfterDur() > maxResolveAfter) {
			return fieldErr("resolve_after_ms", "a rule resolves after 0 (never) or %s to %s", minResolveAfter, maxResolveAfter)
		}
	}
	if len(r.Webhooks) > MaxWebhooks {
		return fieldErr("webhooks", "%d webhooks, more than %d", len(r.Webhooks), MaxWebhooks)
	}
	slices.Sort(r.Webhooks)
	r.Webhooks = slices.Compact(r.Webhooks)
	if r.Webhooks == nil {
		r.Webhooks = []string{}
	}
	return nil
}

// keepIf returns v when keep, and its zero otherwise.
func keepIf[T comparable](keep bool, v T) T {
	var zero T
	if keep {
		return v
	}
	return zero
}

// Silence keeps the alerts of a rule, of a member, or of a rule on a
// member, from being sent between Starts and Ends.
type Silence struct {
	ID        string    `json:"id"`
	Rule      string    `json:"rule,omitempty"`
	Node      string    `json:"node,omitempty"`
	Comment   string    `json:"comment,omitempty"`
	CreatedBy string    `json:"created_by"`
	Created   time.Time `json:"created"`
	Starts    time.Time `json:"starts"`
	Ends      time.Time `json:"ends"`
}

// active reports whether s holds at now.
func (s *Silence) active(now time.Time) bool { return !now.Before(s.Starts) && now.Before(s.Ends) }

// matches reports whether s is about a.
func (s *Silence) matches(a *Alert) bool {
	return (s.Rule == "" || s.Rule == a.Rule) && (s.Node == "" || s.Node == a.Node)
}

// SilenceInput is a silence as POST /api/alerts/silences takes it: its
// rule, its member or both, and its end, or how long it lasts from its
// start (now when left out).
type SilenceInput struct {
	Rule       string    `json:"rule"`
	Node       string    `json:"node"`
	Comment    string    `json:"comment"`
	Starts     time.Time `json:"starts,omitzero"`
	Ends       time.Time `json:"ends,omitzero"`
	DurationMs int64     `json:"duration_ms"`
}

// silence makes the silence of in, at now, by user.
func (in *SilenceInput) silence(user string, now time.Time) (Silence, error) {
	in.Comment = strings.TrimSpace(in.Comment)
	switch {
	case in.Rule == "" && in.Node == "":
		return Silence{}, fieldErr("rule", "a silence names a rule, a member, or both")
	case in.Rule != "" && !namePattern.MatchString(in.Rule) || len(in.Rule) > MaxNameBytes:
		return Silence{}, fieldErr("rule", "%q is not a rule's name", in.Rule)
	case in.Node != "" && !namePattern.MatchString(in.Node) || len(in.Node) > MaxNameBytes:
		return Silence{}, fieldErr("node", "%q is not a member's name", in.Node)
	case len(in.Comment) > maxAlertText || !printable(in.Comment):
		return Silence{}, fieldErr("comment", "a comment is at most %d bytes of printable characters", maxAlertText)
	}
	s := Silence{ID: newOpID(), Rule: in.Rule, Node: in.Node, Comment: in.Comment, CreatedBy: user, Created: now, Starts: in.Starts.UTC(), Ends: in.Ends.UTC()}
	if in.Starts.IsZero() {
		s.Starts = now
	}
	if in.Ends.IsZero() {
		if in.DurationMs <= 0 {
			return Silence{}, fieldErr("ends", "a silence ends: give its end or its duration")
		}
		s.Ends = s.Starts.Add(time.Duration(in.DurationMs) * time.Millisecond)
	}
	switch {
	case !s.Ends.After(s.Starts) || !s.Ends.After(now):
		return Silence{}, fieldErr("ends", "a silence ends after it starts, and after now")
	case s.Ends.Sub(now) > maxSilenceSpan:
		return Silence{}, fieldErr("ends", "a silence ends within %s", maxSilenceSpan)
	}
	return s, nil
}

// Alert is an alert: a rule's condition on a member (Node), or on a
// rollout (Subject), and how it went.
type Alert struct {
	ID       string `json:"id"`
	Rule     string `json:"rule"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Node     string `json:"node,omitempty"`
	Subject  string `json:"subject,omitempty"`
	State    string `json:"state"`
	Summary  string `json:"summary"`
	// Since is when its condition began; Fired when it fired; Resolved
	// when it resolved, and ResolvedWhy why, when not because its
	// condition is gone.
	Since       time.Time `json:"since"`
	Fired       time.Time `json:"fired,omitzero"`
	Resolved    time.Time `json:"resolved,omitzero"`
	ResolvedWhy string    `json:"resolved_why,omitempty"`
	// LastSent is when it was last sent to its webhooks, and Sends how
	// many times it was.
	LastSent time.Time `json:"last_sent,omitzero"`
	Sends    int       `json:"sends"`
	// SilencedBy is the silence that keeps it from being sent now; set in
	// answers only.
	SilencedBy string `json:"silenced_by,omitempty"`
}

// key is what tells alerts apart: its rule, member and subject.
func (a *Alert) key() string { return alertKey(a.Rule, a.Node, a.Subject) }

func alertKey(rule, node, subject string) string { return rule + "|" + node + "|" + subject }

// Webhook is where alerts are sent: its URL and secret, kept in the store
// and never answered or logged; its Format, the generic JSON or a chat
// service's; and for a chat service, Template, the line of each alert (or,
// for FormatCustom, the whole body).
type Webhook struct {
	Name         string    `json:"name"`
	URL          string    `json:"url"`
	Secret       string    `json:"secret,omitempty"`
	Format       string    `json:"format"`
	Template     string    `json:"template,omitempty"`
	TimeoutMs    int64     `json:"timeout_ms"`
	Disabled     bool      `json:"disabled,omitempty"`
	SendResolved bool      `json:"send_resolved"`
	Created      time.Time `json:"created,omitzero"`
	Updated      time.Time `json:"updated,omitzero"`
}

// The bounds of a webhook's timeout.
const (
	DefaultWebhookTimeout = 10 * time.Second
	minWebhookTimeout     = time.Second
	maxWebhookTimeout     = time.Minute
)

func (w *Webhook) timeout() time.Duration { return time.Duration(w.TimeoutMs) * time.Millisecond }

// WebhookInput is a webhook as the API takes it: a URL or secret left
// empty keeps the one kept, ClearSecret drops the secret, and SendResolved
// is true when left out.
type WebhookInput struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Secret       string `json:"secret"`
	ClearSecret  bool   `json:"clear_secret"`
	Format       string `json:"format"`
	Template     string `json:"template"`
	TimeoutMs    int64  `json:"timeout_ms"`
	Disabled     bool   `json:"disabled"`
	SendResolved *bool  `json:"send_resolved"`
}

// webhook makes the webhook of in, over old when it changes one.
func (in *WebhookInput) webhook(old *Webhook, now time.Time) (Webhook, error) {
	w := Webhook{Name: in.Name, URL: in.URL, Secret: in.Secret, Format: in.Format, Template: in.Template, TimeoutMs: in.TimeoutMs,
		Disabled: in.Disabled, SendResolved: in.SendResolved == nil || *in.SendResolved, Created: now, Updated: now}
	if old != nil {
		w.Created = old.Created
		if w.URL == "" {
			w.URL = old.URL
		}
		if w.Secret == "" && !in.ClearSecret {
			w.Secret = old.Secret
		}
	}
	if in.ClearSecret {
		w.Secret = ""
	}
	if w.Format == "" {
		w.Format = FormatJSON
	}
	if w.TimeoutMs == 0 {
		w.TimeoutMs = DefaultWebhookTimeout.Milliseconds()
	}
	return w, w.check()
}

// check checks w.
func (w *Webhook) check() error {
	switch {
	case w.Name == "" || len(w.Name) > MaxNameBytes || !namePattern.MatchString(w.Name):
		return fieldErr("name", "%q is not a name: 1 to %d letters, digits, '_', '.' and '-', starting with a letter or digit", w.Name, MaxNameBytes)
	case !slices.Contains(WebhookFormats, w.Format):
		return fieldErr("format", "%q is not a format: %s", w.Format, strings.Join(WebhookFormats, ", "))
	case len(w.Secret) > MaxWebhookSecret || !printable(w.Secret):
		return fieldErr("secret", "a secret is at most %d bytes of printable characters", MaxWebhookSecret)
	case len(w.Template) > MaxWebhookTemplate || !utf8Valid(w.Template):
		return fieldErr("template", "a template is at most %d bytes of UTF-8", MaxWebhookTemplate)
	case w.timeout() < minWebhookTimeout || w.timeout() > maxWebhookTimeout:
		return fieldErr("timeout_ms", "a webhook's timeout is %s to %s", minWebhookTimeout, maxWebhookTimeout)
	case w.Format == FormatCustom && w.Template == "":
		return fieldErr("template", "a custom webhook's template is its body: JSON with ${text}, ${status}, ${count}, ${manager} and ${alerts}")
	}
	if err := webhookURL(w.URL, w.Format); err != nil {
		return err
	}
	if w.Format == FormatCustom {
		if body := customBody(w.Template, sampleNotice()); !json.Valid(body) {
			return fieldErr("template", "the template does not make JSON once its ${...} are replaced: %s", clip(string(body), 120))
		}
	}
	return nil
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// WebhookView is a webhook as the API answers it: its URL by its scheme and
// host alone, and whether a secret is kept.
type WebhookView struct {
	Name         string        `json:"name"`
	URLHost      string        `json:"url_host"`
	SecretSet    bool          `json:"secret_set"`
	Format       string        `json:"format"`
	Template     string        `json:"template,omitempty"`
	TimeoutMs    int64         `json:"timeout_ms"`
	Disabled     bool          `json:"disabled,omitempty"`
	SendResolved bool          `json:"send_resolved"`
	Created      time.Time     `json:"created,omitzero"`
	Updated      time.Time     `json:"updated,omitzero"`
	Status       WebhookStatus `json:"status"`
}

// view returns w as the API answers it, without its URL's path and query,
// which a chat service's token is in, or its secret.
func (w *Webhook) view() WebhookView {
	return WebhookView{Name: w.Name, URLHost: urlHost(w.URL), SecretSet: w.Secret != "", Format: w.Format, Template: w.Template,
		TimeoutMs: w.TimeoutMs, Disabled: w.Disabled, SendResolved: w.SendResolved, Created: w.Created, Updated: w.Updated}
}

// alertsDoc is the store's section AlertsSection.
type alertsDoc struct {
	Version  int         `json:"version"`
	Rules    []AlertRule `json:"rules"`
	Webhooks []Webhook   `json:"webhooks"`
	Silences []Silence   `json:"silences"`
	// Active are the alerts firing; History those resolved, the oldest
	// first.
	Active  []Alert `json:"active"`
	History []Alert `json:"history"`
}

// alertConfig is what people change: the rules, the webhooks and the
// silences.
type alertConfig struct {
	rules    []AlertRule
	webhooks []Webhook
	silences []Silence
}

func (c *alertConfig) clone() alertConfig {
	out := alertConfig{webhooks: slices.Clone(c.webhooks), silences: slices.Clone(c.silences)}
	for _, r := range c.rules {
		out.rules = append(out.rules, r.clone())
	}
	return out
}

func (c *alertConfig) rule(name string) int {
	return slices.IndexFunc(c.rules, func(r AlertRule) bool { return r.Name == name })
}

func (c *alertConfig) webhook(name string) int {
	return slices.IndexFunc(c.webhooks, func(w Webhook) bool { return w.Name == name })
}

// alertsState is the alerts the manager keeps, under mu; wmu orders the
// writes of the section.
type alertsState struct {
	wmu sync.Mutex
	mu  sync.Mutex
	cfg alertConfig
	rev uint64
	// readOnly is why the section cannot be written, or "".
	readOnly string
	// active are the alerts firing, pending those whose condition has not
	// lasted its time yet, by key; history those resolved, the oldest
	// first.
	active  map[string]*Alert
	pending map[string]*Alert
	history []Alert
	// reloads follow each member's reloads, for reload_failed.
	reloads map[string]*reloadTrack
	// dirty is a change of the alerts not written yet.
	dirty bool
	// full is when the alerts were last found at MaxActiveAlerts, so that
	// it is logged once a while.
	full time.Time
	// drift is the members being read against their templates, and the
	// poll each was last read after.
	driftBusy map[string]bool
	driftPoll map[string]time.Time
	driftSem  chan struct{}
	// senders are the webhooks' queues, by name.
	senders map[string]*sender
	wake    chan struct{}
	done    chan struct{}
	evalMu  sync.Mutex
	// log is the manager's log.
	log func(format string, args ...any)
}

// loadAlerts reads the section, or takes the default rules when there is
// none. A section the manager cannot read, or one a newer fit wrote, leaves
// the alerts read-only.
func (m *Manager) loadAlerts() {
	al := &m.al
	al.active, al.pending, al.reloads = map[string]*Alert{}, map[string]*Alert{}, map[string]*reloadTrack{}
	al.driftBusy, al.driftPoll = map[string]bool{}, map[string]time.Time{}
	al.driftSem = make(chan struct{}, m.o.Settings.Concurrency())
	al.senders = map[string]*sender{}
	al.wake, al.done = make(chan struct{}, 1), make(chan struct{})
	al.log = m.o.Logf
	data := m.o.Store.Section(AlertsSection)
	if len(data) == 0 {
		al.cfg.rules = defaultRules()
		al.dirty = true
		return
	}
	var d alertsDoc
	if err := json.Unmarshal(data, &d); err != nil {
		al.readOnly = fmt.Sprintf("the store's section %q is not the alerts' document: %v; it is left as it is, and the default rules watch the members", AlertsSection, err)
		m.logf("cluster: %s", al.readOnly)
		al.cfg.rules = defaultRules()
		return
	}
	if d.Version > AlertsVersion {
		al.readOnly = fmt.Sprintf("the alerts were written by a newer fit (version %d; this fit writes %d): they are shown, and changed only by that fit", d.Version, AlertsVersion)
		m.logf("cluster: %s", al.readOnly)
	}
	for _, r := range d.Rules {
		if err := r.check(); err != nil {
			m.logf("cluster: the alert rule %q kept in the store is not used: %v", r.Name, err)
			continue
		}
		al.cfg.rules = append(al.cfg.rules, r)
	}
	al.cfg.webhooks = d.Webhooks
	al.cfg.silences = d.Silences
	for i := range d.Active {
		a := d.Active[i]
		al.active[a.key()] = &a
	}
	al.history = d.History
}

// marshalAlertsLocked returns the section as it is kept, with c's rules,
// webhooks and silences. al.mu is held.
func (al *alertsState) marshalLocked(c *alertConfig) []byte {
	d := alertsDoc{Version: AlertsVersion, Rules: c.rules, Webhooks: c.webhooks, Silences: c.silences, Active: []Alert{}, History: al.history}
	for _, a := range al.active {
		d.Active = append(d.Active, *a)
	}
	slices.SortFunc(d.Active, func(a, b Alert) int { return strings.Compare(a.key(), b.key()) })
	if d.Rules == nil {
		d.Rules = []AlertRule{}
	}
	if d.Webhooks == nil {
		d.Webhooks = []Webhook{}
	}
	if d.Silences == nil {
		d.Silences = []Silence{}
	}
	if d.History == nil {
		d.History = []Alert{}
	}
	data, _ := json.Marshal(d) // strings, numbers and times
	return data
}

// saveAlerts writes the section when the alerts changed since it was last
// written; a write that fails is logged, and tried again later. It writes a
// file: never on a fib worker.
func (m *Manager) saveAlerts() {
	al := &m.al
	al.wmu.Lock()
	defer al.wmu.Unlock()
	al.mu.Lock()
	if !al.dirty || al.readOnly != "" {
		al.mu.Unlock()
		return
	}
	data := al.marshalLocked(&al.cfg)
	al.dirty = false
	al.mu.Unlock()
	if err := m.o.Store.SetSection(AlertsSection, data); err != nil {
		m.logf("cluster: the alerts were not saved: %v", err)
		al.mu.Lock()
		al.dirty = true
		al.mu.Unlock()
	}
}

// changeAlerts runs fn on a copy of the rules, webhooks and silences,
// writes the section with it and uses it, then adds fn's audit entry, as
// user. It writes a file: on the console's pool.
func (m *Manager) changeAlerts(user string, fn func(c *alertConfig, now time.Time) (AuditEntry, error)) error {
	al := &m.al
	al.wmu.Lock()
	defer al.wmu.Unlock()
	now := m.now().UTC()
	al.mu.Lock()
	if al.readOnly != "" {
		al.mu.Unlock()
		return errReadOnly(al.readOnly)
	}
	c := al.cfg.clone()
	al.mu.Unlock()
	entry, err := fn(&c, now)
	if err != nil {
		return err
	}
	c.silences = pruneSilences(c.silences, now)
	al.mu.Lock()
	data := al.marshalLocked(&c)
	al.mu.Unlock()
	if err := m.o.Store.SetSection(AlertsSection, data); err != nil {
		return fmt.Errorf("saving the alerts: %w", err)
	}
	al.mu.Lock()
	al.cfg = c
	al.rev++
	al.dirty = false
	al.mu.Unlock()
	m.syncSenders()
	entry.At, entry.User = now, user
	if err := m.appendAudit([]AuditEntry{entry}); err != nil {
		m.logf("cluster: the audit entry of %s was not written: %v", entry.Action, err)
	}
	m.logf("cluster: %q: %s %s", user, entry.Action, entry.Detail)
	m.pokeAlerts()
	return nil
}

// pruneSilences drops the silences that ended more than silenceKept ago.
func pruneSilences(list []Silence, now time.Time) []Silence {
	return slices.DeleteFunc(list, func(s Silence) bool { return now.Sub(s.Ends) > silenceKept })
}

// The actions an AuditEntry records of alerts.
const (
	ActionAlertRuleAdd       = "alert.rule.add"
	ActionAlertRuleUpdate    = "alert.rule.update"
	ActionAlertRuleRemove    = "alert.rule.remove"
	ActionAlertWebhookAdd    = "alert.webhook.add"
	ActionAlertWebhookUpdate = "alert.webhook.update"
	ActionAlertWebhookRemove = "alert.webhook.remove"
	ActionAlertWebhookTest   = "alert.webhook.test"
	ActionAlertSilenceAdd    = "alert.silence.add"
	ActionAlertSilenceExpire = "alert.silence.expire"
)

// checkRefs checks r's group and webhooks against the manager's.
func (m *Manager) checkRefs(r *AlertRule, c *alertConfig) error {
	if r.Group != "" {
		m.mu.Lock()
		ok := groupIndex(&m.doc, r.Group) >= 0
		m.mu.Unlock()
		if !ok {
			return fieldErr("group", "no group is named %q", r.Group)
		}
	}
	for _, w := range r.Webhooks {
		if c.webhook(w) < 0 {
			return fieldErr("webhooks", "no webhook is named %q", w)
		}
	}
	return nil
}

// describeRule says what r watches, for the audit log.
func describeRule(r *AlertRule) string {
	s := r.Kind + " for " + r.forDur().String()
	if r.Group != "" {
		s += " on the group " + r.Group
	}
	if r.Disabled {
		s += ", disabled"
	}
	return s
}

// AddAlertRule adds the rule in, as user.
func (m *Manager) AddAlertRule(user string, in *AlertRule) (AlertRule, error) {
	r := in.clone()
	err := m.changeAlerts(user, func(c *alertConfig, now time.Time) (AuditEntry, error) {
		if err := r.check(); err != nil {
			return AuditEntry{}, err
		}
		if err := m.checkRefs(&r, c); err != nil {
			return AuditEntry{}, err
		}
		if c.rule(r.Name) >= 0 {
			return AuditEntry{}, errConflict("a rule is already named " + strconv.Quote(r.Name))
		}
		if len(c.rules) >= MaxAlertRules {
			return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d rules at most", MaxAlertRules))
		}
		r.Created, r.Updated = now, now
		c.rules = append(c.rules, r)
		return AuditEntry{Action: ActionAlertRuleAdd, Detail: r.Name + ": " + describeRule(&r)}, nil
	})
	return r, err
}

// UpdateAlertRule replaces the rule name with in, which may rename it, as
// user.
func (m *Manager) UpdateAlertRule(user, name string, in *AlertRule) (AlertRule, error) {
	r := in.clone()
	if r.Name == "" {
		r.Name = name
	}
	err := m.changeAlerts(user, func(c *alertConfig, now time.Time) (AuditEntry, error) {
		i := c.rule(name)
		if i < 0 {
			return AuditEntry{}, errNoAlert("no rule is named " + strconv.Quote(name))
		}
		if err := r.check(); err != nil {
			return AuditEntry{}, err
		}
		if err := m.checkRefs(&r, c); err != nil {
			return AuditEntry{}, err
		}
		if r.Name != name && c.rule(r.Name) >= 0 {
			return AuditEntry{}, errConflict("a rule is already named " + strconv.Quote(r.Name))
		}
		r.Created, r.Updated = c.rules[i].Created, now
		c.rules[i] = r
		return AuditEntry{Action: ActionAlertRuleUpdate, Detail: name + ": " + describeRule(&r)}, nil
	})
	return r, err
}

// RemoveAlertRule removes the rule name, as user; its alerts resolve.
func (m *Manager) RemoveAlertRule(user, name string) error {
	return m.changeAlerts(user, func(c *alertConfig, _ time.Time) (AuditEntry, error) {
		i := c.rule(name)
		if i < 0 {
			return AuditEntry{}, errNoAlert("no rule is named " + strconv.Quote(name))
		}
		c.rules = slices.Delete(c.rules, i, i+1)
		return AuditEntry{Action: ActionAlertRuleRemove, Detail: name}, nil
	})
}

// errNoAlert is a rule, a webhook or a silence there is none of.
type errNoAlert string

func (e errNoAlert) Error() string { return string(e) }

// AddWebhook adds the webhook in, as user.
func (m *Manager) AddWebhook(user string, in *WebhookInput) (WebhookView, error) {
	var out WebhookView
	err := m.changeAlerts(user, func(c *alertConfig, now time.Time) (AuditEntry, error) {
		w, err := in.webhook(nil, now)
		if err != nil {
			return AuditEntry{}, err
		}
		if c.webhook(w.Name) >= 0 {
			return AuditEntry{}, errConflict("a webhook is already named " + strconv.Quote(w.Name))
		}
		if len(c.webhooks) >= MaxWebhooks {
			return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d webhooks at most", MaxWebhooks))
		}
		c.webhooks = append(c.webhooks, w)
		out = w.view()
		return AuditEntry{Action: ActionAlertWebhookAdd, Detail: w.Name + ": " + w.Format + " to " + out.URLHost}, nil
	})
	return out, err
}

// UpdateWebhook replaces the webhook name with in, which may rename it, as
// user; the rules that name it follow a rename.
func (m *Manager) UpdateWebhook(user, name string, in *WebhookInput) (WebhookView, error) {
	var out WebhookView
	err := m.changeAlerts(user, func(c *alertConfig, now time.Time) (AuditEntry, error) {
		i := c.webhook(name)
		if i < 0 {
			return AuditEntry{}, errNoAlert("no webhook is named " + strconv.Quote(name))
		}
		if in.Name == "" {
			in.Name = name
		}
		w, err := in.webhook(&c.webhooks[i], now)
		if err != nil {
			return AuditEntry{}, err
		}
		if w.Name != name && c.webhook(w.Name) >= 0 {
			return AuditEntry{}, errConflict("a webhook is already named " + strconv.Quote(w.Name))
		}
		changed := webhookChanges(&c.webhooks[i], &w)
		c.webhooks[i] = w
		for j := range c.rules {
			for k, n := range c.rules[j].Webhooks {
				if n == name {
					c.rules[j].Webhooks[k] = w.Name
				}
			}
			slices.Sort(c.rules[j].Webhooks)
		}
		out = w.view()
		return AuditEntry{Action: ActionAlertWebhookUpdate, Detail: name + ": " + changed}, nil
	})
	return out, err
}

// webhookChanges names what changed from a to b; the URL and the secret by
// name only.
func webhookChanges(a, b *Webhook) string {
	var out []string
	add := func(changed bool, name string) {
		if changed {
			out = append(out, name)
		}
	}
	add(a.Name != b.Name, "name")
	add(a.URL != b.URL, "url")
	add(a.Secret != b.Secret, "secret")
	add(a.Format != b.Format, "format")
	add(a.Template != b.Template, "template")
	add(a.TimeoutMs != b.TimeoutMs, "timeout")
	add(a.Disabled != b.Disabled, "disabled")
	add(a.SendResolved != b.SendResolved, "send_resolved")
	if len(out) == 0 {
		return "nothing"
	}
	return strings.Join(out, ", ")
}

// RemoveWebhook removes the webhook name, as user; one a rule names is
// refused, as the rule would then send to every webhook.
func (m *Manager) RemoveWebhook(user, name string) error {
	return m.changeAlerts(user, func(c *alertConfig, _ time.Time) (AuditEntry, error) {
		i := c.webhook(name)
		if i < 0 {
			return AuditEntry{}, errNoAlert("no webhook is named " + strconv.Quote(name))
		}
		var users []string
		for _, r := range c.rules {
			if slices.Contains(r.Webhooks, name) {
				users = append(users, r.Name)
			}
		}
		if len(users) > 0 {
			return AuditEntry{}, errConflict("the rules " + strings.Join(users, ", ") + " send to the webhook: change them first")
		}
		c.webhooks = slices.Delete(c.webhooks, i, i+1)
		return AuditEntry{Action: ActionAlertWebhookRemove, Detail: name}, nil
	})
}

// AddSilence adds the silence in, as user.
func (m *Manager) AddSilence(user string, in *SilenceInput) (Silence, error) {
	var out Silence
	err := m.changeAlerts(user, func(c *alertConfig, now time.Time) (AuditEntry, error) {
		s, err := in.silence(user, now)
		if err != nil {
			return AuditEntry{}, err
		}
		c.silences = pruneSilences(c.silences, now)
		if len(c.silences) >= MaxSilences {
			// The oldest that ended goes first; one only of those that
			// still hold is refused.
			i := slices.IndexFunc(c.silences, func(s Silence) bool { return !now.Before(s.Ends) })
			if i < 0 {
				return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d silences at most", MaxSilences))
			}
			c.silences = slices.Delete(c.silences, i, i+1)
		}
		c.silences = append(c.silences, s)
		out = s
		return AuditEntry{Action: ActionAlertSilenceAdd, Node: s.Node, Detail: describeSilence(&s)}, nil
	})
	return out, err
}

func describeSilence(s *Silence) string {
	what := "every rule"
	if s.Rule != "" {
		what = "the rule " + s.Rule
	}
	if s.Node != "" {
		what += " on " + s.Node
	}
	return clip(s.ID+": "+what+" from "+s.Starts.Format(time.RFC3339)+" to "+s.Ends.Format(time.RFC3339)+commentOf(s.Comment), 512)
}

func commentOf(c string) string {
	if c == "" {
		return ""
	}
	return " (" + c + ")"
}

// ExpireSilence ends the silence id now, as user; it is kept as past.
func (m *Manager) ExpireSilence(user, id string) (Silence, error) {
	var out Silence
	err := m.changeAlerts(user, func(c *alertConfig, now time.Time) (AuditEntry, error) {
		i := slices.IndexFunc(c.silences, func(s Silence) bool { return s.ID == id })
		if i < 0 {
			return AuditEntry{}, errNoAlert("no silence " + strconv.Quote(id) + " is kept")
		}
		s := &c.silences[i]
		if now.Before(s.Ends) {
			s.Ends = now
		}
		if s.Starts.After(s.Ends) {
			s.Starts = s.Ends
		}
		out = *s
		return AuditEntry{Action: ActionAlertSilenceExpire, Node: s.Node, Detail: describeSilence(s)}, nil
	})
	return out, err
}
