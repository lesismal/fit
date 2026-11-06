package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	stdhttp "net/http"
	"strings"
	"sync"
	"time"
)

// The manager in its own Admin API (I150), for fit cluster: the members,
// their states, the groups, the members' diffs against their templates,
// the series, the certificates' inventory, the logs, and rollouts planned,
// started, followed and steered, at /cluster/... behind the Admin API's
// token. The Admin API is net/http's, on goroutines of its own, not fib's
// workers: these handlers may wait for what they start.
//
// Whoever holds the Admin API's token may change the manager's own config,
// admin.cluster.admins included, so the API acts as a cluster-wide admin,
// AdminAPIUser, whatever the roles say; its actions are audited under that
// name. A console user of that name has no role (roles.go), so that no one
// logs in as the Admin API.

// AdminAPIUser is who the manager's Admin API acts as, in its audit log.
const AdminAPIUser = "Admin API"

// AdminAPIPrefix is where the Admin API serves the manager's endpoints.
const AdminAPIPrefix = "/cluster"

// AdminAPI returns the handler of the manager's endpoints in the Admin API,
// which serves it at AdminAPIPrefix (admin.Server.SetCluster).
func (m *Manager) AdminAPI() stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	p := AdminAPIPrefix
	mux.HandleFunc("GET "+p+"/nodes", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		m.mu.Lock()
		ro, maxNodes := m.readOnly, m.maxNodes
		m.mu.Unlock()
		tag := m.etag()
		apiJSON(w, 200, membersBody{Version: strings.Trim(tag, `"`), MaxNodes: maxNodes, ReadOnly: ro, Nodes: m.Members()})
	})
	mux.HandleFunc("GET "+p+"/state", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		m.mu.Lock()
		ro := m.readOnly
		m.mu.Unlock()
		apiJSON(w, 200, stateBody{IntervalMs: m.interval.Milliseconds(), TimeoutMs: m.lim.timeout.Milliseconds(), ReadOnly: ro, Nodes: m.States()})
	})
	mux.HandleFunc("GET "+p+"/groups", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		m.mu.Lock()
		ro := m.readOnly
		m.mu.Unlock()
		apiJSON(w, 200, groupsBody{Version: strings.Trim(m.etag(), `"`), MaxGroups: MaxGroups, ReadOnly: ro, Groups: m.Groups()})
	})
	mux.HandleFunc("GET "+p+"/drift", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		apiJSON(w, 200, map[string]any{"nodes": m.DriftStates()})
	})
	mux.HandleFunc("POST "+p+"/sync", m.apiSync)
	mux.HandleFunc("GET "+p+"/metrics", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		q, err := metricsQuery(r.URL.Query())
		if err == nil {
			var body MetricsBody
			if body, err = m.Metrics(q); err == nil {
				apiJSON(w, 200, body)
				return
			}
		}
		apiError(w, err)
	})
	mux.HandleFunc("GET "+p+"/certificates", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) { apiJSON(w, 200, m.Certificates()) })
	mux.HandleFunc("GET "+p+"/logs", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		q, err := tailQuery(r.URL.Query())
		if err != nil {
			apiError(w, err)
			return
		}
		select {
		case m.reads <- struct{}{}:
		default:
			apiError(w, errBusy("other reads of members are under way; try again"))
			return
		}
		ch := make(chan struct{})
		var body TailBody
		m.TailLogs(q, func(b TailBody, e error) { body, err = b, e; close(ch) })
		<-ch
		<-m.reads
		if err != nil {
			apiError(w, err)
			return
		}
		apiJSON(w, 200, body)
	})
	mux.HandleFunc("GET "+p+"/rollouts", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		m.mu.Lock()
		ro := m.readOnly
		m.mu.Unlock()
		apiJSON(w, 200, rolloutsBody{MaxActive: MaxActiveRollouts, PlanTTLMs: PlanTTL.Milliseconds(), ReadOnly: ro, Rollouts: m.Rollouts()})
	})
	mux.HandleFunc("POST "+p+"/rollouts", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req RolloutRequest
		if !apiDecode(w, r, &req) {
			return
		}
		ro, err := m.PlanRollout(AdminAPIUser, &req)
		apiRollout(w, 202, ro, err)
	})
	mux.HandleFunc(p+"/rollouts/{id}", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		id := r.PathValue("id")
		switch r.Method {
		case "GET":
			ro, ok := m.RolloutByID(id)
			if !ok {
				apiError(w, errNoRollout(id))
				return
			}
			apiJSON(w, 200, ro)
		case "DELETE":
			if _, err := m.SteerRollout(AdminAPIUser, id, SteerDiscard); err != nil {
				apiError(w, err)
				return
			}
			apiJSON(w, 200, map[string]string{"discarded": id})
		default:
			w.Header().Set("Allow", "GET, DELETE")
			apiJSON(w, 405, map[string]string{"error": "the method is not allowed"})
		}
	})
	mux.HandleFunc("POST "+p+"/rollouts/{id}/{action}", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		id, action := r.PathValue("id"), r.PathValue("action")
		switch action {
		case "start":
			var req StartRequest
			if !apiDecode(w, r, &req) {
				return
			}
			ro, err := m.StartRollout(AdminAPIUser, id, &req)
			apiRollout(w, 200, ro, err)
		case SteerPause, SteerResume, SteerGo, SteerAbort, SteerRollback:
			ro, err := m.SteerRollout(AdminAPIUser, id, action)
			apiRollout(w, 200, ro, err)
		default:
			apiJSON(w, 404, map[string]string{"error": "a rollout's actions are start, pause, resume, go, abort and rollback"})
		}
	})
	mux.HandleFunc(p+"/", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		apiJSON(w, 404, map[string]string{"error": "no such endpoint of the manager's"})
	})
	mux.HandleFunc(p, func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		apiJSON(w, 200, map[string]any{"endpoints": []string{"nodes", "state", "groups", "drift", "sync", "metrics", "certificates", "logs", "rollouts"}})
	})
	return mux
}

