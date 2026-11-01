package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/pool"
)

// Job kinds.
// 作业类型。
const (
	JobKindAction     = "action"      // a fixed action / 固定操作
	JobKindGPUInstall = "gpu-install" // a GPU tool install / 显卡管理软件安装
	JobKindGPUSet     = "gpu-set"     // a GPU setting / 显卡设置
)

// ErrJobNotFound is returned for an unknown (or pruned) job.
// ErrJobNotFound 未知（或已清理）的作业。
var ErrJobNotFound = errors.New("job not found")

// maxJobs is how many jobs a node keeps in memory (finished ones are pruned first, oldest first).
// maxJobs 节点在内存中保留的作业数（先清理已结束的，最早的优先）。
const maxJobs = 200

// maxRunning is how many jobs run at once; one more is refused.
const maxRunning = 8

type job struct {
	model.Job
	out    *outBuf
	cancel context.CancelFunc
}

// Jobs runs background tasks and keeps their output in memory.
// Jobs 执行后台任务，并在内存中保留其输出。
type Jobs struct {
	// pool runs the jobs, a bounded number at once.
	pool   *pool.Pool
	mu     sync.Mutex
	nextID int64
	list   map[int64]*job
	// OnChange (optional, set before the first Start) is called when a job starts or ends; it must not block.
	// OnChange（可选，须在第一次 Start 前设置）在作业开始或结束时调用；不得阻塞。
	OnChange func()
}

// NewJobs returns an empty job manager.
// NewJobs 返回空的作业管理器。
func NewJobs(p *pool.Pool) *Jobs { return &Jobs{pool: p, list: map[int64]*job{}} }

// Start runs fn in the background with timeout; fn writes its output to out and returns the exit code. The
// returned job has no output yet.
// Start 在后台以 timeout 执行 fn；fn 把输出写入 out 并返回退出码。返回的作业尚无输出。
func (js *Jobs) Start(kind, title string, timeout time.Duration, fn func(ctx context.Context, out io.Writer) (int, error)) (model.Job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	js.mu.Lock()
	if js.runningLocked() >= maxRunning {
		js.mu.Unlock()
		cancel()
		return model.Job{}, fmt.Errorf("%d jobs are running already; wait for one to end", maxRunning)
	}
	js.nextID++
	j := &job{Job: model.Job{ID: js.nextID, Kind: kind, Title: title, Status: model.JobRunning, CreatedAt: time.Now().Unix()},
		out: &outBuf{}, cancel: cancel}
	js.list[j.ID] = j
	js.pruneLocked()
	snap := j.Job
	js.mu.Unlock()

	err := js.pool.Go(func() {
		defer cancel()
		defer js.changed()
		code, err := fn(ctx, j.out)
		// Followers learn about the end once the state below is set.
		// 下面的状态设置完成后再通知跟随者作业已结束。
		defer j.out.Notify()
		js.mu.Lock()
		defer js.mu.Unlock()
		j.ExitCode = &code
		j.FinishedAt = time.Now().Unix()
		switch {
		case err == nil:
			j.Status = model.JobDone
		case errors.Is(ctx.Err(), context.Canceled):
			j.Status, j.Error = model.JobCanceled, "canceled"
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			j.Status, j.Error = model.JobFailed, "timeout"
		default:
			j.Status, j.Error = model.JobFailed, err.Error()
		}
	})
	if err != nil {
		cancel()
		js.mu.Lock()
		delete(js.list, snap.ID)
		js.mu.Unlock()
		return model.Job{}, err
	}
	js.changed()
	return snap, nil
}

func (js *Jobs) runningLocked() int {
	n := 0
	for _, j := range js.list {
		if j.Status == model.JobRunning {
			n++
		}
	}
	return n
}

func (js *Jobs) changed() {
	if js.OnChange != nil {
		js.OnChange()
	}
}

func (js *Jobs) pruneLocked() {
	if len(js.list) <= maxJobs {
		return
	}
	var done []*job
	for _, j := range js.list {
		if j.Status != model.JobRunning {
			done = append(done, j)
		}
	}
	sort.Slice(done, func(a, b int) bool { return done[a].ID < done[b].ID })
	for i := 0; i < len(done) && len(js.list) > maxJobs; i++ {
		delete(js.list, done[i].ID)
	}
}

