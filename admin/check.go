package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/adapter/fitconf"
	"github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
)

// The text formats the Admin API renders a config fragment in, parses, and
// applies: fit's own .conf, YAML, and its native JSON. They are the formats
// the web pages' raw editors edit the config's parts in, and what
// fit validate reads from a file.
type textFormat struct {
	name    string   // the format query parameter's value
	adapter string   // the adapter that reads it; "" for native JSON
	types   []string // the Content-Types its text is sent with
}

var textFormats = [...]textFormat{
	{name: "conf", adapter: "fit", types: []string{"text/plain"}},
	{name: "yaml", adapter: "yaml", types: []string{"text/plain", "application/yaml", "text/yaml", "application/x-yaml"}},
	{name: "json", types: []string{"text/plain", "application/json"}},
}

// textFormatOrBadRequest returns the format a request names, or answers it
// 400, listing the formats, and returns false.
func (s *Server) textFormatOrBadRequest(w http.ResponseWriter, r *http.Request) (textFormat, bool) {
	name := r.URL.Query().Get("format")
	for _, f := range textFormats {
		if f.name == name {
			return f, true
		}
	}
	writeError(w, http.StatusBadRequest, "the format must be conf, yaml or json")
	return textFormat{}, false
}

// renderText answers a config fragment, a JSON object, written in one of the
// text formats: POST /config/render. It is what a page's raw text editor
// starts from.
func (s *Server) renderText(w http.ResponseWriter, r *http.Request) {
	f, ok := s.textFormatOrBadRequest(w, r)
	if !ok {
		return
	}
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	std, err := config.Standardize(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the body is not a config: "+err.Error())
		return
	}
	if _, err := decodeObject(std); err != nil {
		writeUpdateError(w, err)
		return
	}
	var out []byte
	switch f.name {
	case "conf":
		out, err = fitconf.FromJSON(std)
	case "yaml":
		out, err = yaml.FromJSON(std)
	default: // json
		var b bytes.Buffer
		if err = json.Indent(&b, bytes.TrimSpace(std), "", "  "); err == nil {
			b.WriteByte('\n')
			out = b.Bytes()
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "rendering the config: "+err.Error())
		return
	}
	writeText(w, "text/plain; charset=utf-8", out)
}

// parseText answers the JSON of a config fragment written in one of the text
// formats: POST /config/parse. It is what a page's raw text editor shows as
// the edit, and the errors it shows beside it.
func (s *Server) parseText(w http.ResponseWriter, r *http.Request) {
	f, ok := s.textFormatOrBadRequest(w, r)
	if !ok {
		return
	}
	data, ok := s.readBody(w, r, f.types...)
	if !ok {
		return
	}
	adapted, err := adaptText(f, data)
	if err != nil {
		writeTextParseError(w, err)
		return
	}
	root, err := decodeObject(adapted.JSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the text is not a config object")
		return
	}
	writeText(w, "application/json", encodeJSON(root))
}

// putText replaces the whole config, or the members of it a page edits, with
// text in one of the text formats, and applies it by the same path as every
// other config: PUT /config/text. The members parameter names the top-level
// members a page edits, comma-separated; without it the text is the whole
// config. Nothing is applied unless the merged config loads and compiles.
func (s *Server) putText(w http.ResponseWriter, r *http.Request) {
	f, ok := s.textFormatOrBadRequest(w, r)
	if !ok {
		return
	}
	members, err := queryMembers(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	dry, err := isDryRun(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	data, ok := s.readBody(w, r, f.types...)
	if !ok {
		return
	}
	adapted, err := adaptText(f, data)
	if err != nil {
		writeTextParseError(w, err)
		return
	}
	fragment, err := decodeJSON(adapted.JSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the text does not parse")
		return
	}
	frag, ok := fragment.(*object)
	if !ok {
		writeError(w, http.StatusBadRequest, "a config is a JSON object")
		return
	}
	if len(members) > 0 {
		for _, name := range frag.keys {
			if !slices.Contains(members, name) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("the text sets %q, which this page does not edit", name))
				return
			}
		}
	}
	res, err := s.update(r, dry, func(cur *Version) ([]byte, error) {
		if len(members) == 0 {
			return adapted.JSON, nil
		}
		root, err := decodeJSON(cur.Data())
		if err != nil {
			return nil, err
		}
		base, ok := root.(*object)
		if !ok {
			return nil, badRequest("the running config is not a JSON object")
		}
		for _, name := range members {
			if v, ok := frag.get(name); ok {
				base.set(name, v)
			} else {
				base.del(name)
			}
		}
		return encodeJSON(base), nil
	})
	if err != nil {
		s.writeTextUpdateError(w, adapted, err)
		return
	}
	s.applied(w, res, nil)
}

