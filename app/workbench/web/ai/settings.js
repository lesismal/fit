// The assistant's module (ai): its Settings page, shown when the console serves the module ai, which it does only while
// admin.assistant is set. GET /api/settings reads the settings in use (admin.assistant's, or the store's once saved),
// whether a store keeps them, and each provider's key as the pages may see it: set or not, where from, and its last four
// characters. PUT /api/settings saves the settings whole with the keys typed (an empty key removes the kept one), and
// POST /api/providers/test calls a provider as the editor has it, saved or not: its model list, then a short call. A key
// typed here goes to the server and never comes back.
//
// 助手模块（ai）：其设置页。仅当控制台提供 ai 模块时显示，而控制台只在设置了 admin.assistant 时提供它。GET /api/settings
// 读取正在使用的设置（admin.assistant 的，保存后则为 store 中的）、是否有 store 保存，以及页面可见的各提供方密钥信息：是否已设置、
// 来源及末四位。PUT /api/settings 整体保存设置与输入的密钥（空密钥表示删除已保存的），POST /api/providers/test 按编辑器中的
// 提供方（无论是否已保存）调用：先取模型列表，再做一次简短调用。在此输入的密钥只发往服务器，不会再返回。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['m.ai', 'Assistant', '助手'], ['n.settings', 'Settings', '设置'], ['n.functions', 'Functions', '功能'],
    ['n.proposals', 'Proposals', '提案'], ['n.audit', 'Audit log', '审计日志'], ['n.skills', 'Skills', '技能'],
    ['src.store', 'These settings are kept in the store, with the providers\' keys.', '这些设置与各提供方的密钥一起保存在 store 中。'],
    ['src.config', 'These settings are admin.assistant\'s from the config file. Saving keeps them in the store from then on, keys included; the file\'s are then only the first ones.',
      '这些设置来自配置文件中的 admin.assistant。保存后改由 store 保存（包括密钥）；此后文件中的设置只是最初的设置。'],
    ['src.nostore', 'This fit runs without admin.store: the settings are admin.assistant\'s in the config file and cannot be saved here, and keys come from api_key_env. A provider can still be tested.',
      '本 fit 未配置 admin.store：设置来自配置文件中的 admin.assistant，无法在此保存，密钥来自 api_key_env。仍可测试提供方。'],
    ['c.providers', 'Providers', '提供方'], ['c.model', 'Model and parameters', '模型与参数'], ['c.limits', 'Limits', '限制'],
    ['c.data', 'Data sent to models', '发送给模型的数据'], ['c.users', 'Users', '用户'],
    ['p.add', 'Add provider', '添加提供方'], ['p.remove', 'Remove', '删除'], ['p.edit', 'Edit provider', '编辑提供方'], ['p.none', 'No providers yet: add one to use the assistant.', '尚无提供方：添加一个才能使用助手。'],
    ['p.name', 'Name', '名称'], ['p.kind', 'Kind', '类型'], ['p.url', 'Base URL', '基础 URL'], ['p.key', 'Key', '密钥'],
    ['p.models', 'Models', '模型'], ['p.default', 'default: {url}', '默认：{url}'],
    ['p.version', 'API version', 'API 版本'], ['p.env', 'Key from an environment variable', '从环境变量读取密钥'],
    ['p.keyNew', 'API key', 'API 密钥'],
    ['p.keyHint', 'Kept in the store, encrypted; never shown again. Leave empty to keep the one set.', '加密保存在 store 中，不再显示。留空则保留已设置的密钥。'],
    ['p.keyRemove', 'Remove the kept key', '删除已保存的密钥'], ['p.keyKept', 'kept: {masked} ({source})', '已保存：{masked}（{source}）'],
    ['p.keyNone', 'no key', '无密钥'], ['p.keyTyped', 'new key, not saved yet', '新密钥，尚未保存'], ['p.keyRemoved', 'removed when saved', '保存时删除'],
    ['p.headers', 'Headers', '请求头'], ['p.headersHint', 'One per line, as Name: value.', '每行一个，格式为 名称: 值。'],
    ['p.advanced', 'Timeouts and retries', '超时与重试'], ['p.connect', 'Connect', '连接'], ['p.firstByte', 'First byte', '首字节'],
    ['p.idle', 'Idle', '空闲'], ['p.timeout', 'Whole call', '整个调用'], ['p.retries', 'Retries', '重试次数'],
    ['p.modelsHint', 'Each the model\'s ID; the context window in tokens and whether it calls tools are optional.', '每项为模型 ID；上下文窗口（token 数）与是否调用工具可选。'],
    ['p.addModel', 'Add model', '添加模型'], ['p.fetch', 'Read the provider\'s list', '读取提供方的模型列表'], ['p.ctx', 'Context', '上下文'],
    ['p.tools', 'Calls tools', '调用工具'], ['p.proxyAt', 'Calls this fit\'s llm.proxy at {addr}{tls}.', '调用本 fit 的 llm.proxy，地址 {addr}{tls}。'],
    ['p.proxyNone', 'This fit runs no llm.proxy with an api: an llm_proxy provider has nothing to call.', '本 fit 没有运行带 api 的 llm.proxy：llm_proxy 提供方无可调用的对象。'],
    ['p.tls', ', over TLS with its certificate pinned', '，经 TLS 并固定其证书'],
    ['t.test', 'Test', '测试'], ['t.running', 'Testing…', '测试中…'], ['t.ok', 'Works', '可用'], ['t.fail', 'Fails', '失败'],
    ['t.models', 'Models listed: {n}, in {ms} ms', '列出的模型：{n} 个，用时 {ms} ms'], ['t.modelsErr', 'Model list: {err}', '模型列表：{err}'],
    ['t.call', '{model} answered “{reply}” in {ms} ms', '{model} 在 {ms} ms 内回答“{reply}”'], ['t.callErr', 'Call to {model}: {err}', '调用 {model}：{err}'],
    ['t.added', '{n} models added', '已添加 {n} 个模型'],
    ['d.provider', 'Provider', '提供方'], ['d.model', 'Model', '模型'], ['d.auto', 'the first model of the first provider', '第一个提供方的第一个模型'],
    ['d.maxOut', 'Max output tokens', '最多输出 token'], ['d.temp', 'Temperature', '温度'], ['d.thinking', 'Thinking', '思考'],
    ['d.modelHint', 'Pick one of the provider\'s models, or type an ID.', '选择提供方的某个模型，或输入 ID。'],
    ['l.max_turns', 'Turns per conversation', '每次对话的轮数'], ['l.max_tool_calls', 'Tool calls per answer', '每次回答的工具调用数'],
    ['l.max_tool_result_bytes', 'Bytes per tool result', '每个工具结果的字节数'], ['l.max_conversations', 'Conversations kept per user', '每个用户保留的对话数'],
    ['l.max_concurrent_chats', 'Answers at once', '同时生成的回答数'], ['l.max_tokens_per_day', 'Tokens per user per day', '每个用户每天的 token 数'],
    ['l.max_stream_bytes', 'Bytes per model answer', '每次模型回答的字节数'], ['l.max_audit_entries', 'Audit entries kept', '保留的审计条目数'],
    ['a.logs', 'Send lines of fit\'s own log, redacted', '发送 fit 自身日志的行（已脱敏）'],
    ['a.access', 'Send lines of the access log, redacted', '发送访问日志的行（已脱敏）'],
    ['a.redact', 'More to redact', '额外脱敏'], ['a.redactHint', 'Regular expressions, one per line; keys, hashes, tokens and private keys always are.', '正则表达式，每行一个；密钥、哈希、令牌与私钥始终会被脱敏。'],
    ['u.default', 'Level of users not listed', '未列出用户的级别'], ['u.user', 'User', '用户'], ['u.level', 'Level', '级别'],
    ['u.inherit', 'default ({level})', '默认（{level}）'],
    ['u.note', 'off: no assistant. read: the read tools. edit: also drafts on the open page, never submitted. propose: also proposals. apply: also applies them.',
      'off：无助手。read：读取类工具。edit：另可在打开的页面上起草，从不提交。propose：另可提出提案。apply：另可应用提案。'],
    ['lv.off', 'off', '关闭'], ['lv.read', 'read', '只读'], ['lv.edit', 'edit', '编辑'], ['lv.propose', 'propose', '提案'], ['lv.apply', 'apply', '应用'],
    ['th.off', 'off', '关闭'], ['th.low', 'low', '低'], ['th.medium', 'medium', '中'], ['th.high', 'high', '高'],
    ['s.save', 'Save settings', '保存设置'], ['s.saved', 'Settings saved', '设置已保存'], ['s.unsaved', 'Unsaved changes', '有未保存的更改'],
    ['s.reset', 'Discard changes', '放弃更改'], ['s.errors', 'The settings are not valid:', '设置无效：'],
    ['s.stale', 'The settings changed since this page read them: reload, then make the change again.', '自本页读取以来设置已被修改：请重新加载后再修改。'],
    ['s.removeP', 'Remove the provider {name}? Its kept key goes with it when the settings are saved.', '删除提供方 {name}？保存设置时其已保存的密钥也会删除。'],
    ['s.dup', 'Another provider is named {name}.', '已有名为 {name} 的提供方。'], ['s.needName', 'Give the provider a name.', '请为提供方命名。'],
    ['src.k.store', 'store', 'store'], ['src.k.env', 'environment', '环境变量']
  ];

  var LIMITS = [['max_turns', 40], ['max_tool_calls', 16], ['max_tool_result_bytes', 32768], ['max_conversations', 50],
    ['max_concurrent_chats', 4], ['max_tokens_per_day', 1000000], ['max_stream_bytes', 8388608], ['max_audit_entries', 1000]];
  var TIMEOUTS = [['connect_timeout', 'p.connect', '10s'], ['first_byte_timeout', 'p.firstByte', '2m'], ['idle_timeout', 'p.idle', '1m'],
    ['timeout', 'p.timeout', '10m']];

  WB.module({
    id: 'ai', icon: 'chat', label: 'm.ai', words: WORDS,
    nav: [{ id: 'functions', icon: 'code', label: 'n.functions' }, { id: 'skills', icon: 'book', label: 'n.skills' }, { id: 'proposals', icon: 'layers', label: 'n.proposals' },
      { id: 'audit', icon: 'history', label: 'n.audit' }, { id: 'settings', icon: 'settings', label: 'n.settings' }],
    // The chat panel, opened from the top bar of every page (ai/panel.js). / 对话面板，从每个页面的顶栏打开（ai/panel.js）。
    topbar: function (UI, shell) { return window.FitAI && FitAI.panel ? FitAI.panel(UI, shell) : null; },
    setup: function (UI, sh) {
      var h = UI.h, t = UI.t;

      function clone(v) { return JSON.parse(JSON.stringify(v)); }
      function num(input) { var v = input.value.trim(); return v === '' ? undefined : Number(v); }
      function setOrDelete(obj, k, v) { if (v === undefined || v === '' || v === null || (typeof v === 'number' && isNaN(v))) delete obj[k]; else obj[k] = v; }
      function pruneEmpty(obj, k) { if (obj[k] && typeof obj[k] === 'object' && !Array.isArray(obj[k]) && Object.keys(obj[k]).length === 0) delete obj[k]; }
      function input(value, attrs) { return h('input.input', Object.assign({ value: value == null ? '' : String(value) }, attrs || {})); }
      function select(options, value, onchange) {
        return h('select.select', { onchange: onchange }, options.map(function (o) { return h('option', { value: o[0], selected: o[0] === value }, o[1]); }));
      }
      function check(label, on, onchange) {
        var c = h('input', { type: 'checkbox', checked: !!on, onchange: function () { onchange(c.checked); } });
        return h('label.check', c, h('span', label));
      }
      function errorBox() { return h('div.callout.callout--danger', { hidden: true }); }
      function showError(box, e) {
        box.textContent = '';
        box.appendChild(document.createTextNode(e && e.message ? e.message : String(e)));
        if (e && e.errors && e.errors.length) {
          box.appendChild(h('ul', e.errors.map(function (x) { return h('li', h('code', x.path), ' ', x.msg); })));
        }
        box.hidden = false;
      }

      // put sends the settings; a refused one comes back with each error at its path. / 发送设置；被拒绝时带回各路径的错误。
      function put(body) {
        return UI.raw('PUT', '/api/settings', body).then(function (r) {
          return r.text().then(function (txt) {
            var j = null;
            try { j = JSON.parse(txt); } catch (e) { /* not JSON / 非 JSON */ }
            if (r.ok) return j;
            var err = new Error(r.status === 412 ? t('s.stale') : (j && j.error) || r.status + ' ' + r.statusText);
            err.status = r.status; err.errors = j && j.errors;
            throw err;
          });
        });
      }

      // testResult renders a provider test's answer. / 渲染提供方测试的结果。
      function testResult(r) {
        var lines = [];
        if (r.models_error) lines.push(h('div', UI.icon('alert'), ' ', t('t.modelsErr', { err: r.models_error })));
        else lines.push(h('div', UI.icon('check'), ' ', t('t.models', { n: (r.models || []).length, ms: r.models_ms })));
        if (r.model) {
          if (r.call_error) lines.push(h('div', UI.icon('alert'), ' ', t('t.callErr', { model: r.model, err: r.call_error })));
          else lines.push(h('div', UI.icon('check'), ' ', t('t.call', { model: r.model, reply: r.reply || '', ms: r.call_ms })));
        }
        return h('div.callout' + (r.ok ? '' : '.callout--danger'), h('div.row', UI.badge(r.ok ? t('t.ok') : t('t.fail'), r.ok ? 'ok' : 'danger', true)), lines);
      }

      function settingsPage() {
        var el = sh.page('settings', t('n.settings'));
        var state = null; // {view, S (settings), keys: {name: newKey|''}, dirty}

        function load() {
          UI.mount(el, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
          UI.api('GET', '/api/settings').then(function (v) {
            if (!document.body.contains(el)) return;
            var S = {};
            try { S = typeof v.settings === 'object' && v.settings ? clone(v.settings) : {}; } catch (e) { S = {}; }
            state = { view: v, S: S, keys: {}, dirty: false };
            draw();
          }, function (e) {
            if (!document.body.contains(el) || (e && e.status === 401)) return;
            UI.mount(el, h('div.callout.callout--danger', e && e.message ? e.message : String(e)));
          });
        }
        UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', load, 'ghost sm'));

        function changed() { state.dirty = true; if (dirtyEl) dirtyEl.hidden = false; }
        var dirtyEl = null;

        function draw() {
          var v = state.view, S = state.S;
          var canSave = v.store;
          var src = !v.store ? h('div.callout.callout--warn', UI.icon('info'), ' ', t('src.nostore'))
            : h('div.callout', UI.icon('info'), ' ', t(v.source === 'store' ? 'src.store' : 'src.config'));
          var err = errorBox();
          dirtyEl = h('span.badge.badge--warn', { hidden: !state.dirty }, t('s.unsaved'));
          var saveBtn = h('button.btn.btn--primary', { type: 'button', disabled: !canSave }, UI.icon('save'), t('s.save'));
          saveBtn.onclick = function () {
            err.hidden = true;
            // Empty objects the cards made are not sent. / 卡片创建的空对象不发送。
            var out = clone(S);
            ['params', 'limits', 'data'].forEach(function (k) { pruneEmpty(out, k); });
            if (out.users && !out.users.length) delete out.users;
            var body = { settings: out, keys: state.keys, version: v.version };
            UI.busy(saveBtn, put(body)).then(function (nv) {
              state = { view: nv, S: clone(nv.settings || {}), keys: {}, dirty: false };
              UI.toast(t('s.saved'));
              draw();
            }, function (e) { showError(err, e); });
          };
          UI.mount(el, h('div.stack', src,
            UI.card(t('c.providers'), providersCard(), UI.btn(t('p.add'), 'plus', function () { editProvider(-1); }, 'sm')),
            UI.card(t('c.model'), modelCard()),
            UI.card(t('c.limits'), limitsCard()),
            UI.card(t('c.data'), dataCard()),
            UI.card(t('c.users'), usersCard()),
            err,
            h('div.row', saveBtn, UI.btn(t('s.reset'), 'undo', load, 'ghost'), dirtyEl)));
        }

        // keyState says what a provider's key is now, on this page. / 提供方密钥在本页上的当前状态。
        function keyState(name) {
          if (Object.prototype.hasOwnProperty.call(state.keys, name)) {
            return state.keys[name] ? UI.badge(t('p.keyTyped'), 'primary') : UI.badge(t('p.keyRemoved'), 'warn');
          }
          var k = state.view.keys[name];
          if (k && k.set) return UI.badge(t('p.keyKept', { masked: k.masked, source: t('src.k.' + k.source) }), 'ok');
          return h('span.muted', t('p.keyNone'));
        }

        function providersCard() {
          var list = state.S.providers || [];
          return UI.table([
            { label: t('p.name'), render: function (p) { return h('b', p.name); } },
            { label: t('p.kind'), render: function (p) { return h('code', p.kind); } },
            { label: t('p.url'), render: function (p) { return p.kind === 'llm_proxy' ? h('span.muted', 'llm.proxy') : (p.base_url || h('span.muted', state.view.defaults[p.kind] || '')); } },
            { label: t('p.key'), render: function (p) { return keyState(p.name); } },
            { label: t('p.models'), render: function (p) { return String((p.models || []).length); } },
            { label: '', cls: 'actions', render: function (p) {
              var i = list.indexOf(p);
              function iconBtn(label, name, onclick) {
                return h('button.btn.btn--ghost.btn--sm.btn--icon', { type: 'button', title: label + ' ' + p.name, 'aria-label': label + ' ' + p.name, onclick: onclick }, UI.icon(name));
              }
              return h('div.row', { style: { flexWrap: 'nowrap' } }, UI.btn(t('t.test'), 'play', function () { quickTest(p); }, 'ghost sm'),
                iconBtn(t('p.edit'), 'edit', function () { editProvider(i); }),
                iconBtn(t('p.remove'), 'trash', function () {
                  UI.confirm(t('s.removeP', { name: p.name }), t('p.remove'), true).then(function (ok) {
                    if (!ok) return;
                    list.splice(i, 1);
                    delete state.keys[p.name];
                    if (state.S.default_model && state.S.default_model.provider === p.name) delete state.S.default_model;
                    changed(); draw();
                  });
                }));
            } }
          ], list, { empty: t('p.none') });
        }

        // test calls a provider as p has it, with key typed (or the kept one). / 按 p 测试提供方，使用输入的密钥（或已保存的）。
        function test(p, key, modelsOnly) {
          return UI.api('POST', '/api/providers/test', { provider: p, key: key || '', models_only: !!modelsOnly });
        }
        function quickTest(p) {
          var body = h('div', h('div.row', h('span.spinner'), h('span.muted', t('t.running'))));
          UI.modal({ title: t('t.test') + ' ' + p.name, body: body });
          var typed = state.keys[p.name];
          test(p, typed).then(function (r) { UI.mount(body, testResult(r)); }, function (e) { UI.mount(body, h('div.callout.callout--danger', e.message)); });
        }

        function editProvider(index) {
          var list = state.S.providers = state.S.providers || [];
          var p = index >= 0 ? clone(list[index]) : { name: '', kind: 'openai' };
          var oldName = p.name;
          var err = errorBox();
          var name = input(p.name, { placeholder: 'openai' });
          var kind = select(state.view.kinds.map(function (k) { return [k, k]; }), p.kind, function () { p.kind = kind.value; kindChanged(); });
          var url = input(p.base_url, {});
          var version = input(p.api_version, { placeholder: '2024-10-21' });
          var keyIn = h('input.input', { type: 'password', autocomplete: 'off', value: state.keys[oldName] || '' });
          var removeKey = false;
          var kept = state.view.keys[oldName];
          var env = input(p.api_key_env, { placeholder: 'OPENAI_API_KEY' });
          var headers = h('textarea.textarea.textarea--code', { rows: 3, placeholder: 'OpenAI-Organization: org-…' },
            Object.keys(p.headers || {}).map(function (k) { return k + ': ' + p.headers[k]; }).join('\n'));
          var timeouts = TIMEOUTS.map(function (x) { return [x[0], input(p[x[0]], { placeholder: x[2] }), x[1]]; });
          var retries = input(p.retries, { type: 'number', min: 0, max: 5, placeholder: '2' });
          var models = (p.models || []).map(function (m) { return clone(m); });
          var modelsEl = h('div.stack');
          var proxyNote = h('div');
          var versionField = UI.field(t('p.version'), version);
          var urlField = UI.field(t('p.url'), url);
          var result = h('div');

          function kindChanged() {
            url.placeholder = state.view.defaults[kind.value] || 'https://…/v1';
            versionField.hidden = kind.value !== 'azure_openai';
            urlField.hidden = kind.value === 'llm_proxy';
            var px = state.view.llm_proxy;
            UI.mount(proxyNote, kind.value !== 'llm_proxy' ? null : px
              ? h('div.callout', UI.icon('info'), ' ', t('p.proxyAt', { addr: px.address, tls: px.tls ? t('p.tls') : '' }))
              : h('div.callout.callout--warn', UI.icon('alert'), ' ', t('p.proxyNone')));
          }
          function drawModels() {
            UI.mount(modelsEl, models.map(function (m, i) {
              var mn = input(m.name, { placeholder: 'model-id', oninput: function () { m.name = mn.value.trim(); } });
              var ctx = input(m.context, { type: 'number', min: 0, placeholder: t('p.ctx'), oninput: function () { setOrDelete(m, 'context', num(ctx)); } });
              return h('div.form-row', mn, ctx, check(t('p.tools'), m.tools !== false, function (on) { if (on) delete m.tools; else m.tools = false; }),
                UI.btn('', 'trash', function () { models.splice(i, 1); drawModels(); }, 'ghost sm'));
            }));
          }
          // collect reads the editor into a provider, or throws what is wrong. / 把编辑器内容读成提供方，出错则抛出。
          function collect() {
            var q = {};
            Object.keys(p).forEach(function (k) { q[k] = p[k]; });
            q.name = name.value.trim(); q.kind = kind.value;
            if (!q.name) throw new Error(t('s.needName'));
            setOrDelete(q, 'base_url', kind.value === 'llm_proxy' ? '' : url.value.trim());
            setOrDelete(q, 'api_version', kind.value === 'azure_openai' ? version.value.trim() : '');
            setOrDelete(q, 'api_key_env', env.value.trim());
            var hd = {};
            headers.value.split('\n').forEach(function (line) {
              var i = line.indexOf(':');
              if (i > 0) hd[line.slice(0, i).trim()] = line.slice(i + 1).trim();
            });
            q.headers = hd; pruneEmpty(q, 'headers');
            timeouts.forEach(function (x) { setOrDelete(q, x[0], x[1].value.trim()); });
            setOrDelete(q, 'retries', num(retries));
            q.models = models.filter(function (m) { return m.name; });
            if (!q.models.length) delete q.models;
            return q;
          }
          function typedKey() { return removeKey ? '' : keyIn.value.trim(); }

          var testBtn = UI.btn(t('t.test'), 'play', function () {
            var q;
            try { q = collect(); } catch (e) { showError(err, e); return; }
            err.hidden = true;
            UI.mount(result, h('div.row', h('span.spinner'), h('span.muted', t('t.running'))));
            UI.busy(testBtn, test(q, typedKey() || state.keys[oldName])).then(function (r) { UI.mount(result, testResult(r)); },
              function (e) { UI.mount(result, null); showError(err, e); });
          }, 'ghost');
          var fetchBtn = UI.btn(t('p.fetch'), 'download', function () {
            var q;
            try { q = collect(); } catch (e) { showError(err, e); return; }
            UI.busy(fetchBtn, test(q, typedKey() || state.keys[oldName], true)).then(function (r) {
              var have = {}, n = 0;
              models.forEach(function (m) { have[m.name] = true; });
              (r.models || []).forEach(function (m) {
                if (!have[m.id]) { var x = { name: m.id }; if (m.context) x.context = m.context; models.push(x); n++; }
              });
              drawModels();
              UI.mount(result, r.models_error ? testResult(r) : h('div.callout', UI.icon('check'), ' ', t('t.added', { n: n })));
            }, function (e) { showError(err, e); });
          }, 'ghost sm');

          var keyHint = kept && kept.set ? t('p.keyKept', { masked: kept.masked, source: t('src.k.' + kept.source) }) + ' — ' + t('p.keyHint') : t('p.keyHint');
          var body = h('div.stack',
            h('div.form-row', UI.field(t('p.name'), name), UI.field(t('p.kind'), kind)),
            proxyNote, urlField, versionField,
            h('div.form-row', UI.field(t('p.keyNew'), keyIn, keyHint), UI.field(t('p.env'), env)),
            kept && kept.set && kept.source === 'store' ? check(t('p.keyRemove'), false, function (on) { removeKey = on; keyIn.disabled = on; }) : null,
            UI.field(t('p.headers'), headers, t('p.headersHint')),
            h('div.field', h('span.field__label', t('p.models')), h('span.field__hint', t('p.modelsHint')), modelsEl,
              h('div.row', UI.btn(t('p.addModel'), 'plus', function () { models.push({ name: '' }); drawModels(); }, 'ghost sm'), fetchBtn)),
            h('details', h('summary', t('p.advanced')),
              h('div.form-row', timeouts.map(function (x) { return UI.field(t(x[2]), x[1]); }), UI.field(t('p.retries'), retries))),
            h('div.row', testBtn), result, err);
          kindChanged(); drawModels();
          UI.modal({
            title: index >= 0 ? t('p.edit') : t('p.add'), body: body, wide: true,
            actions: [{ label: t('cancel') },
              { label: t('ok'), kind: 'primary', onclick: function () {
                var q;
                try { q = collect(); } catch (e) { showError(err, e); return false; }
                if (list.some(function (o, i) { return i !== index && o.name === q.name; })) { showError(err, new Error(t('s.dup', { name: q.name }))); return false; }
                if (index >= 0) list[index] = q; else list.push(q);
                if (oldName && oldName !== q.name) {
                  // Renamed: the kept key follows only when it is typed again. / 改名：已保存的密钥需重新输入才会随之保留。
                  if (state.keys[oldName] !== undefined) { state.keys[q.name] = state.keys[oldName]; delete state.keys[oldName]; }
                  if (state.S.default_model && state.S.default_model.provider === oldName) state.S.default_model.provider = q.name;
                }
                var k = typedKey();
                if (removeKey) state.keys[q.name] = '';
                else if (k) state.keys[q.name] = k;
                changed(); draw();
              } }]
          });
        }

        function modelCard() {
          var S = state.S, P = S.params = S.params || {};
          var providers = S.providers || [];
          var dm = S.default_model || {};
          var prov = select([['', t('d.auto')]].concat(providers.map(function (p) { return [p.name, p.name]; })), dm.provider || '', function () {
            if (!prov.value) delete S.default_model; else S.default_model = { provider: prov.value, model: modelIn.value.trim() };
            fillModels(); changed();
          });
          var listId = 'ai-models-' + Math.random().toString(36).slice(2);
          var modelIn = input(dm.model, { list: listId, disabled: !dm.provider, oninput: function () {
            if (S.default_model) S.default_model.model = modelIn.value.trim();
            changed();
          } });
          var dl = h('datalist', { id: listId });
          function fillModels() {
            var p = providers.filter(function (x) { return x.name === prov.value; })[0];
            UI.mount(dl, ((p && p.models) || []).map(function (m) { return h('option', { value: m.name }); }));
            modelIn.disabled = !prov.value;
          }
          fillModels();
          var maxOut = input(P.max_output_tokens, { type: 'number', min: 0, placeholder: '4096', oninput: function () { setOrDelete(P, 'max_output_tokens', num(maxOut)); changed(); } });
          var temp = input(P.temperature, { type: 'number', min: 0, max: 2, step: 0.1, placeholder: '—', oninput: function () { setOrDelete(P, 'temperature', num(temp)); changed(); } });
          var thinking = select(['off', 'low', 'medium', 'high'].map(function (x) { return [x, t('th.' + x)]; }), P.thinking || 'off', function () {
            setOrDelete(P, 'thinking', thinking.value === 'off' ? '' : thinking.value); changed();
          });
          return h('div.stack', h('div.form-row', UI.field(t('d.provider'), prov), UI.field(t('d.model'), modelIn, t('d.modelHint')), dl),
            h('div.form-row', UI.field(t('d.maxOut'), maxOut), UI.field(t('d.temp'), temp), UI.field(t('d.thinking'), thinking)));
        }

        function limitsCard() {
          var L = state.S.limits = state.S.limits || {};
          return h('div.form-row', LIMITS.map(function (x) {
            var i = input(L[x[0]], { type: 'number', min: 0, placeholder: String(x[1]), oninput: function () { setOrDelete(L, x[0], num(i)); changed(); } });
            return UI.field(t('l.' + x[0]), i);
          }));
        }

        function dataCard() {
          var D = state.S.data = state.S.data || {};
          var redact = h('textarea.textarea.textarea--code', { rows: 3, oninput: function () {
            var list = redact.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean);
            setOrDelete(D, 'redact', list.length ? list : undefined); changed();
          } }, (D.redact || []).join('\n'));
          return h('div.stack',
            check(t('a.logs'), D.send_logs, function (on) { setOrDelete(D, 'send_logs', on || undefined); changed(); }),
            check(t('a.access'), D.send_access_log, function (on) { setOrDelete(D, 'send_access_log', on || undefined); changed(); }),
            UI.field(t('a.redact'), redact, t('a.redactHint')));
        }

        function usersCard() {
          var S = state.S;
          var levels = state.view.levels.map(function (l) { return [l, t('lv.' + l)]; });
          var def = select(levels, S.default_level || 'read', function () { setOrDelete(S, 'default_level', def.value === 'read' ? '' : def.value); changed(); draw(); });
          var users = S.users = S.users || [];
          var rows = state.view.users.map(function (name) {
            var u = users.filter(function (x) { return x.username === name; })[0];
            var sel = select([['', t('u.inherit', { level: t('lv.' + (S.default_level || 'read')) })]].concat(levels), u ? u.level : '', function () {
              var i = users.findIndex(function (x) { return x.username === name; });
              if (i >= 0) users.splice(i, 1);
              if (sel.value) users.push({ username: name, level: sel.value });
              changed();
            });
            return { name: name, sel: sel };
          });
          return h('div.stack', h('div.small.muted', t('u.note')), UI.field(t('u.default'), def),
            UI.table([{ label: t('u.user'), render: function (r) { return h('b', r.name); } }, { label: t('u.level'), render: function (r) { return r.sel; } }], rows));
        }

        load();
      }

      // The Functions, Skills, Proposals and Audit log pages are ai/functions.js's, ai/skills.js's, ai/proposals.js's and
      // ai/audit.js's. / 功能页、技能页、提案页与审计日志页分别由 ai/functions.js、ai/skills.js、ai/proposals.js 与 ai/audit.js 提供。
      return { routes: { settings: settingsPage, functions: function () { FitAI.functionsPage(UI, sh); }, skills: function () { FitAI.skillsPage(UI, sh); },
        proposals: function () { FitAI.proposal.page(UI, sh); }, audit: function () { FitAI.auditPage(UI, sh); } } };
    }
  });
})();
