// The assistant's audit log (GET /api/audit): what it did for each user, the latest first: an answer's tool calls, a
// proposal applied (with the config version it made), refused, stale or discarded, with the conversation, the skill and
// how it ended. Picked by user, kind and conversation, a page at a time; kept in the store within
// limits.max_audit_entries.
//
// 助手的审计日志（GET /api/audit）：助手为每个用户做过的事，最新的在前：一次回答的工具调用，提案的应用（及生成的配置版本）、
// 拒绝、过期或丢弃，连同对话、技能与结果。可按用户、类型与对话筛选，分页读取；在 limits.max_audit_entries 的限度内保存在
// store 中。
(function () {
  'use strict';

  var WORDS = [
    ['n.audit', 'Audit log', '审计日志'],
    ['au.note', 'What the assistant did for each user: the tools an answer called, and each proposal applied, refused, offered again or discarded. Arguments are redacted and cut.',
      '助手为每个用户做过的事：每次回答调用的工具，以及每个提案的应用、拒绝、重新提供或丢弃。参数已脱敏并截断。'],
    ['au.kept', 'Kept in the store, the latest {max} entries.', '保存在 store 中，最多 {max} 条。'],
    ['au.notKept', 'Without admin.store, the log lasts until fit restarts; the latest {max} entries.', '未配置 admin.store：日志只保留到 fit 重启，最多 {max} 条。'],
    ['au.time', 'Time', '时间'], ['au.user', 'User', '用户'], ['au.kind', 'Kind', '类型'], ['au.conv', 'Conversation', '对话'],
    ['au.calls', 'Tool calls', '工具调用'], ['au.proposal', 'Proposal', '提案'], ['au.outcome', 'Outcome', '结果'], ['au.version', 'Version', '版本'],
    ['au.all', 'All', '全部'], ['au.more', 'Older entries', '更早的记录'], ['au.none', 'No entries.', '没有记录。'],
    ['au.for', 'for {user}', '代 {user}'], ['au.moreCalls', 'and {n} more', '另有 {n} 个'], ['au.skill', 'skill {skill}', '技能 {skill}'],
    ['au.convFilter', 'Conversation ID', '对话 ID'],
    ['k.chat', 'answer', '回答'], ['k.apply', 'apply', '应用'], ['k.discard', 'discard', '丢弃'],
    ['o.answer', 'answered', '已回答'], ['o.ask', 'asked', '已提问'], ['o.stopped', 'stopped', '已停止'], ['o.error', 'error', '出错'],
    ['o.max_tool_calls', 'tool call limit', '达到工具调用上限'], ['o.max_tokens_per_day', 'day\'s tokens spent', '当日 token 用尽'],
    ['o.applied', 'applied', '已应用'], ['o.partial', 'applied, a purge failed', '已应用，清除失败'], ['o.failed', 'failed', '失败'],
    ['o.stale', 'offered again', '已重新提供'], ['o.refused', 'refused', '已拒绝'], ['o.discarded', 'discarded', '已丢弃']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh }, 'ai');

  var KIND = { applied: 'ok', answer: 'ok', partial: 'warn', stale: 'warn', ask: '', stopped: '', discarded: '', failed: 'danger', refused: 'danger', error: 'danger' };

  function word(UI, k) { var w = UI.t(k); return w === k ? k.replace(/^\w+\./, '') : w; }

  // auditPage draws the log into the shell sh. / 在外框 sh 中绘制日志。
  function auditPage(UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('audit', t('n.audit'));
    var f = { user: '', kind: '', conversation: '' }, rows = [], last = 0;
    var userSel = h('select.select.select--sm', { onchange: function () { f.user = userSel.value; load(true); } });
    var kindSel = h('select.select.select--sm', { onchange: function () { f.kind = kindSel.value; load(true); } },
      [['', t('au.all')], ['chat', t('k.chat')], ['apply', t('k.apply')], ['discard', t('k.discard')]].map(function (o) { return h('option', { value: o[0] }, o[1]); }));
    var convIn = h('input.input.input--sm', { placeholder: t('au.convFilter'), onchange: function () { f.conversation = convIn.value.trim(); load(true); } });
    var note = h('div.small.muted'), table = h('div'), more = h('div.btn-row');
    sh.tools.appendChild(UI.btn('', 'refresh', function () { load(true); }, 'ghost icon'));
    UI.mount(el, h('div.callout', UI.icon('info'), ' ', t('au.note')), h('div.row.ai-audit__filters', userSel, kindSel, convIn, note),
      UI.card(t('n.audit'), table, null, true), more);

    function calls(e) {
      if (!e.calls || !e.calls.length) return '—';
      return h('details.ai-fold', h('summary.small', e.calls.length + (e.more_calls ? '+' + e.more_calls : '') + ' · ' + e.calls.map(function (c) { return c.tool; }).slice(0, 3).join(', ')),
        h('div', h('ul.small.ai-audit__calls', e.calls.map(function (c) {
          return h('li' + (c.error ? '.ai-err' : ''), h('code', c.tool), c.args ? h('pre.ai-pre', c.args) : null);
        }), e.more_calls ? h('li.muted', t('au.moreCalls', { n: e.more_calls })) : null)));
    }
    function proposal(e) {
      var p = e.proposal;
      if (!p) return '—';
      return h('details.ai-fold', h('summary.small', h('code', p.id), ' · ' + (p.steps || []).length),
        h('div', h('ol.small.ai-prop__steps', (p.steps || []).map(function (s) { return h('li', word(UI, 'tool.' + s.tool), ': ', s.summary); })),
          p.changed && p.changed.length ? h('div.small.muted', t('pr.changes', { members: p.changed.join(', ') })) : null));
    }
    function draw() {
      UI.mount(table, UI.table([
        { label: t('au.time'), render: function (e) { return h('span.small', UI.fmtTime(Math.floor(e.at / 1000))); } },
        { label: t('au.user'), render: function (e) { return h('span', h('b', e.user), e.owner ? h('div.small.muted', t('au.for', { user: e.owner })) : null); } },
        { label: t('au.kind'), render: function (e) { return h('span', UI.badge(word(UI, 'k.' + e.kind)), e.skill ? h('div.small.muted', t('au.skill', { skill: e.skill })) : null); } },
        { label: t('au.conv'), render: function (e) { return e.conversation ? h('code.small', e.conversation.slice(0, 8)) : '—'; } },
        { label: t('au.calls'), render: calls },
        { label: t('au.proposal'), render: proposal },
        { label: t('au.outcome'), render: function (e) {
          return h('span', UI.badge(word(UI, 'o.' + e.outcome), KIND[e.outcome] || '', true), e.detail ? h('div.small.muted.ai-audit__detail', e.detail) : null);
        } },
        { label: t('au.version'), render: function (e) { return e.version ? h('a', { href: '#/sys.versions' }, '#' + e.version) : '—'; } }
      ], rows, { empty: t('au.none') }));
    }
    // load reads a page: from the latest with fresh, else the next older one. / 读取一页：fresh 时从最新开始，否则读更早的一页。
    function load(fresh) {
      var q = ['limit=100'];
      if (f.user) q.push('user=' + encodeURIComponent(f.user));
      if (f.kind) q.push('kind=' + encodeURIComponent(f.kind));
      if (f.conversation) q.push('conversation=' + encodeURIComponent(f.conversation));
      if (!fresh && last) q.push('before=' + last);
      UI.api('GET', '/api/audit?' + q.join('&')).then(function (b) {
        if (fresh) rows = [];
        rows = rows.concat(b.entries || []);
        last = rows.length ? rows[rows.length - 1].seq : 0;
        if (userSel.options.length === 0) {
          UI.mount(userSel, [h('option', { value: '' }, t('au.all'))].concat((b.users || []).map(function (u) { return h('option', { value: u }, u); })));
        }
        note.textContent = (b.kept ? t('au.kept', { max: b.max }) : t('au.notKept', { max: b.max }));
        draw();
        UI.mount(more, (b.entries || []).length === 100 ? UI.btn(t('au.more'), 'history', function () { load(false); }, 'sm') : null);
      }, function (e) { UI.mount(table, h('div.callout.callout--danger', e.message)); });
    }
    load(true);
  }

  window.FitAI = window.FitAI || {};
  window.FitAI.auditPage = auditPage;
})();
