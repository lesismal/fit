package node

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/lesismal/fit/llm/internal/model"
)

// A node never runs a script supplied by a caller. Every management operation is one of the actions below: its
// command is built here, parameters are checked against an anchored pattern (and for control characters) and are
// only ever passed as separate argv elements, never through a shell. The few operations that need a shell (vendor
// install scripts) use a script constant of this file with no parameter at all.
//
// 节点从不执行调用方提供的脚本。所有管理操作都是下面的固定操作之一：命令在此生成，参数按整体匹配的正则（以及控制字符）
// 校验，且只作为独立的 argv 元素传递，从不经过 shell。少数需要 shell 的操作（厂商安装脚本）使用本文件中的常量脚本，
// 且不带任何参数。

// Parameter patterns.
// 参数模式。
const (
	// PatternOllamaModel is an Ollama model reference: name[:tag], optionally with a registry / namespace path. The
	// first character is alphanumeric, so a value can never be taken for an option.
	// PatternOllamaModel Ollama 模型引用：name[:tag]，可带仓库 / 命名空间路径。首字符为字母或数字，因此值不会被当作选项。
	PatternOllamaModel = `^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`
	// PatternLines is a line count, 1-9999.
	// PatternLines 行数，1-9999。
	PatternLines = `^[1-9][0-9]{0,3}$`
)

// maxParamLen bounds every parameter value regardless of its pattern.
// maxParamLen 无论模式如何，限制每个参数值的长度。
const maxParamLen = 256

// scriptOllamaInstall is Ollama's official Linux install (it sets up the ollama systemd service).
// scriptOllamaInstall Ollama 官方的 Linux 安装方式（会配置 ollama systemd 服务）。
const scriptOllamaInstall = `set -e
command -v curl >/dev/null 2>&1 || { echo "curl is required"; exit 1; }
curl -fsSL https://ollama.com/install.sh | sh
ollama --version || true
`

// scriptNvidiaContainerToolkit installs the NVIDIA Container Toolkit from NVIDIA's repository and, when Docker is
// present, configures and restarts it.
// scriptNvidiaContainerToolkit 从 NVIDIA 仓库安装 NVIDIA Container Toolkit；存在 Docker 时配置并重启 Docker。
const scriptNvidiaContainerToolkit = `set -e
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y curl gpg
  curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --yes --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg
  curl -fsSL https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \
    | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \
    > /etc/apt/sources.list.d/nvidia-container-toolkit.list
  apt-get update
  apt-get install -y nvidia-container-toolkit
elif command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1; then
  PM=dnf; command -v dnf >/dev/null 2>&1 || PM=yum
  curl -fsSL https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo \
    > /etc/yum.repos.d/nvidia-container-toolkit.repo
  $PM install -y nvidia-container-toolkit
else
  echo "Unsupported distribution: apt-get, dnf or yum is required"; exit 1
fi
if command -v docker >/dev/null 2>&1; then
  nvidia-ctk runtime configure --runtime=docker
  systemctl restart docker || true
fi
nvidia-ctk --version
`

type actionDef struct {
	model.Action
	build func(p map[string]string) (Command, error)
}

var (
	linux        = []string{"linux"}
	linuxDarwin  = []string{"linux", "darwin"}
	linuxWindows = []string{"linux", "windows"}
)

func argv(args ...string) func(map[string]string) (Command, error) {
	return func(map[string]string) (Command, error) { return Command{Argv: args}, nil }
}

func rootArgv(args ...string) func(map[string]string) (Command, error) {
	return func(map[string]string) (Command, error) { return Command{Argv: args, Root: true}, nil }
}

func rootScript(script string) func(map[string]string) (Command, error) {
	return func(map[string]string) (Command, error) {
		return Command{Argv: []string{"sh", "-c", script}, Root: true}, nil
	}
}

var modelParam = model.ActionParam{Key: "model", Label: "Model", LabelZH: "模型", Pattern: PatternOllamaModel, Example: "qwen3:8b"}

