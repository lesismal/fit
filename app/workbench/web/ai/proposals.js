// The assistant's proposals: what the propose tools made of a conversation's change, checked on the running config
// (GET /api/proposals/{conversation}), drawn as a card with each step, the members it changes, its errors, and its
// diff in .conf, .yaml and .json; Apply opens a dialog with the whole diff, and only its confirm sends
// POST /api/proposals/{conversation}/apply with the proposal's revision and the config's ETag shown, so that what is
// applied is what was seen; a proposal whose config changed meanwhile comes back 412 and is offered again. Discard
// (POST .../discard) drops it. The Proposals page lists the pending ones (GET /api/proposals): the user's own, and, for a
// user whose level is apply, every user's, to review and apply. Nothing a model wrote is inserted as HTML.
//
// 助手的提案：提案工具为某个对话生成的变更，在运行中的配置上检查（GET /api/proposals/{conversation}），绘制为卡片：每个步骤、
// 它改动的成员、错误，以及 .conf、.yaml、.json 三种格式的差异；“应用”先打开含完整差异的对话框，只有其中的确认才会发送
// POST /api/proposals/{conversation}/apply，并带上提案的修订号与所见配置的 ETag，确保应用的正是所见内容；期间配置已变的提案
// 返回 412，并在新版本上重新提供。“丢弃”（POST .../discard）将其删除。提案页列出待处理的提案（GET /api/proposals）：用户自己的；
// 级别为 apply 的用户还能看到所有用户的，以便审阅与应用。模型写的任何内容都不会作为 HTML 插入。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['pr.title', 'Proposal', '提案'], ['pr.steps', 'Steps', '步骤'], ['pr.changes', 'Changes: {members}', '改动：{members}'],
    ['pr.noChange', 'No change to the config', '不改动配置'], ['pr.valid', 'checks', '检查通过'], ['pr.invalid', 'does not check', '检查未通过'],
    ['pr.stale', 'the config changed: version {from} → {to}', '配置已变：版本 {from} → {to}'], ['pr.rev', 'revision {n}', '修订 {n}'],
    ['pr.review', 'Review and apply…', '审阅并应用…'], ['pr.discard', 'Discard', '丢弃'], ['pr.discardQ', 'Discard this proposal? The config does not change.', '丢弃此提案？配置不会改变。'],
    ['pr.diff', 'Diff', '差异'], ['pr.noDiff', 'No change to the config itself.', '配置本身没有改动。'],
    ['pr.purge', 'Cache purge after the config: {what}', '配置之后清除缓存：{what}'], ['pr.zone', 'zone {zone}', '缓存区 {zone}'], ['pr.allZones', 'every zone', '所有缓存区'],
    ['pr.urls', '{n} URLs', '{n} 个 URL'], ['pr.tags', 'tags {tags}', '标签 {tags}'], ['pr.all', 'everything', '全部'],
    ['pr.rollback', 'Rolls back to version {n}, as a new version.', '回滚到版本 {n}（作为新版本）。'],
    ['pr.fixes', 'Consistency fixes: {fixes}', '一致性修复：{fixes}'],
    ['pr.unconverted', 'Not carried over from the config moved here ({n}):', '未从迁入的配置中带过来的项（{n}）：'],
    ['pr.level', 'Your level is {level}: a user whose level is apply applies proposals.', '你的级别为 {level}：只有级别为 apply 的用户才能应用提案。'],
    ['pr.lastError', 'The last apply failed: {error}', '上次应用失败：{error}'],
    ['pr.confirmTitle', 'Apply this proposal?', '应用此提案？'],
    ['pr.confirmIntro', 'This changes the running config: the steps below are applied together as one new version after version {version}, as your change, saved and listed in Versions; if one fails, none is applied.',
      '这会改动运行中的配置：以下步骤作为一次变更一起应用，在版本 {version} 之后生成一个新版本，记为你的变更，保存并列入版本列表；任一失败则全部不应用。'],
    ['pr.confirmPurge', 'The cache purges run after the config.', '缓存清除在配置之后执行。'],
    ['pr.confirm', 'Apply', '应用'], ['pr.applied', 'Applied as config version {version}.', '已应用为配置版本 {version}。'],
    ['pr.appliedPurges', 'Applied: the cache purges ran.', '已应用：缓存清除已执行。'],
    ['pr.partial', 'Applied, but a cache purge failed: {error}', '已应用，但有一次缓存清除失败：{error}'],
    ['pr.offeredAgain', 'The config changed since: the proposal is offered again on version {version}. Review it and apply again.', '配置在此期间已变：提案已在版本 {version} 上重新提供。请审阅后再次应用。'],
    ['pr.failed', 'Not applied: {error}', '未应用：{error}'], ['pr.discarded', 'The proposal was discarded.', '提案已丢弃。'],
    ['pr.none', 'No proposals wait to be applied.', '没有待应用的提案。'], ['pr.user', 'User', '用户'], ['pr.conv', 'Conversation', '对话'],
    ['pr.updated', 'Updated', '更新时间'], ['pr.state', 'State', '状态'], ['pr.open', 'Review', '审阅'],
    ['pr.pageNote', 'Proposals the assistant made in conversations, waiting for a person to apply or discard them. A user whose level is apply sees every user\'s here; others see their own.',
      '助手在对话中生成、等待有人应用或丢弃的提案。级别为 apply 的用户在此可看到所有用户的提案；其他用户只能看到自己的。'],
    ['n.proposals', 'Proposals', '提案'],
    ['tool.config_propose', 'Config change', '配置变更'], ['tool.reconcile_propose', 'Consistency fixes', '一致性修复'],
    ['tool.rollback_propose', 'Rollback', '回滚'], ['tool.cache_purge_propose', 'Cache purge', '缓存清除']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh }, 'ai');

  // diffView draws a unified diff, each line by its kind. / 按行类型绘制统一差异。
  function diffView(UI, text) {
    var h = UI.h;
    if (!text) return h('div.small.muted', UI.t('pr.noDiff'));
    return h('pre.ai-diff', String(text).replace(/\n$/, '').split('\n').map(function (l) {
      var k = l.indexOf('@@') === 0 ? 'hunk' : l.indexOf('+++') === 0 || l.indexOf('---') === 0 ? 'file' : l[0] === '+' ? 'add' : l[0] === '-' ? 'del' : '';
      return h('span' + (k ? '.ai-diff__' + k : ''), l + '\n');
    }));
  }
  // diffTabs draws the diff with a tab per format. / 每种格式一个标签页绘制差异。
  function diffTabs(UI, v) {
    var h = UI.h, fmt = 'conf', body = h('div');
    var bar = h('div');
    function draw() {
      UI.mount(bar, UI.tabs([['conf', '.conf'], ['yaml', '.yaml'], ['json', '.json']], fmt, function (f) { fmt = f; draw(); }));
      UI.mount(body, diffView(UI, (v.diff || {})[fmt]));
    }
    draw();
    return h('div.ai-difftabs', bar, body);
  }
  function purgeText(UI, p) {
    var t = UI.t, what = [];
    if (p.all) what.push(t('pr.all'));
    if (p.urls && p.urls.length) what.push(t('pr.urls', { n: p.urls.length }));
    if (p.tags && p.tags.length) what.push(t('pr.tags', { tags: p.tags.join(', ') }));
    return t('pr.purge', { what: (p.zone ? t('pr.zone', { zone: p.zone }) : t('pr.allZones')) + ': ' + what.join(', ') });
  }
  function toolName(UI, name) { var k = 'tool.' + name, w = UI.t(k); return w === k ? name : w; }
  // summary draws what the proposal does: its steps, members, rollback, fixes and purges. / 绘制提案的内容。
  function summary(UI, v) {
    var h = UI.h, t = UI.t;
    return h('div.stack.ai-prop__body',
      h('ol.ai-prop__steps', (v.steps || []).map(function (s) {
        return h('li', h('b', toolName(UI, s.tool)), ' ', h('span', s.summary || ''), s.error ? h('div.small.ai-err', s.error) : null);
      })),
      v.kind ? h('div.small', v.changed && v.changed.length ? t('pr.changes', { members: v.changed.join(', ') }) : t('pr.noChange')) : null,
      v.rollback ? h('div.small', t('pr.rollback', { n: v.rollback })) : null,
      v.fixes && v.fixes.length ? h('div.small', t('pr.fixes', { fixes: v.fixes.map(function (f) { return f.id + ' (' + f.action + ')'; }).join(', ') })) : null,
      (v.purges || []).map(function (p) { return h('div.small', purgeText(UI, p)); }),
      v.unconverted && v.unconverted.length ? h('div.small.ai-prop__unconverted', t('pr.unconverted', { n: v.unconverted.length }),
        h('ul', v.unconverted.map(function (u) { return h('li', u); }))) : null,
      v.errors && v.errors.length ? h('ul.small.ai-err', v.errors.map(function (e) { return h('li', e.path ? h('code', e.path) : null, e.path ? ' ' : '', e.msg); })) : null);
  }

  // post sends an apply or a discard, and resolves with {status, body}. / 发送应用或丢弃请求，返回 {status, body}。
  function post(UI, v, action) {
    return UI.raw('POST', '/api/proposals/' + encodeURIComponent(v.conversation) + '/' + action,
      { id: v.id, revision: v.revision, etag: v.etag, user: v.user }).then(function (r) {
      return r.text().then(function (txt) {
        var j = null;
        try { j = JSON.parse(txt); } catch (e) { /* not JSON / 非 JSON */ }
        return { status: r.status, body: j || {} };
      });
    });
  }

  // confirmApply opens the dialog with the whole diff; only its confirm applies. o.done(view|null, message, kind) hears
  // what became of it. / 打开含完整差异的确认对话框；只有确认才会应用。o.done(view|null, message, kind) 获知结果。
  function confirmApply(UI, v, o) {
    var h = UI.h, t = UI.t;
    UI.modal({
      title: t('pr.confirmTitle'), wide: true,
      body: h('div.stack', h('div.callout.callout--warn', UI.icon('alert'), ' ', t('pr.confirmIntro', { version: v.version })),
        summary(UI, v), v.kind ? diffTabs(UI, v) : null, v.purges && v.purges.length && v.kind ? h('div.small.muted', t('pr.confirmPurge')) : null),
      actions: [{ label: t('cancel') }, {
        label: t('pr.confirm'), kind: 'primary', onclick: function () {
          return post(UI, v, 'apply').then(function (res) {
            var b = res.body;
            if (res.status === 200) {
              var failed = (b.purged || []).filter(function (p) { return p.error; })[0];
              if (failed) o.done(null, t('pr.partial', { error: failed.error }), 'warn');
              else o.done(null, b.version ? t('pr.applied', { version: b.version }) : t('pr.appliedPurges'), 'ok');
            } else if (res.status === 412) {
              o.done(b.proposal || null, t('pr.offeredAgain', { version: b.proposal ? b.proposal.version : '?' }), 'warn');
            } else if (b.proposal) {
              o.done(b.proposal, t('pr.failed', { error: b.error || res.status }), 'danger');
            } else {
              var err = new Error(b.error || String(res.status));
              throw err;
            }
          });
        }
      }]
    });
  }

  // card draws a proposal, v as GET /api/proposals/{conversation} answers it. o: {done(view|null, message, kind),
  // busy() → true while the conversation answers}. / 绘制提案卡片。o：{done(view|null, message, kind)，busy()}。
  function card(UI, v, o) {
    var h = UI.h, t = UI.t;
    var badges = [v.valid ? UI.badge(t('pr.valid'), 'ok', true) : UI.badge(t('pr.invalid'), 'danger', true)];
    if (v.stale) badges.push(UI.badge(t('pr.stale', { from: v.base_version, to: v.version }), 'warn', true));
    var busy = o.busy ? o.busy() : false;
    var apply = h('button.btn.btn--primary.btn--sm', { type: 'button', disabled: !v.can_apply || !v.valid || busy, onclick: function () { confirmApply(UI, v, o); } },
      UI.icon('check'), t('pr.review'));
    var discard = h('button.btn.btn--ghost.btn--sm', { type: 'button', disabled: busy, onclick: function () {
      UI.confirm(t('pr.discardQ'), t('pr.discard'), true).then(function (ok) {
        if (!ok) return;
        post(UI, v, 'discard').then(function (res) {
          if (res.status === 200) o.done(null, t('pr.discarded'), '');
          else UI.fail(new Error(res.body.error || String(res.status)));
        }, UI.fail);
      });
    } }, UI.icon('trash'), t('pr.discard'));
    var fold = v.kind ? h('details.ai-fold', h('summary', UI.icon('code'), ' ', t('pr.diff')), h('div', diffTabs(UI, v))) : null;
    return h('div.ai-prop',
      h('div.ai-prop__head', UI.icon('layers'), h('b', t('pr.title')), h('span.small.muted', t('pr.rev', { n: v.revision })), badges),
      v.error ? h('div.callout.callout--danger.small', t('pr.lastError', { error: v.error })) : null,
      summary(UI, v), fold,
      !v.can_apply ? h('div.small.muted', t('pr.level', { level: t('lv.' + v.level) })) : null,
      h('div.btn-row', apply, discard));
  }

  // proposalsPage draws the pending proposals and the one picked. / 绘制待处理的提案列表及所选提案。
  function proposalsPage(UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('proposals', t('n.proposals'));
    var detail = h('div');
    sh.tools.appendChild(UI.btn('', 'refresh', function () { proposalsPage(UI, sh); }, 'ghost icon'));
    function open(p) {
      UI.api('GET', '/api/proposals/' + encodeURIComponent(p.conversation) + '?user=' + encodeURIComponent(p.user)).then(function (b) {
        if (!b.proposal) { proposalsPage(UI, sh); return; }
        UI.mount(detail, UI.card(p.title || p.conversation, card(UI, b.proposal, {
          done: function (view, msg, kind) {
            if (msg) UI.toast(msg, kind === 'danger' ? 'error' : kind === 'warn' ? 'info' : undefined);
            if (view) open(p); else proposalsPage(UI, sh);
          }
        })));
        detail.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }, UI.fail);
    }
    UI.api('GET', '/api/proposals').then(function (b) {
      var list = b.proposals || [];
      UI.mount(el, h('div.callout', UI.icon('info'), ' ', t('pr.pageNote')),
        UI.card(t('n.proposals'), UI.table([
          { label: t('pr.user'), render: function (p) { return h('b', p.user); } },
          { label: t('pr.conv'), render: function (p) { return h('span', p.title || p.conversation); } },
          { label: t('pr.steps'), render: function (p) { return h('ol.ai-prop__steps.small', p.steps.map(function (s) { return h('li', toolName(UI, s.tool), ': ', s.summary); })); } },
          { label: t('pr.state'), render: function (p) { return p.valid ? UI.badge(t('pr.valid'), 'ok', true) : UI.badge(t('pr.invalid'), 'danger', true); } },
          { label: t('pr.updated'), render: function (p) { return UI.fmtTime(Math.floor(p.updated / 1000)); } },
          { label: '', render: function (p) { return UI.btn(t('pr.open'), 'chev', function () { open(p); }, 'sm'); } }
        ], list, { empty: t('pr.none') }), null, true), detail);
    }, function (e) { UI.mount(el, h('div.callout.callout--danger', e.message)); });
  }

  window.FitAI = window.FitAI || {};
  window.FitAI.proposal = { card: card, page: proposalsPage, diff: diffView };
})();
