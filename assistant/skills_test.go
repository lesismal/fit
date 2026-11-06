package assistant

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const goodSkill = "\xef\xbb\xbf---\r\n" + `name: my-skill
title: My skill
title_zh: 我的技能
description: Does what I need.
description_zh: 做我需要的事。
tools: [config_get, config_propose]
model: m/other-model
inputs:
  - name: host
    description: The host.
    required: true
  - name: port
    required: false
examples:
  - Do it for example.com.
---

Read the routes, then do it.
`

// TestParseSkill: a skill's Markdown is read whole; front matter that is
// not a skill's is refused with the line it is about.
func TestParseSkill(t *testing.T) {
	sk, err := ParseSkill([]byte(goodSkill))
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "my-skill" || sk.Title != "My skill" || sk.TitleZh != "我的技能" || sk.DescriptionZh != "做我需要的事。" ||
		!slices.Equal(sk.Tools, []string{"config_get", "config_propose"}) || sk.Model != "m/other-model" || len(sk.Inputs) != 2 ||
		!sk.Inputs[0].Required || sk.Inputs[1].Required || sk.Inputs[0].Description != "The host." || len(sk.Examples) != 1 ||
		sk.Prompt != "Read the routes, then do it." || strings.Contains(sk.Source, "\r") || sk.input("port") == nil || sk.input("x") != nil {
		t.Errorf("%+v", sk)
	}
	// Lists left empty, and tools left out.
	sk, err = ParseSkill([]byte("---\nname: a\ntitle: A\ndescription: d\ninputs:\nexamples:\ntools:\n---\np"))
	if err != nil || sk.Tools == nil || len(sk.Tools) != 0 || sk.Inputs != nil {
		t.Errorf("%+v %v", sk, err)
	}

	front := func(lines ...string) string { return "---\n" + strings.Join(lines, "\n") + "\n---\nThe prompt.\n" }
	base := []string{"name: a", "title: A", "description: d"}
	with := func(extra ...string) string { return front(append(slices.Clone(base), extra...)...) }
	for _, c := range []struct {
		src  string
		line int
		msg  string
	}{
		{"name: a\n---\np", 1, "starts with its front matter"},
		{"---\nname: a\n", 1, "no closing line"},
		{front(), 1, "the front matter is empty"},
		{front("# only a comment"), 1, "the front matter is empty"},
		{front("- a", "- b"), 2, "not a mapping"},
		{front("name: a", "title: [A", "description: d"), 3, "not YAML"},
		{front("name: a", "title: A: B"), 3, "not YAML: mapping values"},
		{front("name: a", "\ttitle: x"), 3, "not YAML"},
		{with("colour: red"), 5, `"colour" is not a key`},
		{with("[a]: b"), 5, "not a name"},
		{with("title: B"), 5, "title is given twice"},
		{front("name: a", "title: A"), 1, "has no description"},
		{front("name: a", "title:", "description: d"), 1, "has no title"},
		{front("name: Not-OK", "title: A", "description: d"), 2, "is not a skill's name"},
		{front("name: [a]", "title: A", "description: d"), 2, "name is not a text"},
		{with("title_zh: " + strings.Repeat("长", 50)), 5, "title_zh is 150 bytes"},
		{with("description_zh: |", "  one", "  two"), 5, "is one line"},
		{with("model: has space"), 5, "is not a model"},
		{with("tools: config_get"), 5, "tools is not a list"},
		{with("tools: [Config]"), 5, `"Config" is not a tool's name`},
		{with("tools:", "  - config_get", "  - config_get"), 7, "named twice"},
		{with("tools: [" + strings.Repeat("t,", 33) + "t]"), 5, "it has at most 32"},
		{with("tools: [[a]]"), 5, "item is not a text"},
		{with("examples: ['']"), 5, "an empty item"},
		{with("inputs: a"), 5, "inputs is not a list"},
		{with("inputs: [a]"), 5, "an input is a mapping"},
		{with("inputs:", "  - name: A"), 6, "is not an input's name"},
		{with("inputs:", "  - name: a", "    colour: red"), 7, `"colour" is not a key of an input`},
		{with("inputs:", "  - name: a", "    name: b"), 7, "name is given twice"},
		{with("inputs:", "  - name: a", "    required: yes"), 7, "required is true or false"},
		{with("inputs:", "  - description: x"), 6, "an input has no name"},
		{with("inputs:", "  - name: a", "  - name: a"), 7, "the input a is given twice"},
		{with("inputs: [" + strings.Repeat("{name: a},", 8) + "{name: b}]"), 5, "at most 8"},
		{with("inputs:", "  - name: a", "    description: [x]"), 7, "not a text"},
		{front("name: a", "title: &x A", "description: *x"), 4, "aliases are not taken"},
		{"---\nname: a\ntitle: A\ndescription: d\n---\n\n  \n", 5, "no prompt"},
		{"---\nname: a\n\xff\n---\np", 3, "not UTF-8"},
	} {
		_, err := ParseSkill([]byte(c.src))
		var se *SkillError
		if !errors.As(err, &se) || se.Line != c.line || !strings.Contains(se.Msg, c.msg) {
			t.Errorf("%q: %v (line %d), want line %d %q", c.src, err, func() int {
				if se != nil {
					return se.Line
				}
				return -1
			}(), c.line, c.msg)
		}
	}
	_, err = ParseSkill([]byte(strings.Repeat("x", MaxSkillBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "at most 32768") || strings.HasPrefix(err.Error(), "line") {
		t.Errorf("%v", err)
	}
	if e := (&SkillError{Line: 3, Msg: "m"}).Error(); e != "line 3: m" {
		t.Error(e)
	}
	// A YAML error that names no line is placed at the line that makes it.
	if err := yamlError("a: 1\nb: c: d", errors.New("yaml: mapping values are not allowed in this context"), 1); err.(*SkillError).Line != 3 {
		t.Errorf("%v", err)
	}
	if err := yamlError("a: 1", errors.New("yaml: something else"), 1); err.(*SkillError).Line != 2 {
		t.Errorf("%v", err)
	}
}

// TestSkillPrompt: the current skill's part of the system prompt names the
// inputs not given, the required ones as such.
func TestSkillPrompt(t *testing.T) {
	sk, err := ParseSkill([]byte(goodSkill))
	if err != nil {
		t.Fatal(err)
	}
	sys := systemPrompt(promptParts{level: "read", skill: &skillPart{skill: sk, inputs: []byte(`{"port":"80"}`), tools: []string{"config_get"},
		not: []string{"config_propose (needs the level propose)"}}})
	for _, want := range []string{"The current skill is my-skill (My skill)", "<skill name=\"my-skill\">\nRead the routes, then do it.\n</skill>",
		"- host (required): The host.", `"port":"80"`, "The tools you may call for it: config_get.", "config_propose (needs the level propose)"} {
		if !strings.Contains(sys, want) {
			t.Errorf("no %q in %s", want, sys)
		}
	}
	if strings.Contains(sys, "- port") {
		t.Error(sys)
	}
}

// TestBuiltinSkills: the plan's built-in skills are embedded, in its order,
// each with its words in English and Chinese, examples, and only tools the
// registry has.
func TestBuiltinSkills(t *testing.T) {
	list, err := builtinSkills()
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	for _, tool := range slices.Concat(readTools(), []*Tool{skillLoadTool()}, proposeTools(), pageTools()) {
		if err := reg.Add(tool); err != nil {
			t.Fatal(err)
		}
	}
	if len(list) != len(builtinOrder) {
		t.Fatalf("%d built-in skills", len(list))
	}
	for i, sk := range list {
		if sk.Name != builtinOrder[i] || sk.TitleZh == "" || sk.DescriptionZh == "" || len(sk.Examples) == 0 || len(sk.Tools) == 0 ||
			len(sk.Prompt) < 400 {
			t.Errorf("%s: %+v", sk.Name, sk)
		}
		for _, name := range sk.Tools {
			if reg.Tool(name) == nil || name == "ask_user" || name == "skill_load" {
				t.Errorf("%s names the tool %s", sk.Name, name)
			}
		}
	}
}
