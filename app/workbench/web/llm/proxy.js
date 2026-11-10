// fit llm proxy console: overview (agent API + quick start + stats), models (catalog with routes), the nodes behind
// its hubs (to see, never to manage), traffic (the link map), API keys, connections and settings. Needs ui.js,
// peers.js, flow.js and forms.js.
//
// fit llm proxy 控制台：概览（agent 接口 + 接入示例 + 统计）、模型（目录与路由）、hub 背后的节点（只读，不能管理）、流量
// （链路图）、API Key、连接与设置。依赖 ui.js、peers.js、flow.js 与 forms.js。
(function () {
  'use strict';
  var I18N = {
    'en': {
      'mod.label': 'Proxy', 'c.sub': 'Model gateway for agents on this machine and LAN', 'c.overview': 'Overview', 'c.models': 'Models', 'c.keys': 'API keys',
      'c.peers': 'Connections', 'c.settings': 'Settings',
      'c.api': 'Agent API', 'c.api_note': 'OpenAI-, Anthropic-, Gemini-, Ollama- and Cohere-compatible endpoints at the address below (separate from this console), for agents on this machine or, on a LAN address with TLS, on the LAN; authenticated with an API key (Authorization: Bearer, x-api-key or x-goog-api-key).',
      'c.base_openai': 'OpenAI base URL', 'c.base_anthropic': 'Anthropic base URL', 'c.base_other': 'Gemini, Ollama, Cohere base URL',
      'c.st_models': 'Models', 'c.st_ready': '{n} ready', 'c.st_keys': 'API keys', 'c.st_calls': 'Calls', 'c.st_errors': '{n} errors',
      'c.st_inflight': 'In flight', 'c.st_bytes': '{b} relayed', 'c.quick': 'Quick start', 'c.quick_note': 'Replace <API_KEY> with a key from the API keys page.',
      'c.per_model': 'Calls per model', 'c.model': 'Model', 'c.api_fmt': 'API', 'c.state': 'State', 'c.routes': 'Routes',
      'c.calls': 'Calls', 'c.errors': 'Errors', 'c.bytes': 'Bytes', 'c.inflight': 'In flight', 'c.last': 'Last call',
      'c.ready': 'ready', 'c.not_ready': 'not ready', 'c.no_models': 'No models yet: connect to a hub (or a node) on the Connections page.',
      'c.route_node': 'direct', 'c.route_hub': 'hub', 'c.route_note': 'Calls prefer a directly connected node that has the model ready (least in flight), then hubs (round robin); a route that cannot serve the model is skipped.',
      'c.ready_nodes': '{r}/{n} nodes ready',
      'c.key_new': 'New API key', 'c.key_name': 'Name', 'c.key_name_hint': 'e.g. the agent or machine that uses it',
      'c.key_prefix': 'Key', 'c.key_created': 'Created', 'c.key_used': 'Last used', 'c.key_calls': 'Calls',
      'c.key_none': 'No API keys: create one for your agents.', 'c.key_del': 'Delete API key {n}? Agents using it stop working.',
      'c.key_once': 'Copy the key now: it is shown only this once (only its hash is stored).', 'c.key_created_title': 'API key created',
      'c.key_note': 'Usage (last used, calls) is counted in memory since the role started; it is not saved.',
      'c.profile': 'Profile', 'c.name': 'Display name', 'c.name_hint': 'How hubs and nodes see this proxy',
      'c.nodes': 'Nodes', 'c.traffic': 'Traffic', 'c.nodes_note': 'The nodes behind each hub this proxy links to, as the hub reports them, and the nodes it links to directly. To see only: each machine is managed on its own pages.',
      'c.hub_of': 'Hub {n}', 'c.direct': 'Linked directly', 'c.node': 'Node', 'c.status': 'Status', 'c.hw': 'Hardware', 'c.load': 'Load',
      'c.link': 'Link', 'c.no_nodes': 'No nodes.', 'c.no_hubs': 'No hub or node is linked: add one on the Connections page.',
      'c.more_nodes': '{n} of {t} nodes shown', 'c.more_models': '+{n} more', 'c.cores': '{n} cores', 'c.jobs': '{n} jobs',
      'c.load1': 'load {n}', 'c.calls_n': '{n} calls', 'c.desc': 'Description', 'c.host': 'Host', 'c.os': 'OS', 'c.cpu': 'CPU',
      'c.mem': 'Memory', 'c.gpus': 'GPUs', 'c.uptime': 'Uptime', 'c.seen': 'Last seen', 'c.traffic_via': 'Calls through the hub',
      'c.api_title': 'Agent API', 'c.api_addr': 'Address', 'c.api_addr_hint': 'host:port. A loopback address (127.0.0.1:9301) serves this machine; a LAN address needs a certificate and key.',
      'c.api_cert': 'Certificate file', 'c.api_cert_hint': 'PEM, with any intermediates', 'c.api_key': 'Key file', 'c.api_key_hint': 'PEM private key',
      'c.api_body': 'Max body bytes', 'c.api_body_hint': '32 MiB by default', 'c.api_conns': 'Max connections', 'c.api_conns_hint': '256 by default',
      'c.api_inflight': 'Max calls at once', 'c.api_inflight_hint': '256 by default', 'c.api_first': 'First byte timeout', 'c.api_first_hint': '5m by default',
      'c.api_total': 'Call timeout', 'c.api_total_hint': '30m by default', 'c.api_off': 'Stop the API', 'c.api_off_confirm': 'Stop serving the agent API? Agents using it stop working.',
      'c.api_now': 'Serving at {u}', 'c.api_none': 'No API: agents cannot call this proxy until an address is set.',
      'c.lm_proxy': 'This proxy', 'c.lm_hubs': 'Hubs', 'c.lm_nodes': 'Nodes', 'c.lm_models': 'Models'
    },
    'zh-CN': {
      'mod.label': '代理', 'c.sub': '本机与局域网 agent 的模型网关', 'c.overview': '概览', 'c.models': '模型', 'c.keys': 'API Key',
      'c.peers': '连接', 'c.settings': '设置',
      'c.api': 'Agent 接口', 'c.api_note': '下方地址（与本控制台分开）提供 OpenAI / Anthropic / Gemini / Ollama / Cohere 兼容接口，供本机 agent 调用；监听局域网地址并启用 TLS 时，也供局域网内的 agent 调用。使用 API Key 认证（Authorization: Bearer、x-api-key 或 x-goog-api-key）。',
      'c.base_openai': 'OpenAI base URL', 'c.base_anthropic': 'Anthropic base URL', 'c.base_other': 'Gemini、Ollama、Cohere base URL',
      'c.st_models': '模型', 'c.st_ready': '{n} 个就绪', 'c.st_keys': 'API Key', 'c.st_calls': '调用', 'c.st_errors': '{n} 次失败',
      'c.st_inflight': '进行中', 'c.st_bytes': '已转发 {b}', 'c.quick': '接入示例', 'c.quick_note': '把 <API_KEY> 替换为「API Key」页面创建的 Key。',
      'c.per_model': '各模型调用', 'c.model': '模型', 'c.api_fmt': '格式', 'c.state': '状态', 'c.routes': '路由',
      'c.calls': '调用', 'c.errors': '失败', 'c.bytes': '字节', 'c.inflight': '进行中', 'c.last': '最近调用',
      'c.ready': '就绪', 'c.not_ready': '未就绪', 'c.no_models': '暂无模型：请在「连接」页面连接 hub（或节点）。',
      'c.route_node': '直连', 'c.route_hub': 'hub', 'c.route_note': '调用优先选择已就绪该模型的直连节点（在途最少），其次是 hub（轮询）；不能提供该模型的路由会被跳过。',
      'c.ready_nodes': '{r}/{n} 个节点就绪',
      'c.key_new': '新建 API Key', 'c.key_name': '名称', 'c.key_name_hint': '例如使用它的 agent 或机器',
      'c.key_prefix': 'Key', 'c.key_created': '创建时间', 'c.key_used': '最近使用', 'c.key_calls': '调用',
      'c.key_none': '暂无 API Key：请为 agent 创建一个。', 'c.key_del': '删除 API Key {n}？使用它的 agent 将无法调用。',
      'c.key_once': '请立即复制：Key 只显示这一次（只保存其哈希）。', 'c.key_created_title': 'API Key 已创建',
      'c.key_note': '使用情况（最近使用、调用数）在内存中自角色启动起计数，不会保存。',
      'c.profile': '基本信息', 'c.name': '显示名称', 'c.name_hint': 'hub 与节点看到的本 proxy 名称',
      'c.nodes': '节点', 'c.traffic': '流量', 'c.nodes_note': '本 proxy 所连接的每个 hub 背后的节点（由 hub 上报），以及本 proxy 直连的节点。仅供查看：每台机器在其自身页面上管理。',
      'c.hub_of': 'Hub {n}', 'c.direct': '直连', 'c.node': '节点', 'c.status': '状态', 'c.hw': '硬件', 'c.load': '负载',
      'c.link': '连接', 'c.no_nodes': '暂无节点。', 'c.no_hubs': '尚未连接任何 hub 或节点：请在「连接」页面添加。',
      'c.more_nodes': '显示 {t} 个中的 {n} 个节点', 'c.more_models': '另有 {n} 个', 'c.cores': '{n} 核', 'c.jobs': '{n} 个作业',
      'c.load1': '负载 {n}', 'c.calls_n': '{n} 次调用', 'c.desc': '描述', 'c.host': '主机', 'c.os': '操作系统', 'c.cpu': 'CPU',
      'c.mem': '内存', 'c.gpus': '显卡', 'c.uptime': '运行时间', 'c.seen': '最后在线', 'c.traffic_via': '经 hub 的调用',
      'c.api_title': 'Agent 接口', 'c.api_addr': '地址', 'c.api_addr_hint': 'host:port。回环地址（127.0.0.1:9301）服务本机；局域网地址需要证书与私钥。',
      'c.api_cert': '证书文件', 'c.api_cert_hint': 'PEM，可含中间证书', 'c.api_key': '私钥文件', 'c.api_key_hint': 'PEM 私钥',
      'c.api_body': '请求体上限（字节）', 'c.api_body_hint': '默认 32 MiB', 'c.api_conns': '连接数上限', 'c.api_conns_hint': '默认 256',
      'c.api_inflight': '并发调用上限', 'c.api_inflight_hint': '默认 256', 'c.api_first': '首字节超时', 'c.api_first_hint': '默认 5m',
      'c.api_total': '调用超时', 'c.api_total_hint': '默认 30m', 'c.api_off': '停止接口', 'c.api_off_confirm': '停止提供 agent 接口？使用它的 agent 将无法调用。',
      'c.api_now': '服务地址 {u}', 'c.api_none': '未提供接口：设置地址之前 agent 无法调用本 proxy。',
      'c.lm_proxy': '本 proxy', 'c.lm_hubs': 'Hub', 'c.lm_nodes': '节点', 'c.lm_models': '模型'
    }
  };

  WB.module({
    id: 'llm.proxy', group: 'llm', order: 3, icon: 'proxy', label: 'mod.label', i18n: I18N,
    nav: [
      { id: 'overview', icon: 'dashboard', label: 'c.overview' }, { id: 'models', icon: 'cube', label: 'c.models' },
      { id: 'nodes', icon: 'server', label: 'c.nodes' }, { id: 'traffic', icon: 'activity', label: 'c.traffic' },
      { id: 'keys', icon: 'key', label: 'c.keys' }, { id: 'peers', icon: 'link', label: 'c.peers' },
      { id: 'settings', icon: 'settings', label: 'c.settings' }
    ],
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;
      var Settings = window.Settings.with(UI), PeersPanel = window.PeersPanel.with(UI), Jobs = window.Jobs.with(UI);


      function fmtBytes(n) {
        if (!n) return '0 B';
        if (n < 1024) return n + ' B';
        if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
        return UI.fmtMB(n / 1048576);
      }
      function apiBadge(api) { return UI.badge(api, api === 'anthropic' ? 'warn' : api === 'openai' || api === 'azure_openai' ? 'info' : 'ok'); }
      function codeBlock(text) {
        return h('div.stack', { style: { gap: '6px' } },
          h('pre.console', text),
          h('div.btn-row', UI.btn(t('copy'), 'copy', function () { UI.copy(text); }, 'sm')));
      }
      function copyLine(label, text) {
        return [label, h('span.row', h('code', text), UI.btn('', 'copy', function () { UI.copy(text); }, 'ghost icon sm'))];
      }

      // ---- Overview / 概览 ----
      function snippets(origin, models) {
        function first(apis) { return (models.filter(function (m) { return apis.indexOf(m.api) >= 0; })[0] || {}).name || '<model>'; }
        var oa = first(['openai', 'azure_openai']), an = first(['anthropic']), ge = first(['gemini']), ol = first(['ollama']);
        return [
          ['openai', 'OpenAI SDK', 'from openai import OpenAI\n\nclient = OpenAI(base_url="' + origin + '/v1", api_key="<API_KEY>")\n' +
            'resp = client.chat.completions.create(\n    model="' + oa + '",\n    messages=[{"role": "user", "content": "Hello"}],\n)\nprint(resp.choices[0].message.content)'],
          ['claude', 'Claude Code', '# ANTHROPIC_BASE_URL has no /v1: the SDK appends /v1/messages\n' +
            'export ANTHROPIC_BASE_URL=' + origin + '\nexport ANTHROPIC_AUTH_TOKEN=<API_KEY>   # or ANTHROPIC_API_KEY=<API_KEY>\n' +
            'export ANTHROPIC_MODEL=' + an + '\nexport ANTHROPIC_SMALL_FAST_MODEL=' + an + '\nclaude'],
          ['codex', 'Codex', '# ~/.codex/config.toml\nmodel = "' + oa + '"\nmodel_provider = "fit_llm"\n\n[model_providers.fit_llm]\n' +
            'name = "fit llm"\nbase_url = "' + origin + '/v1"\nenv_key = "FIT_LLM_API_KEY"\nwire_api = "chat"   # or "responses"\n\n' +
            '# then: export FIT_LLM_API_KEY=<API_KEY>; codex'],
          ['curl', 'curl', 'curl ' + origin + '/v1/chat/completions \\\n  -H "Authorization: Bearer <API_KEY>" \\\n  -H "Content-Type: application/json" \\\n' +
            '  -d \'{"model": "' + oa + '", "stream": true, "messages": [{"role": "user", "content": "Hello"}]}\'\n\n' +
            'curl ' + origin + '/v1/messages \\\n  -H "x-api-key: <API_KEY>" -H "anthropic-version: 2023-06-01" \\\n  -H "Content-Type: application/json" \\\n' +
            '  -d \'{"model": "' + an + '", "max_tokens": 256, "messages": [{"role": "user", "content": "Hello"}]}\''],
          ['gemini', 'Gemini SDK', 'from google import genai\nfrom google.genai import types\n\n' +
            'client = genai.Client(api_key="<API_KEY>", http_options=types.HttpOptions(base_url="' + origin + '"))\n' +
            'resp = client.models.generate_content(model="' + ge + '", contents="Hello")\nprint(resp.text)'],
          ['ollama', 'Ollama', 'from ollama import Client\n\n' +
            'client = Client(host="' + origin + '", headers={"Authorization": "Bearer <API_KEY>"})\n' +
            'resp = client.chat(model="' + ol + '", messages=[{"role": "user", "content": "Hello"}])\nprint(resp.message.content)']
        ];
      }

      function overview(el, me) {
        // The agent API listens on an address of its own (api_url), separate from this console.
        // agent 接口有独立的监听地址（api_url），与本控制台分开。
        var origin = String((me && me.api_url) || location.origin).replace(/\/+$/, '');
        var statsBox = h('div'), perModel = h('div'), quick = h('div'), active = 'openai', models = [];
        function drawQuick() {
          var list = snippets(origin, models), cur = list.filter(function (s) { return s[0] === active; })[0] || list[0];
          UI.mount(quick, UI.tabs(list.map(function (s) { return [s[0], s[1]]; }), cur[0], function (k) { active = k; drawQuick(); }),
            h('div', { style: { marginTop: '12px' } }, codeBlock(cur[2])));
        }
        UI.mount(el, h('div.stack',
          h('div.hero', h('h1', t('c.api')), h('p', t('c.api_note'))),
          UI.card(t('c.api'), UI.kv([copyLine(t('c.base_openai'), origin + '/v1'), copyLine(t('c.base_anthropic'), origin), copyLine(t('c.base_other'), origin)])),
          statsBox,
          UI.card(t('c.quick'), h('div.stack', h('div.muted.small', t('c.quick_note')), quick)),
          UI.card(t('c.per_model'), perModel, null, true)));
        drawQuick();
        function load() {
          return Promise.all([UI.rpc('/proxy/stats'), UI.rpc('/proxy/models'), UI.rpc('/proxy/keys/list')]).then(function (r) {
            var st = r[0], ms = r[1] || [], keys = r[2] || [];
            var first = !models.length && ms.length;
            models = ms;
            if (first) drawQuick();
            UI.mount(statsBox, h('div.grid.grid--4',
              UI.stat('cube', t('c.st_models'), String(ms.length), t('c.st_ready', { n: ms.filter(function (m) { return m.ready; }).length }), 'info'),
              UI.stat('key', t('c.st_keys'), String(keys.length)),
              UI.stat('activity', t('c.st_calls'), String(st.total.calls), t('c.st_errors', { n: st.total.errors }), 'ok'),
              UI.stat('zap', t('c.st_inflight'), String(st.total.inflight), t('c.st_bytes', { b: fmtBytes(st.total.bytes) }), 'warn')));
            UI.mount(perModel, UI.table([
              { label: t('c.model'), render: function (m) { return h('b', m.model); } },
              { label: t('c.calls'), render: function (m) { return String(m.calls); } },
              { label: t('c.errors'), render: function (m) { return m.errors ? UI.badge(String(m.errors), 'danger') : '0'; } },
              { label: t('c.inflight'), render: function (m) { return String(m.inflight); } },
              { label: t('c.bytes'), render: function (m) { return fmtBytes(m.bytes); } },
              { label: t('c.last'), render: function (m) { return UI.ago(m.last_at); } }
            ], st.models, { empty: t('none') }));
          }).catch(UI.fail);
        }
        load();
        UI.every(5000, load);
      }

      // ---- Models / 模型 ----
      function routeBadge(r) {
        var label = (r.kind === 'node' ? t('c.route_node') : t('c.route_hub')) + ' · ' + (r.name || r.id);
        var title = r.kind === 'hub' ? t('c.ready_nodes', { r: r.ready_nodes || 0, n: r.nodes || 0 }) : t('c.inflight') + ': ' + r.inflight;
        return h('span', { title: title, style: { marginRight: '6px' } }, UI.badge(label, r.ready ? (r.kind === 'node' ? 'ok' : 'primary') : '', true));
      }
      function modelsPage(el, sh) {
        var box = h('div');
        UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { load(); }, 'sm'));
        UI.mount(el, h('div.stack', h('div.callout', UI.icon('info'), ' ', t('c.route_note')), UI.card(t('c.models'), box, null, true)));
        function load() {
          return UI.rpc('/proxy/models').then(function (ms) {
            UI.mount(box, UI.table([
              { label: t('c.model'), render: function (m) { return h('b', m.name); } },
              { label: t('c.api_fmt'), render: function (m) { return apiBadge(m.api); } },
              { label: t('c.state'), render: function (m) { return UI.badge(m.ready ? t('c.ready') : t('c.not_ready'), m.ready ? 'ok' : 'danger', true); } },
              { label: t('c.routes'), render: function (m) { return h('div', m.routes.map(routeBadge)); } }
            ], ms, { empty: t('c.no_models') }));
          }).catch(UI.fail);
        }
        load();
        UI.every(5000, load);
      }


      // ---- Nodes (read only) / 节点（只读） ----
      function gpuSummary(hw) {
        var names = {};
        ((hw && hw.gpus) || []).forEach(function (g) { names[g.name] = (names[g.name] || 0) + 1; });
        return Object.keys(names).map(function (k) { return names[k] + '× ' + k; }).join(', ');
      }
      function modelBadges(n) {
        var ms = n.models || [];
        var out = ms.map(function (m) {
          var label = m.name + (m.inflight ? ' ×' + m.inflight : '');
          return h('span', { title: m.api + ' · ' + m.state, style: { marginRight: '4px' } }, UI.badge(label, m.state === 'ready' ? 'ok' : m.state === 'error' ? 'danger' : ''));
        });
        if (n.models_total > ms.length) out.push(h('span.small.muted', t('c.more_models', { n: n.models_total - ms.length })));
        return out.length ? h('div', out) : '—';
      }
      function linkCell(n) {
        var ls = n.links || [];
        if (!ls.length) return '—';
        var rtt = null, inb = 0, out = 0;
        ls.forEach(function (l) {
          var k = l.link || {};
          inb += k.bytes_in || 0; out += k.bytes_out || 0;
          if (k.rtt && k.rtt.samples && (!rtt || k.rtt.samples > rtt.samples)) rtt = k.rtt;
        });
        return h('div', LinkMap.rttBadge(rtt), h('div.small.muted', '↑ ' + LinkMap.bytes(out) + ' ↓ ' + LinkMap.bytes(inb)));
      }
      function nodeDetails(n, via) {
        var hw = n.hw || {};
        UI.modal({
          title: n.name || n.id, wide: true,
          body: h('div.stack',
            UI.kv([['ID', h('code', n.id)], [t('c.desc'), n.description], [t('c.status'), n.online ? UI.onlineBadge(true) : h('span', UI.onlineBadge(false), ' ', UI.ago(n.last_seen))],
              [t('c.host'), hw.hostname], [t('c.os'), [hw.os, hw.platform, hw.arch].filter(Boolean).join(' · ')],
              [t('c.cpu'), hw.cpu_model ? hw.cpu_model + ' · ' + t('c.cores', { n: hw.cpu_cores }) + ' · ' + t('c.load1', { n: UI.fmtNum(hw.load1, 2) }) : ''],
              [t('c.mem'), hw.mem_total_mb ? UI.fmtMB(hw.mem_total_mb - hw.mem_available_mb) + ' / ' + UI.fmtMB(hw.mem_total_mb) : ''],
              [t('c.gpus'), gpuSummary(hw)], [t('c.uptime'), UI.fmtDur(hw.uptime_seconds)],
              [t('c.st_inflight'), String(n.inflight || 0)], [t('c.link'), linkCell(n)],
              [via ? t('c.traffic_via') : t('c.calls'), t('lm.calls_v', { c: (n.traffic || {}).calls || 0, e: (n.traffic || {}).errors || 0, i: (n.traffic || {}).inflight || 0 }) +
                ' · ↑ ' + LinkMap.bytes((n.traffic || {}).req_bytes) + ' ↓ ' + LinkMap.bytes((n.traffic || {}).resp_bytes)]]),
            UI.table([
              { label: t('c.model'), render: function (m) { return h('div', h('b', m.name), m.description ? h('div.small.muted', m.description) : null); } },
              { label: t('c.api_fmt'), render: function (m) { return apiBadge(m.api); } },
              { label: t('c.state'), render: function (m) { return h('div', UI.badge(m.state, m.state === 'ready' ? 'ok' : m.state === 'error' ? 'danger' : ''), m.error ? h('div.small.muted', m.error) : null); } },
              { label: t('c.inflight'), render: function (m) { return (m.inflight || 0) + (m.max_inflight ? ' / ' + m.max_inflight : ''); } }
            ], n.models || [], { empty: t('none') }))
        });
      }
      function nodeTable(nodes, via) {
        return UI.table([
          { label: t('c.node'), render: function (n) { return h('div', h('b', n.name || n.id), n.description ? h('div.small.muted', n.description) : h('div.small.muted.mono', n.id)); } },
          { label: t('c.status'), render: function (n) { return n.online ? UI.onlineBadge(true) : h('span', UI.onlineBadge(false), ' ', h('span.small.muted', UI.ago(n.last_seen))); } },
          { label: t('c.hw'), render: function (n) {
            var hw = n.hw || {};
            return h('div.small', h('div', [hw.os, hw.arch].filter(Boolean).join('/') + (hw.cpu_cores ? ' · ' + t('c.cores', { n: hw.cpu_cores }) : '') + (hw.mem_total_mb ? ' · ' + UI.fmtMB(hw.mem_total_mb) : '')),
              gpuSummary(hw) ? h('div.muted', gpuSummary(hw)) : null);
          } },
          { label: t('c.models'), render: modelBadges },
          { label: t('c.load'), render: function (n) {
            return h('div.small', h('div', t('c.calls_n', { n: n.inflight || 0 }) + (n.jobs ? ' · ' + t('c.jobs', { n: n.jobs }) : '')),
              n.hw && n.hw.cpu_cores ? h('div.muted', t('c.load1', { n: UI.fmtNum(n.hw.load1, 2) })) : null);
          } },
          { label: t('c.link'), render: linkCell }
        ], nodes, { empty: t('c.no_nodes'), onRowClick: function (n) { nodeDetails(n, via); } });
      }
      function nodesPage(el, sh) {
        var box = h('div');
        UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { load(); }, 'sm'));
        UI.mount(el, h('div.stack', h('div.callout', UI.icon('info'), ' ', t('c.nodes_note')), box));
        function load() {
          return UI.rpc('/proxy/node/list').then(function (r) {
            var cards = (r.hubs || []).map(function (hb) {
              var head = h('div.row', hb.online ? UI.onlineBadge(true) : UI.onlineBadge(false),
                hb.total > (hb.nodes || []).length ? h('span.small.muted', t('c.more_nodes', { n: (hb.nodes || []).length, t: hb.total })) : null);
              return UI.card(t('c.hub_of', { n: hb.hub.name || hb.hub.id }), nodeTable(hb.nodes || [], true), head, true);
            });
            if ((r.direct || []).length) cards.push(UI.card(t('c.direct'), nodeTable(r.direct, false), null, true));
            UI.mount(box, cards.length ? h('div.stack', cards) : h('div.card', UI.empty(t('c.no_hubs'))));
          }).catch(UI.fail);
        }
        load();
        UI.every(5000, function () { if (!document.querySelector('.modal')) load(); });
      }

      // ---- Traffic / 流量 ----
      // The proxy's link map: agents → this proxy → its hubs and direct nodes → the hubs' nodes → their models. The
      // lines from this proxy are its own links and calls; the hub to node lines are what the hub reports.
      // proxy 的链路图：agent → 本 proxy → 其 hub 与直连节点 → hub 的节点 → 节点的模型。从本 proxy 出发的线是它自己的连接与
      // 调用；hub 到节点的线来自 hub 的上报。
      function proxyGraph(st, nodes) {
        var g = LinkMap.graph([t('c.lm_proxy'), t('c.lm_hubs'), t('c.lm_nodes'), t('c.lm_models')]);
        g.at = st.at;
        var self = st.self || {}, peers = LinkMap.peers(st.links), byTo = LinkMap.sum(st.routes, function (r) { return r.to; });
        var total = LinkMap.sum(st.routes, function () { return 'all'; }).all || {};
        g.node('me', 0, { kind: 'proxy', label: self.name || self.id, sub: t('c.calls_n', { n: total.calls || 0 }), self: true, state: 'ok',
          facts: [['ID', self.id], [t('c.host'), self.hostname]] });
        function modelsOf(nid, n) {
          (n.models || []).forEach(function (m) {
            var mid = 'm:' + m.api + '/' + m.name;
            g.node(mid, 3, { kind: 'model', label: m.name, sub: m.api, state: m.state === 'ready' ? 'ok' : m.state === 'error' ? 'bad' : 'wait' });
            g.link(nid, mid, { inflight: m.inflight || 0, facts: [[t('c.state'), m.state]] });
          });
        }
        function nodeBox(n, via) {
          var nid = 'n:' + n.id, hw = n.hw || {};
          g.node(nid, 2, { kind: 'node', label: n.name || n.id, sub: [hw.os, gpuSummary(hw)].filter(Boolean).join(' · '),
            state: n.online ? 'ok' : 'bad', facts: [['ID', n.id], [t('c.desc'), n.description], [t('c.host'), hw.hostname],
              [t('c.st_inflight'), String(n.inflight || 0)], via ? [t('c.hub_of', { n: via }), ''] : null] });
          return nid;
        }
        (nodes.hubs || []).forEach(function (hb) {
          var id = hb.hub.id, p = peers[id] || { in: 0, out: 0 }, tr = byTo[id] || {};
          g.node('h:' + id, 1, { kind: 'hub', label: hb.hub.name || id, sub: LinkMap.rttText(p.rtt), state: hb.online ? 'ok' : 'bad',
            facts: [['ID', id], [t('c.host'), hb.hub.hostname], [t('c.nodes'), String(hb.total || 0)]] });
          g.link('me', 'h:' + id, { fwd: p.out, back: p.in, rtt: p.rtt, calls: tr.calls || 0, errors: tr.errors || 0, inflight: tr.inflight || 0 });
          (hb.nodes || []).forEach(function (n) {
            var nid = nodeBox(n, hb.hub.name || id), fwd = 0, back = 0, rtt = null;
            (n.links || []).forEach(function (l) {
              var k = l.link || {};
              fwd += k.bytes_out || 0; back += k.bytes_in || 0;
              if (k.rtt && k.rtt.samples && (!rtt || k.rtt.samples > rtt.samples)) rtt = k.rtt;
            });
            var tf = n.traffic || {};
            g.link('h:' + id, nid, { fwd: fwd, back: back, rtt: rtt, calls: tf.calls || 0, errors: tf.errors || 0, inflight: tf.inflight || 0 });
            modelsOf(nid, n);
          });
        });
        (nodes.direct || []).forEach(function (n) {
          var nid = nodeBox(n), p = peers[n.id] || { in: 0, out: 0 }, tr = byTo[n.id] || {};
          g.link('me', nid, { fwd: p.out, back: p.in, rtt: p.rtt, calls: tr.calls || 0, errors: tr.errors || 0, inflight: tr.inflight || 0 });
          modelsOf(nid, n);
        });
        return g;
      }
      function trafficPage(el) {
        LinkMap.mount(el, {
          load: function () {
            return Promise.all([UI.rpc('/mesh/status'), UI.rpc('/proxy/node/list')]).then(function (r) { return proxyGraph(r[0], r[1]); });
          }
        });
      }

      // ---- API keys / API Key ----
      function keysPage(el, sh) {
        var box = h('div');
        function create() {
          var name = h('input.input', { maxlength: 64, placeholder: 'claude-code' });
          UI.modal({
            title: t('c.key_new'), body: h('div.form', UI.field(t('c.key_name'), name, t('c.key_name_hint'))),
            actions: [{ label: t('cancel') }, {
              label: t('create'), kind: 'primary', onclick: function () {
                return UI.rpc('/proxy/keys/create', { name: name.value.trim() }).then(function (k) { load(); showKey(k); });
              }
            }]
          });
        }
        function showKey(k) {
          var input = h('input.input.mono', { readonly: true, value: k.key, onfocus: function () { input.select(); } });
          UI.modal({
            title: t('c.key_created_title'), wide: true,
            body: h('div.stack', h('div.callout.callout--warn', UI.icon('alert'), ' ', t('c.key_once')), UI.field(k.name, input)),
            actions: [{ label: t('copy'), kind: 'primary', onclick: function () { UI.copy(k.key); return false; } }, { label: t('close') }]
          });
        }
        function del(k) {
          UI.confirm(t('c.key_del', { n: k.name }), t('delete'), true).then(function (ok) {
            if (ok) UI.rpc('/proxy/keys/delete', { id: k.id }).then(load).catch(UI.fail);
          });
        }
        UI.mount(sh.tools, UI.btn(t('c.key_new'), 'plus', create, 'primary sm'));
        UI.mount(el, h('div.stack', UI.card(t('c.keys'), box, null, true), h('div.muted.small', t('c.key_note'))));
        function load() {
          return UI.rpc('/proxy/keys/list').then(function (keys) {
            UI.mount(box, UI.table([
              { label: t('c.key_name'), render: function (k) { return h('b', k.name); } },
              { label: t('c.key_prefix'), render: function (k) { return h('code', k.prefix + '…'); } },
              { label: t('c.key_created'), render: function (k) { return UI.fmtTime(k.created_at); } },
              { label: t('c.key_used'), render: function (k) { return UI.ago(k.last_used_at); } },
              { label: t('c.key_calls'), render: function (k) { return String(k.calls); } },
              { label: '', cls: 'right', render: function (k) { return UI.btn(t('delete'), 'trash', function () { del(k); }, 'danger sm'); } }
            ], keys, { empty: t('c.key_none') }));
          }).catch(UI.fail);
        }
        load();
      }

      // ---- Settings / 设置 ----
      function settings(el, me) {
        var name = h('input.input', { maxlength: 64, value: (me.identity && me.identity.name) || '' });
        var b = h('button.btn.btn--primary', { type: 'submit' }, t('save'));
        var form = h('form.form', {
          onsubmit: function (e) {
            e.preventDefault();
            UI.busy(b, UI.rpc('/proxy/profile/set', { name: name.value.trim() })).then(function () {
              if (me.identity) me.identity.name = name.value.trim();
              UI.toast(t('saved'));
            }).catch(UI.fail);
          }
        }, UI.field(t('c.name'), name, t('c.name_hint')), h('div.btn-row', b));
        var api = Settings.card({
          title: t('c.api_title'), get: '/proxy/api/get', set: '/proxy/api/set',
          fields: [
            { key: 'address', kind: 'text', label: t('c.api_addr'), hint: t('c.api_addr_hint'), placeholder: '127.0.0.1:9301' },
            { key: 'cert_file', kind: 'text', label: t('c.api_cert'), hint: t('c.api_cert_hint'), placeholder: '' },
            { key: 'key_file', kind: 'text', label: t('c.api_key'), hint: t('c.api_key_hint'), placeholder: '' },
            { key: 'max_body_bytes', kind: 'int', label: t('c.api_body'), hint: t('c.api_body_hint'), placeholder: '33554432' },
            { key: 'max_conns', kind: 'int', label: t('c.api_conns'), hint: t('c.api_conns_hint'), placeholder: '256' },
            { key: 'max_inflight', kind: 'int', label: t('c.api_inflight'), hint: t('c.api_inflight_hint'), placeholder: '256' },
            { key: 'first_byte_timeout', kind: 'dur', label: t('c.api_first'), hint: t('c.api_first_hint'), placeholder: '5m' },
            { key: 'timeout', kind: 'dur', label: t('c.api_total'), hint: t('c.api_total_hint'), placeholder: '30m' }
          ],
          after: function (cur) {
            return cur.url ? h('div.callout', UI.icon('globe'), ' ', t('c.api_now', { u: cur.url })) : h('div.callout.callout--warn', UI.icon('alert'), ' ', t('c.api_none'));
          },
          onSaved: function (r) { me.api_url = r.url || ''; },
          remove: { label: t('c.api_off'), confirm: t('c.api_off_confirm'), req: { address: '' } }
        });
        UI.mount(el, h('div.stack', UI.configNote('proxy'), h('div.grid.grid--2', UI.card(t('c.profile'), form), Settings.limits()), api));
      }

      return UI.me().then(function (me) {
        return {
          fallback: 'overview',
          routes: {
            overview: function () { overview(sh.page('overview', t('c.overview')), me); },
            models: function () { modelsPage(sh.page('models', t('c.models')), sh); },
            nodes: function () { nodesPage(sh.page('nodes', t('c.nodes')), sh); },
            traffic: function () { trafficPage(sh.page('traffic', t('c.traffic'))); },
            keys: function () { keysPage(sh.page('keys', t('c.keys')), sh); },
            peers: function () { PeersPanel.mount(sh.page('peers', t('c.peers'))); },
            settings: function () { settings(sh.page('settings', t('c.settings')), me); }
          }
        };
      });
    }
  });
})();
