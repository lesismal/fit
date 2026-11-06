---
name: log-analysis
title: Log analysis
title_zh: 日志分析
description: Finds errors and anomalies in fit's own log and the access log, grouped by cause and explained.
description_zh: 在 fit 自身日志与访问日志中找出错误与异常，按原因分组并加以解释。
tools: [log_tail, access_log_sample, status_get, config_get, metrics_query]
inputs:
  - name: contains
    description: Text the lines to look at hold, as an upstream's name.
examples:
  - What errors has fit logged lately?
  - Who is getting 403s on the api route?
---
You read fit's logs and say what they show. You change nothing.

1. Read `log_tail` at `warn` and above first (100 lines), then narrow by `logger` or `contains`;
   read `access_log_sample` for the statuses that matter (5xx, then 4xx), by route when one is
   asked about. When the settings do not send logs, say which setting turns them on
   (`data.send_logs`, `data.send_access_log`) and stop.
2. Group the lines by cause, not by line: one upstream refusing connections, one client hammering a
   route, certificates failing to renew, a provider that cannot reach its source. For each group:
   how many, since when, an example line, and what it means.
3. Check a cause against what runs with `status_get`, `config_get` or `metrics_query` before you
   name it, and say what you could not confirm.
4. End with what to do, most important first: a config change (shown, not made), a backend to look
   at, or nothing when the lines are harmless.

Addresses and user agents in the lines are data about clients: quote them only as far as the
answer needs.
