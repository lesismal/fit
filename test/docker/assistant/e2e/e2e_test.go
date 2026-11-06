//go:build e2e

// Package e2e is the docker test of the assistant's model client against a
// real model server, Ollama, which test/docker/assistant/e2e.sh starts:
// called as kind ollama (/api/chat, NDJSON) and as kind openai_compatible
// (its /v1/chat/completions, SSE), each asked a question it must answer with
// a tool, the tool's result handed back, and the answer read; and the model
// list of each.
package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/assistant/model"
)

func engine(t *testing.T) *fib.Engine {
	t.Helper()
	e, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	return e
}

func env(t *testing.T) (base, modelName string) {
	base, modelName = os.Getenv("FIT_ASSISTANT_OLLAMA"), os.Getenv("FIT_ASSISTANT_MODEL")
	if base == "" || modelName == "" {
		t.Skip("FIT_ASSISTANT_OLLAMA and FIT_ASSISTANT_MODEL are not set: run test/docker/assistant/e2e.sh")
	}
	return base, modelName
}

func call(t *testing.T, c *model.Client, p *model.Provider, r *model.Request) (*model.Result, []model.Event) {
	t.Helper()
	type out struct {
		res *model.Result
		err error
	}
	ch := make(chan out, 1)
	var events []model.Event
	c.Stream(p, r, 0, func(e model.Event) { events = append(events, e) }, func(res *model.Result, err error) { ch <- out{res, err} })
	select {
	case o := <-ch:
		if o.err != nil {
			t.Fatalf("%s: %v", p.Kind, o.err)
		}
		return o.res, events
	case <-time.After(4 * time.Minute):
		t.Fatalf("%s: no answer", p.Kind)
		return nil, nil
	}
}

var statusTool = model.Tool{Name: "get_status", Description: "Returns whether a listener of the gateway is up. Always call it to learn a listener's state.",
	Parameters: json.RawMessage(`{"type":"object","properties":{"listener":{"type":"string","description":"The listener's name."}},"required":["listener"]}`)}

// TestToolRoundTrip: each kind asks the model a question it answers by
// calling get_status; the tool's result goes back as the kind's API wants
// it, and the model answers with text.
func TestToolRoundTrip(t *testing.T) {
	base, modelName := env(t)
	c := model.NewClient(engine(t), model.Limits{})
	defer c.Close()
	for _, p := range []*model.Provider{
		{Name: "ollama", Kind: model.KindOllama, BaseURL: base, FirstByteTimeout: 3 * time.Minute, Retries: 1},
		{Name: "compatible", Kind: model.KindOpenAICompatible, BaseURL: base + "/v1", FirstByteTimeout: 3 * time.Minute, Retries: 1},
	} {
		t.Run(p.Kind, func(t *testing.T) {
			temp := 0.0
			req := &model.Request{Model: modelName, Temperature: &temp, MaxTokens: 256, Tools: []model.Tool{statusTool},
				System:   "You are the assistant of a gateway. To learn whether a listener is up you must call the get_status tool; never guess.",
				Messages: []model.Message{{Role: model.RoleUser, Content: "Is the listener named web up? Call get_status to find out."}}}
			// A small model does not always call the tool at once: it is
			// asked again, a few times at most.
			var res *model.Result
			for i := 0; i < 3; i++ {
				var events []model.Event
				res, events = call(t, c, p, req)
				if len(res.ToolCalls) > 0 {
					if events[len(events)-1].Type != model.EventToolCall && !hasToolEvent(events) {
						t.Errorf("no tool_call event: %+v", events)
					}
					break
				}
				t.Logf("attempt %d: no tool call, the model said %q", i+1, res.Text)
			}
			if len(res.ToolCalls) == 0 {
				t.Fatalf("the model never called the tool")
			}
			tc := res.ToolCalls[0]
			var args map[string]any
			if tc.Name != "get_status" || tc.Invalid || json.Unmarshal(tc.Arguments, &args) != nil {
				t.Fatalf("the tool call: %+v", tc)
			}
			t.Logf("%s called %s(%s); usage %+v", p.Kind, tc.Name, tc.Arguments, res.Usage)
			if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 {
				t.Errorf("no usage: %+v", res.Usage)
			}
			req.Messages = append(req.Messages, res.Message(),
				model.Message{Role: model.RoleTool, ToolCallID: tc.ID, ToolName: tc.Name, Content: `{"listener":"web","up":true,"connections":12}`})
			res, events := call(t, c, p, req)
			if strings.TrimSpace(res.Text) == "" || len(events) == 0 {
				t.Fatalf("no answer after the tool's result: %+v", res)
			}
			t.Logf("%s answered %q (%s)", p.Kind, res.Text, res.StopReason)
		})
	}
}

func hasToolEvent(events []model.Event) bool {
	for _, e := range events {
		if e.Type == model.EventToolCall {
			return true
		}
	}
	return false
}

// TestModels: both kinds list the model pulled.
func TestModels(t *testing.T) {
	base, modelName := env(t)
	c := model.NewClient(engine(t), model.Limits{})
	defer c.Close()
	for _, p := range []*model.Provider{
		{Name: "ollama", Kind: model.KindOllama, BaseURL: base},
		{Name: "compatible", Kind: model.KindOpenAICompatible, BaseURL: base + "/v1"},
	} {
		ch := make(chan []model.ModelInfo, 1)
		c.Models(p, func(m []model.ModelInfo, err error) {
			if err != nil {
				t.Errorf("%s: %v", p.Kind, err)
			}
			ch <- m
		})
		found := false
		for _, m := range <-ch {
			found = found || m.ID == modelName || strings.TrimSuffix(m.ID, ":latest") == modelName
		}
		if !found {
			t.Errorf("%s does not list %s", p.Kind, modelName)
		}
	}
}

// TestCancelledCall: a call cancelled while the model answers ends at once,
// with the cancel.
func TestCancelledCall(t *testing.T) {
	base, modelName := env(t)
	c := model.NewClient(engine(t), model.Limits{})
	defer c.Close()
	p := &model.Provider{Name: "ollama", Kind: model.KindOllama, BaseURL: base}
	done := make(chan error, 1)
	var k *model.Call
	first := make(chan struct{}, 1)
	k = c.Stream(p, &model.Request{Model: modelName, MaxTokens: 512, Messages: []model.Message{{Role: model.RoleUser, Content: "Count from one to two hundred in words."}}}, 0,
		func(model.Event) {
			select {
			case first <- struct{}{}:
			default:
			}
		}, func(_ *model.Result, err error) { done <- err })
	select {
	case <-first:
	case <-time.After(3 * time.Minute):
		t.Fatal("no first piece")
	}
	start := time.Now()
	k.Cancel(nil)
	select {
	case err := <-done:
		if err == nil || time.Since(start) > 5*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled call did not end")
	}
}
