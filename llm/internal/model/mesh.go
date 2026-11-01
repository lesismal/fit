package model

// Roles.
// 角色。
const (
	RoleNode  = "node"
	RoleHub   = "hub"
	RoleProxy = "proxy"
	// RoleLocal is the caller of a method made by the process's own web console (never sent over the mesh).
	// RoleLocal 由进程自身 Web 控制台发起的调用的调用方角色（不会经互联发送）。
	RoleLocal = "local"
)

// Roles lists the three program roles.
// Roles 列出三个程序角色。
var Roles = []string{RoleNode, RoleHub, RoleProxy}

// Mesh management methods: only RoleLocal (the process's own console).
// 互联管理方法：只有 RoleLocal（进程自身的控制台）。
const (
	// MethodHello is called by the dialing side first on every (re)connection; both sides exchange identities.
	// MethodHello 由拨号方在每次（重新）连接后首先调用；双方交换身份。
	MethodHello = "/mesh/hello"
	// MethodPeers lists the identity, listener, connections and dial targets of the process (PeersResp).
	// MethodPeers 列出进程的身份、监听、连接与连接目标（PeersResp）。
	MethodPeers = "/mesh/peers"
	// MethodDial adds (or replaces) a dial target; MethodUndial removes one (UndialReq).
	// MethodDial 添加（或替换）连接目标；MethodUndial 删除连接目标（UndialReq）。
	MethodDial   = "/mesh/dial"
	MethodUndial = "/mesh/undial"
	// MethodKick closes every connection of a peer (KickReq); outbound ones redial.
	// MethodKick 关闭某个对端的全部连接（KickReq）；出站连接随后重连。
	MethodKick = "/mesh/kick"
	// MethodListen changes the rpc listener at runtime (ListenReq; empty addr stops listening).
	// MethodListen 运行时修改 rpc 监听（ListenReq；地址为空表示停止监听）。
	MethodListen = "/mesh/listen"
)

// Identity is what a process announces at hello.
// Identity 进程在 hello 时声明的身份。
type Identity struct {
	Role      string `json:"role"`
	ID        string `json:"id"` // stable, generated on first start / 稳定，首次启动时生成
	Name      string `json:"name"`
	Hostname  string `json:"hostname"`
	Version   string `json:"version"`
	PID       int    `json:"pid"`
	HTTPURL   string `json:"http_url,omitempty"` // web console / Web 控制台
	RPCAddr   string `json:"rpc_addr,omitempty"` // where its mesh listener can be dialed / 其互联监听可被连接的地址
	StartedAt int64  `json:"started_at"`
}

// PeerConn is one live connection.
// PeerConn 一条存活连接。
type PeerConn struct {
	Identity
	ConnID      int64  `json:"conn_id"`
	Direction   string `json:"direction"`        // "in" or "out" / "in" 或 "out"
	Target      string `json:"target,omitempty"` // the dialed address of an outbound connection / 出站连接所拨号的地址
	Remote      string `json:"remote"`
	ConnectedAt int64  `json:"connected_at"`
	// Draining: either side sent drain (wire), no new calls go here.
	// Draining：任一方已发送 drain（wire），新调用不再发往这里。
	Draining bool `json:"draining,omitempty"`
	// Link is the link's traffic and latency.
	// Link 连接的流量与延迟。
	Link LinkStats `json:"link"`
}

// DialReq adds a dial target.
// DialReq 添加连接目标。
type DialReq struct {
	Addr     string `json:"addr"`
	Password string `json:"password,omitempty"`
	// Role the peer at Addr must have; its hello is refused otherwise.
	// Addr 处对端必须具有的角色；否则拒绝其 hello。
	Role string `json:"role"`
}

// DialTarget is a dial target and its state. The password is never returned.
// DialTarget 连接目标及其状态。从不返回密码。
type DialTarget struct {
	Addr      string    `json:"addr"`
	Role      string    `json:"role"`
	Connected bool      `json:"connected"`
	Peer      *Identity `json:"peer,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// ListenConfig is the rpc listener. The password is never returned by MethodPeers (HasPassword instead).
// ListenConfig rpc 监听。MethodPeers 从不返回密码（改为 HasPassword）。
type ListenConfig struct {
	Addr     string `json:"addr"`               // empty: not listening / 为空表示不监听
	Password string `json:"password,omitempty"` // required when Addr is set / 设置 Addr 时必填
}

// ListenReq changes the rpc listener; an empty Password keeps the current one.
// ListenReq 修改 rpc 监听；Password 为空表示保留当前密码。
type ListenReq = ListenConfig

// ListenState is the listener as shown on the console.
// ListenState 控制台上显示的监听状态。
type ListenState struct {
	Addr        string `json:"addr"`
	HasPassword bool   `json:"has_password"`
	Error       string `json:"error,omitempty"` // last failure to listen / 最近一次监听失败
}

// PeersResp is the reply of MethodPeers.
// PeersResp MethodPeers 的应答。
type PeersResp struct {
	Self    Identity     `json:"self"`
	Listen  ListenState  `json:"listen"`
	Conns   []PeerConn   `json:"conns"`
	Targets []DialTarget `json:"targets"`
}

// UndialReq removes the dial target of Addr.
// UndialReq 删除 Addr 的连接目标。
type UndialReq struct {
	Addr string `json:"addr"`
}

// KickReq closes the connections of the peer ID.
// KickReq 关闭对端 ID 的连接。
type KickReq struct {
	ID string `json:"id"`
}
