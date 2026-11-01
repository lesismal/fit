package model

// Node methods every peer role (hub, client) may call.
// 所有对端角色（hub、client）都可调用的节点方法。
const (
	MethodNodeInfo = "/node/info" // NodeInfo
	// MethodNodeProxy is a stream method: ProxyReq opens it, the first stream-data frame is a JSON ProxyHead, every
	// later one raw bytes of the upstream response body, and the stream-end is a ProxyResult.
	// MethodNodeProxy 是流方法：以 ProxyReq 打开，第一个 stream-data 帧为 JSON ProxyHead，之后每帧为上游响应体的原始字节，
	// stream-end 为 ProxyResult。
	MethodNodeProxy = "/node/proxy"
)

// NodeReadMethods are the node methods open to hub and client.
// NodeReadMethods 对 hub 与 client 开放的节点方法。
var NodeReadMethods = []string{MethodNodeInfo, MethodNodeProxy}

// Node management methods: the node's own console, and a hub when the node allows it (NodeSettings.AllowHubManage).
// 节点管理方法：节点自身控制台，以及节点允许时（NodeSettings.AllowHubManage）的 hub。
const (
	MethodNodeModelList   = "/node/model/list"   // -> []Model (API keys masked)
	MethodNodeModelSave   = "/node/model/save"   // Model -> Model (an empty / masked APIKey keeps the stored one)
	MethodNodeModelDelete = "/node/model/delete" // NameReq
	MethodNodeModelCheck  = "/node/model/check"  // NameReq -> ModelView (probes the upstream)
	MethodNodeProfileSet  = "/node/profile/set"  // ProfileReq
	MethodNodeGPUStatus   = "/node/gpu/status"   // -> gpu.Status
	MethodNodeGPUCatalog  = "/node/gpu/catalog"  // -> []gpu.Vendor
	MethodNodeGPUInstall  = "/node/gpu/install"  // GPUInstallReq -> Job
	MethodNodeGPUSet      = "/node/gpu/set"      // GPUSetReq -> Job
	MethodNodeActionList  = "/node/action/list"  // -> []Action
	MethodNodeActionRun   = "/node/action/run"   // ActionRunReq -> Job
	MethodNodeJobList     = "/node/job/list"     // -> []Job (no output)
	MethodNodeJobGet      = "/node/job/get"      // JobGetReq -> Job
	MethodNodeJobFollow   = "/node/job/follow"   // stream: JobGetReq; frames Job; end Job
	MethodNodeJobCancel   = "/node/job/cancel"   // JobIDReq
)

// NodeManageMethods lists the management methods a hub may get (stream methods included).
// NodeManageMethods 列出 hub 可能获得的管理方法（含流方法）。
var NodeManageMethods = []string{
	MethodNodeModelList, MethodNodeModelSave, MethodNodeModelDelete, MethodNodeModelCheck, MethodNodeProfileSet,
	MethodNodeGPUStatus, MethodNodeGPUCatalog, MethodNodeGPUInstall, MethodNodeGPUSet,
	MethodNodeActionList, MethodNodeActionRun,
	MethodNodeJobList, MethodNodeJobGet, MethodNodeJobFollow, MethodNodeJobCancel,
}

// Node local-only methods (the node's own console; never a peer).
// 节点仅本地的方法（节点自身控制台；从不对对端开放）。
const (
	MethodNodeSettingsGet = "/node/settings/get" // -> NodeSettings
	MethodNodeSettingsSet = "/node/settings/set" // NodeSettings
)

// MethodNodeUpdate is the push of a node to its hub / client peers whenever its NodeInfo changes.
// MethodNodeUpdate 节点信息变化时节点推送给其 hub / client 对端。
const MethodNodeUpdate = "/node/update"

// Model is a model a node serves: an upstream API endpoint. Name is how agents ask for it; Upstream is the model id
// sent upstream (default Name).
// Model 节点提供的模型：一个上游 API 端点。Name 是 agent 请求它时用的名称；Upstream 是发往上游的模型 ID（默认 Name）。
type Model struct {
	Name     string `json:"name"`
	API      string `json:"api"`      // one of APIs / APIs 之一
	BaseURL  string `json:"base_url"` // e.g. http://127.0.0.1:11434/v1, https://api.anthropic.com/v1
	Upstream string `json:"upstream,omitempty"`
	// APIVersion is the api-version an azure_openai model's calls carry.
	// APIVersion azure_openai 模型的调用所带的 api-version。
	APIVersion  string `json:"api_version,omitempty"`
	APIKey      string `json:"api_key,omitempty"` // masked when read / 读取时打码
	Disabled    bool   `json:"disabled,omitempty"`
	Description string `json:"description,omitempty"`
	// MaxInflight bounds concurrent calls to this model on this node; 0 = no limit.
	// MaxInflight 本节点上此模型的并发调用上限；0 表示不限。
	MaxInflight int `json:"max_inflight,omitempty"`
}

// Model states.
// 模型状态。
const (
	ModelReady    = "ready"
	ModelDisabled = "disabled"
	ModelError    = "error" // the last check failed / 最近一次检查失败
)

// ModelView is a model as peers see it: no endpoint, no key.
// ModelView 对端看到的模型：不含端点与 Key。
type ModelView struct {
	Name        string `json:"name"`
	API         string `json:"api"`
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	Description string `json:"description,omitempty"`
	Inflight    int    `json:"inflight"`
	MaxInflight int    `json:"max_inflight,omitempty"`
	CheckedAt   int64  `json:"checked_at,omitempty"`
}

