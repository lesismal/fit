package node

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/pool"
)

func TestBuildActionValidation(t *testing.T) {
	c, title, err := BuildAction("ollama_pull", map[string]string{"model": "hf.co/bartowski/Qwen3-8B-GGUF:Q4_K_M"})
	if err != nil || !slices.Equal(c.Argv, []string{"ollama", "pull", "hf.co/bartowski/Qwen3-8B-GGUF:Q4_K_M"}) || !strings.Contains(title, "Qwen3") {
		t.Fatalf("pull = %+v %q %v", c, title, err)
	}
	for _, v := range []string{
		"x; rm -rf /", "$(id)", "`id`", "-flag", "--insecure", "x\nid", "x\n", "x y", "x|id", "x&&id", "x>y", "", ".hidden",
		"a\x00b", "qwen x", strings.Repeat("a", 200),
	} {
		for _, id := range []string{"ollama_pull", "ollama_rm", "ollama_show"} {
			if _, _, err := BuildAction(id, map[string]string{"model": v}); err == nil {
				t.Errorf("%s model=%q accepted", id, v)
			}
		}
	}
	if _, _, err := BuildAction("ollama_pull", nil); err == nil {
		t.Error("missing parameter accepted")
	}
	if _, _, err := BuildAction("ollama_list", map[string]string{"script": "id"}); err == nil {
		t.Error("unknown parameter accepted")
	}
	if _, _, err := BuildAction("run_script", nil); err == nil {
		t.Error("unknown action accepted")
	}
	if runtime.GOOS == "linux" {
		for _, v := range []string{"0", "10000", "1e3", "-5", "5;id"} {
			if _, _, err := BuildAction("ollama_logs", map[string]string{"lines": v}); err == nil {
				t.Errorf("lines=%q accepted", v)
			}
		}
	} else if _, _, err := BuildAction("ollama_status", nil); err == nil {
		t.Error("linux-only action accepted on " + runtime.GOOS)
	}
}

func TestActionCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range actionDefs {
		if d.ID == "" || seen[d.ID] || d.Title == "" || d.TitleZH == "" || d.build == nil {
			t.Errorf("bad action %q", d.ID)
		}
		seen[d.ID] = true
		params := map[string]string{}
		for _, p := range d.Params {
			if p.Label == "" || p.LabelZH == "" || p.Example == "" || checkParam(p, p.Example) != nil {
				t.Errorf("%s: bad param %+v", d.ID, p)
			}
			params[p.Key] = p.Example
		}
		if !supported(d.OS) || d.ID == "ollama_install" {
			continue
		}
		c, _, err := BuildAction(d.ID, params)
		if err != nil || len(c.Argv) == 0 {
			t.Errorf("%s: %+v %v", d.ID, c, err)
		}
		// A shell is only ever used with a constant script and no parameter.
		// shell 只用于不带参数的常量脚本。
		if c.Argv[0] == "sh" && (len(d.Params) > 0 || !slices.Contains([]string{scriptOllamaInstall, scriptNvidiaContainerToolkit}, c.Argv[2])) {
			t.Errorf("%s: shell with a non-constant script", d.ID)
		}
	}
	if len(Actions()) == 0 {
		t.Fatal("no actions")
	}
}

func TestCommandSudoAndString(t *testing.T) {
	c := Command{Argv: []string{"sh", "-c", "set -e\necho hi\n"}, Env: []string{"A=1"}, Root: true}
	if s := c.String(); s != "sh -c 'set -e ...'" {
		t.Fatalf("string = %q", s)
	}
	if got := (Command{Argv: []string{"ollama", "pull", "a b"}}).String(); got != "ollama pull 'a b'" {
		t.Fatalf("string = %q", got)
	}
	if got := c.Sudo(false); !slices.Equal(got.Argv, c.Argv) {
		t.Fatalf("sudo disabled = %q", got.Argv)
	}
	if got := (Command{Argv: []string{"x"}}).Sudo(true); !slices.Equal(got.Argv, []string{"x"}) {
		t.Fatalf("sudo for non-root command = %q", got.Argv)
	}
}

func TestOutBuf(t *testing.T) {
	o := &outBuf{limit: 100}
	ch := o.Changed()
	o.Write([]byte(strings.Repeat("a", 60)))
	select {
	case <-ch:
	default:
		t.Fatal("Changed not closed by Write")
	}
	o.Write([]byte(strings.Repeat("b", 60)))
	s, off, total := o.From(0)
	if total != 120 || len(s) > 100 || off+len(s) != 120 || !strings.HasSuffix(s, strings.Repeat("b", 60)) {
		t.Fatalf("from 0: len %d off %d total %d", len(s), off, total)
	}
	if s, off, _ = o.From(110); s != strings.Repeat("b", 10) || off != 110 {
		t.Fatalf("from 110 = %q %d", s, off)
	}
	if s, off, _ = o.From(500); off+len(s) != 120 || off == 500 {
		t.Fatalf("from beyond end = %d", off)
	}
	for range 1000 {
		o.Write([]byte("0123456789"))
	}
	if s, _, total = o.From(0); len(s) > 100 || total != 10120 || o.Len() != 10120 {
		t.Fatalf("bounds: len %d total %d", len(s), total)
	}

	// Never start in the middle of a rune.
	// 绝不从字符中间开始。
	u := &outBuf{limit: 10}
	u.Write([]byte("中文中文中文"))
	if s, _, _ := u.From(0); !strings.HasPrefix(s, "中") && !strings.HasPrefix(s, "文") {
		t.Fatalf("rune boundary: %q", s)
	}
	if s, _, _ := u.From(u.Len() - 1); s != "文" {
		t.Fatalf("rune boundary at end: %q", s)
	}
}

