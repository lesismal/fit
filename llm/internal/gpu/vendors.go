// Package gpu manages the GPUs of a node across vendors: it detects which vendors' GPUs are present, knows the
// management tool of each vendor (how to install it, how to query and how to change settings), reads detailed
// status and applies settings such as power limits, clock locks, fan speed and persistence mode.
//
// Commands are data (see vendors below), so supporting another tool or fixing a flag is a one-line change.
// Settings values are validated (integers / enumerations only) before being substituted into a command, and setting
// commands run as argv without a shell. Install scripts are constants in this file, never caller input.
//
// Package gpu 跨厂商管理节点的显卡：检测存在哪些厂商的 GPU，掌握每个厂商的管理软件（如何安装、如何查询、如何修改设置），
// 读取详细状态并应用功耗上限、锁定频率、风扇转速、持久模式等设置。
//
// 命令以数据形式描述（见下方 vendors），支持新工具或修正参数只需改一行。设置值在代入命令前会校验（仅限整数 / 枚举），
// 设置命令以 argv 直接执行、不经 shell。安装脚本是本文件中的常量，从不来自调用方。
package gpu

// Setting kinds.
// 设置类型。
const (
	KindNumber = "number" // one integer value / 单个整数
	KindRange  = "range"  // "min,max" integers / "min,max" 两个整数
	KindEnum   = "enum"   // one of Options / Options 之一
	KindAction = "action" // no value / 无值
)

// Setting is one adjustable parameter of a vendor's GPUs.
// Setting 某厂商 GPU 的一项可调参数。
type Setting struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	LabelZH string   `json:"label_zh"`
	Kind    string   `json:"kind"`
	Unit    string   `json:"unit,omitempty"`
	Options []string `json:"options,omitempty"`
	Note    string   `json:"note,omitempty"`
	NoteZH  string   `json:"note_zh,omitempty"`
	// Command template: split on spaces into argv, then {index}, {value}, {min}, {max} and {display} ($DISPLAY or
	// ":0") are replaced inside each argument. It is executed directly (no shell; sudo -n when configured), so tokens
	// must not hold shell syntax, and a validated value can never add or split arguments.
	// 命令模板：按空格拆为 argv，再在每个参数内替换 {index}、{value}、{min}、{max} 与 {display}（$DISPLAY 或 ":0"）。
	// 直接执行（不经 shell；按配置加 sudo -n），因此模板中不能有 shell 语法，校验过的值也无法增加或拆分参数。
	Command string `json:"command"`
	// Group lets the UI lay settings out: power | clock | fan | mode.
	// Group 供界面分组：power | clock | fan | mode。
	Group string `json:"group"`
}

// InstallMethod installs the management tool on one OS family.
// InstallMethod 在某类操作系统上安装管理软件。
type InstallMethod struct {
	OS    string `json:"os"` // debian | rhel | darwin | any
	Title string `json:"title"`
	// Script is a constant shell script run with sh -c (as root when configured).
	// Script 常量 shell 脚本，用 sh -c 执行（按配置以 root 身份）。
	Script string `json:"script"`
}

// Vendor describes the GPUs of one vendor and their management tool.
// Vendor 描述某厂商的 GPU 及其管理软件。
type Vendor struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	PCIVendors  []string        `json:"pci_vendors"` // PCI vendor ids (lower-case hex, no 0x) / PCI 厂商 ID（小写十六进制，无 0x）
	Tool        string          `json:"tool"`        // primary CLI / 主命令行工具
	ExtraTools  []string        `json:"extra_tools,omitempty"`
	QueryCmd    string          `json:"query_cmd"` // shown to the user; parsing is done in query.go / 展示给用户；解析在 query.go
	Docs        string          `json:"docs"`
	Install     []InstallMethod `json:"install"`
	Settings    []Setting       `json:"settings"`
	Description string          `json:"description"`
	DescZH      string          `json:"description_zh"`
}

