package model

import (
	"encoding/json"
	"strconv"
	"strings"
)

// answer gathers an answer as its events are parsed, hands each piece on,
// and bounds the tool calls.
type answer struct {
	res Result
	// emit hands an event on; it returns false once the call has ended,
	// and the parse stops.
	emit     func(Event) bool
	maxTools int
	// done is set once the API has said the answer is complete.
	done bool
	// partial are the tool calls whose arguments are still arriving, by
	// the key the API's parts name them with, in the order they began.
	partial map[string]*partialCall
	order   []string
}

// partialCall is a tool call being assembled.
type partialCall struct {
	id, name, sig string
	args          strings.Builder
}

func (a *answer) text(s string) error {
	if s == "" {
		return nil
	}
	a.res.Text += s
	return a.send(Event{Type: EventText, Text: s})
}

func (a *answer) thinking(s string) error {
	if s == "" {
		return nil
	}
	a.res.Thinking += s
	return a.send(Event{Type: EventThinking, Text: s})
}

// errStopped ends a parse whose call has ended.
var errStopped = &stopped{}

type stopped struct{}

func (*stopped) Error() string { return "the call has ended" }

func (a *answer) send(e Event) error {
	if !a.emit(e) {
		return errStopped
	}
	return nil
}

// begin starts the tool call key, or adds to it: id and name when the API
// gives them first, args as they stream.
func (a *answer) begin(key, id, name string) (*partialCall, error) {
	if a.partial == nil {
		a.partial = map[string]*partialCall{}
	}
	pc := a.partial[key]
	if pc == nil {
		if len(a.res.ToolCalls)+len(a.partial) >= a.maxTools {
			return nil, ErrTooLarge
		}
		pc = &partialCall{}
		a.partial[key] = pc
		a.order = append(a.order, key)
	}
	if id != "" {
		pc.id = id
	}
	if name != "" {
		pc.name = name
	}
	return pc, nil
}

// end finishes the tool call key, if it has begun, and hands it on.
func (a *answer) end(key string) error {
	pc := a.partial[key]
	if pc == nil {
		return nil
	}
	delete(a.partial, key)
	for i, k := range a.order {
		if k == key {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	return a.call(ToolCall{ID: pc.id, Name: pc.name, Arguments: json.RawMessage(pc.args.String()), Signature: pc.sig})
}

// endAll finishes every tool call that has begun, in order.
func (a *answer) endAll() error {
	for len(a.order) > 0 {
		if err := a.end(a.order[0]); err != nil {
			return err
		}
	}
	return nil
}

// call hands on a whole tool call, its arguments made a JSON object: none
// is {}, and what is not JSON becomes a string, marked Invalid.
func (a *answer) call(tc ToolCall) error {
	if len(a.res.ToolCalls) >= a.maxTools {
		return ErrTooLarge
	}
	if tc.ID == "" {
		tc.ID = "call_" + strconv.Itoa(len(a.res.ToolCalls)+1)
	}
	args := strings.TrimSpace(string(tc.Arguments))
	switch {
	case args == "" || args == "null":
		tc.Arguments = json.RawMessage("{}")
	case !json.Valid([]byte(args)) || args[0] != '{':
		b, _ := json.Marshal(args)
		tc.Arguments, tc.Invalid = b, true
	default:
		tc.Arguments = json.RawMessage(args)
	}
	a.res.ToolCalls = append(a.res.ToolCalls, tc)
	return a.send(Event{Type: EventToolCall, ToolCall: &tc})
}

// apiMessage reads the message of an API's error body: {"error":
// {"message": ...}}, {"error": "..."}, {"message": ...} or {"detail": ...},
// else the body itself, cut to 512 bytes.
func apiMessage(body []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(body, &e) == nil {
		var inner struct {
			Message string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(e.Error, &inner) == nil && inner.Message != "":
			return inner.Message
		case json.Unmarshal(e.Error, &s) == nil && s != "":
			return s
		case e.Message != "":
			return e.Message
		case e.Detail != "":
			return e.Detail
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 512 {
		s = s[:512] + "..."
	}
	if s == "" {
		s = "no message"
	}
	return s
}
