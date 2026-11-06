// The config's shared draft and its schema-driven form editor, for the gateway's and the system's modules (the admin
// server module). Every page that edits the config edits the same draft, read once with its ETag, and one bar in
// the top bar saves it whole (PUT /config with If-Match) or discards it; the problems of a save link to the page and
// the field they are about. admin is left out of the forms: it is set in the file fit was started with.
//
//   var C = FitConfig.with(UI)          the store, sharing one draft whoever asks
//   C.ensure() → Promise                 the schema and the config read (again when it changed and nothing is unsaved)
//   C.page(el, sh, render, opts)         a page of the editor: the bar in sh.tools, render(body) drawn now and after
//                                        each save, discard or reload; opts.members are the top-level config members
//                                        the page edits, and the page then also shows the raw .conf, .yaml and .json
//                                        tabs that edit that part of the draft (without them, a read-only page)
//   C.section(name, opts) → node         the form of a top-level member; opts for arrays: {filter(item), make(),
//                                        chips(item) → nodes, addLabel, empty}
//   C.place(fn(path, draft) → hash)      where a problem's path is edited, for its link
//
// A page with opts.members registers its editor with WB.editor (I154), for the assistant's page tools: page_get reads
// the page's part of the draft, page_edit writes a change into it (members set whole, operations at JSON pointers, or
// the page's part as the text of a tab, checked by /config/parse as a typed text is), page_revert takes the assistant's
// drafts out again. A draft is the shared draft changed as a person's edit changes it: unsaved, marked as the
// assistant's (the fields it changed, a note above the forms with Revert, a badge in the bar), never submitted. No
// page tool calls PUT /config, /config/text, a rollback or a reconcile, nor changes the ETag or the version: Save and
// apply stays the person's click. Secrets are not drafted: a [redacted] value the assistant copies back is put back as
// the draft holds it, and any other change of a secret field is refused.
//
// 配置的共享草稿及由 schema 生成的表单编辑器，供网关与系统各模块（admin 服务端模块）使用。所有编辑配置的页面都编辑同一份
// 草稿（连同 ETag 只读取一次），顶栏中的同一个保存栏整体保存（带 If-Match 的 PUT /config）或放弃修改；保存失败的问题
// 链接到其所在的页面与字段。表单不含 admin：它在 fit 启动时所用的配置文件中设置。带 opts.members 的页面另外还有 UI、
// .conf、.yaml、.json 四个标签页：后三者编辑该页所管配置成员在对应格式下的文本，保存时经 /config/text 整体校验后应用。
// 带 opts.members 的页面还会通过 WB.editor 登记其编辑器（I154），供助手的页面工具使用：page_get 读取本页在草稿中的部分，
// page_edit 把修改写入草稿（整体设置成员、按 JSON 指针的操作，或某个标签页格式的文本，像手工输入的文本一样经 /config/parse
// 检查），page_revert 撤回助手的草稿。助手的草稿与人工修改一样只改共享草稿：未保存、标记为助手所写（所改字段、表单上方带
// “撤回”的提示、保存栏中的徽标），从不提交。页面工具从不调用 PUT /config、/config/text、回滚或 reconcile，也不改变 ETag 或
// 版本：“保存并应用”始终由人点击。不起草密钥：助手抄回的 [redacted] 值按草稿中的原值放回，其他对密钥字段的修改一律拒绝。
(function () {
  'use strict';
  var h = UI.h;

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['cf.version', 'Config version {v}', '配置版本 {v}'], ['cf.unsaved', 'Unsaved changes', '有未保存的修改'],
    ['cf.save', 'Save and apply', '保存并应用'], ['cf.discard', 'Discard', '放弃修改'],
    ['cf.discard.q', 'Discard the changes not saved?', '放弃尚未保存的修改？'],
    ['cf.leave', 'The changes not saved will be lost.', '尚未保存的修改将会丢失。'],
    ['cf.applied', 'Applied as config version {v}.', '已应用为配置版本 {v}。'],
    ['cf.warnings', 'Applied as config version {v}, with warnings:', '已应用为配置版本 {v}，但有警告：'],
    ['cf.changed', 'The config was changed elsewhere since it was read. Discard your changes to read it again, or copy them from the JSON page first.',
      '读取之后配置已在别处被修改。放弃修改即可重新读取；也可以先在 JSON 页面复制你的修改。'],
    ['cf.reread', 'Discard and read again', '放弃并重新读取'],
    ['cf.problems', 'The config was not applied; the running config stays.', '配置未应用；运行中的配置保持不变。'],
    ['cf.failed', 'Saving failed: {e}', '保存失败：{e}'],
    ['cf.whole', 'the config', '整个配置'], ['cf.line', 'line {l}, column {c}', '第 {l} 行第 {c} 列'],
    ['cf.raw.bad', 'The JSON page holds text that does not parse: fix it or discard it before saving. The forms show the last JSON that parsed.',
      'JSON 页面中的文本无法解析：保存前请修正或放弃。表单显示的是最后一次可解析的 JSON。'],
    ['cf.tab.ui', 'UI', '界面'], ['cf.tab.conf', '.conf', '.conf'], ['cf.tab.yaml', '.yaml', '.yaml'], ['cf.tab.json', '.json', '.json'],
    ['cf.raw.note', 'This is this page\'s part of the config, as {fmt}. Edits here are parsed into the shared draft as you type; Save and apply checks and applies them.',
      '这是本页所编辑的配置部分（{fmt}）。在此的修改会即时解析进共享草稿；“保存并应用”会校验并应用。'],
    ['cf.raw.parse.bad', 'The text does not parse, so it is kept as typed and blocks saving.', '文本无法解析，已按原样保留并阻止保存。'],
    ['cf.raw.blocked', 'A raw tab of the config holds text that does not parse: fix it or discard it before saving.', '配置有原始文本标签页中的文本无法解析：请修正或放弃后再保存。'],
    ['cf.raw.extra', 'The text sets "{m}", which this page does not edit.', '文本中设置了“{m}”，本页不编辑它。'],
    ['cf.raw.loading', 'Reading the {fmt} text…', '正在读取 {fmt} 文本…'],
    ['cf.raw.format', 'Format', '格式化'], ['cf.raw.download', 'Download', '下载'],
    ['cf.inc', 'Set {name} in the config', '在配置中设置 {name}'],
    ['cf.unset', 'not set', '未设置'], ['cf.set', 'Set', '设置'], ['cf.leaveout', 'Leave out', '移除'],
    ['cf.add', 'Add', '添加'], ['cf.remove', 'Remove', '删除'], ['cf.up', 'Move up', '上移'], ['cf.down', 'Move down', '下移'],
    ['cf.items', '{n} items', '{n} 项'], ['cf.item', '1 item', '1 项'], ['cf.fields', '{n} fields', '{n} 个字段'], ['cf.field', '1 field', '1 个字段'],
    ['cf.default', 'default: {v}', '默认：{v}'], ['cf.leftout', '(left out)', '（不设置）'], ['cf.leftout.d', '(left out: {v})', '（不设置：{v}）'],
    ['cf.module', 'module', 'module'], ['cf.params', 'params', 'params'], ['cf.name', 'name', '名称'],
    ['cf.json.ph', 'JSON', 'JSON'], ['cf.none', 'Nothing yet.', '暂无。'],
    ['cf.remove.q', 'Remove {what}?', '删除 {what}？'],
    ['cf.deprecated', 'deprecated', '已弃用'],
    // the assistant's drafts (I154) / 助手的草稿
    ['cf.ai.badge', 'Assistant draft', '助手草稿'],
    ['cf.ai.note', 'The assistant drafted changes into this config, marked on the page. They are not saved: review them, then Save and apply — or Revert them.',
      '助手已把修改起草进本配置，并在页面上标出。它们尚未保存：请检查后点击“保存并应用”，或撤回。'],
    ['cf.ai.revert', 'Revert the assistant\'s draft', '撤回助手的草稿'],
    ['cf.ai.revert.q', 'You changed the page after the assistant\'s draft. Reverting returns it to what it held before the draft, and drops those changes too. Revert?',
      '助手起草之后你又修改了页面。撤回会让页面回到起草之前的内容，你之后的修改也会一并丢弃。确定撤回？'],
    ['cf.ai.reverted', 'The assistant\'s draft was reverted.', '已撤回助手的草稿。'],
    ['cf.c.listener', 'Listener', '监听'], ['cf.c.names', 'Names', '域名'], ['cf.c.issuer', 'Issuer', '签发者'], ['cf.c.from', 'Valid from', '生效时间'],
    ['cf.c.until', 'Expires', '过期时间'], ['cf.c.serial', 'Serial', '序列号'], ['cf.c.left', '{n} days left', '剩余 {n} 天'], ['cf.c.expired', 'expired', '已过期'],
    ['cf.c.none', 'No listener serves a certificate.', '没有监听在提供证书。'],
    // import / export / 导入与导出
    ['cf.ie.export', 'Export', '导出'], ['cf.ie.import', 'Import', '导入'],
    ['cf.ie.export.title', 'Export {what}', '导出{what}'], ['cf.ie.import.title', 'Import {what}', '导入{what}'],
    ['cf.ie.whole', 'the whole config', '整个配置'], ['cf.ie.part', 'this page\'s part ({m})', '本页的部分（{m}）'],
    ['cf.ie.format', 'Format', '格式'],
    ['cf.ie.f.conf', '.conf (fit\'s config syntax)', '.conf（fit 配置语法）'], ['cf.ie.f.yaml', '.yaml', '.yaml'], ['cf.ie.f.json', '.json', '.json'],
    ['cf.ie.f.store', '.store (encrypted fitstore)', '.store（加密的 fitstore）'],
    ['cf.ie.password', 'Store password', 'store 密码'],
    ['cf.ie.export.pw.hint', 'Seals the file with this password, to open it with the same password. Left empty, the file is sealed with the running store\'s key: opening it elsewhere then needs that store\'s key file or FIT_STORE_PASSWORD.',
      '用此密码加密文件，之后用同一密码打开。留空则用运行中 store 的密钥加密：在别处打开时需要该 store 的密钥文件或 FIT_STORE_PASSWORD。'],
    ['cf.ie.import.pw.hint', 'The password the file was sealed with. Leave it empty for a file sealed with the running store\'s key.',
      '文件加密时所用的密码。若文件是用运行中 store 的密钥加密的，请留空。'],
    ['cf.ie.pw.ascii', 'The store password must be printable ASCII here (it is sent in a request header).', '此处的 store 密码只能是可打印的 ASCII 字符（它在请求头中发送）。'],
    ['cf.ie.export.note', 'Exports the config as it is saved and running; changes not saved yet are not in the file.', '导出的是已保存并在运行的配置；尚未保存的修改不在文件中。'],
    ['cf.ie.export.dirty', 'This draft has unsaved changes: save them first to have them in the file.', '当前草稿有未保存的修改：如需包含它们，请先保存。'],
    ['cf.ie.exported', 'Exported {name}.', '已导出 {name}。'],
    ['cf.ie.file', 'File', '文件'], ['cf.ie.file.need', 'Choose a file to import.', '请选择要导入的文件。'],
    ['cf.ie.warn.whole', 'The file replaces the whole config (admin aside), and is applied at once as a new config version.',
      '文件将替换整个配置（admin 除外），并立即作为新的配置版本应用。'],
    ['cf.ie.warn.part', 'The file replaces {m} in the config, and is applied at once as a new config version. Other members in the file are ignored; a member the file lacks is removed from the config.',
      '文件将替换配置中的 {m}，并立即作为新的配置版本应用。文件中的其他成员会被忽略；文件中缺少的成员会从配置中移除。'],
    ['cf.ie.dirty', 'This draft has unsaved changes. Importing reads the config again afterwards, so they will be lost.',
      '当前草稿有未保存的修改。导入后会重新读取配置，这些修改将会丢失。'],
    ['cf.ie.dirty.q', 'Importing discards the changes not saved. Go on?', '导入会放弃尚未保存的修改。继续？'],
    ['cf.ie.apply', 'Import and apply', '导入并应用'],
    ['cf.ie.failed', 'The import was not applied; the running config stays.', '导入未应用；运行中的配置保持不变。'],
    ['cf.ie.page', 'Import / Export', '导入 / 导出'],
    ['cf.ie.page.note', 'Export the whole config to a file, or import a file that replaces it. A page that edits part of the config has its own Export and Import buttons, next to its format tabs, for that part only.',
      '把整个配置导出为文件，或导入文件替换整个配置。编辑部分配置的页面在其格式标签旁另有“导出”与“导入”按钮，只针对该部分。'],
    ['cf.ie.formats', 'Text formats (.conf, .yaml, .json) are readable and editable; .store is fit\'s encrypted binary format, as admin.store keeps the config.',
      '文本格式（.conf、.yaml、.json）可读可编辑；.store 是 fit 的加密二进制格式，与 admin.store 保存配置的格式相同。']
  ];
  var en = {}, zh = {};
  WORDS.forEach(function (w) { en[w[0]] = w[1]; zh[w[0]] = w[2]; });
  UI.addI18n({ 'en': en, 'zh-CN': zh });
  var t = UI.t;

  // The store: one, whichever module asks. / 存储：只有一个，供所有模块共用。
  var S = {
    ui: null, schema: null, draft: null, baseDraft: null, etag: '', version: '', dirty: false,
    errors: [],      // the problems of the last save, {path, line, column, message} / 上次保存的问题
    message: null,   // {kind, text, list, action} shown above the forms / 显示在表单上方的消息
    rawText: null,   // the JSON page's text while it does not parse / JSON 页面中无法解析的文本
    rawError: '',
    tabs: {},        // page key (location.hash) → {fmt: 'ui'|'conf'|'yaml'|'json', raws: {fmt: rawState}}
    lastRaw: null,   // {key, fmt} of the last save of a raw tab, for its problems' links / 上次原始文本保存的页面与格式
    rawSel: null,    // {key, line, column} to select in a raw text after a problem link / 问题链接要定位的文本位置
    open: {},        // path → whether its node is open / 路径 → 节点是否展开
    focus: '',       // the path to show after the next draw / 下次绘制后要定位的路径
    places: [], current: null, loading: null,
    ai: null         // the assistant's drafts: {before, after, paths, summaries} (I154) / 助手的草稿
  };

  window.addEventListener('beforeunload', function (e) {
    if (isDirty()) { e.preventDefault(); e.returnValue = t('cf.leave'); }
  });

  // ---- requests / 请求 ----
  function call(method, path, body, headers) { return S.ui.raw(method, '/api' + path, body, headers); }
  function readJSON(r) {
    return r.text().then(function (x) {
      try { return x ? JSON.parse(x) : {}; } catch (e) { return { error: x }; }
    });
  }
  // fetchConfig reads the config; cond, only when it changed since the ETag held. / 读取配置；cond 时仅在 ETag 变化后读取。
  function fetchConfig(cond) {
    return call('GET', '/config', undefined, cond && S.etag ? { 'If-None-Match': S.etag } : {}).then(function (r) {
      if (r.status === 304) return false;
      return readJSON(r).then(function (body) {
        if (!r.ok) { var e = new Error(body.error || r.status + ' ' + r.statusText); e.status = r.status; throw e; }
        S.etag = r.headers.get('ETag') || '';
        S.version = r.headers.get('Fit-Config-Version') || '';
        S.draft = body && typeof body === 'object' ? body : {};
        S.baseDraft = JSON.parse(JSON.stringify(S.draft));
        S.errors = []; S.rawText = null; S.rawError = '';
        S.ai = null;
        clearRaw();
        setDirty(false);
        // A new draft is in: the Engine menu follows it. / 新的草稿已就绪：引擎菜单随之更新。
        try { window.dispatchEvent(new Event('fit:config')); } catch (e) { /* ignore / 忽略 */ }
        return true;
      });
    });
  }

  function ensure() {
    if (!S.loading) {
      S.loading = Promise.all([S.ui.api('GET', '/api/schema'), fetchConfig(false)]).then(function (r) { S.schema = r[0]; return true; });
      S.loading.catch(function () { S.loading = null; });
      return S.loading;
    }
    return S.loading.then(function () { return isDirty() ? false : fetchConfig(true); });
  }

  // ---- raw tabs / 原始文本标签页 ----
  // A page of the editor that edits some top-level members shows the UI form and the same fragment of the draft as
  // .conf, .yaml and .json text. The text, its errors and whether it parses are kept per page (by location.hash), so
  // navigating away and back keeps unsaved text; discard, reload and a successful save clear every page's state.
  // 编辑若干顶层成员的页面同时显示 UI 表单与草稿中同一片段的 .conf、.yaml、.json 文本。文本、其错误与是否可解析
  // 按页面（以 location.hash 为键）保存，离开再返回时未保存的文本仍在；放弃修改、重新读取与成功保存会清空所有页面。
  var RAW_FORMATS = ['conf', 'yaml', 'json'], RAW_WAIT = 400;

  function pageKey() { return location.hash || '#/'; }
  function tabOf(key) {
    var st = S.tabs[key];
    if (!st) st = S.tabs[key] = { fmt: 'ui', raws: {} };
    return st;
  }
  function rawOf(key, fmt) {
    var st = tabOf(key);
    var r = st.raws[fmt];
    if (!r) r = st.raws[fmt] = { key: key, fmt: fmt, members: [], text: '', base: '', stamp: '', error: '', details: [], timer: null, loading: false, loaded: false, ta: null, view: null };
    return r;
  }
  // rawStates lists every page's raw text, in any format. / 列出所有页面、所有格式的原始文本状态。
  function rawStates() {
    var out = [];
    Object.keys(S.tabs).forEach(function (k) {
      var raws = S.tabs[k].raws;
      Object.keys(raws).forEach(function (f) { out.push(raws[f]); });
    });
    return out;
  }
  function rawDirty() {
    return rawStates().some(function (r) { return r.timer != null || r.text !== r.base || !!r.error; });
  }
  // isDirty: unsaved form edits, or text that is not parsed (or does not parse). / 是否有未保存的表单修改或未解析的文本。
  function isDirty() { return S.dirty || S.rawText != null || rawDirty(); }
  function clearRaw() {
    rawStates().forEach(function (r) { if (r.timer) clearTimeout(r.timer); r.timer = null; r.view = null; });
    S.tabs = {}; S.lastRaw = null; S.rawSel = null;
  }
  // fragment is the page's part of the draft: {member: draft[member]} for the members set. / 页面在草稿中的部分。
  function fragment(members) {
    var f = {}, d = S.draft || {};
    members.forEach(function (m) { if (Object.prototype.hasOwnProperty.call(d, m)) f[m] = d[m]; });
    return f;
  }
  function fragStamp(members) { return JSON.stringify(fragment(members)); }
  // applyFragment replaces the page's members with the parsed text's, deleting those it leaves out. / 用解析出的片段替换页面的成员。
  function applyFragment(members, frag) {
    var d = S.draft || (S.draft = {});
    members.forEach(function (m) {
      if (Object.prototype.hasOwnProperty.call(frag, m)) d[m] = frag[m]; else delete d[m];
    });
  }
  // partExcept is a draft without the members a page edits, for comparing what no page's raw text covers.
  // partExcept 返回去掉页面所编辑成员后的草稿，用于比较页面原始文本未覆盖的修改。
  function partExcept(d, members) {
    var out = {}, src = d || {};
    Object.keys(src).forEach(function (k) { if (members.indexOf(k) < 0) out[k] = src[k]; });
    return out;
  }
  // otherChanges tells whether the draft has unsaved changes outside members. A raw save sends only its page's members
  // to /config/text, which the server merges into the running config, so it may only be used when nothing else is unsaved;
  // otherwise the whole draft is saved as the UI tab does. / 草稿在页面成员之外是否有未保存的修改：有则不能只保存本页文本。
  function otherChanges(members) {
    if (!S.baseDraft) return false;
    return JSON.stringify(partExcept(S.draft, members)) !== JSON.stringify(partExcept(S.baseDraft, members));
  }

  function extraMember(members, frag) {
    var keys = Object.keys(frag || {}), extra = null;
    for (var i = 0; i < keys.length && !extra; i++) if (members.indexOf(keys[i]) < 0) extra = keys[i];
    return extra;
  }

  // renderRaw (re)reads the page's fragment from the draft, in format r.fmt. When nothing marks it changed since the
  // last read and it parses, the text is kept as it is (so returning to a tab does not discard it).
  // renderRaw（重新）从草稿读取页面片段在 r.fmt 格式下的文本；自上次读取以来草稿未变且文本可解析时保留原文本。
  function renderRaw(r, force) {
    var members = r.members || [], stamp = fragStamp(members);
    if (!force && r.loaded && (r.error || r.stamp === stamp)) { paintRaw(r); return Promise.resolve(); }
    r.loading = true; r.error = ''; r.details = []; r.stamp = stamp;
    paintRaw(r);
    return call('POST', '/config/render?format=' + encodeURIComponent(r.fmt), fragment(members)).then(function (res) {
      return res.text().then(function (x) {
        if (!res.ok) {
          var msg = x;
          try { var j = JSON.parse(x); if (j && j.error) msg = j.error; } catch (e) { /* text / 文本 */ }
          throw new Error(msg || (res.status + ' ' + res.statusText));
        }
        if (!S.tabs[r.key]) return;
        r.text = x; r.base = x; r.loaded = true; r.loading = false; r.error = ''; r.details = []; r.stamp = stamp;
        if (r.ta) r.ta.value = r.text;
        paintRaw(r); paintBar();
      });
    }).catch(function (e) {
      if (e && e.status === 401) return;
      r.loading = false; r.loaded = true; r.error = e.message || String(e); r.details = [];
      paintRaw(r); paintBar();
    });
  }

  // parseRaw reads the page's raw text as the page's fragment of the draft. / 把页面的原始文本解析为草稿中该页的片段。
  function parseRaw(r) {
    var members = r.members || [], text = r.text;
    return call('POST', '/config/parse?format=' + encodeURIComponent(r.fmt), text, { 'Content-Type': 'text/plain; charset=utf-8' }).then(function (res) {
      return res.text().then(function (x) {
        var body = null;
        try { body = x ? JSON.parse(x) : null; } catch (e) { body = null; }
        if (!res.ok) {
          if (res.status === 401) return;
          setRawError(r, body, x);
          return;
        }
        if (!body || typeof body !== 'object' || Array.isArray(body)) { setRawError(r, null, x || t('cf.raw.parse.bad')); return; }
        var extra = extraMember(members, body);
        if (extra) { setRawError(r, null, t('cf.raw.extra', { m: extra })); return; }
        if (!S.tabs[r.key] || S.tabs[r.key].raws[r.fmt] !== r) return;
        applyFragment(members, body);
        r.base = text; r.error = ''; r.details = []; r.stamp = fragStamp(members);
        paintRaw(r);
        changed();
      });
    }).catch(function (e) {
      if (e && e.status === 401) return;
      setRawError(r, null, e.message || String(e));
    });
  }
  function setRawError(r, body, msg) {
    r.error = (body && body.error) || (typeof msg === 'string' && msg ? msg : t('cf.raw.parse.bad'));
    r.details = (body && body.errors) || [];
    paintRaw(r);
    paintBar();
  }
  function rawErrorParts(r) { return problemParts(t('cf.raw.parse.bad'), r.error, r.details); }
  // problemParts renders an error and its problems ({path, line, column, message}) under a title, for a raw text that
  // does not parse and for an import that fails. / 在标题下渲染错误及其问题，供无法解析的原始文本与失败的导入使用。
  function problemParts(title, error, details) {
    if (!error && (!details || !details.length)) return [];
    return [
      h('b', title),
      error ? h('div.small', error) : null,
      details && details.length ? h('ul', { style: { margin: '6px 0 0', paddingLeft: '18px' } }, details.map(function (e) {
        var at = e.line ? ' (' + t('cf.line', { l: e.line, c: e.column || 0 }) + ')' : '';
        return h('li', h('code', e.path || t('cf.whole')), at, ': ', e.message);
      })) : null
    ];
  }
  // paintRaw updates the error and loading state of a drawn raw panel, but never its text (the cursor stays).
  // paintRaw 更新已绘制原始文本面板的错误与加载状态，但不碰文本（光标保持）。
  function paintRaw(r) {
    var v = r.view;
    if (!v || !document.body.contains(v.ta)) return;
    v.ta.disabled = r.loading && !r.loaded;
    v.ta.classList.toggle('is-invalid', !!r.error);
    v.load.hidden = !r.loading;
    var parts = rawErrorParts(r);
    v.err.hidden = !parts.length;
    UI.mount(v.err, parts);
  }

  // flushRaw parses every pending raw text now: save must not apply a text that was not parsed yet. It resolves true
  // when any text does not parse. / 立即解析所有待解析的原始文本；有文本无法解析时返回 true。
  function flushRaw() {
    var pending = rawStates().filter(function (r) { return r.timer != null || (r.text !== r.base && !r.error); });
    if (!pending.length) return Promise.resolve(rawStates().some(function (r) { return !!r.error; }));
    return Promise.all(pending.map(function (r) {
      if (r.timer) { clearTimeout(r.timer); r.timer = null; }
      return parseRaw(r);
    })).then(function () { return rawStates().some(function (r) { return !!r.error; }); });
  }

  function downloadRaw(r) {
    var name = 'fit-' + (r.members || []).join('-') + '-' + (S.version || 'draft') + '.' + r.fmt;
    var a = h('a', { href: URL.createObjectURL(new Blob([r.text], { type: 'text/plain; charset=utf-8' })), download: name });
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
  }

  // selectRaw puts the cursor on a line and column of the raw text, as a problem link names. / 把光标定位到问题所指的行列。
  function selectRaw(r, line, column) {
    if (!r.ta || !line) return;
    var lines = r.text.split('\n'), off = 0;
    for (var i = 0; i < line - 1 && i < lines.length; i++) off += lines[i].length + 1;
    off = Math.min(off + Math.max(0, (column || 1) - 1), r.text.length);
    r.ta.focus();
    r.ta.setSelectionRange(off, Math.min(off + 1, r.text.length));
    if (r.ta.scrollIntoView) r.ta.scrollIntoView({ block: 'center' });
  }

  // drawRaw draws one raw format of a page: the hint, a parse-error callout, the text and its tools. / 绘制页面的一个原始文本格式。
  function drawRaw(c, body, fmt) {
    var r = rawOf(c.key, fmt);
    r.members = membersOf(c);
    var note = c.opts.rawNote, noteText = typeof note === 'function' ? note(fmt) : note || t('cf.raw.note', { fmt: '.' + fmt });
    var ta = h('textarea.textarea.textarea--code.cf-raw', { spellcheck: 'false', 'aria-label': t('cf.tab.' + fmt) });
    ta.value = r.text;
    var load = h('div.muted.small.cf-raw-loading', t('cf.raw.loading', { fmt: '.' + fmt }));
    var err = h('div.callout.callout--danger', { hidden: true });
    var copyB = UI.btn(t('copy'), 'copy', function () { UI.copy(r.ta ? r.ta.value : r.text); }, 'ghost sm');
    var fmtB = UI.btn(t('cf.raw.format'), 'code', function () {
      UI.busy(fmtB, (r.text !== r.base && !r.error ? parseRaw(r) : Promise.resolve()).then(function () {
        return r.error ? null : renderRaw(r, true);
      })).catch(UI.fail);
    }, 'ghost sm');
    var dlB = UI.btn(t('cf.raw.download'), 'download', function () { downloadRaw(r); }, 'ghost sm');
    body.appendChild(h('div.stack.cf-raw-panel', h('div.callout', UI.icon('info'), ' ', noteText), err, load, ta,
      h('div.btn-row.cf-raw-tools', copyB, fmtB, dlB)));
    r.ta = ta;
    r.view = { ta: ta, load: load, err: err };
    paintRaw(r);
    ta.addEventListener('input', function () {
      r.text = ta.value;
      if (r.timer) clearTimeout(r.timer);
      r.timer = setTimeout(function () { r.timer = null; parseRaw(r); paintBar(); }, RAW_WAIT);
      paintBar();
    });
    // A text not edited follows the draft, changed since on the UI tab or by the assistant. / 未修改的文本跟随草稿。
    if (!r.loading && (!r.loaded || (r.text === r.base && !r.error && r.timer == null))) renderRaw(r);
    if (S.rawSel && S.rawSel.key === c.key) { var sel = S.rawSel; S.rawSel = null; selectRaw(r, sel.line, sel.column); }
  }

  // tabBar is a page's format tabs, with the Export and Import of the page's members beside them.
  // tabBar 是页面的格式标签页，旁边是本页成员的导出与导入按钮。
  function tabBar(c, tb) {
    var items = [['ui', t('cf.tab.ui')], ['conf', t('cf.tab.conf')], ['yaml', t('cf.tab.yaml')], ['json', t('cf.tab.json')]];
    var bar2 = UI.tabs(items, tb.fmt, function (k) { switchTab(c, k); });
    bar2.classList.add('cf-tabs__list');
    var ms = membersOf(c);
    var tools = h('div.btn-row.cf-tabs__tools',
      UI.btn(t('cf.ie.export'), 'download', function () { exportDialog(ms, RAW_FORMATS.indexOf(tb.fmt) >= 0 ? tb.fmt : 'yaml'); }, 'ghost sm'),
      UI.btn(t('cf.ie.import'), 'upload', function () { importDialog(ms); }, 'ghost sm'));
    return h('div.cf-tabs', bar2, tools);
  }
  // switchTab changes the page's tab: leaving a raw text parses it first, and a text that does not parse keeps the
  // page on its raw tab. / 切换页面标签：离开原始文本前先解析，无法解析时停留在该标签页。
  function switchTab(c, fmt) {
    var tb = tabOf(c.key);
    if (tb.fmt === fmt) return;
    var leaving = tb.fmt === 'ui' ? null : rawOf(c.key, tb.fmt);
    var wait = Promise.resolve();
    if (leaving) {
      if (leaving.timer) { clearTimeout(leaving.timer); leaving.timer = null; }
      if (leaving.text !== leaving.base && !leaving.error) wait = parseRaw(leaving);
    }
    wait.then(function () {
      if (leaving && leaving.error) {
        UI.toast(t('cf.raw.parse.bad'), 'error');
        paintRaw(leaving);
        return;
      }
      tb.fmt = fmt;
      redraw();
    }).catch(UI.fail);
  }

  // membersOf is the top-level config members a page edits, from its opts. / 页面所编辑的顶层配置成员。
  function membersOf(c) {
    if (!c || !c.opts) return [];
    var ms = typeof c.opts.members === 'function' ? c.opts.members() : c.opts.members;
    return (ms || []).filter(function (x) { return typeof x === 'string' && x; });
  }

  // ---- import / export / 导入与导出 ----
  // Export: POST /config/export {format, members?, password?} answers the file, named by its Content-Disposition.
  // Import: POST /config/import?format=…&members=… with the file as the body (Fit-Store-Password for a store sealed
  // with its own password, If-Match the draft's ETag) answers as PUT /config/text does. members, when given, are the
  // top-level members taken from the file (the page's part); without them the file is the whole config.
  // 导出：POST /config/export {format, members?, password?} 应答文件，文件名取自 Content-Disposition。
  // 导入：POST /config/import?format=…&members=…，请求体为文件（自带密码加密的 store 用 Fit-Store-Password，If-Match 为草稿的
  // ETag），应答同 PUT /config/text。给出 members 时只取文件中的这些顶层成员（本页的部分）；否则文件即整个配置。
  var IE_FORMATS = ['conf', 'yaml', 'json', 'store'];
  var IE_EXT = { conf: 'conf', yaml: 'yaml', yml: 'yaml', json: 'json', store: 'store' };

  // ieWhat names what an import or export covers. / 导入或导出所涉及的范围。
  function ieWhat(members) { return members && members.length ? t('cf.ie.part', { m: members.join(', ') }) : t('cf.ie.whole'); }
  function formatSelect(fmt, onchange) {
    var sel = h('select.select', { onchange: onchange }, IE_FORMATS.map(function (f) { return h('option', { value: f, selected: f === fmt }, t('cf.ie.f.' + f)); }));
    sel.value = fmt;
    return sel;
  }
  // A header value is Latin-1 to fetch; a store password is kept to printable ASCII. / 请求头值须为可打印 ASCII。
  function asciiOK(pw) { return /^[\x20-\x7e]*$/.test(pw); }
  // fileName is the name a reply's Content-Disposition gives, if any. / 应答 Content-Disposition 中的文件名（若有）。
  function fileName(cd) {
    if (!cd) return '';
    var m = /filename\*\s*=\s*(?:UTF-8|utf-8)''([^;]+)/.exec(cd);
    if (m) { try { return decodeURIComponent(m[1].trim()); } catch (e) { /* as written / 原样 */ } }
    m = /filename\s*=\s*"([^"]*)"/.exec(cd) || /filename\s*=\s*([^;]+)/.exec(cd);
    return m ? m[1].trim() : '';
  }
  function saveBlob(blob, name) {
    var a = h('a', { href: URL.createObjectURL(blob), download: name });
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
  }

  // exportDialog asks for the format (and a store's password) and downloads the members' part of the running config,
  // or all of it. / exportDialog 询问格式（及 store 密码），下载运行中配置的成员部分或整个配置。
  function exportDialog(members, fmt) {
    members = members || [];
    var pw = h('input.input', { type: 'password', name: 'store-password', autocomplete: 'new-password' });
    var pwField = UI.field(t('cf.ie.password'), pw, t('cf.ie.export.pw.hint'));
    var sel = formatSelect(IE_FORMATS.indexOf(fmt) >= 0 ? fmt : 'yaml', function () { pwField.hidden = sel.value !== 'store'; });
    pwField.hidden = sel.value !== 'store';
    var err = h('div.callout.callout--danger', { hidden: true });
    function go() {
      err.hidden = true;
      var f = sel.value, body = { format: f };
      if (members.length) body.members = members;
      if (f === 'store' && pw.value) body.password = pw.value;
      return call('POST', '/config/export', body).then(function (r) {
        if (!r.ok) {
          return readJSON(r).then(function (res) {
            if (r.status === 401) return;
            err.textContent = res.error || r.status + ' ' + r.statusText; err.hidden = false;
            return false;
          });
        }
        return r.blob().then(function (b) {
          var name = fileName(r.headers.get('Content-Disposition')) || 'fit-' + (members.length ? members.join('-') : 'config') + '.' + f;
          saveBlob(b, name);
          UI.toast(t('cf.ie.exported', { name: name }));
        });
      });
    }
    UI.modal({
      title: t('cf.ie.export.title', { what: ieWhat(members) }),
      body: h('div.stack', h('div.callout', UI.icon('info'), ' ', t('cf.ie.export.note')),
        isDirty() ? h('div.callout.callout--warn', UI.icon('alert'), ' ', t('cf.ie.export.dirty')) : null,
        UI.field(t('cf.ie.format'), sel), pwField, err),
      actions: [{ label: t('cancel') }, { label: t('cf.ie.export'), kind: 'primary', onclick: go }]
    });
  }

  // importDialog asks for a file, its format (from its extension) and a store's password, and applies it in place of
  // the members' part of the config, or of all of it; a success reads the config again as a save does.
  // importDialog 询问文件、格式（按扩展名推断）与 store 密码，用其替换配置中成员的部分或整个配置并立即应用；
  // 成功后与保存一样重新读取配置。
  function importDialog(members) {
    members = members || [];
    var pw = h('input.input', { type: 'password', name: 'store-password', autocomplete: 'off' });
    var pwField = UI.field(t('cf.ie.password'), pw, t('cf.ie.import.pw.hint'));
    var sel = formatSelect('yaml', function () { pwField.hidden = sel.value !== 'store'; });
    var file = h('input.input', { type: 'file', accept: '.conf,.yaml,.yml,.json,.store', onchange: function () {
      var f = file.files && file.files[0], m = f ? /\.([A-Za-z]+)$/.exec(f.name) : null, ext = m ? IE_EXT[m[1].toLowerCase()] : null;
      if (ext) sel.value = ext;
      pwField.hidden = sel.value !== 'store';
    } });
    pwField.hidden = true;
    var err = h('div.callout.callout--danger', { hidden: true });
    function showErr(parts) { UI.mount(err, parts); err.hidden = false; }
    function go() {
      err.hidden = true;
      var f = file.files && file.files[0], fmt = sel.value;
      if (!f) { showErr(t('cf.ie.file.need')); return false; }
      if (fmt === 'store' && !asciiOK(pw.value)) { showErr(t('cf.ie.pw.ascii')); return false; }
      return (isDirty() ? UI.confirm(t('cf.ie.dirty.q'), t('cf.ie.apply'), true) : Promise.resolve(true)).then(function (ok) {
        if (!ok) return false;
        var q = '/config/import?format=' + encodeURIComponent(fmt) + (members.length ? '&members=' + encodeURIComponent(members.join(',')) : '');
        var hd = { 'Content-Type': fmt === 'store' ? 'application/octet-stream' : 'text/plain; charset=utf-8' };
        if (S.etag) hd['If-Match'] = S.etag;
        if (fmt === 'store' && pw.value) hd['Fit-Store-Password'] = pw.value;
        return call('POST', q, f, hd).then(function (r) {
          return readJSON(r).then(function (res) {
            if (r.ok) {
              var w = res.warnings || [];
              return fetchConfig(false).then(function () {
                S.message = w.length ? { kind: 'warn', text: t('cf.warnings', { v: res.version }), list: w } : null;
                UI.toast(t('cf.applied', { v: res.version }));
                paintNotice(); redraw();
                refreshMenu();
              });
            }
            if (r.status === 401) return;
            if (r.status === 412) { showErr([h('b', t('cf.ie.failed')), h('div.small', t('cf.changed'))]); return false; }
            showErr(problemParts(t('cf.ie.failed'), res.error || (res.errors && res.errors.length ? '' : r.status + ' ' + r.statusText), res.errors));
            return false;
          });
        });
      });
    }
    UI.modal({
      title: t('cf.ie.import.title', { what: ieWhat(members) }),
      body: h('div.stack',
        h('div.callout.callout--warn', UI.icon('alert'), ' ', members.length ? t('cf.ie.warn.part', { m: members.join(', ') }) : t('cf.ie.warn.whole')),
        isDirty() ? h('div.callout.callout--warn', UI.icon('alert'), ' ', t('cf.ie.dirty')) : null,
        UI.field(t('cf.ie.file'), file), UI.field(t('cf.ie.format'), sel), pwField, err),
      actions: [{ label: t('cancel') }, { label: t('cf.ie.apply'), kind: 'danger', onclick: go }]
    });
  }

  // ---- state / 状态 ----
  var bar = null, notice = null;
  function setDirty(d) { S.dirty = d; paintBar(); }
  // syncInc makes each include checkbox show whether its value is set. / 让每个“是否设置”复选框反映其值是否存在。
  function syncInc() {
    var all = document.querySelectorAll('.cf-inc');
    for (var i = 0; i < all.length; i++) { var b = all[i]; if (b._slot) b.checked = b._slot.get() !== undefined; }
  }
  // changed marks the draft edited. / 标记草稿已修改。
  function changed() {
    if (S.message && S.message.kind !== 'danger') S.message = null;
    setDirty(true);
    paintNotice();
    syncInc();
  }

  function redraw() {
    var c = S.current;
    if (!c || !document.body.contains(c.el)) return;
    var sc = document.scrollingElement, main = c.el.closest('.main');
    var y = sc ? sc.scrollTop : 0, my = main ? main.scrollTop : 0;
    draw(c);
    if (sc) sc.scrollTop = y;
    if (main) main.scrollTop = my;
    showFocus(c.el);
  }

  function reload() {
    S.message = null;
    return fetchConfig(false).then(function () { paintNotice(); redraw(); });
  }

  function discard() {
    return (isDirty() ? UI.confirm(t('cf.discard.q'), t('cf.discard'), true) : Promise.resolve(true)).then(function (ok) {
      if (ok) return reload();
    });
  }

  function save() {
    if (S.rawText != null) { UI.toast(t('cf.raw.bad'), 'error'); return Promise.resolve(); }
    var c = S.current, key = c ? c.key : null, tb = key ? S.tabs[key] : null;
    var fmt = tb && RAW_FORMATS.indexOf(tb.fmt) >= 0 ? tb.fmt : null;
    var members = membersOf(c);
    S.lastRaw = null;
    return flushRaw().then(function (bad) {
      if (bad) { UI.toast(t('cf.raw.blocked'), 'error'); return; }
      // The raw text save merges only this page's members into the running config: use it when they are the only
      // unsaved changes, so that its problems carry the text's line and column; otherwise save the whole draft.
      // 原始文本保存只把本页成员合并进运行中的配置：仅当只有它们未保存时才用它（其问题带有文本的行列）；
      // 否则整体保存草稿，以免丢掉其他页面（或本页 UI 标签页）未保存的修改。
      if (fmt && members.length && !otherChanges(members)) {
        S.lastRaw = { key: key, fmt: fmt };
        var q = '/config/text?format=' + encodeURIComponent(fmt) + '&members=' + encodeURIComponent(members.join(','));
        return call('PUT', q, rawOf(key, fmt).text, { 'Content-Type': 'text/plain; charset=utf-8', 'If-Match': S.etag }).then(handleSave);
      }
      return call('PUT', '/config', JSON.stringify(S.draft, null, 2), { 'If-Match': S.etag }).then(handleSave);
    }).catch(UI.fail);
  }

  // handleSave handles the answer of a save, of the whole config or of a page's raw text: the same answers, the same
  // notice, and the problems linked to where each is edited. / 处理整体保存或页面原始文本保存的应答：相同的提示与问题链接。
  function handleSave(r) {
    return readJSON(r).then(function (res) {
      if (r.ok) {
        var w = res.warnings || [];
        return fetchConfig(false).then(function () {
          S.message = w.length ? { kind: 'warn', text: t('cf.warnings', { v: res.version }), list: w } : null;
          UI.toast(t('cf.applied', { v: res.version }));
          paintNotice(); redraw();
          refreshMenu();
        });
      }
      if (r.status === 401) return;
      if (r.status === 412) {
        S.errors = [];
        S.message = { kind: 'danger', text: t('cf.changed'), action: [t('cf.reread'), reload] };
      } else {
        S.errors = res.errors || [];
        S.message = S.errors.length ? null : { kind: 'danger', text: t('cf.failed', { e: res.error || r.status + ' ' + r.statusText }) };
      }
      paintNotice(); redraw();
      var n = notice && notice.firstChild;
      if (n && n.scrollIntoView) n.scrollIntoView({ block: 'nearest' });
    });
  }

  // refreshMenu shows the modules of the roles a config started, hides those it stopped. / 刷新菜单：显示新启动角色的模块，隐藏已停止的。
  function refreshMenu() { if (window.WB && WB.refresh) WB.refresh().catch(UI.fail); }

  // rollback applies version n again, as a new version. / 重新应用版本 n，作为新版本。
  function rollback(n) {
    return call('POST', '/rollback/' + encodeURIComponent(n), undefined, { 'If-Match': S.etag }).then(function (r) {
      return readJSON(r).then(function (res) {
        if (!r.ok) { var e = new Error(res.error || r.status + ' ' + r.statusText); e.status = r.status; throw e; }
        return fetchConfig(false).then(function () {
          var w = res.warnings || [];
          S.message = w.length ? { kind: 'warn', text: t('cf.warnings', { v: res.version }), list: w } : null;
          paintNotice(); refreshMenu(); return res;
        });
      });
    });
  }

  // ---- the bar and the notices / 保存栏与提示 ----
  function paintBar() {
    if (!bar) {
      var saveB = UI.btn(t('cf.save'), 'save', function () { UI.busy(saveB, save()).then(paintBar, paintBar); }, 'primary sm');
      var disc = UI.btn(t('cf.discard'), 'undo', function () { UI.busy(disc, discard()).catch(UI.fail).then(paintBar); }, 'sm');
      bar = { el: h('div.row', h('span.cf-state'), disc, saveB), state: null, save: saveB, discard: disc };
      bar.state = bar.el.firstChild;
    }
    var dirty = isDirty();
    UI.mount(bar.state, dirty ? UI.badge(t('cf.unsaved'), 'warn', true) : h('span.muted.small', S.version ? t('cf.version', { v: S.version }) : ''),
      S.ai ? UI.badge(t('cf.ai.badge'), 'primary', true) : null);
    bar.save.disabled = !dirty;
    bar.discard.disabled = !dirty;
  }

  function problemLink(e) {
    var where = e.path || t('cf.whole');
    var at = e.line ? ' (' + t('cf.line', { l: e.line, c: e.column || 0 }) + ')' : '';
    var hash = e.path ? locate(e.path) : (e.line ? '#/sys.json/json' : null);
    // A line and column are of the raw text last saved: select its tab at that place. / 行列属于上次保存的原始文本：选中其标签页并定位。
    var raw = e.line && S.lastRaw && S.tabs[S.lastRaw.key] ? S.lastRaw : null;
    if (raw) hash = raw.key;
    var label = h('code', where);
    var link = hash ? h('a', { href: hash, onclick: function (ev) {
      ev.preventDefault();
      S.focus = e.path || '';
      if (raw) { S.tabs[raw.key].fmt = raw.fmt; S.rawSel = { key: raw.key, line: e.line, column: e.column }; }
      if (location.hash === hash) redraw(); else location.hash = hash;
    } }, label) : label;
    return h('li', link, at, ': ', e.message);
  }

  function paintNotice() {
    paintBar();
    if (!notice) notice = h('div.stack');
    var parts = [];
    if (S.rawText != null) parts.push(h('div.callout.callout--warn', UI.icon('alert'), ' ', t('cf.raw.bad')));
    if (S.ai) parts.push(aiNote());
    if (S.message) {
      var m = S.message;
      parts.push(h('div.callout' + (m.kind ? '.callout--' + m.kind : ''),
        h('div.row.row--between', h('span', m.text),
          h('span.btn-row', m.action ? UI.btn(m.action[0], 'refresh', function (ev) { UI.busy(ev.currentTarget, m.action[1]()).catch(UI.fail); }, 'sm') : null,
            h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', 'aria-label': t('close'), onclick: function () { S.message = null; paintNotice(); } }, UI.icon('x')))),
        m.list ? h('ul', { style: { margin: '6px 0 0', paddingLeft: '18px' } }, m.list.map(function (x) { return h('li', x); })) : null));
    }
    if (S.errors.length) {
      parts.push(h('div.callout.callout--danger', h('b', t('cf.problems')),
        h('ul', { style: { margin: '6px 0 0', paddingLeft: '18px' } }, S.errors.map(problemLink))));
    }
    UI.mount(notice, parts);
    notice.hidden = !parts.length;
    return notice;
  }

  // ---- places / 位置 ----
  function locate(path) {
    for (var i = 0; i < S.places.length; i++) {
      var hash = S.places[i](path, S.draft || {});
      if (hash) return hash;
    }
    return null;
  }

  function showFocus(el) {
    if (!S.focus) return;
    var p = S.focus;
    S.focus = '';
    while (p) {
      var n = el.querySelector('[data-path="' + (window.CSS && CSS.escape ? CSS.escape(p) : p) + '"]');
      if (n) {
        for (var a = n; a && a !== el; a = a.parentNode) if (a.tagName === 'DETAILS') a.open = true;
        n.scrollIntoView({ block: 'center' });
        n.classList.add('cf-flash');
        var f = n.querySelector('input, select, textarea');
        if (f) f.focus({ preventScroll: true });
        return;
      }
      var cut = Math.max(p.lastIndexOf('.'), p.lastIndexOf('['));
      p = cut > 0 ? p.slice(0, cut) : '';
    }
  }

  // page draws a page of the editor; with opts.members, it also gets the UI/.conf/.yaml/.json tabs.
  // page 绘制编辑器的一个页面；给出 opts.members 时另有 UI/.conf/.yaml/.json 标签页。
  function draw(c) {
    var members = membersOf(c);
    var tb = members.length && c.key ? tabOf(c.key) : null;
    if (tb) tb.members = members;
    var tabs_ = tb ? tabBar(c, tb) : null;
    var body = h('div.stack');
    paintNotice();
    UI.mount(c.el, tabs_, notice, body);
    if (tabs_ && tb.fmt !== 'ui') drawRaw(c, body, tb.fmt);
    else c.render(body);
    paintMarks(c.el);
  }
  function page(el, sh, render, opts) {
    paintBar();
    UI.mount(sh.tools, bar.el);
    var c = S.current = { el: el, render: render, opts: opts || {}, key: pageKey() };
    if (window.WB && WB.editor) WB.editor(membersOf(c).length ? editorOf(c) : null);
    el.appendChild(h('div', UI.icon('refresh'), ' ', t('loading')));
    return ensure().then(function () {
      if (S.current !== c) return;
      draw(c);
      showFocus(el);
    }).catch(function (e) {
      if (e && e.status === 401) return;
      UI.mount(el, h('div.callout.callout--danger', e.message || String(e)));
    });
  }

  // ---- the assistant's drafts (I154) / 助手的草稿 ----
  // The page tools of the assistant reach the page shown through editorOf(c), which WB.editor holds. Every call is
  // answered from the page alone: nothing is sent to the config API but /config/parse, the same check a typed text
  // gets; a draft goes into S.draft and the raw tabs, marked unsaved and as the assistant's, and only the person's
  // Save and apply submits it. S.ai.before is the page as it was before the first draft, which page_revert and the
  // note's Revert restore; S.ai.after is how the assistant left it, so that a revert asked by the assistant cannot drop
  // what the person changed since.
  // 助手的页面工具经 editorOf(c)（由 WB.editor 持有）作用于当前页面。每次调用都只在页面内完成：除了与手工输入的文本相同的
  // /config/parse 检查外，不向配置接口发送任何请求；草稿写入 S.draft 与原始文本标签页，标为未保存且由助手所写，只有人点击
  // “保存并应用”才会提交。S.ai.before 是首次起草之前的页面，page_revert 与提示中的“撤回”据此恢复；S.ai.after 是助手留下的
  // 状态，使助手要求的撤回不会丢掉人之后的修改。
  var AI_MAX_PATHS = 200;
  function clone(v) { return v === undefined ? undefined : JSON.parse(JSON.stringify(v)); }
  function same(a, b) { return JSON.stringify(a) === JSON.stringify(b); }
  // secretName mirrors the assistant's: a member holding a password, a key, a token or a hash. / 与助手的判断一致：是否为密钥字段。
  function secretName(name) {
    var n = String(name).toLowerCase();
    if (/(_file|_env|_ttl)$/.test(n) || /^(max_|min_)/.test(n)) return false;
    if (/password|secret|passphrase/.test(n)) return true;
    if (['passwd', 'pass', 'token', 'tokens', 'key', 'keys', 'apikey', 'hash', 'credential', 'credentials', 'authorization', 'cookie',
      'signature', 'psk', 'private_key'].indexOf(n) >= 0) return true;
    return /(_token|_tokens|_key|_keys|_hash|_sha256|_signature)$/.test(n);
  }
  function redacted(v) { return typeof v === 'string' && v.indexOf('[redacted') >= 0; }
  function ptrOf(tokens) { return '/' + tokens.map(function (x) { return String(x).replace(/~/g, '~0').replace(/\//g, '~1'); }).join('/'); }
  function parsePtr(p) {
    if (typeof p !== 'string' || p[0] !== '/') throw new Error('"' + p + '" is not a JSON pointer: it starts with /');
    return p.slice(1).split('/').map(function (x) { return x.replace(/~1/g, '/').replace(/~0/g, '~'); });
  }
  // guard returns v, to go where old is, with each [redacted] value put back as the draft holds it, and refuses a
  // change of a secret: the assistant never writes one. Array elements are matched by name, else by place.
  // guard 返回将写到 old 处的 v：其中的 [redacted] 按草稿原值放回；拒绝任何密钥的修改（助手从不写密钥）。数组元素按 name 匹配，否则按位置。
  function guard(v, old, tokens, secret) {
    var at = ptrOf(tokens);
    if (typeof v === 'string') {
      if (redacted(v)) {
        if (typeof old === 'string' && old !== '') return old;
        throw new Error(at + ' is "' + v + '", a redacted value the page does not hold there: leave that member as it is');
      }
      if (secret && v !== old) throw new Error(at + ' holds a secret, which the assistant never writes: the person sets it on the page');
      return v;
    }
    if (Array.isArray(v)) {
      var olds = Array.isArray(old) ? old : [];
      return v.map(function (x, i) {
        var o = olds[i];
        if (x && typeof x === 'object' && typeof x.name === 'string') {
          var byName = olds.filter(function (y) { return y && typeof y === 'object' && y.name === x.name; })[0];
          if (byName) o = byName;
        }
        return guard(x, o, tokens.concat([String(i)]), secret);
      });
    }
    if (v && typeof v === 'object') {
      var oo = old && typeof old === 'object' && !Array.isArray(old) ? old : null, out = {};
      Object.keys(v).forEach(function (k) { out[k] = guard(v[k], oo ? oo[k] : undefined, tokens.concat([k]), secret || secretName(k)); });
      if (oo) Object.keys(oo).forEach(function (k) {
        if (!(k in v) && (secret || secretName(k)) && oo[k] != null && oo[k] !== '') {
          throw new Error(ptrOf(tokens.concat([k])) + ' holds a secret the draft would drop: copy it back as [redacted], or the person removes it on the page');
        }
      });
      return out;
    }
    if (secret && !same(v, old)) throw new Error(at + ' holds a secret, which the assistant never writes: the person sets it on the page');
    return v;
  }
  // lookup returns the value at tokens in root. / 返回 root 中 tokens 处的值。
  function lookup(root, tokens) {
    var cur = root;
    for (var i = 0; i < tokens.length; i++) {
      if (cur == null || typeof cur !== 'object') return undefined;
      cur = Array.isArray(cur) ? cur[Number(tokens[i])] : cur[tokens[i]];
    }
    return cur;
  }
  // applyOp applies add, replace or remove at tokens of root; an add makes the objects and arrays its path lacks.
  // applyOp 在 root 的 tokens 处执行 add、replace 或 remove；add 会创建路径上缺少的对象与数组。
  function applyOp(root, op, tokens, v) {
    var parent = root;
    for (var i = 0; i < tokens.length - 1; i++) {
      var k = tokens[i], next = Array.isArray(parent) ? parent[Number(k)] : parent[k];
      if (next == null && op === 'add' && !Array.isArray(parent)) {
        next = /^(\d+|-)$/.test(tokens[i + 1]) ? [] : {};
        parent[k] = next;
      }
      if (next == null || typeof next !== 'object') throw new Error('the draft has nothing at ' + ptrOf(tokens.slice(0, i + 1)));
      parent = next;
    }
    var last = tokens[tokens.length - 1];
    if (Array.isArray(parent)) {
      var n = last === '-' && op === 'add' ? parent.length : /^\d+$/.test(last) ? Number(last) : -1;
      if (n < 0 || n > parent.length || (n === parent.length && op !== 'add')) throw new Error('"' + last + '" is not an index of the array at ' + ptrOf(tokens.slice(0, -1)));
      if (op === 'add') parent.splice(n, 0, v);
      else if (op === 'replace') parent[n] = v;
      else parent.splice(n, 1);
      return;
    }
    if (op !== 'add' && !Object.prototype.hasOwnProperty.call(parent, last)) throw new Error('the draft has nothing at ' + ptrOf(tokens));
    if (op === 'remove') delete parent[last]; else parent[last] = v;
  }
  // diffPaths lists where b differs from a, as the form's data-paths (a.b[0].c), for the marks.
  // diffPaths 以表单的 data-path 写法（a.b[0].c）列出 b 与 a 的不同之处，用于标记。
  function diffPaths(a, b, path, out) {
    if (out.length >= AI_MAX_PATHS || same(a, b)) return out;
    if (Array.isArray(a) && Array.isArray(b)) {
      for (var i = 0; i < Math.max(a.length, b.length); i++) diffPaths(a[i], b[i], path + '[' + i + ']', out);
    } else if (a && b && typeof a === 'object' && typeof b === 'object' && !Array.isArray(a) && !Array.isArray(b)) {
      var keys = Object.keys(a).concat(Object.keys(b).filter(function (k) { return !(k in a); }));
      keys.forEach(function (k) { diffPaths(a[k], b[k], path ? path + '.' + k : k, out); });
    } else out.push(path);
    return out;
  }
  // aiStamp is the draft and the edited texts, to tell whether the person changed the page since the assistant did.
  // aiStamp 为草稿与已修改的文本，用于判断助手起草之后人是否又改过页面。
  function aiStamp() {
    return JSON.stringify([S.draft, rawStates().filter(function (r) { return r.text !== r.base || r.error; }).map(function (r) { return [r.key, r.fmt, r.text]; })]);
  }
  function aiSnapshot() {
    return { draft: clone(S.draft), dirty: S.dirty, raws: rawStates().map(function (r) {
      return { r: r, text: r.text, base: r.base, stamp: r.stamp, loaded: r.loaded };
    }), fmts: Object.keys(S.tabs).map(function (k) { return [k, S.tabs[k].fmt]; }) };
  }
  // aiRestore returns the page to a snapshot: the draft, and each raw text as it was. / 把页面恢复到快照：草稿及各原始文本。
  function aiRestore(snap) {
    S.draft = snap.draft;
    var kept = [];
    snap.raws.forEach(function (x) {
      var r = x.r;
      if (!S.tabs[r.key] || S.tabs[r.key].raws[r.fmt] !== r) return;
      if (r.timer) { clearTimeout(r.timer); r.timer = null; }
      r.text = x.text; r.base = x.base; r.stamp = x.stamp; r.loaded = x.loaded; r.error = ''; r.details = [];
      kept.push(r);
    });
    rawStates().forEach(function (r) { if (kept.indexOf(r) < 0) { r.loaded = false; r.error = ''; r.text = r.base = ''; } });
    snap.fmts.forEach(function (f) { if (S.tabs[f[0]]) S.tabs[f[0]].fmt = f[1]; });
    S.ai = null;
    setDirty(snap.dirty);
  }
  // aiRevert takes the assistant's drafts out; asked by the assistant, only while the person changed nothing since.
  // aiRevert 撤回助手的草稿；由助手要求时，仅当人在此之后没有修改过。
  function aiRevert(byPerson) {
    if (!S.ai) throw new Error('there is no draft of yours on the page: nothing to revert');
    if (!byPerson && aiStamp() !== S.ai.after) {
      throw new Error('the person has changed the page since your draft: they revert it with the page\'s Revert button, or discard the changes');
    }
    aiRestore(S.ai.before);
    paintNotice(); redraw();
    return { reverted: true, unsaved: isDirty() };
  }
  // aiNote is the note above the forms while the draft holds the assistant's changes. / 草稿含助手修改时表单上方的提示。
  function aiNote() {
    var revertB = UI.btn(t('cf.ai.revert'), 'undo', function () {
      var edited = aiStamp() !== S.ai.after;
      (edited ? UI.confirm(t('cf.ai.revert.q'), t('cf.ai.revert'), true) : Promise.resolve(true)).then(function (ok) {
        if (!ok || !S.ai) return;
        aiRevert(true);
        UI.toast(t('cf.ai.reverted'));
      }).catch(UI.fail);
    }, 'sm');
    return h('div.callout.cf-ai-note', h('div.row.row--between', h('span', UI.icon('chat'), ' ', t('cf.ai.note')), revertB),
      S.ai.summaries.length ? h('ul', { style: { margin: '6px 0 0', paddingLeft: '18px' } }, S.ai.summaries.map(function (x) { return h('li', x); })) : null);
  }
  // paintMarks marks the fields the assistant changed, or the nearest node shown that holds them, and a raw text.
  // paintMarks 标出助手修改的字段（或包含它的最近节点）以及原始文本。
  function paintMarks(el) {
    if (!S.ai) return;
    S.ai.paths.forEach(function (p) {
      while (p) {
        var n = el.querySelector('[data-path="' + (window.CSS && CSS.escape ? CSS.escape(p) : p) + '"]');
        if (n) { n.classList.add('cf-ai'); return; }
        var cut = Math.max(p.lastIndexOf('.'), p.lastIndexOf('['));
        p = cut > 0 ? p.slice(0, cut) : '';
      }
    });
    Array.prototype.forEach.call(el.querySelectorAll('.cf-raw'), function (ta) { ta.classList.add('cf-ai'); });
  }
  // parseText checks a text as a typed one is checked: POST /config/parse, then the page's members only.
  // parseText 像检查手工输入的文本一样检查文本：POST /config/parse，且只能含本页的成员。
  function parseText(fmt, text, members) {
    return call('POST', '/config/parse?format=' + encodeURIComponent(fmt), text, { 'Content-Type': 'text/plain; charset=utf-8' }).then(function (res) {
      return readJSON(res).then(function (body) {
        if (!res.ok) {
          var lines = (body.errors || []).slice(0, 10).map(function (e) { return (e.line ? 'line ' + e.line + ', column ' + (e.column || 0) + ': ' : '') + e.message; });
          throw new Error('the text does not parse as ' + fmt + ': ' + (body.error || res.status) + (lines.length ? '\n- ' + lines.join('\n- ') : ''));
        }
        if (!body || typeof body !== 'object' || Array.isArray(body)) throw new Error('the text is not a config\'s members');
        var extra = extraMember(members, body);
        if (extra) throw new Error('the text sets ' + extra + ', which this page does not edit');
        return body;
      });
    });
  }
  // aiEdit writes page_edit's draft into the page. / 把 page_edit 的草稿写入页面。
  function aiEdit(c, args) {
    var members = membersOf(c);
    var useText = typeof args.text === 'string' && args.text !== '';
    if (!args.summary) return Promise.reject(new Error('say in summary what the draft does'));
    if (useText && RAW_FORMATS.indexOf(args.format) < 0) return Promise.reject(new Error('format is conf, yaml or json'));
    var old, next;
    function onPage(m) {
      if (members.indexOf(m) < 0) throw new Error('this page edits ' + members.join(', ') + ', not ' + m);
    }
    return flushRaw().then(function (bad) {
      if (bad || S.rawText != null) throw new Error('the page holds text that does not parse: the person fixes or discards it before a draft goes in');
      return useText ? parseText(args.format, args.text, members) : null;
    }).then(function (frag) {
      old = S.draft || {};
      next = clone(old);
      if (frag) {
        members.forEach(function (m) {
          if (Object.prototype.hasOwnProperty.call(frag, m)) next[m] = guard(frag[m], old[m], [m], secretName(m));
          else delete next[m];
        });
        return frag;
      }
      var ms = args.members && typeof args.members === 'object' ? args.members : {};
      Object.keys(ms).forEach(function (m) {
        onPage(m);
        if (ms[m] === null) delete next[m];
        else next[m] = guard(ms[m], old[m], [m], secretName(m));
      });
      (args.ops || []).forEach(function (op, i) {
        try {
          var tokens = parsePtr(op.path);
          if (!tokens.length || !tokens[0]) throw new Error('the path names the whole config');
          onPage(tokens[0]);
          if (tokens.some(secretName)) throw new Error(op.path + ' is a secret field, which the assistant never writes');
          if (['add', 'replace', 'remove'].indexOf(op.op) < 0) throw new Error('op is add, replace or remove');
          var v;
          if (op.op !== 'remove') {
            if (op.value === undefined) throw new Error(op.op + ' needs a value');
            v = guard(op.value, op.op === 'replace' ? lookup(next, tokens) : undefined, tokens, false);
          }
          applyOp(next, op.op, tokens, v);
        } catch (e) { throw new Error('ops[' + i + ']: ' + e.message); }
      });
      return null;
    }).then(function (frag) {
      var changedPaths = diffPaths(old, next, '', []);
      if (!changedPaths.length) return { drafted: false, note: 'the draft is what the page holds already: nothing changed' };
      if (!S.ai) S.ai = { before: aiSnapshot(), summaries: [], paths: [] };
      S.draft = next;
      S.ai.summaries.push(String(args.summary).slice(0, 300));
      changedPaths.forEach(function (p) { if (S.ai.paths.indexOf(p) < 0 && S.ai.paths.length < AI_MAX_PATHS) S.ai.paths.push(p); });
      // The raw texts follow the draft; a text drafted as it is stays as written, on its tab, unless it held a
      // [redacted] value, which the draft holds as it was. / 原始文本跟随草稿；按原样起草的文本保留在其标签页（含 [redacted] 时除外）。
      rawStates().forEach(function (r) { if (r.timer) { clearTimeout(r.timer); r.timer = null; } r.loaded = false; r.error = ''; r.details = []; });
      if (frag) {
        var tb = tabOf(c.key), r = rawOf(c.key, args.format);
        tb.fmt = args.format;
        r.members = members;
        if (!redacted(args.text)) { r.text = r.base = args.text; r.loaded = true; r.stamp = fragStamp(members); }
      }
      changed();
      S.ai.after = aiStamp();
      redraw();
      return { drafted: true, summary: args.summary, tab: tabOf(c.key).fmt, changed: changedPaths.slice(0, 50), unsaved: true,
        note: 'written into the page as an unsaved draft marked as yours; the person reviews it and clicks Save and apply, or reverts it' };
    });
  }
  // aiGet is page_get's answer: the page's part of the draft and its state; the assistant's side redacts it.
  // aiGet 为 page_get 的应答：本页在草稿中的部分及其状态；由助手一侧脱敏。
  function aiGet(c) {
    var members = membersOf(c), tb = tabOf(c.key);
    var out = { members: members, tab: tb.fmt, unsaved: isDirty(), assistant_draft: !!S.ai, version: S.version, draft: fragment(members) };
    if (tb.fmt !== 'ui') {
      var r = rawOf(c.key, tb.fmt);
      if (r.error || r.text !== r.base) out.text = { format: tb.fmt, text: r.text, error: r.error || undefined };
    }
    if (S.rawText != null) out.json_page_error = S.rawError || 'the JSON page holds text that does not parse';
    if (S.ai) out.assistant_drafts = S.ai.summaries.slice();
    if (S.errors.length) out.save_errors = S.errors.slice(0, 50);
    return out;
  }
  // editorOf is the editor a page registers with WB.editor. / 页面向 WB.editor 登记的编辑器。
  function editorOf(c) {
    function live() {
      if (S.current !== c || !document.body.contains(c.el) || !S.draft) throw new Error('the page that was asked is not shown any more: read the page shown with page_get');
    }
    return {
      describe: function () {
        return { members: membersOf(c), formats: RAW_FORMATS.slice(), tab: tabOf(c.key).fmt, unsaved: isDirty(), assistant_draft: !!S.ai };
      },
      call: function (name, args) {
        return Promise.resolve().then(function () {
          live();
          switch (name) {
            case 'page_get': return aiGet(c);
            case 'page_edit': return aiEdit(c, args || {});
            case 'page_revert': return aiRevert(false);
          }
          throw new Error('the page has no tool ' + name);
        });
      }
    };
  }

  // ---- the schema / schema ----
  // doc is a description of the schema in the page's language: the schema's are English, and schema-zh.js holds
  // their Chinese, by the English text (a test in app/workbench keeps it whole).
  // doc 返回 schema 描述在页面语言下的文本：schema 中为英文，schema-zh.js 按英文原文保存其中文（app/workbench 中的测试保证其完整）。
  var RENAMED = /^Renamed (\S+); the old name goes in config version 2\.$/;
  function doc(text) {
    if (!text || !UI.zh()) return text;
    var zh = window.FitSchemaZh && window.FitSchemaZh[text];
    if (zh) return zh;
    var m = RENAMED.exec(text);
    return m ? '已更名为 ' + m[1] + '；旧名称将在配置版本 2 中移除。' : text;
  }
  function resolve(s) {
    var seen = 0;
    while (s && s.$ref && seen++ < 32) {
      var name = decodeURIComponent(s.$ref.replace(/^#\/\$defs\//, '')).replace(/~1/g, '/').replace(/~0/g, '~');
      var def = S.schema.$defs[name] || {};
      var o = {};
      Object.keys(def).forEach(function (k) { o[k] = def[k]; });
      o.description = s.description || def.description;
      o.default = s.default !== undefined ? s.default : def.default;
      s = o;
    }
    return s || {};
  }
  function rootProps() {
    var props = {}, all = (S.schema && S.schema.properties) || {};
    Object.keys(all).forEach(function (k) { if (k !== 'admin') props[k] = all[k]; });
    return props;
  }
  function isModuleRef(s) {
    return s.type === 'object' && s.properties && s.properties.module && Array.isArray(s.properties.module.enum) && s.properties.params;
  }
  function paramsSchema(s, module) {
    var all = s.allOf || [];
    for (var i = 0; i < all.length; i++) {
      var c = all[i];
      if (c.if && c.if.properties && c.if.properties.module && c.if.properties.module.const === module) return resolve(c.then.properties.params);
    }
    return null;
  }
  function kind(s) {
    if (isModuleRef(s)) return 'module';
    if (s.oneOf) return 'scalar';
    if (s.type === 'array') return 'array';
    if (s.type === 'object' && s.properties) return 'object';
    if (s.type === 'object' && s.additionalProperties && typeof s.additionalProperties === 'object') return 'map';
    if (['string', 'integer', 'number', 'boolean'].indexOf(s.type) >= 0) return s.type;
    return 'json';
  }
  function nestedKind(k) { return k === 'object' || k === 'array' || k === 'map' || k === 'module'; }

  // A slot is a place in the draft: its container's slot and its key there. / 槽：草稿中的一个位置。
  function rootSlot() { return { path: '', get: function () { return S.draft; }, set: function (v) { S.draft = v || {}; } }; }
  function child(parent, key, path) {
    return {
      path: path,
      get: function () { var c = parent.get(); return c == null ? undefined : c[key]; },
      set: function (v) {
        var c = parent.get();
        if (c == null) {
          if (v === undefined) return;
          c = typeof key === 'number' ? [] : {};
          parent.set(c);
        }
        if (v === undefined) { if (Array.isArray(c)) c.splice(key, 1); else delete c[key]; } else c[key] = v;
      }
    };
  }
  // join builds an error path as fit's config errors write it: a.b[0]["key"]. / 按 fit 配置错误的格式拼接路径。
  function join(path, key, isKey) {
    if (typeof key === 'number') return path + '[' + key + ']';
    if (isKey) return path + '[' + JSON.stringify(key) + ']';
    return path ? path + '.' + key : key;
  }
  function hasError(path) {
    return S.errors.some(function (e) {
      var p = e.path || '';
      return p === path || p.indexOf(path + '.') === 0 || p.indexOf(path + '[') === 0;
    });
  }

  function emptyOf(s) {
    s = resolve(s);
    switch (kind(s)) {
      case 'object': {
        var o = {};
        (s.required || []).forEach(function (r) {
          var p = resolve(s.properties[r]), k = kind(p);
          if (k === 'array') o[r] = [];
          else if (k === 'object' || k === 'map') o[r] = {};
          else if (k === 'module') o[r] = { module: p.properties.module.enum[0] };
          else if (p.enum) o[r] = p.enum[0];
          else if (k === 'string') o[r] = '';
        });
        return o;
      }
      case 'module': return { module: s.properties.module.enum[0] };
      case 'array': return [];
      case 'map': return {};
      case 'boolean': return false;
      case 'integer': case 'number': return 0;
      default: return s.enum ? s.enum[0] : '';
    }
  }

  function summary(v) {
    if (v === undefined) return t('cf.unset');
    if (Array.isArray(v)) return v.length === 1 ? t('cf.item') : t('cf.items', { n: v.length });
    if (v && typeof v === 'object') {
      if (typeof v.module === 'string') return v.module;
      var n = Object.keys(v).length;
      return n === 1 ? t('cf.field') : t('cf.fields', { n: n });
    }
    return '';
  }
  function itemLabel(v, i) {
    if (v && typeof v === 'object') {
      var ks = ['name', 'module', 'address', 'username', 'path', 'id'];
      for (var k = 0; k < ks.length; k++) if (typeof v[ks[k]] === 'string' && v[ks[k]]) return '#' + i + ' ' + v[ks[k]];
    }
    if (typeof v === 'string' || typeof v === 'number') return '#' + i + ' ' + v;
    return '#' + i;
  }

  // ---- controls / 控件 ----
  // control returns the control of a value of schema s at slot; rerender draws the page again, after a change of
  // shape. / 返回 slot 处 schema 为 s 的值的控件；rerender 在结构变化后重绘页面。
  function control(s, slot, top) {
    s = resolve(s);
    switch (kind(s)) {
      case 'object': return objectControl(s, slot, top);
      case 'array': return arrayControl(s, slot);
      case 'map': return mapControl(s, slot);
      case 'module': return moduleControl(s, slot);
      case 'boolean': return boolControl(s, slot);
      case 'json': return jsonControl(s, slot);
      default: return scalarControl(s, slot);
    }
  }

  function smallBtn(label, iconName, fn, kind2, title) {
    var b = UI.btn(label, iconName, function (ev) { ev.preventDefault(); ev.stopPropagation(); fn(); }, 'sm' + (kind2 ? ' ' + kind2 : ''));
    if (title) { b.title = title; b.setAttribute('aria-label', title); }
    return b;
  }

  // incBox is the include checkbox of an optional (non-boolean) field: checked when its value is set. Checking sets an
  // empty or default value and unchecking deletes it, so an optional member is left out of the config as a whole.
  // incBox 是可选的（非布尔）字段的“是否设置”复选框：有值时勾选；勾选写入空/默认值，取消勾选删除该值，
  // 于是可选的成员可以整体不写入配置。
  function incBox(name, s, slot, inSummary) {
    var box = h('input.cf-inc', { type: 'checkbox', checked: slot.get() !== undefined, 'aria-label': t('cf.inc', { name: name }) });
    box._slot = slot;
    if (!inSummary) box.addEventListener('change', function () { setInc(s, slot, box.checked); });
    return box;
  }
  function setInc(s, slot, on) {
    if (on) { slot.set(emptyOf(s)); S.open[slot.path] = true; } else slot.set(undefined);
    changed(); redraw();
  }

  function objectControl(s, slot, top) {
    var v = slot.get();
    if (v === undefined && !top) {
      return h('div.row', h('span.muted.small', t('cf.unset')), smallBtn(t('cf.set'), 'plus', function () { slot.set({}); S.open[slot.path] = true; changed(); redraw(); }));
    }
    var box = h('div.cf-form');
    var required = {};
    (s.required || []).forEach(function (r) { required[r] = true; });
    Object.keys(s.properties).forEach(function (name) {
      var p = resolve(s.properties[name]);
      if (p.deprecated && !(v && name in v)) return;
      box.appendChild(field(name, p, child(slot, name, join(slot.path, name)), required[name]));
    });
    if (!top && v !== undefined) {
      box.appendChild(h('div.btn-row', { style: { marginTop: '8px' } },
        smallBtn(t('cf.leaveout'), 'x', function () { slot.set(undefined); changed(); redraw(); }, 'ghost')));
    }
    return box;
  }

  // node is a collapsible box: a nested value, or an item of a list. inc (a nested optional field) puts its include
  // checkbox in the summary; clicking the checkbox must not toggle the box, clicking the rest of the summary does.
  // node 是可折叠框：嵌套值或列表项。inc 为嵌套的可选字段，其“是否设置”复选框放在 summary 中；
  // 点击复选框不展开/收起该框，点击 summary 的其余部分照旧切换。
  function node(path, head, body, defOpen, cls, inc) {
    var err = hasError(path);
    var d = h('details.cf-node' + (cls ? '.' + cls : '') + (err ? '.is-error' : ''), { 'data-path': path });
    d.open = err || (S.open[path] != null ? S.open[path] : !!defOpen);
    d.addEventListener('toggle', function () { S.open[path] = d.open; });
    var sum = h('summary', h('span.cf-chev', UI.icon('chev')), head);
    if (inc) {
      var box = incBox(inc.name, inc.s, inc.slot, true);
      sum.insertBefore(box, sum.firstChild);
      sum.addEventListener('click', function (ev) {
        if (ev.target !== box) return;
        // The checkbox's own click already toggles it before this handler runs: take its new state, and cancel the
        // summary's toggle. / 复选框的点击在处理器运行前已切换其状态：取新状态，并取消 summary 的展开/收起。
        ev.preventDefault();
        setInc(inc.s, inc.slot, box.checked);
      });
    }
    d.appendChild(sum);
    d.appendChild(h('div.cf-body', body));
    return d;
  }

  function field(name, s, slot, required) {
    var k = kind(s);
    var err = hasError(slot.path);
    var optional = !required && k !== 'boolean';
    var nameEl = h('span.cf-label', name, required ? h('span.cf-req', '*') : null, s.deprecated ? h('span.badge.badge--warn', { style: { marginLeft: '6px' } }, t('cf.deprecated')) : null);
    if (nestedKind(k)) {
      var v = slot.get();
      return node(slot.path, [nameEl, h('span.muted.small', summary(v))],
        [s.description ? h('div.cf-doc', doc(s.description)) : null, control(s, slot)], v !== undefined && k !== 'object',
        null, optional ? { s: s, slot: slot, name: name } : null);
    }
    return h('div.cf-field' + (err ? '.is-error' : ''), { 'data-path': slot.path },
      h('div.cf-name', optional ? [incBox(name, s, slot), nameEl] : nameEl),
      h('div.cf-ctl', control(s, slot)), s.description ? h('div.cf-doc', doc(s.description)) : null);
  }

  function swapOpen(prefix, i, j) {
    var a = prefix + '[' + i + ']', b = prefix + '[' + j + ']', moved = {};
    Object.keys(S.open).forEach(function (k) {
      if (k === a || k.indexOf(a + '.') === 0 || k.indexOf(a + '[') === 0) moved[b + k.slice(a.length)] = S.open[k];
      else if (k === b || k.indexOf(b + '.') === 0 || k.indexOf(b + '[') === 0) moved[a + k.slice(b.length)] = S.open[k];
      else return;
      delete S.open[k];
    });
    Object.keys(moved).forEach(function (k) { S.open[k] = moved[k]; });
  }
  function dropOpen(prefix) {
    Object.keys(S.open).forEach(function (k) { if (k.indexOf(prefix + '[') === 0) delete S.open[k]; });
  }

  function itemButtons(slot, idx, i, n) {
    // idx: the indexes shown, in the array; i: this one's place among them. / idx：显示的项在数组中的下标；i：本项的位置。
    return [
      smallBtn('', 'chev', function () { move(slot, idx[i], idx[i - 1]); }, 'ghost icon', t('cf.up')),
      smallBtn('', 'chev', function () { move(slot, idx[i], idx[i + 1]); }, 'ghost icon', t('cf.down')),
      smallBtn('', 'trash', function () { removeAt(slot, idx[i]); }, 'ghost icon', t('cf.remove'))
    ].map(function (b, k) {
      if (k < 2) b.classList.add(k ? 'cf-down' : 'cf-up');
      if ((k === 0 && i === 0) || (k === 1 && i === n - 1)) b.disabled = true;
      return b;
    });
  }
  function move(slot, a, b) {
    var arr = slot.get();
    var x = arr[a]; arr[a] = arr[b]; arr[b] = x;
    swapOpen(slot.path, a, b);
    changed(); redraw();
  }
  function removeAt(slot, i) {
    var arr = slot.get();
    arr.splice(i, 1);
    dropOpen(slot.path);
    if (arr.length === 0) slot.set(undefined);
    changed(); redraw();
  }

  // arrayControl; opts (a top-level list): {filter, make, chips, addLabel, empty}. / 数组控件；opts 用于顶层列表。
  function arrayControl(s, slot, opts) {
    opts = opts || {};
    var items = resolve(s.items || {});
    var ik = kind(items);
    var simple = !nestedKind(ik) && ik !== 'json';
    var v = slot.get() || [];
    var idx = [];
    v.forEach(function (item, i) { if (!opts.filter || opts.filter(item, i)) idx.push(i); });
    var list = h('div.cf-list');
    idx.forEach(function (i, k) {
      var cs = child(slot, i, join(slot.path, i));
      var buttons = itemButtons(slot, idx, k, idx.length);
      if (simple) {
        list.appendChild(h('div.cf-row' + (hasError(cs.path) ? '.is-error' : ''), { 'data-path': cs.path }, control(items, cs), buttons));
        return;
      }
      var head = [h('span.cf-label', itemLabel(v[i], i)), opts.chips ? h('span.cf-chips', opts.chips(v[i])) : null, h('span.cf-btns', buttons)];
      list.appendChild(node(cs.path, head, control(items, cs, true), idx.length === 1 && !opts.top, opts.top ? 'cf-item' : ''));
    });
    if (opts.top && !idx.length) list.appendChild(UI.empty(opts.empty || t('cf.none')));
    list.appendChild(h('div.btn-row', { style: { marginTop: '8px' } }, smallBtn(opts.addLabel || t('cf.add'), 'plus', function () {
      var arr = slot.get() || [];
      arr.push(opts.make ? opts.make(arr) : emptyOf(items));
      slot.set(arr);
      var p = join(slot.path, arr.length - 1);
      S.open[p] = true;
      S.focus = p;
      changed(); redraw();
    })));
    return list;
  }

  function mapControl(s, slot) {
    var vs = resolve(s.additionalProperties);
    var box = h('div.cf-list');
    var v = slot.get() || {};
    var simple = !nestedKind(kind(vs)) && kind(vs) !== 'json';
    Object.keys(v).forEach(function (key) {
      var keyInput = h('input.input.input--sm.cf-key', { value: key, 'aria-label': t('cf.name') });
      keyInput.addEventListener('change', function () {
        var nk = keyInput.value;
        if (!nk || nk === key || nk in v) { keyInput.value = key; return; }
        var o = {};
        Object.keys(v).forEach(function (k) { o[k === key ? nk : k] = v[k]; });
        slot.set(o);
        changed(); redraw();
      });
      var cs = child(slot, key, join(slot.path, key, true));
      var rm = smallBtn('', 'trash', function () {
        var o = slot.get();
        delete o[key];
        if (Object.keys(o).length === 0) slot.set(undefined);
        changed(); redraw();
      }, 'ghost icon', t('cf.remove'));
      if (simple) box.appendChild(h('div.cf-row' + (hasError(cs.path) ? '.is-error' : ''), { 'data-path': cs.path }, keyInput, control(vs, cs), rm));
      else box.appendChild(h('div.cf-mapitem' + (hasError(cs.path) ? '.is-error' : ''), { 'data-path': cs.path }, h('div.cf-row', keyInput, h('span.cf-spacer'), rm), control(vs, cs, true)));
    });
    box.appendChild(h('div.btn-row', { style: { marginTop: '8px' } }, smallBtn(t('cf.add'), 'plus', function () {
      var o = slot.get() || {};
      var k = 'name', n = 1;
      while (k in o) k = 'name' + ++n;
      o[k] = emptyOf(vs);
      slot.set(o);
      changed(); redraw();
    })));
    return box;
  }

  function moduleControl(s, slot) {
    var v = slot.get();
    var box = h('div');
    var sel = h('select.select.select--sm', { 'aria-label': 'module', style: { maxWidth: '320px' } }, h('option', { value: '' }, t('cf.leftout')),
      s.properties.module.enum.map(function (m) { return h('option', { value: m, selected: v && v.module === m }, m); }));
    sel.addEventListener('change', function () {
      slot.set(sel.value ? { module: sel.value } : undefined);
      changed(); redraw();
    });
    var mpath = join(slot.path, 'module');
    box.appendChild(h('div.cf-field' + (hasError(mpath) ? '.is-error' : ''), { 'data-path': mpath },
      h('div.cf-name', h('span.cf-label', t('cf.module'))), h('div.cf-ctl', sel),
      s.properties.module.description ? h('div.cf-doc', doc(s.properties.module.description)) : null));
    if (v && v.module) {
      var ps = paramsSchema(s, v.module);
      var pslot = child(slot, 'params', join(slot.path, 'params'));
      if (ps && ps.properties && Object.keys(ps.properties).length > 0) {
        box.appendChild(h('div.cf-params', { 'data-path': pslot.path }, h('div.muted.small', t('cf.params')), ps.description ? h('div.cf-doc', doc(ps.description)) : null, objectControl(ps, pslot, true)));
      } else if (!ps) {
        box.appendChild(h('div.cf-field', { 'data-path': pslot.path }, h('div.cf-name', h('span.cf-label', t('cf.params'))), h('div.cf-ctl', jsonControl({}, pslot))));
      }
    }
    return box;
  }

  function boolControl(s, slot) {
    var c = h('input', { type: 'checkbox', checked: slot.get() === true });
    c.addEventListener('change', function () {
      // Left out when it is the default. / 等于默认值时不设置。
      slot.set(c.checked ? (s.default === true ? undefined : true) : (s.default === true ? false : undefined));
      changed();
    });
    if (s.default === true && slot.get() === undefined) c.checked = true;
    return h('label.check', c, h('span.muted.small', s.default !== undefined ? t('cf.default', { v: String(s.default) }) : ''));
  }

  function scalarControl(s, slot) {
    var v = slot.get();
    var ph = s.default !== undefined ? String(typeof s.default === 'object' ? JSON.stringify(s.default) : s.default) : '';
    if (s.enum) {
      var sel = h('select.select.select--sm', h('option', { value: '' }, ph ? t('cf.leftout.d', { v: ph }) : t('cf.leftout')),
        s.enum.map(function (e) { return h('option', { value: String(e), selected: v === e }, String(e)); }));
      sel.addEventListener('change', function () {
        var e = s.enum.filter(function (x) { return String(x) === sel.value; })[0];
        slot.set(sel.value === '' ? undefined : e);
        changed();
      });
      return sel;
    }
    var numeric = s.type === 'integer' || s.type === 'number';
    var input = h('input.input.input--sm', {
      type: numeric ? 'number' : 'text',
      value: v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v),
      placeholder: ph,
      step: s.type === 'integer' ? '1' : numeric ? 'any' : null,
      min: s.minimum, max: s.maximum,
      pattern: s.pattern && !numeric ? s.pattern.replace(/^\^/, '').replace(/\$$/, '') : null,
      spellcheck: 'false',
      'data-fill': ph ? true : null
    });
    input.addEventListener('input', function () {
      var x = input.value, nv;
      if (x === '') nv = undefined;
      else if (numeric) nv = Number(x);
      else if (s.oneOf && /^-?\d+$/.test(x) && s.oneOf.some(function (o) { return o.type === 'integer'; })) nv = Number(x);
      else nv = x;
      slot.set(nv);
      changed();
    });
    return input;
  }

  function jsonControl(s, slot) {
    var v = slot.get();
    var ta = h('textarea.textarea.textarea--code', { spellcheck: 'false', placeholder: t('cf.json.ph'), rows: 4 });
    ta.value = v === undefined ? '' : JSON.stringify(v, null, 2);
    ta.addEventListener('input', function () {
      if (ta.value.trim() === '') { slot.set(undefined); ta.setCustomValidity(''); changed(); return; }
      try { slot.set(JSON.parse(ta.value)); ta.setCustomValidity(''); changed(); } catch (e) { ta.setCustomValidity(e.message); }
    });
    return ta;
  }

  // section returns the form of a top-level member. / 返回顶层成员的表单。
  function section(name, opts) {
    opts = opts || {};
    var s = resolve(rootProps()[name] || {});
    var slot = child(rootSlot(), name, name);
    var body;
    if (kind(s) === 'array') {
      var o = {};
      Object.keys(opts).forEach(function (k) { o[k] = opts[k]; });
      o.top = true;
      body = h('div', { 'data-path': name }, arrayControl(s, slot, o));
    } else {
      body = h('section.card', { 'data-path': name }, h('div.card__body', control(s, slot, true)));
    }
    return h('div.stack', opts.noDoc || !s.description ? null : h('div.callout', UI.icon('info'), ' ', doc(s.description)), body);
  }

  // certTable lists the certificates of /status, for the overview and the TLS pages. / 列出 /status 中的证书，供概览与 TLS 页面使用。
  function certTable(UI, list) {
    return UI.table([
      { label: t('cf.c.listener'), render: function (c) { return h('b', c.listener); } },
      { label: t('cf.c.names'), render: function (c) { return (c.names || []).map(function (n) { return UI.badge(n); }); } },
      { label: t('cf.c.issuer'), render: function (c) { return h('span.small', c.issuer); } },
      { label: t('cf.c.from'), render: function (c) { return h('span.small', UI.fmtTime(Date.parse(c.not_before) / 1000)); } },
      { label: t('cf.c.until'), render: function (c) {
        var left = (Date.parse(c.not_after) - Date.now()) / 86400e3;
        return h('span.small', UI.fmtTime(Date.parse(c.not_after) / 1000), ' ',
          UI.badge(left < 0 ? t('cf.c.expired') : t('cf.c.left', { n: Math.floor(left) }), left < 0 ? 'danger' : left < 14 ? 'warn' : 'ok'));
      } },
      { label: t('cf.c.serial'), render: function (c) { return h('code.small', c.serial); } }
    ], list, { empty: t('cf.c.none') });
  }

  window.FitConfig = {
    with: function (ui) {
      if (!S.ui) S.ui = ui;
      return api;
    },
    certTable: certTable
  };
  var api = {
    ensure: function () { return ensure(); },
    page: page, section: section, redraw: redraw, changed: changed, save: save, discard: discard, reload: reload, rollback: rollback,
    place: function (fn) { S.places.push(fn); },
    // exportDialog(members, fmt) and importDialog(members): the whole config without members. / 无 members 时为整个配置。
    exportDialog: function (members, fmt) { exportDialog(members, fmt); }, importDialog: function (members) { importDialog(members); },
    sections: function () { return Object.keys(rootProps()); },
    describe: function (name) { return doc(resolve(rootProps()[name] || {}).description || ''); },
    resolve: function (s) { return resolve(s); },
    schemaDef: function (name) { return S.schema && S.schema.$defs[name]; },
    get draft() { return S.draft; },
    set draft(v) { S.draft = v; },
    get version() { return S.version; },
    get focus() { return S.focus; },
    get etag() { return S.etag; },
    get dirty() { return isDirty(); },
    // the JSON page's text that does not parse (null when it does). / JSON 页面中无法解析的文本（可解析时为 null）。
    get rawText() { return S.rawText; },
    setRaw: function (text, err) { S.rawText = err ? text : null; S.rawError = err || ''; if (!err) S.errors = S.errors.filter(function (e) { return e.path; }); setDirty(S.dirty); },
    notice: function () { return paintNotice(); },
    errors: function () { return S.errors; }
  };
})();
