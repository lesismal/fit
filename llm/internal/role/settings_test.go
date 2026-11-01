package role

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
)

// The limits read their defaults, are written without them, and a refused
// edit changes nothing.
func TestLimitsSettings(t *testing.T) {
	ed := &editor{}
	b := newBase(t, model.RoleHub, ed)
	ctx := context.Background()
	local := model.Identity{Role: model.RoleLocal}
	out, err := b.Router.Invoke(ctx, local, model.MethodLimits, nil)
	if err != nil || out.(model.Limits) != (model.Limits{MaxConns: 1024, MaxInflight: 1024, PingInterval: "15s", SlowTimeout: "30s", RetryInterval: "5s", StreamIdle: "2m0s"}) {
		t.Fatalf("limits %+v %v", out, err)
	}
	if _, err := b.Router.Invoke(ctx, local, model.MethodLimitsSet, json.RawMessage(`{"max_inflight": 8, "slow_timeout": "1m", "retry_interval": "5s"}`)); err != nil {
		t.Fatal(err)
	}
	lim, _ := json.Marshal(ed.member()["limits"])
	if string(lim) != `{"max_inflight":8,"slow_timeout":"1m0s"}` {
		t.Fatalf("written %s", lim)
	}
	ed.mu.Lock()
	ed.err = &config.Error{Msg: "refused"}
	ed.mu.Unlock()
	if _, err := b.Router.Invoke(ctx, local, model.MethodLimitsSet, json.RawMessage(`{"max_conns": 3}`)); err == nil {
		t.Fatal("a refused edit answered well")
	}
	if b, _ := json.Marshal(ed.member()["limits"]); string(b) != string(lim) {
		t.Fatalf("a refused edit changed %s", b)
	}
}

func TestObject(t *testing.T) {
	o := Object("a", "", "b", 0, "c", int64(0), "d", "x", "e", 2, "f", int64(3), "g", true)
	if len(o) != 4 || o["d"] != "x" || o["e"] != 2 || o["f"] != int64(3) || o["g"] != true {
		t.Fatalf("%v", o)
	}
}
