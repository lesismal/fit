// The assistant's Functions page: every tool of the registry (GET /api/tools), by group, with the level a user needs
// for it, whether this user may call it under the settings in use and why not, what it does, and the JSON Schema of
// its arguments. The model is told what a tool does in English; the page says it in the workbench's language.
//
// 助手的功能页：注册表中的每个工具（GET /api/tools），按组列出：所需级别、当前设置下本用户能否调用及原因、作用说明，以及参数
// 的 JSON Schema。工具说明以英文提供给模型；本页以工作台的语言显示。
(function () {
  'use strict';

  // [key, en, zh]: the page's words, and each tool's description. / 页面的词条，以及每个工具的说明。
  var WORDS = [
    ['fn.title', 'Functions', '功能'], ['fn.level', 'Your level: {level}', '你的级别：{level}'],
    ['fn.model', 'Model: {model}', '模型：{model}'], ['fn.noModel', 'No model is set yet.', '尚未设置模型。'],
    ['fn.useTools', 'calls tools', '调用工具'], ['fn.noTools', 'calls no tools', '不调用工具'],
    ['fn.note', 'The tools the assistant may call. Each reads what this fit runs, on the console\'s pool, through the Admin API in process; secrets are redacted from every result, and each result is bounded. None writes the config, a version or the store.',
      '助手可以调用的工具。每个工具都在控制台的线程池上、经进程内的 Admin API 读取本 fit 的运行情况；每个结果都会脱敏并受大小限制。没有任何工具会写入配置、版本或 store。'],
    ['fn.name', 'Tool', '工具'], ['fn.needs', 'Level', '级别'], ['fn.state', 'State', '状态'], ['fn.what', 'What it does', '作用'],
    ['fn.on', 'enabled', '已启用'], ['fn.off', 'disabled', '未启用'], ['fn.schema', 'Arguments', '参数'],
    ['g.config', 'Config', '配置'], ['g.runtime', 'Runtime', '运行时'], ['g.roles', 'Roles', '角色'], ['g.logs', 'Logs', '日志'],
    ['g.page', 'Page', '页面'], ['g.change', 'Change', '变更'], ['g.convert', 'Convert', '转换'], ['g.agent', 'Agent', '代理'],
    ['why.data.send_logs is off', 'off: data.send_logs is not set', '未启用：未设置 data.send_logs'],
    ['why.data.send_access_log is off', 'off: data.send_access_log is not set', '未启用：未设置 data.send_access_log'],
    ['why.the model does not call tools', 'the model calls no tools', '该模型不调用工具'],
    ['t.config_get', 'Reads the base or the effective config, whole or by top-level members, as JSON, .conf or YAML, redacted.', '读取基础配置或生效配置（整体或按顶层成员），格式为 JSON、.conf 或 YAML，已脱敏。'],
    ['t.schema_describe', 'Describes a member of the config from its schema: type, values, default and members, in English or Chinese.', '根据 schema 描述配置的某个成员：类型、取值、默认值与子成员，英文或中文。'],
    ['t.config_validate', 'Checks config text, or a patch over the running config, as fit validate does, with each error\'s line and column.', '像 fit validate 一样检查配置文本或基于运行配置的补丁，给出每个错误的行与列。'],
    ['t.versions_diff', 'Lists the kept versions, and what changed between two of them.', '列出保存的版本，以及两个版本之间的变化。'],
    ['t.status_get', 'What runs now: version, listeners, connections, upstreams, roles, engine limits and uptime.', '当前运行情况：版本、监听器、连接、上游、角色、引擎限制与运行时长。'],
    ['t.metrics_query', 'The metrics as they stand, filtered by name, route, upstream or listener.', '当前指标，可按名称、路由、上游或监听器筛选。'],
    ['t.upstream_health', 'Each upstream pool\'s backends: up or down, in flight, succeeded and failed.', '每个上游池的后端：是否可用、进行中、成功与失败数。'],
    ['t.consistency_check', 'The differences between what runs and the config, as the Consistency page shows them.', '运行状态与配置之间的差异，与一致性页面相同。'],
    ['t.certs_list', 'The certificates served, their names, issuers and days left, with the ACME settings.', '提供的证书、名称、签发者与剩余天数，以及 ACME 设置。'],
    ['t.cache_zones', 'The cache\'s zones, their sizes and hit rates.', '缓存区、大小与命中率。'],
    ['t.log_tail', 'The latest lines of fit\'s own log, from a bounded ring in memory, by logger, level and text.', 'fit 自身日志的最新行（来自内存中的有界环形缓冲），可按记录器、级别与文字筛选。'],
    ['t.access_log_sample', 'Recent lines of the access log\'s file matching a route, status, client or text.', '访问日志文件中与路由、状态码、客户端或文字匹配的最近行。'],
    ['t.adapt_convert', 'Converts nginx or Caddyfile text to fit\'s config, as fit adapt does, with what it could not convert.', '像 fit adapt 一样把 nginx 或 Caddyfile 文本转换为 fit 配置，并列出无法转换的部分。'],
    ['t.ask_user', 'Asks you a question with options, shown as buttons in the panel; your choice is the result.', '向你提出带选项的问题（在面板中显示为按钮），你的选择即为结果。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh }, 'ai');

  // functionsPage draws the page into the shell sh. / 在外框 sh 中绘制本页。
  function functionsPage(UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('functions', t('fn.title'));
    sh.tools.appendChild(UI.btn('', 'refresh', function () { functionsPage(UI, sh); }, 'ghost icon'));
    UI.api('GET', '/api/tools').then(function (b) {
      var level = t('lv.' + b.level);
      var model = b.model ? t('fn.model', { model: b.provider + ' / ' + b.model }) + ' (' + (b.use_tools ? t('fn.useTools') : t('fn.noTools')) + ')' : t('fn.noModel');
      var groups = [], by = {};
      (b.tools || []).forEach(function (x) {
        if (!by[x.group]) { by[x.group] = []; groups.push(x.group); }
        by[x.group].push(x);
      });
      var desc = function (x) { var k = 't.' + x.name, w = t(k); return w === k ? x.description : w; };
      var why = function (x) { var k = 'why.' + x.why, w = t(k); return w === k ? x.why : w; };
      UI.mount(el,
        h('div.callout', UI.icon('info'), ' ', t('fn.note')),
        h('div.row', UI.badge(t('fn.level', { level: level }), b.level === 'off' ? 'danger' : 'ok', true), h('span.small.muted', model)),
        groups.map(function (g) {
          return UI.card(t('g.' + g), UI.table([
            { label: t('fn.name'), render: function (x) { return h('code', x.name); } },
            { label: t('fn.needs'), render: function (x) { return UI.badge(t('lv.' + x.level)); } },
            { label: t('fn.state'), render: function (x) { return x.enabled ? UI.badge(t('fn.on'), 'ok', true) : h('span', UI.badge(t('fn.off'), '', true), x.why ? h('div.small.muted', why(x)) : null); } },
            { label: t('fn.what'), render: function (x) { return h('span', desc(x)); } },
            { label: t('fn.schema'), render: function (x) {
              return h('details.ai-fold', h('summary.small', t('fn.schema')), h('pre.ai-pre', JSON.stringify(x.parameters, null, 2)));
            } }
          ], by[g]), null, true);
        }));
    }, function (e) { UI.mount(el, h('div.callout.callout--danger', e.message)); });
  }

  window.FitAI = window.FitAI || {};
  window.FitAI.functionsPage = functionsPage;
})();
