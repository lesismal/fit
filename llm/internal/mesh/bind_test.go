package mesh

import (
	"errors"
	"testing"

	"github.com/lesismal/fit/internal/bindtest"
	"github.com/lesismal/fit/llm/internal/model"
)

// TestListenRefusesHeldAddress checks a role's links listener does not
// listen beside another server on its address, which fib's SO_REUSEPORT
// would let it, and says why in its status.
func TestListenRefusesHeldAddress(t *testing.T) {
	m, _ := newTestMesh(t, model.RoleHub, "hub-self", []string{model.RoleNode})
	addr := bindtest.Held(t, "tcp")
	err := m.SetListen(model.ListenConfig{Addr: addr, Password: "pw"})
	bindtest.Refused(t, err, "tcp", addr)
	if e := m.Listen().Error; e == "" {
		t.Fatal("the listener's status has no error")
	} else {
		bindtest.Refused(t, errors.New(e), "tcp", addr)
	}
}