// Ready reports whether the model may be called.
// Ready 判断模型是否可调用。
func (m ModelView) Ready() bool {
	return m.State == ModelReady && (m.MaxInflight <= 0 || m.Inflight < m.MaxInflight)
}

// NodeInfo is what a node pushes (MethodNodeUpdate) and answers (MethodNodeInfo).
// NodeInfo 节点推送（MethodNodeUpdate）与应答（MethodNodeInfo）的信息。
type NodeInfo struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	HW          HWInfo      `json:"hw"`
	Models      []ModelView `json:"models"`
	Inflight    int         `json:"inflight"` // proxy calls in progress / 进行中的转发调用
	Jobs        int         `json:"jobs"`     // running jobs / 运行中的作业
	// HubManage: hubs may call NodeManageMethods.
	// HubManage：hub 可以调用 NodeManageMethods。
	HubManage bool `json:"hub_manage"`
	// Seq increases with every NodeInfo, so receivers drop stale ones arriving out of order.
	// Seq 随每份 NodeInfo 递增，接收方据此丢弃乱序到达的旧信息。
	Seq       int64 `json:"seq"`
	UpdatedAt int64 `json:"updated_at"`
}

// NodeSettings are the node's own settings (its own pages only), as its
// member's settings writes them: durations as Go writes them, "30s".
type NodeSettings struct {
	AllowHubManage bool `json:"allow_hub_manage"`
	// GPUSudo prefixes GPU setting commands and tool installs with "sudo -n" when not running as root.
	GPUSudo         bool   `json:"gpu_sudo"`
	RefreshInterval string `json:"refresh_interval"` // hardware refresh, default 30s
	JobTimeout      string `json:"job_timeout"`      // default 1h
	GPUDataDir      string `json:"gpu_data_dir,omitempty"`
}

// ProfileReq sets the display name and description of a node.
// ProfileReq 设置节点的显示名称与描述。
type ProfileReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// NameReq names a model. API is needed only when two models share Name (one per API format).
// NameReq 指定一个模型。仅当两个模型同名（各属一种接口格式）时才需要 API。
type NameReq struct {
	Name string `json:"name"`
	API  string `json:"api,omitempty"`
}

// GPUInstallReq installs the management tool of a vendor.
// GPUInstallReq 安装某厂商的显卡管理软件。
type GPUInstallReq struct {
	Vendor string `json:"vendor"`
}

// GPUSetReq applies one setting of a vendor (see gpu.Setting) to one GPU, or every GPU of the vendor (Index < 0).
// GPUSetReq 把某厂商的一项设置（见 gpu.Setting）应用到一块 GPU，或该厂商的全部 GPU（Index < 0）。
type GPUSetReq struct {
	Vendor string `json:"vendor"`
	Index  int    `json:"index"`
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
}

// Action is one fixed management operation of a node (installing a model runtime, pulling a model, ...). The command
// is built in code from validated parameters; no script text ever comes from a caller.
// Action 节点的一项固定管理操作（安装模型运行时、拉取模型等）。命令由代码依据校验过的参数生成；脚本文本从不来自调用方。
type Action struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	TitleZH string        `json:"title_zh"`
	Note    string        `json:"note,omitempty"`
	NoteZH  string        `json:"note_zh,omitempty"`
	Params  []ActionParam `json:"params,omitempty"`
	// OS lists the GOOS values the action supports; empty = all.
	// OS 列出该操作支持的 GOOS；为空表示全部。
	OS []string `json:"os,omitempty"`
}

// ActionParam is one parameter of an Action, validated against Pattern (a Go regexp, anchored) before use.
// ActionParam Action 的一个参数，使用前按 Pattern（Go 正则，整体匹配）校验。
type ActionParam struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	LabelZH string `json:"label_zh"`
	Pattern string `json:"pattern"`
	Example string `json:"example,omitempty"`
}

// ActionRunReq runs Action ID with Params.
// ActionRunReq 以 Params 运行 Action ID。
type ActionRunReq struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params,omitempty"`
}

// Job states.
// 作业状态。
const (
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
	JobCanceled = "canceled"
)

// MaxJobOutput bounds the output a job keeps in memory (the oldest part is dropped).
// MaxJobOutput 作业在内存中保留的输出上限（丢弃最早的部分）。
const MaxJobOutput = 1 << 20

// Job is a background management operation and its output.
// Job 后台管理操作及其输出。
type Job struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code"`
	Error      string `json:"error,omitempty"`
	Output     string `json:"output,omitempty"` // from the requested offset / 从请求的偏移开始
	Offset     int    `json:"offset"`
	OutputLen  int    `json:"output_len"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
}

// JobGetReq reads a job and its output from Offset (MethodNodeJobGet, MethodNodeJobFollow).
// JobGetReq 读取作业及其从 Offset 开始的输出（MethodNodeJobGet、MethodNodeJobFollow）。
type JobGetReq struct {
	ID     int64 `json:"id"`
	Offset int   `json:"offset"`
}

// JobIDReq names a job.
// JobIDReq 指定一个作业。
type JobIDReq struct {
	ID int64 `json:"id"`
}
