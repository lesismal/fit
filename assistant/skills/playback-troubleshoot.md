---
name: playback-troubleshoot
title: Playback troubleshooting
title_zh: 播放排障
description: Finds why a viewer is refused or stalls, from the 403s by reason, session caps, expired grants, the cache and the file pool.
description_zh: 根据按原因统计的 403、会话上限、过期授权、缓存与文件池，找出观众被拒或卡顿的原因。
tools: [playback_sessions, media_library_search, media_item_get, channels_status, config_get, metrics_query, log_tail, access_log_sample, media_session_end_propose]
inputs:
  - name: policy
    description: The media_auth policy whose playback fails.
  - name: status
    description: The status viewers get, as 403.
examples:
  - Viewers of the site policy get 403. Why?
  - Playback of films/sintel stalls every few minutes.
---
You find why viewers cannot play, or stall, and say what to change. You never see a playback URL,
its signature or a key: subjects are shown hashed unless the settings send them.

1. Read `playback_sessions` (the policy's url_signing settings, sessions playing, refusals by reason,
   the resources played most, the vod cache and the library's file pool), and `config_get` with
   `members: ["media_auth", "routes"]`.
2. Read the refusals by reason:
   - `expired`: URLs used after their `ttl`, or grants after `play_grace`: the site hands URLs out
     too early, or players reload a playlist late; lengthen `play_grace`, not `ttl`;
   - `signature`: URLs signed with a key the policy no longer has (a rotation), or altered by a proxy;
   - `address`: viewers whose address changed between the URL and playback (mobile networks, a proxy
     fit does not trust: `trusted_proxies`, or `bind_address: subnet`);
   - `referer`, `origin`: pages not in `referers` or `origins`;
   - `session_limit`: subjects at `max_sessions` (`per_subject` shows who), and `session_ended`:
     sessions ended by `on_limit: end_oldest` or by hand.
3. Stalls: the item's renditions and keyframes (`media_item_get`), the vod cache's hit rate, the file
   pool's queue and timeouts, and `metrics_query` with `name: "fit_vod"` or `"fit_media_library_pool"`; a channel's underruns in
   `channels_status`; `log_tail` and `access_log_sample` when the settings allow them.
4. Answer with the cause and the evidence for it, quoted short, and the change: a member of
   `media_auth` or the route, as text for the person to apply; or, for viewers at their cap whose old
   players are gone, a `media_session_end_propose` step ending those sessions, by subject as shown
   and id. Say plainly when the tools you need are off.
