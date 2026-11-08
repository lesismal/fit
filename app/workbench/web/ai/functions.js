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
    ['fn.note', 'The tools the assistant may call. Each reads what this fit runs, on the console\'s pool, through the Admin API in process; secrets are redacted from every result, and each result is bounded. None writes the config, a version or the store: the page tools only draft into the open page, which you save yourself, and the propose tools only add steps to a proposal, which a person whose level is apply applies after a second confirm.',
      '助手可以调用的工具。每个工具都在控制台的线程池上、经进程内的 Admin API 读取本 fit 的运行情况；每个结果都会脱敏并受大小限制。没有任何工具会写入配置、版本或 store：页面工具只在打开的页面上起草，由你自己保存；提案工具只向提案加入步骤，由级别为 apply 的人再次确认后应用。'],
    ['fn.name', 'Tool', '工具'], ['fn.needs', 'Level', '级别'], ['fn.state', 'State', '状态'], ['fn.what', 'What it does', '作用'],
    ['fn.on', 'enabled', '已启用'], ['fn.off', 'disabled', '未启用'], ['fn.schema', 'Arguments', '参数'],
    ['g.config', 'Config', '配置'], ['g.runtime', 'Runtime', '运行时'], ['g.roles', 'Roles', '角色'], ['g.media', 'Media', '媒体'], ['g.sync', 'Sync', '数据同步'], ['g.cluster', 'Cluster', '集群'], ['g.logs', 'Logs', '日志'],
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
    ['t.ask_user', 'Asks you a question with options, shown as buttons in the panel; your choice is the result.', '向你提出带选项的问题（在面板中显示为按钮），你的选择即为结果。'],
    ['t.skill_load', 'Loads an enabled skill the assistant picked from the catalog: its whole prompt, and from then on only the tools it names that your level allows.',
      '加载助手从目录中选出的已启用技能：其完整提示词；此后只使用它所列且你的级别允许的工具。'],
    ['why.needs the level propose', 'needs the level propose', '需要 propose 级别'],
    ['why.needs the level edit', 'needs the level edit', '需要 edit 级别'],
    ['t.page_get', 'Reads what the open page holds, your unsaved edits included: the members it edits as its draft has them, redacted. Runs in the page.',
      '读取当前打开页面的内容（含你未保存的修改）：其所编辑成员在草稿中的值，已脱敏。在页面中执行。'],
    ['t.page_edit', 'Drafts the change you asked for into the open page\'s form or text tab, marked as the assistant\'s and unsaved. Never submits or saves: Save and apply stays your click.',
      '把你要求的修改起草到当前页面的表单或文本标签页中，标为助手所写且未保存。从不提交或保存：“保存并应用”始终由你点击。'],
    ['t.page_revert', 'Takes the assistant\'s drafts off the open page, back to what it held before them.', '从当前页面撤回助手的草稿，恢复到起草之前的内容。'],
    ['t.config_propose', 'Adds a config change to the conversation\'s proposal: top-level members, or operations at JSON pointers; checked as a whole, never applied by the assistant.',
      '向对话的提案加入一项配置变更：顶层成员，或按 JSON 指针的操作；整体检查，助手从不自行应用。'],
    ['t.reconcile_propose', 'Adds Consistency fixes to the proposal, by difference and action; applied together through the reconcile endpoint.', '向提案加入一致性修复（按差异与操作）；经 reconcile 接口一起应用。'],
    ['t.rollback_propose', 'Proposes applying a kept version again, as a new version.', '提议将保存的某个版本重新应用为新版本。'],
    ['t.cache_purge_propose', 'Adds a cache purge, by zone, URL, tag or all, run after the proposal\'s config change.', '加入一次缓存清除（按缓存区、URL、标签或全部），在提案的配置变更之后执行。'],
    ['t.media_library_search', 'The media libraries, or one library\'s items searched and filtered by codec, resolution and probe errors, paged.', '列出媒体库，或按编码、分辨率与探测错误搜索、过滤某个库的条目（分页）。'],
    ['t.media_item_get', 'An item\'s tracks, codecs, renditions, subtitles, sidecar and probe errors, and what uses it now.', '条目的轨道、编码、码率版本、字幕、sidecar 与探测错误，以及当前使用它的频道与会话。'],
    ['t.channels_status', 'Each channel\'s now and next with the position, and its push targets\' state, bytes and last error; never a stream key.', '各频道正在与即将播放的条目及进度，以及推流目标的状态、字节数与最近错误；从不含推流密钥。'],
    ['t.playback_sessions', 'Playback sessions by resource and address, each subject\'s against max_sessions, and the 403s by reason; subjects hashed unless data.send_subjects.', '按资源与地址列出播放会话、各主体相对 max_sessions 的数量与按原因统计的 403；除非设置 data.send_subjects，主体以哈希显示。'],
    ['t.media_scan_propose', 'Adds a rescan of a library to the proposal.', '向提案加入一次媒体库重新扫描。'],
    ['t.media_session_end_propose', 'Adds the end of playback sessions to the proposal, by subject and session.', '向提案加入结束播放会话（按主体与会话）。'],
    ['t.media_key_rotate_propose', 'Adds the rotation of a media_auth key, or all, to the proposal; the new secret is never shown.', '向提案加入 media_auth 某个或全部密钥的轮换；新密钥从不显示。'],
    ['t.media_file_propose', 'Adds a rename, move, delete or sidecar edit of a library\'s files to the proposal, with what plays them now.', '向提案加入媒体库文件的重命名、移动、删除或 sidecar 编辑，并附上当前播放它们的频道与观众。'],
    ['t.sync_pipelines', 'The sync pipelines\' state, counts, throughput, lag and last error; one pipeline\'s whole status and its config.', '各同步管道的状态、计数、吞吐、延迟与最近的错误；或单个管道的完整状态及其配置。'],
    ['t.sync_test', 'Tests a running pipeline\'s source and sink connections, writing no record.', '测试运行中管道的源与 sink 连接，不写入任何记录。'],
    ['t.sync_dry_run', 'Reads a pipeline\'s first records through its transforms, writing nothing; values as their kinds unless data.send_records.', '读取管道的前几条记录并经过各转换，不写入任何内容；除非设置 data.send_records，值只显示其类型。'],
    ['t.sync_dead_letters', 'A pipeline\'s dead letters: error, where it failed and attempts; values as their kinds unless data.send_records.', '管道的死信：错误、失败位置与尝试次数；除非设置 data.send_records，值只显示其类型。'],
    ['t.cluster_members', 'This manager\'s members as last polled: health, versions, config version, drift, sync, certificates, alerts; by group, label or status.',
      '本管理端成员的最近轮询状态：健康、版本、配置版本、漂移、同步、证书与告警；可按分组、标签或状态筛选。'],
    ['t.cluster_templates', 'The templates, their parts, variables and groups, and each member\'s sync with them.', '模板及其拥有的部分、变量与分组，以及各成员与模板的同步状态。'],
    ['t.cluster_rollouts', 'Rollouts planned, running and past, batch by batch with each member\'s result, and the latest operations.', '已规划、进行中与过去的发布（逐批及各成员结果），以及最近的操作。'],
    ['t.cluster_metrics', 'Each member\'s requests, connections, reloads and backends down, read now, and their sums by group; a slow or down member is reported, not waited for.',
      '各成员当前读取的请求、连接、重载与不可用后端，以及按分组的汇总；慢或离线的成员只报告、不等待。'],
    ['t.cluster_certs', 'The certificate inventory across the members, as polled, the one that expires first first.', '各成员证书清单（按轮询读取），最先过期的在前。'],
    ['t.cluster_log_tail', 'The latest lines of up to 16 members\' own logs, merged by time; a slow or down member is reported, not waited for.', '最多 16 个成员自身日志的最新行，按时间合并；慢或离线的成员只报告、不等待。'],
    ['t.rollout_propose', 'Plans a change across members as a rollout (a template\'s draft, a sync, a patch or a copy): each member\'s diff and dry run; a person starts it on the Rollouts page.',
      '把跨成员的修改规划为一次发布（模板草稿、同步、修改或复制）：各成员的差异与试运行；由人在发布页启动。'],
    ['t.cluster_op_propose', 'Checks an operation on many members (reload, rollback, check, purge, test) without starting it, and links the operations page filled in.',
      '检查对多个成员的操作（重载、回滚、检查、清除缓存、测试）但不启动，并链接到预填的操作页。'],
    ['t.drift_fix_propose', 'Plans the fix of members\' drift: a sync rollout, or a reload for a consistency difference; started by a person.',
      '规划成员漂移的修复：同步发布，或针对一致性差异的重载；由人启动。']
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
