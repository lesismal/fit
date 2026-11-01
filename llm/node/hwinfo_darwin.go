package node

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
)

func collectPlatform(h *model.HWInfo) {
	h.CPUModel = runCmd(2*time.Second, "sysctl", "-n", "machdep.cpu.brand_string")
	h.Kernel = runCmd(2*time.Second, "sysctl", "-n", "kern.osrelease")
	if v := runCmd(2*time.Second, "sw_vers", "-productVersion"); v != "" {
		h.Platform = "macOS " + v
	}
	if f := strings.Fields(strings.Trim(runCmd(2*time.Second, "sysctl", "-n", "vm.loadavg"), "{} ")); len(f) > 0 {
		h.Load1, _ = strconv.ParseFloat(f[0], 64)
	}
	if m := regexp.MustCompile(`sec = (\d+)`).FindStringSubmatch(runCmd(2*time.Second, "sysctl", "-n", "kern.boottime")); m != nil {
		if boot, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			h.UptimeSeconds = time.Now().Unix() - boot
		}
	}
	total, _ := strconv.ParseInt(runCmd(2*time.Second, "sysctl", "-n", "hw.memsize"), 10, 64)
	h.MemTotalMB = total >> 20
	h.MemAvailMB = vmStatAvailable() >> 20
}

// vmStatAvailable estimates available memory (free + inactive + speculative + purgeable pages) in bytes.
// vmStatAvailable 估算可用内存（free + inactive + speculative + purgeable 页），单位字节。
func vmStatAvailable() int64 {
	out := runCmd(2*time.Second, "vm_stat")
	if out == "" {
		return 0
	}
	pageSize := int64(4096)
	if m := regexp.MustCompile(`page size of (\d+) bytes`).FindStringSubmatch(out); m != nil {
		pageSize, _ = strconv.ParseInt(m[1], 10, 64)
	}
	var pages int64
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
			pages += n
		}
	}
	return pages * pageSize
}

var (
	darwinGPUOnce sync.Once
	darwinGPUs    []model.GPUInfo
)

// collectPlatformGPUs reads the GPUs from system_profiler once (it is slow); Apple silicon GPUs share
// system memory, so their memory is reported as the total RAM.
//
// collectPlatformGPUs 只读取一次 system_profiler（较慢）；Apple 芯片 GPU 共享系统内存，显存按总内存上报。
func collectPlatformGPUs(h *model.HWInfo) []model.GPUInfo {
	darwinGPUOnce.Do(func() {
		out := runCmd(15*time.Second, "system_profiler", "SPDisplaysDataType", "-json")
		var doc struct {
			Displays []map[string]any `json:"SPDisplaysDataType"`
		}
		if json.Unmarshal([]byte(out), &doc) != nil {
			return
		}
		for i, d := range doc.Displays {
			name, _ := d["sppci_model"].(string)
			vendor, _ := d["spdisplays_vendor"].(string)
			g := model.GPUInfo{Index: i, Name: name, Vendor: strings.TrimPrefix(vendor, "sppci_vendor_")}
			if cores, _ := d["sppci_cores"].(string); cores != "" {
				g.Name += " (" + cores + " cores)"
			}
			if vram, _ := d["spdisplays_vram"].(string); vram != "" {
				f := strings.Fields(vram)
				if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					if len(f) > 1 && strings.EqualFold(f[1], "GB") {
						n <<= 10
					}
					g.MemoryMB = n
				}
			}
			darwinGPUs = append(darwinGPUs, g)
		}
	})
	out := make([]model.GPUInfo, len(darwinGPUs))
	copy(out, darwinGPUs)
	for i := range out {
		if out[i].MemoryMB == 0 && strings.HasPrefix(h.CPUModel, "Apple") {
			out[i].Vendor, out[i].MemoryMB = "apple", h.MemTotalMB
		}
	}
	return out
}

func collectDisks() []model.DiskInfo {
	out := runCmd(5*time.Second, "df", "-k", "-P", "-l")
	var disks []model.DiskInfo
	// Volumes of one APFS container share its space: report each container once.
	// 同一 APFS 容器内的卷共享空间：每个容器只上报一次。
	seen := map[[2]int64]bool{}
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 6 || !strings.HasPrefix(f[0], "/dev/") {
			continue
		}
		mount := strings.Join(f[5:], " ")
		// Skip the APFS system snapshots / helper volumes, keep "/" and the data volume.
		// 跳过 APFS 系统快照与辅助卷，保留 "/" 与数据卷。
		if strings.HasPrefix(mount, "/System/Volumes/") && mount != "/System/Volumes/Data" {
			continue
		}
		total, _ := strconv.ParseInt(f[1], 10, 64)
		free, _ := strconv.ParseInt(f[3], 10, 64)
		k := [2]int64{total, free}
		if seen[k] {
			continue
		}
		seen[k] = true
		disks = append(disks, model.DiskInfo{Mount: mount, FSType: "apfs", TotalMB: total >> 10, FreeMB: free >> 10})
	}
	return disks
}
