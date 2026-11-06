// Cluster → Templates (I145): config the manager owns on the members of the groups a template is attached to. A
// template is a fragment of fit's config: its top-level members owned whole (log, media_auth, ...), and the elements of
// listeners, routes, services and upstreams owned one by one, by name; its strings may use variables, ${node.name},
// ${node.id}, ${node.hostname}, ${node.labels.<key>} and a group's ${<name>}. A member's desired config is its own with
// the parts its templates own taken from them, a template of a higher order winning a part two own.
//
// The pages: #/cluster/templates, the list with each template's groups, parts and members' sync states, Import from a
// node and Compare; #/cluster/template/<name> (and #/cluster/newtemplate), a template's editor (its fields, its config
// as a form and as .conf, .yaml and .json text, converted by POST /api/m/cluster/text/render and /text/parse, secrets
// shown as [secret] and kept as they are when sent back so), the parts it owns, its variables and where each comes
// from, its warnings, and its members, each one's sync state read now (POST /api/m/cluster/sync) with each part's diff;
// #/cluster/rendered/<member>, a member's config beside its desired config (GET /api/m/cluster/desired/<member>);
// #/cluster/compare, two members' configs side by side, part by part (POST /api/m/cluster/compare). Nothing here
// applies anything to a member: reading a sync state tells the member what the manager owns there (its
// PUT /node/managed), which its own pages mark; rollouts (I146, rollouts.js) apply, opened by Roll out on a template's
// page and Sync this node on a rendered result. Needs shared.js; before cluster.js.
//
// 集群 → 模板（I145）：管理端在模板所关联分组的成员上拥有的配置。模板是 fit 配置的片段：其顶层成员整体拥有（log、
// media_auth 等），listeners、routes、services、upstreams 的元素按名称逐个拥有；字符串可使用变量 ${node.name}、
// ${node.id}、${node.hostname}、${node.labels.<key>} 与分组变量 ${<name>}。成员的期望配置是其自身配置，再用模板拥有的部分
// 替换；两个模板拥有同一部分时，order 较大的模板胜出。
// 页面：#/cluster/templates 列表（各模板的分组、部分与成员同步状态，以及“从节点导入”与“对比”）；#/cluster/template/<name>
// （与 #/cluster/newtemplate）模板编辑器（字段；配置的表单与 .conf、.yaml、.json 文本，经 POST /api/m/cluster/text/render 与
// /text/parse 转换；密钥显示为 [secret]，原样发回即保留）、其拥有的部分、变量及来源、警告，以及其成员（即时读取的同步
// 状态，POST /api/m/cluster/sync，每个部分附差异）；#/cluster/rendered/<member> 成员的配置与期望配置并排
// （GET /api/m/cluster/desired/<member>）；#/cluster/compare 两个成员的配置并排、逐部分对比（POST /api/m/cluster/compare）。
// 这里不向成员应用任何配置：读取同步状态会告知成员管理端拥有哪些部分（成员的 PUT /node/managed），由成员自己的页面标出；
// 应用由发布（I146）完成。依赖 shared.js；在 cluster.js 之前加载。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['n.templates', 'Templates', '模板'], ['n.compare', 'Compare', '对比'],
    ['tp.add', 'Add a template', '添加模板'], ['tp.import', 'Import from a node', '从节点导入'], ['tp.compare', 'Compare two nodes', '对比两个节点'],
    ['tp.none', 'No templates yet. A template is config the manager owns on the members of the groups it is attached to: whole top-level members, and routes, upstreams, listeners and services by name.',
      '还没有模板。模板是管理端在其关联分组的成员上拥有的配置：整个顶层成员，以及按名称的路由、上游、监听与服务。'],
    ['tp.name', 'Name', '名称'], ['tp.desc', 'Description', '说明'], ['tp.groups', 'Groups', '分组'], ['tp.order', 'Order', '顺序'],
    ['tp.parts', 'Parts it owns', '拥有的部分'], ['tp.members', 'Members', '成员'], ['tp.vars', 'Variables', '变量'], ['tp.warnings', 'Warnings', '警告'],
    ['tp.count', '{n} of at most {max} templates', '{n} 个模板，最多 {max} 个'],
    ['tp.missing', 'no such group: {names}', '没有这些分组：{names}'],
    ['tp.order.hint', 'Where two templates own the same part of a member, the one of the higher order wins (then the later name); -1000 to 1000.',
      '两个模板拥有成员的同一部分时，order 较大者胜出（相同则名称靠后者胜出）；取值 -1000 到 1000。'],
    ['tp.groups.hint', 'The members of these groups get the template. Their variables are the template\'s ${name}s, a later group\'s winning.',
      '这些分组的成员使用该模板。分组变量即模板中的 ${name}，靠后的分组优先。'],
    ['tp.config', 'Config', '配置'],
    ['tp.config.hint', 'A part of fit\'s config, as in a config file: top-level members are owned whole; listeners, routes, services and upstreams element by element, each named. Strings may use ${node.name}, ${node.id}, ${node.hostname}, ${node.labels.<key>} and the groups\' ${<name>}; $${ writes ${. [secret] stands for a secret the manager keeps.',
      'fit 配置的一部分，写法同配置文件：顶层成员整体拥有；listeners、routes、services、upstreams 按元素拥有，每个元素须有名称。字符串可使用 ${node.name}、${node.id}、${node.hostname}、${node.labels.<key>} 与分组的 ${<name>}；$${ 表示字面的 ${。[secret] 代表管理端保存的密钥。'],
    ['tp.save', 'Save', '保存'], ['tp.saved', 'The template {name} saved.', '已保存模板 {name}。'], ['tp.added', 'The template {name} added.', '已添加模板 {name}。'],
    ['tp.rm.ask', 'Remove the template {name}? Its members keep what it gave them; the manager no longer owns it there, and tells them so.',
      '删除模板 {name}？其成员保留模板给出的配置；管理端不再拥有这些部分，并会告知成员。'],
    ['tp.rm.done', 'The template {name} removed.', '已删除模板 {name}。'], ['tp.back', 'All templates', '全部模板'],
    ['tp.notfound', 'No template is named {name}.', '没有名为 {name} 的模板。'],
    ['tp.addpart', 'Add a part', '添加部分'], ['tp.addel', 'Add {what}', '添加 {what}'], ['tp.addfield', 'Add a field…', '添加字段…'],
    ['tp.leaveout', 'Leave out', '移除'], ['tp.json.bad', 'Not JSON: {e}', '不是 JSON：{e}'],
    ['tp.raw.bad', 'The text does not parse: fix it before saving.', '文本无法解析：请先修正再保存。'],
    ['tp.raw.note', 'The template\'s config as {fmt}. Edits are parsed as you type.', '模板配置的 {fmt} 文本。输入时即时解析。'],
    ['tp.tab.ui', 'Form', '表单'],
    ['tp.var.node', 'the member\'s own', '成员自身'], ['tp.var.from', 'from {groups}', '来自 {groups}'], ['tp.var.none', 'in none of its groups', '其分组都没有'],
    ['tp.nowarn', 'None.', '无。'],
    ['tp.sync', 'Members and their sync state', '成员及其同步状态'], ['tp.read', 'Read now', '立即读取'],
    ['tp.read.note', 'Reading compares each member\'s config with what its templates render, and tells the member what the manager owns there; it changes nothing else on it.',
      '读取会比较每个成员的配置与其模板渲染的结果，并告知成员管理端拥有哪些部分；不会改变成员上的其他内容。'],
    ['tp.notread', 'Not read yet: read now.', '尚未读取：请立即读取。'], ['tp.pending', 'Still being read: read again in a moment.', '仍在读取：稍后再读取一次。'],
    ['tp.rendered', 'Rendered result', '渲染结果'], ['tp.have', 'On the member', '成员上的值'], ['tp.want', 'From the template', '模板给出的值'],
    ['tp.absent', '(not there)', '（不存在）'],
    ['sy.in_sync', 'In sync', '已同步'], ['sy.out_of_sync', 'Out of sync', '未同步'], ['sy.conflict', 'Conflict', '冲突'],
    ['sy.error', 'Error', '错误'], ['sy.unmanaged', 'No template', '无模板'], ['sy.pending', 'Reading', '读取中'],
    ['sy.r.missing', 'not on the member yet', '成员上还没有'],
    ['sy.r.template_changed', 'the template changed since the member was in sync', '成员同步之后模板已修改'],
    ['sy.r.changed_here', 'changed on the member since it was in sync', '同步之后在成员上被修改'],
    ['sy.r.removed_here', 'removed on the member since it was in sync', '同步之后在成员上被删除'],
    ['sy.r.own_value', 'the member\'s own value, from before the template owned it', '成员自己的值，早于模板拥有该部分'],
    ['sy.counts', '{s} in sync, {o} out of sync, {c} in conflict', '{s} 个已同步，{o} 个未同步，{c} 个冲突'],
    ['sy.told', 'told what it owns', '已告知其被管理的部分'], ['sy.kept', 'knows what it owns', '已知其被管理的部分'],
    ['sy.unsupported', 'an older fit: its pages cannot mark what the manager owns', '较旧的 fit：其页面无法标出管理端拥有的部分'],
    ['sy.failed', 'not told what it owns: {e}', '未能告知其被管理的部分：{e}'],
    ['sy.overrides', 'overrides {names}', '覆盖 {names}'], ['sy.released', 'No longer owned: {parts}', '不再拥有：{parts}'],
    ['sy.part', 'Part', '部分'], ['sy.template', 'Template', '模板'], ['sy.state', 'State', '状态'], ['sy.why', 'Why', '原因'],
    ['rd.title', 'Rendered for {name}', '{name} 的渲染结果'], ['rd.current', 'Its config now', '当前配置'], ['rd.desired', 'Its desired config', '期望配置'],
    ['rd.note', 'The desired config is the member\'s own with the parts its templates own taken from them; a rollout (I146) applies it. Secrets are masked on both sides.',
      '期望配置是成员自身的配置，再用其模板拥有的部分替换；由发布（I146）应用。两侧的密钥均已隐藏。'],
    ['rd.etag', 'Read at its config\'s ETag {etag}.', '读取时其配置的 ETag 为 {etag}。'],
    ['cp.a', 'First node', '第一个节点'], ['cp.b', 'Second node', '第二个节点'], ['cp.run', 'Compare', '对比'],
    ['cp.only', 'Differences only', '只看差异'], ['cp.whole', 'Whole configs side by side', '整个配置并排对比'],
    ['cp.same', 'Same', '相同'], ['cp.differs', 'Differs', '不同'], ['cp.only_a', 'Only in {name}', '仅 {name} 有'], ['cp.only_b', 'Only in {name}', '仅 {name} 有'],
    ['cp.pick', 'Pick two nodes.', '请选择两个节点。'], ['cp.allsame', 'Every part is the same.', '所有部分都相同。'],
    ['im.title', 'Import a template from a node', '从节点导入模板'], ['im.node', 'Node', '节点'], ['im.read', 'Read its config', '读取其配置'],
    ['im.pick', 'Pick the parts the template owns. Unnamed elements of lists cannot be owned: name them on the member first.',
      '选择模板拥有的部分。列表中未命名的元素不能被拥有：请先在成员上为其命名。'],
    ['im.create', 'Create the template', '创建模板'], ['im.none', 'Pick at least one part.', '请至少选择一个部分。'],
    ['im.secret', 'holds secrets, kept by the manager', '含密钥，由管理端保存'],
    ['nd.a.template.add', 'template added', '添加模板'], ['nd.a.template.update', 'template changed', '修改模板'], ['nd.a.template.remove', 'template removed', '删除模板']
  );

  var SYNC_KIND = { in_sync: 'ok', out_of_sync: 'warn', conflict: 'danger', error: 'danger', unmanaged: '', pending: 'info' };
  var FORMATS = ['conf', 'yaml', 'json'];
  var LISTS = ['listeners', 'routes', 'services', 'upstreams'];

  function tplPath(name) { return '/api/templates/' + encodeURIComponent(name); }
  function templates(UI) {
    return FC.call(UI, 'GET', '/api/templates').then(function (r) {
      var b = r.body || {};
      return { etag: r.etag, templates: b.templates || [], max: b.max_templates || 0, readOnly: b.read_only || '' };
    });
  }
  function syncBadge(UI, s) { return UI.badge(UI.t('sy.' + (SYNC_KIND.hasOwnProperty(s) ? s : 'error')), SYNC_KIND[s] || '', true); }
  function pretty(v) { return v === undefined || v === null ? '' : JSON.stringify(v, null, 2); }
  function clone(v) { return v === undefined ? undefined : JSON.parse(JSON.stringify(v)); }

  // ---- A side-by-side diff of two texts, by lines / 两段文本按行并排对比 ----
  // diffRows pairs the lines of a and b by their longest common subsequence: [aLine, bLine, kind], kind '' (the same),
  // 'del' (in a only), 'add' (in b only) or 'chg' (changed). Past 3000 lines it pairs them by index.
  // diffRows 按最长公共子序列配对 a 与 b 的行：[a 行, b 行, 类型]；类型 ''（相同）、'del'（仅 a）、'add'（仅 b）或 'chg'。
  function diffRows(a, b) {
    var x = a ? a.split('\n') : [], y = b ? b.split('\n') : [], rows = [], i, j;
    if (x.length * y.length > 9e6) {
      for (i = 0; i < Math.max(x.length, y.length); i++) rows.push([x[i], y[i], x[i] === y[i] ? '' : 'chg']);
      return rows;
    }
    var n = x.length, m = y.length, L = [];
    for (i = 0; i <= n; i++) { L.push(new Int32Array(m + 1)); }
    for (i = n - 1; i >= 0; i--) for (j = m - 1; j >= 0; j--) L[i][j] = x[i] === y[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1]);
    i = 0; j = 0;
    var dels = [], adds = [];
    function flush() {
      var k;
      for (k = 0; k < Math.max(dels.length, adds.length); k++) {
        rows.push([dels[k], adds[k], dels[k] === undefined ? 'add' : adds[k] === undefined ? 'del' : 'chg']);
      }
      dels = []; adds = [];
    }
    while (i < n && j < m) {
      if (x[i] === y[j]) { flush(); rows.push([x[i], y[j], '']); i++; j++; } else if (L[i + 1][j] >= L[i][j + 1]) dels.push(x[i++]); else adds.push(y[j++]);
    }
    while (i < n) dels.push(x[i++]);
    while (j < m) adds.push(y[j++]);
    flush();
    return rows;
  }
  // sideBySide draws two texts side by side, their differing lines marked. / 并排显示两段文本，标出不同的行。
  function sideBySide(UI, aTitle, a, bTitle, b) {
    var h = UI.h;
    var rows = diffRows(a, b);
    return h('div.tp-diff', h('table',
      h('thead', h('tr', h('th', aTitle), h('th', bTitle))),
      h('tbody', rows.map(function (r) {
        return h('tr' + (r[2] ? '.tp-' + r[2] : ''), h('td', h('pre', r[0] === undefined ? '' : r[0])), h('td', h('pre', r[1] === undefined ? '' : r[1])));
      }))));
  }

  // ---- The schema, for the form / 表单所用的 schema ----
  var schemaP = null;
  function schema() {
    if (!schemaP) {
      schemaP = UI.scope('admin').api('GET', '/api/schema');
      schemaP.catch(function () { schemaP = null; });
    }
    return schemaP;
  }
  function docOf(text) { return UI.zh() && window.FitSchemaZh && window.FitSchemaZh[text] ? window.FitSchemaZh[text] : (text || ''); }

  // form is a schema-driven form over model, a template's config: a card per top-level member, an element of the named
  // lists a box each, fields set shown with a control each and the others offered in "Add a field…"; what has no plain
  // control (a handler's module, a map, a list of objects below the top) is edited as JSON. onChange is told of every
  // edit, redraw redraws the form after a change of shape.
  // form 是基于 schema 的模板配置表单：每个顶层成员一张卡片，命名列表的每个元素一个框；已设置的字段各有控件，其余字段在
  // “添加字段…”中提供；没有普通控件的值（处理器模块、映射、顶层以下的对象列表）以 JSON 编辑。onChange 接收每次修改，
  // 结构变化后由 redraw 重绘表单。
  function form(UI, S, getModel, onChange, redraw) {
    var h = UI.h, t = UI.t;
    function resolve(s) {
      var n = 0;
      while (s && s.$ref && n++ < 32) {
        var name = decodeURIComponent(s.$ref.replace(/^#\/\$defs\//, '')).replace(/~1/g, '/').replace(/~0/g, '~');
        var def = S.$defs[name] || {}, o = {};
        Object.keys(def).forEach(function (k) { o[k] = def[k]; });
        o.description = s.description || def.description;
        s = o;
      }
      return s || {};
    }
    function kind(s) {
      if (s.type === 'object' && s.properties && s.properties.module && s.properties.params) return 'json';
      if (s.oneOf) return 'string';
      if (s.type === 'array') {
        var it = resolve(s.items || {});
        return ['string', 'integer', 'number'].indexOf(it.type) >= 0 && !it.enum ? 'lines' : 'json';
      }
      if (s.type === 'object' && s.properties) return 'object';
      if (['string', 'integer', 'number', 'boolean'].indexOf(s.type) >= 0) return s.type;
      return 'json';
    }
    function emptyOf(s) {
      var k = kind(s);
      return k === 'object' ? {} : k === 'boolean' ? true : k === 'integer' || k === 'number' ? 0 : k === 'lines' ? [] : k === 'json' ? (s.type === 'array' ? [] : {}) : '';
    }
    function control(s, get, set) {
      var v = get(), k = kind(s);
      if (k === 'object') return objectForm(s, get, set);
      if (k === 'boolean') {
        var c = h('input', { type: 'checkbox', checked: v === true });
        c.addEventListener('change', function () { set(c.checked); onChange(); });
        return c;
      }
      if (s.enum) {
        var sel = h('select.select.select--sm', s.enum.map(function (e) { return h('option', { value: String(e), selected: v === e }, String(e)); }));
        sel.addEventListener('change', function () { set(s.enum.filter(function (e) { return String(e) === sel.value; })[0]); onChange(); });
        return sel;
      }
      if (k === 'lines') {
        var ta = h('textarea.textarea.cl-mono', { rows: Math.max(2, (v || []).length), spellcheck: 'false' });
        ta.value = (v || []).join('\n');
        ta.addEventListener('input', function () {
          var it = resolve(s.items || {}), num = it.type === 'integer' || it.type === 'number';
          set(ta.value.split('\n').map(function (x) { return x.trim(); }).filter(Boolean).map(function (x) { return num && /^-?\d+(\.\d+)?$/.test(x) ? Number(x) : x; }));
          onChange();
        });
        return ta;
      }
      if (k === 'json') {
        var j = h('textarea.textarea.textarea--code', { rows: 4, spellcheck: 'false' });
        j.value = pretty(v);
        j.addEventListener('input', function () {
          try { set(JSON.parse(j.value)); j.setCustomValidity(''); onChange(); } catch (e) { j.setCustomValidity(t('tp.json.bad', { e: e.message })); j.reportValidity(); }
        });
        return j;
      }
      var numeric = k === 'integer' || k === 'number';
      var input = h('input.input.input--sm' + (numeric ? '' : '.cl-mono'), { type: numeric ? 'number' : 'text', value: v === undefined ? '' : String(v), spellcheck: 'false' });
      input.addEventListener('input', function () {
        set(numeric ? (input.value === '' ? 0 : Number(input.value)) : input.value);
        onChange();
      });
      return input;
    }
    function objectForm(s, get, set) {
      var v = get();
      if (!v || typeof v !== 'object' || Array.isArray(v)) { v = {}; set(v); }
      var props = s.properties || {}, box = h('div.tp-obj');
      Object.keys(v).forEach(function (name) {
        var p = resolve(props[name] || {});
        var row = h('div.tp-field', h('div.tp-fname', h('code', name),
          h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: t('tp.leaveout'), 'aria-label': t('tp.leaveout') + ' ' + name,
            onclick: function () { delete v[name]; onChange(); redraw(); } }, UI.icon('x'))),
        h('div.tp-fctl', props[name] ? control(p, function () { return v[name]; }, function (x) { v[name] = x; }) :
          control({}, function () { return v[name]; }, function (x) { v[name] = x; })),
        p.description ? h('div.tp-fdoc.small.muted', docOf(p.description)) : null);
        box.appendChild(row);
      });
      var rest = Object.keys(props).filter(function (k) { return !(k in v) && !resolve(props[k]).deprecated; });
      if (rest.length) {
        var add = h('select.select.select--sm.tp-add', h('option', { value: '' }, t('tp.addfield')), rest.map(function (k) { return h('option', { value: k }, k); }));
        add.addEventListener('change', function () {
          if (!add.value) return;
          v[add.value] = emptyOf(resolve(props[add.value]));
          onChange(); redraw();
        });
        box.appendChild(add);
      }
      return box;
    }
    return function draw() {
      var model = getModel(), root = S.properties || {}, out = h('div.stack');
      Object.keys(model).forEach(function (member) {
        var s = resolve(root[member] || {});
        var rm = UI.btn(t('tp.leaveout'), 'x', function () { delete model[member]; onChange(); redraw(); }, 'ghost sm');
        var body;
        if (LISTS.indexOf(member) >= 0 && Array.isArray(model[member])) {
          var items = resolve(s.items || {});
          body = h('div.stack', model[member].map(function (el, i) {
            var d = h('details.tp-el', { open: true }, h('summary', h('b', el && el.name ? el.name : '#' + i),
              h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', 'aria-label': t('tp.leaveout'),
                onclick: function (ev) { ev.preventDefault(); model[member].splice(i, 1); onChange(); redraw(); } }, UI.icon('trash'))),
            objectForm(items, function () { return model[member][i]; }, function (x) { model[member][i] = x; }));
            return d;
          }), UI.btn(t('tp.addel', { what: member }), 'plus', function () { model[member].push({ name: '' }); onChange(); redraw(); }, 'ghost sm'));
        } else {
          body = control(s, function () { return model[member]; }, function (x) { model[member] = x; });
        }
        out.appendChild(UI.card(member, h('div.stack', s.description ? h('div.small.muted', docOf(s.description)) : null, body), rm));
      });
      var free = Object.keys(root).filter(function (k) { return k !== 'admin' && k !== 'schema_version' && !(k in model); });
      var add = h('select.select.select--sm', h('option', { value: '' }, t('tp.addpart')), free.map(function (k) { return h('option', { value: k }, k); }));
      add.addEventListener('change', function () {
        if (!add.value) return;
        var s = resolve(root[add.value] || {});
        model[add.value] = LISTS.indexOf(add.value) >= 0 ? [{ name: '' }] : emptyOf(s);
        onChange(); redraw();
      });
      out.appendChild(h('div', add));
      return out;
    };
  }

  // ---- The list / 列表 ----
  FC.pages.templates = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('templates', t('n.templates'));
    var head = h('div.row.row--between.cl-bar'), body = h('div');
    UI.mount(el, h('div.stack', head, body));
    UI.mount(body, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    var data = null, syncs = {};
    var readBtn = UI.btn(t('tp.read'), 'refresh', function () {
      UI.busy(readBtn, FC.call(UI, 'POST', '/api/sync', {}).then(function (r) {
        ((r.body && r.body.nodes) || []).forEach(function (s) { syncs[s.node] = s; });
        draw();
      }, function (e) { UI.toast(e.message, 'error'); }));
    }, 'ghost sm');
    readBtn.title = t('tp.read.note');
    UI.mount(sh.tools, readBtn);
    function remove(tp) {
      UI.confirm(t('tp.rm.ask', { name: tp.name }), t('rm.ok'), true).then(function (yes) {
        if (!yes) return;
        FC.call(UI, 'DELETE', tplPath(tp.name), undefined, data.etag).then(function () {
          UI.toast(t('tp.rm.done', { name: tp.name }));
          read();
        }, function (e) { UI.toast(e.status === 412 ? t('fm.stale') : e.message, 'error'); read(); });
      });
    }
    function counts(names) {
      var c = {};
      names.forEach(function (n) { var s = syncs[n]; var k = s ? s.state : 'pending'; c[k] = (c[k] || 0) + 1; });
      return h('span.btn-row', UI.badge(t('gr.n', { n: names.length }), 'info'),
        Object.keys(c).map(function (k) { return k === 'pending' ? UI.badge(c[k] + ' ' + t('tp.notread').split(':')[0], '') : UI.badge(c[k] + ' ' + t('sy.' + k), SYNC_KIND[k] || '', true); }));
    }
    function draw() {
      if (!data || !document.body.contains(el)) return;
      var addBtn = h('a.btn.btn--primary', { href: '#/cluster/newtemplate' }, UI.icon('plus'), t('tp.add'));
      if (data.readOnly || data.templates.length >= data.max) {
        addBtn = UI.btn(t('tp.add'), 'plus', null, 'primary');
        addBtn.disabled = true;
      }
      var imp = UI.btn(t('tp.import'), 'download', function () { importDialog(UI); }, 'ghost');
      imp.disabled = !!data.readOnly;
      // Templates are changed by a cluster-wide admin only (I148). / 只有集群范围的管理员才能修改模板（I148）。
      var admin = FC.canCluster('admin');
      if (!admin) addBtn = imp = null;
      UI.mount(head, h('span.btn-row', addBtn, imp, h('a.btn.btn--ghost', { href: '#/cluster/compare' }, UI.icon('swap'), t('tp.compare'))),
        h('span.small.muted', t('tp.count', { n: data.templates.length, max: data.max })));
      var parts = [];
      if (data.readOnly) parts.push(h('div.callout.callout--warn', t('readonly', { why: data.readOnly })));
      if (!data.templates.length) parts.push(UI.empty(t('tp.none'), 'layers'));
      else parts.push(UI.card(null, UI.table([
        { label: t('tp.name'), render: function (tp) {
          return h('div', h('a', { href: '#/cluster/template/' + encodeURIComponent(tp.name) }, h('b', tp.name)), tp.description ? h('div.small.muted', tp.description) : null);
        } },
        { label: t('tp.groups'), render: function (tp) {
          return h('div', tp.groups.length ? h('span.small', tp.groups.join(', ')) : h('span.muted', '—'),
            tp.missing_groups.length ? h('div.small.cl-warn', t('tp.missing', { names: tp.missing_groups.join(', ') })) : null);
        } },
        { label: t('tp.order'), cls: 'num', render: function (tp) { return String(tp.order || 0); } },
        { label: t('tp.parts'), render: function (tp) { return h('div.cl-chips', tp.parts.map(function (p) { return h('span.cl-chip', p.path); })); } },
        { label: t('tp.members'), render: function (tp) { return counts(tp.nodes); } },
        { label: '', cls: 'actions', render: function (tp) {
          if (!admin) return null;
          var rb = UI.btn(t('delete'), 'trash', function () { remove(tp); }, 'ghost sm');
          rb.title = t('delete') + ' ' + tp.name;
          rb.disabled = !!data.readOnly;
          return rb;
        } }
      ], data.templates, { onRowClick: function (tp) { location.hash = '#/cluster/template/' + encodeURIComponent(tp.name); } }), null, true));
      UI.mount(body, h('div.stack', parts));
    }
    function read() {
      return Promise.all([templates(UI), FC.call(UI, 'GET', '/api/sync')]).then(function (r) {
        data = r[0];
        ((r[1].body && r[1].body.nodes) || []).forEach(function (s) { syncs[s.node] = s; });
        draw();
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(body, h('div.callout.callout--danger', e.message));
      });
    }
    read();
  };

  // ---- A member's sync, part by part / 成员的同步状态，逐部分 ----
  function partsTable(UI, ms, only) {
    var h = UI.h, t = UI.t;
    var parts = (ms.parts || []).filter(function (p) { return !only || only.indexOf(p.path) >= 0; });
    return h('div.stack', parts.map(function (p) {
      var head = h('summary', h('code', p.path), ' ', syncBadge(UI, p.state), p.reason ? h('span.small.muted', ' ' + t('sy.r.' + p.reason)) : null,
        h('span.small.muted', ' · ' + p.template), p.overrides && p.overrides.length ? h('span.small.cl-warn', ' · ' + t('sy.overrides', { names: p.overrides.join(', ') })) : null);
      return h('details.tp-el', { open: p.state !== 'in_sync' }, head,
        sideBySide(UI, t('tp.have'), p.have === undefined || p.have === null ? t('tp.absent') : pretty(p.have), t('tp.want'), pretty(p.want)));
    }), ms.released && ms.released.length ? h('div.small.muted', t('sy.released', { parts: ms.released.join(', ') })) : null);
  }
  function managedNote(UI, ms) {
    var t = UI.t;
    if (!ms.managed) return null;
    if (ms.managed === 'failed') return UI.badge(t('sy.failed', { e: ms.managed_error || '' }), 'danger');
    return UI.badge(t('sy.' + ms.managed), ms.managed === 'unsupported' ? 'warn' : '');
  }
  function syncSummary(UI, ms) {
    var h = UI.h, t = UI.t;
    return h('div.stack',
      h('div.btn-row', syncBadge(UI, ms.state), ms.counts ? h('span.small.muted', t('sy.counts', { s: ms.counts.in_sync, o: ms.counts.out_of_sync, c: ms.counts.conflict })) : null, managedNote(UI, ms)),
      ms.error ? FC.problemBox(UI, ms.error) : null,
      (ms.errors || []).length ? h('div.callout.callout--danger', h('ul', ms.errors.map(function (e) { return h('li', e); }))) : null,
      ms.note ? h('div.callout.callout--warn.small', ms.note) : null);
  }

  // ---- A template's page / 模板页 ----
  FC.pages.template = function (UI, sh, params) {
    var h = UI.h, t = UI.t, name = params ? params.name : '', isNew = !name;
    var el = sh.page('templates', isNew ? t('tp.add') : name, t('m.cluster') + ' / ' + t('n.templates') + (isNew ? '' : ' / ' + name));
    var top = h('div.stack'), sync = h('div.stack');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/templates' }, '← ' + t('tp.back'))), top, sync));
    UI.mount(top, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    var etag = '', tp = null, allGroups = [], S = null, model = {}, tab = 'ui', raw = { text: '', error: '', timer: null }, dirty = false;
    var fields = {};

    function fieldErr(key, msg) { Object.keys(fields).forEach(function (k) { fields[k].hidden = true; }); if (fields[key]) { fields[key].textContent = msg; fields[key].hidden = false; } }
    function field(key, label, input, hint) {
      var err = h('span.field__hint.cl-err', { hidden: true });
      fields[key] = err;
      var f = UI.field(label, input, hint);
      f.appendChild(err);
      return f;
    }
    function draw() {
      if (!document.body.contains(el)) return;
      var nameIn = h('input.input', { name: 'template-name', value: tp ? tp.name : '', autocomplete: 'off', spellcheck: 'false', placeholder: 'edge-routes' });
      var descIn = h('input.input', { name: 'template-description', value: tp ? tp.description || '' : '', autocomplete: 'off' });
      var orderIn = h('input.input.input--sm', { name: 'template-order', type: 'number', min: -1000, max: 1000, step: 1, value: tp ? tp.order || 0 : 0 });
      var picked = (tp ? tp.groups : []).slice();
      var groupsBox = h('div.cl-chips', allGroups.map(function (g) {
        var c = h('input', { type: 'checkbox', checked: picked.indexOf(g.name) >= 0 });
        c.addEventListener('change', function () {
          var i = picked.indexOf(g.name);
          if (c.checked && i < 0) picked.push(g.name); else if (!c.checked && i >= 0) picked.splice(i, 1);
          dirty = true;
        });
        return h('label.cl-chip.tp-pick', c, ' ', g.name, ' ', h('span.small.muted', '(' + (g.nodes || []).length + ')'));
      }), (tp ? tp.missing_groups : []).map(function (g) { return h('span.cl-chip.cl-warn', g); }));
      [nameIn, descIn, orderIn].forEach(function (x) { x.addEventListener('input', function () { dirty = true; }); });

      var cfgBox = h('div');
      var tabs = h('div');
      function drawTabs() {
        UI.mount(tabs, UI.tabs([['ui', t('tp.tab.ui')]].concat(FORMATS.map(function (f) { return [f, '.' + f]; })), tab, function (f) {
          if (f === tab) return;
          if (tab !== 'ui' && raw.error) { UI.toast(t('tp.raw.bad'), 'error'); return; }
          tab = f;
          drawTabs();
          drawConfig();
        }));
      }
      function drawConfig() {
        if (tab === 'ui') {
          if (!S) { UI.mount(cfgBox, h('div.row', h('span.spinner'))); return; }
          var f = form(UI, S, function () { return model; }, function () { dirty = true; }, drawConfig);
          UI.mount(cfgBox, f());
          return;
        }
        var ta = h('textarea.textarea.textarea--code.tp-raw', { rows: 22, spellcheck: 'false', 'aria-label': '.' + tab });
        var note = h('div.small.muted', t('tp.raw.note', { fmt: '.' + tab })), err = h('div.callout.callout--danger', { hidden: true });
        UI.mount(cfgBox, h('div.stack', note, ta, err));
        raw.error = '';
        UI.raw('POST', '/api/text/render?format=' + tab, model).then(function (r) {
          return r.text().then(function (x) {
            if (!r.ok) throw new Error(x);
            ta.value = raw.text = x;
          });
        }).catch(function (e) { err.textContent = e.message; err.hidden = false; });
        ta.addEventListener('input', function () {
          dirty = true;
          if (raw.timer) clearTimeout(raw.timer);
          raw.error = 'pending';
          var fmt = tab;
          raw.timer = setTimeout(function () {
            raw.timer = null;
            UI.raw('POST', '/api/text/parse?format=' + fmt, ta.value, { 'Content-Type': 'text/plain' }).then(function (r) {
              return r.text().then(function (x) {
                var j = null;
                try { j = JSON.parse(x); } catch (e) { j = null; }
                if (!r.ok) throw new Error(j && j.error ? j.error : x);
                model = j || {};
                raw.error = '';
                err.hidden = true;
              });
            }).catch(function (e) { raw.error = e.message; err.textContent = t('tp.raw.bad') + ' ' + e.message; err.hidden = false; });
          }, 400);
        });
      }
      drawTabs();
      drawConfig();

      var saveBtn = UI.btn(t('tp.save'), 'save', function () {
        if (raw.error) { UI.toast(t('tp.raw.bad'), 'error'); return; }
        var body = { name: nameIn.value.trim(), description: descIn.value.trim(), order: Number(orderIn.value) || 0, groups: picked, config: model };
        var p = isNew ? FC.call(UI, 'POST', '/api/templates', body, etag) : FC.call(UI, 'PUT', tplPath(name), body, etag);
        UI.busy(saveBtn, p.then(function (r) {
          dirty = false;
          UI.toast(t(isNew ? 'tp.added' : 'tp.saved', { name: r.body.name }));
          if (isNew || r.body.name !== name) location.hash = '#/cluster/template/' + encodeURIComponent(r.body.name);
          else read();
        }, function (e) {
          if (e.status === 412) { UI.toast(t('fm.stale'), 'error'); return; }
          fieldErr(e.field || 'config', e.message);
          UI.toast(e.message, 'error');
        }));
      }, 'primary');
      saveBtn.disabled = !!(tp && tp.read_only);

      var info = [];
      if (tp) {
        info.push(UI.card(t('tp.parts'), h('div.cl-chips', tp.parts.length ? tp.parts.map(function (p) { return h('span.cl-chip', p.path); }) : h('span.muted', '—'))));
        var vars = (tp.variables || []).map(function (v) {
          var from;
          if (v.indexOf('node.') === 0) from = t('tp.var.node');
          else {
            var gs = tp.groups.filter(function (g) { return allGroups.some(function (x) { return x.name === g && x.variables && v in x.variables; }); });
            from = gs.length ? t('tp.var.from', { groups: gs.join(', ') }) : t('tp.var.none');
          }
          return h('li', h('code', '${' + v + '}'), ' ', h('span.small' + (from === t('tp.var.none') ? '.cl-warn' : '.muted'), from));
        });
        info.push(UI.card(t('tp.vars'), vars.length ? h('ul.tp-list', vars) : h('span.muted', '—')));
        info.push(UI.card(t('tp.warnings'), tp.warnings.length ? h('ul.tp-list', tp.warnings.map(function (w) { return h('li.small', w); })) : h('span.muted', t('tp.nowarn'))));
      }
      UI.mount(top, h('div.stack',
        UI.card(isNew ? t('tp.add') : t('edit') + ' ' + name, h('div.stack',
          h('div.tp-grid', field('name', t('tp.name'), nameIn, t('gr.name.hint')), field('description', t('tp.desc'), descIn),
            field('order', t('tp.order'), orderIn, t('tp.order.hint'))),
          field('groups', t('tp.groups'), groupsBox, t('tp.groups.hint')),
          h('div.field', h('span.field__label', t('tp.config')), h('span.field__hint', t('tp.config.hint')), tabs, cfgBox, fields.config = h('span.field__hint.cl-err', { hidden: true })),
          FC.canCluster('admin') ? h('div.btn-row', saveBtn) : null)),
        info.length ? h('div.tp-info', info) : null));
    }

    function drawSync(res) {
      if (!tp) { UI.mount(sync, null); return; }
      var only = tp.parts.map(function (p) { return p.path; });
      var readBtn = UI.btn(t('tp.read'), 'refresh', function () {
        UI.busy(readBtn, FC.call(UI, 'POST', '/api/sync', { template: name }).then(function (r) { drawSync(r.body); }, function (e) { UI.toast(e.message, 'error'); }));
      }, 'sm');
      var rows;
      if (!res) rows = UI.empty(t('tp.notread'), 'refresh');
      else {
        rows = h('div.stack', (res.nodes || []).map(function (ms) {
          return h('section.card', h('div.card__head', h('h2', h('a', { href: '#/cluster/node/' + encodeURIComponent(ms.node) }, ms.node)),
            h('a.btn.btn--ghost.btn--sm', { href: '#/cluster/rendered/' + encodeURIComponent(ms.node) }, UI.icon('code'), t('tp.rendered'))),
          h('div.card__body.stack', syncSummary(UI, ms), partsTable(UI, ms, only)));
        }), (res.pending || []).length ? h('div.callout.callout--warn', t('tp.pending') + ' ' + res.pending.join(', ')) : null);
      }
      // Roll out the template's change (I146). / 发布模板的修改（I146）。
      // A template's rollout is an operator's, on each of its members (I148). / 模板的发布需要在其每个成员上是操作员（I148）。
      var roll = FC.canAll('operator', tp.nodes) ? h('a.btn.btn--primary.btn--sm', { href: '#/cluster/newrollout?kind=template&template=' + encodeURIComponent(name) }, UI.icon('send'), t('ro.rollout')) : null;
      UI.mount(sync, UI.card(t('tp.sync'), h('div.stack', h('div.small.muted', t('tp.read.note')), rows), h('span.btn-row', readBtn, roll)));
    }

    function read() {
      var reqs = [FC.call(UI, 'GET', '/api/groups'), schema().catch(function () { return null; })];
      if (!isNew) reqs.push(FC.call(UI, 'GET', tplPath(name)));
      else reqs.push(templates(UI));
      return Promise.all(reqs).then(function (r) {
        allGroups = (r[0].body && r[0].body.groups) || [];
        S = r[1];
        if (isNew) { etag = r[2].etag; tp = null; model = {}; } else { etag = r[2].etag; tp = r[2].body; model = clone(tp.config) || {}; }
        raw = { text: '', error: '', timer: null };
        draw();
        drawSync(null);
        if (!isNew && tp.nodes.length) FC.call(UI, 'POST', '/api/sync', { template: name }).then(function (x) { drawSync(x.body); }, function () { /* the button reads it / 由按钮读取 */ });
      }, function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(top, h('div.callout.callout--danger', e.status === 404 ? t('tp.notfound', { name: name }) : e.message));
      });
    }
    read();
  };

  // ---- A member's rendered result / 成员的渲染结果 ----
  FC.pages.rendered = function (UI, sh, params) {
    var h = UI.h, t = UI.t, name = params.name;
    var el = sh.page('templates', t('rd.title', { name: name }), t('m.cluster') + ' / ' + t('n.templates') + ' / ' + name);
    var box = h('div.stack');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/templates' }, '← ' + t('tp.back'))), box));
    function read() {
      UI.mount(box, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
      FC.call(UI, 'GET', '/api/desired/' + encodeURIComponent(name)).then(function (r) {
        var d = r.body || {};
        UI.mount(box, h('div.stack', syncSummary(UI, d), h('div.small.muted', t('rd.note')),
          d.config_etag ? h('div.small.muted', t('rd.etag', { etag: d.config_etag })) : null,
          (d.parts || []).length ? UI.card(t('tp.parts'), partsTable(UI, d, null)) : null,
          d.config ? UI.card(t('tp.rendered'), sideBySide(UI, t('rd.current'), pretty(d.config), t('rd.desired'), pretty(d.desired)), null, true) : null));
      }, function (e) { UI.mount(box, h('div.callout.callout--danger', e.message)); });
    }
    UI.mount(sh.tools, FC.canOn('operator', name) ? h('a.btn.btn--ghost.btn--sm', { href: '#/cluster/newrollout?kind=sync&nodes=' + encodeURIComponent(name) }, UI.icon('send'), t('ro.syncthis')) : null,
      UI.btn(t('refresh'), 'refresh', read, 'ghost sm'));
    read();
  };

  // ---- Compare / 对比 ----
  FC.pages.compare = function (UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('templates', t('n.compare'), t('m.cluster') + ' / ' + t('n.templates') + ' / ' + t('n.compare'));
    var bar = h('div.row.cl-filter'), box = h('div.stack');
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/templates' }, '← ' + t('tp.back'))), bar, box));
    FC.members(UI).then(function (ms) {
      var names = ms.nodes.map(function (n) { return n.name; }).sort();
      function sel(label, v) {
        return h('select.select', { 'aria-label': label }, h('option', { value: '' }, label), names.map(function (n) { return h('option', { value: n, selected: n === v }, n); }));
      }
      var a = sel(t('cp.a'), names[0]), b = sel(t('cp.b'), names[1]);
      var only = h('input', { type: 'checkbox', checked: true });
      var last = null;
      var run = UI.btn(t('cp.run'), 'swap', go, 'primary');
      only.addEventListener('change', function () { if (last) draw(last); });
      UI.mount(bar, a, b, run, h('label.row.cl-check', only, h('span', t('cp.only'))));
      function go() {
        if (!a.value || !b.value || a.value === b.value) { UI.toast(t('cp.pick'), 'error'); return; }
        UI.busy(run, FC.call(UI, 'POST', '/api/compare', { a: a.value, b: b.value }).then(function (r) { last = r.body; draw(last); },
          function (e) { UI.mount(box, e.problem ? FC.problemBox(UI, e.problem) : h('div.callout.callout--danger', e.message)); }));
      }
      function draw(res) {
        var an = res.a.node, bn = res.b.node;
        var parts = res.parts.filter(function (p) { return !only.checked || p.state !== 'same'; });
        var kinds = { same: 'ok', differs: 'warn', only_a: 'info', only_b: 'info' };
        UI.mount(box, h('div.stack',
          parts.length ? h('div.stack', parts.map(function (p) {
            var label = p.state === 'only_a' ? t('cp.only_a', { name: an }) : p.state === 'only_b' ? t('cp.only_b', { name: bn }) : t('cp.' + p.state);
            return h('details.tp-el', { open: p.state === 'differs' }, h('summary', h('code', p.path), ' ', UI.badge(label, kinds[p.state] || '', true)),
              sideBySide(UI, an, p.a === undefined ? t('tp.absent') : pretty(p.a), bn, p.b === undefined ? (p.state === 'same' ? pretty(p.a) : t('tp.absent')) : pretty(p.b)));
          })) : UI.empty(t('cp.allsame'), 'check'),
          h('details.tp-el', h('summary', t('cp.whole')), sideBySide(UI, an, pretty(res.a.config), bn, pretty(res.b.config)))));
      }
      if (a.value && b.value && a.value !== b.value) go();
    }, function (e) { UI.mount(box, h('div.callout.callout--danger', e.message)); });
  };

  // ---- Import from a node / 从节点导入 ----
  function importDialog(UI) {
    var h = UI.h, t = UI.t;
    var box = h('div.stack'), err = h('div.callout.callout--danger', { hidden: true });
    var nodeSel = h('select.select', { name: 'import-node', 'aria-label': t('im.node') });
    var nameIn = h('input.input', { name: 'import-name', autocomplete: 'off', spellcheck: 'false', placeholder: 'from-edge-1' });
    var groupsBox = h('div.cl-chips'), picked = [], parts = null, node = '';
    Promise.all([FC.members(UI), FC.call(UI, 'GET', '/api/groups')]).then(function (r) {
      UI.mount(nodeSel, r[0].nodes.map(function (n) { return h('option', { value: n.name }, n.name); }));
      UI.mount(groupsBox, ((r[1].body && r[1].body.groups) || []).map(function (g) {
        var c = h('input', { type: 'checkbox' });
        c.addEventListener('change', function () { var i = picked.indexOf(g.name); if (c.checked && i < 0) picked.push(g.name); else if (!c.checked && i >= 0) picked.splice(i, 1); });
        return h('label.cl-chip.tp-pick', c, ' ', g.name);
      }));
    }, UI.fail);
    var readBtn = UI.btn(t('im.read'), 'download', function () {
      node = nodeSel.value;
      err.hidden = true;
      UI.busy(readBtn, FC.call(UI, 'POST', '/api/import', { node: node }).then(function (r) {
        parts = r.body.parts || [];
        if (!nameIn.value) nameIn.value = 'from-' + node;
        UI.mount(box, h('div.small.muted', t('im.pick')), h('div.stack.tp-parts', parts.map(function (p) {
          p.box = h('input', { type: 'checkbox', disabled: !p.ownable, 'aria-label': p.path });
          return h('details.tp-el', h('summary', p.box, ' ', h('code', p.path), p.secret ? h('span.small.cl-warn', ' · ' + t('im.secret')) : null,
            p.why ? h('span.small.muted', ' · ' + p.why) : null), h('pre.tp-pre', pretty(p.value)));
        })));
      }, function (e) { err.textContent = e.message; err.hidden = false; }));
    }, 'sm');
    UI.modal({
      title: t('im.title'), wide: true,
      body: h('div.stack', h('div.row', UI.field(t('im.node'), nodeSel), readBtn), UI.field(t('tp.name'), nameIn, t('gr.name.hint')),
        UI.field(t('tp.groups'), groupsBox, t('tp.groups.hint')), box, err),
      actions: [{ label: t('cancel') }, { label: t('im.create'), kind: 'primary', onclick: function () {
        var paths = (parts || []).filter(function (p) { return p.box && p.box.checked; }).map(function (p) { return p.path; });
        if (!paths.length) { err.textContent = t('im.none'); err.hidden = false; return false; }
        return FC.call(UI, 'POST', '/api/templates', { name: nameIn.value.trim(), groups: picked, from: { node: node, paths: paths } }).then(function (r) {
          UI.toast(t('tp.added', { name: r.body.name }));
          location.hash = '#/cluster/template/' + encodeURIComponent(r.body.name);
        }, function (e) { err.textContent = e.message; err.hidden = false; return false; });
      } }]
    });
  }

  FC.importTemplate = importDialog;
  FC.diffRows = diffRows;
  FC.sideBySide = sideBySide;
})();
