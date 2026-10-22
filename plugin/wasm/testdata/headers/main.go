//go:build wasip1

// Command headers is the plugin the wasm middleware's tests run: it edits
// fields, answers a request itself, and misbehaves on purpose.
package main

import "github.com/lesismal/fit/plugin/wasm/guest"

func init() {
	guest.OnRequest(func() guest.Action {
		switch guest.Path() {
		case "/deny":
			v, _ := guest.RequestHeader("X-Deny")
			guest.SetResponseHeader("X-Denied-By", "plugin")
			return guest.Respond(403, "denied: "+v)
		case "/loop":
			for {
			}
		case "/panic":
			panic("on purpose")
		case "/grow":
			hold = append(hold, make([]byte, 64<<20))
		}
		guest.SetRequestHeader("X-Plugin", guest.Config())
		guest.DeleteRequestHeader("X-Secret")
		guest.SetResponseHeader("X-Served-By", "wasm "+guest.Method()+" "+guest.Path())
		guest.AddResponseHeader("X-Count", "one")
		guest.AddResponseHeader("X-Count", "two")
		return guest.Continue
	})
}

// hold keeps what /grow allocates.
var hold [][]byte

func main() {}
