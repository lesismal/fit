package console

import (
	"testing"

	"github.com/lesismal/fit/internal/bindtest"
)

// TestStartRefusesHeldAddress checks a console does not listen beside
// another server on its address, which fib's SO_REUSEPORT would let it.
func TestStartRefusesHeldAddress(t *testing.T) {
	addr := bindtest.Held(t, "tcp")
	o := options(t, discardLog(t), nil)
	o.Config.Address = addr
	c, err := Start(newEngine(t), o)
	if err == nil {
		c.Stop(t.Context())
	}
	bindtest.Refused(t, err, "tcp", addr)
}
