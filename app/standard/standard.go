// Package standard imports every role of the apps fit ships with, the
// agent's and the tunnel's, so that importing it registers them all.
package standard

import (
	// The roles are added here as they are ported; see
	// docs/decisions/028-agent-and-tunnel.md.
	_ "github.com/lesismal/fit/agent/client"
	_ "github.com/lesismal/fit/agent/hub"
	_ "github.com/lesismal/fit/agent/node"
	_ "github.com/lesismal/fit/app"
	_ "github.com/lesismal/fit/tunnel/client"
	_ "github.com/lesismal/fit/tunnel/node"
	_ "github.com/lesismal/fit/tunnel/server"
)
