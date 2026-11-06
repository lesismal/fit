package cluster

import (
	"fmt"
	"strings"

	"github.com/lesismal/fit/app/console"
)

// rolloutsBody is GET /api/rollouts's answer.
type rolloutsBody struct {
	MaxActive int       `json:"max_active"`
	PlanTTLMs int64     `json:"plan_ttl_ms"`
	ReadOnly  string    `json:"read_only,omitempty"`
	Rollouts  []Rollout `json:"rollouts"`
}

func (m *Manager) rolloutRoutes(api *console.API) {
	api.Handle("GET", "/api/rollouts", func(r *console.Request) {
		m.mu.Lock()
		ro := m.readOnly
		m.mu.Unlock()
		_ = r.JSON(200, rolloutsBody{MaxActive: MaxActiveRollouts, PlanTTLMs: PlanTTL.Milliseconds(), ReadOnly: ro, Rollouts: m.Rollouts()})
	})
	api.Handle("POST", "/api/rollouts", m.planRoute)
	api.HandlePrefix("", "/api/rollouts/", m.rolloutRoute)
}

// bodyBounded answers 413 for a body past MaxBodyBytes.
func bodyBounded(r *console.Request) bool {
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("a request is %d bytes at most", MaxBodyBytes))
		return false
	}
	return true
}

// planRoute answers POST /api/rollouts: 202 with the rollout planning,
// which GET /api/rollouts/<id> follows.
func (m *Manager) planRoute(r *console.Request) {
	var req RolloutRequest
	if !bodyBounded(r) || !r.Decode(&req) {
		return
	}
	user := r.User
	r.Go(func() {
		ro, err := m.PlanRollout(user, &req)
		m.answerRollout(r, 202, ro, err)
	})
}

// answerRollout answers a rollout, or its error.
func (m *Manager) answerRollout(r *console.Request, status int, ro Rollout, err error) {
	if err == errClosed {
		_ = console.Error(r.Context, 503, err.Error())
		return
	}
	m.answer(r, status, ro, err)
}

// rolloutRoute answers /api/rollouts/<id> and /api/rollouts/<id>/<action>.
func (m *Manager) rolloutRoute(r *console.Request) {
	id, action, _ := strings.Cut(strings.TrimPrefix(r.Path, "/api/rollouts/"), "/")
	method := r.Request.Method
	switch {
	case action == "" && method == "GET":
		ro, ok := m.RolloutByID(id)
		if !ok {
			_ = console.Error(r.Context, 404, errNoRollout(id).Error())
			return
		}
		_ = r.JSON(200, ro)
	case action == "" && method == "DELETE":
		user := r.User
		r.Go(func() {
			_, err := m.SteerRollout(user, id, SteerDiscard)
			if err != nil {
				m.answerRollout(r, 0, Rollout{}, err)
				return
			}
			_ = r.JSON(200, map[string]string{"discarded": id})
		})
	case action == "start" && method == "POST":
		var req StartRequest
		if !bodyBounded(r) || !r.Decode(&req) {
			return
		}
		user := r.User
		r.Go(func() {
			ro, err := m.StartRollout(user, id, &req)
			m.answerRollout(r, 200, ro, err)
		})
	case (action == SteerPause || action == SteerResume || action == SteerGo || action == SteerAbort || action == SteerRollback) && method == "POST":
		user := r.User
		r.Go(func() {
			ro, err := m.SteerRollout(user, id, action)
			m.answerRollout(r, 200, ro, err)
		})
	case action == "":
		r.Header().Set("Allow", "GET, DELETE")
		_ = console.Error(r.Context, 405, "the method is not allowed")
	default:
		_ = console.Error(r.Context, 404, "no such endpoint")
	}
}
