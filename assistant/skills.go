package assistant

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// A skill is a named task the assistant runs: when it is for, the prompt
// that says how, the tools it may call (never wider than the user's level),
// the inputs it takes and the model it wants. A skill is written as
// Markdown: YAML front matter between two lines "---", then the prompt.
//
//	---
//	name: route-builder
//	title: Route builder
//	title_zh: 路由生成
//	description: A route, its matcher, handler and middleware from a sentence.
//	description_zh: 用一句话生成路由及其匹配、处理器与中间件。
//	tools: [config_get, schema_describe, config_validate, config_propose]
//	inputs:
//	  - name: host
//	    description: The host name the route answers.
//	model: anthropic/claude-sonnet-4-5
//	examples:
//	  - Send /api/* on example.com to 10.0.0.7:9000.
//	---
//	The prompt, in Markdown.
//
// The built-in skills are embedded (skills/*.md); users add theirs on the
// Skills page, kept in the store (skillstore.go).

// The bounds of a skill.
const (
	// MaxSkillBytes bounds a skill's Markdown, front matter and prompt.
	MaxSkillBytes = 32 << 10
	// MaxUserSkills bounds the skills users add.
	MaxUserSkills = 64

	maxSkillTools    = 32
	maxSkillInputs   = 8
	maxSkillExamples = 8
	maxSkillTitle    = 120
	maxSkillText     = 400
	maxSkillModel    = 128
	maxInputValue    = 2048
)

// Skill is a skill as its Markdown says it.
type Skill struct {
	// Name is how it is called, as /name: lower case letters, digits and
	// '-', starting with a letter.
	Name string `json:"name"`
	// Title and Description say what it is for, in English and, when
	// given, in Chinese; the model's catalog has the English description.
	Title         string `json:"title"`
	TitleZh       string `json:"title_zh,omitempty"`
	Description   string `json:"description"`
	DescriptionZh string `json:"description_zh,omitempty"`
	// Inputs are what it takes beside the user's words.
	Inputs []SkillInput `json:"inputs,omitempty"`
	// Tools are the tools it may call, narrowed again by the user's level;
	// ask_user is always among them.
	Tools []string `json:"tools"`
	// Model, when not "", is the model it runs with: "provider/model", or a
	// model of the default provider.
	Model string `json:"model,omitempty"`
	// Examples are requests it is for, shown to the user.
	Examples []string `json:"examples,omitempty"`
	// Prompt is how to do the task, the Markdown after the front matter.
	Prompt string `json:"prompt,omitempty"`
	// Source is the Markdown it was read from, which export gives back.
	Source string `json:"-"`
}

// SkillInput is an input a skill takes.
type SkillInput struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// SkillError is a skill's Markdown refused: the line of the file it is
// about, from 1, or 0 for the whole, and what is wrong.
type SkillError struct {
	Line int
	Msg  string
}

func (e *SkillError) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

