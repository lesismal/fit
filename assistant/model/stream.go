package model

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"
)

// Stream sends r to p and parses its answer as it streams: on hears each
// piece, text, thinking or a whole tool call, in order, and done hears the
// whole answer, or why there is none, once, after the last piece. Both run
// on fib's goroutines, the worker that reads the answer among them, and must
// not block; done may run before Stream returns, for a call refused at once
// (a bad base URL, a key that would go in the clear). maxBytes bounds the
// answer's body; the client's MaxStreamBytes when 0.
//
// The call is tried again, as the provider's Retries allow, when it failed
// before any of its answer was taken: the connect failed, the connection
// closed before an answer, or the API answered 408, 429, 500, 502, 503,
// 504 or 529. Once an answer of 2xx has begun, it is never sent again.
func (c *Client) Stream(p *Provider, r *Request, maxBytes int64, on func(Event), done func(*Result, error)) *Call {
	if maxBytes <= 0 {
		maxBytes = c.lim.MaxStreamBytes
	}
	kind := p.Kind
	if kind == KindLLMProxy {
		kind = KindOpenAICompatible
	}
	// The body does not change between attempts; each attempt parses its
	// answer anew.
	if err := r.check(); err != nil {
		done(nil, fmt.Errorf("%s: %w", p.Name, err))
		return &Call{ended: true}
	}
	u, body := newCodec(kind).call(p, r)
	var k *Call
	var a *answer
	newSink := func() sink {
		cd := newCodec(kind)
		a = &answer{maxTools: c.lim.MaxToolCalls}
		a.emit = func(e Event) bool {
			k.mu.Lock()
			live := !k.ended && k.cause == nil
			k.mu.Unlock()
			if live && on != nil {
				on(e)
			}
			return live
		}
		f := &framer{ndjson: cd.ndjson(), max: c.lim.MaxEventBytes, handle: func(name string, data []byte) error { return cd.parse(a, name, data) }}
		return &streamSink{f: f, cd: cd, a: a}
	}
	k = c.prepare(p, stdhttp.MethodPost, u, body, maxBytes, orDur(p.FirstByteTimeout, DefaultFirstByteTimeout), newSink, func(err error) {
		if err != nil {
			done(nil, err)
			return
		}
		res := a.res
		res.Attempts = k.Attempts()
		done(&res, nil)
	})
	return k.run()
}

// check finds what a request may not hold: a tool's parameters, or a
// call's arguments, that are not JSON.
func (r *Request) check() error {
	for _, t := range r.Tools {
		if len(t.Parameters) > 0 && !json.Valid(t.Parameters) {
			return fmt.Errorf("the parameters of the tool %q are not JSON", t.Name)
		}
	}
	for _, m := range r.Messages {
		for _, tc := range m.ToolCalls {
			if len(tc.Arguments) > 0 && !json.Valid(tc.Arguments) {
				return fmt.Errorf("the arguments of the call %q are not JSON", tc.ID)
			}
		}
	}
	return nil
}

type streamSink struct {
	f  *framer
	cd codec
	a  *answer
}

func (s *streamSink) data(p []byte) error { return s.f.write(p) }

func (s *streamSink) end() error {
	if err := s.f.end(); err != nil {
		return err
	}
	return s.cd.finish(s.a)
}

// Models asks p for its list of models, and calls done once with it, or
// with why there is none: GET <base>/models (openai, openai_responses,
// openai_compatible, llm_proxy, anthropic, gemini),
// <base>/openai/models?api-version= (azure_openai) or <base>/api/tags
// (ollama). The wait is bounded by the provider's timeouts and by 30s.
func (c *Client) Models(p *Provider, done func([]ModelInfo, error)) *Call {
	u := p.BaseURL + "/models"
	switch p.Kind {
	case KindAzureOpenAI:
		u = p.BaseURL + "/openai/models?api-version=" + url.QueryEscape(p.APIVersion)
	case KindOllama:
		u = p.BaseURL + "/api/tags"
	case KindAnthropic:
		u += "?limit=1000"
	case KindGemini:
		u += "?pageSize=1000"
	}
	q := *p
	q.Timeout = min(orDur(p.Timeout, DefaultTimeout), maxModelsWait)
	var body []byte
	return c.prepare(&q, stdhttp.MethodGet, u, nil, c.lim.MaxModelsBytes, min(orDur(p.FirstByteTimeout, DefaultFirstByteTimeout), maxModelsWait),
		func() sink { body = body[:0]; return &wholeSink{buf: &body} },
		func(err error) {
			if err != nil {
				done(nil, err)
				return
			}
			models, err := parseModels(p.Kind, body)
			if err != nil {
				err = fmt.Errorf("%s: %w", p.Name, err)
			}
			done(models, err)
		}).run()
}

// wholeSink keeps a body whole; the call bounds it.
type wholeSink struct{ buf *[]byte }

func (s *wholeSink) data(p []byte) error { *s.buf = append(*s.buf, p...); return nil }
func (s *wholeSink) end() error          { return nil }

// parseModels reads a model list of kind.
func parseModels(kind string, body []byte) ([]ModelInfo, error) {
	var l struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name                       string   `json:"name"`
			Model                      string   `json:"model"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &l); err != nil {
		return nil, errors.New("the model list is not the JSON expected")
	}
	out := []ModelInfo{}
	for _, d := range l.Data {
		if d.ID != "" {
			out = append(out, ModelInfo{ID: d.ID})
		}
	}
	for _, m := range l.Models {
		id := m.Name
		if id == "" {
			id = m.Model
		}
		if kind == KindGemini {
			if len(m.SupportedGenerationMethods) > 0 && !contains(m.SupportedGenerationMethods, "generateContent") {
				continue
			}
			id = strings.TrimPrefix(id, "models/")
		}
		if id != "" {
			out = append(out, ModelInfo{ID: id, Context: m.InputTokenLimit})
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
