// The assistant's MCP page (GET /api/mcp): the MCP server at /mcp on the console's address, on or off, its settings
// (admin.assistant.mcp, saved with the assistant's settings through PUT /api/settings), how to connect a client, and
// the user's MCP tokens (everyone's for a user whose level is apply): made with POST /api/mcp/tokens and shown once,
// revoked with DELETE /api/mcp/tokens/{id}. A token has its user's level, at most propose; what it proposes waits on
// the Proposals page, applied only there.
//
// 助手的 MCP 页（GET /api/mcp）：控制台地址上 /mcp 处的 MCP 服务器是否开启、其设置（admin.assistant.mcp，经 PUT
// /api/settings 与助手设置一起保存）、如何连接客户端，以及用户的 MCP 令牌（级别为 apply 的用户可见所有人的）：经 POST
// /api/mcp/tokens 创建并只显示一次，经 DELETE /api/mcp/tokens/{id} 吊销。令牌具有其用户的级别，最高为 propose；其提出的
// 提案在提案页等待，只在那里应用。
(function () {
  'use strict';

  var WORDS = [
    ['n.mcp', 'MCP', 'MCP'],
    ['mc.note', 'Outside agents (Claude Code, Cursor, an SDK\'s client) reach this fit\'s assistant over MCP: they call the tools the panel calls, read the config, the schema, the versions and the reference as resources, and use the enabled skills as prompts. Each needs a token of yours, which has your level, at most propose: its proposals wait on the Proposals page, and only a person whose level is apply applies them there. The page tools and ask_user are not served.',
      '外部代理（Claude Code、Cursor、SDK 的客户端）可通过 MCP 使用本 fit 的助手：调用面板所用的工具，以资源形式读取配置、schema、版本与参考文档，并把已启用的技能用作提示词。每个代理都需要你的一个令牌，令牌具有你的级别，最高为 propose：其提案在提案页等待，只有级别为 apply 的人才能在那里应用。页面工具与 ask_user 不对外提供。'],
    ['mc.on', 'on', '已开启'], ['mc.off', 'off', '已关闭'],
    ['mc.server', 'Server', '服务器'], ['mc.endpoint', 'Endpoint', '端点'], ['mc.versions', 'Protocol versions', '协议版本'],
    ['mc.offNote', 'The MCP server is off: turn it on below, or set admin.assistant.mcp in the config.', 'MCP 服务器已关闭：可在下方开启，或在配置中设置 admin.assistant.mcp。'],
    ['mc.settings', 'Settings', '设置'], ['mc.enable', 'Serve MCP at /mcp', '在 /mcp 提供 MCP'],
    ['mc.allow', 'Allowed addresses', '允许的地址'], ['mc.allowHint', 'Addresses and prefixes, one a line; loopback only when empty.', '地址与前缀，每行一个；为空时仅限回环地址。'],
    ['mc.max_tokens_per_user', 'Tokens per user', '每用户令牌数'], ['mc.max_token_days', 'Longest token, days', '令牌最长天数'],
    ['mc.max_sessions', 'Sessions per token (before 2026-07-28)', '每令牌会话数（2026-07-28 之前）'], ['mc.session_idle_timeout', 'Session idle timeout', '会话空闲超时'],
    ['mc.max_request_bytes', 'Request bytes', '请求字节数'], ['mc.max_concurrent_calls', 'Requests at once per token', '每令牌并发请求数'],
    ['mc.max_calls_per_minute', 'Requests a minute per token', '每令牌每分钟请求数'],
    ['mc.save', 'Save settings', '保存设置'], ['mc.saved', 'Settings saved', '设置已保存'],
    ['mc.noStore', 'Without admin.store these settings are admin.assistant.mcp in the config file, and tokens last until fit restarts.',
      '未配置 admin.store：这些设置即配置文件中的 admin.assistant.mcp，令牌在 fit 重启后失效。'],
    ['mc.tokens', 'Tokens', '令牌'], ['mc.make', 'Make a token', '创建令牌'], ['mc.name', 'Name', '名称'], ['mc.level', 'Level', '级别'],
    ['mc.days', 'Days', '天数'], ['mc.tokAllow', 'Only from (optional)', '仅限来源（可选）'], ['mc.user', 'User', '用户'],
    ['mc.created', 'Made', '创建于'], ['mc.expires', 'Expires', '过期于'], ['mc.expired', 'expired', '已过期'], ['mc.last', 'Last used', '最近使用'],
    ['mc.effective', 'now {level}', '当前 {level}'], ['mc.revoke', 'Revoke', '吊销'], ['mc.none', 'No tokens.', '没有令牌。'],
    ['mc.revokeQ', 'Revoke the token {name}? A client using it is refused from its next request.', '吊销令牌 {name}？使用它的客户端从下一个请求起会被拒绝。'],
    ['mc.shown', 'Your new token', '你的新令牌'],
    ['mc.once', 'This is the only time the token is shown: fit keeps only its hash. Copy it now.', '令牌只显示这一次：fit 只保存其哈希。请立即复制。'],
    ['mc.copy', 'Copy', '复制'],
    ['mc.connect', 'Connect a client', '连接客户端'],
    ['mc.claude', 'Claude Code: run this in a terminal, with your token in place of <token>.', 'Claude Code：在终端中运行以下命令，用你的令牌替换 <token>。'],
    ['mc.json', 'Clients configured by JSON (Cursor, .mcp.json):', '以 JSON 配置的客户端（Cursor、.mcp.json）：'],
    ['mc.propose', 'A proposal a token makes waits on the Proposals page.', '令牌提出的提案在提案页等待。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh }, 'ai');

  var NUMS = ['max_tokens_per_user', 'max_token_days', 'max_sessions', 'max_request_bytes', 'max_concurrent_calls', 'max_calls_per_minute'];

  // claudeCommand is how Claude Code adds the server. / Claude Code 添加该服务器的命令。
  function claudeCommand(endpoint, token) {
    return 'claude mcp add --transport http fit ' + endpoint + ' --header "Authorization: Bearer ' + (token || '<token>') + '"';
  }
  function clientJSON(endpoint, token) {
    return JSON.stringify({ mcpServers: { fit: { type: 'http', url: endpoint, headers: { Authorization: 'Bearer ' + (token || '<token>') } } } }, null, 2);
  }

  // mcpPage draws the page into the shell sh. / 在外框 sh 中绘制本页。
  function mcpPage(UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('mcp', t('n.mcp'));
    sh.tools.appendChild(UI.btn('', 'refresh', function () { mcpPage(UI, sh); }, 'ghost icon'));
    function fail(e) { UI.mount(el, h('div.callout.callout--danger', e.message)); }
    function ms(v) { return v ? UI.fmtTime(Math.floor(v / 1000)) : '—'; }

    UI.api('GET', '/api/mcp').then(function (v) {
      var canPropose = v.level === 'propose' || v.level === 'apply';

      // The settings, saved whole with the assistant's through PUT /api/settings. / 设置经 PUT /api/settings 整体保存。
      var on = h('input', { type: 'checkbox', checked: v.enabled });
      var allow = h('textarea.input', { rows: 3 }, (v.settings.allow || []).join('\n'));
      var nums = {};
      NUMS.forEach(function (k) { nums[k] = h('input.input', { type: 'number', min: 0, value: v.settings[k] }); });
      var idle = h('input.input', { value: v.settings.session_idle_timeout });
      var errBox = h('div.callout.callout--danger', { hidden: true });
      function save(b) {
        errBox.hidden = true;
        return UI.busy(b, UI.api('GET', '/api/settings').then(function (cur) {
          var s = cur.settings && typeof cur.settings === 'object' ? JSON.parse(JSON.stringify(cur.settings)) : {};
          if (!on.checked) {
            delete s.mcp;
          } else {
            var m = {};
            var list = allow.value.split(/[\s,]+/).filter(function (x) { return x; });
            if (list.length) m.allow = list;
            NUMS.forEach(function (k) { var n = Number(nums[k].value); if (nums[k].value !== '' && n > 0) m[k] = n; });
            if (idle.value.trim()) m.session_idle_timeout = idle.value.trim();
            s.mcp = m;
          }
          return UI.api('PUT', '/api/settings', { settings: s, keys: {}, version: cur.version });
        })).then(function () { UI.toast(t('mc.saved')); mcpPage(UI, sh); }, function (e) {
          errBox.textContent = e.message; errBox.hidden = false;
        });
      }
      var saveBtn = UI.btn(t('mc.save'), 'save', function () { save(saveBtn); }, 'primary');
      var settings = h('div',
        v.store ? null : h('div.callout', UI.icon('info'), ' ', t('mc.noStore')),
        h('label.check', on, h('span', t('mc.enable'))),
        UI.field(t('mc.allow'), allow, t('mc.allowHint')),
        h('div.grid.grid--3', NUMS.map(function (k) { return UI.field(t('mc.' + k), nums[k]); }), UI.field(t('mc.session_idle_timeout'), idle)),
        errBox, v.store ? h('div.btn-row', saveBtn) : null);

      // Making a token. / 创建令牌。
      var name = h('input.input', { placeholder: 'laptop' });
      var level = h('select.select', v.levels.map(function (l) {
        return h('option', { value: l, disabled: l === 'propose' && !canPropose }, t('lv.' + l));
      }));
      var days = h('input.input', { type: 'number', min: 1, max: v.settings.max_token_days, value: Math.min(30, v.settings.max_token_days) });
      var tokAllow = h('input.input', { placeholder: '127.0.0.1, 10.0.0.0/8' });
      function make(b) {
        var list = tokAllow.value.split(/[\s,]+/).filter(function (x) { return x; });
        return UI.busy(b, UI.api('POST', '/api/mcp/tokens', { name: name.value, level: level.value, days: Number(days.value), allow: list }))
          .then(function (r) {
            var cmd = claudeCommand(v.endpoint, r.token);
            UI.modal({ title: t('mc.shown'), wide: true, onclose: function () { mcpPage(UI, sh); }, body: h('div',
              h('div.callout.callout--warn', UI.icon('alert'), ' ', t('mc.once')),
              h('pre.ai-pre', r.token), UI.btn(t('mc.copy'), 'copy', function () { UI.copy(r.token); }, 'sm'),
              h('p.small', t('mc.claude')), h('pre.ai-pre', cmd), UI.btn(t('mc.copy'), 'copy', function () { UI.copy(cmd); }, 'sm')) });
          }, UI.fail);
      }
      var makeBtn = UI.btn(t('mc.make'), 'key', function () { make(makeBtn); }, 'primary');
      var maker = h('div', h('div.grid.grid--3', UI.field(t('mc.name'), name), UI.field(t('mc.level'), level), UI.field(t('mc.days'), days)),
        UI.field(t('mc.tokAllow'), tokAllow), h('div.btn-row', makeBtn));

      var cols = [{ label: t('mc.name'), render: function (x) { return h('b', x.name); } }];
      if (v.all) cols.push({ label: t('mc.user'), render: function (x) { return x.user; } });
      cols.push(
        { label: t('mc.level'), render: function (x) {
          return h('span', UI.badge(t('lv.' + x.level)), x.effective !== x.level ? h('div.small.muted', t('mc.effective', { level: t('lv.' + x.effective) })) : null);
        } },
        { label: t('mc.created'), render: function (x) { return h('span.small', ms(x.created)); } },
        { label: t('mc.expires'), render: function (x) { return x.expired ? UI.badge(t('mc.expired'), 'danger', true) : h('span.small', ms(x.expires)); } },
        { label: t('mc.last'), render: function (x) { return h('span.small', x.last_used ? ms(x.last_used) + ' · ' + x.last_addr : '—'); } },
        { label: t('mc.tokAllow'), render: function (x) { return x.allow && x.allow.length ? h('code.small', x.allow.join(', ')) : '—'; } },
        { label: '', render: function (x) {
          var b = UI.btn(t('mc.revoke'), 'trash', function () {
            UI.confirm(t('mc.revokeQ', { name: x.name }), t('mc.revoke'), true).then(function (yes) {
              if (yes) UI.busy(b, UI.api('DELETE', '/api/mcp/tokens/' + encodeURIComponent(x.id))).then(function () { mcpPage(UI, sh); }, UI.fail);
            });
          }, 'sm danger');
          return b;
        } });

      UI.mount(el,
        h('div.callout', UI.icon('info'), ' ', t('mc.note')),
        UI.card(t('mc.server'), h('div',
          h('div.row', UI.badge(v.enabled ? t('mc.on') : t('mc.off'), v.enabled ? 'ok' : '', true)),
          v.enabled ? null : h('p.small.muted', t('mc.offNote')),
          UI.kv([[t('mc.endpoint'), h('code', v.endpoint)], [t('mc.versions'), v.versions.join(', ')]]),
          h('p.small', t('mc.propose'), ' ', h('a', { href: '#/ai/proposals' }, v.proposals)))),
        UI.card(t('mc.settings'), settings),
        UI.card(t('mc.tokens'), h('div', maker, UI.table(cols, v.tokens, { empty: t('mc.none') }))),
        UI.card(t('mc.connect'), h('div',
          h('p.small', t('mc.claude')), h('pre.ai-pre', claudeCommand(v.endpoint)),
          h('p.small', t('mc.json')), h('pre.ai-pre', clientJSON(v.endpoint)))));
    }, fail);
  }

  window.FitAI = window.FitAI || {};
  window.FitAI.mcpPage = mcpPage;
})();
