package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
	"github.com/lesismal/fit/llm/internal/wire"
	"github.com/lesismal/fit/upstream"
)

// testEngine is the engine the nodes run on and dial their upstreams on.
var testEngine = sync.OnceValue(func() *fib.Engine {
	e, _, err := upstream.StartEngine()
	if err != nil {
		panic(err)
	}
	return e
})

var (
	local  = model.Identity{Role: model.RoleLocal, ID: "console"}
	asHub  = model.Identity{Role: model.RoleHub, ID: "hub-t"}
	client = model.Identity{Role: model.RoleProxy, ID: "proxy-t"}
)

// editor is the config edits of a node's member: it keeps the member as
// JSON, and fails every edit with err when set.
type editor struct {
	mu  sync.Mutex
	cur []byte
	err error
}

func (e *editor) edit(_ string, _ []string, fn func([]byte) ([]byte, error)) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return nil, e.err
	}
	b, err := fn(e.cur)
	if err != nil {
		return nil, err
	}
	e.cur = b
	return nil, nil
}

func (e *editor) set(cur string, err error) {
	e.mu.Lock()
	e.cur, e.err = []byte(cur), err
	e.mu.Unlock()
}

func (e *editor) member(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(e.cur, &m); err != nil {
		t.Fatalf("member %s: %v", e.cur, err)
	}
	return m
}

// startNode runs member through an app.Manager whose config edits go to ed.
func startNode(t *testing.T, member *config.LLMNode, ed *editor) *Node {
	t.Helper()
	if ed == nil {
		ed = &editor{}
	}
	if member.ID == "" {
		member.ID = "node-t"
	}
	mgr := app.NewManager(testEngine(), nil, nil, ed.edit)
	if err := mgr.Apply(&config.Config{LLM: &config.LLM{Node: member}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mgr.Stop(ctx)
	})
	n, _ := role.Find(testEngine(), model.RoleNode).(*Node)
	if n == nil {
		t.Fatal("no node running")
	}
	return n
}

func noSudo() *config.LLMNodeSettings {
	off := false
	return &config.LLMNodeSettings{GPUSudo: &off}
}

// call invokes a method of n as caller.
func call(n *Node, caller model.Identity, method string, req any) (any, error) {
	raw, _ := json.Marshal(req)
	return n.Router.Invoke(context.Background(), caller, method, raw)
}

func code(err error) int {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return 0
}

// follow follows job id to its end, and returns its output and final state.
func follow(t *testing.T, n *Node, caller model.Identity, id int64) (string, model.Job, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, _ := json.Marshal(model.JobGetReq{ID: id})
	st, err := n.Router.OpenLocal(ctx, caller, model.MethodNodeJobFollow, raw)
	if err != nil {
		return "", model.Job{}, err
	}
	var out string
	for {
		b, err := st.Recv(ctx)
		if errors.Is(err, io.EOF) {
			var end model.Job
			err = json.Unmarshal(st.Result(), &end)
			return out, end, err
		}
		if err != nil {
			return out, model.Job{}, err
		}
		var j model.Job
		_ = json.Unmarshal(b, &j)
		out += j.Output
	}
}

// fakeTools puts shell scripts standing in for the tools a node runs on a
// PATH of their own: name -> script body.
func fakeTools(t *testing.T, tools map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
