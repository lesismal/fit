package gpu

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// query reads the GPUs of a vendor whose tool is installed. Vendors without a parser return the raw output.
// query 读取已安装工具的厂商的 GPU。没有解析器的厂商返回原始输出。
func query(ctx context.Context, v *Vendor) ([]Device, string, error) {
	switch v.ID {
	case "nvidia":
		out, err := output(ctx, "nvidia-smi", "--query-gpu=index,name,uuid,driver_version,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,power.limit,power.min_limit,power.max_limit,power.default_limit,clocks.gr,clocks.mem,clocks.max.gr,clocks.max.mem,fan.speed,pstate,persistence_mode,compute_mode",
			"--format=csv,noheader,nounits")
		if err != nil {
			return nil, out, err
		}
		return ParseNvidiaSMI(out), "", nil
	case "amd":
		out, err := output(ctx, "rocm-smi", "--showproductname", "--showdriverversion", "--showtemp", "--showpower", "--showmaxpower",
			"--showuse", "--showmeminfo", "vram", "--showclocks", "--showfan", "--showperflevel", "--json")
		if err != nil && !strings.HasPrefix(strings.TrimSpace(out), "{") {
			return nil, out, err
		}
		devs, perr := ParseRocmSMI(out)
		if perr != nil {
			return nil, out, perr
		}
		return devs, "", nil
	case "intel":
		return queryIntel(ctx)
	case "apple":
		out, err := output(ctx, "system_profiler", "SPDisplaysDataType", "-json")
		if err != nil {
			return nil, out, err
		}
		return ParseAppleDisplays(out), "", nil
	case "hygon":
		out, err := output(ctx, "hy-smi")
		return nil, out, err
	case "ascend":
		out, err := output(ctx, "npu-smi", "info")
		return nil, out, err
	case "mthreads":
		out, err := output(ctx, "mthreads-gmi")
		return nil, out, err
	}
	return nil, "", nil
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("%s: %w", name, err)
	}
	return string(b), nil
}

// num parses a number from tool output; "[N/A]", "N/A", "" and other non-numbers become -1.
// num 解析工具输出中的数字；"[N/A]"、"N/A"、"" 等非数字为 -1。
func num(s string) float64 {
	s = strings.TrimSpace(s)
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return f
}

func unknownDevice(vendor string, index int) Device {
	return Device{Vendor: vendor, Index: index, MemTotalMB: -1, MemUsedMB: -1, Utilization: -1, Temperature: -1,
		PowerDraw: -1, PowerLimit: -1, PowerMin: -1, PowerMax: -1, PowerDefault: -1,
		ClockGfx: -1, ClockMem: -1, ClockGfxMax: -1, ClockMemMax: -1, FanSpeed: -1}
}

// ParseNvidiaSMI parses the CSV of the nvidia-smi query in Vendors.
// ParseNvidiaSMI 解析 Vendors 中 nvidia-smi 查询的 CSV。
func ParseNvidiaSMI(out string) []Device {
	r := csv.NewReader(strings.NewReader(out))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return nil
	}
	var devs []Device
	for _, f := range recs {
		if len(f) < 21 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		d := unknownDevice("nvidia", idx)
		d.Name, d.UUID, d.Driver = f[1], f[2], f[3]
		d.MemTotalMB, d.MemUsedMB = num(f[4]), num(f[5])
		d.Utilization, d.Temperature = num(f[6]), num(f[7])
		d.PowerDraw, d.PowerLimit, d.PowerMin, d.PowerMax, d.PowerDefault = num(f[8]), num(f[9]), num(f[10]), num(f[11]), num(f[12])
		d.ClockGfx, d.ClockMem, d.ClockGfxMax, d.ClockMemMax = num(f[13]), num(f[14]), num(f[15]), num(f[16])
		d.FanSpeed = num(f[17])
		d.PState, d.Persistence, d.ComputeMode = f[18], f[19], f[20]
		devs = append(devs, d)
	}
	return devs
}

var (
	reCard  = regexp.MustCompile(`^card(\d+)$`)
	reClock = regexp.MustCompile(`\(?(\d+(?:\.\d+)?)\s*Mhz\)?`)
)

