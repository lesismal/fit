package cluster

import (
	"sync"
	"time"

	"github.com/lesismal/fit/app/console"
)

// TestResult is a member's test: whether the manager reaches it, logs in
// and reads its GET /node, and the certificate it presented, whose
// fingerprint a person confirms before it is pinned.
type TestResult struct {
	OK                 bool      `json:"ok"`
	Error              *Problem  `json:"error,omitempty"`
	LatencyMs          int64     `json:"latency_ms"`
	Certificate        *CertInfo `json:"certificate,omitempty"`
	Node               *NodeInfo `json:"node,omitempty"`
	MustChangePassword bool      `json:"must_change_password,omitempty"`
}

// testAudit is a test's audit entry: the member, kept or a draft, how it is
// reached, and how the test went.
func testAudit(at time.Time, user string, mem *Member, draft bool, res *TestResult) AuditEntry {
	what := "the member as kept"
	if draft {
		what = "a draft: " + describe(mem)
	}
	e := AuditEntry{At: at, User: user, Action: ActionTest, Node: mem.Name, Outcome: AuditOK, Detail: clip(what, 512)}
	if !res.OK {
		e.Outcome = AuditFailed
		if res.Error != nil {
			e.Detail = clip(what+"; "+res.Error.Kind+": "+res.Error.Message, 512)
		}
	}
	return e
}

// testSaved answers POST /api/nodes/<name>/test: the member as kept, or, when
// the body holds one, as the body would change it, not saved.
func (m *Manager) testSaved(r *console.Request, name string) {
	in, err := unmarshalInput(r.Context.Body())
	if err != nil {
		_ = console.Error(r.Context, 400, "the body is not the JSON expected: "+err.Error())
		return
	}
	// A test of the member as kept is an operation, which an operator
	// runs too; one of a draft is the member's form, an admin's.
	need := Need{Role: RoleOperator, Nodes: []string{name}}
	if in != nil {
		need.Role = RoleAdmin
	}
	if !m.allow(r, need) {
		return
	}
	m.mu.Lock()
	i := index(&m.doc, name)
	var saved Member
	if i >= 0 {
		saved = m.doc.Members[i].clone()
	}
	n := m.nodes[name]
	m.mu.Unlock()
	if i < 0 {
		_ = console.Error(r.Context, 404, errNotFound(name).Error())
		return
	}
	mem := saved
	if in != nil {
		if in.Name == "" {
			in.Name = name
		}
		if mem, err = in.member(&saved, m.now().UTC()); err != nil {
			m.answer(r, 0, nil, err)
			return
		}
	} else if err := mem.check(); err != nil {
		m.answer(r, 0, nil, err)
		return
	}
	m.test(r, &mem, in != nil, func(res *TestResult) {
		// The kept credentials worked: the poller need not wait out a
		// backoff a refused login set.
		if res.OK && n != nil && sameLink(&n.spec, &mem) {
			n.resetLogin()
		}
	})
}

// testDraft answers POST /api/test: a member not added yet, as the body
// has it.
func (m *Manager) testDraft(r *console.Request) {
	var in Input
	if !decode(r, &in) {
		return
	}
	mem, err := in.member(nil, m.now().UTC())
	if err != nil {
		m.answer(r, 0, nil, err)
		return
	}
	if err := m.allowDraft(r.User, &mem); err != nil {
		m.answer(r, 0, nil, err)
		return
	}
	m.test(r, &mem, true, nil)
}

// allowDraft checks that user is an admin of mem, a member not saved, by
// the groups it would be in.
func (m *Manager) allowDraft(user string, mem *Member) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if roleRank(rolesIn(&m.doc, m.admins, user).on(m.groupsOf(mem))) < roleRank(RoleAdmin) {
		return &ErrForbidden{User: user, Role: RoleAdmin, Nodes: []string{mem.Name}}
	}
	return nil
}

// test tests mem on connections of its own, answering r once the test is
// over, and then calls after, when not nil, with the result, which the
// audit log records, draft saying whether mem is the member as kept. A
// browser that leaves abandons the test.
func (m *Manager) test(r *console.Request, mem *Member, draft bool, after func(*TestResult)) {
	select {
	case m.tests <- struct{}{}:
	default:
		_ = console.Error(r.Context, 429, "other tests are under way; try again")
		return
	}
	n := newNode(m, *mem)
	r.Context.Retain()
	var once sync.Once
	end := func(res *TestResult) {
		once.Do(func() {
			if after != nil {
				after(res)
			}
			m.record(testAudit(m.now().UTC(), r.User, mem, draft, res))
			_ = r.JSON(200, res)
			r.Context.Release()
			n.shutdown(func() { <-m.tests })
		})
	}
	r.Context.OnCancel(func(error) { n.t.abandon() })
	p := &poller{n: n, start: m.now()}
	res := &TestResult{}
	report := func(prob *Problem) {
		if seen := n.t.certificate(); seen != nil {
			c := *seen
			c.err = nil
			res.Certificate = &c
		}
		if prob != nil {
			prob.At = m.now().UTC()
			prob.Message = n.redact(prob.Message)
			if prob.Certificate != nil {
				// The certificate is the result's, once.
				prob.Certificate = nil
			}
			res.Error, res.MustChangePassword = prob, prob.Kind == KindPasswordChange
			end(res)
			return
		}
		info := p.answer.NodeInfo
		info.Roles, info.Capabilities = nonNil(info.Roles), nonNil(info.Capabilities)
		res.OK, res.Node, res.LatencyMs = true, &info, p.latency.Milliseconds()
		end(res)
	}
	// A failed poll step ends the test with its problem.
	p.finishFn = report
	p.get(n.p.node, &p.answer, func() { report(nil) })
}
