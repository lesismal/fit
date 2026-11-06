package assistant

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestAuditLog: entries are bounded by limits.max_audit_entries, numbered
// across restarts, picked by user, conversation and kind and paged; the
// store keeps them, and without one, or with one that cannot write, they
// are kept in memory.
func TestAuditLog(t *testing.T) {
	st := &memStore{}
	cfg := loadConfig(t, `{"limits": {"max_audit_entries": 3}}`)
	a := toolsAssistant(t, cfg, func(o *Options) { o.Settings, o.Console, o.Store = cfg.Admin.Assistant, cfg.Admin.Console, st })
	long := strings.Repeat("x", 4000)
	var calls []AuditCall
	for i := range 40 {
		calls = append(calls, AuditCall{Tool: fmt.Sprint("t", i), Args: long})
	}
	for i := range 5 {
		a.Audit(AuditEntry{User: []string{"ada", "bob"}[i%2], Kind: AuditChat, Conversation: fmt.Sprint("c", i), Outcome: ReasonAnswer, Calls: calls, Detail: long})
	}
	all, total := a.AuditLog(AuditFilter{})
	if total != 3 || len(all) != 3 || all[0].Seq != 5 || all[2].Seq != 3 || all[0].Source != SourcePanel || all[0].At == 0 ||
		len(all[0].Calls) != maxAuditCalls || all[0].MoreCalls != 8 || len(all[0].Calls[0].Args) > maxAuditArgs+4 || len(all[0].Detail) > maxAuditDetail+4 {
		t.Fatalf("%d %+v", total, all[0].Seq)
	}
	if e, _ := a.AuditLog(AuditFilter{User: "ada"}); len(e) != 2 || e[0].Conversation != "c4" {
		t.Errorf("%+v", e)
	}
	if e, _ := a.AuditLog(AuditFilter{Conversation: "c3"}); len(e) != 1 || e[0].User != "bob" {
		t.Errorf("%+v", e)
	}
	if e, _ := a.AuditLog(AuditFilter{Before: 5, Limit: 1}); len(e) != 1 || e[0].Seq != 4 {
		t.Errorf("%+v", e)
	}
	if e, _ := a.AuditLog(AuditFilter{Kind: AuditApply, Limit: 9999}); len(e) != 0 {
		t.Errorf("%+v", e)
	}
	a.Close()
	if !strings.Contains(string(st.Section(auditSection)), `"seq":5`) {
		t.Fatalf("%s", st.Section(auditSection))
	}

	// A restart: the entries and their numbers hold, within a bound now
	// smaller.
	cfg2 := loadConfig(t, `{"limits": {"max_audit_entries": 2}}`)
	b := toolsAssistant(t, cfg2, func(o *Options) { o.Settings, o.Console, o.Store = cfg2.Admin.Assistant, cfg2.Admin.Console, st })
	b.Audit(AuditEntry{User: "ada", Kind: AuditDiscard, Outcome: OutcomeDiscarded})
	if e, total := b.AuditLog(AuditFilter{}); total != 2 || e[0].Seq != 6 || e[1].Seq != 5 {
		t.Errorf("%d %+v", total, e)
	}

	// A section that does not load is started again; a store that cannot
	// write keeps them in memory.
	var logs []string
	bad := &memStore{sec: map[string][]byte{auditSection: []byte(`[1]`)}, fail: true}
	c := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.Store = bad
		o.Logf = func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	})
	c.Audit(AuditEntry{User: "ada", Kind: AuditChat})
	c.Close()
	if text := strings.Join(logs, "\n"); !strings.Contains(text, "does not load") || !strings.Contains(text, "kept in memory only") {
		t.Error(text)
	}
	// Without a store.
	d := toolsAssistant(t, &config.Config{})
	d.Audit(AuditEntry{User: "ada", Kind: AuditChat})
	d.Close()
	if e, _ := d.AuditLog(AuditFilter{}); len(e) != 1 {
		t.Error(e)
	}
}

// TestAuditEndpoint: GET /api/audit answers the log, filtered, with the
// console's users and its bound.
func TestAuditEndpoint(t *testing.T) {
	f := startProposals(t, `"limits": {"max_audit_entries": 10}`)
	f.a.Audit(AuditEntry{User: "bob", Kind: AuditChat, Conversation: "x", Outcome: ReasonAnswer})
	f.a.Audit(AuditEntry{User: "ada", Kind: AuditApply, Conversation: "y", Outcome: OutcomeApplied, Version: 4})
	status, body := f.do("GET", "/api/m/ai/audit?user=ada&limit=5", "")
	if status != 200 || !strings.Contains(body, `"version":4`) || strings.Contains(body, `"user":"bob"`) || !strings.Contains(body, `"users":["ada","bob"]`) ||
		!strings.Contains(body, `"max":10`) || !strings.Contains(body, `"kept":true`) || !strings.Contains(body, `"total":2`) {
		t.Errorf("%d %s", status, body)
	}
	if e := f.audit("?conversation=x&before=2"); len(e) != 1 || e[0].User != "bob" {
		t.Errorf("%+v", e)
	}
}