// apiSync answers POST /cluster/sync: the members read now, each one's
// config compared with its templates, with the parts' values, secrets
// masked; the members are told nothing. Those not read within the wait are
// answered pending.
func (m *Manager) apiSync(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req syncRequest
	if !apiDecode(w, r, &req) {
		return
	}
	names, err := m.syncTargets(&req)
	if err != nil {
		apiError(w, err)
		return
	}
	m.tpl.mu.Lock()
	if m.tpl.syncs >= MaxSyncs {
		m.tpl.mu.Unlock()
		apiError(w, errBusy("other reads of members are under way; try again"))
		return
	}
	m.tpl.syncs++
	m.tpl.wg.Add(1)
	m.tpl.mu.Unlock()
	defer func() {
		m.tpl.mu.Lock()
		m.tpl.syncs--
		m.tpl.mu.Unlock()
		m.tpl.wg.Done()
	}()
	var mu sync.Mutex
	got := map[string]MemberSync{}
	ended := make(chan struct{})
	go func() {
		m.runSync(names, false, func(o *syncOut) {
			mu.Lock()
			got[o.sync.Node] = o.sync
			mu.Unlock()
		})
		close(ended)
	}()
	timer := time.NewTimer(syncWait)
	select {
	case <-ended:
	case <-timer.C:
	case <-m.stop:
	case <-r.Context().Done():
	}
	timer.Stop()
	body := syncBody{Nodes: []MemberSync{}, Pending: []string{}}
	mu.Lock()
	for _, n := range names {
		if s, ok := got[n]; ok {
			body.Nodes = append(body.Nodes, s)
		} else {
			body.Pending = append(body.Pending, n)
		}
	}
	mu.Unlock()
	apiJSON(w, 200, body)
}

// apiDecode reads a JSON body of at most MaxBodyBytes into v, or answers
// why it cannot.
func apiDecode(w stdhttp.ResponseWriter, r *stdhttp.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		apiJSON(w, 415, map[string]string{"error": "the body must be sent as application/json"})
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	switch {
	case err != nil:
		apiJSON(w, 400, map[string]string{"error": "reading the body: " + err.Error()})
		return false
	case len(data) > MaxBodyBytes:
		apiJSON(w, 413, map[string]string{"error": fmt.Sprintf("a request is %d bytes at most", MaxBodyBytes)})
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		apiJSON(w, 400, map[string]string{"error": "the body is not the JSON expected: " + err.Error()})
		return false
	}
	return true
}

// apiRollout answers a rollout, or its error.
func apiRollout(w stdhttp.ResponseWriter, status int, ro Rollout, err error) {
	if err != nil {
		apiError(w, err)
		return
	}
	apiJSON(w, status, ro)
}

// apiError answers err at the status the console's module answers it with.
func apiError(w stdhttp.ResponseWriter, err error) {
	var fe *FieldError
	var fb *ErrForbidden
	switch {
	case err == errClosed:
		apiJSON(w, 503, map[string]string{"error": err.Error()})
	case errors.As(err, &fb):
		apiJSON(w, 403, forbiddenBody{Error: fb.Error(), Kind: KindForbidden, ErrForbidden: fb})
	case errors.As(err, &fe):
		apiJSON(w, 400, fe)
	case errors.As(err, new(errNotFound)), errors.As(err, new(errNoGroup)), errors.As(err, new(errNoTemplate)), errors.As(err, new(errNoRollout)):
		apiJSON(w, 404, map[string]string{"error": err.Error()})
	case errors.As(err, new(errBusy)):
		apiJSON(w, 429, map[string]string{"error": err.Error()})
	case errors.As(err, new(errConflict)), errors.As(err, new(errReadOnly)):
		apiJSON(w, 409, map[string]string{"error": err.Error()})
	default:
		apiJSON(w, 500, map[string]string{"error": err.Error()})
	}
}

func apiJSON(w stdhttp.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = 500, []byte(`{"error":"encoding the answer failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
