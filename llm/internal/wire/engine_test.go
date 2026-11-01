package wire

import (
	"sync"
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/upstream"
)

var (
	engineOnce sync.Once
	engine     *fib.Engine
)

// SharedEngine returns the engine the package's tests listen and dial on,
// started once and left running until the test binary exits.
func SharedEngine(t testing.TB) *fib.Engine {
	t.Helper()
	engineOnce.Do(func() {
		var err error
		if engine, _, err = upstream.StartEngine(); err != nil {
			panic(err)
		}
	})
	return engine
}

// Listen serves l on 127.0.0.1 and returns its address; the binding is
// removed, and its connections closed, when t ends.
func Listen(t testing.TB, l Listener) string {
	t.Helper()
	e := SharedEngine(t)
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: l})
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := e.BindingAddrs(i)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.RemoveBinding(i); l.CloseAll() })
	return addrs[0].String()
}
