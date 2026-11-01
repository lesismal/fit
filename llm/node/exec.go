package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lesismal/fit/llm/internal/model"
)

// Command is a fixed command built in code: Argv runs directly (no shell). The only shell form is
// {"sh", "-c", <constant script>}, where the script text is a constant of this program and parameters reach it
// through Env, never through the script text.
//
// Command 由代码生成的固定命令：Argv 直接执行（不经 shell）。唯一的 shell 形式是 {"sh", "-c", <常量脚本>}，
// 脚本文本是本程序中的常量，参数只经 Env 传入，从不拼进脚本文本。
type Command struct {
	Argv []string
	Env  []string // extra KEY=VALUE pairs / 额外的 KEY=VALUE
	Dir  string   // working directory ("" = current) / 工作目录（"" 为当前目录）
	// Root marks commands that need root; Sudo adds "sudo -n" for them.
	// Root 标记需要 root 的命令；Sudo 为其加 "sudo -n"。
	Root bool
}

// Sudo returns c prefixed with "sudo -n" (keeping c.Env) when c needs root, enabled is set and the node does not
// run as root.
// Sudo 在 c 需要 root、enabled 为真且节点不以 root 运行时，返回加 "sudo -n" 前缀（保留 c.Env）的 c。
func (c Command) Sudo(enabled bool) Command {
	if !c.Root || !enabled || runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return c
	}
	argv := []string{"sudo", "-n"}
	if len(c.Env) > 0 {
		keys := make([]string, 0, len(c.Env))
		for _, kv := range c.Env {
			k, _, _ := strings.Cut(kv, "=")
			keys = append(keys, k)
		}
		argv = append(argv, "--preserve-env="+strings.Join(keys, ","))
	}
	c.Argv = append(argv, c.Argv...)
	return c
}

// String renders the command for logs and job output (a script is shown by its first line).
// String 渲染命令，用于日志与作业输出（脚本只显示第一行）。
func (c Command) String() string {
	parts := make([]string, 0, len(c.Argv))
	for i, a := range c.Argv {
		if i > 0 && c.Argv[i-1] == "-c" && strings.Contains(a, "\n") {
			first, _, _ := strings.Cut(strings.TrimSpace(a), "\n")
			parts = append(parts, "'"+first+" ...'")
			continue
		}
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`;&|<>(){}*?") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Run runs the command in its own process group, writing stdout and stderr to out. Cancelling ctx kills the whole
// group. It returns the exit code (-1 if the process did not exit normally).
// Run 在独立进程组中执行命令，stdout 与 stderr 写入 out。取消 ctx 会杀掉整个进程组。返回退出码（进程未正常退出时为 -1）。
func (c Command) Run(ctx context.Context, out io.Writer) (int, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return -1, errors.New("empty command")
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Env = append(os.Environ(), c.Env...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Dir = c.Dir
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcGroup(cmd)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), fmt.Errorf("exit status %d", ee.ExitCode())
		}
		return -1, err
	}
	return 0, nil
}

// outBuf collects the output of a job, keeping the last model.MaxJobOutput bytes. Offsets are absolute (bytes
// written since the start), so a follower's offset stays valid when the oldest part is dropped. It is safe for
// concurrent writes and reads; followers wait on Changed for new output.
//
// outBuf 收集作业输出，保留最后 model.MaxJobOutput 字节。偏移是绝对的（自开始以来写入的字节数），丢弃最早部分后
// 跟随者的偏移仍然有效。可并发读写；跟随者通过 Changed 等待新输出。
type outBuf struct {
	mu      sync.Mutex
	b       []byte
	base    int           // bytes dropped from the front / 从头部丢弃的字节数
	limit   int           // 0 = model.MaxJobOutput
	changed chan struct{} // closed at the next write or Notify / 下次写入或 Notify 时关闭
}

// Changed returns a channel that is closed at the next write (or Notify). Take it before reading the output, so
// that nothing written in between is missed.
// Changed 返回在下次写入（或 Notify）时关闭的通道。须在读取输出之前获取，以免错过两者之间写入的内容。
func (o *outBuf) Changed() <-chan struct{} {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.changed == nil {
		o.changed = make(chan struct{})
	}
	return o.changed
}

// Notify wakes the followers without writing (the job's state changed).
// Notify 不写入而唤醒跟随者（作业状态变化）。
func (o *outBuf) Notify() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.notifyLocked()
}

func (o *outBuf) notifyLocked() {
	if o.changed != nil {
		close(o.changed)
		o.changed = nil
	}
}

func (o *outBuf) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	limit := o.limit
	if limit <= 0 {
		limit = model.MaxJobOutput
	}
	o.b = append(o.b, p...)
	if over := len(o.b) - limit; over > 0 {
		// Drop a quarter more than needed, so a chatty job does not copy the buffer on every write.
		// 多丢弃四分之一，避免输出频繁的作业每次写入都复制缓冲区。
		drop := min(over+limit/4, len(o.b))
		o.b = append(o.b[:0], o.b[drop:]...)
		o.base += drop
	}
	o.notifyLocked()
	return len(p), nil
}

// From returns the output from the absolute offset (moved forward to the oldest kept byte, and back to a rune
// boundary), the offset actually used and the total length written.
// From 返回从绝对偏移 offset 开始的输出（不早于保留的最早字节，并回退到字符边界）、实际使用的偏移与已写入的总长度。
func (o *outBuf) From(offset int) (string, int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	end := o.base + len(o.b)
	if offset < o.base || offset > end {
		offset = o.base
	}
	i := offset - o.base
	for i > 0 && i < len(o.b) && !utf8.RuneStart(o.b[i]) {
		i--
	}
	// The first kept byte may be in the middle of a rune: skip it.
	// 保留的第一个字节可能位于字符中间：跳过。
	if i == 0 && o.base > 0 {
		for i < len(o.b) && !utf8.RuneStart(o.b[i]) {
			i++
		}
	}
	return string(o.b[i:]), o.base + i, end
}

// Len returns the total length written.
// Len 返回已写入的总长度。
func (o *outBuf) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.base + len(o.b)
}
