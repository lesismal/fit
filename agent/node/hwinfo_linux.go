package node

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/lesismal/fit/agent/internal/model"
)

func collectPlatform(h *model.HWInfo) {
	h.Kernel = readTrim("/proc/sys/kernel/osrelease")
	h.Platform = osRelease()
	h.CPUModel = cpuModel()
	if f := strings.Fields(readTrim("/proc/loadavg")); len(f) > 0 {
		h.Load1, _ = strconv.ParseFloat(f[0], 64)
	}
	if f := strings.Fields(readTrim("/proc/uptime")); len(f) > 0 {
		up, _ := strconv.ParseFloat(f[0], 64)
		h.UptimeSeconds = int64(up)
	}
	mem := meminfo()
	h.MemTotalMB = mem["MemTotal"] / 1024
	h.MemAvailMB = mem["MemAvailable"] / 1024
}

func collectPlatformGPUs(*model.HWInfo) []model.GPUInfo { return nil }

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func osRelease() string {
	for _, line := range strings.Split(readTrim("/etc/os-release"), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name", "Model", "Hardware", "cpu model":
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// meminfo returns /proc/meminfo values in kB.
// meminfo 返回 /proc/meminfo 中的值（kB）。
func meminfo() map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(readTrim("/proc/meminfo"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if f := strings.Fields(v); len(f) > 0 {
			out[k], _ = strconv.ParseInt(f[0], 10, 64)
		}
	}
	return out
}

var diskFSTypes = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "jfs": true, "reiserfs": true, "vfat": true, "exfat": true, "ntfs": true, "ntfs3": true,
	"nfs": true, "nfs4": true, "cifs": true, "fuseblk": true, "overlay": false,
}

func collectDisks() []model.DiskInfo {
	var out []model.DiskInfo
	seen := map[string]bool{}
	for _, line := range strings.Split(readTrim("/proc/mounts"), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !diskFSTypes[f[2]] || seen[f[0]] {
			continue
		}
		mount := strings.ReplaceAll(f[1], `\040`, " ")
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil || st.Blocks == 0 {
			continue
		}
		seen[f[0]] = true
		bs := uint64(st.Bsize)
		out = append(out, model.DiskInfo{
			Mount:   mount,
			FSType:  f[2],
			TotalMB: int64(uint64(st.Blocks) * bs >> 20),
			FreeMB:  int64(uint64(st.Bavail) * bs >> 20),
		})
	}
	return out
}
