// Cluster → Groups (I144): resource groups of members, by a label selector (env=prod, region, tier!=db, !canary), by
// name, or both (GET/POST/PUT/DELETE /api/m/cluster/groups[/<name>], with the document's ETag as If-Match); a member is
// in any number of groups. The list shows each group's selector, its members by name and how many members it has now,
// coloured by their states. A group's form previews the members its selector and names select as they are typed
// (POST /api/m/cluster/select) and edits its variables, which I145's templates use. A group's page,
// #/cluster/group/<name>, shows its members' states with a box per member to select them, and the operations bar:
// Reload, Roll back, Check, Purge cache and Test, run on the members selected (POST /api/m/cluster/ops), each confirmed
// in a dialog that names them, then followed member by member (GET /api/m/cluster/ops/<id>) to each one's result.
// #/cluster/ops is the same page over every member, for a selection that is no group. The overview filters by group
// (overview.js). Needs shared.js; before cluster.js.
//
// 集群 → 分组（I144）：成员的资源分组，按标签选择器（env=prod, region, tier!=db, !canary）、按名称，或两者兼用
// （GET/POST/PUT/DELETE /api/m/cluster/groups[/<name>]，以文档的 ETag 作为 If-Match）；一个成员可属于任意多个分组。列表
// 显示每个分组的选择器、按名称列出的成员，以及当前成员数（按状态着色）。分组表单在输入时预览其选择器与名称选中的成员
// （POST /api/m/cluster/select），并编辑分组变量（供 I145 的模板使用）。分组页 #/cluster/group/<name> 显示其成员的状态，
// 每个成员一个复选框，以及操作栏：重新加载、回滚、一致性检查、清除缓存与测试，作用于选中的成员
// （POST /api/m/cluster/ops）；每次操作先在对话框中列出成员并确认，然后逐个成员跟踪其结果（GET /api/m/cluster/ops/<id>）。
// #/cluster/ops 是覆盖全部成员的同一页面，用于不属于任何分组的选择。概览按分组筛选（overview.js）。依赖 shared.js；
// 在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.groups', 'Groups', '分组'],
    // The list / 列表
    ['gr.add', 'Add a group', '添加分组'], ['gr.edit', 'Edit {name}', '编辑 {name}'],
    ['gr.name', 'Name', '名称'], ['gr.desc', 'Description', '说明'], ['gr.selector', 'Label selector', '标签选择器'],
    ['gr.members', 'Members by name', '按名称的成员'], ['gr.now', 'Members now', '当前成员'], ['gr.vars', 'Variables', '变量'],
    ['gr.count', '{n} of at most {max} groups', '{n} 个分组，最多 {max} 个'],
    ['gr.none', 'No groups yet. A group is the members a label selector matches, members named, or both; operations run on a group\'s members at once.',
      '还没有分组。分组是标签选择器匹配的成员、按名称指定的成员，或两者之和；操作可一次作用于分组的全部成员。'],
    ['gr.anyops', 'Operations on any members', '对任意成员操作'],
    ['gr.missing', 'not a member: {names}', '不是成员：{names}'],
    ['gr.n', '{n} members', '{n} 个成员'],
    // The form / 表单
    ['gr.name.hint', 'Letters, digits, \'_\', \'.\' and \'-\'.', '字母、数字、“_”、“.”与“-”。'],
    ['gr.selector.hint', 'Terms separated by commas, every one of which a member\'s labels must meet: key=value, key!=value, key (the label is there) or !key (it is not). Empty: members by name alone.',
      '以逗号分隔的条件，成员的标签须全部满足：key=value、key!=value、key（有该标签）或 !key（没有该标签）。留空则只按名称。'],
    ['gr.members.hint', 'Members in the group whatever their labels; a name kept for a member not added yet is listed as missing.',
      '无论标签如何都属于本分组的成员；尚未添加的成员名称会保留，并显示为缺失。'],
    ['gr.vars.hint', 'Values the group\'s templates use (I145): names of letters, digits and \'_\'. They are not secret: they are shown as written.',
      '分组的模板（I145）使用的值：名称由字母、数字与“_”组成。它们不是机密，按原样显示。'],
    ['gr.var.name', 'Name', '名称'], ['gr.var.value', 'Value', '值'], ['gr.var.add', 'Add a variable', '添加变量'],
    ['gr.preview', 'Members it selects now', '当前选中的成员'], ['gr.preview.none', 'No member yet.', '还没有成员。'],
    ['gr.save', 'Save', '保存'], ['gr.added', 'The group {name} added.', '已添加分组 {name}。'], ['gr.saved', 'The group {name} saved.', '已保存分组 {name}。'],
    ['gr.rm.ask', 'Remove the group {name}? Its members stay as they are; only the group goes.', '删除分组 {name}？其成员保持不变，只删除分组本身。'],
    ['gr.rm.done', 'The group {name} removed.', '已删除分组 {name}。'],
    ['gr.notfound', 'No group is named {name}.', '没有名为 {name} 的分组。'], ['gr.back', 'All groups', '全部分组'],
    ['gr.all', 'Every member', '全部成员'],
    // Operations / 操作
    ['op.reload', 'Reload', '重新加载'], ['op.rollback', 'Roll back', '回滚'], ['op.check', 'Check', '一致性检查'],
    ['op.purge', 'Purge cache', '清除缓存'], ['op.test', 'Test', '测试'],
    ['op.reload.what', 'Apply each member\'s running config again, as a new version, loading again what it loads from files (certificates, keys, token files).',
      '在每个成员上重新应用其运行中的配置（作为新版本），重新加载其从文件读取的内容（证书、密钥、令牌文件）。'],
    ['op.rollback.what', 'Apply again each member\'s previous config: the latest version it keeps whose content differs from the running one, if the running one has not changed meanwhile.',
      '在每个成员上重新应用其上一个配置：其保存的、内容与运行中配置不同的最新版本；若运行中的配置在此期间已变化则不回滚。'],
    ['op.check.what', 'Run each member\'s consistency check: the differences between what runs and its config.', '运行每个成员的一致性检查：运行状态与其配置之间的差异。'],
    ['op.purge.what', 'Remove responses each member\'s cache keeps, in a zone or in every zone.', '删除每个成员的缓存中保存的响应，限定某个 zone 或全部 zone。'],
    ['op.test.what', 'Read each member\'s GET /node, as the manager reaches it.', '按管理端访问成员的方式读取每个成员的 GET /node。'],
    ['op.title', '{op} on {n} members', '对 {n} 个成员执行{op}'],
    ['op.on', 'The members:', '成员：'], ['op.run', 'Run', '执行'], ['op.selected', '{n} of {all} selected', '已选 {n} / {all}'],
    ['op.select', 'Select', '选择'], ['op.selectall', 'Select all', '全选'], ['op.nosel', 'Select the members to run on.', '请选择要操作的成员。'],
    ['op.zone', 'Zone', 'Zone'], ['op.zone.hint', 'The cache zone (the cache middleware\'s zone); every zone when empty.', '缓存 zone（cache 中间件的 zone）；留空表示全部 zone。'],
    ['op.keys', 'Keys (URLs)', '键（URL）'], ['op.keys.hint', 'Absolute URLs as clients request them, one a line: https://example.com/a?b=1.', '客户端请求的绝对 URL，每行一个：https://example.com/a?b=1。'],
    ['op.prefixes', 'Prefixes', '前缀'], ['op.prefixes.hint', 'Every URL starting with one of these, one a line: https://example.com/img/. Needs fit with cache.prefix.',
      '以其中任一开头的全部 URL，每行一个：https://example.com/img/。需要支持 cache.prefix 的 fit。'],
    ['op.tags', 'Tags', '标签'], ['op.tags.hint', 'Cache-Tag or Surrogate-Key values, separated by spaces or lines.', 'Cache-Tag 或 Surrogate-Key 的值，以空格或换行分隔。'],
    ['op.purgeall', 'Everything in the zone (or every zone)', 'zone（或全部 zone）中的全部内容'],
    ['op.purge.empty', 'Name keys, prefixes or tags, or purge everything.', '请填写键、前缀或标签，或选择清除全部。'],
    ['op.started', '{op} started on {n} members.', '已对 {n} 个成员开始{op}。'],
    ['op.result', 'Results', '结果'], ['op.recent', 'Recent operations', '最近的操作'], ['op.recent.none', 'No operations yet.', '还没有操作。'],
    ['op.progress', '{done} done, {failed} failed, {left} to go', '{done} 个完成，{failed} 个失败，{left} 个待完成'],
    ['op.finished', 'Finished: {done} done, {failed} failed', '已结束：{done} 个完成，{failed} 个失败'],
    ['op.s.pending', 'Waiting', '等待中'], ['op.s.running', 'Running', '执行中'], ['op.s.done', 'Done', '完成'], ['op.s.failed', 'Failed', '失败'],
    ['op.member', 'Member', '成员'], ['op.status', 'Status', '状态'], ['op.outcome', 'Outcome', '结果'], ['op.took', 'Took', '耗时'],
    ['op.when', 'Started', '开始'], ['op.who', 'By', '用户'], ['op.what', 'Operation', '操作'], ['op.where', 'On', '对象'],
    ['op.view', 'Results', '结果'],
    ['nd.a.group.add', 'group added', '添加分组'], ['nd.a.group.update', 'group changed', '修改分组'], ['nd.a.group.remove', 'group removed', '删除分组'],
    ['nd.a.op.reload', 'reloaded', '重新加载'], ['nd.a.op.rollback', 'rolled back', '回滚'], ['nd.a.op.check', 'checked', '一致性检查'],
    ['nd.a.op.purge', 'purged caches', '清除缓存'], ['nd.a.op.test', 'tested', '测试'],
    ['ov.group', 'Group', '分组'], ['ov.group.all', 'All groups', '全部分组'],
    ['k.refused', 'Refused', '被拒绝'],
    ['k.refused.hint', 'The member refused the operation, or could not do it: its message says why. A config it did not apply leaves the running one as it was.',
      '成员拒绝了该操作或无法完成：原因见其消息。未应用的配置不会改变运行中的配置。'],
    ['k.no_earlier_config', 'Nothing to roll back to', '没有可回滚的版本'],
    ['k.no_earlier_config.hint', 'The member keeps no config before the running one that differs from it: its history holds only the running config, or the same config applied again.',
      '成员没有保存与运行中配置不同的更早版本：其历史中只有运行中的配置，或同一配置的再次应用。']
  );
  ['refused', 'no_earlier_config'].forEach(function (k) { if (FC.KINDS.indexOf(k) < 0) FC.KINDS.push(k); });

  var OPS = [['reload', 'refresh'], ['rollback', 'undo'], ['check', 'activity'], ['purge', 'disk'], ['test', 'zap']];
  var OP_KIND = { pending: '', running: 'info', done: 'ok', failed: 'danger' };

  // groups reads the groups and the document's ETag. / 读取分组及文档的 ETag。
  function groups(UI) {
    return FC.call(UI, 'GET', '/api/groups').then(function (r) {
      var b = r.body || {};
      return { etag: r.etag, groups: b.groups || [], maxGroups: b.max_groups || 0, readOnly: b.read_only || '' };
    });
  }
  function groupPath(name) { return '/api/groups/' + encodeURIComponent(name); }
  function lines(s) { return String(s || '').split(/\n/).map(function (x) { return x.trim(); }).filter(Boolean); }
  function words(s) { return String(s || '').split(/[\s,]+/).map(function (x) { return x.trim(); }).filter(Boolean); }

  // ---- The group's form / 分组表单 ----
  // groupForm opens the form of a new group (o.group null) or of o.group, previewing its members, and saves it.
  // groupForm 打开新分组（o.group 为 null）或 o.group 的表单，预览其成员并保存。
  function groupForm(UI, o) {
    var g = o.group, editing = !!g, h = UI.h, t = UI.t;
    g = g || { name: '', description: '', selector: '', members: [], variables: {} };
    var name = h('input.input', { name: 'group-name', value: g.name, autocomplete: 'off', spellcheck: 'false', placeholder: 'edge-eu' });
    var desc = h('input.input', { name: 'group-description', value: g.description || '', autocomplete: 'off' });
    var sel = h('input.input.cl-mono', { name: 'group-selector', value: g.selector || '', autocomplete: 'off', spellcheck: 'false', placeholder: 'env=prod, region=eu' });
    var names = h('textarea.textarea.cl-mono', { name: 'group-members', rows: 3, spellcheck: 'false', placeholder: 'edge-1, edge-2' }, (g.members || []).join(', '));
    var vars = h('div.stack.cl-vars'), preview = h('div.cl-preview');
    var fields = {};
    function field(key, label, el, hint) {
      var err = h('span.field__hint.cl-err', { hidden: true });
      fields[key] = err;
      var f = UI.field(label, el, hint);
      f.appendChild(err);
      return f;
    }
    function varRow(k, v) {
      var key = h('input.input.input--sm.cl-mono', { value: k || '', placeholder: 'region', 'aria-label': t('gr.var.name'), spellcheck: 'false' });
      var val = h('input.input.input--sm', { value: v || '', placeholder: 'eu-west-1', 'aria-label': t('gr.var.value'), spellcheck: 'false' });
      var row = h('div.cl-var', key, val, UI.btn('', 'x', function () { row.remove(); }, 'ghost sm icon'));
      return row;
    }
    Object.keys(g.variables || {}).sort().forEach(function (k) { vars.appendChild(varRow(k, g.variables[k])); });
    var addVar = UI.btn(t('gr.var.add'), 'plus', function () { vars.appendChild(varRow('', '')); }, 'ghost sm');
    var err = h('div.callout.callout--danger', { hidden: true });

    function draft() {
      var vs = {};
      vars.querySelectorAll('.cl-var').forEach(function (r) {
        var ins = r.querySelectorAll('input');
        if (ins[0].value.trim()) vs[ins[0].value.trim()] = ins[1].value;
      });
      return { name: name.value.trim(), description: desc.value.trim(), selector: sel.value.trim(), members: words(names.value), variables: vs };
    }
    function clearErrors() {
      err.hidden = true;
      Object.keys(fields).forEach(function (k) { fields[k].hidden = true; });
    }
    function showError(e) {
      var f = e && e.field ? e.field.split('.')[0] : '';
      if (f && fields[f]) {
        fields[f].textContent = e.message.replace(/^[a-z.]+: /, '');
        fields[f].hidden = false;
      } else {
        err.textContent = e && e.message ? e.message : String(e);
        err.hidden = false;
      }
    }
    // The preview follows what is typed, a moment after. / 预览跟随输入，稍有延迟。
    var timer = 0, seq = 0;
    function previewSoon() { clearTimeout(timer); timer = setTimeout(doPreview, 250); }
    function doPreview() {
      var d = draft(), mine = ++seq;
      fields.selector.hidden = true;
      if (!d.selector && !d.members.length) { UI.mount(preview, h('span.muted.small', t('gr.preview.none'))); return; }
      FC.call(UI, 'POST', '/api/select', { selector: d.selector, members: d.members }).then(function (r) {
        if (mine !== seq) return;
        var b = r.body || {};
        UI.mount(preview, h('div.stack',
          (b.nodes || []).length ? h('div.cl-chips', b.nodes.map(function (n) { return h('a.cl-chip', { href: '#/cluster/node/' + encodeURIComponent(n), target: '_blank' }, n); }))
            : h('span.muted.small', t('gr.preview.none')),
          (b.missing || []).length ? h('div.small.cl-warn', t('gr.missing', { names: b.missing.join(', ') })) : null));
      }, function (e) {
        if (mine !== seq) return;
        UI.clear(preview);
        showError(e);
      });
    }
    [sel, names].forEach(function (x) { x.addEventListener('input', previewSoon); });

    var body = h('form.form.cl-form', { onsubmit: function (e) { e.preventDefault(); } },
      h('div.form-row', field('name', t('gr.name'), name, t('gr.name.hint')), field('description', t('gr.desc'), desc)),
      field('selector', t('gr.selector'), sel, t('gr.selector.hint')),
      field('members', t('gr.members'), names, t('gr.members.hint')),
      h('div.field', h('span.field__label', t('gr.preview')), preview),
      h('div.field', h('span.field__label', t('gr.vars')), vars, h('div.row', addVar), h('span.field__hint', t('gr.vars.hint')), fields.variables = h('span.field__hint.cl-err', { hidden: true })),
      err);
    doPreview();
    UI.modal({
      title: editing ? t('gr.edit', { name: g.name }) : t('gr.add'), wide: true, body: body,
      actions: [{ label: t('cancel') }, {
        label: t('gr.save'), kind: 'primary', onclick: function () {
          clearErrors();
          var d = draft(), p = editing ? FC.call(UI, 'PUT', groupPath(g.name), d, o.etag) : FC.call(UI, 'POST', '/api/groups', d, o.etag);
          return p.then(function (r) {
            UI.toast(t(editing ? 'gr.saved' : 'gr.added', { name: (r.body && r.body.name) || d.name }));
            if (o.then) o.then(r.body);
          }, function (e) {
            if (e.status === 412) { UI.toast(t('fm.stale'), 'error'); if (o.reload) o.reload(); return false; }
            showError(e);
            return false;
          });
        }
      }]
    });
  }

  // stateCounts are a group's members counted by state, as badges. / 按状态统计分组成员，显示为徽章。
  function stateCounts(UI, names, states) {
    var c = { up: 0, error: 0, down: 0, unknown: 0 };
    names.forEach(function (n) { var s = states[n]; c[s && s.status in c ? s.status : 'unknown']++; });
    return UI.h('span.btn-row', UI.badge(UI.t('gr.n', { n: names.length }), 'info'),
      ['up', 'error', 'down', 'unknown'].map(function (k) { return c[k] ? UI.badge(c[k] + ' ' + FC.stateWord(UI, k), FC.STATE_KIND[k] || '', true) : null; }));
  }

  FC.pages.groups = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('groups', t('n.groups'));
    var data = null, states = {};
    var head = h('div.row.row--between.cl-bar'), body = h('div');
    UI.mount(el, h('div.stack', head, body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { read(); }, 'ghost sm'));

    function edit(g) { groupForm(UI, { group: g, etag: data.etag, reload: read, then: read }); }
    function remove(g) {
      UI.confirm(t('gr.rm.ask', { name: g.name }), t('rm.ok'), true).then(function (yes) {
        if (!yes) return;
        FC.call(UI, 'DELETE', groupPath(g.name), undefined, data.etag).then(function () {
          UI.toast(t('gr.rm.done', { name: g.name }));
          read();
        }, function (e) { UI.toast(e.status === 412 ? t('fm.stale') : e.message, 'error'); read(); });
      });
    }
    function draw() {
      if (!data || !document.body.contains(el)) return;
      // Groups decide roles: only a cluster-wide admin changes them (I148). / 分组决定角色：只有集群范围的管理员才能修改（I148）。
      var admin = FC.canCluster('admin');
      var addBtn = admin ? UI.btn(t('gr.add'), 'plus', function () { groupForm(UI, { etag: data.etag, reload: read, then: read }); }, 'primary') : null;
      if (addBtn) addBtn.disabled = !!data.readOnly || data.groups.length >= data.maxGroups;
      UI.mount(head, h('span.btn-row', addBtn, h('a.btn.btn--ghost', { href: '#/cluster/ops' }, UI.icon('play'), t('gr.anyops'))),
        h('span.small.muted', t('gr.count', { n: data.groups.length, max: data.maxGroups })));
      var parts = [];
      if (data.readOnly) parts.push(h('div.callout.callout--warn', t('readonly', { why: data.readOnly })));
      if (!data.groups.length) parts.push(UI.empty(t('gr.none'), 'cube'));
      else parts.push(UI.card(null, UI.table([
        { label: t('gr.name'), render: function (g) {
          return h('div', h('a', { href: '#/cluster/group/' + encodeURIComponent(g.name) }, h('b', g.name)), g.description ? h('div.small.muted', g.description) : null);
        } },
        { label: t('gr.selector'), render: function (g) { return g.selector ? h('code.small', g.selector) : h('span.muted', '—'); } },
        { label: t('gr.members'), render: function (g) {
          return h('div', (g.members || []).length ? h('span.small', g.members.join(', ')) : h('span.muted', '—'),
            (g.missing || []).length ? h('div.small.cl-warn', t('gr.missing', { names: g.missing.join(', ') })) : null);
        } },
        { label: t('gr.now'), render: function (g) { return stateCounts(UI, g.nodes || [], states); } },
        { label: t('gr.vars'), cls: 'num', render: function (g) { return String(Object.keys(g.variables || {}).length); } },
        { label: '', cls: 'actions', render: function (g) {
          if (!admin) return null;
          var eb = UI.btn(t('edit'), 'edit', function () { edit(g); }, 'ghost sm'), rb = UI.btn(t('delete'), 'trash', function () { remove(g); }, 'ghost sm');
          eb.title = t('edit') + ' ' + g.name; rb.title = t('delete') + ' ' + g.name;
          eb.disabled = rb.disabled = !!data.readOnly;
          return h('span.btn-row', eb, rb);
        } }
      ], data.groups, { onRowClick: function (g) { location.hash = '#/cluster/group/' + encodeURIComponent(g.name); } }), null, true));
      UI.mount(body, h('div.stack', parts));
    }
    function read() {
      return Promise.all([groups(UI), FC.call(UI, 'GET', '/api/state')]).then(function (r) {
        data = r[0];
        states = {};
        ((r[1].body && r[1].body.nodes) || []).forEach(function (s) { states[s.name] = s; });
        draw();
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    read();
  };

  // ---- Operations / 操作 ----
  // purgeForm is a purge's fields; its value() is the purge's params, or null with the form's error shown.
  // purgeForm 为清除缓存的字段；value() 返回参数，出错时显示错误并返回 null。
  function purgeForm(UI) {
    var h = UI.h, t = UI.t;
    var zone = h('input.input', { name: 'purge-zone', autocomplete: 'off', spellcheck: 'false', placeholder: 'site' });
    var keys = h('textarea.textarea.cl-mono', { name: 'purge-keys', rows: 2, spellcheck: 'false', placeholder: 'https://example.com/index.html' });
    var prefixes = h('textarea.textarea.cl-mono', { name: 'purge-prefixes', rows: 2, spellcheck: 'false', placeholder: 'https://example.com/img/' });
    var tags = h('input.input.cl-mono', { name: 'purge-tags', autocomplete: 'off', spellcheck: 'false', placeholder: 'product-42' });
    var all = h('input', { type: 'checkbox', name: 'purge-all' });
    var err = h('div.callout.callout--danger', { hidden: true });
    var rest = h('div.stack', UI.field(t('op.keys'), keys, t('op.keys.hint')), UI.field(t('op.prefixes'), prefixes, t('op.prefixes.hint')),
      UI.field(t('op.tags'), tags, t('op.tags.hint')));
    all.addEventListener('change', function () { rest.hidden = all.checked; });
    return {
      node: h('div.stack', UI.field(t('op.zone'), zone, t('op.zone.hint')), h('label.row.cl-check', all, h('span', t('op.purgeall'))), rest, err),
      value: function () {
        var p = { zone: zone.value.trim(), keys: lines(keys.value), prefixes: lines(prefixes.value), tags: words(tags.value), all: all.checked };
        if (p.all) { p.keys = []; p.prefixes = []; p.tags = []; }
        if (!p.all && !p.keys.length && !p.prefixes.length && !p.tags.length) {
          err.textContent = t('op.purge.empty'); err.hidden = false;
          return null;
        }
        err.hidden = true;
        return p;
      },
      error: function (e) { err.textContent = e.message; err.hidden = false; }
    };
  }

  // confirmOp asks a person to confirm op on the members named, with a purge's form: a Promise of the operation
  // started, or null. / confirmOp 列出成员请人确认操作（清除缓存时带表单）：返回已开始的操作或 null 的 Promise。
  function confirmOp(UI, op, names, group) {
    var h = UI.h, t = UI.t;
    return new Promise(function (resolve) {
      var done = false, purge = op === 'purge' ? purgeForm(UI) : null;
      UI.modal({
        title: t('op.title', { op: t('op.' + op), n: names.length }), wide: !!purge,
        body: h('div.stack', h('div.small', t('op.' + op + '.what')),
          h('div', h('div.small.muted', t('op.on')), h('div.cl-chips', names.map(function (n) { return h('span.cl-chip', n); }))),
          purge ? purge.node : null),
        onclose: function () { if (!done) resolve(null); },
        actions: [{ label: t('cancel') }, {
          label: t('op.run') + ' · ' + t('op.' + op), kind: op === 'rollback' || op === 'purge' ? 'danger' : 'primary', onclick: function () {
            var req = { op: op, members: names };
            if (group) req.group = group;
            if (purge) {
              req.purge = purge.value();
              if (!req.purge) return false;
            }
            return FC.call(UI, 'POST', '/api/ops', req).then(function (r) {
              done = true;
              UI.toast(t('op.started', { op: t('op.' + op), n: names.length }));
              resolve(r.body);
            }, function (e) {
              if (purge) purge.error(e); else UI.toast(e.message, 'error');
              return false;
            });
          }
        }]
      });
    });
  }

  // opResults draws an operation's progress and each member's outcome. / 绘制操作的进度与每个成员的结果。
  function opResults(UI, op) {
    var h = UI.h, t = UI.t, c = op.counts || {};
    var left = (c.pending || 0) + (c.running || 0);
    var head = op.finished ? t('op.finished', { done: c.done || 0, failed: c.failed || 0 }) : t('op.progress', { done: c.done || 0, failed: c.failed || 0, left: left });
    return h('div.stack',
      h('div.row', op.finished ? UI.icon(c.failed ? 'alert' : 'check') : h('span.spinner'),
        h('b', t('op.' + op.op)), h('span.muted', head), h('span.small.muted', '· ' + op.user + ' · ' + FC.when(op.started))),
      UI.table([
        { label: t('op.member'), render: function (x) { return h('a', { href: '#/cluster/node/' + encodeURIComponent(x.node) }, h('b', x.node)); } },
        { label: t('op.status'), render: function (x) { return UI.badge(t('op.s.' + x.status), OP_KIND[x.status] || '', true); } },
        { label: t('op.outcome'), render: function (x) {
          if (x.error) return FC.problemBox(UI, x.error);
          return x.detail ? h('span', x.detail) : h('span.muted', '—');
        } },
        { label: t('op.took'), cls: 'num', render: function (x) { return x.ended ? t('f.ms', { n: x.took_ms || 0 }) : '—'; } }
      ], op.outcomes || []));
  }

  // follow reads the operation id every second into box until it finishes, while box is shown; then calls after.
  // follow 每秒读取操作 id 并绘制到 box 中，直到其结束（box 显示期间）；结束后调用 after。
  function follow(UI, id, box, after) {
    function tick() {
      if (!document.body.contains(box)) return;
      FC.call(UI, 'GET', '/api/ops/' + encodeURIComponent(id)).then(function (r) {
        if (!document.body.contains(box)) return;
        UI.mount(box, opResults(UI, r.body || {}));
        if (r.body && r.body.finished) { if (after) after(r.body); return; }
        setTimeout(tick, 1000);
      }, function (e) {
        if (!document.body.contains(box)) return;
        UI.mount(box, h0(UI, e));
      });
    }
    tick();
  }
  function h0(UI, e) { return UI.h('div.callout.callout--danger', e && e.message ? e.message : String(e)); }

  // opsPage is a group's page, or with no group the page over every member: the members' states, a box each, the
  // operations bar, the results of the latest operation and the recent operations.
  // opsPage 为分组页；没有分组时为覆盖全部成员的页面：成员状态（每个一个复选框）、操作栏、最近一次操作的结果与最近的操作。
  function opsPage(UI, sh, group) {
    var h = UI.h, t = UI.t;
    var el = group ? sh.page('groups', group, t('m.cluster') + ' / ' + t('n.groups') + ' / ' + group)
      : sh.page('groups', t('gr.anyops'), t('m.cluster') + ' / ' + t('n.groups') + ' / ' + t('gr.anyops'));
    var info = h('div.stack'), bar = h('div.row.row--between.cl-bar'), list = h('div'), results = h('div'), recent = h('div');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/groups' }, '← ' + t('gr.back'))), info, bar, list, results, recent));
    UI.mount(list, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { read(); }, 'ghost sm'));
    var g = null, etag = '', states = [], picked = null, readOnly = '', following = '';

    function names() { return states.map(function (s) { return s.name; }); }
    function selected() { return names().filter(function (n) { return picked[n]; }); }
    function drawInfo() {
      if (!group) { UI.clear(info); return; }
      var vars = Object.keys(g.variables || {}).sort();
      UI.mount(info, UI.card(g.name, h('div.stack',
        g.description ? h('div', g.description) : null,
        UI.kv([
          [t('gr.selector'), g.selector ? h('code', g.selector) : '—'],
          [t('gr.members'), (g.members || []).length ? g.members.join(', ') : '—'],
          [t('gr.now'), stateCounts(UI, g.nodes || [], byName())],
          [t('gr.vars'), vars.length ? h('div.cl-vars-view', vars.map(function (k) { return h('div', h('code', k), ' = ', h('span', g.variables[k])); })) : '—']
        ]),
        (g.missing || []).length ? h('div.small.cl-warn', t('gr.missing', { names: g.missing.join(', ') })) : null),
        h('span.btn-row', (function () {
          if (!FC.canCluster('admin')) return null;
          var b = UI.btn(t('edit'), 'edit', function () {
            groupForm(UI, { group: g, etag: etag, reload: read, then: function (saved) {
              if (saved && saved.name && saved.name !== group) location.hash = '#/cluster/group/' + encodeURIComponent(saved.name); else read();
            } });
          }, 'ghost sm');
          b.disabled = !!readOnly;
          return b;
        })())));
    }
    function byName() { var m = {}; states.forEach(function (s) { m[s.name] = s; }); return m; }
    function drawBar() {
      // Operations are an operator's, on each member picked (I148). / 操作需要在所选的每个成员上是操作员（I148）。
      if (!FC.canAny('operator')) { UI.clear(bar); return; }
      var sel = selected();
      var btns = OPS.map(function (o) {
        var b = UI.btn(t('op.' + o[0]), o[1], function () { run(o[0]); }, o[0] === 'rollback' || o[0] === 'purge' ? 'ghost sm' : 'ghost sm');
        b.disabled = !sel.length || !!readOnly;
        b.title = t('op.' + o[0] + '.what');
        return b;
      });
      UI.mount(bar, h('span.btn-row.cl-ops', btns), h('span.small.muted', sel.length ? t('op.selected', { n: sel.length, all: states.length }) : t('op.nosel')));
    }
    function drawList() {
      if (!states.length) {
        UI.mount(list, UI.empty(group ? t('gr.preview.none') : t('ov.none'), 'server'));
        return;
      }
      var allBox = h('input', { type: 'checkbox', 'aria-label': t('op.selectall'), checked: selected().length === states.length });
      allBox.indeterminate = selected().length > 0 && selected().length < states.length;
      allBox.addEventListener('change', function () { states.forEach(function (s) { picked[s.name] = allBox.checked && FC.canOn('operator', s.name); }); drawList(); drawBar(); });
      UI.mount(list, UI.card(null, UI.table([
        { label: allBox, cls: 'cl-pick', render: function (s) {
          var b = h('input', { type: 'checkbox', checked: !!picked[s.name], 'aria-label': t('op.select') + ' ' + s.name, disabled: !FC.canOn('operator', s.name) });
          b.addEventListener('change', function () { picked[s.name] = b.checked; drawList(); drawBar(); });
          return b;
        } },
        { label: t('ov.name'), render: function (s) {
          return h('div', h('a', { href: '#/cluster/node/' + encodeURIComponent(s.name) }, h('b', s.name)), FC.labelChips(UI, s.labels));
        } },
        { label: t('ov.state'), render: function (s) {
          return h('div', FC.stateBadge(UI, s.status), s.error ? h('div.small.muted.cl-msg', { title: s.error.message }, FC.kindWord(UI, s.error.kind)) : null);
        } },
        { label: t('f.fit'), render: function (s) { return s.node ? h('code', s.node.fit_version || '—') : '—'; } },
        { label: t('f.config'), render: function (s) { return s.config ? 'v' + s.config.version : '—'; } },
        { label: t('f.drift'), render: function (s) {
          return s.drift ? (s.drift.count ? UI.badge(t('f.drift.n', { n: s.drift.count }), 'warn') : UI.badge(t('f.drift.none'), 'ok')) : '—';
        } },
        { label: t('n.groups'), render: function (s) {
          return h('span.cl-chips', (s.groups || []).map(function (x) { return h('a.cl-chip', { href: '#/cluster/group/' + encodeURIComponent(x) }, x); }));
        } }
      ], states), null, true));
    }
    function run(op) {
      var sel = selected();
      if (!sel.length) return;
      confirmOp(UI, op, sel, group).then(function (started) {
        if (!started) return;
        showOp(started.id);
      });
    }
    function showOp(id) {
      following = id;
      var box = h('div');
      UI.mount(results, UI.card(t('op.result'), box));
      follow(UI, id, box, function () { if (following === id) { readRecent(); readStates(); } });
      readRecent();
    }
    function readRecent() {
      FC.call(UI, 'GET', '/api/ops').then(function (r) {
        if (!document.body.contains(el)) return;
        var ops = ((r.body && r.body.ops) || []).filter(function (o) { return !group || o.group === group; }).slice(0, 10);
        UI.mount(recent, UI.card(t('op.recent'), UI.table([
          { label: t('op.when'), render: function (o) { return h('span.small', FC.when(o.started)); } },
          { label: t('op.what'), render: function (o) { return h('b', t('op.' + o.op)); } },
          { label: t('op.where'), render: function (o) {
            return h('span.small', o.group ? o.group + ' · ' : '', (o.outcomes || []).map(function (x) { return x.node; }).join(', '));
          } },
          { label: t('op.who'), render: function (o) { return o.user; } },
          { label: t('op.status'), render: function (o) {
            var c = o.counts || {};
            return o.finished ? h('span.btn-row', UI.badge(c.done + ' ' + t('op.s.done'), 'ok'), c.failed ? UI.badge(c.failed + ' ' + t('op.s.failed'), 'danger') : null)
              : UI.badge(t('op.s.running'), 'info', true);
          } },
          { label: '', cls: 'actions', render: function (o) { return UI.btn(t('op.view'), 'history', function () { showOp(o.id); }, 'ghost sm'); } }
        ], ops, { empty: t('op.recent.none') }), null, true));
      }, function () { /* ignore / 忽略 */ });
    }
    // readStates reads the members' states: the group's (with the group), or every member's.
    // readStates 读取成员状态：有分组时为分组的，否则为全部成员的。
    function readStates() {
      var p = group ? FC.call(UI, 'GET', groupPath(group)).then(function (r) {
        g = r.body || {}; etag = r.etag;
        return g.states || [];
      }) : FC.call(UI, 'GET', '/api/state').then(function (r) { readOnly = (r.body && r.body.read_only) || ''; return (r.body && r.body.nodes) || []; });
      return p.then(function (ss) {
        if (!document.body.contains(el)) return;
        states = ss;
        // Every member is selected at first; one that joins later is too. / 起初选中全部成员；之后加入的成员也选中。
        if (!picked) picked = {};
        states.forEach(function (s) { if (!(s.name in picked)) picked[s.name] = FC.canOn('operator', s.name); });
        drawInfo(); drawList(); drawBar();
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(list, e.status === 404 ? UI.empty(t('gr.notfound', { name: group }), 'cube') : h0(UI, e));
        UI.clear(bar); UI.clear(info);
      });
    }
    function read() {
      if (group) groups(UI).then(function (d) { readOnly = d.readOnly; }, function () { /* ignore / 忽略 */ });
      readStates(); readRecent();
    }
    read();
    UI.every(10000, function () { if (!document.querySelector('.modal')) readStates(); });
  }

  FC.pages.group = function (UI, sh, p) { opsPage(UI, sh, p.name); };
  FC.pages.ops = function (UI, sh) { opsPage(UI, sh, ''); };
  FC.groups = groups;
  FC.groupForm = groupForm;
  FC.confirmOp = confirmOp;
})();
