package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// The protocol versions served. Modern is the stateless revision: every
// request carries its version and the client's capabilities in its _meta,
// and there is no session. The legacy ones open a session with initialize,
// which the server names with Mcp-Session-Id.
const (
	versionModern = "2026-07-28"
	versionLegacy = "2025-11-25"
)

// legacyVersions are the revisions served with initialize, newest first.
var legacyVersions = []string{versionLegacy, "2025-06-18", "2025-03-26"}

// supportedVersions are every revision served, newest first.
var supportedVersions = append([]string{versionModern}, legacyVersions...)

// The _meta keys of the modern revision.
const (
	metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	metaServerInfo      = "io.modelcontextprotocol/serverInfo"
)

// The headers of the Streamable HTTP transport.
const (
	headerVersion = "Mcp-Protocol-Version"
	headerMethod  = "Mcp-Method"
	headerName    = "Mcp-Name"
	headerSession = "Mcp-Session-Id"
)

// JSON-RPC's error codes, and the protocol's own.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	// codeServer is an error of this server's: a bound, a token refused.
	codeServer = -32000
	// codeLegacyResourceNotFound is a resource not found before the modern
	// revision, which uses codeInvalidParams.
	codeLegacyResourceNotFound = -32002
	codeHeaderMismatch         = -32020
	codeUnsupportedVersion     = -32022
)

// request is a JSON-RPC message a client sent: a request, with an ID, or a
// notification, without one.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	// Result and Error make it a response, which a client does not send to
	// this server: it asks the client nothing.
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// isNotification reports whether m is a notification, or a response: a
// message without an answer.
func (m *request) isNotification() bool { return len(m.ID) == 0 || string(m.ID) == "null" }

// params are what every request's params may carry beside its own: the
// name or URI the request is for, and _meta.
type params struct {
	Name string                     `json:"name"`
	URI  string                     `json:"uri"`
	Meta map[string]json.RawMessage `json:"_meta"`
}

// version returns the protocol version the request's _meta declares, and
// whether it declares one.
func (p *params) version() (string, bool) {
	raw, ok := p.Meta[metaProtocolVersion]
	if !ok {
		return "", false
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v, true
}

// rpcError is a JSON-RPC error.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// response is a JSON-RPC response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// resultOf is the response to the request id, which has one.
func resultOf(id json.RawMessage, v any) []byte {
	data, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Result: v})
	return data
}

func errorOf(id json.RawMessage, code int, msg string, data any) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
	return b
}

// headerValue decodes a header the client mirrored from the body: as it
// is, or Base64 between =?base64? and ?=. ok is false for a value that is
// marked so but does not decode.
func headerValue(v string) (string, bool) {
	inner, ok := strings.CutPrefix(v, "=?base64?")
	if !ok {
		return v, true
	}
	inner, ok = strings.CutSuffix(inner, "?=")
	if !ok {
		return v, true
	}
	b, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return "", false
	}
	return string(b), true
}
