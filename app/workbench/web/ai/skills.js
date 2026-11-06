// The assistant's Skills page: every skill (GET /api/skills), the built-in ones and those users added, each with its
// title and what it is for in the workbench's language, the tools it names and whether this user may call them, and
// whether it is enabled. A skill is Markdown with YAML front matter (name, title, title_zh, description,
// description_zh, inputs, tools, model, examples) and the prompt after it: viewed whole (GET /api/skills/{name}), added
// (POST /api/skills), edited (PUT /api/skills/{name}), enabled or disabled (POST .../enable, .../disable), deleted
// (DELETE), imported from .md files and exported as one. A skill refused comes back with the line of its Markdown that is
// wrong, which the editor selects. Only a user whose level is apply changes skills; every user reads them and runs them
// in the chat panel with /name or its picker. A skill's prompt is drawn as Markdown built as DOM nodes, never as HTML.
//
// 助手的技能页：所有技能（GET /api/skills），包括内置的与用户添加的；每个技能以工作台的语言显示标题与用途、它所列的工具及本用户能否
// 调用，以及是否启用。技能是带 YAML front matter（name、title、title_zh、description、description_zh、inputs、tools、model、
// examples）的 Markdown，其后为提示词：可查看全文（GET /api/skills/{name}）、添加（POST /api/skills）、编辑
// （PUT /api/skills/{name}）、启用或停用（POST .../enable、.../disable）、删除（DELETE），可从 .md 文件导入、导出为 .md。被拒绝的
// 技能会带回 Markdown 中出错的行号，编辑器会选中该行。只有级别为 apply 的用户可以修改技能；所有用户都可以查看，并在对话面板中用
// /name 或选择器运行。技能的提示词以 DOM 节点构建的 Markdown 绘制，从不作为 HTML 插入。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['n.skills', 'Skills', '技能'],
    ['sk.note', 'A skill is a named task: when to use it, a prompt that says how, and the tools it may call, never more than your level allows. In the chat, /name runs one; without a name the assistant picks one from the enabled skills\' descriptions and loads it.',
      '技能是一个命名的任务：何时使用、说明做法的提示词，以及它可调用的工具（永远不超过你的级别）。在对话中用 /name 运行某个技能；不指定时，助手会根据已启用技能的描述选出一个并加载。'],
    ['sk.change', 'A user whose level is {level} adds, edits, enables and disables skills; yours is {mine}.', '级别为 {level} 的用户才能添加、编辑、启用与停用技能；你的级别为 {mine}。'],
    ['sk.kept', 'Skills you add are kept in the store, encrypted; at most {max}.', '你添加的技能加密保存在 store 中；最多 {max} 个。'],
    ['sk.notKept', 'Without admin.store, skills you add last until fit restarts; at most {max}.', '未配置 admin.store：你添加的技能只保留到 fit 重启；最多 {max} 个。'],
    ['sk.skill', 'Skill', '技能'], ['sk.what', 'What it is for', '用途'], ['sk.tools', 'Tools', '工具'], ['sk.on', 'Enabled', '启用'],
    ['sk.builtin', 'built in', '内置'], ['sk.added', 'added', '已添加'], ['sk.by', 'by {user}', '由 {user}'],
    ['sk.add', 'Add skill', '添加技能'], ['sk.import', 'Import', '导入'], ['sk.export', 'Export', '导出'], ['sk.view', 'View', '查看'],
    ['sk.edit', 'Edit', '编辑'], ['sk.delete', 'Delete', '删除'], ['sk.deleteQ', 'Delete the skill {name}? Conversations running it go on without it.', '删除技能 {name}？正在运行它的对话将不再使用它。'],
    ['sk.use', 'Use in chat', '在对话中使用'], ['sk.try', 'Try', '试用'], ['sk.save', 'Save', '保存'],
    ['sk.newTitle', 'Add a skill', '添加技能'], ['sk.editTitle', 'Edit {name}', '编辑 {name}'],
    ['sk.format', 'Markdown: front matter between two lines ---, then the prompt. The keys are name, title, title_zh, description, description_zh, inputs (each a name, a description and required), tools, model (provider/model) and examples.',
      'Markdown：两行 --- 之间为 front matter，其后为提示词。键为 name、title、title_zh、description、description_zh、inputs（每项有 name、description 与 required）、tools、model（provider/model）与 examples。'],
    ['sk.prompt', 'Prompt', '提示词'], ['sk.source', 'Markdown', 'Markdown'], ['sk.inputs', 'Inputs', '输入'], ['sk.examples', 'Examples', '示例'],
    ['sk.model', 'Model: {model}', '模型：{model}'], ['sk.required', 'required', '必填'], ['sk.none', 'none', '无'],
    ['sk.toolOff', 'not for you: {why}', '你不可用：{why}'], ['sk.noTools', 'no tools: it answers from what it is told', '无工具：只根据所给信息回答'],
    ['sk.imported', '{n} imported', '已导入 {n} 个'], ['sk.importErrs', 'Not imported', '未导入'], ['sk.tooBig', 'larger than {max} bytes', '超过 {max} 字节'],
    ['sk.saved', 'Skill {name} saved', '技能 {name} 已保存'], ['sk.enabled', '{name} enabled', '{name} 已启用'], ['sk.disabled', '{name} disabled', '{name} 已停用'],
    ['why.this fit has no such tool', 'this fit has no such tool', '本 fit 没有此工具']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh }, 'ai');

  // TEMPLATE is a new skill's Markdown. / 新技能的 Markdown 模板。
  var TEMPLATE = '---\nname: my-skill\ntitle: My skill\ntitle_zh: 我的技能\ndescription: When to use it, in one line the assistant picks it by.\n' +
    'description_zh: 何时使用，一句话。\ntools: [config_get, schema_describe, config_validate, config_propose]\ninputs:\n  - name: host\n' +
    '    description: The host name it is about.\nexamples:\n  - An example of a request it is for.\n---\nHow to do the task, step by step, in Markdown.\n';

  // words returns a skill's title and description in the workbench's language. / 以工作台语言返回技能的标题与描述。
  function words(UI, s) {
    var z = UI.zh();
    return { title: (z && s.title_zh) || s.title, description: (z && s.description_zh) || s.description };
  }
  function why(UI, w) { var k = 'why.' + w, x = UI.t(k); return x === k ? w : x; }
  // toolBadges draws the tools a skill names, those this user may not call muted. / 绘制技能所列工具，本用户不可用的置灰。
  function toolBadges(UI, s) {
    var h = UI.h, st = s.tool_state || [];
    if (!st.length) return h('span.small.muted', UI.t('sk.noTools'));
    return h('div.ai-skill__tools', st.map(function (x) {
      return h('code' + (x.ok ? '' : '.ai-skill__off'), { title: x.ok ? '' : UI.t('sk.toolOff', { why: why(UI, x.why) }) }, x.name);
    }));
  }
  // download saves text as a file. / 将文本保存为文件。
  function download(name, text) {
    var a = document.createElement('a'), url = URL.createObjectURL(new Blob([text], { type: 'text/markdown;charset=utf-8' }));
    a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
  }
  // refused builds the Error of a skill the server refused, with its line. / 构建服务端拒绝技能时的 Error（含行号）。
  function send(UI, method, path, source) {
    return UI.raw(method, path, { source: source }).then(function (r) {
      return r.text().then(function (txt) {
        var j = null;
        try { j = JSON.parse(txt); } catch (e) { /* not JSON / 非 JSON */ }
        if (r.ok) return j;
        var err = new Error((j && j.error) || r.status + ' ' + r.statusText);
        err.status = r.status; err.line = j && j.line;
        throw err;
      });
    });
  }

  // skillsPage draws the page into the shell sh. / 在外框 sh 中绘制本页。
  function skillsPage(UI, sh) {
    var h = UI.h, t = UI.t;
    var el = sh.page('skills', t('n.skills'));
    var info = null;
    var file = h('input', { type: 'file', accept: '.md,.markdown,text/markdown', multiple: true, hidden: true, onchange: function () { importFiles(file.files); file.value = ''; } });
    var addBtn = UI.btn(t('sk.add'), 'plus', function () { editor(null); }, 'sm');
    var impBtn = UI.btn(t('sk.import'), 'upload', function () { file.click(); }, 'ghost sm');
    sh.tools.appendChild(file);
    sh.tools.appendChild(impBtn);
    sh.tools.appendChild(addBtn);
    sh.tools.appendChild(UI.btn('', 'refresh', load, 'ghost icon'));

    function load() {
      UI.api('GET', '/api/skills').then(function (b) {
        if (!document.body.contains(el)) return;
        info = b;
        addBtn.hidden = impBtn.hidden = !b.can_change;
        draw();
      }, function (e) { UI.mount(el, h('div.callout.callout--danger', e.message)); });
    }
    function draw() {
      var b = info, can = b.can_change;
      var cols = [
        { label: t('sk.skill'), render: function (s) {
          var w = words(UI, s);
          return h('div', h('b', w.title), h('div.row.small', h('code', '/' + s.name), UI.badge(s.builtin ? t('sk.builtin') : t('sk.added')),
            s.by ? h('span.muted', t('sk.by', { user: s.by })) : null));
        } },
        { label: t('sk.what'), render: function (s) { return h('span', words(UI, s).description); } },
        { label: t('sk.tools'), render: function (s) { return toolBadges(UI, s); } },
        { label: t('sk.on'), render: function (s) {
          var c = h('input', { type: 'checkbox', checked: !!s.enabled, disabled: !can, 'aria-label': t('sk.on'), onchange: function () { toggle(s, c); } });
          return h('label.check', c);
        } },
        { label: '', cls: 'ai-skill__acts', render: function (s) {
          return h('div.btn-row', UI.btn(t('sk.view'), 'book', function () { view(s.name); }, 'ghost sm'),
            !s.builtin && can ? UI.btn(t('sk.edit'), 'edit', function () { edit(s.name); }, 'ghost sm') : null,
            UI.btn(t('sk.export'), 'download', function () { exportOne(s.name); }, 'ghost sm'),
            !s.builtin && can ? UI.btn('', 'trash', function () { remove(s); }, 'ghost icon sm') : null);
        } }
      ];
      UI.mount(el,
        h('div.callout', UI.icon('info'), ' ', t('sk.note')),
        h('div.small.muted', t('sk.change', { level: t('lv.' + b.change_level), mine: t('lv.' + b.level) }), ' ',
          b.kept ? t('sk.kept', { max: b.max }) : t('sk.notKept', { max: b.max })),
        UI.card(t('n.skills'), UI.table(cols, b.skills || [], { onRowClick: function (s) { view(s.name); } }), null, true));
    }
    function toggle(s, c) {
      c.disabled = true;
      UI.api('POST', '/api/skills/' + encodeURIComponent(s.name) + '/' + (c.checked ? 'enable' : 'disable'), {}).then(function () {
        UI.toast(t(c.checked ? 'sk.enabled' : 'sk.disabled', { name: s.name }));
        load();
      }, function (e) { c.checked = !c.checked; c.disabled = false; UI.fail(e); });
    }
    function remove(s) {
      UI.confirm(t('sk.deleteQ', { name: s.name }), t('sk.delete'), true).then(function (ok) {
        if (ok) UI.api('DELETE', '/api/skills/' + encodeURIComponent(s.name)).then(load, UI.fail);
      });
    }
    function get(name) { return UI.api('GET', '/api/skills/' + encodeURIComponent(name)); }
    function exportOne(name) { get(name).then(function (s) { download(s.name + '.md', s.source); }, UI.fail); }
    function edit(name) { get(name).then(editor, UI.fail); }

    // view shows a skill whole: its words, inputs, examples, tools and prompt. / 完整显示技能：说明、输入、示例、工具与提示词。
    function view(name) {
      get(name).then(function (s) {
        var w = words(UI, s);
        var use = function (msg) { if (window.FitAI && FitAI.useSkill) { UI.closeModals(); FitAI.useSkill(s.name, { message: msg }); } };
        var body = h('div.stack',
          h('div.row', h('code', '/' + s.name), UI.badge(s.builtin ? t('sk.builtin') : t('sk.added')),
            UI.badge(s.enabled ? t('fn.on') : t('fn.off'), s.enabled ? 'ok' : '', true), s.model ? h('span.small.muted', t('sk.model', { model: s.model })) : null),
          h('p', w.description),
          UI.field(t('sk.tools'), toolBadges(UI, s)),
          s.inputs && s.inputs.length ? UI.field(t('sk.inputs'), h('ul.small.ai-skill__list', s.inputs.map(function (i) {
            return h('li', h('code', i.name), i.required ? [' ', UI.badge(t('sk.required'), 'warn')] : null, i.description ? ' — ' + i.description : '');
          }))) : null,
          s.examples && s.examples.length ? UI.field(t('sk.examples'), h('ul.small.ai-skill__list', s.examples.map(function (x) {
            return h('li', x, ' ', s.enabled ? h('button.btn.btn--ghost.btn--sm', { type: 'button', onclick: function () { use(x); } }, t('sk.try')) : null);
          }))) : null,
          h('details.ai-fold', { open: true }, h('summary', UI.icon('book'), ' ', t('sk.prompt')), h('div.ai-skill__prompt', FitAI.markdown(UI, s.prompt))),
          h('details.ai-fold', h('summary', UI.icon('code'), ' ', t('sk.source')), h('pre.ai-pre', s.source)));
        var acts = [{ label: t('sk.export'), onclick: function () { download(s.name + '.md', s.source); return false; } }];
        if (s.enabled) acts.push({ label: t('sk.use'), kind: 'primary', onclick: function () { use(''); } });
        acts.push({ label: t('close') });
        UI.modal({ title: w.title, body: body, wide: true, actions: acts });
      }, UI.fail);
    }

    // editor adds a skill (s null) or edits one, the server's refusal shown with its line selected.
    // editor 添加（s 为 null）或编辑技能；服务端拒绝时显示错误并选中出错的行。
    function editor(s) {
      var area = h('textarea.textarea.mono.ai-skill__src', { rows: 22, spellcheck: false, value: s ? s.source : TEMPLATE });
      var err = h('div.callout.callout--danger', { hidden: true });
      UI.modal({
        title: s ? t('sk.editTitle', { name: s.name }) : t('sk.newTitle'), wide: true,
        body: h('div.stack', h('div.small.muted', t('sk.format')), area, err),
        actions: [{ label: t('cancel') }, { label: t('sk.save'), kind: 'primary', onclick: function () {
          err.hidden = true;
          var p = s ? send(UI, 'PUT', '/api/skills/' + encodeURIComponent(s.name), area.value) : send(UI, 'POST', '/api/skills', area.value);
          return p.then(function (v) {
            UI.toast(t('sk.saved', { name: v.name }));
            load();
          }, function (e) {
            err.textContent = e.message;
            err.hidden = false;
            if (e.line) selectLine(area, e.line);
            return false;
          });
        } }]
      });
    }
    function selectLine(area, n) {
      var lines = area.value.split('\n'), start = 0;
      for (var i = 0; i < n - 1 && i < lines.length; i++) start += lines[i].length + 1;
      area.focus();
      area.setSelectionRange(start, start + (lines[n - 1] || '').length);
    }

    // importFiles adds each .md file as a skill, one at a time, and says what was not. / 逐个将 .md 文件添加为技能，并列出未导入的。
    function importFiles(list) {
      var files = Array.prototype.slice.call(list || []), done = 0, errs = [];
      function next() {
        var f = files.shift();
        if (!f) {
          if (done) UI.toast(t('sk.imported', { n: done }));
          if (errs.length) {
            UI.modal({ title: t('sk.importErrs'), body: h('ul.small', errs.map(function (x) { return h('li', h('b', x[0]), ': ', x[1]); })) });
          }
          load();
          return;
        }
        if (f.size > info.max_bytes) { errs.push([f.name, t('sk.tooBig', { max: info.max_bytes })]); next(); return; }
        f.text().then(function (txt) { return send(UI, 'POST', '/api/skills', txt); }).then(function () { done++; next(); },
          function (e) { errs.push([f.name, e.message]); next(); });
      }
      next();
    }
    load();
  }

  window.FitAI = window.FitAI || {};
  window.FitAI.skillsPage = skillsPage;
  window.FitAI.skillWords = words;
})();