var (
	skillName  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	inputName  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	modelName  = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`)
	yamlLineRe = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)
)

// ParseSkill reads a skill from its Markdown, refusing front matter that is
// not what a skill's is, with the line it is about.
func ParseSkill(data []byte) (*Skill, error) {
	if len(data) > MaxSkillBytes {
		return nil, &SkillError{Msg: fmt.Sprintf("the skill is %d bytes; a skill is at most %d", len(data), MaxSkillBytes)}
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	src := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if !utf8.ValidString(l) {
			return nil, &SkillError{Line: i + 1, Msg: "the line is not UTF-8"}
		}
	}
	if strings.TrimRight(lines[0], " \t") != "---" {
		return nil, &SkillError{Line: 1, Msg: "a skill starts with its front matter: a line \"---\", the YAML, and another \"---\""}
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, &SkillError{Line: 1, Msg: "the front matter has no closing line \"---\""}
	}
	front := strings.Join(lines[1:end], "\n")
	sk := &Skill{Tools: []string{}, Source: src}
	if err := sk.front(front); err != nil {
		return nil, err
	}
	sk.Prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if sk.Prompt == "" {
		return nil, &SkillError{Line: end + 1, Msg: "the skill has no prompt after its front matter"}
	}
	return sk, nil
}

// front reads the front matter, whose first line is the file's second.
func (sk *Skill) front(text string) error {
	const offset = 1
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return yamlError(text, err, offset)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return &SkillError{Line: 1, Msg: "the front matter is empty: it needs name, title and description"}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return &SkillError{Line: root.Line + offset, Msg: "the front matter is not a mapping of keys to values"}
	}
	at := func(n *yaml.Node, format string, args ...any) error {
		return &SkillError{Line: n.Line + offset, Msg: fmt.Sprintf(format, args...)}
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return at(k, "a key of the front matter is not a name")
		}
		if seen[k.Value] {
			return at(k, "%s is given twice", k.Value)
		}
		seen[k.Value] = true
		if v.Kind == yaml.AliasNode {
			return at(v, "%s: anchors and aliases are not taken", k.Value)
		}
		var err error
		switch k.Value {
		case "name":
			if sk.Name, err = text1(v, k.Value, 40, offset); err == nil && !skillName.MatchString(sk.Name) {
				err = at(v, "name %q is not a skill's name: lower case letters, digits and '-', starting with a letter, at most 40", sk.Name)
			}
		case "title":
			sk.Title, err = text1(v, k.Value, maxSkillTitle, offset)
		case "title_zh":
			sk.TitleZh, err = text1(v, k.Value, maxSkillTitle, offset)
		case "description":
			sk.Description, err = text1(v, k.Value, maxSkillText, offset)
		case "description_zh":
			sk.DescriptionZh, err = text1(v, k.Value, maxSkillText, offset)
		case "model":
			if sk.Model, err = text1(v, k.Value, maxSkillModel, offset); err == nil && sk.Model != "" && !modelName.MatchString(sk.Model) {
				err = at(v, "model %q is not a model: \"provider/model\" or a model's ID, without spaces", sk.Model)
			}
		case "tools":
			sk.Tools, err = list(v, k.Value, maxSkillTools, 64, offset)
			for j, name := range sk.Tools {
				switch {
				case err != nil:
				case !toolName.MatchString(name):
					err = at(v.Content[j], "%q is not a tool's name", name)
				case slices.Contains(sk.Tools[:j], name):
					err = at(v.Content[j], "the tool %s is named twice", name)
				}
			}
		case "examples":
			sk.Examples, err = list(v, k.Value, maxSkillExamples, maxSkillText, offset)
		case "inputs":
			sk.Inputs, err = inputs(v, offset)
		default:
			err = at(k, "%q is not a key of a skill's front matter: name, title, title_zh, description, description_zh, inputs, tools, model and examples are", k.Value)
		}
		if err != nil {
			return err
		}
	}
	for _, need := range []string{"name", "title", "description"} {
		if !seen[need] || need == "name" && sk.Name == "" || need == "title" && sk.Title == "" || need == "description" && sk.Description == "" {
			return &SkillError{Line: 1, Msg: "the front matter has no " + need}
		}
	}
	if sk.Tools == nil {
		sk.Tools = []string{}
	}
	return nil
}

// yamlError places a YAML error at its line of the file. The parser's own
// line is at times that of what the error is inside of, as a list opened
// lines before, or none: the error is placed at the later of it and the
// first line whose text, up to it, the parser refuses as it refuses the
// whole (or refuses at all).
func yamlError(text string, err error, offset int) error {
	msg, line := strings.TrimPrefix(err.Error(), "yaml: "), 0
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
		msg = m[2]
	}
	lines := strings.Split(text, "\n")
	first, same := 0, 0
	for i := 1; i <= len(lines) && same == 0; i++ {
		var n yaml.Node
		e := yaml.Unmarshal([]byte(strings.Join(lines[:i], "\n")), &n)
		if e == nil {
			continue
		}
		if first == 0 {
			first = i
		}
		if em := e.Error(); strings.TrimPrefix(em, "yaml: ") == msg || strings.HasSuffix(em, ": "+msg) {
			same = i
		}
	}
	if same == 0 {
		same = max(first, 1)
	}
	return &SkillError{Line: max(line, same) + offset, Msg: "the front matter is not YAML: " + msg}
}

// text1 returns a scalar's text, at most n bytes on one line.
func text1(v *yaml.Node, key string, n, offset int) (string, error) {
	if v.Kind != yaml.ScalarNode {
		return "", &SkillError{Line: v.Line + offset, Msg: key + " is not a text"}
	}
	s := strings.TrimSpace(v.Value)
	if v.Tag == "!!null" {
		s = ""
	}
	switch {
	case len(s) > n:
		return "", &SkillError{Line: v.Line + offset, Msg: fmt.Sprintf("%s is %d bytes; it is at most %d", key, len(s), n)}
	case strings.ContainsAny(s, "\n\r"):
		return "", &SkillError{Line: v.Line + offset, Msg: key + " is one line"}
	}
	return s, nil
}

// list returns a sequence of texts, at most max of at most n bytes each.
func list(v *yaml.Node, key string, max, n, offset int) ([]string, error) {
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return nil, nil
	}
	if v.Kind != yaml.SequenceNode {
		return nil, &SkillError{Line: v.Line + offset, Msg: key + " is not a list, as [a, b] or lines of \"- a\""}
	}
	if len(v.Content) > max {
		return nil, &SkillError{Line: v.Line + offset, Msg: fmt.Sprintf("%s has %d items; it has at most %d", key, len(v.Content), max)}
	}
	out := make([]string, 0, len(v.Content))
	for _, e := range v.Content {
		s, err := text1(e, key+"'s item", n, offset)
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, &SkillError{Line: e.Line + offset, Msg: key + " has an empty item"}
		}
		out = append(out, s)
	}
	return out, nil
}

// inputs returns a skill's inputs: a sequence of mappings of name,
// description and required.
func inputs(v *yaml.Node, offset int) ([]SkillInput, error) {
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return nil, nil
	}
	at := func(n *yaml.Node, format string, args ...any) error {
		return &SkillError{Line: n.Line + offset, Msg: fmt.Sprintf(format, args...)}
	}
	if v.Kind != yaml.SequenceNode {
		return nil, at(v, "inputs is not a list of inputs, each with a name")
	}
	if len(v.Content) > maxSkillInputs {
		return nil, at(v, "inputs has %d items; it has at most %d", len(v.Content), maxSkillInputs)
	}
	var out []SkillInput
	for _, e := range v.Content {
		if e.Kind != yaml.MappingNode {
			return nil, at(e, "an input is a mapping of name, description and required")
		}
		var in SkillInput
		seen := map[string]bool{}
		for i := 0; i+1 < len(e.Content); i += 2 {
			k, val := e.Content[i], e.Content[i+1]
			if seen[k.Value] {
				return nil, at(k, "an input's %s is given twice", k.Value)
			}
			seen[k.Value] = true
			var err error
			switch k.Value {
			case "name":
				if in.Name, err = text1(val, "an input's name", 32, offset); err == nil && !inputName.MatchString(in.Name) {
					err = at(val, "%q is not an input's name: lower case letters, digits and '_', starting with a letter", in.Name)
				}
			case "description":
				in.Description, err = text1(val, "an input's description", maxSkillText, offset)
			case "required":
				if val.Kind != yaml.ScalarNode || val.Tag != "!!bool" {
					err = at(val, "an input's required is true or false")
				} else {
					in.Required = val.Value == "true" || val.Value == "True" || val.Value == "TRUE"
				}
			default:
				err = at(k, "%q is not a key of an input: name, description and required are", k.Value)
			}
			if err != nil {
				return nil, err
			}
		}
		if in.Name == "" {
			return nil, at(e, "an input has no name")
		}
		if slices.ContainsFunc(out, func(x SkillInput) bool { return x.Name == in.Name }) {
			return nil, at(e, "the input %s is given twice", in.Name)
		}
		out = append(out, in)
	}
	return out, nil
}

// input returns the skill's input name, or nil.
func (sk *Skill) input(name string) *SkillInput {
	for i := range sk.Inputs {
		if sk.Inputs[i].Name == name {
			return &sk.Inputs[i]
		}
	}
	return nil
}

// builtinFiles are the built-in skills, in the order the Skills page lists
// them: the plan's.
//
//go:embed skills/*.md
var builtinFiles embed.FS

// builtinOrder is the built-in skills' order.
var builtinOrder = []string{"explain", "route-builder", "reverse-proxy", "tls-acme", "rate-limit-auth", "cache-tuning", "diagnose",
	"drift-explain", "log-analysis", "security-review", "migrate", "media-setup", "llm-mesh-setup", "tunnel-setup",
	"vod-setup", "channel-scheduler", "library-health", "playback-troubleshoot", "fleet-health", "rollout-planner", "cluster-drift"}

// builtinSkills reads the built-in skills, in builtinOrder; one that does
// not parse, or is not in the order, is an error of the build.
func builtinSkills() ([]*Skill, error) {
	entries, err := builtinFiles.ReadDir("skills")
	if err != nil {
		return nil, err
	}
	out := make([]*Skill, 0, len(entries))
	for _, e := range entries {
		data, _ := builtinFiles.ReadFile("skills/" + e.Name())
		sk, err := ParseSkill(data)
		if err != nil {
			return nil, fmt.Errorf("assistant: the built-in skill %s: %v", e.Name(), err)
		}
		if sk.Name+".md" != e.Name() || !slices.Contains(builtinOrder, sk.Name) {
			return nil, fmt.Errorf("assistant: the built-in skill %s is named %s, or is not in the order", e.Name(), sk.Name)
		}
		out = append(out, sk)
	}
	slices.SortFunc(out, func(x, y *Skill) int { return slices.Index(builtinOrder, x.Name) - slices.Index(builtinOrder, y.Name) })
	return out, nil
}
