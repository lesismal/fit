// Package tunnel holds the tunnel's roles, which reach services behind NAT
// through a hub: tunnel/hub, tunnel/node and tunnel/proxy, each a
// member of fit's config under tunnel and run on the listeners' fib engine
// by package app. What they share is in tunnel/internal. See docs/tunnel.md
// and docs/decisions/028-llm-and-tunnel.md.
package tunnel
