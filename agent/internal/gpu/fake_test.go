package gpu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeTools puts shell scripts standing in for the vendors' tools on a PATH of their own: name -> script body.
// fakeTools 把代替厂商工具的 shell 脚本放到单独的 PATH 上：名字 -> 脚本内容。
func fakeTools(t *testing.T, tools map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// printing is a script body printing out (through a file, so any bytes do) and exiting with code.
// printing 是输出 out（经文件，任意字节皆可）并以 code 退出的脚本内容。
func printing(t *testing.T, out string, code int) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return "/bin/cat '" + f + "'\nexit " + strconv.Itoa(code)
}

const (
	nvidiaOut = "0, NVIDIA A10, GPU-a, 550.1, 24564, 100, 5, 40, 20.5, 150.00, 100.00, 150.00, 150.00, 210, 405, 1695, 6251, 30, P8, Enabled, Default\n"
	rocmOut   = `{"card0": {"Card Series": "MI210"}, "system": {"Driver version": "6.7.0"}}`
	appleOut  = `{"SPDisplaysDataType": [{"sppci_model": "Apple M2", "sppci_cores": "10"}]}`
	xpuDisc   = `{"device_list": [{"device_id": 0, "device_name": "Arc A770", "uuid": "u0"}, {"device_id": 1, "device_name": "Arc A380", "uuid": "u1"}]}`
	xpuStats  = `{"device_level": [{"metrics_type": "XPUM_STATS_POWER", "value": 35}]}`
)

func vendor(t *testing.T, id string) *Vendor {
	t.Helper()
	v, ok := VendorByID(id)
	if !ok {
		t.Fatalf("no vendor %s", id)
	}
	return v
}

