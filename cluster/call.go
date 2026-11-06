package cluster

import (
	stdhttp "net/http"
	"strings"
)

// Reply is a member's answer to Call, whole: at most max_response_bytes.
type Reply struct {
	Status int
	Header stdhttp.Header
	Body   []byte
}

func (p *Problem) Error() string { return p.Kind + ": " + p.Message }

// Call sends a request to the Admin API of the member name, as the manager
// reaches it, whichever way that is: over HTTP, through its console with
// the session the manager keeps (logged in first when there is none, and
// again when the call answers 401) or with its Admin API's token; or over
// the link it opened to the manager. path is the Admin API's own, with its
// query, as "/config" or "/versions/3?x=1": Call adds what the member's
// access puts before it. header goes with the request but for credentials,
// Host and the hop-by-hop fields, which are the manager's; body, when not
// nil, is sent whole, as application/json unless header says otherwise.
//
// done is called once, with the answer, whatever its status, or with why
// there is none, from a fib worker, a timer or the caller's goroutine; it
// must not block. Call itself never blocks. A member there is none of is
// reported by the error Call returns, and done is then not called. The
// call is bounded by admin.cluster.timeout, and its answer by
// max_response_bytes; a linked member's request by max_response_bytes too.
func (m *Manager) Call(name, method, path string, header stdhttp.Header, body []byte, done func(*Reply, *Problem)) error {
	m.mu.Lock()
	n := m.nodes[name]
	closed := m.closed
	m.mu.Unlock()
	switch {
	case closed:
		return errClosed
	case n == nil:
		return errNotFound(name)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if n.spec.Access == AccessConsole {
		path = "/api/m/admin" + path
	}
	n.call(method, path, header, body, func(r *reply, _ *session, prob *Problem) {
		if prob != nil {
			if prob.At.IsZero() {
				prob.At = m.now().UTC()
			}
			prob.Message = n.redact(prob.Message)
			done(nil, prob)
			return
		}
		done(&Reply{Status: r.status, Header: r.header, Body: r.body}, nil)
	})
	return nil
}
