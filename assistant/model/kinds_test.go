package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/assistant/internal/fakeapi"
)

const testKey = "sk-test-0123456789"

// engine returns a fib engine that runs until the test ends.
func engine(t testing.TB) *fib.Engine {
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

func newClient(t testing.TB, lim Limits) *Client {
	c := NewClient(engine(t), lim)
	t.Cleanup(c.Close)
	return c
}

// outcome is what a call heard: its events and its end.
type outcome struct {
	events []Event
	res    *Result
	err    error
}

// stream makes the call and waits for its end, at most 10s.
func stream(t *testing.T, c *Client, p *Provider, r *Request, max int64) outcome {
	t.Helper()
	var mu sync.Mutex
	var o outcome
	done := make(chan struct{})
	c.Stream(p, r, max, func(e Event) {
		mu.Lock()
		o.events = append(o.events, e)
		mu.Unlock()
	}, func(res *Result, err error) {
		mu.Lock()
		o.res, o.err = res, err
		mu.Unlock()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the call did not end")
	}
	mu.Lock()
	defer mu.Unlock()
	return o
}

var statusTool = Tool{Name: "get_status", Description: "A listener's status.",
	Parameters: json.RawMessage(`{"type":"object","properties":{"listener":{"type":"string"}},"required":["listener"]}`)}

// TestKindsRoundTrip: each kind calls its recorded API, takes the tool call
// of the first answer, hands the tool's result back as that API wants it,
// and gets the second answer's text; the events stream in order, the usage
// is counted, and the key goes only in the kind's own header.
func TestKindsRoundTrip(t *testing.T) {
	fake := fakeapi.Start(t, testKey)
	c := newClient(t, Limits{})
	temp := 0.2
	for _, kind := range fakeapi.Kinds {
		t.Run(kind, func(t *testing.T) {
			p := &Provider{Name: "p-" + kind, Kind: kind, BaseURL: fake.BaseURL(kind), Key: testKey, APIVersion: "2024-10-21",
				Headers: map[string]string{"X-Org": "fit"}, Retries: 0}
			if kind == KindOllama {
				p.Key = ""
			}
			model := map[string]string{KindAnthropic: "claude-test", KindGemini: "gemini-test", KindOllama: "qwen-test", KindAzureOpenAI: "dep"}[kind]
			if model == "" {
				model = "gpt-test"
			}
			req := &Request{Model: model, System: "You are fit's assistant.", Tools: []Tool{statusTool}, Temperature: &temp,
				Messages: []Message{{Role: RoleUser, Content: "Is the listener web up?"}}}
			if kind == KindAnthropic || kind == KindOpenAIResponses {
				req.Thinking = ThinkingLow
			}
			o := stream(t, c, p, req, 0)
			if o.err != nil {
				t.Fatalf("turn 1: %v", o.err)
			}
			res := o.res
			if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "get_status" || string(res.ToolCalls[0].Arguments) != `{"listener":"web"}` && string(res.ToolCalls[0].Arguments) != `{"listener": "web"}` {
				t.Fatalf("turn 1's tool calls: %+v", res.ToolCalls)
			}
			last := o.events[len(o.events)-1]
			if last.Type != EventToolCall || last.ToolCall.Name != "get_status" {
				t.Errorf("the last event: %+v", last)
			}
			if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 || res.Attempts != 1 {
				t.Errorf("turn 1's usage: %+v, attempts %d", res.Usage, res.Attempts)
			}
			if kind != KindOpenAI && kind != KindAzureOpenAI && kind != KindOpenAICompatible && kind != KindLLMProxy && res.Thinking == "" {
				t.Errorf("turn 1 has no thinking")
			}
			call := res.ToolCalls[0]
			req.Messages = append(req.Messages, res.Message(),
				Message{Role: RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: `{"listener":"web","up":true}`})
			o = stream(t, c, p, req, 0)
			if o.err != nil {
				t.Fatalf("turn 2: %v", o.err)
			}
			if o.res.Text != "The listener web is up." || len(o.res.ToolCalls) != 0 || o.res.StopReason == "" || o.res.Model == "" {
				t.Errorf("turn 2: %+v", o.res)
			}
			var text strings.Builder
			for _, e := range o.events {
				if e.Type == EventText {
					text.WriteString(e.Text)
				}
			}
			if text.String() != o.res.Text || len(o.events) < 2 {
				t.Errorf("turn 2's events: %+v", o.events)
			}
			reqs := fake.Requests()
			got := reqs[len(reqs)-1]
			if got.Header.Get("X-Org") != "fit" {
				t.Error("the provider's header was not sent")
			}
			for name, v := range got.Header {
				if strings.Contains(strings.Join(v, ","), testKey) && !map[string]bool{"Authorization": true, "X-Api-Key": true, "Api-Key": true, "X-Goog-Api-Key": true}[name] {
					t.Errorf("the key is in %s", name)
				}
			}
			if strings.Contains(string(got.Raw), testKey) {
				t.Error("the key is in the body")
			}
		})
	}
}

// TestKindsModels: each kind's model list is read from its own endpoint,
// and a bad key fails it without the key in the error.
func TestKindsModels(t *testing.T) {
	fake := fakeapi.Start(t, testKey)
	c := newClient(t, Limits{})
	for _, kind := range fakeapi.Kinds {
		p := &Provider{Name: "p", Kind: kind, BaseURL: fake.BaseURL(kind), Key: testKey, APIVersion: "v1"}
		models, err := listModels(t, c, p)
		if err != nil || len(models) == 0 {
			t.Errorf("%s: %v %v", kind, models, err)
			continue
		}
		if kind == KindGemini && (len(models) != 1 || models[0].ID != "gemini-test" || models[0].Context != 1048576) {
			t.Errorf("gemini's models: %+v", models)
		}
		if kind == KindOllama {
			continue
		}
		bad := *p
		bad.Key = "sk-wrong-0123456789"
		if _, err := listModels(t, c, &bad); err == nil || strings.Contains(err.Error(), string(bad.Key)) {
			t.Errorf("%s with a bad key: %v", kind, err)
		}
	}
}

func listModels(t *testing.T, c *Client, p *Provider) ([]ModelInfo, error) {
	t.Helper()
	type res struct {
		m   []ModelInfo
		err error
	}
	ch := make(chan res, 1)
	c.Models(p, func(m []ModelInfo, err error) { ch <- res{m, err} })
	select {
	case r := <-ch:
		return r.m, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("the model list did not come")
		return nil, nil
	}
}

// TestWrongKeyRedacted: an API that says the key back in its error has it
// replaced, and the error names the provider and the status.
func TestWrongKeyRedacted(t *testing.T) {
	fake := fakeapi.Start(t, testKey)
	c := newClient(t, Limits{})
	key := Secret("sk-wrong-abcdefgh")
	p := &Provider{Name: "openai", Kind: KindOpenAI, BaseURL: fake.BaseURL(KindOpenAI), Key: key, Retries: 3}
	o := stream(t, c, p, &Request{Model: "gpt-test", System: "s", Tools: []Tool{statusTool}, Messages: []Message{{Role: RoleUser, Content: "hi"}}}, 0)
	var ae *APIError
	if o.err == nil || !asAPIError(o.err, &ae) || ae.Status != 401 || ae.Provider != "openai" {
		t.Fatalf("a wrong key: %v", o.err)
	}
	if strings.Contains(o.err.Error(), string(key)) || !strings.Contains(o.err.Error(), "***") {
		t.Errorf("the key is not replaced: %v", o.err)
	}
	if n := len(fake.Requests()); n != 1 {
		t.Errorf("a 401 was tried %d times", n)
	}
	if fmt.Sprint(p) == "" || strings.Contains(fmt.Sprintf("%v %+v %#v %s", *p, *p, *p, p.Key), string(key)) {
		t.Error("the provider prints its key")
	}
	if b, _ := json.Marshal(p); strings.Contains(string(b), string(key)) {
		t.Error("the provider marshals its key")
	}
	if Secret("").String() != "" {
		t.Error("no key prints as something")
	}
}

func asAPIError(err error, ae **APIError) bool { return errors.As(err, ae) }
