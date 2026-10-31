package node

import (
	"context"
	"encoding/csv"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
)

// CollectHW gathers a hardware snapshot of this machine. It uses only the standard library, procfs and
// common vendor tools (nvidia-smi, sysctl, system_profiler) so it works without cgo.
//
// CollectHW 采集本机硬件快照。仅使用标准库、procfs 与常见厂商工具（nvidia-smi、sysctl、system_profiler），
// 无需 cgo。
func CollectHW() model.HWInfo {
	h := model.HWInfo{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUCores:    runtime.NumCPU(),
		CollectedAt: time.Now().Unix(),
	}
	h.Hostname, _ = os.Hostname()
	collectPlatform(&h)
	h.Disks = collectDisks()
	h.GPUs = append(collectNvidiaGPUs(), collectPlatformGPUs(&h)...)
	h.IPs = localIPs()
	if h.GPUs == nil {
		h.GPUs = []model.GPUInfo{}
	}
	if h.Disks == nil {
		h.Disks = []model.DiskInfo{}
	}
	return h
}

// runCmd runs a short command and returns its trimmed stdout ("" on any error).
// runCmd 执行短命令并返回去除首尾空白的 stdout（出错时返回 ""）。
func runCmd(timeout time.Duration, name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func collectNvidiaGPUs() []model.GPUInfo {
	out := runCmd(5*time.Second, "nvidia-smi",
		"--query-gpu=index,name,memory.total,memory.used,utilization.gpu,temperature.gpu,driver_version,power.draw,power.limit",
		"--format=csv,noheader,nounits")
	if out == "" {
		return nil
	}
	return parseNvidiaSMI(out)
}

func parseNvidiaSMI(out string) []model.GPUInfo {
	r := csv.NewReader(strings.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil
	}
	var gpus []model.GPUInfo
	for _, f := range recs {
		if len(f) < 7 {
			continue
		}
		g := model.GPUInfo{Vendor: "nvidia", Name: f[1], Driver: f[6]}
		g.Index, _ = strconv.Atoi(f[0])
		g.MemoryMB, _ = strconv.ParseInt(f[2], 10, 64)
		g.MemoryUsed, _ = strconv.ParseInt(f[3], 10, 64)
		g.Utilization, _ = strconv.ParseFloat(f[4], 64)
		g.Temperature, _ = strconv.ParseFloat(f[5], 64)
		if len(f) >= 9 {
			g.PowerDraw, _ = strconv.ParseFloat(f[7], 64)
			g.PowerLimit, _ = strconv.ParseFloat(f[8], 64)
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func localIPs() []string {
	ips := []string{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, ipn.IP.String())
		}
	}
	sort.Strings(ips)
	return ips
}
