package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// cluster is a manager as the cluster tools reach it, which plans what it
// is asked and reads its member m1.
type cluster struct{}

func (cluster) Members(user, group, selector, status string) ([]byte, error) {
	return []byte(`{"members":[{"name":"m1","status":"up"}]}`), nil
}
func (cluster) Templates(user, name string) ([]byte, error) { return []byte(`{"templates":[]}`), nil }
func (cluster) Rollouts(user, id string) ([]byte, error)    { return []byte(`{"rollouts":[]}`), nil }
func (cluster) Metrics(ctx context.Context, user, group string, nodes []string) ([]byte, error) {
	return []byte(`{"members":[]}`), nil
}
func (cluster) Certs(ctx context.Context, user, group string, nodes []string, within int) ([]byte, error) {
	return []byte(`{"certificates":[]}`), nil
}
func (cluster) Logs(ctx context.Context, user, group string, nodes []string, logger, level, contains string, limit int) ([]byte, error) {
	return []byte(`{"records":[]}`), nil
}
func (cluster) PlanRollout(ctx context.Context, user string, req []byte) ([]byte, error) {
	return []byte(`{"link":"#/cluster/rollout/r1","id":"r1","state":"planned","planned_by":"` + user + `"}`), nil
}
func (cluster) CheckOp(user string, req []byte) ([]byte, error) {
	return []byte(`{"link":"#/cluster/ops?op=reload&members=m1"}`), nil
}
func (cluster) FixDrift(ctx context.Context, user string, nodes []string) ([]byte, error) {
	return []byte(`{"nothing_to_fix":["m1"]}`), nil
}
func (cluster) MemberCall(ctx context.Context, user, member, method, path string, body []byte) (int, http.Header, []byte, string, error) {
	return 200, http.Header{}, []byte(`{"version":7,"connections":{"open":1}}`), "", nil
}

// TestClusterTools: on a manager, the MCP server serves the cluster tools
// within a token's level: a read token reads the cluster and a member, a
// propose token plans a rollout, whose answer gives the workbench's link
// where a person starts it, and which no proposal holds.
func TestClusterTools(t *testing.T) {
	f := start(t, settings(`{}`, ""), &memStore{}, cluster{})
	read, tok := f.token("reader", "read"), f.token("agent", "propose")
	names := toolNames(f.modern(read, "tools/list", nil))
	if !has(names, "cluster_members") || !has(names, "cluster_certs") || has(names, "rollout_propose") {
		t.Fatalf("a read token's tools: %v", names)
	}
	if a := f.modern(read, "tools/call", map[string]any{"name": "status_get", "arguments": map[string]any{"member": "m1"}}); a.result()["isError"] != false ||
		!strings.Contains(a.text(), `"member":"m1"`) || !strings.Contains(a.text(), `"version":7`) {
		t.Fatalf("a member's status: %s", a.body)
	}
	if names := toolNames(f.modern(tok, "tools/list", nil)); !has(names, "rollout_propose") || !has(names, "cluster_op_propose") || !has(names, "drift_fix_propose") {
		t.Fatalf("a propose token's tools: %v", names)
	}
	a := f.modern(tok, "tools/call", map[string]any{"name": "rollout_propose", "arguments": map[string]any{"summary": "sync", "kind": "sync", "group": "edge"}})
	if a.result()["isError"] != false || !strings.Contains(a.text(), `"planned_by":"ada"`) || !strings.Contains(a.text(), f.url+"/#/cluster/rollout/r1") ||
		!strings.Contains(a.text(), "No tool starts it") || strings.Contains(a.text(), "/#/ai/proposals") {
		t.Fatalf("rollout_propose: %s", a.body)
	}
	if a := f.modern(tok, "tools/call", map[string]any{"name": "drift_fix_propose", "arguments": map[string]any{"summary": "fix", "nodes": []string{"m1"}}}); strings.Contains(a.text(), "No tool starts it") {
		t.Fatalf("nothing to start: %s", a.body)
	}
	if _, body := f.do("GET", "/api/m/ai/proposals", ""); strings.Contains(body, "MCP: agent") {
		t.Fatalf("a proposal: %s", body)
	}
	if a := f.modern(tok, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "x", "version": "1"}}); !strings.Contains(a.body, "rollout_propose") {
		t.Fatalf("the instructions: %s", a.body)
	}
}
