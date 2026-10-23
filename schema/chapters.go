package schema

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/lesismal/fit/adapter/yaml"
)

// doc holds the reference's chapters: what each part of the config is for,
// and its examples.
//
//go:embed doc
var doc embed.FS

// chapters are the parts of the reference after the config's own table,
// one for each of the config's top-level fields that has a table, in the
// order the config has them.
var chapters = []struct{ field, title, anchor, file string }{
	{"listeners", "Listeners", "listeners", "listeners.md"},
	{"routes", "Routes", "routes", "routes.md"},
	{"upstreams", "Upstreams", "upstreams", "upstreams.md"},
	{"services", "Services", "services", "services.md"},
	{"access_log", "Access log", "access-log", "access_log.md"},
	{"telemetry", "Telemetry", "telemetry", "telemetry.md"},
	{"limits", "Limits", "limits", "limits.md"},
	{"admin", "Admin API", "admin-api", "admin.md"},
	{"auto_https", "HTTPS by default", "https-by-default", "auto_https.md"},
	{"metrics", "Metrics", "metrics", "metrics.md"},
	{"acme", "ACME", "acme", "acme.md"},
	{"providers", "Providers", "providers", "providers.md"},
}

// standardModules are the modules in modules/standard, each of which has a
// chapter in schema/doc/modules.
var standardModules = map[string]struct{}{
	"fastcgi": {}, "forward_proxy": {}, "proxy": {}, "respond": {}, "static": {},
	"basic_auth": {}, "body_limit": {}, "cache": {}, "circuit_breaker": {}, "client_cert": {}, "compress": {},
	"concurrency_limit": {}, "forward_auth": {}, "headers": {}, "jwt": {}, "rate_limit": {}, "redirect": {},
	"rewrite": {}, "wasm": {},
}

// chapter is a chapter's text, and its examples: in JSON, from which the
// YAML is made, and as a Caddyfile, when one says the same or near it.
type chapter struct {
	file      string
	text      string
	json      string
	caddyfile string
	// route says the JSON is a route's, not a whole config's.
	route bool
}

var (
	jsonBlock      = regexp.MustCompile("(?s)```json( route)?\n(.*?)\n```\n?")
	caddyfileBlock = regexp.MustCompile("(?s)```caddyfile\n(.*?)\n```\n?")
)

// readChapter reads a chapter of schema/doc: Markdown, with at most one
// JSON example and one Caddyfile example in fenced blocks.
func readChapter(name string) (*chapter, error) {
	data, err := doc.ReadFile("doc/" + name)
	if err != nil {
		return nil, fmt.Errorf("schema: no chapter %s: %w", name, err)
	}
	c := &chapter{file: name}
	text := string(data)
	if m := jsonBlock.FindStringSubmatch(text); m != nil {
		c.json, c.route = m[2], m[1] != ""
		text = strings.Replace(text, m[0], "", 1)
	}
	if m := caddyfileBlock.FindStringSubmatch(text); m != nil {
		c.caddyfile = m[1]
		text = strings.Replace(text, m[0], "", 1)
	}
	if jsonBlock.MatchString(text) || caddyfileBlock.MatchString(text) {
		return nil, fmt.Errorf("schema: chapter %s has more than one example of a kind", name)
	}
	c.text = strings.TrimSpace(text)
	return c, nil
}

// write writes the chapter: its text, then its examples, as a Caddyfile,
// in YAML and in JSON.
func (c *chapter) write(buf *bytes.Buffer) error {
	if c.text != "" {
		fmt.Fprintf(buf, "\n%s\n", c.text)
	}
	if c.caddyfile != "" {
		fmt.Fprintf(buf, "\nAs a Caddyfile:\n\n```caddyfile\n%s\n```\n", c.caddyfile)
	}
	if c.json != "" {
		y, err := yaml.FromJSON([]byte(c.json))
		if err != nil {
			return fmt.Errorf("schema: chapter %s: its JSON: %w", c.file, err)
		}
		what := ""
		if c.route {
			what = ", a route"
		}
		fmt.Fprintf(buf, "\nIn YAML%s:\n\n```yaml\n%s```\n\nIn JSON:\n\n```json\n%s\n```\n", what, y, c.json)
	}
	return nil
}
