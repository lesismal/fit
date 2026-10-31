// The hub console: overview, nodes (with management of the nodes that allow it), models, clients, connections and
// settings. Needs ui.js, peers.js and jobs.js.
//
// hub 控制台：概览、节点（含对允许代管的节点的管理）、模型、客户端、连接与设置。依赖 ui.js、peers.js 与 jobs.js。
(function () {
  'use strict';
  var h = UI.h, t = UI.t;

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['app.sub', 'Model hub', '模型汇聚'],
    ['nav.overview', 'Overview', '概览'], ['nav.nodes', 'Nodes', '节点'], ['nav.models', 'Models', '模型'],
    ['nav.clients', 'Clients', '客户端'], ['nav.peers', 'Connections', '连接'], ['nav.settings', 'Settings', '设置'],
    ['nodes.online', 'Nodes online', '在线节点'], ['nodes.total', '{n} known', '共 {n} 个'],
    ['clients.count', 'Clients', '客户端'], ['models.count', 'Models', '模型'], ['models.ready', '{n} ready', '{n} 个就绪'],
    ['inflight', 'In flight', '在途'], ['name', 'Name', '名称'], ['host', 'Host', '主机'], ['status', 'Status', '状态'],
    ['gpus', 'GPUs', '显卡'], ['models', 'Models', '模型'], ['api', 'API', '接口'], ['nodes', 'Nodes', '节点'],
    ['ready', 'Ready', '就绪'], ['last_seen', 'Last seen', '最后在线'], ['managed', 'Managed', '可代管'],
    ['version', 'Version', '版本'], ['remote', 'Remote', '远端地址'], ['connected', 'Connected', '连接时间'],
    ['direction', 'Direction', '方向'], ['id', 'ID', 'ID'], ['jobs', 'Jobs', '作业'],
    ['no.nodes', 'No nodes yet: let nodes dial this hub, or add a dial target on the Connections page.', '暂无节点：让节点连接本 hub，或在“连接”页添加连接目标。'],
    ['no.models', 'No models: the online nodes serve none.', '暂无模型：在线节点没有提供模型。'],
    ['no.clients', 'No client is connected.', '没有已连接的客户端。'],
    ['node.back', 'All nodes', '全部节点'], ['node.notfound', 'Node not found', '未找到节点'],
    ['tab.info', 'Details', '详情'], ['tab.models', 'Models', '模型管理'], ['tab.gpu', 'GPU', '显卡'],
    ['tab.actions', 'Actions', '操作'], ['tab.jobs', 'Jobs', '作业'], ['tab.profile', 'Profile', '资料'],
    ['manage.off', 'This node does not allow hub management. Enable “allow hub management” on the node\'s own console to manage it here.',
      '该节点未允许 hub 代管。在节点自身控制台中开启“允许 hub 代管”后即可在此管理。'],
    ['manage.offline', 'The node is offline; management needs it online.', '节点离线；代管需要节点在线。'],
    ['hw.system', 'System', '系统'], ['hw.os', 'OS', '操作系统'], ['hw.kernel', 'Kernel', '内核'], ['hw.arch', 'Arch', '架构'],
    ['hw.cpu', 'CPU', 'CPU'], ['hw.load', 'Load (1m)', '负载（1 分钟）'], ['hw.mem', 'Memory', '内存'], ['hw.uptime', 'Uptime', '运行时间'],
    ['hw.ips', 'IPs', 'IP'], ['hw.disks', 'Disks', '磁盘'], ['hw.mount', 'Mount', '挂载点'], ['hw.free', 'Free', '可用'], ['hw.total', 'Total', '总量'],
    ['gpu.mem', 'Memory', '显存'], ['gpu.util', 'Util', '利用率'], ['gpu.temp', 'Temp', '温度'], ['gpu.power', 'Power', '功耗'],
    ['gpu.vendors', 'Vendors', '厂商'], ['gpu.devices', 'Devices', '设备'], ['gpu.detected', 'Detected', '检测到'],
    ['gpu.tool', 'Tool', '管理软件'], ['gpu.installed', 'Installed', '已安装'], ['gpu.missing', 'Not installed', '未安装'],
    ['gpu.install', 'Install', '安装'], ['gpu.set', 'Apply setting', '应用设置'], ['gpu.setting', 'Setting', '设置项'],
    ['gpu.index', 'GPU index (-1 = all)', 'GPU 序号（-1 = 全部）'], ['gpu.value', 'Value', '值'], ['gpu.apply', 'Apply', '应用'],
    ['gpu.errors', 'Errors', '错误'],
    ['m.add', 'Add model', '添加模型'], ['m.edit', 'Edit model', '编辑模型'], ['m.base', 'Base URL', 'Base URL'],
    ['m.upstream', 'Upstream model id', '上游模型 ID'], ['m.key', 'API key', 'API Key'], ['m.key.hint', 'Empty keeps the stored key.', '留空保留已保存的 Key。'],
    ['m.max', 'Max in flight (0 = no limit)', '并发上限（0 = 不限）'], ['m.desc', 'Description', '描述'], ['m.disabled', 'Disabled', '停用'],
    ['m.check', 'Check', '检查'], ['m.delete', 'Delete', '删除'], ['m.delete.q', 'Delete model {name}?', '删除模型 {name}？'],
    ['m.saved', 'Saved', '已保存'], ['m.checked', '{name}: {state}', '{name}：{state}'],
    ['a.run', 'Run', '运行'], ['a.none', 'No actions on this node.', '该节点没有可用操作。'],
    ['p.name', 'Display name', '显示名称'], ['p.save', 'Save', '保存'], ['p.desc', 'Description', '描述'],
    ['set.profile', 'Hub', 'Hub'], ['set.saved', 'Name saved; peers see it at their next connection.', '名称已保存；对端在下次连接时看到新名称。'],
    ['yes', 'Yes', '是'], ['no', 'No', '否']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh });

  var nodeNames = {}; // id → name, for the model tables / 供模型表使用

  function nodeLabel(id) { return nodeNames[id] || id; }
  function ellipsisId(id) { return h('span.muted.small', id); }

  // ---- Overview / 概览 ----
  function overview(el) {
    var stats = h('div.grid.grid--4'), models = h('div');
    UI.mount(el, h('div.stack', stats, UI.card(t('models'), models, null, true)));
    function load() {
      Promise.all([UI.rpc('/hub/nodes'), UI.rpc('/mesh/peers'), UI.rpc('/hub/models')]).then(function (r) {
        var nodes = r[0] || [], peers = r[1], hm = r[2] || {};
        nodes.forEach(function (n) { nodeNames[n.info.id] = n.info.name || n.info.id; });
        var online = nodes.filter(function (n) { return n.online; });
        var clients = distinct((peers.conns || []).filter(function (c) { return c.role === 'client'; }));
        var ms = hm.models || [];
        var ready = ms.filter(function (m) { return m.ready > 0; }).length;
        var inflight = online.reduce(function (s, n) { return s + (n.info.inflight || 0); }, 0);
        UI.mount(stats,
          UI.stat('server', t('nodes.online'), online.length + ' / ' + nodes.length, t('nodes.total', { n: nodes.length }), online.length ? 'ok' : ''),
          UI.stat('users', t('clients.count'), clients.length, null, 'info'),
          UI.stat('cube', t('models.count'), ms.length, t('models.ready', { n: ready }), ready ? 'ok' : 'warn'),
          UI.stat('activity', t('inflight'), inflight));
        UI.mount(models, modelTable(ms));
      }).catch(UI.fail);
    }
    load();
    UI.every(5000, load);
  }

  function distinct(conns) {
    var seen = {};
    return conns.filter(function (c) { if (seen[c.id]) return false; seen[c.id] = true; return true; });
  }

  // ---- Nodes / 节点 ----
  function gpuSummary(hw) {
    var g = (hw && hw.gpus) || [];
    if (!g.length) return '—';
    var names = {};
    g.forEach(function (x) { names[x.name] = (names[x.name] || 0) + 1; });
    return Object.keys(names).map(function (k) { return names[k] + '× ' + k; }).join(', ');
  }

  function nodesPage(el) {
    var box = h('div');
    UI.mount(el, UI.card(t('nav.nodes'), box, null, true));
    function load() {
      UI.rpc('/hub/nodes').then(function (nodes) {
        nodes = nodes || [];
        nodes.forEach(function (n) { nodeNames[n.info.id] = n.info.name || n.info.id; });
        UI.mount(box, UI.table([
          { label: t('name'), render: function (n) { return h('div', h('b', n.info.name || n.info.id), h('div', ellipsisId(n.info.id))); } },
          { label: t('host'), render: function (n) { return n.info.hw && n.info.hw.hostname || '—'; } },
          { label: t('status'), render: function (n) { return n.online ? UI.onlineBadge(true) : h('span', UI.onlineBadge(false), ' ', h('span.muted.small', UI.ago(n.last_seen))); } },
          { label: t('models'), render: function (n) {
            var ms = n.info.models || [];
            return ms.length ? ms.map(function (m) { return UI.badge(m.name, m.state === 'ready' ? 'ok' : m.state === 'error' ? 'danger' : ''); }) : '—';
          } },
          { label: t('inflight'), render: function (n) { return n.info.inflight || 0; } },
          { label: t('gpus'), render: function (n) { return gpuSummary(n.info.hw); } },
          { label: t('managed'), render: function (n) { return n.info.hub_manage ? UI.badge(t('managed'), 'primary') : ''; } }
        ], nodes, { empty: t('no.nodes'), onRowClick: function (n) { location.hash = '#/nodes/' + encodeURIComponent(n.info.id); } }));
      }).catch(UI.fail);
    }
    load();
    UI.every(5000, load);
  }

  // mgr returns the call / stream functions that route management methods to node id (MethodHubNodeCall / Stream).
  // mgr 返回把管理方法路由到节点 id 的 call / stream 函数（MethodHubNodeCall / Stream）。
  function mgr(id) {
    return {
      call: function (method, req) { return UI.rpc('/hub/node/call', { id: id, method: method, req: req || {} }); },
      stream: function (method, req, onData, signal) {
        return UI.rpcStream('/hub/node/stream', { id: id, method: method, req: req || {} }, onData, signal);
      }
    };
  }

  function nodePage(sh, id) {
    var el = sh.page('nodes', t('nav.nodes'), id);
    UI.mount(sh.tools, UI.btn(t('node.back'), null, function () { location.hash = '#/nodes'; }, 'ghost sm'));
    var tab = 'info', node = null;
    var head = h('div'), tabsEl = h('div'), body = h('div');
    UI.mount(el, h('div.stack', head, tabsEl, body));
    function render() {
      var n = node, info = n.info;
      UI.mount(head, h('div.row',
        h('h2', { style: { margin: 0 } }, info.name || info.id), UI.onlineBadge(n.online),
        info.hub_manage ? UI.badge(t('managed'), 'primary') : null, h('span.muted.small', info.id),
        info.description ? h('span.muted', info.description) : null));
      var items = [['info', t('tab.info')]];
      if (info.hub_manage) items = items.concat([['models', t('tab.models')], ['gpu', t('tab.gpu')], ['actions', t('tab.actions')], ['jobs', t('tab.jobs')], ['profile', t('tab.profile')]]);
      if (!items.some(function (x) { return x[0] === tab; })) tab = 'info';
      UI.mount(tabsEl, UI.tabs(items, tab, function (k) { tab = k; render(); show(); }));
    }
    function show() {
      var n = node, m = mgr(id);
      if (tab === 'info') return UI.mount(body, nodeInfo(n));
      if (!n.online) return UI.mount(body, h('div.callout.callout--warn', UI.icon('alert'), ' ', t('manage.offline')));
      if (tab === 'models') return manageModels(body, m);
      if (tab === 'gpu') return manageGPU(body, m);
      if (tab === 'actions') return manageActions(body, m);
      if (tab === 'jobs') return Jobs.mount(body, m);
      if (tab === 'profile') return manageProfile(body, m, n.info, load);
    }
    function load() {
      return UI.rpc('/hub/nodes').then(function (nodes) {
        var n = (nodes || []).filter(function (x) { return x.info.id === id; })[0];
        if (!n) { UI.mount(body, UI.empty(t('node.notfound'))); return; }
        var first = !node;
        node = n;
        render();
        if (first || tab === 'info') show();
      }).catch(UI.fail);
    }
    load();
    UI.every(5000, function () { if (tab === 'info') load(); });
  }

  function nodeInfo(n) {
    var info = n.info, hw = info.hw || {};
    var models = UI.table([
      { label: t('name'), render: function (m) { return h('b', m.name); } },
      { label: t('api'), render: function (m) { return m.api; } },
      { label: t('status'), render: function (m) { return h('span', UI.badge(m.state, m.state === 'ready' ? 'ok' : m.state === 'error' ? 'danger' : ''), m.error ? h('div.small.muted', m.error) : null); } },
      { label: t('inflight'), render: function (m) { return (m.inflight || 0) + (m.max_inflight ? ' / ' + m.max_inflight : ''); } },
      { label: t('m.desc'), render: function (m) { return m.description || ''; } }
    ], info.models || []);
    var gpus = UI.table([
      { label: '#', render: function (g) { return g.index; } },
      { label: t('name'), render: function (g) { return h('span', h('b', g.name), ' ', h('span.muted.small', g.vendor)); } },
      { label: t('gpu.mem'), render: function (g) { return UI.fmtMB(g.memory_used_mb) + ' / ' + UI.fmtMB(g.memory_mb); } },
      { label: t('gpu.util'), render: function (g) { return UI.fmtNum(g.utilization, 0, '%'); } },
      { label: t('gpu.temp'), render: function (g) { return UI.fmtNum(g.temperature, 0, '°C'); } },
      { label: t('gpu.power'), render: function (g) { return g.power_draw ? UI.fmtNum(g.power_draw, 0, 'W') + (g.power_limit ? ' / ' + UI.fmtNum(g.power_limit, 0, 'W') : '') : '—'; } }
    ], hw.gpus || []);
    var disks = UI.table([
      { label: t('hw.mount'), render: function (d) { return d.mount; } },
      { label: t('hw.free'), render: function (d) { return UI.fmtMB(d.free_mb); } },
      { label: t('hw.total'), render: function (d) { return UI.fmtMB(d.total_mb); } }
    ], hw.disks || []);
    var conns = UI.table([
      { label: t('direction'), render: function (c) { return c.direction; } },
      { label: t('remote'), render: function (c) { return c.remote; } },
      { label: t('version'), render: function (c) { return c.version; } },
      { label: t('connected'), render: function (c) { return UI.ago(c.connected_at); } }
    ], n.conns || []);
    return h('div.stack',
      h('div.grid.grid--2',
        UI.card(t('hw.system'), UI.kv([
          [t('host'), hw.hostname], [t('hw.os'), [hw.os, hw.platform].filter(Boolean).join(' · ')], [t('hw.kernel'), hw.kernel],
          [t('hw.arch'), hw.arch], [t('hw.cpu'), hw.cpu_model ? hw.cpu_model + ' × ' + hw.cpu_cores : ''],
          [t('hw.load'), hw.load1 != null ? hw.load1.toFixed(2) : ''],
          [t('hw.mem'), hw.mem_total_mb ? UI.fmtMB(hw.mem_total_mb - hw.mem_available_mb) + ' / ' + UI.fmtMB(hw.mem_total_mb) : ''],
          [t('hw.uptime'), UI.fmtDur(hw.uptime_seconds)], [t('hw.ips'), (hw.ips || []).join(', ')],
          [t('inflight'), info.inflight || 0], [t('jobs'), info.jobs || 0],
          [t('last_seen'), n.online ? t('online') : UI.ago(n.last_seen)]
        ])),
        UI.card(t('nav.peers'), conns, null, true)),
      UI.card(t('models'), models, null, true),
      UI.card(t('gpus'), gpus, null, true),
      UI.card(t('hw.disks'), disks, null, true));
  }

  // ---- Managed node: models / 代管节点：模型 ----
  function manageModels(el, m) {
    var box = h('div');
    UI.mount(el, UI.card(t('tab.models'), box, UI.btn(t('m.add'), 'plus', function () { edit(null); }, 'primary sm'), true));
    function load() {
      m.call('/node/model/list').then(function (list) {
        UI.mount(box, UI.table([
          { label: t('name'), render: function (x) { return h('div', h('b', x.name), x.disabled ? h('span', ' ', UI.badge(t('m.disabled'))) : null, x.description ? h('div.small.muted', x.description) : null); } },
          { label: t('api'), render: function (x) { return x.api; } },
          { label: t('m.base'), render: function (x) { return h('span.small', x.base_url); } },
          { label: t('m.upstream'), render: function (x) { return x.upstream || '—'; } },
          { label: t('m.key'), render: function (x) { return x.api_key || '—'; } },
          { label: t('inflight'), render: function (x) { return x.max_inflight || '∞'; } },
          { label: '', render: function (x) {
            var c = UI.btn(t('m.check'), 'activity', function (e) {
              e.stopPropagation();
              UI.busy(c, m.call('/node/model/check', { name: x.name })).then(function (v) {
                UI.toast(t('m.checked', { name: x.name, state: v.state + (v.error ? ' — ' + v.error : '') }), v.state === 'ready' ? 'info' : 'error');
              }).catch(UI.fail);
            }, 'ghost sm');
            return h('div.btn-row', c,
              UI.btn('', 'edit', function (e) { e.stopPropagation(); edit(x); }, 'ghost sm icon'),
              UI.btn('', 'trash', function (e) {
                e.stopPropagation();
                UI.confirm(t('m.delete.q', { name: x.name }), t('m.delete'), true).then(function (ok) {
                  if (ok) m.call('/node/model/delete', { name: x.name }).then(load).catch(UI.fail);
                });
              }, 'ghost sm icon'));
          } }
        ], list || []));
      }).catch(function (e) { UI.mount(box, h('div.callout.callout--danger', e.message)); });
    }
    function edit(x) {
      x = x || { api: 'openai' };
      var f = {
        name: h('input.input', { value: x.name || '', disabled: !!x.name }),
        api: h('select.select', ['openai', 'anthropic'].map(function (a) { return h('option', { value: a, selected: a === x.api }, a); })),
        base: h('input.input', { value: x.base_url || '', placeholder: 'http://127.0.0.1:11434/v1' }),
        upstream: h('input.input', { value: x.upstream || '' }),
        key: h('input.input', { type: 'password', autocomplete: 'new-password', placeholder: x.api_key || '' }),
        max: h('input.input', { type: 'number', min: 0, value: x.max_inflight || 0 }),
        desc: h('input.input', { value: x.description || '' }),
        disabled: h('input', { type: 'checkbox', checked: !!x.disabled })
      };
      UI.modal({
        title: x.name ? t('m.edit') : t('m.add'), wide: true,
        body: h('div.form',
          h('div.form-row', UI.field(t('name'), f.name), UI.field(t('api'), f.api)),
          UI.field(t('m.base'), f.base), UI.field(t('m.upstream'), f.upstream),
          UI.field(t('m.key'), f.key, x.name ? t('m.key.hint') : null),
          h('div.form-row', UI.field(t('m.max'), f.max), UI.field(t('m.desc'), f.desc)),
          h('label.check', f.disabled, ' ', t('m.disabled'))),
        actions: [{ label: t('cancel') }, {
          label: t('p.save'), kind: 'primary', onclick: function () {
            return m.call('/node/model/save', {
              name: f.name.value.trim(), api: f.api.value, base_url: f.base.value.trim(), upstream: f.upstream.value.trim(),
              api_key: f.key.value, max_inflight: +f.max.value || 0, description: f.desc.value, disabled: f.disabled.checked
            }).then(function () { UI.toast(t('m.saved')); load(); }).catch(function (e) { UI.fail(e); return false; });
          }
        }]
      });
    }
    load();
  }

  // ---- Managed node: GPU / 代管节点：显卡 ----
  function manageGPU(el, m) {
    function follow(p, btn) { UI.busy(btn, p).then(function (job) { Jobs.follow(job, m, load); }).catch(UI.fail); }
    function load() {
      Promise.all([m.call('/node/gpu/status'), m.call('/node/gpu/catalog')]).then(function (r) {
        var st = r[0] || {}, catalog = r[1] || [];
        var devices = UI.table([
          { label: '#', render: function (d) { return d.index; } },
          { label: t('name'), render: function (d) { return h('span', h('b', d.name), ' ', h('span.muted.small', d.vendor)); } },
          { label: t('gpu.mem'), render: function (d) { return UI.fmtMB(d.mem_used_mb) + ' / ' + UI.fmtMB(d.mem_total_mb); } },
          { label: t('gpu.util'), render: function (d) { return UI.fmtNum(d.utilization, 0, '%'); } },
          { label: t('gpu.temp'), render: function (d) { return UI.fmtNum(d.temperature, 0, '°C'); } },
          { label: t('gpu.power'), render: function (d) { return UI.fmtNum(d.power_draw, 0, 'W') + ' / ' + UI.fmtNum(d.power_limit, 0, 'W'); } }
        ], st.devices || []);
        var vendors = UI.table([
          { label: t('name'), render: function (v) { return h('b', v.name); } },
          { label: t('gpu.detected'), render: function (v) { return v.detected; } },
          { label: t('gpu.tool'), render: function (v) { return h('span', v.tool, ' ', v.installed ? UI.badge(t('gpu.installed'), 'ok') : UI.badge(t('gpu.missing'))); } },
          { label: '', render: function (v) {
            if (!v.install_for) return '';
            var b = UI.btn(t('gpu.install'), 'download', function () {
              UI.confirm(t('gpu.install') + ' ' + v.name + ' (' + v.install_for + ')?').then(function (ok) {
                if (ok) follow(m.call('/node/gpu/install', { vendor: v.id }), b);
              });
            }, 'sm');
            return b;
          } }
        ], (st.vendors || []).filter(function (v) { return v.detected > 0 || v.installed; }));
        var errs = Object.keys(st.errors || {}).map(function (k) { return h('div.callout.callout--warn', k + ': ' + st.errors[k]); });
        var raws = Object.keys(st.raw || {}).map(function (k) { return UI.card(k, h('pre.console', st.raw[k])); });
        UI.mount(el, h('div.stack', errs, UI.card(t('gpu.devices'), devices, UI.btn('', 'refresh', load, 'ghost sm icon'), true), raws,
          UI.card(t('gpu.vendors'), vendors, null, true), setForm(catalog, st)));
      }).catch(function (e) { UI.mount(el, h('div.callout.callout--danger', e.message)); });
    }
    function setForm(catalog, st) {
      var installed = (st.vendors || []).filter(function (v) { return v.installed && (v.settings || []).length; });
      if (!installed.length) return null;
      var vendor = h('select.select', installed.map(function (v) { return h('option', { value: v.id }, v.name); }));
      var key = h('select.select'), index = h('input.input', { type: 'number', value: -1, min: -1 }), valueBox = h('span');
      var note = h('div.small.muted'), value = null;
      function settings() { var v = installed.filter(function (x) { return x.id === vendor.value; })[0]; return (v && v.settings) || []; }
      function onKey() {
        var s = settings().filter(function (x) { return x.key === key.value; })[0];
        if (!s) return;
        value = s.kind === 'enum' ? h('select.select', (s.options || []).map(function (o) { return h('option', { value: o }, o); }))
          : s.kind === 'action' ? null : h('input.input', { placeholder: s.kind === 'range' ? 'min,max' : (s.unit || '') });
        UI.mount(valueBox, value ? UI.field(t('gpu.value') + (s.unit ? ' (' + s.unit + ')' : ''), value) : null);
        note.textContent = (UI.zh() ? s.note_zh : s.note) || '';
      }
      function onVendor() {
        UI.mount(key, settings().map(function (s) { return h('option', { value: s.key }, UI.zh() ? s.label_zh || s.label : s.label); }));
        onKey();
      }
      vendor.addEventListener('change', onVendor);
      key.addEventListener('change', onKey);
      onVendor();
      var b = UI.btn(t('gpu.apply'), 'zap', function () {
        follow(m.call('/node/gpu/set', { vendor: vendor.value, index: +index.value, key: key.value, value: value ? value.value : '' }), b);
      }, 'primary');
      return UI.card(t('gpu.set'), h('div.form',
        h('div.form-row', UI.field(t('gpu.vendors'), vendor), UI.field(t('gpu.setting'), key), UI.field(t('gpu.index'), index)),
        valueBox, note, h('div.btn-row', b)));
    }
    UI.mount(el, h('div.spinner'));
    load();
  }

  // ---- Managed node: actions / 代管节点：操作 ----
  function manageActions(el, m) {
    m.call('/node/action/list').then(function (list) {
      if (!list || !list.length) { UI.mount(el, UI.empty(t('a.none'))); return; }
      UI.mount(el, h('div.grid.grid--2', list.map(function (a) {
        var inputs = {};
        var params = (a.params || []).map(function (p) {
          inputs[p.key] = h('input.input', { placeholder: p.example || '' });
          return UI.field(UI.zh() ? p.label_zh || p.label : p.label, inputs[p.key]);
        });
        var b = UI.btn(t('a.run'), 'play', function () {
          var ps = {};
          Object.keys(inputs).forEach(function (k) { ps[k] = inputs[k].value.trim(); });
          UI.busy(b, m.call('/node/action/run', { id: a.id, params: ps })).then(function (job) { Jobs.follow(job, m); }).catch(UI.fail);
        }, 'primary sm');
        return UI.card(UI.zh() ? a.title_zh || a.title : a.title, h('div.form',
          (UI.zh() ? a.note_zh : a.note) ? h('div.small.muted', UI.zh() ? a.note_zh : a.note) : null,
          params, h('div.btn-row', b, h('span.muted.small', a.id))));
      })));
    }).catch(function (e) { UI.mount(el, h('div.callout.callout--danger', e.message)); });
  }

  // ---- Managed node: profile / 代管节点：资料 ----
  function manageProfile(el, m, info, reload) {
    var name = h('input.input', { value: info.name || '' }), desc = h('input.input', { value: info.description || '' });
    var b = h('button.btn.btn--primary', { type: 'submit' }, t('p.save'));
    UI.mount(el, UI.card(t('tab.profile'), h('form.form', {
      onsubmit: function (e) {
        e.preventDefault();
        UI.busy(b, m.call('/node/profile/set', { name: name.value.trim(), description: desc.value.trim() }))
          .then(function () { UI.toast(t('m.saved')); setTimeout(reload, 500); }).catch(UI.fail);
      }
    }, UI.field(t('p.name'), name), UI.field(t('p.desc'), desc), h('div.btn-row', b))));
  }

  // ---- Models / 模型 ----
  function modelTable(ms) {
    return UI.table([
      { label: t('name'), render: function (x) { return h('b', x.name); } },
      { label: t('api'), render: function (x) { return UI.badge(x.api, x.api === 'anthropic' ? 'warn' : 'info'); } },
      { label: t('ready'), render: function (x) { return UI.badge(x.ready + ' / ' + (x.nodes || []).length, x.ready ? 'ok' : 'danger', true); } },
      { label: t('nodes'), render: function (x) {
        return (x.nodes || []).map(function (id) { return h('a.badge', { href: '#/nodes/' + encodeURIComponent(id) }, nodeLabel(id)); });
      } }
    ], ms, { empty: t('no.models') });
  }

  function modelsPage(el) {
    var box = h('div');
    UI.mount(el, UI.card(t('nav.models'), box, null, true));
    function load() {
      Promise.all([UI.rpc('/hub/models'), UI.rpc('/hub/nodes')]).then(function (r) {
        (r[1] || []).forEach(function (n) { nodeNames[n.info.id] = n.info.name || n.info.id; });
        UI.mount(box, modelTable((r[0] && r[0].models) || []));
      }).catch(UI.fail);
    }
    load();
    UI.every(5000, load);
  }

  // ---- Clients / 客户端 ----
  function clientsPage(el) {
    var box = h('div');
    UI.mount(el, UI.card(t('nav.clients'), box, null, true));
    function load() {
      UI.rpc('/mesh/peers').then(function (p) {
        var cs = (p.conns || []).filter(function (c) { return c.role === 'client'; });
        UI.mount(box, UI.table([
          { label: t('name'), render: function (c) { return h('div', h('b', c.name || c.id), h('div', ellipsisId(c.id))); } },
          { label: t('host'), render: function (c) { return c.hostname || '—'; } },
          { label: t('version'), render: function (c) { return c.version || '—'; } },
          { label: t('direction'), render: function (c) { return c.direction; } },
          { label: t('remote'), render: function (c) { return c.remote; } },
          { label: t('connected'), render: function (c) { return UI.ago(c.connected_at); } },
          { label: t('status'), render: function (c) { return c.draining ? UI.badge('draining', 'warn') : UI.onlineBadge(true); } }
        ], cs, { empty: t('no.clients') }));
      }).catch(UI.fail);
    }
    load();
    UI.every(5000, load);
  }

  // ---- Settings / 设置 ----
  function settingsPage(el, me) {
    var name = h('input.input', { value: (me.identity && me.identity.name) || '' });
    var b = h('button.btn.btn--primary', { type: 'submit' }, t('p.save'));
    var id = me.identity || {};
    UI.mount(el, h('div.stack',
      UI.configNote('hub'),
      h('div.grid.grid--2',
        UI.card(t('set.profile'), h('form.form', {
          onsubmit: function (e) {
            e.preventDefault();
            UI.busy(b, UI.rpc('/hub/profile/set', { name: name.value.trim() })).then(function (self) {
              me.identity = self; UI.toast(t('set.saved'));
            }).catch(UI.fail);
          }
        }, UI.field(t('p.name'), name), UI.kv([[t('id'), id.id], [t('version'), id.version], [t('host'), id.hostname]]), h('div.btn-row', b))))));
  }

  var app = document.getElementById('app');
  var s = UI.boot(app, {
    brand: 'fit agent hub', sub: t('app.sub'),
    start: function (me) {
      var sh = UI.shell({
        brand: 'fit agent hub', sub: (me.identity && me.identity.name) || t('app.sub'), user: me.user,
        onLogout: function () { s.logout(); },
        nav: [
          { id: 'overview', icon: 'dashboard', label: t('nav.overview') },
          { id: 'nodes', icon: 'server', label: t('nav.nodes') },
          { id: 'models', icon: 'cube', label: t('nav.models') },
          { id: 'clients', icon: 'users', label: t('nav.clients') },
          { id: 'peers', icon: 'link', label: t('nav.peers') },
          { id: 'settings', icon: 'settings', label: t('nav.settings') }
        ]
      });
      UI.mount(app, sh.el);
      UI.router({
        'overview': function () { overview(sh.page('overview', t('nav.overview'))); },
        'nodes': function () { nodesPage(sh.page('nodes', t('nav.nodes'))); },
        'nodes/:id': function (p) { nodePage(sh, p.id); },
        'models': function () { modelsPage(sh.page('models', t('nav.models'))); },
        'clients': function () { clientsPage(sh.page('clients', t('nav.clients'))); },
        'peers': function () { PeersPanel.mount(sh.page('peers', t('nav.peers'))); },
        'settings': function () { settingsPage(sh.page('settings', t('nav.settings')), me); }
      }, 'overview');
    }
  });
})();
