package cluster

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// TestAuditEntries: each change of the manager's leaves its entry, with
// its user, members, outcome, the document's revisions before and after,
// and its diff (credentials as set or not, secrets masked) and digest; a
// test of a member too.
func TestAuditEntries(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	in := consoleInput("a", downURL(t))
	in.Labels = map[string]string{"env": "prod"}
	f.add(in)
	in.Password, in.Labels = "a new one", map[string]string{"env": "dev"}
	if _, err := f.m.Update("ada", "a", in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.AddGroup("ada", &GroupInput{Name: "dev", Selector: "env=dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpdateGroup("ada", "dev", &GroupInput{Selector: "env=dev", Description: "the dev ones"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.AddTemplate("ada", &TemplateInput{Name: "t", Groups: []string{"dev"}, Config: json.RawMessage(`{"log":{"level":"info"}}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.UpdateTemplate("ada", "t", &TemplateInput{Groups: []string{"dev"}, Config: json.RawMessage(`{"log":{"level":"debug"}}`)}); err != nil {
		t.Fatal(err)
	}
	if code, got, _ := s.do("POST", "/api/m/cluster/nodes/a/test", ""); code != 200 {
		t.Fatalf("%d %s", code, got)
	}
	// The test's entry is written apart from the changes: wait for it.
	waitFor(t, "the test's entry", func() bool { return len(f.m.AuditWhere(AuditFilter{Action: ActionTest})) == 1 })
	if err := f.m.RemoveTemplate("ada", "t"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.RemoveGroup("ada", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Remove("ada", "a"); err != nil {
		t.Fatal(err)
	}
	all := f.m.Audit()
	var acts []string
	for _, e := range all {
		acts = append(acts, e.Action)
	}
	want := "node.add,node.update,group.add,group.update,template.add,template.update,node.test,template.remove,group.remove,node.remove"
	if got := strings.Join(acts, ","); got != want {
		t.Fatalf("%s", got)
	}
	byAction := map[string]AuditEntry{}
	for _, e := range all {
		byAction[e.Action] = e
	}
	add, upd := byAction[ActionAdd], byAction[ActionUpdate]
	if add.User != "tester" || add.Outcome != AuditOK || add.Before != "r0" || add.After != "r1" || !strings.Contains(add.Diff, `+  "password_set": true,`) ||
		!strings.HasPrefix(add.Digest, "sha256:") || strings.Contains(add.Diff, fakePassword) {
		t.Errorf("%+v", add)
	}
	if upd.Before != "r1" || upd.After != "r2" || !strings.Contains(upd.Diff, `-    "env": "prod"`) || !strings.Contains(upd.Diff, `+    "env": "dev"`) ||
		strings.Contains(upd.Diff, "a new one") || !strings.Contains(upd.Detail, "password") {
		t.Errorf("%+v", upd)
	}
	if g := byAction[ActionGroupUpdate]; !strings.Contains(g.Diff, `+  "description": "the dev ones",`) || strings.Join(g.Nodes, ",") != "a" {
		t.Errorf("%+v", g)
	}
	if tu := byAction[ActionTemplateUpdate]; !strings.Contains(tu.Diff, `-    "level": "info"`) || !strings.Contains(tu.Diff, `+    "level": "debug"`) {
		t.Errorf("%+v", tu)
	}
	if rm := byAction[ActionRemove]; !strings.Contains(rm.Diff, `-  "name": "a",`) || rm.Node != "a" {
		t.Errorf("%+v", rm)
	}
	if te := byAction[ActionTest]; te.User != "admin" || te.Node != "a" || te.Outcome != AuditFailed || !strings.Contains(te.Detail, "the member as kept; unreachable") {
		t.Errorf("%+v", te)
	}
	// A diff past MaxAuditDiff is cut, its digest the whole's.
	var e AuditEntry
	e.setDiff(strings.Repeat("a\n", MaxAuditDiff), "", "x", "y")
	if !e.Clipped || len(e.Diff) > MaxAuditDiff+4 || e.Digest == "" {
		t.Errorf("%d %v", len(e.Diff), e.Clipped)
	}
	e = AuditEntry{}
	if e.setDiff("same\n", "same\n", "x", "y"); e.Diff != "" || e.Digest != "" {
		t.Errorf("%+v", e)
	}
}

// TestAuditFilterExport: the log read filtered by user, member, operation
// and time, and exported as JSON lines; it holds across a restart, within
// its bounds.
func TestAuditFilterExport(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	t0 := time.Now().UTC()
	f.add(consoleInput("a", downURL(t)))
	f.add(consoleInput("b", downURL(t)))
	if _, err := f.m.AddGroup("ada", &GroupInput{Name: "g", Members: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	t1 := time.Now().UTC()
	if err := f.m.Remove("ada", "b"); err != nil {
		t.Fatal(err)
	}
	read := func(q string) []AuditEntry {
		t.Helper()
		code, got, _ := s.do("GET", "/api/m/cluster/audit"+q, "")
		if code != 200 {
			t.Fatalf("%s: %d %s", q, code, got)
		}
		var b auditBody
		if err := json.Unmarshal([]byte(got), &b); err != nil {
			t.Fatal(err)
		}
		if b.Total != 4 || b.MaxEntries != MaxAudit || strings.Join(b.Users, ",") != "ada,tester" || len(b.Actions) != 3 {
			t.Errorf("%+v", b)
		}
		return b.Entries
	}
	names := func(es []AuditEntry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Action+":"+strings.Join(e.auditNodes(), "+"))
		}
		return strings.Join(out, ",")
	}
	for q, want := range map[string]string{
		"":                                      "node.add:a,node.add:b,group.add:a,node.remove:b",
		"?user=ada":                             "group.add:a,node.remove:b",
		"?node=a":                               "node.add:a,group.add:a",
		"?action=node":                          "node.add:a,node.add:b,node.remove:b",
		"?action=node.remove":                   "node.remove:b",
		"?action=nod":                           "",
		"?since=" + t1.Format(time.RFC3339Nano): "node.remove:b",
		"?until=" + t1.Format(time.RFC3339Nano): "node.add:a,node.add:b,group.add:a",
		"?since=" + t0.Add(-time.Minute).Format(time.RFC3339) + "&node=b&user=tester": "node.add:b",
	} {
		if got := names(read(q)); got != want {
			t.Errorf("%q: %s", q, got)
		}
	}
	if code, _, _ := s.do("GET", "/api/m/cluster/audit?since=yesterday", ""); code != 400 {
		t.Error("a bad time")
	}
	if code, _, _ := s.do("GET", "/api/m/cluster/audit?format=xml", ""); code != 400 {
		t.Error("a bad format")
	}
	// JSON lines, a file to save.
	code, got, h := s.do("GET", "/api/m/cluster/audit?format=jsonl&node=a", "")
	if code != 200 || h.Get("Content-Type") != "application/jsonl" || !strings.Contains(h.Get("Content-Disposition"), `filename="fit-cluster-audit-`) {
		t.Fatalf("%d %v", code, h)
	}
	sc, n := bufio.NewScanner(strings.NewReader(got)), 0
	for sc.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.At.IsZero() {
			t.Errorf("%v %s", err, sc.Text())
		}
		n++
	}
	if n != 2 {
		t.Errorf("%d lines: %s", n, got)
	}

	// Kept across a restart, and bounded: by entries and by bytes, the
	// oldest going first, however they are written.
	f.m.Close()
	f.start()
	if got := len(f.m.Audit()); got != 4 {
		t.Fatalf("%d entries after a restart", got)
	}
	big := AuditEntry{Action: ActionAPI, Node: "a", Detail: strings.Repeat("x", 8<<10), Diff: strings.Repeat("y", 8<<10)}
	batch := make([]AuditEntry, 300)
	for i := range batch {
		batch[i] = big
	}
	if err := f.m.appendAudit(batch); err != nil {
		t.Fatal(err)
	}
	log := f.m.Audit()
	size := 0
	for i := range log {
		size += auditSize(&log[i])
	}
	if size > MaxAuditBytes || len(log) < 100 || log[0].Action != ActionAPI {
		t.Errorf("%d entries of %d bytes", len(log), size)
	}
	f.m.Close()
	f.start()
	if got := len(f.m.Audit()); got != len(log) {
		t.Errorf("%d entries after a restart, not %d", got, len(log))
	}
}
