// Package model holds the types and mesh method names shared by node, hub and client.
// Package model 存放 node、hub、client 共用的类型与互联方法名。
package model

// APIResp is the envelope of every JSON HTTP API response of the consoles (mesh replies carry the bare data; errors use
// wire's error flag).
// APIResp 控制台所有 JSON HTTP 接口响应的外层结构（互联应答直接携带数据；错误使用 wire 的错误标志）。
type APIResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Business codes.
// 业务码。
const (
	CodeOK           = 0
	CodeBadRequest   = 1000
	CodeUnauthorized = 2000
	CodeForbidden    = 2001
	CodeNotFound     = 3000
	CodeConflict     = 3001
	CodeRateLimited  = 4000
	// CodeUnavailable: the peer that should serve the request is offline, or a node cannot serve the requested model
	// right now; a hub tries another node.
	// CodeUnavailable：应处理请求的对端不在线，或节点当前不能提供所请求的模型；hub 会换一个节点。
	CodeUnavailable = 4003
	CodeInternal    = 5000
)

// API formats of model endpoints. Calls are forwarded byte for byte, so a model is only reachable through the agent
// endpoints of its own format.
// 模型端点的接口格式。调用按字节原样转发，所以模型只能经与其格式相同的 agent 接口调用。
const (
	APIOpenAI    = "openai"
	APIAnthropic = "anthropic"
)

// APIs lists every supported API format.
// APIs 列出所有支持的接口格式。
var APIs = []string{APIOpenAI, APIAnthropic}

// ProxyPaths lists, per API format, the upstream paths (relative to the model's base URL) a call may use.
// ProxyPaths 按接口格式列出调用可以使用的上游路径（相对于模型的 base URL）。
var ProxyPaths = map[string][]string{
	APIOpenAI:    {"chat/completions", "responses", "completions", "embeddings"},
	APIAnthropic: {"messages", "messages/count_tokens"},
}
