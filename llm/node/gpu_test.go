package node

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/gpu"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/pool"
)

// fakeNvidiaSMI answers the GPU queries of gpu.Collect (21 fields) and of
// CollectHW (9 fields), and the setting commands, a power limit of 999
// failing.
const fakeNvidiaSMI = `case "$*" in
*uuid*) echo "0, NVIDIA A10, GPU-a, 550.1, 24564, 100, 5, 40, 20.5, 150.00, 100.00, 150.00, 150.00, 210, 405, 1695, 6251, 30, P8, Enabled, Default" ;;
--query-gpu*) echo "0, NVIDIA A10, 24564, 100, 5, 40, 550.1, 20.5, 150.00" ;;
*"-pl 999"*) echo "refused"; exit 4 ;;
*) echo "set $*" ;;
esac`

func TestGPUMethods(t *testing.T) {
	fakeTools(t, map[string]string{"nvidia-smi": fakeNvidiaSMI})
	s := noSudo()
	s.GPUDataDir = t.TempDir()
	n := startNode(t, &config.LLMNode{Settings: s}, nil)

	res, err := call(n, local, model.MethodNodeGPUStatus, gpuStatusReq{})
	st, _ := res.(gpu.Status)
	if err != nil || len(st.Devices) != 1 || st.Devices[0].Vendor != "nvidia" {
		t.Fatalf("gpu status = %+v %v", res, err)
	}
	// Cached, then collected again when asked fresh.
	if res, _ := call(n, local, model.MethodNodeGPUStatus, gpuStatusReq{}); res.(gpu.Status).CollectedAt != st.CollectedAt {
		t.Fatal("the cached status was not used")
	}
	if res, err := call(n, local, model.MethodNodeGPUStatus, gpuStatusReq{Fresh: true}); err != nil || len(res.(gpu.Status).Devices) != 1 {
		t.Fatalf("fresh status = %v %v", res, err)
	}

	for _, req := range []model.GPUSetReq{
		{Vendor: "amd", Index: -1, Key: "power_limit", Value: "100"}, // no AMD GPU
		{Vendor: "nvidia", Index: 0, Key: "nope"},
		{Vendor: "nvidia", Index: -1, Key: "power_limit", Value: "lots"},
	} {
		if _, err := call(n, local, model.MethodNodeGPUSet, req); code(err) != model.CodeBadRequest {
			t.Fatalf("%+v: %v", req, err)
		}
	}
	if _, err := call(n, local, model.MethodNodeGPUInstall, model.GPUInstallReq{Vendor: "nope"}); code(err) != model.CodeBadRequest {
		t.Fatalf("install of an unknown vendor: %v", err)
	}

	run := func(req model.GPUSetReq) (model.Job, string, model.Job) {
		t.Helper()
		res, err := call(n, local, model.MethodNodeGPUSet, req)
		if err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
		j := res.(model.Job)
		out, end, err := follow(t, n, local, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		return j, out, end
	}
	j, out, end := run(model.GPUSetReq{Vendor: "nvidia", Index: -1, Key: "power_limit", Value: "150"})
	if end.Status != model.JobDone || !strings.Contains(out, "set -i 0 -pl 150") || j.Title != "nvidia power_limit=150 · all GPUs" {
		t.Fatalf("power limit: %q %q %+v", j.Title, out, end)
	}
	j, _, end = run(model.GPUSetReq{Vendor: "nvidia", Index: 0, Key: "gpu_clock_reset"})
	if end.Status != model.JobDone || j.Title != "nvidia gpu_clock_reset · GPU 0" {
		t.Fatalf("clock reset: %q %+v", j.Title, end)
	}
	_, out, end = run(model.GPUSetReq{Vendor: "nvidia", Index: 0, Key: "power_limit", Value: "999"})
	if end.Status != model.JobFailed || end.ExitCode == nil || *end.ExitCode != 4 || !strings.Contains(out, "refused") {
		t.Fatalf("failed setting: %q %+v", out, end)
	}
	if env := n.gpuEnv(); len(env) != 1 || !strings.HasPrefix(env[0], gpu.EnvDataDir+"=") {
		t.Fatalf("gpu env = %v", env)
	}
}

func TestCollectNvidia(t *testing.T) {
	fakeTools(t, map[string]string{"nvidia-smi": fakeNvidiaSMI})
	gs := collectNvidiaGPUs()
	if len(gs) != 1 || gs[0].Name != "NVIDIA A10" || gs[0].MemoryMB != 24564 || gs[0].PowerLimit != 150 || gs[0].Driver != "550.1" {
		t.Fatalf("gpus = %+v", gs)
	}
	if h := CollectHW(); len(h.GPUs) == 0 || h.GPUs[0].Vendor != "nvidia" {
		t.Fatalf("hw gpus = %+v", h.GPUs)
	}
	// A tool that fails, and one that is missing, give nothing.
	fakeTools(t, map[string]string{"nvidia-smi": "exit 1"})
	if gs := collectNvidiaGPUs(); gs != nil {
		t.Fatalf("failing tool = %+v", gs)
	}
	if out := runCmd(time.Second, "no-such-tool"); out != "" {
		t.Fatalf("missing tool = %q", out)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	if gs := parseNvidiaSMI(`"unterminated`); gs != nil {
		t.Fatalf("bad csv = %+v", gs)
	}
	gs := parseNvidiaSMI("short, row\n1, T4, 15360, 0, 0, 30, 535.0\n")
	if len(gs) != 1 || gs[0].Index != 1 || gs[0].PowerDraw != 0 || gs[0].Temperature != 30 {
		t.Fatalf("gpus = %+v", gs)
	}
}

func TestCommandSudoEnv(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as root: no sudo")
	}
	c := Command{Argv: []string{"x"}, Env: []string{"A=1", "B=2"}, Root: true}.Sudo(true)
	if want := []string{"sudo", "-n", "--preserve-env=A,B", "x"}; !slices.Equal(c.Argv, want) {
		t.Fatalf("sudo = %q", c.Argv)
	}
	if c := (Command{Argv: []string{"x"}, Root: true}).Sudo(true); !slices.Equal(c.Argv, []string{"sudo", "-n", "x"}) {
		t.Fatalf("sudo without env = %q", c.Argv)
	}
}

func TestBuildOllamaInstall(t *testing.T) {
	c, err := buildOllamaInstall(nil)
	switch {
	case err != nil:
		if !strings.Contains(err.Error(), "homebrew") {
			t.Fatalf("error = %v", err)
		}
	case c.Argv[0] == "sh":
		if !c.Root || c.Argv[2] != scriptOllamaInstall {
			t.Fatalf("linux install = %+v", c)
		}
	case !slices.Equal(c.Argv[1:], []string{"install", "ollama"}) || c.Root:
		t.Fatalf("brew install = %+v", c)
	}
	// Without brew on PATH, the usual places are looked at.
	fakeTools(t, nil)
	if c, err := buildOllamaInstall(nil); err == nil && len(c.Argv) == 0 {
		t.Fatal("no command and no error")
	}
}

func TestJobsEdges(t *testing.T) {
	// A pool that stopped refuses the job, which is forgotten.
	p := pool.New(1, 1)
	p.Stop(context.Background())
	js := NewJobs(p)
	if _, err := js.Start(JobKindAction, "x", time.Minute, func(context.Context, io.Writer) (int, error) { return 0, nil }); err == nil {
		t.Fatal("a job started on a stopped pool")
	}
	if len(js.List()) != 0 {
		t.Fatal("a refused job is listed")
	}

	keep := followKeepalive
	t.Cleanup(func() { followKeepalive = keep })
	followKeepalive = 10 * time.Millisecond
	js = NewJobs(pool.New(maxRunning, maxRunning))
	release := make(chan struct{})
	j := mustStart(t, js, JobKindAction, "quiet", time.Minute, func(ctx context.Context, _ io.Writer) (int, error) {
		<-release
		return 0, nil
	})
	// A quiet job sends its state again after followKeepalive; a send that
	// fails ends the follow.
	frames := 0
	boom := errors.New("boom")
	_, err := js.Follow(context.Background(), j.ID, 0, func(model.Job) error {
		if frames++; frames == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("follow = %v", err)
	}
	// ctx ending in the batch wait.
	w := mustStart(t, js, JobKindAction, "chatty", time.Minute, func(ctx context.Context, out io.Writer) (int, error) {
		for {
			select {
			case <-ctx.Done():
				return -1, ctx.Err()
			case <-time.After(time.Millisecond):
				_, _ = out.Write([]byte("x"))
			}
		}
	})
	batch := followBatch
	t.Cleanup(func() { followBatch = batch })
	followBatch = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	sent := 0
	_, err = js.Follow(ctx, w.ID, 0, func(model.Job) error {
		if sent++; sent == 1 {
			go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("follow with ctx ended in the batch wait = %v", err)
	}
	js.Cancel(w.ID)
	close(release)
	// A job pruned while followed is not found.
	js.mu.Lock()
	r := &job{Job: model.Job{ID: 1000, Status: model.JobRunning}, out: &outBuf{}}
	js.list[r.ID] = r
	js.mu.Unlock()
	go func() {
		time.Sleep(20 * time.Millisecond)
		js.mu.Lock()
		delete(js.list, r.ID)
		js.mu.Unlock()
		r.out.Notify()
	}()
	followBatch = time.Millisecond
	if _, err := js.Follow(context.Background(), r.ID, 0, func(model.Job) error { return nil }); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("follow of a pruned job = %v", err)
	}
}

func TestGPUInstall(t *testing.T) {
	vendor := ""
	for _, v := range gpu.Vendors {
		if _, err := gpu.InstallScript(v.ID); err == nil {
			vendor = v.ID
			break
		}
	}
	if vendor == "" {
		t.Skip("no install method on this OS")
	}
	// sh stands in for the install script's shell: nothing is installed.
	fakeTools(t, map[string]string{"sh": `echo "would install"`})
	n := startNode(t, &config.LLMNode{Settings: noSudo()}, nil)
	if env := n.gpuEnv(); env != nil {
		t.Fatalf("gpu env without a data dir = %v", env)
	}
	res, err := call(n, local, model.MethodNodeGPUInstall, model.GPUInstallReq{Vendor: vendor})
	if err != nil {
		t.Fatal(err)
	}
	out, end, err := follow(t, n, local, res.(model.Job).ID)
	if err != nil || end.Status != model.JobDone || !strings.Contains(out, "would install") {
		t.Fatalf("install = %q %+v %v", out, end, err)
	}
}

func TestHardwareRefresh(t *testing.T) {
	calls := t.TempDir() + "/calls"
	fakeTools(t, map[string]string{"nvidia-smi": `echo x >> '` + calls + `'`})
	s := noSudo()
	s.RefreshInterval = config.Duration(10 * time.Millisecond)
	startNode(t, &config.LLMNode{Settings: s}, nil)
	waitFor(t, "hardware read again", func() bool {
		b, _ := os.ReadFile(calls)
		return strings.Count(string(b), "x") >= 3
	})
}
