The agent's roles, run beside the gateway on its engine: a `hub` that nodes and clients meet at, a
`node` that serves AI models from local or remote upstreams (OpenAI- and Anthropic-compatible
APIs), and a `client` that offers one API calling the models of the nodes it reaches. The roles
link over a WebSocket mesh, each listening at `listen` and dialing its `peers`. A role runs while
its member is set; any of them, in any number of processes. What a role's pages, behind
`console`, change is written back into its member, and kept in `admin.store` when there is one.
See [the agent](../agent.md).

```json
{
  "agent": {
    "hub": {
      "listen": {"address": ":9210", "password": "mesh-secret"}
    },
    "node": {
      "peers": [{"address": "127.0.0.1:9210", "password": "mesh-secret", "role": "hub"}],
      "models": [{"name": "qwen3", "api": "openai", "base_url": "http://127.0.0.1:11434/v1", "upstream": "qwen3:32b"}]
    },
    "client": {
      "peers": [{"address": "127.0.0.1:9210", "password": "mesh-secret", "role": "hub"}],
      "api": {"address": "127.0.0.1:9300"}
    }
  }
}
```
