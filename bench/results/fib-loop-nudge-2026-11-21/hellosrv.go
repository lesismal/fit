// hellosrv serves "hello" over HTTP/1.1 on :8082 with fib's defaults, for
// A/B runs of fib's loops.
package main

import (
	stdhttp "net/http"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
)

func main() {
	body := []byte("hello\n")
	app := fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		_ = c.Respond(stdhttp.StatusOK, "text/plain", body)
	})
	engine, err := fib.New(fib.DefaultConfig(), fib.Binding{Addr: ":8082", Handler: fibhttp.NewHandler(app)})
	if err != nil {
		panic(err)
	}
	if err := engine.Run(); err != nil {
		panic(err)
	}
}
