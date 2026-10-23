// Package yaml adapts configs written in YAML to fit's native JSON, and
// native JSON to YAML (decision 019). The YAML says what the JSON would,
// key for key: a mapping is an object, a sequence an array, and a scalar
// the JSON value its YAML 1.2 tag says, a string unless it is a number, a
// boolean (true or false) or null. Anchors, aliases and merge keys (<<) are
// resolved, within bounds. Errors in the config point at the YAML.
package yaml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/lesismal/fit/adapter"
)

func init() {
	adapter.Register("yaml", Adapt, ".yaml", ".yml")
}

// The bounds on what a document may expand to through its aliases.
const (
	maxNodes = 1 << 20
	maxDepth = 256
	maxJSON  = 64 << 20
)

// Adapt turns a YAML config into native JSON.
func Adapt(data []byte) (*adapter.Result, error) {
	var doc yamlv3.Node
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the YAML holds no document")
		}
		return nil, err
	}
	var extra yamlv3.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("the YAML holds more than one document")
	}
	c := &converter{pos: map[string][2]int{}}
	root := &doc
	if root.Kind == yamlv3.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if err := c.value(root, "", 0); err != nil {
		return nil, err
	}
	pos := c.pos
	return &adapter.Result{JSON: c.b.Bytes(), Position: func(path string) (int, int, bool) {
		p, ok := pos[path]
		return p[0], p[1], ok
	}}, nil
}

// converter writes the JSON of a YAML tree, and where each path's value was.
type converter struct {
	b     bytes.Buffer
	pos   map[string][2]int
	nodes int
}

func (c *converter) at(n *yamlv3.Node, path string) {
	if _, ok := c.pos[path]; !ok {
		c.pos[path] = [2]int{n.Line, n.Column}
	}
}

func errAt(n *yamlv3.Node, format string, args ...any) error {
	return fmt.Errorf("line %d, column %d: %s", n.Line, n.Column, fmt.Sprintf(format, args...))
}

func (c *converter) value(n *yamlv3.Node, path string, depth int) error {
	if c.nodes++; c.nodes > maxNodes || c.b.Len() > maxJSON {
		return errAt(n, "the document expands to too much, through its aliases")
	}
	if depth > maxDepth {
		return errAt(n, "nested too deep")
	}
	c.at(n, path)
	switch n.Kind {
	case yamlv3.AliasNode:
		return c.value(n.Alias, path, depth+1)
	case yamlv3.MappingNode:
		return c.mapping(n, path, depth)
	case yamlv3.SequenceNode:
		c.b.WriteByte('[')
		for i, e := range n.Content {
			if i > 0 {
				c.b.WriteByte(',')
			}
			if err := c.value(e, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		c.b.WriteByte(']')
		return nil
	case yamlv3.ScalarNode:
		return c.scalar(n)
	case yamlv3.DocumentNode:
		return errAt(n, "a document inside a document")
	}
	return errAt(n, "a node of unknown kind")
}

// member is a mapping's key and value, with merged ones resolved.
type member struct {
	key      string
	keyNode  *yamlv3.Node
	valNode  *yamlv3.Node
	inMerged bool
}

// members returns a mapping's members in order, those merge keys bring in
// after, unless a key of its own or an earlier merge has them.
func (c *converter) members(n *yamlv3.Node, depth int) ([]member, error) {
	if depth > maxDepth {
		return nil, errAt(n, "nested too deep")
	}
	var own, merged []member
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind == yamlv3.ScalarNode && k.ShortTag() == "!!merge" {
			var sources []*yamlv3.Node
			switch src := deref(v); src.Kind {
			case yamlv3.MappingNode:
				sources = []*yamlv3.Node{src}
			case yamlv3.SequenceNode:
				for _, s := range src.Content {
					sources = append(sources, deref(s))
				}
			default:
				return nil, errAt(v, "a merge key (<<) needs a mapping or a list of them")
			}
			for _, src := range sources {
				if src.Kind != yamlv3.MappingNode {
					return nil, errAt(src, "a merge key (<<) needs a mapping or a list of them")
				}
				ms, err := c.members(src, depth+1)
				if err != nil {
					return nil, err
				}
				for _, m := range ms {
					m.inMerged = true
					merged = append(merged, m)
				}
			}
			continue
		}
		if k.Kind != yamlv3.ScalarNode || k.ShortTag() != "!!str" && k.ShortTag() != "!!int" && k.ShortTag() != "!!bool" && k.ShortTag() != "!!float" && k.ShortTag() != "!!null" {
			return nil, errAt(k, "a key must be a string")
		}
		if seen[k.Value] {
			return nil, errAt(k, "%q is a key twice", k.Value)
		}
		seen[k.Value] = true
		own = append(own, member{key: k.Value, keyNode: k, valNode: v})
	}
	for _, m := range merged {
		if !seen[m.key] {
			seen[m.key] = true
			own = append(own, m)
		}
	}
	return own, nil
}

func deref(n *yamlv3.Node) *yamlv3.Node {
	for i := 0; n.Kind == yamlv3.AliasNode && i < maxDepth; i++ {
		n = n.Alias
	}
	return n
}

func (c *converter) mapping(n *yamlv3.Node, path string, depth int) error {
	ms, err := c.members(n, depth)
	if err != nil {
		return err
	}
	c.b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			c.b.WriteByte(',')
		}
		key, _ := json.Marshal(m.key)
		c.b.Write(key)
		c.b.WriteByte(':')
		// Errors name a struct's field a.b and a map's key a["b"]; the
		// adapter does not know which this is, and records both.
		field := m.key
		if path != "" {
			field = path + "." + m.key
		}
		quoted := path + "[" + strconv.Quote(m.key) + "]"
		at := m.valNode
		if !m.inMerged {
			c.at(at, field)
			c.at(at, quoted)
		}
		if err := c.value(m.valNode, field, depth+1); err != nil {
			return err
		}
		if !m.inMerged {
			c.pos[quoted] = c.pos[field]
		}
	}
	c.b.WriteByte('}')
	return nil
}

