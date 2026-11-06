package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// maxAPIBytes bounds what a tool reads of an Admin API answer, before its
// own bound cuts it further: a config is at most a few MiB.
const maxAPIBytes = 16 << 20

// apiAnswer is an answer of the Admin API, called in process.
type apiAnswer struct {
	status int
	header http.Header
	body   []byte
}

// apiRecorder is the ResponseWriter of an in-process call, bounded.
type apiRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	over   bool
}

func (w *apiRecorder) Header() http.Header { return w.header }
func (w *apiRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *apiRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(p) > maxAPIBytes {
		w.over = true
		return 0, errors.New("the answer is too large")
	}
	return w.body.Write(p)
}

// errNoAPI is a tool that needs the Admin API on an assistant without it.
var errNoAPI = errors.New("the Admin API is not reachable from the assistant here")

// api calls the Admin API in process, as the console's own pages do: on
// the console's pool, which the tools run on; ifMatch, when not "", is the
// ETag of the config a change is for.
func (a *Assistant) api(ctx context.Context, method, path, ctype, ifMatch string, body []byte) (*apiAnswer, error) {
	return a.apiAs(ctx, "", method, path, ctype, ifMatch, body)
}

// ConsoleUserHeader is the header field the console sets on the Admin API's
// requests it passes on: the user logged in, whom a change of a media
// library's files is audited as (admin.ConsoleUserHeader).
const ConsoleUserHeader = "Fit-Console-User"

// apiAs is api as the console's user, when not "": a proposal's media
// steps are applied as the person who applies it.
func (a *Assistant) apiAs(ctx context.Context, user, method, path, ctype, ifMatch string, body []byte) (*apiAnswer, error) {
	if a.o.API == nil {
		return nil, errNoAPI
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://console"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	if user != "" {
		req.Header.Set(ConsoleUserHeader, user)
	}
	w := &apiRecorder{header: http.Header{}}
	a.o.API.ServeHTTP(w, req)
	if w.over {
		return nil, fmt.Errorf("%s %s: the answer is larger than %d bytes", method, path, maxAPIBytes)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &apiAnswer{status: w.status, header: w.header, body: w.body.Bytes()}, nil
}

// apiJSON calls the Admin API and returns the body of a 2xx answer, or the
// error the API gave.
func (a *Assistant) apiJSON(ctx context.Context, method, path, ctype string, body []byte) ([]byte, error) {
	return a.apiDo(ctx, method, path, ctype, "", body)
}

// apiDo is apiJSON with If-Match ifMatch, when not "".
func (a *Assistant) apiDo(ctx context.Context, method, path, ctype, ifMatch string, body []byte) ([]byte, error) {
	ans, err := a.api(ctx, method, path, ctype, ifMatch, body)
	if err != nil {
		return nil, err
	}
	if ans.status/100 != 2 {
		return nil, ans.err()
	}
	return ans.body, nil
}

// apiError is an answer of the Admin API that is not a 2xx: its status,
// its message and the config's errors it lists.
type apiError struct {
	Status int
	Msg    string
	Errors []apiProblem
}

// apiProblem is a config error as the Admin API reports it.
type apiProblem struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("the Admin API answered %d: %s", e.Status, e.Msg)
}

// err returns the answer as an *apiError.
func (ans *apiAnswer) err() error {
	var e struct {
		Error  string       `json:"error"`
		Errors []apiProblem `json:"errors"`
	}
	_ = json.Unmarshal(ans.body, &e)
	if e.Error == "" {
		e.Error = clip(string(ans.body), 512)
	}
	return &apiError{Status: ans.status, Msg: e.Error, Errors: e.Errors}
}