// ParseRocmSMI parses `rocm-smi ... --json`. Its keys vary between ROCm releases, so values are matched by
// key substrings.
// ParseRocmSMI 解析 `rocm-smi ... --json`。其键名随 ROCm 版本变化，因此按键名子串匹配。
func ParseRocmSMI(out string) ([]Device, error) {
	if i := strings.Index(out, "{"); i > 0 {
		out = out[i:]
	}
	var data map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &data); err != nil {
		return nil, fmt.Errorf("parse rocm-smi json: %w", err)
	}
	driver := ""
	if sys, ok := data["system"]; ok {
		driver = find(sys, "driver version")
	}
	var devs []Device
	for card, kv := range data {
		m := reCard.FindStringSubmatch(card)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		d := unknownDevice("amd", idx)
		d.Name = firstNonEmpty(find(kv, "card series"), find(kv, "device name"), find(kv, "card model"))
		d.Driver = firstNonEmpty(find(kv, "driver version"), driver)
		d.Temperature = num(firstNonEmpty(find(kv, "temperature (sensor edge)"), find(kv, "temperature (sensor junction)")))
		d.PowerDraw = num(firstNonEmpty(find(kv, "average graphics package power"), find(kv, "current socket graphics package power")))
		d.PowerLimit = num(find(kv, "max graphics package power"))
		d.Utilization = num(find(kv, "gpu use"))
		if b := num(find(kv, "vram total memory")); b >= 0 {
			d.MemTotalMB = b / (1 << 20)
		}
		if b := num(find(kv, "vram total used memory")); b >= 0 {
			d.MemUsedMB = b / (1 << 20)
		}
		d.ClockGfx = clock(find(kv, "sclk clock speed"))
		d.ClockMem = clock(find(kv, "mclk clock speed"))
		d.FanSpeed = num(find(kv, "fan speed (%)"))
		d.PerfLevel = find(kv, "performance level")
		devs = append(devs, d)
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Index < devs[j].Index })
	return devs, nil
}

func find(kv map[string]string, sub string) string {
	for k, v := range kv {
		if strings.Contains(strings.ToLower(k), sub) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func clock(s string) float64 {
	if m := reClock.FindStringSubmatch(s); m != nil {
		return num(m[1])
	}
	return -1
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func queryIntel(ctx context.Context) ([]Device, string, error) {
	out, err := output(ctx, "xpu-smi", "discovery", "-j")
	if err != nil {
		return nil, out, err
	}
	var disc struct {
		DeviceList []struct {
			DeviceID   int    `json:"device_id"`
			DeviceName string `json:"device_name"`
			UUID       string `json:"uuid"`
		} `json:"device_list"`
	}
	if err := json.Unmarshal([]byte(out), &disc); err != nil {
		return nil, out, fmt.Errorf("parse xpu-smi discovery: %w", err)
	}
	var devs []Device
	for _, dd := range disc.DeviceList {
		d := unknownDevice("intel", dd.DeviceID)
		d.Name, d.UUID = dd.DeviceName, dd.UUID
		if s, err := output(ctx, "xpu-smi", "stats", "-d", strconv.Itoa(dd.DeviceID), "-j"); err == nil {
			ApplyXPUStats(&d, s)
		}
		devs = append(devs, d)
	}
	return devs, "", nil
}

// ApplyXPUStats fills d from `xpu-smi stats -d N -j`.
// ApplyXPUStats 用 `xpu-smi stats -d N -j` 的输出填充 d。
func ApplyXPUStats(d *Device, out string) {
	var st struct {
		DeviceLevel []struct {
			Type  string  `json:"metrics_type"`
			Value float64 `json:"value"`
		} `json:"device_level"`
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return
	}
	for _, m := range st.DeviceLevel {
		switch m.Type {
		case "XPUM_STATS_POWER":
			d.PowerDraw = m.Value
		case "XPUM_STATS_GPU_FREQUENCY":
			d.ClockGfx = m.Value
		case "XPUM_STATS_GPU_CORE_TEMPERATURE":
			d.Temperature = m.Value
		case "XPUM_STATS_MEMORY_USED":
			d.MemUsedMB = m.Value
		case "XPUM_STATS_GPU_UTILIZATION":
			d.Utilization = m.Value
		case "XPUM_STATS_MEMORY_FREQUENCY":
			d.ClockMem = m.Value
		}
	}
}

// ParseAppleDisplays parses `system_profiler SPDisplaysDataType -json`.
// ParseAppleDisplays 解析 `system_profiler SPDisplaysDataType -json`。
func ParseAppleDisplays(out string) []Device {
	var data struct {
		Items []map[string]any `json:"SPDisplaysDataType"`
	}
	if json.Unmarshal([]byte(out), &data) != nil {
		return nil
	}
	var devs []Device
	for i, it := range data.Items {
		d := unknownDevice("apple", i)
		d.Name, _ = it["sppci_model"].(string)
		if d.Name == "" {
			d.Name, _ = it["_name"].(string)
		}
		if cores, _ := it["sppci_cores"].(string); cores != "" {
			d.Name += " (" + cores + " cores)"
		}
		devs = append(devs, d)
	}
	return devs
}