func TestQueryFakeTools(t *testing.T) {
	ctx := context.Background()
	t.Run("nvidia", func(t *testing.T) {
		fakeTools(t, map[string]string{"nvidia-smi": printing(t, nvidiaOut, 0)})
		devs, raw, err := query(ctx, vendor(t, "nvidia"))
		if err != nil || raw != "" || len(devs) != 1 || devs[0].Name != "NVIDIA A10" {
			t.Fatalf("devs = %+v, raw = %q, err = %v", devs, raw, err)
		}
		fakeTools(t, map[string]string{"nvidia-smi": printing(t, "NVIDIA-SMI has failed", 9)})
		if devs, raw, err := query(ctx, vendor(t, "nvidia")); err == nil || devs != nil || raw != "NVIDIA-SMI has failed" {
			t.Fatalf("failing tool: devs = %+v, raw = %q, err = %v", devs, raw, err)
		}
	})
	t.Run("amd", func(t *testing.T) {
		fakeTools(t, map[string]string{"rocm-smi": printing(t, rocmOut, 0)})
		if devs, _, err := query(ctx, vendor(t, "amd")); err != nil || len(devs) != 1 || devs[0].Driver != "6.7.0" {
			t.Fatalf("devs = %+v, err = %v", devs, err)
		}
		// rocm-smi exits non-zero when one of the many values is missing, yet prints them: the JSON is still read.
		// 某个值缺失时 rocm-smi 以非零退出，但仍会输出：照样读取 JSON。
		fakeTools(t, map[string]string{"rocm-smi": printing(t, rocmOut, 2)})
		if devs, _, err := query(ctx, vendor(t, "amd")); err != nil || len(devs) != 1 {
			t.Fatalf("non-zero exit with JSON: devs = %+v, err = %v", devs, err)
		}
		fakeTools(t, map[string]string{"rocm-smi": printing(t, "no driver", 2)})
		if _, raw, err := query(ctx, vendor(t, "amd")); err == nil || raw != "no driver" {
			t.Fatalf("failing tool: raw = %q, err = %v", raw, err)
		}
		fakeTools(t, map[string]string{"rocm-smi": printing(t, "{not json", 0)})
		if _, raw, err := query(ctx, vendor(t, "amd")); err == nil || raw != "{not json" {
			t.Fatalf("bad JSON: raw = %q, err = %v", raw, err)
		}
	})
	t.Run("intel", func(t *testing.T) {
		// Device 1's stats fail: it is listed with unknown values.
		// 设备 1 的统计失败：仍列出，数值未知。
		fakeTools(t, map[string]string{"xpu-smi": `case "$1 $3" in
"discovery ") ` + printing(t, xpuDisc, 0) + ` ;;
"stats 0") ` + printing(t, xpuStats, 0) + ` ;;
*) exit 1 ;;
esac`})
		devs, raw, err := query(ctx, vendor(t, "intel"))
		if err != nil || raw != "" || len(devs) != 2 {
			t.Fatalf("devs = %+v, raw = %q, err = %v", devs, raw, err)
		}
		if devs[0].PowerDraw != 35 || devs[0].Name != "Arc A770" || devs[0].UUID != "u0" || devs[1].PowerDraw != -1 {
			t.Fatalf("devs = %+v", devs)
		}
		fakeTools(t, map[string]string{"xpu-smi": printing(t, "no device", 1)})
		if _, raw, err := query(ctx, vendor(t, "intel")); err == nil || raw != "no device" {
			t.Fatalf("failing tool: raw = %q, err = %v", raw, err)
		}
		fakeTools(t, map[string]string{"xpu-smi": printing(t, "[", 0)})
		if _, raw, err := query(ctx, vendor(t, "intel")); err == nil || !strings.Contains(err.Error(), "discovery") || raw != "[" {
			t.Fatalf("bad JSON: raw = %q, err = %v", raw, err)
		}
	})
	t.Run("apple", func(t *testing.T) {
		fakeTools(t, map[string]string{"system_profiler": printing(t, appleOut, 0)})
		if devs, _, err := query(ctx, vendor(t, "apple")); err != nil || len(devs) != 1 || devs[0].Name != "Apple M2 (10 cores)" {
			t.Fatalf("devs = %+v, err = %v", devs, err)
		}
		fakeTools(t, map[string]string{"system_profiler": printing(t, "denied", 1)})
		if _, raw, err := query(ctx, vendor(t, "apple")); err == nil || raw != "denied" {
			t.Fatalf("failing tool: raw = %q, err = %v", raw, err)
		}
	})
	t.Run("raw", func(t *testing.T) {
		fakeTools(t, map[string]string{
			"hy-smi":       printing(t, "hygon table", 0),
			"npu-smi":      printing(t, "ascend table", 0),
			"mthreads-gmi": printing(t, "mthreads table", 3),
		})
		for id, want := range map[string]string{"hygon": "hygon table", "ascend": "ascend table", "mthreads": "mthreads table"} {
			devs, raw, err := query(ctx, vendor(t, id))
			if devs != nil || raw != want || (err != nil) != (id == "mthreads") {
				t.Errorf("%s: devs = %+v, raw = %q, err = %v", id, devs, raw, err)
			}
		}
	})
	if devs, raw, err := query(ctx, &Vendor{ID: "nope"}); devs != nil || raw != "" || err != nil {
		t.Fatalf("unknown vendor: %+v %q %v", devs, raw, err)
	}
}

func TestCollectFakeTools(t *testing.T) {
	fakeTools(t, map[string]string{
		"nvidia-smi":      printing(t, nvidiaOut, 0),
		"nvidia-settings": "exit 0",
		"rocm-smi":        printing(t, "rocm down", 1),
		"system_profiler": printing(t, appleOut, 0),
		"hy-smi":          printing(t, "hygon table", 0),
	})
	st := Collect(context.Background(), Options{})
	byID := map[string]VendorStatus{}
	for _, v := range st.Vendors {
		byID[v.ID] = v
	}
	if nv := byID["nvidia"]; !nv.Installed || nv.ToolPath == "" || !nv.ExtraFound["nvidia-settings"] {
		t.Fatalf("nvidia = %+v", nv)
	}
	if amd := byID["amd"]; !amd.Installed || amd.ExtraFound["amd-smi"] {
		t.Fatalf("amd = %+v", amd)
	}
	if byID["intel"].Installed {
		t.Fatal("intel installed without xpu-smi")
	}
	if st.Errors["amd"] == "" || st.Raw["amd"] != "rocm down" || st.Raw["hygon"] != "hygon table" {
		t.Fatalf("errors = %v, raw = %v", st.Errors, st.Raw)
	}
	var vendors []string
	for _, d := range st.Devices {
		vendors = append(vendors, d.Vendor)
	}
	if !slices.Equal(vendors, []string{"nvidia", "apple"}) {
		t.Fatalf("devices of %v", vendors)
	}
}

