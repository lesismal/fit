package model

// GPUInfo is the basic snapshot of one GPU, part of HWInfo (see gopkg/gpu for the detailed status and settings).
// GPUInfo 单块 GPU 的基础快照，属于 HWInfo（详细状态与设置见 gopkg/gpu）。
type GPUInfo struct {
	Index       int     `json:"index"`
	Vendor      string  `json:"vendor"`
	Name        string  `json:"name"`
	MemoryMB    int64   `json:"memory_mb"`
	MemoryUsed  int64   `json:"memory_used_mb"`
	Utilization float64 `json:"utilization"` // percent / 百分比
	Temperature float64 `json:"temperature"` // celsius / 摄氏度
	PowerDraw   float64 `json:"power_draw,omitempty"`
	PowerLimit  float64 `json:"power_limit,omitempty"`
	Driver      string  `json:"driver,omitempty"`
}

// DiskInfo is one mounted file system.
// DiskInfo 一个已挂载的文件系统。
type DiskInfo struct {
	Mount   string `json:"mount"`
	FSType  string `json:"fs_type,omitempty"`
	TotalMB int64  `json:"total_mb"`
	FreeMB  int64  `json:"free_mb"`
}

// HWInfo is the system environment and hardware of a node.
// HWInfo 节点的系统环境与硬件规格。
type HWInfo struct {
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"`
	Platform      string     `json:"platform,omitempty"` // distro / product version / 发行版 / 系统版本
	Kernel        string     `json:"kernel,omitempty"`
	Arch          string     `json:"arch"`
	CPUModel      string     `json:"cpu_model"`
	CPUCores      int        `json:"cpu_cores"`
	Load1         float64    `json:"load1"`
	MemTotalMB    int64      `json:"mem_total_mb"`
	MemAvailMB    int64      `json:"mem_available_mb"`
	GPUs          []GPUInfo  `json:"gpus"`
	Disks         []DiskInfo `json:"disks"`
	IPs           []string   `json:"ips"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	CollectedAt   int64      `json:"collected_at"`
}
