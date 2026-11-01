// fit llm proxy console: overview (agent API + quick start + stats), models (catalog with routes), API keys,
// connections and settings. Needs ui.js and peers.js.
//
// fit llm proxy 控制台：概览（agent 接口 + 接入示例 + 统计）、模型（目录与路由）、API Key、连接与设置。依赖 ui.js 与 peers.js。
(function () {
  'use strict';
  var h = UI.h, t = UI.t;

  UI.addI18n({
    'en': {
      'c.sub': 'Model gateway for agents on this machine and LAN', 'c.overview': 'Overview', 'c.models': 'Models', 'c.keys': 'API keys',
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
      'c.key_note': 'Usage (last used, calls) is saved at most once a minute.',
      'c.profile': 'Profile', 'c.name': 'Display name', 'c.name_hint': 'How hubs and nodes see this proxy'
    },
    'zh-CN': {
      'c.sub': '本机与局域网 agent 的模型网关', 'c.overview': '概览', 'c.models': '模型', 'c.keys': 'API Key',
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
      'c.key_note': '使用情况（最近使用、调用数）最多每分钟保存一次。',
      'c.profile': '基本信息', 'c.name': '显示名称', 'c.name_hint': 'hub 与节点看到的本 proxy 名称'
    }
  });

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
    UI.mount(el, h('div.stack', UI.configNote('proxy'), UI.card(t('c.profile'), form)));
  }

  var app = document.getElementById('app');
  var brand = 'fit llm proxy';
  var s = UI.boot(app, {
    brand: brand, sub: t('c.sub'),
    start: function (me) {
      var sh = UI.shell({
        brand: brand, sub: (me.identity && me.identity.name) || t('c.sub'), user: me.user, onLogout: function () { s.logout(); },
        nav: [
          { id: 'overview', icon: 'dashboard', label: t('c.overview') },
          { id: 'models', icon: 'cube', label: t('c.models') },
          { id: 'keys', icon: 'key', label: t('c.keys') },
          { id: 'peers', icon: 'link', label: t('c.peers') },
          { id: 'settings', icon: 'settings', label: t('c.settings') }
        ]
      });
      UI.mount(app, sh.el);
      UI.router({
        overview: function () { overview(sh.page('overview', t('c.overview')), me); },
        models: function () { modelsPage(sh.page('models', t('c.models')), sh); },
        keys: function () { keysPage(sh.page('keys', t('c.keys')), sh); },
        peers: function () { PeersPanel.mount(sh.page('peers', t('c.peers'))); },
        settings: function () { settings(sh.page('settings', t('c.settings')), me); }
      }, 'overview');
    }
  });
})();
