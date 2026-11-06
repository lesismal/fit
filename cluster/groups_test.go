package cluster

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

func TestSelector(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "",
		"  ":                         "",
		"env=prod":                   "env=prod",
		" env == prod , region":      "env=prod, region",
		"tier!=db,!canary":           "tier!=db, !canary",
		"app.kubernetes.io/name=fit": "app.kubernetes.io/name=fit",
		"env=":                       "env=",
		"a=b,c!=d,e,!f,g==h":         "a=b, c!=d, e, !f, g=h",
	} {
		sel, err := ParseSelector(in)
		if err != nil || sel.String() != want {
			t.Errorf("%q: %q %v, want %q", in, sel.String(), err, want)
		}
	}
	for _, in := range []string{"a,", ",a", "a,,b", "-a", "a=-b", "!a=b", "a b", strings.Repeat("a,", 33) + "a",
		strings.Repeat("x", MaxSelectorBytes+1), "k=" + strings.Repeat("v", MaxLabelValue+1)} {
		if _, err := ParseSelector(in); err == nil {
			t.Errorf("%q parsed", in)
		}
	}
	labels := map[string]string{"env": "prod", "region": "eu", "empty": ""}
	for in, want := range map[string]bool{
		"":                    false,
		"env=prod":            true,
		"env=dev":             false,
		"env!=dev":            true,
		"env!=prod":           false,
		"missing!=x":          true,
		"region":              true,
		"missing":             false,
		"!missing":            true,
		"!env":                false,
		"env=prod, region=eu": true,
		"env=prod, region=us": false,
		"empty=":              true,
		"missing=":            false,
	} {
		sel, _ := ParseSelector(in)
		if got := sel.Matches(labels); got != want {
			t.Errorf("%q matches %v, want %v", in, got, want)
		}
	}
}

