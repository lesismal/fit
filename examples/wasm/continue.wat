;; continue.wasm, written out: a plugin of fit's WASM ABI that lets every
;; request through. A real one is built from Go with plugin/wasm/guest:
;;   GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
(module
  (func (export "fit_on_request") (result i32)
    i32.const 0))