func shellCommand(script string) Command {
	if runtime.GOOS == "windows" {
		return Command{Argv: []string{"cmd", "/c", script}}
	}
	return Command{Argv: []string{"sh", "-c", script}}
}

func mustStart(t *testing.T, js *Jobs, kind, title string, timeout time.Duration, fn func(context.Context, io.Writer) (int, error)) model.Job {
	t.Helper()
	j, err := js.Start(kind, title, timeout, fn)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// At most maxRunning jobs run at once; one more is refused.
func TestJobsBound(t *testing.T) {
	js := NewJobs(pool.New(maxRunning, maxRunning))
	block := make(chan struct{})
	for range maxRunning {
		mustStart(t, js, JobKindAction, "block", time.Minute, func(ctx context.Context, _ io.Writer) (int, error) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return 0, nil
		})
	}
	if _, err := js.Start(JobKindAction, "one more", time.Minute, func(context.Context, io.Writer) (int, error) { return 0, nil }); err == nil {
		t.Fatal("a job past the bound was started")
	}
	close(block)
}

func TestJobsLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	followBatch = 5 * time.Millisecond
	js := NewJobs(pool.New(maxRunning, maxRunning))
	var changes int
	changed := make(chan struct{}, 16)
	js.OnChange = func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}

	j := mustStart(t, js, JobKindAction, "echo", time.Minute, RunCommand(shellCommand("echo one; sleep 0.1; echo two; exit 3")))
	if j.Status != model.JobRunning || j.ID != 1 || js.Running() != 1 {
		t.Fatalf("started = %+v", j)
	}
	var got strings.Builder
	final, err := js.Follow(context.Background(), j.ID, 0, func(f model.Job) error { got.WriteString(f.Output); return nil })
	if err != nil || final.Status != model.JobFailed || final.ExitCode == nil || *final.ExitCode != 3 {
		t.Fatalf("final = %+v, err = %v", final, err)
	}
	if !strings.Contains(got.String(), "one\n") || !strings.Contains(got.String(), "two\n") || !strings.HasPrefix(got.String(), "$ sh -c") {
		t.Fatalf("followed output = %q", got.String())
	}
	if g, ok := js.Get(j.ID, 0); !ok || g.Output != got.String() || g.OutputLen != len(got.String()) {
		t.Fatalf("get = %+v", g)
	}
	ok := mustStart(t, js, JobKindAction, "ok", time.Minute, RunCommand(shellCommand("true")))
	if f, _ := js.Follow(context.Background(), ok.ID, 0, func(model.Job) error { return nil }); f.Status != model.JobDone || *f.ExitCode != 0 {
		t.Fatalf("ok job = %+v", f)
	}

	// Cancel kills the whole process group (the sleep child too).
	// Cancel 杀掉整个进程组（包括 sleep 子进程）。
	long := mustStart(t, js, JobKindAction, "sleep", time.Minute, RunCommand(shellCommand("sleep 30 & wait")))
	time.Sleep(50 * time.Millisecond)
	if !js.Cancel(long.ID) {
		t.Fatal("cancel failed")
	}
	start := time.Now()
	f, err := js.Follow(context.Background(), long.ID, 0, func(model.Job) error { return nil })
	if err != nil || f.Status != model.JobCanceled || time.Since(start) > 5*time.Second {
		t.Fatalf("canceled job = %+v, err = %v", f, err)
	}
	if js.Cancel(long.ID) {
		t.Fatal("cancel of an ended job succeeded")
	}

	to := mustStart(t, js, JobKindAction, "timeout", 50*time.Millisecond, RunCommand(shellCommand("sleep 30")))
	if f, _ := js.Follow(context.Background(), to.ID, 0, func(model.Job) error { return nil }); f.Status != model.JobFailed || f.Error != "timeout" {
		t.Fatalf("timeout job = %+v", f)
	}

	if l := js.List(); len(l) != 4 || l[0].ID != 4 || l[0].Output != "" || js.Running() != 0 {
		t.Fatalf("list = %+v", l)
	}
	if _, err := js.Follow(context.Background(), 99, 0, func(model.Job) error { return nil }); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("follow unknown = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	blk := mustStart(t, js, JobKindAction, "block", time.Minute, func(ctx context.Context, _ io.Writer) (int, error) { <-ctx.Done(); return -1, ctx.Err() })
	cancel()
	if _, err := js.Follow(ctx, blk.ID, 0, func(model.Job) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("follow with canceled ctx = %v", err)
	}
	js.Cancel(blk.ID)
	for len(changed) > 0 {
		<-changed
		changes++
	}
	if changes == 0 {
		t.Fatal("OnChange never called")
	}
}

func TestJobsPrune(t *testing.T) {
	js := NewJobs(pool.New(maxRunning, maxRunning))
	noop := func(context.Context, io.Writer) (int, error) { return 0, nil }
	for range maxJobs + 20 {
		j := mustStart(t, js, JobKindAction, "noop", time.Minute, noop)
		js.Follow(context.Background(), j.ID, 0, func(model.Job) error { return nil })
	}
	if n := len(js.List()); n > maxJobs {
		t.Fatalf("kept %d jobs", n)
	}
	if _, ok := js.Get(1, 0); ok {
		t.Fatal("oldest job not pruned")
	}
}

func TestCollectHW(t *testing.T) {
	h := CollectHW()
	if h.OS != runtime.GOOS || h.CPUCores <= 0 || h.GPUs == nil || h.Disks == nil || h.IPs == nil {
		t.Fatalf("hw = %+v", h)
	}
}
