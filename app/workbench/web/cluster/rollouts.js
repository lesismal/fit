// Cluster → Rollouts (I146): one change applied to many members in stages. A rollout is planned from a template's change
// (its members' desired configs), a sync of members with their templates, or parts of one member's config copied to
// others; the plan reads each member, takes its diff and has the member check it by its own dry run (PUT /config?dry_run=1
// with If-Match). A person then starts it with a strategy: all at once, rolling by batch size or percentage with a pause
// between batches, or a canary of the first members that waits for the person to go on; after each batch a health gate
// checks each member (reachable, running the config applied, no new drift, and if set its share of 5xx over a window).
// A failed gate stops the rollout and rolls back its batch, or all of it; a member changed since the plan is skipped.
//
// The pages: #/cluster/rollouts, the rollouts kept, the latest first (the history), and New rollout;
// #/cluster/newrollout (with ?kind=template&template=<name>, ?kind=sync&nodes=a,b or ?kind=copy&from=<name>), the form
// of a new plan; #/cluster/rollout/<id>, a rollout: while planned, each member's diff and dry run (its errors linked to
// the member's pages), the members to leave out, the strategy form and the confirm dialog with the diff; once started,
// its progress batch by batch with each member's gate, and Pause, Resume, Go on, Abort and Roll back. It calls
// /api/m/cluster/rollouts (docs/cluster.md). Needs shared.js and templates.js (its side-by-side diff); before cluster.js.
//
// 集群 → 发布（I146）：一次修改分阶段应用到多个成员。发布可由模板的修改（其成员的期望配置）、成员与模板的同步，或把一个成员配置
// 的部分复制到其他成员而规划；规划会读取每个成员、取得其差异，并由成员自己的试运行检查（带 If-Match 的
// PUT /config?dry_run=1）。之后由人选择策略启动：一次全部、按批次大小或百分比滚动（批次间可暂停），或先发布金丝雀成员并等待人
// 确认继续；每批之后有健康检查（可达、运行所应用的配置、没有新的不一致，以及设置时一段时间内 5xx 的比例）。检查失败会停止发布并
// 回滚该批或全部；规划后在成员上被修改过的成员会被跳过。
// 页面：#/cluster/rollouts 保存的发布（最新在前，即历史）与“新建发布”；#/cluster/newrollout（可带
// ?kind=template&template=<name>、?kind=sync&nodes=a,b 或 ?kind=copy&from=<name>）新规划的表单；#/cluster/rollout/<id> 一次发布：
// 规划完成时显示每个成员的差异与试运行（错误链接到成员页面）、可排除的成员、策略表单以及带差异的确认对话框；启动后逐批显示进度与
// 每个成员的检查结果，以及暂停、继续、继续发布、中止与回滚。调用 /api/m/cluster/rollouts（见 docs/cluster.md）。依赖 shared.js 与
// templates.js（其并排差异）；在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.rollouts', 'Rollouts', '发布'],
    ['ro.new', 'New rollout', '新建发布'], ['ro.back', 'All rollouts', '全部发布'],
    ['ro.none', 'No rollouts yet. A rollout applies a template\'s change, a sync with the templates, or parts copied from one member, to many members in stages, each checked first by the member\'s own dry run.',
      '还没有发布。发布把模板的修改、与模板的同步，或从一个成员复制的部分，分阶段应用到多个成员，每个成员先经其自己的试运行检查。'],
    ['ro.history', 'History', '历史'], ['ro.when', 'When', '时间'], ['ro.what', 'What', '内容'], ['ro.state', 'State', '状态'],
    ['ro.members', 'Members', '成员'], ['ro.who', 'By', '操作人'], ['ro.notfound', 'No rollout {id} is kept.', '没有保存发布 {id}。'],
    ['ro.max', 'At most {n} rollouts are planned or under way at once; plans not started within {min} minutes expire.',
      '同时最多 {n} 个发布处于规划或进行中；规划后 {min} 分钟内未启动即过期。'],
    // Kinds / 类型
    ['ro.kind', 'What to roll out', '发布内容'], ['ro.k.template', 'A template\'s change', '模板的修改'], ['ro.k.sync', 'A sync with the templates', '与模板同步'],
    ['ro.k.copy', 'Parts copied from a member', '从成员复制部分配置'], ['ro.k.patch', 'Parts written here', '在此填写的部分配置'],
    ['ro.k.patch.hint', 'Top-level members whole, and routes, upstreams, listeners and services by name, put on the members picked or a group\'s as written: config no template owns. A secret written as [secret] keeps each member\'s own.',
      '把顶层成员（整体）以及按名称的路由、上游、监听与服务按所写内容放到所选成员或分组成员上：不属于任何模板的配置。写作 [secret] 的密钥保留各成员自己的值。'],
    ['ro.config', 'Config (JSON)', '配置（JSON）'], ['ro.draft', 'A draft of the template\'s config (JSON), optional', '模板配置的草稿（JSON，可选）'],
    ['ro.draft.hint', 'Planned on its members without saving the template; starting the rollout saves it, which a cluster-wide admin may. Empty: the template as it is.',
      '在其成员上规划而不保存模板；启动发布时保存模板（需要集群范围的管理员）。留空：按模板现状。'],
    ['ro.badjson', 'The config is not a JSON object.', '配置不是 JSON 对象。'],
    ['ro.nodes.patch', 'The members it changes; none picked: the group\'s.', '要修改的成员；未选择时：分组的成员。'],
    ['ro.from.patch', 'a patch of {p}', '修改 {p}'], ['ro.from.draft', 'a draft of the template {t}', '模板 {t} 的草稿'],
    ['ro.byai', 'planned by the assistant for {p}', '由助手为 {p} 规划'],
    ['ro.byai.started', 'planned by the assistant for {p}, started by {s}', '由助手为 {p} 规划，{s} 启动'],
    ['ro.cfg.draft', 'The template\'s draft, saved as the template when the rollout starts', '模板的草稿，在发布启动时保存为模板'],
    ['ro.cfg.patch', 'The parts the patch puts on the members', '此次修改放到成员上的部分'],
    ['ro.suggested', 'The strategy is the one suggested by the assistant; change it as you see fit.', '策略为助手建议的策略，可按需修改。'],
    ['ro.confirm.draft', 'Starting it first saves the draft as the template {t}, as your change, then applies it.', '启动时先以你的名义把草稿保存为模板 {t}，然后应用。'],
    ['ro.k.template.hint', 'Every member of the template\'s groups (or those picked) gets its desired config: its own with the parts its templates own taken from them.',
      '模板所在分组的每个成员（或所选成员）获得其期望配置：自身配置中由模板拥有的部分替换为模板给出的值。'],
    ['ro.k.sync.hint', 'The members picked, a group\'s, or every member, each brought in sync with all its templates.',
      '所选成员、某个分组的成员或全部成员，各自与其所有模板同步。'],
    ['ro.k.copy.hint', 'Top-level members whole, and routes, upstreams, listeners and services by name, copied from one member to others as it has them now, secrets and all.',
      '把一个成员当前的顶层成员（整体）以及按名称的路由、上游、监听与服务（含密钥）复制到其他成员。'],
    ['ro.template', 'Template', '模板'], ['ro.group', 'Group', '分组'], ['ro.nodes', 'Members', '成员'], ['ro.from', 'Copied from', '复制来源'],
    ['ro.paths', 'Parts', '部分'], ['ro.paths.hint', 'One a line, as log or routes[api].', '每行一个，如 log 或 routes[api]。'],
    ['ro.anygroup', 'Every member', '全部成员'], ['ro.desc', 'Description', '说明'],
    ['ro.nodes.template', 'None picked: every member of the template\'s groups.', '未选择时：模板所在分组的全部成员。'],
    ['ro.nodes.sync', 'None picked: the group\'s members, or every member.', '未选择时：分组的成员，或全部成员。'],
    ['ro.nodes.copy', 'The members it is copied to; none picked: the group\'s.', '复制到的成员；未选择时：分组的成员。'],
    ['ro.plan', 'Plan', '规划'], ['ro.planning', 'Planning: each member is read, its desired config made, and checked by its own dry run…',
      '规划中：读取每个成员、生成期望配置，并由其自己的试运行检查……'],
    // States / 状态
    ['ro.s.planning', 'Planning', '规划中'], ['ro.s.planned', 'Planned', '已规划'], ['ro.s.running', 'Running', '进行中'],
    ['ro.s.paused', 'Paused', '已暂停'], ['ro.s.waiting', 'Waiting for go', '等待继续'], ['ro.s.rolling_back', 'Rolling back', '回滚中'],
    ['ro.s.done', 'Done', '已完成'], ['ro.s.failed', 'Failed', '失败'], ['ro.s.aborted', 'Aborted', '已中止'], ['ro.s.rolled_back', 'Rolled back', '已回滚'],
    ['ro.s.expired', 'Expired', '已过期'],
    ['ro.m.planning', 'Reading', '读取中'], ['ro.m.ready', 'Ready', '就绪'], ['ro.m.unchanged', 'Nothing to change', '无需修改'],
    ['ro.m.error', 'Not ready', '未就绪'], ['ro.m.excluded', 'Left out', '已排除'], ['ro.m.pending', 'Pending', '等待中'],
    ['ro.m.applying', 'Applying', '应用中'], ['ro.m.applied', 'Applied, gate pending', '已应用，等待检查'], ['ro.m.done', 'Done', '完成'],
    ['ro.m.failed', 'Failed', '失败'], ['ro.m.skipped', 'Skipped: changed since the plan', '已跳过：规划后被修改'],
    ['ro.m.rolled_back', 'Rolled back', '已回滚'], ['ro.m.rollback_failed', 'Rollback failed', '回滚失败'],
    ['ro.b.pending', 'Pending', '等待中'], ['ro.b.applying', 'Applying', '应用中'], ['ro.b.gating', 'Health gate', '健康检查中'],
    ['ro.b.passed', 'Passed', '已通过'], ['ro.b.failed', 'Failed', '失败'], ['ro.b.rolled_back', 'Rolled back', '已回滚'], ['ro.b.aborted', 'Aborted', '已中止'],
    // The plan / 规划
    ['ro.plan.title', 'The plan', '规划'], ['ro.changes', 'Changes', '修改'], ['ro.diff', 'Diff', '差异'], ['ro.have', 'On the member now', '成员当前'],
    ['ro.want', 'After the rollout', '发布之后'], ['ro.absent', '(not there)', '（不存在）'], ['ro.c.added', 'added', '新增'], ['ro.c.differs', 'changed', '修改'],
    ['ro.dry', 'Dry run', '试运行'], ['ro.dry.ok', 'checked by the member: from version {v}', '已由成员检查：基于版本 {v}'],
    ['ro.errors', 'The member\'s dry run refused it:', '成员的试运行拒绝了它：'], ['ro.openmember', 'The member\'s page', '成员页面'],
    ['ro.openpages', 'Its own pages', '其自身页面'], ['ro.rendered', 'Rendered result', '渲染结果'], ['ro.include', 'Include', '包含'],
    ['ro.none.ready', 'No member is ready: nothing to roll out.', '没有就绪的成员：无可发布。'],
    ['ro.discard', 'Discard the plan', '放弃规划'], ['ro.discard.ask', 'Discard this plan? Nothing was applied.', '放弃此规划？尚未应用任何内容。'],
    ['ro.discarded', 'The plan was discarded.', '规划已放弃。'],
    // The strategy / 策略
    ['ro.strategy', 'Strategy', '策略'], ['ro.mode', 'How', '方式'], ['ro.mode.all', 'All at once', '一次全部'],
    ['ro.mode.rolling', 'Rolling, in batches', '滚动，分批'], ['ro.mode.canary', 'A canary first, then the rest', '先金丝雀，再其余'],
    ['ro.batch', 'Batch', '批次'], ['ro.batch.size', 'members a batch', '个成员一批'], ['ro.batch.pct', '% of the members a batch', '% 的成员一批'],
    ['ro.batch.hint', 'For a canary, of the members after it; empty for the rest at once.', '对金丝雀而言指其后的成员；留空则其余一次全部。'],
    ['ro.pause', 'Pause between batches (s)', '批次间暂停（秒）'], ['ro.canary', 'Canary members', '金丝雀成员数'],
    ['ro.canary.hint', 'The first members, by name, go first; the rollout then waits for you to go on.', '按名称排在最前的成员先发布；随后发布等待你确认继续。'],
    ['ro.gate', 'Health gate', '健康检查'], ['ro.gate.wait', 'Check after (s)', '检查等待（秒）'],
    ['ro.gate.hint', 'After each batch, each member must be reachable, run the config the rollout applied, and find no more differences in its consistency check than before.',
      '每批之后，每个成员都必须可达、运行发布所应用的配置，且其一致性检查发现的不一致不多于发布前。'],
    ['ro.gate.ratio', 'At most 5xx (%)', '5xx 至多（%）'], ['ro.gate.ratio.hint', 'Empty: not gated. Needs metrics in the members\' configs.',
      '留空则不检查。需要成员配置中设置 metrics。'],
    ['ro.gate.window', 'Over (s)', '统计时长（秒）'], ['ro.gate.min', 'From requests', '最少请求数'],
    ['ro.onfail', 'A failed gate rolls back', '检查失败时回滚'], ['ro.onfail.batch', 'its batch', '该批'], ['ro.onfail.all', 'every member applied', '所有已应用的成员'],
    ['ro.start', 'Start the rollout', '启动发布'], ['ro.confirm.title', 'Start the rollout?', '启动发布？'],
    ['ro.confirm.note', '{n} members in {b} batches. Each gets its desired config by its own apply, with If-Match: a member changed since the plan is skipped, never overwritten.',
      '{n} 个成员，共 {b} 批。每个成员通过其自身的应用接口（带 If-Match）获得期望配置：规划后被修改的成员会被跳过，绝不覆盖。'],
    ['ro.confirm.batch', 'Batch {n}', '第 {n} 批'], ['ro.confirm.canary', 'canary', '金丝雀'], ['ro.started', 'The rollout started.', '发布已启动。'],
    // Progress / 进度
    ['ro.progress', 'Progress', '进度'], ['ro.next', 'Next batch {at}', '下一批 {at}'], ['ro.gate.at', 'gate at {at}', '检查时间 {at}'],
    ['ro.pause.btn', 'Pause', '暂停'], ['ro.resume', 'Resume', '继续'], ['ro.go', 'Go on', '继续发布'], ['ro.abort', 'Abort', '中止'],
    ['ro.rollback', 'Roll back', '回滚'],
    ['ro.abort.ask', 'Abort the rollout? What was applied stays; roll it back after if you want.', '中止发布？已应用的内容保留；之后如需可回滚。'],
    ['ro.rollback.ask', 'Roll back every member this rollout applied, each to the version it ran before, by its own rollback with If-Match? A member changed since is left as it is.',
      '把此发布应用过的每个成员回滚到发布前运行的版本（通过其自身带 If-Match 的回滚）？之后被修改过的成员保持不变。'],
    ['ro.g.passed', 'gate passed', '检查通过'], ['ro.g.failed', 'gate failed', '检查失败'], ['ro.g.ratio', '{e} of {n} requests 5xx ({p}%)', '{n} 个请求中 {e} 个 5xx（{p}%）'],
    ['ro.g.drift', 'drift {a} (before {b})', '不一致 {a}（之前 {b}）'],
    ['ro.v', 'v{a} → v{b}', 'v{a} → v{b}'], ['ro.v.back', 'rolled back as v{n}', '回滚为 v{n}'],
    ['ro.reason', 'Why', '原因'], ['ro.by', 'planned by {p}, started by {s}', '{p} 规划，{s} 启动'], ['ro.byplan', 'planned by {p}', '{p} 规划'],
    ['ro.from.tpl', 'the template {t}', '模板 {t}'], ['ro.from.sync', 'a sync with the templates', '与模板同步'], ['ro.from.copy', '{p} from {f}', '从 {f} 复制 {p}'],
    ['ro.rollout', 'Roll out', '发布'], ['ro.syncthis', 'Sync this node', '同步此节点'],
    ['nd.a.rollout.plan', 'rollout planned', '规划发布'], ['nd.a.rollout.start', 'rollout started', '启动发布'], ['nd.a.rollout.end', 'rollout ended', '发布结束'],
    ['nd.a.rollout.pause', 'rollout paused', '暂停发布'], ['nd.a.rollout.resume', 'rollout resumed', '继续发布'], ['nd.a.rollout.go', 'rollout went on', '金丝雀后继续'],
    ['nd.a.rollout.abort', 'rollout aborted', '中止发布'], ['nd.a.rollout.rollback', 'rollout rolled back', '回滚发布'], ['nd.a.rollout.discard', 'plan discarded', '放弃规划']
  );

  var STATE_KIND = { planning: 'info', planned: 'info', running: 'info', paused: 'warn', waiting: 'warn', rolling_back: 'warn',
    done: 'ok', failed: 'danger', aborted: 'warn', rolled_back: '', expired: '' };
  var MEMBER_KIND = { planning: 'info', ready: 'ok', unchanged: '', error: 'danger', excluded: '', pending: '', applying: 'info', applied: 'info',
    done: 'ok', failed: 'danger', skipped: 'warn', rolled_back: 'warn', rollback_failed: 'danger' };
  var BATCH_KIND = { pending: '', applying: 'info', gating: 'info', passed: 'ok', failed: 'danger', rolled_back: 'warn', aborted: 'warn' };
  var ACTIVE = ['planning', 'running', 'rolling_back'];

  function path(id, action) { return '/api/rollouts/' + encodeURIComponent(id) + (action ? '/' + action : ''); }
  function stateBadge(UI, s) { return UI.badge(UI.t('ro.s.' + s), STATE_KIND[s] || '', true); }
  function memberBadge(UI, s) { return UI.badge(UI.t('ro.m.' + s), MEMBER_KIND[s] || '', true); }
  function pretty(v) { return v === undefined || v === null ? '' : JSON.stringify(v, null, 2); }
  // query reads the hash's query. / 读取哈希中的查询参数。
  function query() {
    var q = {}, i = location.hash.indexOf('?');
    if (i < 0) return q;
    location.hash.slice(i + 1).split('&').forEach(function (p) {
      var j = p.indexOf('=');
      if (j > 0) q[decodeURIComponent(p.slice(0, j))] = decodeURIComponent(p.slice(j + 1));
    });
    return q;
  }
  // what says what a rollout changes. / 描述发布的内容。
  function what(UI, r) {
    var t = UI.t;
    if (r.kind === 'template') return t(r.config ? 'ro.from.draft' : 'ro.from.tpl', { t: r.template });
    if (r.kind === 'copy') return t('ro.from.copy', { p: (r.paths || []).join(', '), f: r.from });
    if (r.kind === 'patch') return t('ro.from.patch', { p: Object.keys(r.config || {}).join(', ') }) + (r.group ? ' · ' + r.group : '');
    return t('ro.from.sync') + (r.group ? ' · ' + r.group : '');
  }
  function countBadges(UI, r) {
    var c = r.counts || {};
    return UI.h('span.btn-row', Object.keys(c).sort().map(function (k) { return UI.badge(c[k] + ' ' + UI.t('ro.m.' + k), MEMBER_KIND[k] || ''); }));
  }

  // ---- The list and the history / 列表与历史 ----
  FC.pages.rollouts = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('rollouts', t('n.rollouts'));
    var head = h('div.row.row--between.cl-bar'), body = h('div');
    UI.mount(el, h('div.stack', head, body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    function read() {
      FC.call(UI, 'GET', '/api/rollouts').then(function (r) {
        if (!document.body.contains(el)) return;
        var b = r.body || {}, list = b.rollouts || [];
        var add = h('a.btn.btn--primary', { href: '#/cluster/newrollout' }, UI.icon('plus'), t('ro.new'));
        if (b.read_only) { add = UI.btn(t('ro.new'), 'plus', null, 'primary'); add.disabled = true; }
        if (!FC.canAny('operator')) add = null; // a rollout is an operator's at least (I148) / 发布至少需要操作员（I148）
        UI.mount(head, add, h('span.small.muted', t('ro.max', { n: b.max_active, min: Math.round((b.plan_ttl_ms || 0) / 60000) })));
        UI.mount(body, h('div.stack', b.read_only ? h('div.callout.callout--warn', t('readonly', { why: b.read_only })) : null,
          list.length ? UI.card(t('ro.history'), UI.table([
            { label: t('ro.when'), render: function (x) { return h('span.small', FC.when(x.started || x.created)); } },
            { label: t('ro.what'), render: function (x) {
              return h('div', h('a', { href: '#/cluster/rollout/' + encodeURIComponent(x.id) }, h('b', what(UI, x))), x.description ? h('div.small.muted', x.description) : null);
            } },
            { label: t('ro.state'), render: function (x) { return stateBadge(UI, x.state); } },
            { label: t('ro.members'), render: function (x) { return countBadges(UI, x); } },
            { label: t('ro.who'), render: function (x) { return h('span.small', x.started_by || x.planned_by); } }
          ], list, { onRowClick: function (x) { location.hash = '#/cluster/rollout/' + encodeURIComponent(x.id); } }), null, true)
            : UI.empty(t('ro.none'), 'send')));
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', read, 'ghost sm'));
    read();
    UI.every(5000, function () { if (!document.querySelector('.modal')) read(); });
  };

  // ---- A new plan / 新规划 ----
  FC.pages.newrollout = function (UI, sh) {
    var h = UI.h, t = UI.t, q = query();
    var el = sh.page('rollouts', t('ro.new'), t('m.cluster') + ' / ' + t('n.rollouts') + ' / ' + t('ro.new'));
    var box = h('div.stack');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/rollouts' }, '← ' + t('ro.back'))), box));
    UI.mount(box, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    Promise.all([FC.members(UI), FC.call(UI, 'GET', '/api/groups'), FC.call(UI, 'GET', '/api/templates')]).then(function (res) {
      var names = res[0].nodes.map(function (n) { return n.name; }).sort();
      var groups = ((res[1].body && res[1].body.groups) || []).map(function (g) { return g.name; });
      var tpls = ((res[2].body && res[2].body.templates) || []).map(function (x) { return x.name; });
      // A copy applies config of no template: an admin's (I148). / 复制应用的不是模板中的配置：需要管理员（I148）。
      var kinds = FC.canAny('admin') ? ['template', 'sync', 'copy', 'patch'] : ['template', 'sync'];
      var kind = h('select.select', { name: 'rollout-kind' }, kinds.map(function (k) {
        return h('option', { value: k, selected: (q.kind || 'template') === k }, t('ro.k.' + k));
      }));
      var kindHint = h('span.field__hint');
      var tpl = h('select.select', { name: 'rollout-template' }, tpls.map(function (n) { return h('option', { value: n, selected: n === q.template }, n); }));
      var group = h('select.select', { name: 'rollout-group' }, h('option', { value: '' }, t('ro.anygroup')),
        groups.map(function (n) { return h('option', { value: n, selected: n === q.group }, n); }));
      var from = h('select.select', { name: 'rollout-from' }, names.map(function (n) { return h('option', { value: n, selected: n === q.from }, n); }));
      var paths = h('textarea.textarea.textarea--code', { name: 'rollout-paths', rows: 3, spellcheck: 'false', placeholder: 'log\nroutes[api]' }, (q.paths || '').split(',').join('\n'));
      var picked = {};
      (q.nodes ? q.nodes.split(',') : []).forEach(function (n) { picked[n] = true; });
      var nodes = h('div.cl-chips', names.map(function (n) {
        var c = h('input', { type: 'checkbox', checked: !!picked[n], 'aria-label': n });
        c.addEventListener('change', function () { picked[n] = c.checked; });
        return h('label.cl-chip.tp-pick', c, ' ', n);
      }));
      var desc = h('input.input', { name: 'rollout-description', autocomplete: 'off', maxlength: 512 });
      // A patch's parts, and a template's draft (I153). / 修改的部分配置，以及模板的草稿（I153）。
      var cfgText = h('textarea.textarea.textarea--code', { name: 'rollout-config', rows: 6, spellcheck: 'false', placeholder: '{"log": {"level": "debug"}}' });
      var fCfg = UI.field(t('ro.config'), cfgText);
      var fCfgHint = h('span.field__hint');
      fCfg.appendChild(fCfgHint);
      var err = h('div.callout.callout--danger', { hidden: true });
      var fTpl = UI.field(t('ro.template'), tpl), fGroup = UI.field(t('ro.group'), group), fFrom = UI.field(t('ro.from'), from);
      var nodesHint = h('span.field__hint');
      var fPaths = UI.field(t('ro.paths'), paths, t('ro.paths.hint')), fNodes = h('label.field', h('span.field__label', t('ro.nodes')), nodes, nodesHint);
      function sync() {
        var k = kind.value;
        kindHint.textContent = t('ro.k.' + k + '.hint');
        nodesHint.textContent = t('ro.nodes.' + k);
        fTpl.hidden = k !== 'template'; fFrom.hidden = fPaths.hidden = k !== 'copy'; fGroup.hidden = k === 'template';
        fCfg.hidden = k !== 'patch' && !(k === 'template' && FC.canCluster('admin'));
        fCfg.querySelector('.field__label').textContent = t(k === 'patch' ? 'ro.config' : 'ro.draft');
        fCfgHint.textContent = k === 'patch' ? '' : t('ro.draft.hint');
      }
      kind.addEventListener('change', sync);
      sync();
      var plan = UI.btn(t('ro.plan'), 'send', function () {
        err.hidden = true;
        var sel = names.filter(function (n) { return picked[n]; });
        var req = { kind: kind.value, description: desc.value.trim() };
        if (req.kind === 'template') req.template = tpl.value;
        if (req.kind === 'copy') {
          req.from = from.value;
          req.paths = paths.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean);
          sel = sel.filter(function (n) { return n !== from.value; });
        }
        if (sel.length) req.nodes = sel; else if (req.kind !== 'template' && group.value) req.group = group.value;
        if (!fCfg.hidden && cfgText.value.trim()) {
          try { req.config = JSON.parse(cfgText.value); } catch (e) { req.config = null; }
          if (!req.config || typeof req.config !== 'object' || Array.isArray(req.config)) { err.textContent = t('ro.badjson'); err.hidden = false; return; }
        }
        UI.busy(plan, FC.call(UI, 'POST', '/api/rollouts', req).then(function (r) {
          location.hash = '#/cluster/rollout/' + encodeURIComponent(r.body.id);
        }, function (e) { err.textContent = e.message; err.hidden = false; }));
      }, 'primary');
      UI.mount(box, UI.card(t('ro.new'), h('div.stack.cl-form',
        h('label.field', h('span.field__label', t('ro.kind')), kind, kindHint), fTpl, fFrom, fPaths, fCfg, fGroup, fNodes, UI.field(t('ro.desc'), desc),
        err, h('div.btn-row', plan))));
    }, function (e) { UI.mount(box, h('div.callout.callout--danger', e.message)); });
  };

  // ---- A member's diff / 成员的差异 ----
  function changesView(UI, x) {
    var h = UI.h, t = UI.t;
    var cs = x.changes || [];
    // Once a rollout ends it keeps the paths changed, not their values. / 发布结束后只保留修改的路径，不保留其值。
    if (!cs.length || cs.every(function (c) { return c.want === undefined || c.want === null; })) {
      return h('div.cl-chips', (x.changed || []).map(function (p) { return h('span.cl-chip', p); }));
    }
    return h('div.stack', cs.map(function (c) {
      return h('details.tp-el', h('summary', h('code', c.path), ' ', UI.badge(t('ro.c.' + c.state), c.state === 'added' ? 'info' : 'warn'),
        c.sync ? h('span.small.muted', ' · ' + t('sy.' + c.sync) + (c.reason ? ': ' + t('sy.r.' + c.reason) : '')) : null,
        c.template ? h('span.small.muted', ' · ' + c.template) : null),
      FC.sideBySide(UI, t('ro.have'), c.have === undefined || c.have === null ? t('ro.absent') : pretty(c.have), t('ro.want'), pretty(c.want)));
    }));
  }
  // memberLinks are the member's page, its own pages through the switcher and, for templates, its rendered result.
  // memberLinks：成员页、经节点切换器打开的其自身页面，以及（模板类发布）其渲染结果。
  function memberLinks(UI, r, name) {
    var h = UI.h, t = UI.t;
    return h('span.btn-row',
      h('a.small', { href: '#/cluster/node/' + encodeURIComponent(name) }, t('ro.openmember')),
      h('a.small', { href: UI.targetURL(name, '#/gw.overview/overview') }, t('ro.openpages')),
      r.kind !== 'copy' ? h('a.small', { href: '#/cluster/rendered/' + encodeURIComponent(name) }, t('ro.rendered')) : null);
  }
  function errorsView(UI, x) {
    var h = UI.h, t = UI.t;
    if (!x.error && !(x.errors || []).length) return null;
    return h('div.stack', x.error ? FC.problemBox(UI, x.error) : null,
      (x.errors || []).length ? h('div.callout.callout--danger.small', h('div', t('ro.errors')),
        h('ul.tp-list', x.errors.map(function (e) { return h('li', e.path ? h('code', e.path) : null, e.path ? ' ' : null, e.message); }))) : null);
  }
  function gateView(UI, x) {
    var h = UI.h, t = UI.t, g = x.gate;
    if (!g || !g.checked) return null;
    var facts = [];
    if (g.ratio != null) facts.push(t('ro.g.ratio', { e: g.errors_5xx, n: g.requests, p: (g.ratio * 100).toFixed(1) }));
    if (g.drift_after != null) facts.push(t('ro.g.drift', { a: g.drift_after, b: g.drift_before }));
    return h('div.small', UI.badge(t(g.passed ? 'ro.g.passed' : 'ro.g.failed'), g.passed ? 'ok' : 'danger'), facts.length ? ' ' + facts.join(' · ') : '',
      (g.reasons || []).concat(g.notes || []).map(function (s) { return h('div.muted', s); }));
  }
  function versions(UI, x) {
    var t = UI.t, out = [];
    if (x.applied) out.push(t('ro.v', { a: x.version, b: x.applied }));
    if (x.rolled_back) out.push(t('ro.v.back', { n: x.rolled_back }));
    return out.join(' · ');
  }

  // ---- The strategy form / 策略表单 ----
  function strategyForm(UI, n, sug) {
    var h = UI.h, t = UI.t;
    function num(name, v, min) { return h('input.input.input--sm', { name: name, type: 'number', min: min || 0, step: 1, value: v == null ? '' : v }); }
    var mode = h('select.select', { name: 'rollout-mode' }, ['all', 'rolling', 'canary'].map(function (m) {
      return h('option', { value: m, selected: m === (n > 2 ? 'rolling' : 'all') }, t('ro.mode.' + m));
    }));
    var size = num('rollout-batch', n > 2 ? Math.max(1, Math.ceil(n / 4)) : '', 1);
    var unit = h('select.select.select--sm', { name: 'rollout-unit', style: { width: 'auto' } },
      h('option', { value: 'size' }, t('ro.batch.size')), h('option', { value: 'pct' }, t('ro.batch.pct')));
    var pause = num('rollout-pause', 0), canary = num('rollout-canary', 1, 1);
    var wait = num('rollout-wait', 5), ratio = num('rollout-ratio', ''), win = num('rollout-window', 30, 1), min = num('rollout-min', 20);
    var onfail = h('select.select', { name: 'rollout-onfail' }, ['batch', 'all'].map(function (m) { return h('option', { value: m }, t('ro.onfail.' + m)); }));
    var fBatch = UI.field(t('ro.batch'), h('span.row', size, unit), t('ro.batch.hint')), fPause = UI.field(t('ro.pause'), pause);
    var fCanary = UI.field(t('ro.canary'), canary, t('ro.canary.hint'));
    var fWin = UI.field(t('ro.gate.window'), win), fMin = UI.field(t('ro.gate.min'), min);
    function sync() {
      var m = mode.value;
      fBatch.hidden = m === 'all'; fPause.hidden = m === 'all'; fCanary.hidden = m !== 'canary';
      fWin.hidden = fMin.hidden = ratio.value === '';
    }
    // The strategy the assistant suggested, when it planned the rollout (I153). / 助手规划时建议的策略（I153）。
    if (sug) {
      var g = sug.gate || {};
      mode.value = sug.mode || mode.value;
      if (sug.batch_percent) { size.value = sug.batch_percent; unit.value = 'pct'; } else if (sug.batch_size) size.value = sug.batch_size;
      pause.value = (sug.pause_ms || 0) / 1000; canary.value = sug.canary || 1; onfail.value = sug.on_failure || 'batch';
      wait.value = (g.wait_ms || 0) / 1000;
      if (g.max_5xx_ratio) { ratio.value = g.max_5xx_ratio * 100; win.value = (g.window_ms || 30000) / 1000; min.value = g.min_requests || 20; }
    }
    mode.addEventListener('change', sync); ratio.addEventListener('input', sync);
    sync();
    var el = h('div.stack.cl-form', sug ? h('div.callout.callout--info.small', t('ro.suggested')) : null,
      h('div.form-row', UI.field(t('ro.mode'), mode), fCanary), h('div.form-row', fBatch, fPause),
      h('div.small.muted', t('ro.gate.hint')),
      h('div.form-row', UI.field(t('ro.gate.wait'), wait), UI.field(t('ro.gate.ratio'), ratio, t('ro.gate.ratio.hint')), fWin, fMin),
      UI.field(t('ro.onfail'), onfail));
    function value() {
      var s = { mode: mode.value, on_failure: onfail.value, gate: { wait_ms: Math.round((+wait.value || 0) * 1000) } };
      if (mode.value !== 'all') {
        var v = parseInt(size.value, 10);
        if (v > 0) { if (unit.value === 'pct') s.batch_percent = v; else s.batch_size = v; }
        s.pause_ms = Math.round((+pause.value || 0) * 1000);
      }
      if (mode.value === 'canary') s.canary = parseInt(canary.value, 10) || 1;
      if (ratio.value !== '') {
        s.gate.max_5xx_ratio = (+ratio.value || 0) / 100;
        s.gate.window_ms = Math.round((+win.value || 0) * 1000);
        s.gate.min_requests = parseInt(min.value, 10) || 0;
      }
      return s;
    }
    return { el: el, value: value };
  }
  // batchesOf previews the batches a strategy makes of names, as the manager does. / 预览策略对 names 的分批（与管理端一致）。
  function batchesOf(s, names) {
    var out = [], rest = names.slice();
    if (s.mode === 'all') return [{ nodes: rest }];
    if (s.mode === 'canary') { out.push({ nodes: rest.splice(0, s.canary), canary: true }); }
    var size = s.batch_percent ? Math.ceil(rest.length * s.batch_percent / 100) : s.batch_size || rest.length;
    while (rest.length) out.push({ nodes: rest.splice(0, size) });
    return out;
  }

  // ---- A rollout / 发布页 ----
  FC.pages.rollout = function (UI, sh, params) {
    var h = UI.h, t = UI.t, id = params.id;
    var el = sh.page('rollouts', id, t('m.cluster') + ' / ' + t('n.rollouts') + ' / ' + id);
    var head = h('div.stack'), body = h('div.stack');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/rollouts' }, '← ' + t('ro.back'))), head, body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    var r = null, exclude = {}, form = null, lastState = '', timer = null;

    function act(action, ask, danger) {
      var go = function () {
        return FC.call(UI, 'POST', path(id, action)).then(function () { read(); }, function (e) { UI.toast(e.message, 'error'); read(); });
      };
      if (!ask) return go();
      UI.confirm(ask, t('ro.' + (action === 'pause' ? 'pause.btn' : action)), danger).then(function (yes) { if (yes) go(); });
    }
    // steers says whether the user may steer r: an operator of each member, an admin of each for a copy (I148).
    // steers 判断用户能否操作 r：需要在每个成员上是操作员，复制则需要管理员（I148）。
    function steers() {
      if (r.kind === 'template' && r.config && r.state === 'planned' && !FC.canCluster('admin')) return false;
      return FC.canAll(r.kind === 'copy' || r.kind === 'patch' ? 'admin' : 'operator', (r.members || []).map(function (x) { return x.node; }));
    }
    function controls() {
      var s = r.state, btns = [];
      if (!steers()) return null;
      if (s === 'running') btns.push(UI.btn(t('ro.pause.btn'), 'stop', function () { act('pause'); }, 'ghost sm'));
      if (s === 'paused') btns.push(UI.btn(t('ro.resume'), 'play', function () { act('resume'); }, 'primary sm'));
      if (s === 'waiting') btns.push(UI.btn(t('ro.go'), 'forward', function () { act('go'); }, 'primary sm'));
      if (s === 'running' || s === 'paused' || s === 'waiting') btns.push(UI.btn(t('ro.abort'), 'x', function () { act('abort', t('ro.abort.ask'), true); }, 'ghost sm'));
      var applied = (r.members || []).some(function (x) { return x.applied && ['applied', 'done', 'failed', 'rollback_failed'].indexOf(x.status) >= 0; });
      if (applied && ['paused', 'waiting', 'done', 'failed', 'aborted'].indexOf(s) >= 0) {
        btns.push(UI.btn(t('ro.rollback'), 'undo', function () { act('rollback', t('ro.rollback.ask'), true); }, 'ghost sm'));
      }
      return btns.length ? h('span.btn-row', btns) : null;
    }
    function drawHead() {
      var by = r.started_by ? t('ro.by', { p: r.planned_by, s: r.started_by }) : t('ro.byplan', { p: r.planned_by });
      if (r.source === 'assistant') by = r.started_by ? t('ro.byai.started', { p: r.planned_by, s: r.started_by }) : t('ro.byai', { p: r.planned_by });
      var cfg = r.config ? h('details.tp-el', h('summary', t(r.kind === 'patch' ? 'ro.cfg.patch' : 'ro.cfg.draft')), h('pre.cl-mono', pretty(r.config))) : null;
      UI.mount(head, UI.card(what(UI, r), h('div.stack',
        r.description ? h('div', r.description) : null,
        h('div.btn-row', stateBadge(UI, r.state), countBadges(UI, r), h('span.small.muted', by + ' · ' + FC.when(r.created))),
        r.reason ? h('div.callout.callout--' + (r.state === 'failed' ? 'danger' : r.state === 'done' ? 'ok' : 'warn') + '.small', h('b', t('ro.reason') + ': '), r.reason) : null,
        r.next_at ? h('div.small.muted', t('ro.next', { at: FC.when(r.next_at) })) : null, cfg), controls()));
    }
    function memberCard(x, plan) {
      var ready = x.status === 'ready';
      var box = null;
      if (plan && ready) {
        box = h('input', { type: 'checkbox', checked: !exclude[x.node], 'aria-label': t('ro.include') + ' ' + x.node });
        box.addEventListener('change', function () { exclude[x.node] = !box.checked; });
      }
      return h('section.card.ro-member', h('div.card__head', h('h2', box, box ? ' ' : null, x.node, ' ', memberBadge(UI, x.status)), memberLinks(UI, r, x.node)),
        h('div.card__body.stack',
          x.note ? h('div.callout.callout--warn.small', x.note) : null,
          errorsView(UI, x),
          ready && x.version ? h('div.small.muted', t('ro.dry') + ': ' + t('ro.dry.ok', { v: x.version }) + ((x.warnings || []).length ? ' · ' + x.warnings.join('; ') : '')) : null,
          versions(UI, x) ? h('div.small', versions(UI, x)) : null,
          gateView(UI, x),
          (x.changes || []).length || (x.changed || []).length ? changesView(UI, x) : null));
    }
    function drawPlan() {
      var ms = r.members || [];
      var ready = ms.filter(function (x) { return x.status === 'ready'; });
      if (!form) form = strategyForm(UI, ready.length, r.suggested);
      var start = UI.btn(t('ro.start'), 'send', confirmStart, 'primary');
      start.disabled = !ready.length;
      var discard = UI.btn(t('ro.discard'), 'trash', function () {
        UI.confirm(t('ro.discard.ask'), t('ro.discard'), true).then(function (yes) {
          if (!yes) return;
          FC.call(UI, 'DELETE', path(id)).then(function () { UI.toast(t('ro.discarded')); location.hash = '#/cluster/rollouts'; }, function (e) { UI.toast(e.message, 'error'); });
        });
      }, 'ghost');
      UI.mount(body, h('div.stack',
        UI.card(t('ro.plan.title'), h('div.stack', ready.length ? null : UI.empty(t('ro.none.ready'), 'info'), ms.map(function (x) { return memberCard(x, true); }))),
        !steers() ? null : ready.length ? UI.card(t('ro.strategy'), h('div.stack', form.el, h('div.btn-row', start, discard))) : h('div.btn-row', discard)));
    }
    function confirmStart() {
      var s = form.value();
      var names = (r.members || []).filter(function (x) { return x.status === 'ready' && !exclude[x.node]; }).map(function (x) { return x.node; });
      var bs = batchesOf(s, names);
      var byName = {};
      (r.members || []).forEach(function (x) { byName[x.node] = x; });
      UI.modal({
        title: t('ro.confirm.title'), wide: true,
        body: h('div.stack', h('div', t('ro.confirm.note', { n: names.length, b: bs.length })),
          r.kind === 'template' && r.config ? h('div.callout.callout--warn.small', t('ro.confirm.draft', { t: r.template })) : null,
          h('ol.tp-list', bs.map(function (b, i) {
            return h('li', t('ro.confirm.batch', { n: i + 1 }), b.canary ? ' (' + t('ro.confirm.canary') + ')' : '', ': ', b.nodes.join(', '));
          })),
          h('div.stack', names.map(function (n) { return h('details.tp-el', h('summary', h('b', n), ' ', h('span.small.muted', (byName[n].changed || []).join(', '))), changesView(UI, byName[n])); }))),
        actions: [{ label: t('cancel') }, { label: t('ro.start'), kind: 'primary', onclick: function () {
          return FC.call(UI, 'POST', path(id, 'start'), { strategy: s, exclude: Object.keys(exclude).filter(function (k) { return exclude[k]; }) }).then(function () {
            UI.toast(t('ro.started'));
            read();
          }, function (e) { UI.toast(e.message, 'error'); return false; });
        } }]
      });
    }
    function drawProgress() {
      var byName = {};
      (r.members || []).forEach(function (x) { byName[x.node] = x; });
      var listed = {};
      var cards = (r.batches || []).map(function (b, i) {
        b.nodes.forEach(function (n) { listed[n] = true; });
        var title = h('span', t('ro.confirm.batch', { n: i + 1 }), b.canary ? ' (' + t('ro.confirm.canary') + ')' : '', ' ',
          UI.badge(t('ro.b.' + b.state), BATCH_KIND[b.state] || '', true));
        var gateAt = b.state === 'gating' && b.gate_at ? h('span.small.muted', t('ro.gate.at', { at: FC.when(b.gate_at) })) : null;
        // A failed batch asks the assistant why, and how to go on (I153, askai.js). / 失败的批次可询问助手原因与后续（I153，askai.js）。
        var ask = FC.askBatch ? FC.askBatch(UI, r, b) : null;
        return UI.card(title, h('div.stack', b.nodes.map(function (n) { return memberCard(byName[n], false); })), gateAt || ask ? h('span.btn-row', gateAt, ask) : null);
      });
      var others = (r.members || []).filter(function (x) { return !listed[x.node]; });
      UI.mount(body, h('div.stack', UI.card(t('ro.progress'), h('div.stack', cards)),
        others.length ? UI.card(t('ro.members'), h('div.stack', others.map(function (x) { return memberCard(x, false); }))) : null));
    }
    function read() {
      return FC.call(UI, 'GET', path(id)).then(function (res) {
        if (!document.body.contains(el)) return;
        r = res.body;
        drawHead();
        if (r.state === 'planning') {
          UI.mount(body, h('div.stack', h('div.row', h('span.spinner'), h('span.muted', t('ro.planning'))),
            h('div.stack', (r.members || []).map(function (x) { return memberCard(x, false); }))));
        } else if (r.state === 'planned') {
          // The plan is drawn once, so that the form keeps what is typed in it. / 规划只绘制一次，以保留表单中已输入的内容。
          if (lastState !== 'planned') drawPlan();
        } else {
          drawProgress();
        }
        lastState = r.state;
        schedule();
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.status === 404 ? t('ro.notfound', { id: id }) : e.message));
      });
    }
    // schedule reads the rollout again: soon while it moves, slowly while it waits for a person. / 发布进行中时尽快重读，等待人时慢速重读。
    function schedule() {
      clearTimeout(timer);
      var ms = ACTIVE.indexOf(r.state) >= 0 ? 1000 : r.state === 'paused' || r.state === 'waiting' ? 5000 : 0;
      if (ms) timer = setTimeout(function () { if (document.body.contains(el) && !document.querySelector('.modal')) read(); else if (document.body.contains(el)) schedule(); }, ms);
    }
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', read, 'ghost sm'));
    read();
  };
})();
