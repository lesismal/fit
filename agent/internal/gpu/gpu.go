package gpu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// VendorStatus is a vendor as seen on this node.
// VendorStatus 本节点视角下的一个厂商。
type VendorStatus struct {
	Vendor
	Detected   int             `json:"detected"` // devices found on the PCI bus / PCI 总线上发现的设备数
	ToolPath   string          `json:"tool_path,omitempty"`
	Installed  bool            `json:"installed"`
	ExtraFound map[string]bool `json:"extra_found,omitempty"`
	// InstallFor is the install method matching this node's OS ("" when none matches).
	// InstallFor 与本节点操作系统匹配的安装方式（无匹配时为 ""）。
	InstallFor string `json:"install_for,omitempty"`
}

// Device is the detailed status of one GPU. Unknown numeric values are -1.
// Device 单块 GPU 的详细状态。未知数值为 -1。
type Device struct {
	Vendor       string  `json:"vendor"`
	Index        int     `json:"index"`
	Name         string  `json:"name"`
	UUID         string  `json:"uuid,omitempty"`
	Driver       string  `json:"driver,omitempty"`
	MemTotalMB   float64 `json:"mem_total_mb"`
	MemUsedMB    float64 `json:"mem_used_mb"`
	Utilization  float64 `json:"utilization"`
	Temperature  float64 `json:"temperature"`
	PowerDraw    float64 `json:"power_draw"`
	PowerLimit   float64 `json:"power_limit"`
	PowerMin     float64 `json:"power_min"`
	PowerMax     float64 `json:"power_max"`
	PowerDefault float64 `json:"power_default"`
	ClockGfx     float64 `json:"clock_gfx"`
	ClockMem     float64 `json:"clock_mem"`
	ClockGfxMax  float64 `json:"clock_gfx_max"`
	ClockMemMax  float64 `json:"clock_mem_max"`
	FanSpeed     float64 `json:"fan_speed"`
	PState       string  `json:"pstate,omitempty"`
	Persistence  string  `json:"persistence,omitempty"`
	ComputeMode  string  `json:"compute_mode,omitempty"`
	PerfLevel    string  `json:"perf_level,omitempty"`
}

// Status is the GPU status of a node.
// Status 节点的显卡状态。
type Status struct {
	OS          string            `json:"os"`       // debian | rhel | darwin | linux | <goos>
	Root        bool              `json:"root"`     // the node runs as root / 节点以 root 运行
	UseSudo     bool              `json:"use_sudo"` // commands are prefixed with sudo -n / 命令加 sudo -n 前缀
	Vendors     []VendorStatus    `json:"vendors"`  // every vendor of the catalog / 目录中的所有厂商
	Devices     []Device          `json:"devices"`  // GPUs read through the installed tools / 通过已安装工具读取的 GPU
	Raw         map[string]string `json:"raw"`      // raw tool output for vendors without a parser / 无解析器的厂商的原始输出
	Errors      map[string]string `json:"errors"`   // per vendor / 按厂商
	CollectedAt int64             `json:"collected_at"`
}

// Options configures command execution.
// Options 命令执行配置。
type Options struct {
	// UseSudo prefixes setting commands and install scripts (see Wrap) with "sudo -n" when not running as root.
	// UseSudo 非 root 运行时为设置命令与安装脚本加 "sudo -n" 前缀。
	UseSudo bool
}

func (o Options) sudo() bool { return o.UseSudo && runtime.GOOS != "windows" && os.Geteuid() != 0 }

// Collect detects the vendors and reads every GPU through the installed tools.
// Collect 检测厂商，并通过已安装的工具读取所有 GPU。
func Collect(ctx context.Context, opts Options) Status {
	st := Status{OS: osFamily(), Root: os.Geteuid() == 0, UseSudo: opts.sudo(),
		Raw: map[string]string{}, Errors: map[string]string{}, Devices: []Device{}, CollectedAt: time.Now().Unix()}
	pci := pciVendors()
	for _, v := range Vendors {
		vs := VendorStatus{Vendor: v, Detected: pci[v.ID]}
		if v.ID == "apple" && runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			vs.Detected = 1
		}
		if p, err := exec.LookPath(v.Tool); err == nil {
			vs.ToolPath, vs.Installed = p, true
		}
		for _, t := range v.ExtraTools {
			if vs.ExtraFound == nil {
				vs.ExtraFound = map[string]bool{}
			}
			_, err := exec.LookPath(t)
			vs.ExtraFound[t] = err == nil
		}
		if m := installFor(&v, st.OS); m != nil {
			vs.InstallFor = m.OS
		}
		st.Vendors = append(st.Vendors, vs)
		if !vs.Installed {
			continue
		}
		devs, raw, err := query(ctx, &v)
		if err != nil {
			st.Errors[v.ID] = err.Error()
		}
		if raw != "" {
			st.Raw[v.ID] = raw
		}
		st.Devices = append(st.Devices, devs...)
	}
	return st
}

