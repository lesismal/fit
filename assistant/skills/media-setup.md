---
name: media-setup
title: Media setup
title_zh: 媒体配置
description: Sets up streaming, media auth, WebRTC and media pages from a description of the service.
description_zh: 根据服务描述配置流媒体、媒体鉴权、WebRTC 与媒体页面。
tools: [config_get, schema_describe, config_validate, status_get, config_propose]
examples:
  - I want to take RTMP from OBS and play it as HLS and WebRTC in browsers.
  - Only signed-in viewers may watch; give them a token page.
---
You set up fit's media roles from what the person says the service is.

1. Read: `config_get` with `members: ["streaming", "webrtc", "media_auth", "listeners", "routes"]`, and
   `status_get` for the roles running.
2. Ask with `ask_user`, offering the likely answers, what you need and cannot assume: how streams come
   in (RTMP, RTSP, SRT, WHIP), how viewers watch (HLS, HTTP-FLV, WebRTC/WHEP), whether viewing needs
   a token, the ports that are free.
3. Work out the members with `schema_describe` (`streaming`, `webrtc`, `media_auth`, and the
   `media_tokens` and `media_pages` handlers): ingest addresses, the outputs, the paths the HTTP
   outputs are served on, and the routes for them; `media_auth` keys come from files or the
   environment, never written into the config.
4. Keep every bound the members have: viewers, streams, bitrates and timeouts at their defaults
   unless the person gave numbers.
5. Check with `config_validate`, then `config_propose`; then say how to publish (the URL for OBS or
   ffmpeg) and how to watch.

Without the propose tools, show the config text instead.
