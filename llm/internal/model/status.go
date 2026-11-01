package model

// MethodStatus is the links and traffic of a process (MeshStatus): every
// link's bytes and latency, and the calls it relayed, per route. Its own
// pages only.
// MethodStatus 进程的连接与流量（MeshStatus）：每条连接的字节数与延迟，以及按路由统计的转发调用。仅自身页面可调用。
const MethodStatus = "/mesh/status"

// RTT is a link's round trips, in milliseconds, from its pings.
// RTT 连接的往返时间（毫秒），来自其 ping。
type RTT struct {
	Last float64 `json:"last"`
	// Avg is a moving average of the round trips, weighing recent ones most.
	// Avg 往返时间的移动平均，近期的权重最大。
	Avg     float64 `json:"avg"`
	Min     float64 `json:"min"`
	Samples int     `json:"samples"`
}

// LinkStats is the traffic and latency of one link: the bytes of the frames
// it carried each way, compressed as they crossed, since it was made.
// LinkStats 一条连接的流量与延迟：自连接建立以来双向传输的帧字节数（按传输时的压缩大小）。
type LinkStats struct {
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
	RTT      RTT   `json:"rtt"`
}

// RouteStats counts the model calls a role relayed on one route, since it
// started. From is the peer the calls came from, empty for a proxy's own
// agents; To is the peer they went to, empty for a node's upstream. The
// bytes are the bodies' (a request's inline body included), the calls
// running counted as far as they got.
// RouteStats 统计角色自启动以来在一条路由上转发的模型调用。From 为调用来自的对端，proxy 自身的 agent 为空；To 为调用
// 发往的对端，节点的上游为空。字节数为请求体与响应体的字节（含请求内联的请求体），进行中的调用按目前进度计入。
type RouteStats struct {
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Model     string `json:"model"`
	Calls     int64  `json:"calls"`
	Errors    int64  `json:"errors"`
	Inflight  int64  `json:"inflight"`
	ReqBytes  int64  `json:"req_bytes"`
	RespBytes int64  `json:"resp_bytes"`
	LastAt    int64  `json:"last_at,omitempty"`
}

// RouteOther is the From, To and Model of the calls counted together once
// a role counts as many routes as it may apart.
// RouteOther 角色单独统计的路由数达到上限后，其余调用合并统计时使用的 From、To 与 Model。
const RouteOther = "other"

// MeshStatus is the reply of MethodStatus. At is when it was taken, in
// milliseconds since 1970, for rates between two of them.
// MeshStatus MethodStatus 的应答。At 为取得的时间（自 1970 年起的毫秒数），用于计算两次之间的速率。
type MeshStatus struct {
	Self   Identity     `json:"self"`
	Links  []PeerConn   `json:"links"`
	Routes []RouteStats `json:"routes"`
	At     int64        `json:"at"`
}

// Limits are a role's bounds on its links (the member's limits), as its
// pages read and set them (MethodLimits, MethodLimitsSet): durations as Go
// writes them, "15s"; a zero or empty field is the default.
// Limits 角色连接的上限（成员的 limits），供其页面读取与设置（MethodLimits、MethodLimitsSet）：时长按 Go 的格式书写，
// 如 "15s"；零值或空字段表示默认值。
type Limits struct {
	MaxConns      int    `json:"max_conns"`
	MaxInflight   int    `json:"max_inflight"`
	PingInterval  string `json:"ping_interval"`
	SlowTimeout   string `json:"slow_timeout"`
	RetryInterval string `json:"retry_interval"`
	StreamIdle    string `json:"stream_idle"`
}

// The limits methods, every role's, its own pages only.
// limits 方法，每个角色都有，仅自身页面可调用。
const (
	MethodLimits    = "/mesh/limits/get" // -> Limits
	MethodLimitsSet = "/mesh/limits/set" // Limits -> Limits
)
