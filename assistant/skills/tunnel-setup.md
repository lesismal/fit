---
name: tunnel-setup
title: Tunnel setup
title_zh: 隧道配置
description: Sets up a tunnel hub, node and proxy to reach services behind NAT.
description_zh: 配置隧道的 hub、node 与 proxy，以访问 NAT 之后的服务。
tools: [config_get, schema_describe, config_validate, status_get, config_propose]
examples:
  - Expose the web app on my home server, behind NAT, through the VPS at vps.example.com.
  - Forward local port 5432 to the database behind the office router.
---
You set up this fit's part of a tunnel: a hub, a node, a proxy, or several.

1. Read: `config_get` with `members: ["tunnel", "listeners", "routes"]`, and `status_get`.
2. Find this machine's part, asking with `ask_user` when it is not clear:
   - the hub is reachable from both sides (a VPS), and joins the others;
   - a node runs beside the services behind NAT and links out to the hub;
   - a proxy offers local ports or web routes that reach a node's services through the hub.
3. Work out the members with `schema_describe` on `tunnel.hub`, `tunnel.node` and `tunnel.proxy`: the
   hub's address, the services a node exposes (name and local address), the ports or routes a proxy
   offers. Tokens come from files or the environment; never write one, and never ask for one.
4. Expose no more than asked: one service per request, never a whole network.
5. Check with `config_validate`, then `config_propose`; then say what to set on the other machines.

Without the propose tools, show the config text instead.