// adaptText adapts text, a config fragment in format f, to native JSON, and
// gives the adapter's result, which knows where the text wrote the values a
// config error names.
func adaptText(f textFormat, text []byte) (*adapter.Result, error) {
	res, err := adapter.Adapt(f.adapter, text)
	if err != nil {
		return nil, err
	}
	if f.adapter == "" {
		// JSON: read it with the config's own parser, so that a syntax
		// error carries the line and column it was found at, as one in a
		// .json file fit reads does, and so that map errors back to it.
		v, err := config.ParseValue(res.JSON, "")
		if err != nil {
			return nil, err
		}
		res.Position = valuePosition(v)
	}
	return res, nil
}

// valuePosition returns where in its document each value of v is, for the
// paths the config's errors name, as an adapter's Position does for its
// format.
func valuePosition(v config.Value) func(path string) (line, col int, ok bool) {
	return func(path string) (line, col int, ok bool) {
		cur := v
		if path != "" {
			steps, ok := splitPath(path)
			if !ok {
				return 0, 0, false
			}
			for _, s := range steps {
				if s.array {
					cur = cur.Index(s.index)
				} else {
					cur = cur.Field(s.name)
				}
			}
		}
		line, col = cur.Pos()
		return line, col, line != 0
	}
}

// pathStep is one step of a config error's path: an object's member, or an
// array's element.
type pathStep struct {
	name  string // a member's name
	index int    // an array's element
	array bool
}

// splitPath splits a config error's path, as in listeners[0].address or
// providers["a.b"], into the member names and array indexes it goes through.
func splitPath(path string) ([]pathStep, bool) {
	var steps []pathStep
	for path != "" {
		switch {
		case path[0] == '.':
			path = path[1:]
		case path[0] == '[':
			end := strings.IndexByte(path, ']')
			if end < 0 {
				return nil, false
			}
			tok := path[1:end]
			if strings.HasPrefix(tok, `"`) {
				name, err := strconv.Unquote(tok)
				if err != nil {
					return nil, false
				}
				steps = append(steps, pathStep{name: name})
			} else {
				i, err := strconv.Atoi(tok)
				if err != nil {
					return nil, false
				}
				steps = append(steps, pathStep{index: i, array: true})
			}
			path = path[end+1:]
		default:
			end := strings.IndexAny(path, ".[")
			if end < 0 {
				end = len(path)
			}
			if end == 0 {
				return nil, false
			}
			steps = append(steps, pathStep{name: path[:end]})
			path = path[end:]
		}
	}
	return steps, true
}

// queryMembers returns the top-level config members a page's raw text edits,
// from the members parameter, comma-separated, or nil for the whole config.
// admin is refused: it comes from the config fit started with, not from a
// page.
func queryMembers(r *http.Request) ([]string, error) {
	param := r.URL.Query().Get("members")
	if param == "" {
		return nil, nil
	}
	var members []string
	for _, name := range strings.Split(param, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if name == "admin" {
			return nil, badRequest("admin cannot be edited this way: it comes from the config fit started with")
		}
		if !slices.Contains(members, name) {
			members = append(members, name)
		}
	}
	return members, nil
}

// writeTextParseError answers a text that does not parse: the config's own
// errors with their positions, or another adapter's error as it is.
func writeTextParseError(w http.ResponseWriter, err error) {
	var errs config.Errors
	if configErrors(err, &errs) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "the text does not parse", Errors: problems(errs)})
		return
	}
	writeError(w, http.StatusBadRequest, "the text does not parse: "+err.Error())
}

// writeTextUpdateError answers a text save that failed, as writeUpdateError
// does, but with the config errors moved to the lines and columns of the
// text they were written at: those of paths outside the fragment have none.
func (s *Server) writeTextUpdateError(w http.ResponseWriter, adapted *adapter.Result, err error) {
	var errs config.Errors
	var re *RequestError
	switch {
	case errors.Is(err, ErrChanged):
		writeError(w, http.StatusPreconditionFailed, strings.TrimPrefix(err.Error(), "admin: "))
	case errors.As(err, &re):
		writeError(w, re.Status, re.Msg)
	case configErrors(err, &errs):
		mapped := config.AsErrors(adapted.MapErrors(err))
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "the config was not applied; the running config stays", Errors: problems(mapped)})
	default:
		writeError(w, http.StatusInternalServerError, "the config was not applied; the running config stays: "+err.Error())
	}
}

// decodeObject parses data, one JSON value, and requires it to be an object:
// a config, or a fragment of one.
func decodeObject(data []byte) (*object, error) {
	v, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*object)
	if !ok {
		return nil, badRequest("a config is a JSON object")
	}
	return o, nil
}

// configErrors returns the config's own errors err holds: err itself when it
// is a *config.Error, its elements when it is config.Errors. An error of
// another kind, as an adapter's syntax error, is not one.
func configErrors(err error, errs *config.Errors) bool {
	var one *config.Error
	switch {
	case errors.As(err, errs):
		return true
	case errors.As(err, &one):
		*errs = config.Errors{one}
		return true
	}
	return false
}

// writeText answers with data in contentType.
func writeText(w http.ResponseWriter, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
