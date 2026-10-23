package wasm

// A WebAssembly encoder for the tests' own small modules, written out
// instruction by instruction: plugins that misbehave in ways a compiler would
// not let them, built without a toolchain.

// The instructions the tests' modules use.
const (
	opUnreachable = 0x00
	opIf          = 0x04
	opEnd         = 0x0b
	opCall        = 0x10
	opLocalGet    = 0x20
	opGlobalGet   = 0x23
	opI32Const    = 0x41
	opI32Eqz      = 0x45
	opI32Ne       = 0x47
	blockEmpty    = 0x40
	typeI32       = 0x7f
)

// wasmImport is a function a module imports, of i32 params and results.
type wasmImport struct {
	module, name    string
	params, results int
}

// wasmFunc is a function a module defines, exported as export unless that is
// empty, of i32 params and results; body leaves out the final end.
type wasmFunc struct {
	export          string
	params, results int
	body            []byte
}

// wasmModule describes a module: its imports and functions, a memory of
// pages exported as "memory" unless pages is 0, and, when global is set, a
// mutable i32 global of that value exported as "global".
type wasmModule struct {
	imports []wasmImport
	funcs   []wasmFunc
	pages   int
	global  *int32
}

func uleb(b []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

func sleb(b []byte, v int64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && c&0x40 == 0) || (v == -1 && c&0x40 != 0) {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func wasmName(b []byte, s string) []byte {
	return append(uleb(b, uint64(len(s))), s...)
}

func wasmSection(b []byte, id byte, count int, items []byte) []byte {
	content := append(uleb(nil, uint64(count)), items...)
	return append(uleb(append(b, id), uint64(len(content))), content...)
}

func wasmType(b []byte, params, results int) []byte {
	b = uleb(append(b, 0x60), uint64(params))
	for range params {
		b = append(b, typeI32)
	}
	b = uleb(b, uint64(results))
	for range results {
		b = append(b, typeI32)
	}
	return b
}

// i32 is the instruction i32.const v.
func i32(v int32) []byte { return sleb([]byte{opI32Const}, int64(v)) }

// cat joins instructions.
func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// encode returns the module's binary.
func (m wasmModule) encode() []byte {
	b := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	// One type for each import and function, in that order.
	var types []byte
	for _, im := range m.imports {
		types = wasmType(types, im.params, im.results)
	}
	for _, f := range m.funcs {
		types = wasmType(types, f.params, f.results)
	}
	b = wasmSection(b, 1, len(m.imports)+len(m.funcs), types)
	if len(m.imports) > 0 {
		var imports []byte
		for i, im := range m.imports {
			imports = uleb(append(wasmName(wasmName(imports, im.module), im.name), 0x00), uint64(i))
		}
		b = wasmSection(b, 2, len(m.imports), imports)
	}
	var funcs []byte
	for i := range m.funcs {
		funcs = uleb(funcs, uint64(len(m.imports)+i))
	}
	b = wasmSection(b, 3, len(m.funcs), funcs)
	if m.pages > 0 {
		b = wasmSection(b, 5, 1, uleb([]byte{0x00}, uint64(m.pages)))
	}
	if m.global != nil {
		b = wasmSection(b, 6, 1, append(cat([]byte{typeI32, 0x01}, i32(*m.global)), opEnd))
	}
	var exports []byte
	n := 0
	if m.pages > 0 {
		exports, n = append(wasmName(exports, "memory"), 0x02, 0x00), n+1
	}
	if m.global != nil {
		exports, n = append(wasmName(exports, "global"), 0x03, 0x00), n+1
	}
	for i, f := range m.funcs {
		if f.export != "" {
			exports, n = uleb(append(wasmName(exports, f.export), 0x00), uint64(len(m.imports)+i)), n+1
		}
	}
	b = wasmSection(b, 7, n, exports)
	var code []byte
	for _, f := range m.funcs {
		body := append(append([]byte{0x00}, f.body...), opEnd) // no locals
		code = append(uleb(code, uint64(len(body))), body...)
	}
	return wasmSection(b, 10, len(m.funcs), code)
}
