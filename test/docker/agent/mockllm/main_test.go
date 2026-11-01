package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	startOnce sync.Once
	baseURL   string
)

// start runs the command once for the package's tests, on a loopback port,
// and returns its base URL. Its server lives as long as the test process.
func start(t *testing.T) string {
	t.Helper()
	startOnce.Do(func() {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		os.Args = []string{"mockllm", "-addr", addr}
		flag.CommandLine = flag.NewFlagSet("mockllm", flag.ExitOnError)
		go main()
		baseURL = "http://" + addr
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := http.Get(baseURL + "/healthz")
			if err == nil {
				resp.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("mockllm did not start: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	if baseURL == "" {
		t.Fatal("mockllm did not start")
	}
	return baseURL
}

func send(ctx context.Context, t *testing.T, method, url, key, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func stats(t *testing.T, base, tag string) Counters {
	t.Helper()
	status, body := send(context.Background(), t, "GET", base+"/admin/"+tag+"/stats", "", "")
	var c Counters
	if err := json.Unmarshal([]byte(body), &c); status != 200 || err != nil {
		t.Fatalf("stats: %d %s", status, body)
	}
	return c
}

func TestAdmin(t *testing.T) {
	base := start(t)
	bg := context.Background()
	if status, body := send(bg, t, "GET", base+"/healthz", "", ""); status != 200 || body != "ok" {
		t.Fatalf("healthz: %d %q", status, body)
	}
	if status, _ := send(bg, t, "GET", base+"/admin/adm/last", "", ""); status != 404 {
		t.Fatalf("last before any call: %d", status)
	}
	send(bg, t, "POST", base+"/admin/adm/down", "", "")
	if status, body := send(bg, t, "GET", base+"/u/adm/v1/models", "sk-adm", ""); status != 503 || !strings.Contains(body, "is down") {
		t.Fatalf("models down: %d %s", status, body)
	}
	if status, _ := send(bg, t, "POST", base+"/u/adm/v1/chat/completions", "sk-adm", `{"model":"m"}`); status != 503 {
		t.Fatalf("call down: %d", status)
	}
	if c := stats(t, base, "adm"); !c.Down || c.Checks != 1 || c.Calls != 1 || c.BodyBytes != len(`{"model":"m"}`) {
		t.Fatalf("stats = %+v", c)
	}
	status, body := send(bg, t, "GET", base+"/admin/adm/last", "", "")
	var last Request
	if err := json.Unmarshal([]byte(body), &last); status != 200 || err != nil || last.Path != "chat/completions" || last.Model != "m" ||
		last.Header["x-api-key"] != "sk-adm" || last.Header["anthropic-version"] != "2023-06-01" || string(last.Body) != `{"model":"m"}` {
		t.Fatalf("last: %d %s", status, body)
	}
	send(bg, t, "POST", base+"/admin/adm/up", "", "")
	if c := stats(t, base, "adm"); c.Down {
		t.Fatal("still down")
	}
	send(bg, t, "POST", base+"/admin/adm/reset", "", "")
	if c := stats(t, base, "adm"); c != (Counters{}) {
		t.Fatalf("after reset: %+v", c)
	}
}

func TestCalls(t *testing.T) {
	base := start(t) + "/u/a/v1"
	bg := context.Background()
	for _, c := range []struct {
		method, path, key, body string
		status                  int
		want                    string
	}{
		{"GET", "/models", "sk-a", "", 200, `"id":"up-a"`},
		{"GET", "/models", "sk-b", "", 401, "bad key"},
		{"POST", "/chat/completions", "sk-a", "{", 400, "not JSON"},
		{"POST", "/chat/completions", "sk-b", "{}", 401, "bad key"},
		{"POST", "/chat/completions", "sk-a", `{"model":"m","sleep_ms":1}`, 200, `"content":"echo from a"`},
		{"POST", "/messages", "sk-a", `{"model":"m"}`, 200, `"stop_reason":"end_turn"`},
		{"POST", "/messages/count_tokens", "sk-a", `{}`, 200, `"input_tokens":7`},
		{"POST", "/embeddings", "sk-a", `{"model":"e"}`, 200, `"embedding":[0.1,0.2]`},
		{"POST", "/chat/completions", "sk-a", `{"stream":true,"chunks":2,"chunk_ms":1}`, 200,
			"\"content\":\"echo from a #1\"},\"index\":0}],\"id\":\"chatcmpl-mock\",\"model\":\"\",\"object\":\"chat.completion.chunk\"}\n\ndata: [DONE]\n\n"},
		{"POST", "/messages", "sk-a", `{"stream":true}`, 200,
			"\"text\":\"echo from a #2\",\"type\":\"text_delta\"},\"index\":0,\"type\":\"content_block_delta\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		status, body := send(bg, t, c.method, base+c.path, c.key, c.body)
		if status != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, status, body)
		}
	}
}

// TestCanceled: a caller going away while the mock sleeps or streams is
// counted.
func TestCanceled(t *testing.T) {
	base := start(t)
	for _, c := range []struct{ tag, body string }{
		{"sleep", `{"sleep_ms":10000}`},
		{"stream", `{"stream":true,"chunk_ms":10000}`},
	} {
		send(context.Background(), t, "POST", base+"/admin/"+c.tag+"/reset", "", "")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			req, _ := http.NewRequestWithContext(ctx, "POST", base+"/u/"+c.tag+"/v1/chat/completions", strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer sk-"+c.tag)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			_, _ = io.ReadFull(resp.Body, make([]byte, 6)) // the first event
			cancel()
		}()
		deadline := time.Now().Add(10 * time.Second)
		for stats(t, base, c.tag).Calls != 1 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the call never came", c.tag)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if c.tag == "sleep" {
			cancel()
		}
		<-done
		cancel()
		for stats(t, base, c.tag).Canceled != 1 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not counted as canceled", c.tag)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