func TestRun(t *testing.T) {
	if _, code, err := Run(context.Background(), Options{}, nil); err == nil || code != -1 {
		t.Fatalf("empty argv: %d %v", code, err)
	}
	fakeTools(t, map[string]string{"ok": printing(t, "done", 0), "fail": printing(t, "oops", 4)})
	if out, code, err := Run(context.Background(), Options{}, []string{"ok", "x"}); err != nil || code != 0 || out != "done" {
		t.Fatalf("ok: %q %d %v", out, code, err)
	}
	if out, code, err := Run(context.Background(), Options{}, []string{"fail"}); err == nil || code != 4 || out != "oops" {
		t.Fatalf("fail: %q %d %v", out, code, err)
	}
	if _, code, err := Run(context.Background(), Options{}, []string{"missing-tool"}); err == nil || code != -1 {
		t.Fatalf("missing: %d %v", code, err)
	}
}

func TestWrapSudo(t *testing.T) {
	got := Options{UseSudo: true}.Wrap([]string{"nvidia-smi", "-pm", "1"})
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		if !slices.Equal(got, []string{"nvidia-smi", "-pm", "1"}) {
			t.Fatalf("wrap as root = %q", got)
		}
		return
	}
	want := []string{"sudo", "-n", "--preserve-env=DISPLAY,XAUTHORITY," + EnvDataDir, "nvidia-smi", "-pm", "1"}
	if !slices.Equal(got, want) {
		t.Fatalf("wrap = %q", got)
	}
}

func TestDeviceIndices(t *testing.T) {
	st := Status{
		Devices: []Device{{Vendor: "nvidia", Index: 2}, {Vendor: "amd", Index: 0}, {Vendor: "nvidia", Index: 5}},
		Vendors: []VendorStatus{{Vendor: Vendor{ID: "intel"}, Detected: 3}, {Vendor: Vendor{ID: "nvidia"}, Detected: 8}},
	}
	if got := DeviceIndices(st, "nvidia"); !slices.Equal(got, []int{2, 5}) {
		t.Fatalf("nvidia = %v", got)
	}
	// No device read: every GPU found on the bus.
	// 未读到设备：总线上发现的每块 GPU。
	if got := DeviceIndices(st, "intel"); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("intel = %v", got)
	}
	if got := DeviceIndices(st, "ascend"); got != nil {
		t.Fatalf("ascend = %v", got)
	}
}

func TestInstallFor(t *testing.T) {
	v := &Vendor{Install: []InstallMethod{{OS: "debian", Title: "deb"}, {OS: "any", Title: "any"}}}
	for family, want := range map[string]string{"debian": "deb", "rhel": "any", "linux": "any", "darwin": "", "windows": ""} {
		got := ""
		if m := installFor(v, family); m != nil {
			got = m.Title
		}
		if got != want {
			t.Errorf("%s: %q, want %q", family, got, want)
		}
	}
	// A vendor with no method for this OS: apple everywhere but darwin, nvidia on darwin.
	// 本操作系统没有安装方式的厂商：除 darwin 外的 apple，darwin 上的 nvidia。
	id := "apple"
	if osFamily() == "darwin" {
		id = "nvidia"
	}
	if _, err := InstallScript(id); err == nil || !strings.Contains(err.Error(), "no install method") {
		t.Fatalf("%s: err = %v", id, err)
	}
	if _, _, err := InstallCommand(id); err == nil {
		t.Fatalf("%s: install command without a method", id)
	}
	ok := 0
	for _, v := range Vendors {
		if title, argv, err := InstallCommand(v.ID); err == nil && title != "" && len(argv) == 3 {
			ok++
		}
	}
	if ok == 0 {
		t.Fatalf("no vendor installs on %s", osFamily())
	}
}

