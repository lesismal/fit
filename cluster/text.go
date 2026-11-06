package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/adapter/fitconf"
	"github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/app/console"
)

// The text formats a template's editor shows its config in, as the config
// pages show a part of the config: fit's .conf, YAML and JSON.
var textFormats = []string{"conf", "yaml", "json"}

// renderTemplateText writes a template's config, a JSON object, in format.
func renderTemplateText(format string, data []byte) ([]byte, error) {
	v, err := parseJV(data)
	if err != nil || v.kind != 'o' {
		return nil, fmt.Errorf("a template's config is a JSON object")
	}
	switch format {
	case "conf":
		return fitconf.FromJSON(v.bytes())
	case "yaml":
		return yaml.FromJSON(v.bytes())
	}
	return v.pretty(), nil
}

// parseTemplateText reads a template's config written in format, as JSON.
func parseTemplateText(format string, text []byte) ([]byte, error) {
	var res *adapter.Result
	var err error
	switch format {
	case "conf":
		res, err = fitconf.Adapt(text)
	case "yaml":
		res, err = yaml.Adapt(text)
	default:
		res = &adapter.Result{JSON: bytes.TrimSpace(text)}
		if len(res.JSON) == 0 {
			res.JSON = []byte("{}")
		}
	}
	if err != nil {
		return nil, err
	}
	v, err := parseJV(res.JSON)
	if err != nil {
		return nil, fmt.Errorf("the text is not JSON: %v", err)
	}
	if v.kind != 'o' {
		return nil, fmt.Errorf("a template's config is an object of top-level members")
	}
	return v.pretty(), nil
}

// textFormat returns the format the request's ?format= names, or answers
// 400.
func textFormat(r *console.Request) (string, bool) {
	f := r.Request.URL.Query().Get("format")
	for _, x := range textFormats {
		if x == f {
			return f, true
		}
	}
	_ = console.Error(r.Context, 400, "the format must be conf, yaml or json")
	return "", false
}

// renderText answers POST /api/text/render?format=: a template's config,
// JSON, written in the format, as text.
func (m *Manager) renderText(r *console.Request) {
	f, ok := textFormat(r)
	if !ok {
		return
	}
	body := r.Context.Body()
	if len(body) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a template's text is %d bytes at most", MaxBodyBytes))
		return
	}
	out, err := renderTemplateText(f, body)
	if err != nil {
		_ = console.Error(r.Context, 400, "rendering the config: "+err.Error())
		return
	}
	_ = console.Write(r.Context, 200, "text/plain; charset=utf-8", out)
}

// parseText answers POST /api/text/parse?format=: a template's config
// written in the format, as JSON.
func (m *Manager) parseText(r *console.Request) {
	f, ok := textFormat(r)
	if !ok {
		return
	}
	body := r.Context.Body()
	if len(body) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a template's text is %d bytes at most", MaxBodyBytes))
		return
	}
	out, err := parseTemplateText(f, body)
	if err != nil {
		_ = console.Error(r.Context, 400, err.Error())
		return
	}
	_ = console.Write(r.Context, 200, "application/json", json.RawMessage(out))
}