// InstallScript returns the install method of vendor for this node's OS.
// InstallScript 返回厂商在本节点操作系统上的安装方式。
func InstallScript(vendor string) (*InstallMethod, error) {
	v, ok := VendorByID(vendor)
	if !ok {
		return nil, fmt.Errorf("unknown vendor %q", vendor)
	}
	m := installFor(v, osFamily())
	if m == nil {
		return nil, fmt.Errorf("no install method of %s for %s", v.Name, osFamily())
	}
	return m, nil
}

func installFor(v *Vendor, family string) *InstallMethod {
	for i := range v.Install {
		m := &v.Install[i]
		if m.OS == family || m.OS == "any" && family != "darwin" && family != "windows" {
			return m
		}
	}
	return nil
}

// EnvDataDir names the environment variable install scripts read the node's data directory from (vendors whose
// driver packages must be downloaded by hand look for them there).
// EnvDataDir 安装脚本从中读取节点数据目录的环境变量名（驱动包需手动下载的厂商在该目录下查找）。
const EnvDataDir = "FIT_AGENT_GPU_DATA_DIR"

var reDisplay = regexp.MustCompile(`^[A-Za-z0-9._-]*:[0-9]+(\.[0-9]+)?$`)

// display is the X display for nvidia-settings: $DISPLAY when well-formed, else ":0".
// display nvidia-settings 使用的 X display：格式正确时为 $DISPLAY，否则为 ":0"。
func display() string {
	if d := os.Getenv("DISPLAY"); reDisplay.MatchString(d) {
		return d
	}
	return ":0"
}

// Command builds the argv of a setting after validating value: only integers (bounded) or one of the fixed
// options are accepted, and they are substituted inside single arguments, so a value can never inject shell syntax
// or extra arguments. index < 0 is not accepted here: the caller expands "every GPU" into indices.
// Command 校验 value 后构造设置的 argv：只接受（有界的）整数或固定选项之一，且只在单个参数内替换，因此值既不能注入
// shell 语法，也不能增加参数。此处不接受 index < 0：调用方负责把“所有 GPU”展开为序号。
func Command(vendor string, index int, key, value string) ([]string, error) {
	v, ok := VendorByID(vendor)
	if !ok {
		return nil, fmt.Errorf("unknown vendor %q", vendor)
	}
	s, ok := v.SettingByKey(key)
	if !ok {
		return nil, fmt.Errorf("%s has no setting %q", v.Name, key)
	}
	if index < 0 || index > 1024 {
		return nil, fmt.Errorf("invalid gpu index %d", index)
	}
	value = strings.TrimSpace(value)
	repl := []string{"{index}", strconv.Itoa(index), "{display}", display()}
	switch s.Kind {
	case KindNumber:
		n, err := strconv.Atoi(value)
		if err != nil || n < -100000 || n > 100000 {
			return nil, fmt.Errorf("%s: value must be an integer", s.Label)
		}
		repl = append(repl, "{value}", strconv.Itoa(n))
	case KindRange:
		lo, hi, ok := strings.Cut(value, ",")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if !ok || err1 != nil || err2 != nil || a < 0 || b < a || b > 100000 {
			return nil, fmt.Errorf("%s: value must be min,max integers with min ≤ max", s.Label)
		}
		repl = append(repl, "{min}", strconv.Itoa(a), "{max}", strconv.Itoa(b))
	case KindEnum:
		if !slices.Contains(s.Options, value) {
			return nil, fmt.Errorf("%s: value must be one of %s", s.Label, strings.Join(s.Options, ", "))
		}
		repl = append(repl, "{value}", value)
	case KindAction:
	default:
		return nil, fmt.Errorf("bad setting kind %q", s.Kind)
	}
	r := strings.NewReplacer(repl...)
	argv := strings.Fields(s.Command)
	for i, a := range argv {
		argv[i] = r.Replace(a)
	}
	return argv, nil
}

