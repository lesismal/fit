// Package model holds the types and mesh method names shared by node, hub and proxy.
// Package model 存放 node、hub、proxy 共用的类型与互联方法名。
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

// Kinds of model endpoints (a model's API): what the upstream speaks, and so how a node calls it. Calls are
// forwarded byte for byte, so a model is only reachable through the agent endpoints of the API its kind speaks
// (Protocol).
// 模型端点的种类（模型的 API）：上游所讲的接口，也就是节点如何调用它。调用按字节原样转发，所以模型只能经其种类所讲接口
// （Protocol）的 agent 端点调用。
const (
	APIOpenAI      = "openai"       // OpenAI and every OpenAI-compatible server / OpenAI 及所有 OpenAI 兼容服务
	APIAnthropic   = "anthropic"    // Anthropic Messages
	APIGemini      = "gemini"       // Google Gemini (Generative Language API), the model in the path / 模型在路径中
	APIAzureOpenAI = "azure_openai" // Azure OpenAI deployments, called as openai / 以 openai 调用
	APIOllama      = "ollama"       // Ollama's native API / Ollama 原生接口
	APICohere      = "cohere"       // Cohere v2
)

// APIs lists every kind, as the config's llm.node.models[].api takes them.
// APIs 列出所有种类，即配置 llm.node.models[].api 接受的值。
var APIs = []string{APIOpenAI, APIAnthropic, APIGemini, APIAzureOpenAI, APIOllama, APICohere}

// Protocols lists the APIs agents speak to llm.proxy: every kind's but azure_openai, which agents call as openai.
// Protocols 列出 agent 向 llm.proxy 讲的接口：除 azure_openai 外每种的接口，azure_openai 以 openai 调用。
var Protocols = []string{APIOpenAI, APIAnthropic, APIGemini, APIOllama, APICohere}

// Protocol returns the API agents speak to reach a model of kind api.
// Protocol 返回调用 api 种类模型时 agent 所讲的接口。
func Protocol(api string) string {
	if api == APIAzureOpenAI {
		return APIOpenAI
	}
	return api
}

// ProxyPaths lists, per protocol, the calls (ProxyReq.Path) a model may get. For openai, anthropic, ollama and cohere
// it is the upstream path relative to the model's base URL; for gemini it is the method after the model,
// models/{model}:{method}.
// ProxyPaths 按接口列出模型可接受的调用（ProxyReq.Path）。openai、anthropic、ollama、cohere 为相对模型 base URL 的上游
// 路径；gemini 为模型之后的方法名，即 models/{model}:{method}。
var ProxyPaths = map[string][]string{
	APIOpenAI: {"chat/completions", "responses", "completions", "embeddings", "moderations",
		"images/generations", "images/edits", "images/variations",
		"audio/speech", "audio/transcriptions", "audio/translations", "rerank"},
	APIAnthropic: {"messages", "messages/count_tokens"},
	APIGemini:    {"generateContent", "streamGenerateContent", "countTokens", "embedContent", "batchEmbedContents"},
	APIOllama:    {"api/chat", "api/generate", "api/embed"},
	APICohere:    {"chat", "embed", "rerank"},
}

// kindLacks lists, per kind, the calls of its protocol its upstreams do not serve.
var kindLacks = map[string][]string{
	APIAzureOpenAI: {"moderations", "images/variations", "rerank"},
}

// Serves reports whether a model of kind api takes the call path of its protocol.
// Serves 判断 api 种类的模型是否接受其接口的调用 path。
func Serves(api, path string) bool {
	for _, p := range ProxyPaths[Protocol(api)] {
		if p == path {
			for _, l := range kindLacks[api] {
				if l == path {
					return false
				}
			}
			return true
		}
	}
	return false
}

// ProxyQuery lists, per protocol, the query parameters of an agent's call that go upstream (ProxyReq.Query); no
// other does, the agent's key included.
// ProxyQuery 按接口列出 agent 调用中会发往上游的查询参数（ProxyReq.Query）；其他参数（含 agent 的 Key）都不会。
var ProxyQuery = map[string][]string{
	APIGemini: {"alt"},
}