var actionDefs = []actionDef{
	{Action: model.Action{ID: "ollama_install", Title: "Install Ollama", TitleZH: "安装 Ollama", OS: linuxDarwin,
		Note:   "Linux: the official install script from ollama.com (sets up the ollama service). macOS: brew install ollama.",
		NoteZH: "Linux：使用 ollama.com 官方安装脚本（会配置 ollama 服务）。macOS：brew install ollama。"},
		build: buildOllamaInstall},
	{Action: model.Action{ID: "ollama_list", Title: "List Ollama models", TitleZH: "列出 Ollama 模型"},
		build: argv("ollama", "list")},
	{Action: model.Action{ID: "ollama_ps", Title: "Show loaded Ollama models", TitleZH: "查看已加载的 Ollama 模型"},
		build: argv("ollama", "ps")},
	{Action: model.Action{ID: "ollama_pull", Title: "Pull an Ollama model", TitleZH: "拉取 Ollama 模型", Params: []model.ActionParam{modelParam},
		Note: "Downloads the model from the Ollama library", NoteZH: "从 Ollama 模型库下载模型"},
		build: func(p map[string]string) (Command, error) {
			return Command{Argv: []string{"ollama", "pull", p["model"]}}, nil
		}},
	{Action: model.Action{ID: "ollama_show", Title: "Show an Ollama model", TitleZH: "查看 Ollama 模型", Params: []model.ActionParam{modelParam}},
		build: func(p map[string]string) (Command, error) {
			return Command{Argv: []string{"ollama", "show", p["model"]}}, nil
		}},
	{Action: model.Action{ID: "ollama_rm", Title: "Remove an Ollama model", TitleZH: "删除 Ollama 模型", Params: []model.ActionParam{modelParam}},
		build: func(p map[string]string) (Command, error) {
			return Command{Argv: []string{"ollama", "rm", p["model"]}}, nil
		}},
	{Action: model.Action{ID: "ollama_status", Title: "Ollama service status", TitleZH: "Ollama 服务状态", OS: linux},
		build: argv("systemctl", "status", "ollama", "--no-pager")},
	{Action: model.Action{ID: "ollama_restart", Title: "Restart the Ollama service", TitleZH: "重启 Ollama 服务", OS: linux},
		build: rootArgv("systemctl", "restart", "ollama")},
	{Action: model.Action{ID: "ollama_logs", Title: "Ollama service logs", TitleZH: "Ollama 服务日志", OS: linux,
		Params: []model.ActionParam{{Key: "lines", Label: "Lines", LabelZH: "行数", Pattern: PatternLines, Example: "200"}}},
		build: func(p map[string]string) (Command, error) {
			return Command{Argv: []string{"journalctl", "-u", "ollama", "-n", p["lines"], "--no-pager"}, Root: true}, nil
		}},
	{Action: model.Action{ID: "nvidia_smi", Title: "Show nvidia-smi", TitleZH: "查看 nvidia-smi", OS: linuxWindows},
		build: argv("nvidia-smi")},
	{Action: model.Action{ID: "nvidia_container_toolkit_install", Title: "Install NVIDIA Container Toolkit", TitleZH: "安装 NVIDIA Container Toolkit", OS: linux,
		Note:   "Adds NVIDIA's repository (apt / dnf / yum), installs the toolkit and configures Docker when present",
		NoteZH: "添加 NVIDIA 软件源（apt / dnf / yum），安装工具包；存在 Docker 时为其配置并重启"},
		build: rootScript(scriptNvidiaContainerToolkit)},
}

var paramPatterns = map[string]*regexp.Regexp{}

func init() {
	for _, d := range actionDefs {
		for _, p := range d.Params {
			if !strings.HasPrefix(p.Pattern, "^") || !strings.HasSuffix(p.Pattern, "$") {
				panic("node: unanchored action pattern " + p.Pattern)
			}
			if paramPatterns[p.Pattern] == nil {
				paramPatterns[p.Pattern] = regexp.MustCompile(p.Pattern)
			}
		}
	}
}

// Actions returns the fixed actions supported on this OS.
// Actions 返回本操作系统支持的固定操作。
func Actions() []model.Action {
	out := []model.Action{}
	for _, d := range actionDefs {
		if supported(d.OS) {
			out = append(out, d.Action)
		}
	}
	return out
}

func supported(oses []string) bool { return len(oses) == 0 || slices.Contains(oses, runtime.GOOS) }

// BuildAction validates params against action id and returns its command and a job title. Every declared
// parameter is required, unknown ones are rejected, and values must match the parameter's anchored pattern and
// contain no control or space characters. The caller applies Command.Sudo and runs it as a job.
// BuildAction 按操作 id 校验 params，返回其命令与作业标题。声明的参数都是必填，未知参数被拒绝，值必须整体匹配参数的
// 模式，且不含控制字符或空白。调用方套用 Command.Sudo 后以作业运行。
func BuildAction(id string, params map[string]string) (Command, string, error) {
	i := slices.IndexFunc(actionDefs, func(d actionDef) bool { return d.ID == id })
	if i < 0 {
		return Command{}, "", fmt.Errorf("unknown action %q", id)
	}
	d := &actionDefs[i]
	if !supported(d.OS) {
		return Command{}, "", fmt.Errorf("action %s is not supported on %s", id, runtime.GOOS)
	}
	for k := range params {
		if !slices.ContainsFunc(d.Params, func(p model.ActionParam) bool { return p.Key == k }) {
			return Command{}, "", fmt.Errorf("action %s: unknown parameter %q", id, k)
		}
	}
	title := d.Title
	for _, p := range d.Params {
		v, ok := params[p.Key]
		if !ok || v == "" {
			return Command{}, "", fmt.Errorf("action %s: missing parameter %q", id, p.Key)
		}
		if err := checkParam(p, v); err != nil {
			return Command{}, "", fmt.Errorf("action %s: %w", id, err)
		}
		title += " " + v
	}
	c, err := d.build(params)
	if err != nil {
		return Command{}, "", err
	}
	return c, title, nil
}

func checkParam(p model.ActionParam, v string) error {
	if len(v) > maxParamLen || strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 ||
		!paramPatterns[p.Pattern].MatchString(v) {
		return fmt.Errorf("invalid %s %q: must match %s", p.Key, v, p.Pattern)
	}
	return nil
}

// buildOllamaInstall uses the official script on Linux and Homebrew on macOS.
// buildOllamaInstall 在 Linux 上使用官方脚本，在 macOS 上使用 Homebrew。
func buildOllamaInstall(map[string]string) (Command, error) {
	if runtime.GOOS == "linux" {
		return rootScript(scriptOllamaInstall)(nil)
	}
	brew, err := exec.LookPath("brew")
	if err != nil {
		for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
			if _, err2 := exec.LookPath(p); err2 == nil {
				brew, err = p, nil
				break
			}
		}
	}
	if err != nil {
		return Command{}, errors.New("homebrew not found: install it from https://brew.sh, or download Ollama from https://ollama.com/download")
	}
	// Homebrew refuses to run as root, so this never gets sudo.
	// Homebrew 拒绝以 root 运行，因此不加 sudo。
	return Command{Argv: []string{brew, "install", "ollama"}}, nil
}
