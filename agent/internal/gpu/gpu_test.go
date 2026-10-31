package gpu

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestParseNvidiaSMI(t *testing.T) {
	out := "0, NVIDIA GeForce RTX 4090, GPU-abc, 550.54.15, 24564, 1024, 37, 45, 30.52, 450.00, 150.00, 600.00, 450.00, 210, 405, 3120, 10501, 30, P8, Enabled, Default\n" +
		"1, NVIDIA A100, GPU-def, 550.54.15, 81920, [N/A], 0, 30, [N/A], 400.00, 100.00, 400.00, 400.00, 1410, 1593, 1410, 1593, [N/A], P0, Disabled, Default\n"
	devs := ParseNvidiaSMI(out)
	if len(devs) != 2 {
		t.Fatalf("devices = %d", len(devs))
	}
	d := devs[0]
	if d.Name != "NVIDIA GeForce RTX 4090" || d.PowerLimit != 450 || d.PowerMax != 600 || d.ClockGfxMax != 3120 || d.Persistence != "Enabled" {
		t.Fatalf("device 0 = %+v", d)
	}
	if devs[1].MemUsedMB != -1 || devs[1].FanSpeed != -1 || devs[1].Index != 1 {
		t.Fatalf("N/A values not -1: %+v", devs[1])
	}
}

func TestParseRocmSMI(t *testing.T) {
	out := `WARNING: something
{"card1": {"Card Series": "Instinct MI210", "Temperature (Sensor edge) (C)": "36.0", "Average Graphics Package Power (W)": "41.0",
 "Max Graphics Package Power (W)": "300.0", "GPU use (%)": "12", "VRAM Total Memory (B)": "68702699520", "VRAM Total Used Memory (B)": "1073741824",
 "sclk clock speed:": "(800Mhz)", "mclk clock speed:": "(1600Mhz)", "Fan speed (%)": "20", "Performance Level": "auto"},
 "card0": {"Card Series": "Radeon RX 7900 XTX"}, "system": {"Driver version": "6.7.0"}}`
	devs, err := ParseRocmSMI(out)
	if err != nil || len(devs) != 2 {
		t.Fatalf("devs = %+v, err = %v", devs, err)
	}
	d := devs[1]
	if d.Index != 1 || d.Name != "Instinct MI210" || d.PowerLimit != 300 || d.MemUsedMB != 1024 || d.ClockGfx != 800 || d.PerfLevel != "auto" || d.Driver != "6.7.0" {
		t.Fatalf("device = %+v", d)
	}
}

func TestCommandValidation(t *testing.T) {
	cmd, err := Command("nvidia", 1, "power_limit", "250")
	if err != nil || strings.Join(cmd, " ") != "nvidia-smi -i 1 -pl 250" {
		t.Fatalf("cmd = %q, err = %v", cmd, err)
	}
	if cmd, err = Command("nvidia", 0, "gpu_clock_lock", "210, 1800"); err != nil || strings.Join(cmd, " ") != "nvidia-smi -i 0 -lgc 210,1800" {
		t.Fatalf("range cmd = %q, err = %v", cmd, err)
	}
	for _, bad := range []struct{ key, value string }{
		{"power_limit", "250; rm -rf /"}, {"power_limit", "$(id)"}, {"power_limit", "1\n2"}, {"power_limit", "-pl"},
		{"power_limit", "999999"}, {"gpu_clock_lock", "1800,210"}, {"gpu_clock_lock", "1,2;id"},
		{"compute_mode", "BOGUS"}, {"compute_mode", "DEFAULT; id"}, {"nope", "1"},
	} {
		if _, err := Command("nvidia", 0, bad.key, bad.value); err == nil {
			t.Errorf("%s=%q accepted", bad.key, bad.value)
		}
	}
	if _, err := Command("nvidia", -1, "power_limit", "100"); err == nil {
		t.Error("negative index accepted")
	}
	if cmd, err = Command("amd", 2, "fan_auto", "ignored"); err != nil || !strings.Contains(strings.Join(cmd, " "), "-d 2 --resetfans") {
		t.Fatalf("action cmd = %q, err = %v", cmd, err)
	}
	t.Setenv("DISPLAY", `:0"; id; "`)
	if cmd, err = Command("nvidia", 3, "fan_speed", "60"); err != nil ||
		!slices.Equal(cmd, []string{"nvidia-settings", "-c", ":0", "-a", "[gpu:3]/GPUFanControlState=1", "-a", "[fan:3]/GPUTargetFanSpeed=60"}) {
		t.Fatalf("fan cmd = %q, err = %v", cmd, err)
	}
	if got := (Options{}).Wrap([]string{"x"}); !slices.Equal(got, []string{"x"}) {
		t.Fatalf("wrap without sudo = %q", got)
	}
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	placeholder := regexp.MustCompile(`\{[a-z]+\}`)
	for _, v := range Vendors {
		if seen[v.ID] || v.Tool == "" || len(v.Install) == 0 {
			t.Errorf("bad vendor %+v", v.ID)
		}
		seen[v.ID] = true
		for _, s := range v.Settings {
			if s.Command == "" || s.Kind == "" || s.Label == "" || s.LabelZH == "" {
				t.Errorf("%s: bad setting %+v", v.ID, s.Key)
			}
			// Templates are argv, never shell: no shell syntax, only known placeholders.
			// 模板是 argv 而非 shell：不含 shell 语法，只含已知占位符。
			if strings.ContainsAny(s.Command, "$`;|&<>()\\'\"*?~\n") {
				t.Errorf("%s/%s: shell syntax in %q", v.ID, s.Key, s.Command)
			}
			for _, p := range placeholder.FindAllString(s.Command, -1) {
				if !slices.Contains([]string{"{index}", "{value}", "{min}", "{max}", "{display}"}, p) {
					t.Errorf("%s/%s: unknown placeholder %s", v.ID, s.Key, p)
				}
			}
			for _, o := range s.Options {
				if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(o) {
					t.Errorf("%s/%s: bad option %q", v.ID, s.Key, o)
				}
			}
		}
	}
	if _, argv, err := InstallCommand("nvidia"); err == nil && (len(argv) != 3 || argv[0] != "sh") {
		t.Fatalf("install argv = %q", argv)
	}
	if _, _, err := InstallCommand("nope"); err == nil {
		t.Fatal("unknown vendor accepted")
	}
	// Collect must not fail on a machine without any GPU tools.
	// 在没有任何显卡工具的机器上 Collect 也不能出错。
	st := Collect(context.Background(), Options{})
	if len(st.Vendors) != len(Vendors) {
		t.Fatalf("vendors = %d", len(st.Vendors))
	}
}
