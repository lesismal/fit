The LLM feature's roles, run beside the gateway on its engine: a `hub` that nodes and proxies meet at, a
`node` that serves AI models from local or remote upstreams (OpenAI and every OpenAI-compatible
API, Anthropic, Gemini, Azure OpenAI, Ollama, Cohere), and a `proxy` that serves those APIs to the
agents of its machine or LAN, calling the models of the nodes it reaches. The roles
link over a WebSocket mesh, each listening at `listen` and dialing its `peers`; off loopback the
links go over TLS, the dialer checking the listener's certificate by its `pin` or its `ca_file`.
A dialer links with a `token` the listener's role issued, from its pages, its Admin API or
`fit token`, which the role keeps as a hash in its `tokens`; the listener's shared passwords still
link in this release, with a warning. Here the three share a machine and link over loopback. A role
runs while its member is set; any of them, in any number of processes. What a role's pages, behind
`console`, change is written back into its member, and kept in `admin.store` when there is one.
See [the LLM feature](../llm.md).

```json
{
  "llm": {
    "hub": {
      "listen": {"address": "127.0.0.1:9210"},
      "tokens": [
        {"id": "fdd44d906b8e6ce2", "name": "gpu-1", "hash": "1c994a095de7b566dcce0f229b829af96d85911b6d3e786f217c77340bc36aa4", "created_at": 1793900000},
        {"id": "5a3716dd56e5e458", "name": "laptop", "hash": "4e6349fdb79b1b48fb57cc61b5aa9593490f288f28f722e0245c04a9df6c66df", "created_at": 1793900000}
      ]
    },
    "node": {
      "peers": [{"address": "127.0.0.1:9210", "token": "fitt1.fdd44d906b8e6ce2.Hm2vJvGyTEJVh2_zzu0ZJlENz1A3IYXS2JydjHyJShQ", "role": "hub"}],
      "models": [{"name": "qwen3", "api": "openai", "base_url": "http://127.0.0.1:11434/v1", "upstream": "qwen3:32b"},
        {"name": "llama3.2", "api": "ollama", "base_url": "http://127.0.0.1:11434", "upstream": "llama3.2:3b"}]
    },
    "proxy": {
      "peers": [{"address": "127.0.0.1:9210", "token": "fitt1.5a3716dd56e5e458.nz51QI_A3avzPo4tagvevbtlJ-dNi-Us-k4-il9gAV4", "role": "hub"}],
      "api": {"address": "127.0.0.1:9300"}
    }
  }
}
```