func TestCommandMore(t *testing.T) {
	if _, err := Command("nope", 0, "power_limit", "1"); err == nil {
		t.Error("unknown vendor accepted")
	}
	if _, err := Command("nvidia", 1025, "power_limit", "100"); err == nil {
		t.Error("index 1025 accepted")
	}
	if cmd, err := Command("nvidia", 0, "compute_mode", " DEFAULT "); err != nil || !slices.Contains(cmd, "DEFAULT") {
		t.Fatalf("enum: %q %v", cmd, err)
	}
	t.Setenv("DISPLAY", "host-1.example:1.0")
	if got := display(); got != "host-1.example:1.0" {
		t.Fatalf("display = %q", got)
	}
	saved := Vendors
	t.Cleanup(func() { Vendors = saved })
	Vendors = []Vendor{{ID: "x", Settings: []Setting{{Key: "k", Kind: "bogus", Command: "x"}}}}
	if _, err := Command("x", 0, "k", "1"); err == nil || !strings.Contains(err.Error(), "bad setting kind") {
		t.Fatalf("bad kind: %v", err)
	}
}

func TestParsersBadInput(t *testing.T) {
	// A CSV error, a short record, and an index that is not a number.
	// CSV 错误、字段不足的记录、非数字的序号。
	if devs := ParseNvidiaSMI("0, \"a\"b\n"); devs != nil {
		t.Errorf("bad CSV: %+v", devs)
	}
	if devs := ParseNvidiaSMI("0, short\n" + strings.Replace(nvidiaOut, "0,", "x,", 1)); devs != nil {
		t.Errorf("skipped records: %+v", devs)
	}
	if _, err := ParseRocmSMI("garbage"); err == nil {
		t.Error("rocm garbage accepted")
	}
	devs, err := ParseRocmSMI(`{"card2": {"Device Name": "Navi", "sclk clock speed:": "n/a"}, "cardx": {}}`)
	if err != nil || len(devs) != 1 || devs[0].Name != "Navi" || devs[0].ClockGfx != -1 || devs[0].Driver != "" {
		t.Fatalf("rocm = %+v, %v", devs, err)
	}
	if devs := ParseAppleDisplays("nope"); devs != nil {
		t.Errorf("apple garbage: %+v", devs)
	}
	if devs := ParseAppleDisplays(`{"SPDisplaysDataType": [{"_name": "kHW_AppleM1Item"}]}`); len(devs) != 1 || devs[0].Name != "kHW_AppleM1Item" {
		t.Errorf("apple _name: %+v", devs)
	}
}

func TestApplyXPUStats(t *testing.T) {
	d := unknownDevice("intel", 0)
	ApplyXPUStats(&d, "not json")
	if d.PowerDraw != -1 {
		t.Fatalf("bad JSON changed %+v", d)
	}
	ApplyXPUStats(&d, `{"device_level": [
		{"metrics_type": "XPUM_STATS_POWER", "value": 1}, {"metrics_type": "XPUM_STATS_GPU_FREQUENCY", "value": 2},
		{"metrics_type": "XPUM_STATS_GPU_CORE_TEMPERATURE", "value": 3}, {"metrics_type": "XPUM_STATS_MEMORY_USED", "value": 4},
		{"metrics_type": "XPUM_STATS_GPU_UTILIZATION", "value": 5}, {"metrics_type": "XPUM_STATS_MEMORY_FREQUENCY", "value": 6},
		{"metrics_type": "XPUM_STATS_OTHER", "value": 7}]}`)
	if d.PowerDraw != 1 || d.ClockGfx != 2 || d.Temperature != 3 || d.MemUsedMB != 4 || d.Utilization != 5 || d.ClockMem != 6 {
		t.Fatalf("stats = %+v", d)
	}
}
