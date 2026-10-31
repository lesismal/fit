// The node console: overview, models, GPUs, fixed actions, jobs, mesh connections and settings. Every call goes to
// the node's own /api/rpc (and /api/stream) as the local console.
//
// 节点控制台：概览、模型、显卡、固定操作、作业、互联与设置。所有调用以本地控制台身份发往节点自身的 /api/rpc（与 /api/stream）。
(function () {
  'use strict';
  var h = UI.h, t = UI.t, call = UI.rpc;

  UI.addI18n({
    'en': {
      'app.sub': 'GPU node', 'nav.overview': 'Overview', 'nav.models': 'Models', 'nav.gpus': 'GPUs', 'nav.actions': 'Actions',
      'nav.jobs': 'Jobs', 'nav.peers': 'Connections', 'nav.settings': 'Settings',
      'ov.gpus': 'GPUs', 'ov.models': 'Ready models', 'ov.inflight': 'Calls in flight', 'ov.jobs': 'Running jobs',
      'ov.profile': 'Profile', 'ov.system': 'System', 'ov.res': 'Memory & disks', 'ov.name': 'Name', 'ov.desc': 'Description',
      'ov.id': 'ID', 'ov.version': 'Version', 'ov.web': 'Web console', 'ov.rpc': 'RPC address', 'ov.started': 'Started',
      'ov.hub_manage': 'Hub management', 'ov.allowed': 'Allowed', 'ov.denied': 'Not allowed',
      'ov.hostname': 'Hostname', 'ov.os': 'OS', 'ov.kernel': 'Kernel', 'ov.arch': 'Arch', 'ov.cpu': 'CPU', 'ov.cores': '{n} cores',
      'ov.load': 'Load (1m)', 'ov.uptime': 'Uptime', 'ov.ips': 'IP addresses', 'ov.memory': 'Memory', 'ov.used_of': '{a} / {b}',
      'ov.util': 'Utilization', 'ov.vram': 'Memory',
      'm.title': 'Models', 'm.add': 'Add model', 'm.edit': 'Edit model', 'm.name': 'Name', 'm.name_hint': 'What agents ask for (no spaces)',
      'm.api': 'API format', 'm.base_url': 'Base URL', 'm.upstream': 'Upstream model id', 'm.upstream_hint': 'Sent upstream in place of the name; empty = the name',
      'm.key': 'API key', 'm.key_keep': 'Leave as is to keep the current key', 'm.max': 'Max concurrent calls', 'm.max_hint': '0 = no limit',
      'm.desc': 'Description', 'm.disabled': 'Disabled', 'm.state': 'State', 'm.inflight': 'In flight', 'm.checked': 'Checked',
      'm.check': 'Check', 'm.enable': 'Enable', 'm.disable': 'Disable', 'm.delete_confirm': 'Delete model {m} ({a})?',
      'm.none': 'No models yet. A model is an upstream API endpoint (e.g. Ollama at http://127.0.0.1:11434/v1).',
      'm.s.ready': 'Ready', 'm.s.disabled': 'Disabled', 'm.s.error': 'Error', 'm.checked_ok': 'Upstream reachable',
      'm.try': 'Test call', 'm.prompt': 'Prompt', 'm.send': 'Send', 'm.stop': 'Stop', 'm.raw': 'Raw response', 'm.no_ready': 'No model is ready',
      'g.os': 'OS family', 'g.run_as': 'Runs as', 'g.root': 'root', 'g.sudo': 'sudo -n', 'g.user': 'current user (settings may fail without root)',
      'g.no_gpu': 'No GPU was read through an installed tool.', 'g.vendors': 'GPU vendors & management tools',
      'g.detected': 'Detected: {n}', 'g.not_detected': 'Not detected', 'g.tool_ok': 'Tool installed', 'g.tool_missing': 'Tool not installed',
      'g.install': 'Install tool', 'g.install_confirm': 'Install the {v} management tool? The install script runs as a job (it may need root).',
      'g.docs': 'Docs', 'g.scripts': 'Query command / install script', 'g.raw': 'Tool output',
      'g.temp': 'Temperature', 'g.power': 'Power', 'g.clock_gfx': 'GPU clock', 'g.clock_mem': 'Memory clock', 'g.fan': 'Fan',
      'g.settings': 'Settings', 'g.value': 'Value', 'g.apply': 'Apply', 'g.all': 'All GPUs of this vendor',
      'a.title': 'Fixed actions', 'a.note': 'A node only runs the fixed actions below; parameters are validated and never pass through a shell.',
      'a.run': 'Run', 'a.none': 'No action is supported on this OS',
      's.profile': 'Profile', 's.node': 'Node settings', 's.hub_manage': 'Allow hubs to manage this node',
      's.hub_manage_hint': 'Connected hubs may then edit models, change GPU settings, run fixed actions and follow jobs',
      's.gpu_sudo': 'Use sudo -n for GPU settings, tool installs and root actions', 's.gpu_sudo_hint': 'Needs password-less sudo when the node does not run as root',
      's.refresh': 'Hardware refresh', 's.refresh_hint': 'A Go duration, 5s to 1h, e.g. 30s',
      's.job_timeout': 'Job timeout', 's.job_timeout_hint': 'A Go duration, 10s to 168h, e.g. 1h',
      's.gpu_data_dir': 'GPU driver package directory', 's.gpu_data_dir_hint': 'Optional: a directory where the vendor install scripts look for driver packages'
    },
    'zh-CN': {
      'app.sub': 'GPU 节点', 'nav.overview': '概览', 'nav.models': '模型', 'nav.gpus': '显卡', 'nav.actions': '操作',
      'nav.jobs': '作业', 'nav.peers': '连接', 'nav.settings': '设置',
      'ov.gpus': '显卡', 'ov.models': '就绪模型', 'ov.inflight': '进行中的调用', 'ov.jobs': '运行中作业',
      'ov.profile': '资料', 'ov.system': '系统', 'ov.res': '内存与磁盘', 'ov.name': '名称', 'ov.desc': '描述',
      'ov.id': 'ID', 'ov.version': '版本', 'ov.web': 'Web 控制台', 'ov.rpc': 'RPC 地址', 'ov.started': '启动时间',
      'ov.hub_manage': 'hub 代管', 'ov.allowed': '允许', 'ov.denied': '不允许',
      'ov.hostname': '主机名', 'ov.os': '操作系统', 'ov.kernel': '内核', 'ov.arch': '架构', 'ov.cpu': 'CPU', 'ov.cores': '{n} 核',
      'ov.load': '负载（1 分钟）', 'ov.uptime': '运行时长', 'ov.ips': 'IP 地址', 'ov.memory': '内存', 'ov.used_of': '{a} / {b}',
      'ov.util': '利用率', 'ov.vram': '显存',
      'm.title': '模型', 'm.add': '添加模型', 'm.edit': '编辑模型', 'm.name': '名称', 'm.name_hint': 'agent 请求时使用的名称（不含空格）',
      'm.api': '接口格式', 'm.base_url': '接口地址', 'm.upstream': '上游模型 ID', 'm.upstream_hint': '发往上游时替换名称；留空则使用名称',
      'm.key': 'API Key', 'm.key_keep': '保持不变则保留当前 Key', 'm.max': '最大并发调用', 'm.max_hint': '0 表示不限',
      'm.desc': '描述', 'm.disabled': '停用', 'm.state': '状态', 'm.inflight': '在途', 'm.checked': '检查时间',
      'm.check': '检查', 'm.enable': '启用', 'm.disable': '停用', 'm.delete_confirm': '删除模型 {m}（{a}）？',
      'm.none': '还没有模型。模型即一个上游 API 端点（例如 Ollama：http://127.0.0.1:11434/v1）。',
      'm.s.ready': '就绪', 'm.s.disabled': '已停用', 'm.s.error': '错误', 'm.checked_ok': '上游可达',
      'm.try': '测试调用', 'm.prompt': '提示词', 'm.send': '发送', 'm.stop': '停止', 'm.raw': '原始应答', 'm.no_ready': '没有就绪的模型',
      'g.os': '系统类别', 'g.run_as': '执行身份', 'g.root': 'root', 'g.sudo': 'sudo -n', 'g.user': '当前用户（非 root 时设置可能失败）',
      'g.no_gpu': '尚未通过已安装的工具读取到任何显卡。', 'g.vendors': '显卡厂商与管理软件',
      'g.detected': '检测到：{n}', 'g.not_detected': '未检测到', 'g.tool_ok': '已安装管理软件', 'g.tool_missing': '未安装管理软件',
      'g.install': '安装管理软件', 'g.install_confirm': '安装 {v} 显卡管理软件？安装脚本以作业方式执行（可能需要 root）。',
      'g.docs': '文档', 'g.scripts': '查询命令 / 安装脚本', 'g.raw': '工具输出',
      'g.temp': '温度', 'g.power': '功耗', 'g.clock_gfx': '核心频率', 'g.clock_mem': '显存频率', 'g.fan': '风扇',
      'g.settings': '设置', 'g.value': '值', 'g.apply': '应用', 'g.all': '该厂商的所有显卡',
      'a.title': '固定操作', 'a.note': '节点只执行下面的固定操作；参数逐项校验，且从不经过 shell。',
      'a.run': '执行', 'a.none': '本系统不支持任何操作',
      's.profile': '资料', 's.node': '节点设置', 's.hub_manage': '允许 hub 代管本节点',
      's.hub_manage_hint': '开启后，已连接的 hub 可以编辑模型、修改显卡设置、执行固定操作并查看作业',
      's.gpu_sudo': '显卡设置、工具安装与需要 root 的操作使用 sudo -n', 's.gpu_sudo_hint': '节点不以 root 运行时需要配置免密 sudo',
      's.refresh': '硬件刷新间隔', 's.refresh_hint': 'Go 时长格式，5s 到 1h，例如 30s',
      's.job_timeout': '作业超时', 's.job_timeout_hint': 'Go 时长格式，10s 到 168h，例如 1h',
      's.gpu_data_dir': '显卡驱动包目录', 's.gpu_data_dir_hint': '可选：厂商安装脚本在此目录中查找驱动包'
    }
  });

  function zhOr(o, k) { return UI.zh() && o[k + '_zh'] ? o[k + '_zh'] : o[k]; }
  function setLabel(s) { return UI.zh() && s.label_zh ? s.label_zh : s.label; }
  var STATE_KIND = { ready: 'ok', disabled: '', error: 'danger' };
  function stateBadge(v) { return UI.badge(t('m.s.' + v.state), STATE_KIND[v.state], v.state === 'ready'); }
  function idle(el) {
    // Skip a timed refresh while a modal is open or a field of el is being edited. / 弹窗打开或正在编辑时跳过定时刷新。
    var a = document.activeElement;
    return !document.querySelector('.modal') && !(a && el.contains(a) && /INPUT|SELECT|TEXTAREA/.test(a.tagName));
  }
  function followJob(j, done) { Jobs.follow(j, {}, done); }

  var me = null;

  // ---- 概览 / Overview ----
  function overview(el) {
    function draw(i) {
      var hw = i.hw || {}, id = (me && me.identity) || {};
      var ready = (i.models || []).filter(function (m) { return m.state === 'ready'; }).length;
      var memUsed = hw.mem_total_mb - hw.mem_available_mb;
      var gpus = hw.gpus || [];
      UI.mount(el,
        h('div.grid.grid--4',
          UI.stat('gpu', t('ov.gpus'), gpus.length, gpus.map(function (g) { return g.name; }).slice(0, 2).join(', ')),
          UI.stat('cube', t('ov.models'), ready + ' / ' + (i.models || []).length, null, 'ok'),
          UI.stat('activity', t('ov.inflight'), i.inflight || 0, null, 'info'),
          UI.stat('terminal', t('ov.jobs'), i.jobs || 0, null, 'warn')),
        h('div.grid.grid--2',
          UI.card(t('ov.profile'), UI.kv([
            [t('ov.name'), i.name], [t('ov.desc'), i.description || '—'], [t('ov.id'), h('code', i.id)], [t('ov.version'), id.version],
            [t('ov.web'), id.http_url], [t('ov.rpc'), id.rpc_addr || '—'], [t('ov.started'), UI.fmtTime(id.started_at)],
            [t('ov.hub_manage'), i.hub_manage ? UI.badge(t('ov.allowed'), 'warn') : UI.badge(t('ov.denied'))]
          ])),
          UI.card(t('ov.system'), UI.kv([
            [t('ov.hostname'), hw.hostname], [t('ov.os'), hw.platform || hw.os], [t('ov.kernel'), hw.kernel], [t('ov.arch'), hw.arch],
            [t('ov.cpu'), (hw.cpu_model || '') + ' · ' + t('ov.cores', { n: hw.cpu_cores || 0 })], [t('ov.load'), UI.fmtNum(hw.load1, 2)],
            [t('ov.uptime'), UI.fmtDur(hw.uptime_seconds)], [t('ov.ips'), (hw.ips || []).join(', ')]
          ]))),
        UI.card(t('ov.res'), h('div.stack',
          UI.meter(t('ov.memory'), memUsed, hw.mem_total_mb, t('ov.used_of', { a: UI.fmtMB(memUsed), b: UI.fmtMB(hw.mem_total_mb) })),
          (hw.disks || []).map(function (d) {
            return UI.meter(d.mount + (d.fs_type ? ' (' + d.fs_type + ')' : ''), d.total_mb - d.free_mb, d.total_mb,
              t('ov.used_of', { a: UI.fmtMB(d.total_mb - d.free_mb), b: UI.fmtMB(d.total_mb) }));
          }))),
        gpus.length ? UI.card(t('ov.gpus'), h('div.grid.grid--auto', gpus.map(function (g) {
          return h('div.gpu', h('div.gpu__head', h('div.gpu__chip.gpu__chip--' + g.vendor, (g.vendor || '?').slice(0, 3).toUpperCase()),
            h('div', h('div.gpu__name', g.name), h('div.gpu__sub', '#' + g.index + (g.driver ? ' · ' + g.driver : '')))),
            UI.meter(t('ov.util'), g.utilization, 100, UI.fmtNum(g.utilization, 0, '%')),
            g.memory_mb ? UI.meter(t('ov.vram'), g.memory_used_mb, g.memory_mb, t('ov.used_of', { a: UI.fmtMB(g.memory_used_mb), b: UI.fmtMB(g.memory_mb) })) : null);
        }))) : null);
    }
    function load() { return call('/node/info').then(draw).catch(UI.fail); }
    load();
    UI.every(10000, load);
  }

  // ---- 模型 / Models ----
  function modelForm(m) {
    var isNew = !m;
    m = m || { api: 'openai' };
    var f = {
      name: h('input.input', { value: m.name || '', placeholder: 'qwen3-8b', disabled: !isNew }),
      api: h('select.select', { disabled: !isNew }, ['openai', 'anthropic'].map(function (a) { return h('option', { value: a, selected: m.api === a }, a); })),
      base_url: h('input.input', { value: m.base_url || '', placeholder: 'http://127.0.0.1:11434/v1' }),
      upstream: h('input.input', { value: m.upstream || '', placeholder: 'qwen3:8b' }),
      api_key: h('input.input', { type: 'password', autocomplete: 'new-password', value: m.api_key || '' }),
      max_inflight: h('input.input', { type: 'number', min: 0, value: m.max_inflight || 0 }),
      description: h('input.input', { value: m.description || '' }),
      disabled: h('input', { type: 'checkbox', checked: !!m.disabled })
    };
    f.api.addEventListener('change', function () {
      if (!f.base_url.value) f.base_url.placeholder = f.api.value === 'anthropic' ? 'https://api.anthropic.com/v1' : 'http://127.0.0.1:11434/v1';
    });
    return {
      el: h('div.form',
        h('div.form-row', UI.field(t('m.name'), f.name, t('m.name_hint')), UI.field(t('m.api'), f.api)),
        UI.field(t('m.base_url'), f.base_url),
        h('div.form-row', UI.field(t('m.upstream'), f.upstream, t('m.upstream_hint')), UI.field(t('m.max'), f.max_inflight, t('m.max_hint'))),
        UI.field(t('m.key'), f.api_key, m.api_key ? t('m.key_keep') : null),
        UI.field(t('m.desc'), f.description),
        h('label.check', f.disabled, t('m.disabled'))),
      value: function () {
        return {
          name: f.name.value.trim(), api: f.api.value, base_url: f.base_url.value.trim(), upstream: f.upstream.value.trim(),
          api_key: f.api_key.value, max_inflight: +f.max_inflight.value || 0, description: f.description.value, disabled: f.disabled.checked
        };
      }
    };
  }
  function editModel(m, done) {
    var form = modelForm(m);
    UI.modal({
      title: m ? t('m.edit') + ' · ' + m.name : t('m.add'), wide: true, body: form.el,
      actions: [{ label: t('cancel') }, {
        label: t('save'), kind: 'primary', onclick: function () {
          return call('/node/model/save', form.value()).then(function () { UI.toast(t('saved')); done(); });
        }
      }]
    });
  }
  function models(el) {
    var tableEl = h('div'), tryEl = h('div');
    UI.mount(el, h('div.stack', tableEl, tryEl));
    var tryReady = null;
    function draw(r) {
      var list = r[0], views = {};
      (r[1].models || []).forEach(function (v) { views[v.api + '/' + v.name] = v; });
      function view(m) { return views[m.api + '/' + m.name] || { state: m.disabled ? 'disabled' : 'ready', inflight: 0 }; }
      var cols = [
        { label: t('m.name'), render: function (m) { return h('div', h('b', m.name), m.description ? h('div.small.muted', m.description) : null); } },
        { label: t('m.api'), render: function (m) { return UI.badge(m.api, 'primary'); } },
        { label: t('m.base_url'), render: function (m) { return h('div.small', h('div.mono.ellipsis', { style: { maxWidth: '260px' } }, m.base_url), m.upstream ? h('div.muted', '→ ', h('code', m.upstream)) : null); } },
        { label: t('m.key'), render: function (m) { return m.api_key ? h('code.small', m.api_key) : h('span.muted', '—'); } },
        { label: t('m.state'), render: function (m) { var v = view(m); return h('div', stateBadge(v), v.error ? h('div.small', { style: { color: 'var(--danger)', maxWidth: '280px' } }, v.error) : null, v.checked_at ? h('div.small.muted', t('m.checked') + ' ' + UI.ago(v.checked_at)) : null); } },
        { label: t('m.inflight'), cls: 'num', render: function (m) { var v = view(m); return v.inflight + (m.max_inflight ? ' / ' + m.max_inflight : ''); } },
        {
          label: '', cls: 'actions', render: function (m) {
            var checkBtn = UI.btn(t('m.check'), 'activity', function () {
              UI.busy(checkBtn, call('/node/model/check', { name: m.name, api: m.api })).then(function (v) {
                if (v.error) UI.toast(v.error, 'error'); else UI.toast(t('m.checked_ok'));
                load();
              }).catch(UI.fail);
            }, 'sm');
            var toggle = UI.btn(m.disabled ? t('m.enable') : t('m.disable'), m.disabled ? 'play' : 'stop', function () {
              var x = Object.assign({}, m, { disabled: !m.disabled });
              UI.busy(toggle, call('/node/model/save', x)).then(load).catch(UI.fail);
            }, 'sm');
            return h('div.btn-row', { style: { justifyContent: 'flex-end' } }, checkBtn, toggle,
              UI.btn('', 'edit', function () { editModel(m, load); }, 'ghost sm icon'),
              UI.btn('', 'trash', function () {
                UI.confirm(t('m.delete_confirm', { m: m.name, a: m.api }), t('delete'), true).then(function (ok) {
                  if (ok) call('/node/model/delete', { name: m.name, api: m.api }).then(load).catch(UI.fail);
                });
              }, 'ghost sm icon'));
          }
        }];
      UI.mount(tableEl, UI.card(t('m.title'), UI.table(cols, list, { empty: t('m.none') }),
        h('div.btn-row', UI.btn(t('refresh'), 'refresh', load, 'sm'), UI.btn(t('m.add'), 'plus', function () { editModel(null, load); }, 'primary sm')), true));
      var ready = (r[1].models || []).filter(function (v) { return v.state === 'ready'; });
      var sig = ready.map(function (v) { return v.api + '/' + v.name; }).join(',');
      if (sig !== tryReady) { tryReady = sig; UI.mount(tryEl, tryBox(ready)); }
    }
    function load() { return Promise.all([call('/node/model/list'), call('/node/info')]).then(draw).catch(UI.fail); }
    load();
    UI.every(5000, function () { if (idle(el)) load(); });
  }

  function b64(s) { var u = new TextEncoder().encode(s), bin = ''; for (var i = 0; i < u.length; i++) bin += String.fromCharCode(u[i]); return btoa(bin); }

  // tryBox: one streamed call through /node/proxy, as an agent would make it (the text deltas are extracted from
  // the SSE events; the raw response is shown below).
  // tryBox：经 /node/proxy 发起一次流式调用，与 agent 的调用相同（从 SSE 事件中提取文本增量；原始应答显示在下方）。
  function tryBox(ready) {
    if (!ready.length) return UI.card(t('m.try'), UI.empty(t('m.no_ready'), 'cube'));
    var sel = h('select.select.select--sm', { style: { width: 'auto' } }, ready.map(function (v, i) { return h('option', { value: i }, v.name + ' (' + v.api + ')'); }));
    var prompt = h('textarea.textarea', { rows: 2 }, 'Say hello in one sentence.');
    var out = h('div', { style: { whiteSpace: 'pre-wrap', minHeight: '24px' } });
    var meta = h('div.small.muted');
    var raw = h('pre.console', { style: { maxHeight: '240px', minHeight: '0' } });
    var ctrl = null;
    var btn = UI.btn(t('m.send'), 'send', function () {
      if (ctrl) { ctrl.abort(); return; }
      var v = ready[+sel.value];
      var body = { model: v.name, stream: true, messages: [{ role: 'user', content: prompt.value }] };
      if (v.api === 'anthropic') body.max_tokens = 512;
      var req = { api: v.api, path: v.api === 'anthropic' ? 'messages' : 'chat/completions', model: v.name, body: b64(JSON.stringify(body)) };
      out.textContent = ''; raw.textContent = ''; meta.textContent = '';
      var buf = '', first = true;
      ctrl = new AbortController();
      UI.mount(btn, UI.icon('stop'), t('m.stop'));
      function sse(chunk) {
        buf += chunk;
        for (var i = buf.indexOf('\n'); i >= 0; i = buf.indexOf('\n')) {
          var line = buf.slice(0, i).trim(); buf = buf.slice(i + 1);
          if (line.indexOf('data:') !== 0) continue;
          var d; try { d = JSON.parse(line.slice(5)); } catch (e) { continue; }
          var c = d.choices && d.choices[0] && d.choices[0].delta && d.choices[0].delta.content;
          if (!c && d.type === 'content_block_delta' && d.delta) c = d.delta.text;
          if (c) out.textContent += c;
        }
      }
      UI.rpcStream('/node/proxy', req, function (f) {
        if (first) { first = false; meta.textContent = 'HTTP ' + f.status + ' · ' + (f.content_type || ''); return; }
        var s = typeof f === 'string' ? f : JSON.stringify(f);
        raw.textContent += s + '\n';
        sse(s + '\n');
      }, ctrl.signal).then(function (r) {
        meta.textContent += ' · ' + UI.fmtNum((r.duration_ms || 0) / 1000, 1, 's') + ' · ' + r.bytes + ' B';
      }).catch(function (e) { if (!ctrl.signal.aborted) UI.fail(e); }).then(function () {
        ctrl = null; UI.mount(btn, UI.icon('send'), t('m.send'));
      });
    }, 'primary sm');
    return UI.card(t('m.try'), h('div.stack',
      h('div.row', sel, h('span.topbar__spacer'), btn), prompt, meta, out,
      h('details', h('summary.small.muted', { style: { cursor: 'pointer' } }, t('m.raw')), raw)));
  }

  // ---- 显卡 / GPUs ----
  function gpus(el) {
    function deviceCard(d, vendor) {
      var settings = (vendor && vendor.settings) || [];
      var power = d.power_draw >= 0 ? UI.fmtNum(d.power_draw, 0, ' W') + (d.power_limit >= 0 ? ' / ' + UI.fmtNum(d.power_limit, 0, ' W') : '') : '—';
      return h('div.card', h('div.card__body.gpu',
        h('div.gpu__head', h('div.gpu__chip.gpu__chip--' + d.vendor, d.vendor.slice(0, 3).toUpperCase()),
          h('div', { style: { flex: 1, minWidth: 0 } }, h('div.gpu__name.ellipsis', d.name || '—'),
            h('div.gpu__sub.ellipsis', '#' + d.index + (d.driver ? ' · ' + d.driver : '') + (d.uuid ? ' · ' + d.uuid : '')))),
        h('div.gpu__metrics',
          UI.meter(t('ov.util'), d.utilization, 100, UI.fmtNum(d.utilization, 0, '%')),
          UI.meter(t('ov.vram'), d.mem_used_mb, d.mem_total_mb, d.mem_total_mb > 0 ? t('ov.used_of', { a: UI.fmtMB(d.mem_used_mb), b: UI.fmtMB(d.mem_total_mb) }) : '—'),
          UI.meter(t('g.temp'), d.temperature, 100, UI.fmtNum(d.temperature, 0, ' °C')),
          UI.meter(t('g.power'), d.power_draw, d.power_limit > 0 ? d.power_limit : d.power_max, power),
          UI.meter(t('g.clock_gfx'), d.clock_gfx, d.clock_gfx_max, UI.fmtNum(d.clock_gfx, 0, ' MHz')),
          UI.meter(t('g.clock_mem'), d.clock_mem, d.clock_mem_max, UI.fmtNum(d.clock_mem, 0, ' MHz')),
          d.fan_speed >= 0 ? UI.meter(t('g.fan'), d.fan_speed, 100, UI.fmtNum(d.fan_speed, 0, '%')) : null),
        settings.length ? settingForm(d.vendor, d.index, settings) : null));
    }
    // settingForm: one input per setting kind (number, range "min,max", enum, action). / 按设置类型提供输入。
    function settingForm(vendorID, index, settings) {
      var sel = h('select.select.select--sm', settings.map(function (s) { return h('option', { value: s.key }, setLabel(s) + (s.unit ? ' (' + s.unit + ')' : '')); }));
      var valWrap = h('div', { style: { flex: '1', minWidth: '120px' } });
      var note = h('div.field__hint');
      var all = h('input', { type: 'checkbox' });
      var input = null;
      function cur() { return settings.filter(function (s) { return s.key === sel.value; })[0]; }
      function sync() {
        var s = cur();
        if (s.kind === 'enum') input = h('select.select.select--sm', s.options.map(function (x) { return h('option', { value: x }, x); }));
        else if (s.kind === 'action') input = null;
        else input = h('input.input.input--sm', { placeholder: s.kind === 'range' ? 'min,max' : (s.unit || t('g.value')), inputmode: s.kind === 'number' ? 'numeric' : 'text' });
        UI.mount(valWrap, input);
        note.textContent = zhOr(s, 'note') || '';
      }
      sel.addEventListener('change', sync);
      sync();
      var apply = UI.btn(t('g.apply'), 'check', function () {
        var req = { vendor: vendorID, index: all.checked ? -1 : index, key: cur().key, value: input ? input.value : '' };
        UI.busy(apply, call('/node/gpu/set', req)).then(function (j) { followJob(j, load); }).catch(UI.fail);
      }, 'primary sm');
      return h('div.stack', { style: { gap: '8px', borderTop: '1px solid var(--border)', paddingTop: '12px' } },
        h('div.row', h('b.small', t('g.settings')), h('span.topbar__spacer'), h('label.check.small', all, t('g.all'))),
        h('div.row', h('div', { style: { flex: '1', minWidth: '160px' } }, sel), valWrap, apply), note);
    }
    function vendorCard(v) {
      var detected = v.detected > 0;
      var installBtn = v.install_for ? UI.btn(t('g.install'), 'download', function () {
        UI.confirm(t('g.install_confirm', { v: v.name })).then(function (ok) {
          if (ok) call('/node/gpu/install', { vendor: v.id }).then(function (j) { followJob(j, load); }).catch(UI.fail);
        });
      }, v.installed ? 'sm' : 'primary sm') : null;
      var method = (v.install || []).filter(function (m) { return m.os === v.install_for; })[0];
      return h('div.card', h('div.card__body.stack', { style: { gap: '10px' } },
        h('div.gpu__head', h('div.gpu__chip.gpu__chip--' + v.id, v.id.slice(0, 3).toUpperCase()),
          h('div', { style: { flex: 1 } }, h('div.gpu__name', v.name), h('div.gpu__sub', h('code', v.tool)))),
        h('div.row', detected ? UI.badge(t('g.detected', { n: v.detected }), 'info') : UI.badge(t('g.not_detected')),
          v.installed ? UI.badge(t('g.tool_ok'), 'ok', true) : UI.badge(t('g.tool_missing'), detected ? 'warn' : '')),
        h('div.small.muted', zhOr(v, 'description')),
        h('details', h('summary.small.muted', { style: { cursor: 'pointer' } }, t('g.scripts')),
          h('div.stack', { style: { marginTop: '8px' } }, h('code', { style: { display: 'block', whiteSpace: 'pre-wrap' } }, v.query_cmd),
            method ? h('pre.console', { style: { maxHeight: '220px', minHeight: '0' } }, method.script) : null)),
        h('div.row', installBtn, v.docs ? h('a.btn.btn--ghost.btn--sm', { href: v.docs, target: '_blank', rel: 'noopener' }, UI.icon('book'), t('g.docs')) : null)));
    }
    function draw(s) {
      var vendors = {};
      (s.vendors || []).forEach(function (v) { vendors[v.id] = v; });
      var relevant = (s.vendors || []).filter(function (v) { return v.detected > 0 || v.installed; });
      var others = (s.vendors || []).filter(function (v) { return !(v.detected > 0 || v.installed); });
      var runAs = s.root ? t('g.root') : s.use_sudo ? t('g.sudo') : t('g.user');
      var refreshBtn = UI.btn(t('refresh'), 'refresh', function () { UI.busy(refreshBtn, call('/node/gpu/status', { fresh: true }).then(draw)).catch(UI.fail); }, 'sm');
      UI.mount(el,
        h('div.row.small.muted', h('span', t('g.os') + ': ', h('b', s.os)), h('span', '·'), h('span', t('g.run_as') + ': ', h('b', runAs)), h('span.topbar__spacer'), refreshBtn),
        Object.keys(s.errors || {}).map(function (k) { return h('div.callout.callout--danger', (vendors[k] ? vendors[k].name : k) + ': ' + s.errors[k]); }),
        (s.devices || []).length ? h('div.grid.grid--2', s.devices.map(function (d) { return deviceCard(d, vendors[d.vendor]); })) : h('div.card', UI.empty(t('g.no_gpu'), 'gpu')),
        Object.keys(s.raw || {}).map(function (k) { return UI.card(t('g.raw') + ' · ' + (vendors[k] ? vendors[k].name : k), h('pre.console', s.raw[k])); }),
        h('h2', { style: { marginTop: '6px' } }, t('g.vendors')),
        h('div.grid.grid--3', relevant.concat(others).map(vendorCard)));
    }
    function load() { return call('/node/gpu/status').then(draw).catch(UI.fail); }
    load();
    UI.every(10000, function () { if (idle(el)) load(); });
  }

  // ---- 操作 / Actions ----
  function actions(el) {
    call('/node/action/list').then(function (list) {
      var cards = list.map(function (a) {
        var inputs = {};
        var fields = (a.params || []).map(function (p) {
          inputs[p.key] = h('input.input.input--sm', { placeholder: p.example || '', title: p.pattern });
          return UI.field(zhOr(p, 'label'), inputs[p.key]);
        });
        var run = UI.btn(t('a.run'), 'play', function () {
          var params = {};
          Object.keys(inputs).forEach(function (k) { params[k] = inputs[k].value.trim(); });
          UI.busy(run, call('/node/action/run', { id: a.id, params: params })).then(function (j) { followJob(j); }).catch(UI.fail);
        }, 'primary sm');
        return h('div.card', h('div.card__body.stack', { style: { gap: '10px' } },
          h('div.row', h('b', zhOr(a, 'title')), h('span.topbar__spacer'), h('code.small.muted', a.id)),
          zhOr(a, 'note') ? h('div.small.muted', zhOr(a, 'note')) : null,
          fields.length ? h('div.form', fields) : null,
          h('div.btn-row', run)));
      });
      UI.mount(el, h('div.stack', h('div.callout', UI.icon('info'), ' ', t('a.note')),
        cards.length ? h('div.grid.grid--3', cards) : h('div.card', UI.empty(t('a.none')))));
    }).catch(UI.fail);
  }

  // ---- 设置 / Settings ----
  function settings(el) {
    Promise.all([call('/node/info'), call('/node/settings/get')]).then(function (r) {
      var info = r[0], s = r[1];
      var name = h('input.input', { value: info.name || '' }), desc = h('textarea.textarea', { rows: 2 }, info.description || '');
      var saveProfile = h('button.btn.btn--primary', { type: 'submit' }, t('save'));
      var profile = h('form.form', {
        onsubmit: function (e) {
          e.preventDefault();
          UI.busy(saveProfile, call('/node/profile/set', { name: name.value, description: desc.value })).then(function () { UI.toast(t('saved')); }).catch(UI.fail);
        }
      }, UI.field(t('ov.name'), name), UI.field(t('ov.desc'), desc), h('div.btn-row', saveProfile));

      var hub = h('input', { type: 'checkbox', checked: s.allow_hub_manage });
      var sudo = h('input', { type: 'checkbox', checked: s.gpu_sudo });
      // Durations are Go duration strings ("30s", "1h"); the server validates the ranges.
      // 时长为 Go duration 字符串（"30s"、"1h"）；范围由服务端校验。
      var refresh = h('input.input.mono', { placeholder: '30s', autocomplete: 'off', spellcheck: 'false', value: s.refresh_interval || '' });
      var timeout = h('input.input.mono', { placeholder: '1h', autocomplete: 'off', spellcheck: 'false', value: s.job_timeout || '' });
      var dataDir = h('input.input.mono', { autocomplete: 'off', spellcheck: 'false', value: s.gpu_data_dir || '' });
      var saveSettings = h('button.btn.btn--primary', { type: 'submit' }, t('save'));
      var form = h('form.form', {
        onsubmit: function (e) {
          e.preventDefault();
          var req = { allow_hub_manage: hub.checked, gpu_sudo: sudo.checked, refresh_interval: refresh.value.trim(), job_timeout: timeout.value.trim(), gpu_data_dir: dataDir.value.trim() };
          UI.busy(saveSettings, call('/node/settings/set', req)).then(function () { UI.toast(t('saved')); }).catch(UI.fail);
        }
      },
        h('label.check', hub, t('s.hub_manage')), h('div.field__hint', t('s.hub_manage_hint')),
        h('label.check', sudo, t('s.gpu_sudo')), h('div.field__hint', t('s.gpu_sudo_hint')),
        h('div.form-row', UI.field(t('s.refresh'), refresh, t('s.refresh_hint')), UI.field(t('s.job_timeout'), timeout, t('s.job_timeout_hint'))),
        UI.field(t('s.gpu_data_dir'), dataDir, t('s.gpu_data_dir_hint')),
        h('div.btn-row', saveSettings));
      UI.mount(el, h('div.stack', UI.configNote('node'),
        h('div.grid.grid--2', UI.card(t('s.profile'), profile), UI.card(t('s.node'), form))));
    }).catch(UI.fail);
  }

  var appEl = document.getElementById('app');
  var session = UI.boot(appEl, {
    brand: 'fit agent node', sub: t('app.sub'),
    start: function (m) {
      me = m;
      var nav = [['overview', 'dashboard'], ['models', 'cube'], ['gpus', 'gpu'], ['actions', 'terminal'], ['jobs', 'log'], ['peers', 'link'], ['settings', 'settings']];
      var sh = UI.shell({
        brand: 'fit agent node', sub: (m.identity && m.identity.name) || t('app.sub'), user: m.user, onLogout: function () { session.logout(); },
        nav: nav.map(function (n) { return { id: n[0], icon: n[1], label: t('nav.' + n[0]) }; })
      });
      UI.mount(appEl, sh.el);
      function page(id) { return sh.page(id, t('nav.' + id)); }
      UI.router({
        overview: function () { overview(page('overview')); },
        models: function () { models(page('models')); },
        gpus: function () { gpus(page('gpus')); },
        actions: function () { actions(page('actions')); },
        jobs: function () { Jobs.mount(page('jobs')); },
        peers: function () { PeersPanel.mount(page('peers')); },
        settings: function () { settings(page('settings')); }
      }, 'overview');
    }
  });
})();
