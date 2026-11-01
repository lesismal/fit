package config

import (
	"strings"
	"testing"
)

func TestLLMMembers(t *testing.T) {
	const hash = `"$2a$10$abcdefghijklmnopqrstuuK3o5D2wC6Qn7oW7v3yTzR3z0e6Gm9Ka"`
	good := `{"llm": {
		"hub": {"listen": {"address": ":9200", "password": "p"}, "max_nodes": 10},
		"node": {"id": "node-1", "name": "gpu", "peers": [{"address": "10.0.0.1:9200", "password": "p", "role": "hub"}],
			"models": [{"name": "m", "api": "openai", "base_url": "http://127.0.0.1:11434/v1", "max_inflight": 4},
				{"name": "m", "api": "anthropic", "base_url": "https://api.example.com/v1"},
				{"name": "m", "api": "gemini", "base_url": "https://generativelanguage.googleapis.com/v1beta"},
				{"name": "m", "api": "ollama", "base_url": "http://127.0.0.1:11434"},
				{"name": "m", "api": "cohere", "base_url": "https://api.cohere.com/v2"},
				{"name": "z", "api": "azure_openai", "base_url": "https://r.openai.azure.com", "api_version": "2024-10-21"}],
			"settings": {"allow_hub_manage": true, "gpu_sudo": false, "refresh_interval": "10s", "job_timeout": "2h"},
			"proxy": {"timeout": "10m", "max_inflight": 8}},
		"proxy": {"api": {"address": "127.0.0.1:9300"}, "console": {"address": "127.0.0.1:9301", "users": [{"username": "a", "password_hash": ` + hash + `}]},
			"api_keys": [{"id": "0123456789abcdef", "name": "k", "prefix": "ah-xxxxxxx", "hash": "` + strings.Repeat("a", 64) + `"}]}}}`
	c, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if c.LLM.Node.Settings.Sudo() || c.LLM.Node.Settings.Job().String() != "2h0m0s" || c.LLM.Node.Proxy.Inflight() != 8 ||
		c.LLM.Node.Proxy.Idle() != DefaultLLMIdleTimeout.Std() || c.LLM.Hub.Nodes() != 10 || c.LLM.Proxy.API.Body() != DefaultLLMMaxBodyBytes {
		t.Fatalf("%+v", c.LLM)
	}
	for _, bad := range []struct{ doc, want string }{
		{`{"llm": {"hub": {"peers": [{"address": "h:1", "password": "p", "role": "hub"}]}}}`, "links with node and proxy"},
		{`{"llm": {"hub": {"peers": [{"address": ":1", "password": "p", "role": "node"}]}}}`, "not a host and port"},
		{`{"llm": {"hub": {"peers": [{"address": "h:1", "password": "p", "role": "node"}, {"address": "h:1", "password": "q", "role": "node"}]}}}`, "listed twice"},
		{`{"llm": {"node": {"models": [{"name": "a b", "api": "openai", "base_url": "http://h"}]}}}`, "without spaces"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "palm", "base_url": "http://h"}]}}}`, "palm"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "azure_openai", "base_url": "https://r.openai.azure.com"}]}}}`, "needs its api_version"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "openai", "base_url": "http://h", "api_version": "2024-10-21"}]}}}`, "only for azure_openai"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "azure_openai", "base_url": "http://h", "api_version": "2024 10"}]}}}`, "api_version is up to 64"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "openai", "base_url": "http://h"}, {"name": "a", "api": "azure_openai", "base_url": "http://i", "api_version": "v"}]}}}`, "listed twice for agents' openai API"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "openai", "base_url": "ftp://h"}]}}}`, "base_url"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "openai", "base_url": "http://h/"}]}}}`, "trailing slash"},
		{`{"llm": {"node": {"models": [{"name": "a", "api": "openai", "base_url": "http://h"}, {"name": "a", "api": "openai", "base_url": "http://i"}]}}}`, "listed twice"},
		{`{"llm": {"node": {"settings": {"refresh_interval": "1s"}}}}`, "refresh_interval is 5s to 1h"},
		{`{"llm": {"node": {"listen": {"address": "nope", "password": "p"}}}}`, "not a host and port"},
		{`{"llm": {"proxy": {"api": {"address": "0.0.0.0:9300"}}}}`, "not a loopback address"},
		{`{"llm": {"proxy": {"api_keys": [{"id": "x", "name": "k", "hash": "y"}]}}}`, "16 hex digits"},
		{`{"llm": {"proxy": {"api_keys": [{"id": "0123456789abcdef", "name": "k", "hash": "y"}]}}}`, "64 hex digits"},
		{`{"llm": {"node": {"name": " x"}}}`, "spaces around it"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}
