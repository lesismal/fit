package node

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/lesismal/fit/agent/internal/gpu"
	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/pool"
)

// gpuCacheTTL is how long a gpu.Collect result is reused.
// gpuCacheTTL gpu.Collect 结果的复用时长。
const gpuCacheTTL = 5 * time.Second

type gpuCache struct {
	mu  sync.Mutex
	st  gpu.Status
	at  time.Time
	ok  bool
	use bool // UseSudo it was collected with / 采集时的 UseSudo
}

func (c *gpuCache) get(useSudo bool) (gpu.Status, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st, c.ok && c.use == useSudo && time.Since(c.at) < gpuCacheTTL
}

func (c *gpuCache) set(st gpu.Status, useSudo bool) {
	c.mu.Lock()
	c.st, c.at, c.ok, c.use = st, time.Now(), true, useSudo
	c.mu.Unlock()
}

// invalidate drops the cached status (a GPU job started or ended).
// invalidate 丢弃缓存的状态（显卡作业开始或结束）。
func (c *gpuCache) invalidate() {
	c.mu.Lock()
	c.ok = false
	c.mu.Unlock()
}

func (n *Node) gpuOpts() gpu.Options { return gpu.Options{UseSudo: n.settings().GPUSudo} }

// startJob starts a job with the node's job timeout.
func (n *Node) startJob(kind, title string, fn func(ctx context.Context, out io.Writer) (int, error)) (any, error) {
	j, err := n.jobs.Start(kind, title, n.jobTimeout(), fn)
	if err != nil {
		return nil, mesh.Errorf(model.CodeRateLimited, "%v", err)
	}
	return j, nil
}

// gpuStatus returns the GPU status, collected at most every gpuCacheTTL (fresh: now).
// gpuStatus 返回显卡状态，最多每 gpuCacheTTL 采集一次（fresh：立即采集）。
func (n *Node) gpuStatus(ctx context.Context, fresh bool) gpu.Status {
	opts := n.gpuOpts()
	if st, ok := n.gpu.get(opts.UseSudo); ok && !fresh {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Collecting runs the vendors' tools: on the node's pool, one at a time.
	st, _ := pool.Do(ctx, n.work, func(ctx context.Context) (gpu.Status, error) {
		n.gpuMu.Lock()
		defer n.gpuMu.Unlock()
		if st, ok := n.gpu.get(opts.UseSudo); ok && !fresh {
			return st, nil
		}
		st := gpu.Collect(ctx, opts)
		n.gpu.set(st, opts.UseSudo)
		return st, nil
	})
	return st
}

func gpuCatalog() []gpu.Vendor { return gpu.Vendors }

func (n *Node) gpuEnv() []string {
	if dir := n.settings().GPUDataDir; dir != "" {
		return []string{gpu.EnvDataDir + "=" + dir}
	}
	return nil
}

func (n *Node) gpuInstall(_ context.Context, _ model.Identity, req *model.GPUInstallReq) (any, error) {
	title, argv, err := gpu.InstallCommand(req.Vendor)
	if err != nil {
		return nil, mesh.BadRequest("%v", err)
	}
	c := Command{Argv: n.gpuOpts().Wrap(argv), Env: n.gpuEnv()}
	return n.startJob(JobKindGPUInstall, title, RunCommand(c))
}

// gpuSet applies one setting as a job; Index < 0 runs it on every GPU of the vendor, one after another.
// gpuSet 以作业应用一项设置；Index < 0 时依次应用到该厂商的每块 GPU。
func (n *Node) gpuSet(ctx context.Context, _ model.Identity, req *model.GPUSetReq) (any, error) {
	indices := []int{req.Index}
	target := "GPU " + strconv.Itoa(req.Index)
	if req.Index < 0 {
		indices = gpu.DeviceIndices(n.gpuStatus(ctx, false), req.Vendor)
		if len(indices) == 0 {
			return nil, mesh.BadRequest("no %s GPU found", req.Vendor)
		}
		target = "all GPUs"
	}
	opts := n.gpuOpts()
	var cmds []Command
	for _, i := range indices {
		argv, err := gpu.Command(req.Vendor, i, req.Key, req.Value)
		if err != nil {
			return nil, mesh.BadRequest("%v", err)
		}
		cmds = append(cmds, Command{Argv: opts.Wrap(argv), Env: n.gpuEnv()})
	}
	title := fmt.Sprintf("%s %s", req.Vendor, req.Key)
	if req.Value != "" {
		title += "=" + req.Value
	}
	title += " · " + target
	return n.startJob(JobKindGPUSet, title, func(ctx context.Context, out io.Writer) (int, error) {
		for _, c := range cmds {
			if code, err := RunCommand(c)(ctx, out); err != nil {
				return code, err
			}
		}
		return 0, nil
	})
}
