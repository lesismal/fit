// Package tunnel holds the tunnel's roles, which reach services behind NAT
// through a server: tunnel/server, tunnel/node and tunnel/client, each a
// member of fit's config under tunnel and run on the listeners' fib engine
// by package app. What they share is in tunnel/internal. See docs/tunnel.md
// and docs/decisions/028-agent-and-tunnel.md.
package tunnel
