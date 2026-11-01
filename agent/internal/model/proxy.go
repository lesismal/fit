package model

// ProxyReq opens MethodNodeProxy / MethodHubProxy: one agent API call, forwarded byte for byte.
// ProxyReq 打开 MethodNodeProxy / MethodHubProxy：一次 agent 接口调用，按字节原样转发。
type ProxyReq struct {
	API   string `json:"api"`   // the protocol, one of Protocols / 接口，Protocols 之一
	Path  string `json:"path"`  // one of ProxyPaths[API] / ProxyPaths[API] 之一
	Model string `json:"model"` // the model the call names, in its body or path / 调用所指的模型，在请求体或路径中
	Body  []byte `json:"body"`  // the raw request body / 原始请求体
	// ContentType is the body's media type when it is not application/json: multipart/form-data with its boundary,
	// as OpenAI's uploads are.
	// ContentType 请求体不是 application/json 时的媒体类型：带 boundary 的 multipart/form-data，如 OpenAI 的上传。
	ContentType string `json:"content_type,omitempty"`
	// Query is the encoded query of the call's parameters that go upstream (ProxyQuery[API]), as Gemini's alt=sse.
	// Query 发往上游的调用参数（ProxyQuery[API]）编码后的查询串，如 Gemini 的 alt=sse。
	Query string `json:"query,omitempty"`
	// Header carries the few request headers that change upstream behaviour (anthropic-version, anthropic-beta,
	// openai-beta); never auth headers.
	// Header 携带少数会改变上游行为的请求头（anthropic-version、anthropic-beta、openai-beta）；从不携带认证头。
	Header map[string]string `json:"header,omitempty"`
	// BodyStream says the body is not in Body but follows, as the opener's
	// data frames, ended by its half-close, so that a large body neither
	// needs a frame of its size nor is held whole on its way.
	BodyStream bool `json:"body_stream,omitempty"`
	// BodySize is the length of a streamed body, sent upstream as its
	// Content-Length; -1 or 0 when unknown.
	BodySize int64 `json:"body_size,omitempty"`
	// Exclude lists node IDs not to pick (a hub retrying elsewhere).
	// Exclude 不要选择的节点 ID（hub 换节点重试时）。
	Exclude []string `json:"exclude,omitempty"`
}

// ProxyHeaders are the request headers ProxyReq.Header may carry (lower case).
// ProxyHeaders ProxyReq.Header 可以携带的请求头（小写）。
var ProxyHeaders = []string{"anthropic-version", "anthropic-beta", "openai-beta"}

// ProxyHead is the first stream-data frame of a proxy stream: the upstream status and content type.
// ProxyHead 转发流的第一个 stream-data 帧：上游状态码与内容类型。
type ProxyHead struct {
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Node        string `json:"node"` // the node that served it / 提供服务的节点
}

// ProxyResult is the stream-end of a proxy stream.
// ProxyResult 转发流的 stream-end。
type ProxyResult struct {
	Node       string `json:"node"`
	Model      string `json:"model"`
	Status     int    `json:"status"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"duration_ms"`
}