// Get returns a job with its output from offset (absolute; see model.Job.Offset / OutputLen).
// Get 返回作业及其从 offset（绝对偏移；见 model.Job.Offset / OutputLen）开始的输出。
func (js *Jobs) Get(id int64, offset int) (model.Job, bool) {
	js.mu.Lock()
	j, ok := js.list[id]
	var snap model.Job
	if ok {
		snap = j.Job
	}
	js.mu.Unlock()
	if !ok {
		return snap, false
	}
	snap.Output, snap.Offset, snap.OutputLen = j.out.From(offset)
	return snap, true
}

// Following a job: frames are at least followBatch apart (so a chatty job is sent in batches), and a frame with
// the job's state is sent after followKeepalive without output (so the stream's idle timeout measures the
// connection, not a silent job).
// 跟随作业：帧之间至少间隔 followBatch（输出频繁的作业按批发送）；没有输出时每隔 followKeepalive 发送一帧作业状态
// （使流空闲超时衡量的是连接，而不是一个安静的作业）。
var (
	followBatch     = 100 * time.Millisecond
	followKeepalive = 30 * time.Second
)

// Follow sends the job's output from offset as it is written, each frame a model.Job with the new output, until
// the job has ended and everything was sent; it returns the final job (without output).
// Follow 从 offset 开始随写入发送作业输出，每帧是带新输出的 model.Job，直到作业结束且输出全部发出；
// 返回作业的最终状态（不含输出）。
func (js *Jobs) Follow(ctx context.Context, id int64, offset int, send func(model.Job) error) (model.Job, error) {
	js.mu.Lock()
	j, ok := js.list[id]
	js.mu.Unlock()
	if !ok {
		return model.Job{}, fmt.Errorf("job %d: %w", id, ErrJobNotFound)
	}
	keep := time.NewTimer(followKeepalive)
	defer keep.Stop()
	first := true
	lastStatus := ""
	for {
		changed := j.out.Changed()
		snap, ok := js.Get(id, offset)
		if !ok {
			return model.Job{}, fmt.Errorf("job %d: %w", id, ErrJobNotFound)
		}
		if first || snap.Output != "" || snap.Status != lastStatus {
			if err := send(snap); err != nil {
				return model.Job{}, err
			}
			first, lastStatus = false, snap.Status
			keep.Reset(followKeepalive)
		}
		offset = snap.OutputLen
		if snap.Status != model.JobRunning {
			snap.Output = ""
			return snap, nil
		}
		select {
		case <-changed:
		case <-keep.C:
			first = true // send the state even without output / 没有输出也发送状态
			continue
		case <-ctx.Done():
			return model.Job{}, ctx.Err()
		}
		select {
		case <-time.After(followBatch):
		case <-ctx.Done():
			return model.Job{}, ctx.Err()
		}
	}
}

// List returns every job, newest first, without output.
// List 返回所有作业（最新在前），不含输出。
func (js *Jobs) List() []model.Job {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := make([]model.Job, 0, len(js.list))
	for _, j := range js.list {
		s := j.Job
		s.OutputLen = j.out.Len()
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out
}

// Cancel cancels a running job (its process group is killed); false if the job is unknown or has ended.
// Cancel 取消运行中的作业（杀掉其进程组）；作业未知或已结束时返回 false。
func (js *Jobs) Cancel(id int64) bool {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.list[id]
	if !ok || j.Status != model.JobRunning {
		return false
	}
	j.cancel()
	return true
}

// Running returns the number of running jobs.
// Running 返回运行中的作业数。
func (js *Jobs) Running() int {
	js.mu.Lock()
	defer js.mu.Unlock()
	return js.runningLocked()
}

// RunCommand is a Start fn running c: it prints "$ <command>" and then the command's output.
// RunCommand 是运行 c 的 Start fn：先输出 "$ <命令>"，再输出命令的输出。
func RunCommand(c Command) func(ctx context.Context, out io.Writer) (int, error) {
	return func(ctx context.Context, out io.Writer) (int, error) {
		fmt.Fprintf(out, "$ %s\n", c)
		return c.Run(ctx, out)
	}
}
