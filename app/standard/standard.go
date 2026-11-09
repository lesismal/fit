// Package standard imports every role of the apps fit ships with, the
// LLM feature's, the tunnel's, WebRTC's TURN and media hubs, and the
// media_auth policies, the streaming role, the media libraries, the
// sync role and the BitTorrent client and tracker, so that importing it registers them all.
package standard

import (
	// The roles are added here as they are made; see
	// docs/decisions/028-llm-and-tunnel.md and 029-webrtc.md.
	_ "github.com/lesismal/fit/app"
	_ "github.com/lesismal/fit/bittorrent"
	_ "github.com/lesismal/fit/bittorrent/tracker"
	_ "github.com/lesismal/fit/llm/hub"
	_ "github.com/lesismal/fit/llm/node"
	_ "github.com/lesismal/fit/llm/proxy"
	_ "github.com/lesismal/fit/mediaauth"
	_ "github.com/lesismal/fit/medialib"
	_ "github.com/lesismal/fit/streaming"
	_ "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/tunnel/hub"
	_ "github.com/lesismal/fit/tunnel/node"
	_ "github.com/lesismal/fit/tunnel/proxy"
	_ "github.com/lesismal/fit/webrtc/media"
	_ "github.com/lesismal/fit/webrtc/turn"
)
