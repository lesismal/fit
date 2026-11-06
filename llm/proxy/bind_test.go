package proxy

import (
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/bindtest"
)

// TestAPIRefusesHeldAddress checks the agents' API does not listen beside
// another server on its address, which fib's SO_REUSEPORT would let it.
func TestAPIRefusesHeldAddress(t *testing.T) {
	f := newFixture(t)
	addr := bindtest.Held(t, "tcp")
	s, err := startAPI(f.c, &config.LLMProxyAPI{Address: addr})
	if err == nil {
		s.stop()
	}
	bindtest.Refused(t, err, "tcp", addr)
}