// Wrap prefixes argv with "sudo -n" (keeping DISPLAY, XAUTHORITY and EnvDataDir) when configured and not root.
// Wrap 在已配置且非 root 时为 argv 加 "sudo -n" 前缀（保留 DISPLAY、XAUTHORITY 与 EnvDataDir）。
func (o Options) Wrap(argv []string) []string {
	if !o.sudo() {
		return argv
	}
	return append([]string{"sudo", "-n", "--preserve-env=DISPLAY,XAUTHORITY," + EnvDataDir}, argv...)
}

// InstallCommand returns the title and argv (sh -c with the constant install script) installing the tool of
// vendor on this node's OS. The caller applies Options.Wrap and runs it as a job.
// InstallCommand 返回在本节点操作系统上安装厂商工具的标题与 argv（sh -c 加常量安装脚本）。调用方套用 Options.Wrap
// 后以作业运行。
func InstallCommand(vendor string) (string, []string, error) {
	m, err := InstallScript(vendor)
	if err != nil {
		return "", nil, err
	}
	return m.Title, []string{"sh", "-c", m.Script}, nil
}

// Run runs a setting argv from Command (no shell; prefixed with sudo -n when configured) and returns its combined
// output and exit code.
// Run 执行 Command 生成的设置 argv（不经 shell；按配置加 sudo -n 前缀），返回合并后的输出与退出码。
func Run(ctx context.Context, opts Options, argv []string) (string, int, error) {
	if len(argv) == 0 {
		return "", -1, errors.New("empty command")
	}
	args := opts.Wrap(argv)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return buf.String(), code, err
}

// DeviceIndices returns the indices of the devices of vendor in st (used to expand "every GPU").
// DeviceIndices 返回 st 中该厂商设备的序号（用于展开“所有 GPU”）。
func DeviceIndices(st Status, vendor string) []int {
	var out []int
	for _, d := range st.Devices {
		if d.Vendor == vendor {
			out = append(out, d.Index)
		}
	}
	if len(out) == 0 {
		for _, v := range st.Vendors {
			if v.ID == vendor {
				for i := 0; i < v.Detected; i++ {
					out = append(out, i)
				}
			}
		}
	}
	return out
}

// osFamily maps /etc/os-release to an install family.
// osFamily 把 /etc/os-release 映射为安装方式类别。
func osFamily() string {
	if runtime.GOOS != "linux" {
		return runtime.GOOS
	}
	b, _ := os.ReadFile("/etc/os-release")
	var ids []string
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			ids = append(ids, strings.Trim(v, `"`))
		}
		if v, ok := strings.CutPrefix(line, "ID_LIKE="); ok {
			ids = append(ids, strings.Fields(strings.Trim(v, `"`))...)
		}
	}
	for _, id := range ids {
		switch id {
		case "debian", "ubuntu", "linuxmint", "pop", "kylin", "uos", "deepin":
			return "debian"
		case "rhel", "centos", "fedora", "rocky", "almalinux", "ol", "anolis", "openEuler", "openeuler", "kylinsec":
			return "rhel"
		}
	}
	return "linux"
}

// pciVendors counts the display controllers (class 0x03) and processing accelerators (class 0x12) per vendor.
// pciVendors 按厂商统计显示控制器（类 0x03）与处理加速器（类 0x12）的数量。
func pciVendors() map[string]int {
	out := map[string]int{}
	if runtime.GOOS != "linux" {
		return out
	}
	dirs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, d := range dirs {
		class := readTrim(filepath.Join(d, "class"))
		if !strings.HasPrefix(class, "0x03") && !strings.HasPrefix(class, "0x12") {
			continue
		}
		vid := strings.TrimPrefix(strings.ToLower(readTrim(filepath.Join(d, "vendor"))), "0x")
		for _, v := range Vendors {
			if slices.Contains(v.PCIVendors, vid) {
				out[v.ID]++
			}
		}
	}
	return out
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