func TestGroupCheck(t *testing.T) {
	now := time.Now()
	ok := GroupInput{Name: "g", Selector: "env=prod", Members: []string{" b", "a", "a", ""}, Variables: map[string]string{"region": "eu", "_x1": "multi\nline"}}
	g, err := ok.group(nil, now, 3)
	if err != nil || !slices.Equal(g.Members, []string{"a", "b"}) || g.Selector != "env=prod" {
		t.Fatalf("%+v %v", g, err)
	}
	old := Group{Created: now.Add(-time.Hour)}
	if g, _ := ok.group(&old, now, 3); !g.Created.Equal(old.Created) || !g.Updated.Equal(now) {
		t.Errorf("%+v", g)
	}
	big := map[string]string{}
	for i := range MaxVariables + 1 {
		big["v"+strconv.Itoa(i)] = ""
	}
	for field, in := range map[string]GroupInput{
		"name":        {Name: "bad name", Selector: "a"},
		"description": {Name: "g", Selector: "a", Description: strings.Repeat("x", MaxDescriptionBytes+1)},
		"selector":    {Name: "g"},
		"members":     {Name: "g", Members: []string{"a", "b", "c", "d"}},
		"variables":   {Name: "g", Selector: "a", Variables: map[string]string{"1x": "v"}},
	} {
		if _, err := in.group(nil, now, 3); err == nil || err.(*FieldError).Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	for _, in := range []GroupInput{
		{Name: "g", Selector: "a,"},
		{Name: "g", Members: []string{"bad name"}},
		{Name: "g", Selector: "a", Variables: big},
		{Name: "g", Selector: "a", Variables: map[string]string{"v": strings.Repeat("x", MaxVariableValue+1)}},
		{Name: "g", Selector: "a", Variables: map[string]string{"v": "a\x00b"}},
	} {
		if _, err := in.group(nil, now, 3); err == nil {
			t.Errorf("%+v taken", in)
		}
	}
	if got := groupChanges(&Group{}, &Group{}); got != "nothing" {
		t.Error(got)
	}
	a, b := Group{Name: "a", Description: "d", Selector: "s", Members: []string{"m"}, Variables: map[string]string{"v": "1"}}, Group{Name: "b"}
	if got := groupChanges(&a, &b); got != "name, description, selector, members, variables" {
		t.Error(got)
	}
	if got := describeGroup(&a); got != "selector s; members m; variables v" {
		t.Error(got)
	}
}

// TestGroupsAPI adds, reads, changes and removes groups through the console
// module: a selector's members follow their labels' changes, a member by
// name follows its renames and leaves with its removal, and every change is
// audited.
func TestGroupsAPI(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxNodes: 4})
	s := f.serve()
	fm := newFakeMember(t, false)
	call := func(method, path, body string, want int, header ...string) string {
		t.Helper()
		code, got, _ := s.do(method, "/api/m/cluster"+path, body, header...)
		if code != want {
			t.Fatalf("%s %s: %d %s", method, path, code, got)
		}
		return got
	}
	add := func(name string, labels map[string]string) {
		in := consoleInput(name, fm.srv.URL)
		in.Labels = labels
		f.add(in)
	}
	add("a", map[string]string{"env": "prod", "region": "eu"})
	add("b", map[string]string{"env": "prod"})
	add("c", map[string]string{"env": "dev"})
	view := func(name string) GroupView {
		t.Helper()
		var v GroupView
		if err := json.Unmarshal([]byte(call("GET", "/groups/"+name, "", 200)), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	call("POST", "/groups", `{"name":"prod","selector":"env=prod","variables":{"region":"eu"}}`, 201)
	call("POST", "/groups", `{"name":"prod","selector":"env=prod"}`, 409)
	got := call("POST", "/groups", `{"name":"named","members":["c","ghost"],"description":"by name"}`, 201)
	if !strings.Contains(got, `"nodes":["c"]`) || !strings.Contains(got, `"missing":["ghost"]`) {
		t.Errorf("%s", got)
	}
	call("POST", "/groups", `{"name":"both","selector":"region=eu","members":["c"]}`, 201)
	if got := call("POST", "/groups", `{"name":"x","selector":"env=,"}`, 400); !strings.Contains(got, `"field":"selector"`) {
		t.Errorf("%s", got)
	}
	call("POST", "/groups", `{"name":"x"}`, 400)
	call("POST", "/groups", `{`, 400)
	call("POST", "/groups", `{"name":"x","description":"`+strings.Repeat("x", MaxBodyBytes)+`"}`, 413)

	if v := view("prod"); !slices.Equal(v.Nodes, []string{"a", "b"}) || v.Variables["region"] != "eu" || len(v.Missing) != 0 {
		t.Errorf("%+v", v)
	}
	if got := call("GET", "/groups/prod", "", 200); !strings.Contains(got, `"states":[{"name":"a"`) {
		t.Errorf("%s", got)
	}
	if v := view("both"); !slices.Equal(v.Nodes, []string{"a", "c"}) {
		t.Errorf("%+v", v)
	}
	if st := f.state("a"); !slices.Equal(st.Groups, []string{"both", "prod"}) {
		t.Errorf("a's groups: %v", st.Groups)
	}

	// A label's change moves members in and out.
	b := consoleInput("b", fm.srv.URL)
	b.Labels = map[string]string{"env": "dev"}
	if _, err := f.m.Update("tester", "b", b); err != nil {
		t.Fatal(err)
	}
	c := consoleInput("c", fm.srv.URL)
	c.Labels = map[string]string{"env": "prod"}
	if _, err := f.m.Update("tester", "c", c); err != nil {
		t.Fatal(err)
	}
	if v := view("prod"); !slices.Equal(v.Nodes, []string{"a", "c"}) {
		t.Errorf("after the labels changed: %+v", v)
	}
	if st := f.state("b"); len(st.Groups) != 0 {
		t.Errorf("b's groups: %v", st.Groups)
	}

	// A member listed by name follows its rename, and leaves when removed.
	c.Name = "c2"
	if _, err := f.m.Update("tester", "c", c); err != nil {
		t.Fatal(err)
	}
	if v := view("named"); !slices.Equal(v.Members, []string{"c2", "ghost"}) || !slices.Equal(v.Nodes, []string{"c2"}) {
		t.Errorf("after the rename: %+v", v)
	}
	if err := f.m.Remove("tester", "c2"); err != nil {
		t.Fatal(err)
	}
	if v := view("named"); !slices.Equal(v.Members, []string{"ghost"}) || len(v.Nodes) != 0 {
		t.Errorf("after the removal: %+v", v)
	}

	// The preview of a form.
	if got := call("POST", "/select", `{"selector":"env","members":["b","nobody"]}`, 200); strings.TrimSpace(got) != `{"missing":["nobody"],"nodes":["a","b"]}` {
		t.Errorf("%s", got)
	}
	call("POST", "/select", `{"selector":"!"}`, 400)
	call("POST", "/select", `{`, 400)
	call("POST", "/select", `{"selector":"`+strings.Repeat("x", MaxBodyBytes)+`"}`, 413)

	// Changes take the document's ETag.
	code, list, h := s.do("GET", "/api/m/cluster/groups", "")
	if code != 200 || h.Get("ETag") == "" || !strings.Contains(list, `"max_groups":256`) || !strings.Contains(list, `"name":"both"`) {
		t.Fatalf("%d %s", code, list)
	}
	call("PUT", "/groups/prod", `{"selector":"env=prod","variables":{"region":"us"}}`, 412, "If-Match", `"999"`)
	got = call("PUT", "/groups/prod", `{"selector":"env=prod","variables":{"region":"us"}}`, 200, "If-Match", h.Get("ETag"))
	if !strings.Contains(got, `"region":"us"`) || !strings.Contains(got, `"name":"prod"`) {
		t.Errorf("%s", got)
	}
	call("PUT", "/groups/prod", `{"name":"live","selector":"env=prod"}`, 200)
	call("PUT", "/groups/live", `{"name":"both","selector":"env=prod"}`, 409)
	call("PUT", "/groups/live", `{"selector":"!"}`, 400)
	call("PUT", "/groups/nothing", `{"selector":"a"}`, 404)
	call("PUT", "/groups/live", `{`, 400)
	call("GET", "/groups/prod", "", 404)
	call("GET", "/groups/live/x", "", 404)
	call("PATCH", "/groups/live", `{}`, 405)
	call("DELETE", "/groups/live", "", 412, "If-Match", `"999"`)
	call("DELETE", "/groups/live", "", 200)
	call("DELETE", "/groups/live", "", 404)

	var actions []string
	for _, e := range f.m.Audit() {
		if e.Group != "" {
			actions = append(actions, e.Action+" "+e.Group)
		}
	}
	if want := "group.add prod, group.add named, group.add both, group.update prod, group.update prod, group.remove live"; strings.Join(actions, ", ") != want {
		t.Errorf("audit: %s", strings.Join(actions, ", "))
	}

	// The groups are kept in the store, and read back.
	f.m.Close()
	f.start()
	if gs := f.m.Groups(); len(gs) != 2 || gs[0].Name != "both" || gs[1].Name != "named" {
		t.Errorf("%+v", gs)
	}
}

// TestGroupsBounds keeps the groups bounded, and leaves out a group kept in
// the store that does not check.
func TestGroupsBounds(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	for i := range MaxGroups {
		if _, err := f.m.AddGroup("tester", &GroupInput{Name: "g" + strconv.Itoa(i), Selector: "env"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.m.AddGroup("tester", &GroupInput{Name: "one-more", Selector: "env"}); err == nil {
		t.Error("a group past MaxGroups")
	}
	if _, err := f.m.UpdateGroup("tester", "g0", &GroupInput{Selector: ""}); err == nil {
		t.Error("a group of nothing")
	}
	if err := f.m.RemoveGroup("tester", "nothing"); err == nil {
		t.Error("no such group")
	}
	if _, _, err := f.m.Select("a,", nil); err == nil {
		t.Error("a bad selector")
	}

	f.m.Close()
	f.store.sections[Section] = []byte(`{"version":2,"members":[],"groups":[{"name":"ok","selector":"a"},{"name":"bad","selector":"a,"}]}`)
	f.start()
	if !strings.Contains(f.log.String(), `the group "bad" kept in the store is left out`) {
		t.Errorf("%s", f.log.String())
	}
	f.add(&Input{Name: "m", URL: "http://127.0.0.1:1", Access: AccessAdminAPI, Token: fakeToken, Labels: map[string]string{"a": ""}})
	if st := f.state("m"); !slices.Equal(st.Groups, []string{"ok"}) {
		t.Errorf("%v", st.Groups)
	}
}
