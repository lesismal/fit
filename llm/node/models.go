package node

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
)

// Model checks: a check probes the upstream's model list; enabled models are
// checked every checkEvery, and those in error every checkErrorEvery, so a
// model that came back is served again soon.
var (
	checkTick       = 5 * time.Second
	checkEvery      = 60 * time.Second
	checkErrorEvery = 15 * time.Second
	checkTimeout    = 15 * time.Second
)

// defaultAnthropicVersion is sent upstream when the caller sent no
// anthropic-version.
const defaultAnthropicVersion = "2023-06-01"

// modelRT is the runtime state of a model (not in the config).
type modelRT struct {
	err       string
	checkedAt int64
	inflight  int
	checking  bool
	// probes counts the probes started; applied is the newest probe whose
	// outcome (or a later proxy outcome) the state holds. A probe that
	// ends after a newer one has been applied is stale and dropped, so a
	// slow background check cannot overwrite a fresher result.
	probes, applied uint64
}

func key(name, api string) string { return api + "\x00" + name }

// maskKey hides an API key except a few characters at both ends.
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 12 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// isMasked reports whether k is a masked key sent back unchanged.
func isMasked(k string) bool { return strings.Contains(k, "****") }

// find returns the index of the model name (of api, when given) in models.
func find(models []model.Model, name, api string) (int, error) {
	idx := -1
	for i, m := range models {
		if m.Name == name && (api == "" || m.API == api) {
			if idx >= 0 {
				return -1, mesh.BadRequest("model %s exists for several API formats: give api", name)
			}
			idx = i
		}
	}
	if idx < 0 {
		return -1, mesh.Errorf(model.CodeNotFound, "model %s not found", name)
	}
	return idx, nil
}

func (n *Node) viewLocked(m model.Model) model.ModelView {
	v := model.ModelView{Name: m.Name, API: m.API, State: model.ModelReady, Description: m.Description,
		MaxInflight: m.MaxInflight}
	if r := n.rt[key(m.Name, m.API)]; r != nil {
		v.Inflight, v.CheckedAt, v.Error = r.inflight, r.checkedAt, r.err
	}
	switch {
	case m.Disabled:
		v.State = model.ModelDisabled
	case v.Error != "":
		v.State = model.ModelError
	}
	return v
}

func (n *Node) viewsLocked() []model.ModelView {
	out := make([]model.ModelView, 0, len(n.models))
	for _, m := range n.models {
		out = append(out, n.viewLocked(m))
	}
	return out
}

func (n *Node) listModels() []model.Model {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := slices.Clone(n.models)
	if out == nil {
		out = []model.Model{}
	}
	for i := range out {
		out[i].APIKey = maskKey(out[i].APIKey)
	}
	return out
}

// validModel checks and normalizes a model as the config will.
func validModel(m *model.Model) error {
	m.Name, m.Upstream, m.BaseURL = strings.TrimSpace(m.Name), strings.TrimSpace(m.Upstream), strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	m.APIKey, m.Description, m.APIVersion = strings.TrimSpace(m.APIKey), strings.TrimSpace(m.Description), strings.TrimSpace(m.APIVersion)
	c := config.LLMModel{Name: m.Name, API: m.API, BaseURL: m.BaseURL, Upstream: m.Upstream, APIKey: m.APIKey,
		APIVersion: m.APIVersion, Description: m.Description, MaxInflight: m.MaxInflight}
	if isMasked(c.APIKey) {
		c.APIKey = ""
	}
	if _, msg := config.CheckLLMModel(&c); msg != "" {
		return mesh.BadRequest("%s", msg)
	}
	return nil
}

