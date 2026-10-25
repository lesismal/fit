// Command response is fit's own Proxy-Wasm filter for the response's
// callbacks, written with the Proxy-Wasm Go SDK as Envoy's filters are; its
// plugin configuration says what it does:
//
//	upper   adds x-response-code to the response's header, with the status
//	        it sees; upper-cases each part of the body as it comes; and at the
//	        end adds the trailer x-body-bytes, with how much body it saw, and
//	        x-upstream-seen, with the upstream's trailer x-upstream when there
//	        is one, which it removes
//	slow:N  does what upper does, spinning N milliseconds in each body
//	        callback
//	answer  sends a local response from proxy_on_response_headers
package main

import (
	"bytes"
	"strconv"
	"strings"
	"time"

	"github.com/proxy-wasm/proxy-wasm-go-sdk/proxywasm"
	"github.com/proxy-wasm/proxy-wasm-go-sdk/proxywasm/types"
)

func main() {}

func init() { proxywasm.SetVMContext(&vmContext{}) }

type vmContext struct{ types.DefaultVMContext }

func (*vmContext) NewPluginContext(uint32) types.PluginContext { return &pluginContext{} }

type pluginContext struct {
	types.DefaultPluginContext
	mode string
	spin time.Duration
}

func (p *pluginContext) OnPluginStart(int) types.OnPluginStartStatus {
	data, _ := proxywasm.GetPluginConfiguration()
	p.mode = string(data)
	if ms, ok := strings.CutPrefix(p.mode, "slow:"); ok {
		n, _ := strconv.Atoi(ms)
		p.mode, p.spin = "upper", time.Duration(n)*time.Millisecond
	}
	return types.OnPluginStartStatusOK
}

func (p *pluginContext) NewHttpContext(uint32) types.HttpContext {
	return &httpContext{plugin: p}
}

type httpContext struct {
	types.DefaultHttpContext
	plugin *pluginContext
	seen   int
}

func (h *httpContext) OnHttpResponseHeaders(int, bool) types.Action {
	if h.plugin.mode == "answer" {
		_ = proxywasm.SendHttpResponse(418, nil, []byte("too late"), -1)
		return types.ActionPause
	}
	status, err := proxywasm.GetHttpResponseHeader(":status")
	if err != nil {
		proxywasm.LogCriticalf("no :status: %v", err)
	}
	_ = proxywasm.AddHttpResponseHeader("x-response-code", status)
	return types.ActionContinue
}

func (h *httpContext) OnHttpResponseBody(size int, end bool) types.Action {
	start := time.Now()
	for time.Since(start) < h.plugin.spin {
	}
	if size > 0 {
		body, err := proxywasm.GetHttpResponseBody(0, size)
		if err != nil {
			proxywasm.LogCriticalf("reading the body: %v", err)
			return types.ActionContinue
		}
		h.seen += len(body)
		if err := proxywasm.ReplaceHttpResponseBody(bytes.ToUpper(body)); err != nil {
			proxywasm.LogCriticalf("replacing the body: %v", err)
		}
	}
	if end {
		h.addTrailers()
	}
	return types.ActionContinue
}

func (h *httpContext) OnHttpResponseTrailers(int) types.Action {
	if v, err := proxywasm.GetHttpResponseTrailer("x-upstream"); err == nil {
		_ = proxywasm.RemoveHttpResponseTrailer("x-upstream")
		_ = proxywasm.AddHttpResponseTrailer("x-upstream-seen", v)
	}
	h.addTrailers()
	return types.ActionContinue
}

func (h *httpContext) addTrailers() {
	_ = proxywasm.AddHttpResponseTrailer("x-body-bytes", strconv.Itoa(h.seen))
}
