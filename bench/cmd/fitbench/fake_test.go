package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests run fitbench against a fake docker: the test binary itself,
// copied as docker into a directory put first on PATH, which answers each
// command as Docker and the load generators would, from canned output. No
// container is started and no load is generated.

// fakeEnv names the fake's state directory: its rules and their counts. The
// test binary acts as docker when it is set.
const fakeEnv = "FITBENCH_FAKE_DOCKER"

// A fakeRule overrides the fake's answer to the commands whose arguments,
// joined by spaces, contain Match: it prints Out and fails when Fail is set.
// The first After matching commands are answered as usual, and, when Times
// is set, so are those after the Times the rule answers.
type fakeRule struct {
	Match string `json:"match"`
	Out   string `json:"out"`
	Fail  bool   `json:"fail"`
	After int    `json:"after"`
	Times int    `json:"times"`
}

// fakeBin is the directory holding the fake docker.
var fakeBin string

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeEnv); dir != "" {
		os.Exit(fakeDocker(dir, os.Args[1:]))
	}
	code := m.Run()
	if fakeBin != "" {
		os.RemoveAll(fakeBin)
	}
	os.Exit(code)
}

// installFake puts the fake docker first on PATH for t, answering by rules,
// and returns its state directory.
func installFake(t *testing.T, rules ...fakeRule) string {
	t.Helper()
	if fakeBin == "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := os.MkdirTemp("", "fitbench-fake")
		if err != nil {
			t.Fatal(err)
		}
		name := "docker"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
		fakeBin = dir
	}
	state := t.TempDir()
	data, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "rules.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "static"), []byte(smallHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeEnv, state)
	// Under the race detector the fake would sleep a second as it exits.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return state
}

// smallHTML is the static file of the fake bench directories.
const smallHTML = "<p>small</p>\n"

// fakeDocker answers one docker command.
func fakeDocker(state string, args []string) int {
	joined := strings.Join(args, " ")
	var rules []fakeRule
	if data, err := os.ReadFile(filepath.Join(state, "rules.json")); err == nil {
		_ = json.Unmarshal(data, &rules)
	}
	for i, r := range rules {
		if !strings.Contains(joined, r.Match) {
			continue
		}
		countFile := filepath.Join(state, "count-"+strconv.Itoa(i))
		data, _ := os.ReadFile(countFile)
		n, _ := strconv.Atoi(string(data))
		_ = os.WriteFile(countFile, []byte(strconv.Itoa(n+1)), 0o644)
		if n < r.After || (r.Times > 0 && n >= r.After+r.Times) {
			continue
		}
		fmt.Print(r.Out)
		if r.Fail {
			return 1
		}
		return 0
	}
	if len(args) == 0 {
		return 1
	}
	switch args[0] {
	case "info":
		fmt.Println("16 x86_64")
	case "version":
		fmt.Println("27.3.1")
	case "image":
		fmt.Println("image@sha256:0123")
	case "inspect":
		fmt.Println("0123abcd")
	case "logs":
		fmt.Println("the last logs")
	case "exec":
		return fakeExec(state, args[1:])
	}
	return 0
}

// fakeExec answers a docker exec in one of the containers.
func fakeExec(state string, args []string) int {
	if args[0] == "-i" {
		args = args[1:]
	}
	cmd := args[1:]
	switch cmd[0] {
	case "curl":
		i := slices.Index(cmd, "-w")
		switch {
		case i < 0: // the cache origin's stub_status
			fmt.Print("Active connections: 1\nserver accepts handled requests\n 1 1 100\nReading: 0 Writing: 1 Waiting: 0\n")
		case cmd[i+1] == "%{http_code}": // waitReady
			fmt.Print("200")
		default: // check
			u, _ := url.Parse(cmd[len(cmd)-1])
			body := ""
			for _, sc := range scenarios {
				if sc.path == u.Path {
					body = sc.body
				}
			}
			if body == "" {
				data, _ := os.ReadFile(filepath.Join(state, "static"))
				body = string(data)
			}
			version := "1.1"
			switch {
			case slices.Contains(cmd, "--http2-prior-knowledge"), slices.Contains(cmd, "--http2"):
				version = "2"
			case slices.Contains(cmd, "--http3-only"):
				version = "3"
			}
			fmt.Print(body + "\n200 " + version)
		}
	case "wrk":
		if slices.Contains(cmd, "-v") {
			fmt.Println("wrk 4.2.0 [epoll] Copyright (C) 2012 Will Glozer")
			return 1
		}
		fmt.Print(wrkOutput)
	case "h2load":
		if slices.Contains(cmd, "--version") {
			fmt.Println("h2load nghttp2/1.64.0")
			return 0
		}
		fmt.Print(h2loadOutput)
	case "lscpu":
		fmt.Println("Architecture:        x86_64\nCPU(s):              16\nModel name:          Fake CPU")
	case "uname":
		fmt.Println("Linux 6.8.0")
	case "grep":
		fmt.Println("MemTotal:       32768 kB")
	case "cat":
		switch filepath.Base(cmd[1]) {
		case "memory.stat":
			fmt.Println("anon 1048576\nfile 4096")
		case "memory.current":
			fmt.Println("4194304")
		case "cpu.stat":
			fmt.Printf("usage_usec %d\n", time.Now().UnixMicro())
		}
	case "/fitbench/idleconn":
		fmt.Println("READY 4 1ms")
		_, _ = io.Copy(io.Discard, os.Stdin)
		fmt.Println("DONE 1 closed by the server")
	default: // a server's version
		fmt.Println("fake 1.0\nbuilt today")
	}
	return 0
}

// fakeRoot makes a repository with what fitbench reads of bench, and a
// directory with fit and idleconn built, as -root and -bin take them.
func fakeRoot(t *testing.T) (root, bin string) {
	t.Helper()
	root, bin = t.TempDir(), t.TempDir()
	for name, data := range map[string]string{
		"bench/configs/fit/fit.konf":     `backend {{BACKEND}} workers {{WORKERS}}`,
		"bench/www/static/small.html":    smallHTML,
		"bench/loadgen/Dockerfile.h3":    "FROM scratch\n",
		"bench/configs/nginx/nginx.conf": "{{BACKEND_HOST}}:{{BACKEND_PORT}}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"fit", "idleconn"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("binary"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, bin
}