// editModels changes the models of the node's member with fn, and waits for
// the config to be applied.
func (n *Node) editModels(ctx context.Context, fn func(models []model.Model) ([]model.Model, error)) error {
	return n.Edit(ctx, func(m map[string]json.RawMessage) error {
		var models []model.Model
		if raw, ok := m["models"]; ok {
			if err := json.Unmarshal(raw, &models); err != nil {
				return err
			}
		}
		models, err := fn(models)
		if err != nil {
			return err
		}
		if len(models) > config.MaxLLMModels {
			return mesh.BadRequest("at most %d models", config.MaxLLMModels)
		}
		return role.Set(m, "models", models)
	})
}

// saveModel adds a model, or replaces the one with the same name and API (an
// empty or masked key keeps the one kept), in the config.
func (n *Node) saveModel(ctx context.Context, caller model.Identity, req *model.Model) (any, error) {
	m := *req
	if err := validModel(&m); err != nil {
		return nil, err
	}
	err := n.editModels(ctx, func(models []model.Model) ([]model.Model, error) {
		idx := slices.IndexFunc(models, func(x model.Model) bool { return x.Name == m.Name && x.API == m.API })
		if m.APIKey == "" || isMasked(m.APIKey) {
			m.APIKey = ""
			if idx >= 0 {
				m.APIKey = models[idx].APIKey
			}
		}
		if idx >= 0 {
			models[idx] = m
		} else {
			models = append(models, m)
		}
		return models, nil
	})
	if err != nil {
		return nil, err
	}
	n.log.Infof("model %s (%s) saved by %s, base_url %s", m.Name, m.API, caller.Role, m.BaseURL)
	m.APIKey = maskKey(m.APIKey)
	return m, nil
}

func (n *Node) deleteModel(ctx context.Context, caller model.Identity, req *model.NameReq) (any, error) {
	err := n.editModels(ctx, func(models []model.Model) ([]model.Model, error) {
		i, err := find(models, req.Name, req.API)
		if err != nil {
			return nil, err
		}
		return slices.Delete(models, i, i+1), nil
	})
	if err != nil {
		return nil, err
	}
	n.log.Infof("model %s deleted by %s", req.Name, caller.Role)
	return struct{}{}, nil
}

func (n *Node) checkModel(ctx context.Context, _ model.Identity, req *model.NameReq) (any, error) {
	n.mu.Lock()
	i, err := find(n.models, req.Name, req.API)
	var m model.Model
	if err == nil {
		m = n.models[i]
	}
	n.mu.Unlock()
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	n.check(m, func() { close(done) })
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.viewLocked(m), nil
}

// check probes the upstream of m, records the result and calls done. It
// does not wait: the probe goes through the asynchronous client.
func (n *Node) check(m model.Model, done func()) {
	var seq uint64
	n.mu.Lock()
	if r := n.rt[key(m.Name, m.API)]; r != nil {
		r.probes++
		seq = r.probes
	}
	n.mu.Unlock()
	n.probeModel(m, func(err error) {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		n.setState(m, msg, true, seq)
		if done != nil {
			done()
		}
	})
}

// setModelErr records the state of a model; checked also sets the check
// time. It supersedes the probes in flight.
func (n *Node) setModelErr(m model.Model, msg string, checked bool) {
	n.setState(m, msg, checked, 0)
}

// setState records the state of a model: the outcome of probe seq, or of
// a call when seq is 0. A probe older than the state it would replace is
// dropped.
func (n *Node) setState(m model.Model, msg string, checked bool, seq uint64) {
	n.mu.Lock()
	r := n.rt[key(m.Name, m.API)]
	if r != nil && seq != 0 && seq <= r.applied {
		n.mu.Unlock()
		return
	}
	changed := r != nil && r.err != msg
	if r != nil {
		if seq == 0 {
			seq = r.probes
		}
		r.applied = seq
		if changed && msg != "" {
			n.log.Warnf("model %s (%s) unavailable: %s", m.Name, m.API, msg)
		} else if changed {
			n.log.Infof("model %s (%s) available again", m.Name, m.API)
		}
		r.err = msg
		if checked {
			r.checkedAt = time.Now().Unix()
		}
	}
	n.mu.Unlock()
	if r != nil {
		n.changed()
	}
}

