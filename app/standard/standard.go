// Package standard imports every role of the apps fit ships with, the
// agent's, the tunnel's and WebRTC's TURN server, so that importing it
// registers them all.
package standard

import (
	// The roles are added here as they are made; see
	// docs/decisions/028-agent-and-tunnel.md and 029-webrtc.md.
	_ "github.com/lesismal/fit/agent/client"
	_ "github.com/lesismal/fit/agent/hub"
	_ "github.com/lesismal/fit/agent/node"
	_ "github.com/lesismal/fit/app"
	_ "github.com/lesismal/fit/tunnel/client"
	_ "github.com/lesismal/fit/tunnel/node"
	_ "github.com/lesismal/fit/tunnel/server"
	_ "github.com/lesismal/fit/webrtc/turn"
)