func (c *converter) scalar(n *yamlv3.Node) error {
	switch n.ShortTag() {
	case "!!null":
		c.b.WriteString("null")
	case "!!bool":
		var v bool
		if err := n.Decode(&v); err != nil {
			return errAt(n, "%v", err)
		}
		c.b.WriteString(strconv.FormatBool(v))
	case "!!int":
		var v int64
		if err := n.Decode(&v); err != nil {
			var u uint64
			if err2 := n.Decode(&u); err2 != nil {
				return errAt(n, "%q is not a number JSON can hold", n.Value)
			}
			c.b.WriteString(strconv.FormatUint(u, 10))
			return nil
		}
		c.b.WriteString(strconv.FormatInt(v, 10))
	case "!!float":
		var v float64
		if err := n.Decode(&v); err != nil {
			return errAt(n, "%v", err)
		}
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return errAt(n, "%s is not a number JSON can hold", n.Value)
		}
		c.b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
	case "!!str", "!!timestamp", "!!binary":
		s, _ := json.Marshal(n.Value)
		c.b.Write(s)
	default:
		return errAt(n, "the tag %s is not one fit's config knows", n.Tag)
	}
	return nil
}

// FromJSON writes native JSON as YAML, key for key, with two spaces of
// indentation: what Adapt turns back into the same JSON.
func FromJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := node(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON: more after the value")
	}
	var b bytes.Buffer
	enc := yamlv3.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// node reads the next JSON value from dec as a YAML node.
func node(dec *json.Decoder) (*yamlv3.Node, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("JSON: %w", err)
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &yamlv3.Node{Kind: yamlv3.MappingNode, Tag: "!!map"}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("JSON: %w", err)
				}
				v, err := node(dec)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: kt.(string)}, v)
			}
			_, err := dec.Token()
			return n, err
		case '[':
			n := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq"}
			for dec.More() {
				v, err := node(dec)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, v)
			}
			_, err := dec.Token()
			return n, err
		}
	case string:
		n := &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: t}
		if strings.Contains(t, "\n") {
			n.Style = yamlv3.LiteralStyle
		}
		return n, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(string(t), ".eE") {
			tag = "!!float"
		}
		return &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: tag, Value: string(t)}, nil
	case bool:
		return &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}, nil
	case nil:
		return &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("JSON: unexpected %v", t)
}