// probeModel calls the upstream's model list with the model's credentials,
// and done with the outcome: GET <base_url>/models (openai, anthropic,
// gemini, cohere), <base_url>/openai/models?api-version= (azure_openai) or
// <base_url>/api/tags (ollama). The upstream counts as reachable unless the
// request fails, or the answer is 401 / 403 (bad key) or 5xx: a server
// without the list (404, 405) still serves calls.
func (n *Node) probeModel(m model.Model, done func(error)) {
	req, err := stdhttp.NewRequest(stdhttp.MethodGet, probeURL(m), nil)
	if err != nil {
		done(err)
		return
	}
	setAuth(req.Header, m, nil)
	n.probe.Do(req, func(resp *fibclientResponse, err error) {
		if err != nil {
			done(fmt.Errorf("upstream unreachable: %v", unwrapURLErr(err)))
			return
		}
		body, _ := resp.BodyBytes()
		if len(body) > 512 {
			body = body[:512]
		}
		if resp.StatusCode == stdhttp.StatusUnauthorized || resp.StatusCode == stdhttp.StatusForbidden || resp.StatusCode >= 500 {
			done(fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
			return
		}
		done(nil)
	})
}

// probeURL is the URL of m's upstream model list.
func probeURL(m model.Model) string {
	switch m.API {
	case model.APIAzureOpenAI:
		return m.BaseURL + "/openai/models?api-version=" + url.QueryEscape(m.APIVersion)
	case model.APIOllama:
		return m.BaseURL + "/api/tags"
	}
	return m.BaseURL + "/models"
}

// setAuth sets the upstream auth and behaviour headers of m's kind. hdr
// are the caller's pass-through headers (model.ProxyHeaders); no header of
// the caller's goes upstream otherwise, its own key included.
func setAuth(h stdhttp.Header, m model.Model, hdr map[string]string) {
	pass := func(k string) string {
		for hk, v := range hdr {
			if strings.EqualFold(hk, k) {
				return v
			}
		}
		return ""
	}
	key := m.APIKey
	switch m.API {
	case model.APIAnthropic:
		if key != "" {
			h.Set("x-api-key", key)
		}
		v := pass("anthropic-version")
		if v == "" {
			v = defaultAnthropicVersion
		}
		h.Set("anthropic-version", v)
		if v := pass("anthropic-beta"); v != "" {
			h.Set("anthropic-beta", v)
		}
	case model.APIGemini:
		if key != "" {
			h.Set("x-goog-api-key", key)
		}
	case model.APIAzureOpenAI:
		if key != "" {
			h.Set("api-key", key)
		}
	default: // openai, ollama, cohere
		if key != "" {
			h.Set("Authorization", "Bearer "+key)
		}
		if v := pass("openai-beta"); v != "" && m.API == model.APIOpenAI {
			h.Set("OpenAI-Beta", v)
		}
	}
}

// checkLoop checks the enabled models in the background, one probe per
// model at a time.
func (n *Node) checkLoop() {
	defer n.wg.Done()
	for {
		now := time.Now()
		var due []model.Model
		n.mu.Lock()
		for _, m := range n.models {
			r := n.rt[key(m.Name, m.API)]
			if m.Disabled || r == nil || r.checking {
				continue
			}
			every := checkEvery
			if r.err != "" {
				every = checkErrorEvery
			}
			if now.Sub(time.Unix(r.checkedAt, 0)) >= every {
				r.checking = true
				due = append(due, m)
			}
		}
		n.mu.Unlock()
		for _, m := range due {
			k := key(m.Name, m.API)
			n.check(m, func() {
				n.mu.Lock()
				if r := n.rt[k]; r != nil {
					r.checking = false
				}
				n.mu.Unlock()
			})
		}
		t := time.NewTimer(checkTick)
		select {
		case <-n.stopCh:
			t.Stop()
			return
		case <-t.C:
		}
	}
}