// Vendors is the catalog of supported GPU vendors.
// Vendors 支持的显卡厂商目录。
var Vendors = []Vendor{
	{
		ID: "nvidia", Name: "NVIDIA", PCIVendors: []string{"10de"}, Tool: "nvidia-smi", ExtraTools: []string{"nvidia-settings"},
		QueryCmd:    "nvidia-smi --query-gpu=index,name,uuid,driver_version,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,power.limit,power.min_limit,power.max_limit,power.default_limit,clocks.gr,clocks.mem,clocks.max.gr,clocks.max.mem,fan.speed,pstate,persistence_mode,compute_mode --format=csv,noheader,nounits",
		Docs:        "https://docs.nvidia.com/deploy/nvidia-smi/index.html",
		Description: "nvidia-smi ships with the NVIDIA driver (NVML). Clock offsets and manual fan control need nvidia-settings, a running X server and Coolbits enabled.",
		DescZH:      "nvidia-smi 随 NVIDIA 驱动（NVML）一起安装。频率偏移（超频）与手动风扇控制需要 nvidia-settings、运行中的 X Server 并开启 Coolbits。",
		Install: []InstallMethod{
			{OS: "debian", Title: "Ubuntu / Debian: driver + nvidia-smi", Script: `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update
if command -v ubuntu-drivers >/dev/null 2>&1 || apt-get install -y ubuntu-drivers-common; then
  # Install the recommended server driver (includes nvidia-smi / NVML)
  ubuntu-drivers install --gpgpu || ubuntu-drivers autoinstall
else
  # Debian: non-free driver from the distribution
  apt-get install -y nvidia-driver firmware-misc-nonfree
fi
apt-get install -y nvidia-settings || true
command -v nvidia-smi && nvidia-smi || echo "Driver installed: reboot the node to load the kernel module."
`},
			{OS: "rhel", Title: "RHEL / Rocky / Alma: CUDA repo driver", Script: `set -e
. /etc/os-release
MAJOR="${VERSION_ID%%.*}"
ARCH="$(uname -m)"
dnf install -y dnf-plugins-core kernel-devel-"$(uname -r)" kernel-headers || true
dnf config-manager --add-repo "https://developer.download.nvidia.com/compute/cuda/repos/rhel${MAJOR}/${ARCH}/cuda-rhel${MAJOR}.repo"
dnf module install -y nvidia-driver:latest-dkms || dnf install -y nvidia-driver nvidia-driver-cuda
command -v nvidia-smi && nvidia-smi || echo "Driver installed: reboot the node to load the kernel module."
`},
		},
		Settings: []Setting{
			{Key: "persistence", Label: "Persistence mode", LabelZH: "持久模式", Kind: KindEnum, Options: []string{"1", "0"}, Group: "mode",
				Command: "nvidia-smi -i {index} -pm {value}", Note: "1 = on: keeps the driver loaded, reduces start-up latency", NoteZH: "1 = 开启：保持驱动常驻，降低任务启动延迟"},
			{Key: "power_limit", Label: "Power limit", LabelZH: "功耗上限", Kind: KindNumber, Unit: "W", Group: "power",
				Command: "nvidia-smi -i {index} -pl {value}", Note: "Must be within the min / max power limit of the board", NoteZH: "须在显卡的最小 / 最大功耗范围内"},
			{Key: "gpu_clock_lock", Label: "Lock GPU clocks", LabelZH: "锁定核心频率", Kind: KindRange, Unit: "MHz", Group: "clock",
				Command: "nvidia-smi -i {index} -lgc {min},{max}", Note: "min,max in MHz (Volta and newer)", NoteZH: "min,max，单位 MHz（Volta 及更新架构）"},
			{Key: "gpu_clock_reset", Label: "Reset GPU clocks", LabelZH: "重置核心频率", Kind: KindAction, Group: "clock",
				Command: "nvidia-smi -i {index} -rgc"},
			{Key: "mem_clock_lock", Label: "Lock memory clocks", LabelZH: "锁定显存频率", Kind: KindRange, Unit: "MHz", Group: "clock",
				Command: "nvidia-smi -i {index} -lmc {min},{max}", Note: "min,max in MHz (Ampere and newer)", NoteZH: "min,max，单位 MHz（Ampere 及更新架构）"},
			{Key: "mem_clock_reset", Label: "Reset memory clocks", LabelZH: "重置显存频率", Kind: KindAction, Group: "clock",
				Command: "nvidia-smi -i {index} -rmc"},
			{Key: "gpu_clock_offset", Label: "GPU clock offset (overclock)", LabelZH: "核心频率偏移（超频）", Kind: KindNumber, Unit: "MHz", Group: "clock",
				Command: "nvidia-settings -c {display} -a [gpu:{index}]/GPUGraphicsClockOffsetAllPerformanceLevels={value}",
				Note:    "Needs nvidia-settings, an X server and Coolbits (nvidia-xconfig --cool-bits=28)", NoteZH: "需要 nvidia-settings、X Server 与 Coolbits（nvidia-xconfig --cool-bits=28）"},
			{Key: "mem_clock_offset", Label: "Memory clock offset (overclock)", LabelZH: "显存频率偏移（超频）", Kind: KindNumber, Unit: "MHz", Group: "clock",
				Command: "nvidia-settings -c {display} -a [gpu:{index}]/GPUMemoryTransferRateOffsetAllPerformanceLevels={value}",
				Note:    "Transfer rate offset (2× the memory clock); needs X + Coolbits", NoteZH: "传输速率偏移（显存频率的 2 倍）；需要 X + Coolbits"},
			{Key: "fan_speed", Label: "Fan speed", LabelZH: "风扇转速", Kind: KindNumber, Unit: "%", Group: "fan",
				Command: "nvidia-settings -c {display} -a [gpu:{index}]/GPUFanControlState=1 -a [fan:{index}]/GPUTargetFanSpeed={value}",
				Note:    "Manual fan control; needs X + Coolbits", NoteZH: "手动风扇控制；需要 X + Coolbits"},
			{Key: "fan_auto", Label: "Automatic fan control", LabelZH: "风扇自动控制", Kind: KindAction, Group: "fan",
				Command: "nvidia-settings -c {display} -a [gpu:{index}]/GPUFanControlState=0"},
			{Key: "compute_mode", Label: "Compute mode", LabelZH: "计算模式", Kind: KindEnum, Options: []string{"DEFAULT", "EXCLUSIVE_PROCESS", "PROHIBITED"}, Group: "mode",
				Command: "nvidia-smi -i {index} -c {value}"},
		},
	},
	{
		ID: "amd", Name: "AMD", PCIVendors: []string{"1002"}, Tool: "rocm-smi", ExtraTools: []string{"amd-smi"},
		QueryCmd:    "rocm-smi --showproductname --showdriverversion --showtemp --showpower --showmaxpower --showuse --showmeminfo vram --showclocks --showfan --showperflevel --json",
		Docs:        "https://rocm.docs.amd.com/projects/rocm_smi_lib/en/latest/",
		Description: "rocm-smi (and the newer amd-smi) come with ROCm. Changing clocks needs the perf level set to manual; the power overdrive changes the board power cap.",
		DescZH:      "rocm-smi（以及更新的 amd-smi）随 ROCm 安装。修改频率需先把性能等级设为 manual；power overdrive 修改显卡功耗上限。",
		Install: []InstallMethod{
			{OS: "debian", Title: "Ubuntu / Debian: rocm-smi", Script: `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update
if apt-get install -y rocm-smi; then
  rocm-smi || true
  exit 0
fi
# Fall back to AMD's installer (Ubuntu 22.04 / 24.04)
. /etc/os-release
VER=6.2.4; PKG=6.2.60204-1
curl -fsSLO "https://repo.radeon.com/amdgpu-install/${VER}/ubuntu/${VERSION_CODENAME}/amdgpu-install_${PKG}_all.deb"
apt-get install -y "./amdgpu-install_${PKG}_all.deb"
amdgpu-install -y --usecase=rocm --no-dkms
rocm-smi || echo "Installed: reboot the node if the amdgpu driver was updated."
`},
			{OS: "rhel", Title: "RHEL / Rocky / Alma: amdgpu-install", Script: `set -e
. /etc/os-release
MAJOR="${VERSION_ID%%.*}"
dnf install -y "https://repo.radeon.com/amdgpu-install/6.2.4/rhel/${VERSION_ID}/amdgpu-install-6.2.60204-1.el${MAJOR}.noarch.rpm"
amdgpu-install -y --usecase=rocm --no-dkms
rocm-smi || echo "Installed: reboot the node if the amdgpu driver was updated."
`},
		},
		Settings: []Setting{
			{Key: "power_cap", Label: "Power cap (overdrive)", LabelZH: "功耗上限（overdrive）", Kind: KindNumber, Unit: "W", Group: "power",
				Command: "rocm-smi -d {index} --autorespond y --setpoweroverdrive {value}"},
			{Key: "power_reset", Label: "Reset power cap", LabelZH: "重置功耗上限", Kind: KindAction, Group: "power",
				Command: "rocm-smi -d {index} --autorespond y --resetpoweroverdrive"},
			{Key: "perf_level", Label: "Performance level", LabelZH: "性能等级", Kind: KindEnum, Options: []string{"auto", "low", "high", "manual"}, Group: "mode",
				Command: "rocm-smi -d {index} --setperflevel {value}"},
			{Key: "sclk_level", Label: "GPU clock level", LabelZH: "核心频率档位", Kind: KindNumber, Group: "clock",
				Command: "rocm-smi -d {index} --autorespond y --setsclk {value}", Note: "DPM level index; needs perf level manual", NoteZH: "DPM 档位序号；需要性能等级为 manual"},
			{Key: "mclk_level", Label: "Memory clock level", LabelZH: "显存频率档位", Kind: KindNumber, Group: "clock",
				Command: "rocm-smi -d {index} --autorespond y --setmclk {value}", Note: "DPM level index; needs perf level manual", NoteZH: "DPM 档位序号；需要性能等级为 manual"},
			{Key: "overdrive", Label: "GPU clock overdrive", LabelZH: "核心频率超频比例", Kind: KindNumber, Unit: "%", Group: "clock",
				Command: "rocm-smi -d {index} --autorespond y --setoverdrive {value}", Note: "Percent above the default top clock (0-20)", NoteZH: "相对默认最高频率的提升百分比（0-20）"},
			{Key: "clock_reset", Label: "Reset clocks", LabelZH: "重置频率", Kind: KindAction, Group: "clock",
				Command: "rocm-smi -d {index} --resetclocks"},
			{Key: "fan_speed", Label: "Fan speed", LabelZH: "风扇转速", Kind: KindNumber, Unit: "%", Group: "fan",
				Command: "rocm-smi -d {index} --setfan {value}%"},
			{Key: "fan_auto", Label: "Automatic fan control", LabelZH: "风扇自动控制", Kind: KindAction, Group: "fan",
				Command: "rocm-smi -d {index} --resetfans"},
		},
	},
	{
		ID: "intel", Name: "Intel", PCIVendors: []string{"8086"}, Tool: "xpu-smi", ExtraTools: []string{"intel_gpu_top"},
		QueryCmd:    "xpu-smi discovery -j; xpu-smi stats -d <id> -j",
		Docs:        "https://github.com/intel/xpumanager",
		Description: "xpu-smi (Intel XPU Manager) manages Data Center GPUs (Flex / Max) and Arc; intel_gpu_top (intel-gpu-tools) only monitors integrated / consumer GPUs.",
		DescZH:      "xpu-smi（Intel XPU Manager）管理数据中心 GPU（Flex / Max）与 Arc；intel_gpu_top（intel-gpu-tools）只能监控集成 / 消费级 GPU。",
		Install: []InstallMethod{
			{OS: "debian", Title: "Ubuntu: Intel GPU repository + xpu-smi", Script: `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y gpg curl intel-gpu-tools || true
. /etc/os-release
curl -fsSL https://repositories.intel.com/gpu/intel-graphics.key | gpg --yes --dearmor -o /usr/share/keyrings/intel-graphics.gpg
echo "deb [arch=amd64 signed-by=/usr/share/keyrings/intel-graphics.gpg] https://repositories.intel.com/gpu/ubuntu ${VERSION_CODENAME} unified" > /etc/apt/sources.list.d/intel-gpu.list
apt-get update
apt-get install -y xpu-smi
xpu-smi discovery
`},
			{OS: "rhel", Title: "RHEL / Rocky: Intel GPU repository + xpu-smi", Script: `set -e
. /etc/os-release
dnf install -y 'dnf-command(config-manager)' || true
dnf config-manager --add-repo "https://repositories.intel.com/gpu/rhel/${VERSION_ID}/unified/intel-gpu-${VERSION_ID}.repo"
dnf install -y xpu-smi intel-gpu-tools || dnf install -y xpu-smi
xpu-smi discovery
`},
		},
		Settings: []Setting{
			{Key: "power_limit", Label: "Power limit", LabelZH: "功耗上限", Kind: KindNumber, Unit: "W", Group: "power",
				Command: "xpu-smi config -d {index} --powerlimit {value}"},
			{Key: "freq_range", Label: "GPU frequency range", LabelZH: "核心频率范围", Kind: KindRange, Unit: "MHz", Group: "clock",
				Command: "xpu-smi config -d {index} --frequencyrange {min},{max}"},
			{Key: "standby", Label: "Standby mode", LabelZH: "待机模式", Kind: KindEnum, Options: []string{"0", "1"}, Group: "mode",
				Command: "xpu-smi config -d {index} --standby {value}", Note: "0 = default, 1 = never standby", NoteZH: "0 = 默认，1 = 从不待机"},
			{Key: "scheduler_timeout", Label: "Scheduler: timeout mode", LabelZH: "调度器：超时模式", Kind: KindNumber, Unit: "µs", Group: "mode",
				Command: "xpu-smi config -d {index} --scheduler timeout,{value}"},
		},
	},
	{
		ID: "ascend", Name: "Huawei Ascend", PCIVendors: []string{"19e5"}, Tool: "npu-smi",
		QueryCmd:    "npu-smi info",
		Docs:        "https://www.hiascend.com/document",
		Description: "npu-smi is installed with the Ascend NPU driver (Ascend-hdk-*-npu-driver). Download the driver package matching the card from the Ascend site and install it with --full.",
		DescZH:      "npu-smi 随昇腾 NPU 驱动（Ascend-hdk-*-npu-driver）安装。从昇腾官网下载与卡型号匹配的驱动包，并以 --full 安装。",
		Install: []InstallMethod{
			{OS: "any", Title: "Ascend driver package", Script: `set -e
# Put the driver / firmware .run packages for this card in $FIT_AGENT_GPU_DATA_DIR/ascend first
# (download them from https://www.hiascend.com/hardware/firmware-drivers/community).
DIR="${FIT_AGENT_GPU_DATA_DIR:-.}/ascend"
ls "$DIR"/*.run >/dev/null 2>&1 || { echo "No .run packages in $DIR"; exit 1; }
groupadd -f HwHiAiUser; id HwHiAiUser >/dev/null 2>&1 || useradd -g HwHiAiUser -d /home/HwHiAiUser -m HwHiAiUser -s /bin/bash
for f in "$DIR"/*npu-driver*.run "$DIR"/*npu-firmware*.run; do
  [ -f "$f" ] || continue
  chmod +x "$f"; "$f" --full --install-for-all --quiet
done
npu-smi info || echo "Installed: reboot the node to load the driver."
`},
		},
		Settings: []Setting{
			{Key: "power_mode", Label: "Power-saving mode (custom)", LabelZH: "节能模式（自定义）", Kind: KindEnum, Options: []string{"0", "1"}, Group: "power",
				Command: "npu-smi set -t pwr-mode -i {index} -d {value}", Note: "Atlas 300I / 800 series; 0 = high performance, 1 = power saving", NoteZH: "Atlas 300I / 800 系列；0 = 高性能，1 = 节能"},
		},
	},
	{
		ID: "mthreads", Name: "Moore Threads", PCIVendors: []string{"1ed5"}, Tool: "mthreads-gmi",
		QueryCmd:    "mthreads-gmi -q",
		Docs:        "https://developer.mthreads.com/",
		Description: "mthreads-gmi is installed with the MUSA driver (musa_*.deb / .rpm from the Moore Threads developer site).",
		DescZH:      "mthreads-gmi 随 MUSA 驱动安装（从摩尔线程开发者网站获取 musa_*.deb / .rpm）。",
		Install: []InstallMethod{
			{OS: "debian", Title: "MUSA driver package", Script: `set -e
# Put the musa driver .deb for this card in $FIT_AGENT_GPU_DATA_DIR/mthreads first
DIR="${FIT_AGENT_GPU_DATA_DIR:-.}/mthreads"
ls "$DIR"/*.deb >/dev/null 2>&1 || { echo "No .deb packages in $DIR"; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get install -y "$DIR"/*.deb
mthreads-gmi || echo "Installed: reboot the node to load the driver."
`},
		},
	},
	{
		ID: "hygon", Name: "Hygon DCU", PCIVendors: []string{"1d94"}, Tool: "hy-smi",
		QueryCmd:    "hy-smi",
		Docs:        "https://developer.hpccube.com/",
		Description: "hy-smi is installed with the DTK / DCU driver; its options follow rocm-smi.",
		DescZH:      "hy-smi 随 DTK / DCU 驱动安装，参数与 rocm-smi 一致。",
		Install: []InstallMethod{
			{OS: "any", Title: "DCU driver package", Script: `set -e
# Put the DCU driver .run package in $FIT_AGENT_GPU_DATA_DIR/hygon first
DIR="${FIT_AGENT_GPU_DATA_DIR:-.}/hygon"
ls "$DIR"/*.run >/dev/null 2>&1 || { echo "No .run packages in $DIR"; exit 1; }
for f in "$DIR"/*.run; do chmod +x "$f"; "$f" -y || "$f"; done
hy-smi || echo "Installed: reboot the node to load the driver."
`},
		},
		Settings: []Setting{
			{Key: "power_cap", Label: "Power cap", LabelZH: "功耗上限", Kind: KindNumber, Unit: "W", Group: "power",
				Command: "hy-smi -d {index} --setpoweroverdrive {value}"},
			{Key: "perf_level", Label: "Performance level", LabelZH: "性能等级", Kind: KindEnum, Options: []string{"auto", "low", "high", "manual"}, Group: "mode",
				Command: "hy-smi -d {index} --setperflevel {value}"},
			{Key: "fan_speed", Label: "Fan speed", LabelZH: "风扇转速", Kind: KindNumber, Unit: "%", Group: "fan",
				Command: "hy-smi -d {index} --setfan {value}%"},
		},
	},
	{
		ID: "apple", Name: "Apple Silicon", Tool: "system_profiler", ExtraTools: []string{"powermetrics"},
		QueryCmd:    "system_profiler SPDisplaysDataType -json; sudo powermetrics --samplers gpu_power -n 1",
		Docs:        "https://support.apple.com/guide/mac-help/",
		Description: "The GPU is part of the SoC: the tools are built into macOS and there are no power or clock settings. Use low power mode (pmset) to cap power.",
		DescZH:      "GPU 集成在 SoC 中：工具为 macOS 内置，没有功耗或频率设置。可以用低功耗模式（pmset）限制功耗。",
		Install: []InstallMethod{
			{OS: "darwin", Title: "Built in (optional: asitop monitor)", Script: `set -e
system_profiler SPDisplaysDataType
if command -v pip3 >/dev/null 2>&1; then pip3 install --user asitop && echo "asitop installed: run sudo asitop"; fi
`},
		},
		Settings: []Setting{
			{Key: "low_power", Label: "Low power mode", LabelZH: "低功耗模式", Kind: KindEnum, Options: []string{"0", "1"}, Group: "power",
				Command: "pmset -a lowpowermode {value}", Note: "Applies to the whole machine", NoteZH: "作用于整机"},
		},
	},
}

// VendorByID returns the vendor with the given id.
// VendorByID 返回指定 ID 的厂商。
func VendorByID(id string) (*Vendor, bool) {
	for i := range Vendors {
		if Vendors[i].ID == id {
			return &Vendors[i], true
		}
	}
	return nil, false
}

// SettingByKey returns the setting with the given key.
// SettingByKey 返回指定 key 的设置。
func (v *Vendor) SettingByKey(key string) (*Setting, bool) {
	for i := range v.Settings {
		if v.Settings[i].Key == key {
			return &v.Settings[i], true
		}
	}
	return nil, false
}
