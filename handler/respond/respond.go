// Package respond is the respond handler: it answers every request with the
// same fixed response.
package respond

import (
	"maps"
	stdhttp "net/http"
	"slices"
	"strings"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".respond", New: func() any { return new(Respond) }})
}

// Respond answers with a fixed status, headers and body.
type Respond struct {
	Status  int               `json:"status,omitempty" min:"100" max:"599" default:"200" doc:"The status code; 200 when left out."`
	Headers map[string]string `json:"headers,omitempty" doc:"Response headers. Content-Type defaults to text/plain; charset=utf-8 when there is a body."`
	Body    string            `json:"body,omitempty" doc:"The response body."`

	// response is built once and shared by every request: fib does not
	// change a Response's header it is handed.
	response fibhttp.Response
}

// Provision checks the headers and builds the response.
func (r *Respond) Provision(ctx *module.Context) error {
	status := r.Status
	if status == 0 {
		status = stdhttp.StatusOK
	}
	if status < 200 {
		return ctx.Errorf("status", "%d is an informational status, which cannot be a final response", status)
	}
	header := make(stdhttp.Header, len(r.Headers)+1)
	for _, name := range slices.Sorted(maps.Keys(r.Headers)) {
		value := r.Headers[name]
		if !validHeaderName(name) {
			return ctx.Errorf("headers", "%q is not a valid header name", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return ctx.Errorf("headers", "the value of %s holds a line break or NUL", name)
		}
		header.Set(name, value)
	}
	if r.Body != "" && header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	if r.Body != "" && !bodyAllowed(status) {
		return ctx.Errorf("body", "a %d response cannot have a body", status)
	}
	r.response = fibhttp.Response{StatusCode: status, Header: header, Body: []byte(r.Body)}
	return nil
}

// ServeHTTP answers with the response.
func (r *Respond) ServeHTTP(c *fibhttp.Context) {
	_ = c.WriteResponse(r.response) // fails only when the client is gone
}

func bodyAllowed(status int) bool {
	return status != stdhttp.StatusNoContent && status != stdhttp.StatusNotModified
}

// validHeaderName reports whether name is an HTTP token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}
