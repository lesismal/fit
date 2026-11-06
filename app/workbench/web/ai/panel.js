// The assistant's chat panel: a button in the top bar of every page, while the console serves the module ai and the
// user's level is not off, opens a panel beside the page. A message goes to POST /api/chat with the page shown (WB.page)
// and the text selected on it as context, and the answer streams back as SSE events (text, thinking, tool_call,
// tool_result, ask, usage, summary, then end or error): the text is drawn as Markdown as it comes, each tool call is a
// folded row with its arguments and its result, an ask_user question is a row of buttons whose choice goes back as the
// call's result. Stop ends the answer (POST /api/chat/stop), retry answers the last message again, and the user's
// conversations (GET /api/conversations, GET and DELETE /api/conversations/{id}) are picked, started anew or deleted in
// the panel's head. Markdown is built as DOM nodes, never as HTML, so nothing a model writes runs on the page. A
// conversation's pending proposal (the propose tools' steps, I126) is shown above the composer, with Apply, which asks
// again in a dialog with the whole diff, and Discard (ai/proposals.js). A skill (I127) is picked with the Skills button or
// by typing /name at the start of the message, which shows the enabled skills that match: the chip above the composer
// says which the conversation runs, its × stops it, and the message goes with "skill" when the chip changed it. A skill
// the assistant loads itself (skill_load) comes as the event skill and sets the chip. FitAI.useSkill(name, {message,
// inputs, send}) opens the panel on a new conversation with a skill picked, and with send sends the message at once,
// for the pages' Ask AI buttons (I128, ai/ask.js), which the panel shows or hides with the user's level. The page's
// editor, when it has one (WB.editor), goes with the page as editable; a page tool's call (I154) comes as the event
// page_call, is run on the page shown (WB.pageTool), which drafts into its editor and forms and never saves, and its
// answer goes back with POST /api/chat/page as the call's result.
//
// 助手的对话面板：控制台提供 ai 模块且用户级别不是 off 时，每个页面的顶栏都有一个按钮，点开后面板显示在页面旁。消息连同当前
// 页面（WB.page）与页面上选中的文字作为上下文发往 POST /api/chat，回答以 SSE 事件流回（text、thinking、tool_call、
// tool_result、ask、usage、summary，最后为 end 或 error）：文字按 Markdown 随到随画，每次工具调用为一行可展开的记录（参数与
// 结果），ask_user 的问题显示为一排按钮，所选即该调用的结果。停止会结束回答（POST /api/chat/stop），重试会重新回答最后一条
// 消息；用户的对话（GET /api/conversations、GET 与 DELETE /api/conversations/{id}）在面板头部选择、新建或删除。Markdown
// 以 DOM 节点构建，从不作为 HTML 插入，模型写的任何内容都不会在页面上执行。对话的待处理提案（提案工具的各步骤，I126）显示在
// 输入框上方，带“应用”（会再弹出含完整差异的对话框确认）与“丢弃”（ai/proposals.js）。技能（I127）可用“技能”按钮选择，或在
// 消息开头输入 /name，面板会列出匹配的已启用技能：输入框上方的标签显示对话运行的技能，点 × 停止使用；标签有变化时消息带上 "skill"。
// 助手自行加载的技能（skill_load）以 skill 事件到达并更新标签。FitAI.useSkill(name, {message, inputs, send}) 以选好的技能打开面板并
// 新建对话，带 send 时立即发送消息，供各页面的“问 AI”按钮使用（I128，ai/ask.js），面板按用户级别显示或隐藏这些按钮。页面有编辑器时（WB.editor），其声明作为 editable 随页面发送；页面工具的调用
// （I154）以 page_call 事件到达，在当前页面上执行（WB.pageTool）：只起草到页面的编辑器与表单、从不保存，其应答经
// POST /api/chat/page 作为该调用的结果返回。
(function () {
  'use strict';

  UI.addI18n({
    'en': {
      'p.open': 'Assistant', 'p.close': 'Close the assistant', 'p.new': 'New conversation', 'p.delete': 'Delete this conversation',
      'p.deleteQ': 'Delete the conversation “{title}”?', 'p.pick': 'Conversations', 'p.untitled': 'New conversation',
      'p.placeholder': 'Ask about this fit: a route, an upstream, a log error…  (Enter sends, Shift+Enter a new line)',
      'p.send': 'Send', 'p.stop': 'Stop', 'p.retry': 'Retry', 'p.page': 'Send this page as context: {title}',
      'p.pageNone': 'Send this page as context', 'p.today': '{n} tokens today', 'p.model': 'Model: {model}',
      'p.noModel': 'No model is set: add a provider and a model on the Settings page.', 'p.off': 'The assistant is off for you.',
      'p.noTools': 'This model calls no tools: it answers without reading this fit.',
      'p.empty': 'Ask about what this fit runs: the assistant reads the config, its status, metrics and logs through read-only tools, and never changes anything itself.',
      'p.thinking': 'Thinking', 'p.args': 'Arguments', 'p.result': 'Result', 'p.running': 'running…', 'p.waiting': 'waiting for your choice', 'p.failed': 'failed',
      'p.cut': 'cut to its bound', 'p.stopped': 'Stopped.', 'p.summary': 'The earlier turns are summarized for the model from here on.',
      'p.summarized': 'The turns above are summarized for the model.', 'p.chose': 'Chose: {choice}', 'p.limit.max_tool_calls': 'The answer reached its limit of tool calls.',
      'p.limit.max_tokens_per_day': 'Today\'s tokens are spent.', 'p.kept': 'Conversations are kept in the store, encrypted.',
      'p.notKept': 'Without admin.store, conversations last until fit restarts.', 'p.copy': 'Copy', 'p.you': 'You',
      'p.step': 'Added to the proposal — {tool}: {summary}',
      'p.skills': 'Skills', 'p.skillsHint': 'Pick a skill, or type /name at the start of a message', 'p.noSkill': 'No skill: the assistant picks one when it fits',
      'p.clearSkill': 'Stop using this skill', 'p.skillLoaded': 'The assistant loaded the skill {skill}.', 'p.noMatch': 'No enabled skill matches.',
      'p.onPage': 'on the page…', 'p.drafted': 'Drafted on the page, not saved: review it there, then Save and apply — or revert it.',
      'p.reverted': 'The assistant\'s draft was taken off the page.',
      'p.skillOff': 'The skill {skill} is not enabled: the assistant answers without it.'
    },
    'zh-CN': {
      'p.open': '助手', 'p.close': '关闭助手', 'p.new': '新对话', 'p.delete': '删除此对话',
      'p.deleteQ': '删除对话“{title}”？', 'p.pick': '对话', 'p.untitled': '新对话',
      'p.placeholder': '询问本 fit 的情况：某个路由、上游、日志错误……（Enter 发送，Shift+Enter 换行）',
      'p.send': '发送', 'p.stop': '停止', 'p.retry': '重试', 'p.page': '将本页作为上下文发送：{title}',
      'p.pageNone': '将本页作为上下文发送', 'p.today': '今日已用 {n} 个 token', 'p.model': '模型：{model}',
      'p.noModel': '尚未设置模型：请在设置页添加提供方与模型。', 'p.off': '你的助手级别为关闭。',
      'p.noTools': '此模型不调用工具：它不读取本 fit 的情况就回答。',
      'p.empty': '询问本 fit 运行的情况：助手通过只读工具读取配置、状态、指标与日志，自己从不做任何修改。',
      'p.thinking': '思考', 'p.args': '参数', 'p.result': '结果', 'p.running': '运行中…', 'p.waiting': '等待你的选择', 'p.failed': '失败',
      'p.cut': '已截断至上限', 'p.stopped': '已停止。', 'p.summary': '从这里起，之前的轮次以摘要提供给模型。',
      'p.summarized': '以上轮次以摘要提供给模型。', 'p.chose': '已选择：{choice}', 'p.limit.max_tool_calls': '本次回答已达到工具调用次数上限。',
      'p.limit.max_tokens_per_day': '今日的 token 已用完。', 'p.kept': '对话加密保存在 store 中。',
      'p.notKept': '未配置 admin.store：对话只保留到 fit 重启。', 'p.copy': '复制', 'p.you': '你',
      'p.step': '已加入提案 — {tool}：{summary}',
      'p.skills': '技能', 'p.skillsHint': '选择一个技能，或在消息开头输入 /name', 'p.noSkill': '未选技能：合适时助手会自行选择',
      'p.clearSkill': '停止使用此技能', 'p.skillLoaded': '助手加载了技能 {skill}。', 'p.noMatch': '没有匹配的已启用技能。',
      'p.onPage': '在页面上执行…', 'p.drafted': '已起草到页面上，尚未保存：请在页面上检查，然后“保存并应用”，或撤回。',
      'p.reverted': '已从页面上撤回助手的草稿。',
      'p.skillOff': '技能 {skill} 未启用：助手将不使用它来回答。'
    }
  }, 'ai');

  var h = UI.h;

  // ---- Markdown, as DOM nodes / Markdown（构建为 DOM 节点）----
  // safeHref allows links to the web and to the workbench's own pages only. / 只允许指向网页与工作台自身页面的链接。
  function safeHref(u) { return /^(https?:\/\/|#\/)/i.test(u) ? u : null; }
  // inline draws `code`, **bold**, *italic* and [links](url). / 绘制行内代码、粗体、斜体与链接。
  function inline(s) {
    var out = [], re = /`([^`]+)`|\*\*([^*]+)\*\*|\*([^*\s][^*]*)\*|\[([^\]]+)\]\(([^)\s]+)\)/g, last = 0, m;
    while ((m = re.exec(s))) {
      if (m.index > last) out.push(s.slice(last, m.index));
      if (m[1] != null) out.push(h('code', m[1]));
      else if (m[2] != null) out.push(h('strong', m[2]));
      else if (m[3] != null) out.push(h('em', m[3]));
      else {
        var href = safeHref(m[5]);
        out.push(href ? h('a', { href: href, target: href[0] === '#' ? null : '_blank', rel: 'noopener noreferrer' }, m[4]) : m[0]);
      }
      last = re.lastIndex;
    }
    if (last < s.length) out.push(s.slice(last));
    return out;
  }
  function codeBlock(UI, text, lang) {
    return h('div.ai-code', h('div.ai-code__bar', h('span', lang || 'text'),
      h('button.btn.btn--ghost.btn--sm', { type: 'button', onclick: function () { UI.copy(text); } }, UI.icon('copy'), UI.t('p.copy'))),
      h('pre', h('code', text)));
  }
  var LIST = /^\s*([-*+]|\d+[.)])\s+/;
  function cells(line) { return line.trim().replace(/^\||\|$/g, '').split('|').map(function (c) { return c.trim(); }); }
  // markdown draws text: fenced code (an unclosed fence, as while it streams, runs to the end), headings, lists, quotes,
  // tables, rules and paragraphs. / 绘制 Markdown：代码块（流式输出时未闭合的代码块延续到末尾）、标题、列表、引用、表格、
  // 分隔线与段落。
  function markdown(UI, text) {
    var root = h('div.ai-md'), lines = String(text || '').replace(/\r/g, '').split('\n'), i = 0, para = [];
    function flush() {
      if (!para.length) return;
      var p = h('p');
      para.forEach(function (l, k) { if (k) p.appendChild(h('br')); inline(l).forEach(function (n) { p.append(n); }); });
      root.appendChild(p);
      para = [];
    }
    while (i < lines.length) {
      var l = lines[i], fence = l.match(/^\s*(```|~~~)\s*([\w.+-]*)\s*$/), hd;
      if (fence) {
        flush();
        var code = [];
        for (i++; i < lines.length && !/^\s*(```|~~~)\s*$/.test(lines[i]); i++) code.push(lines[i]);
        i++;
        root.appendChild(codeBlock(UI, code.join('\n'), fence[2]));
      } else if ((hd = l.match(/^(#{1,6})\s+(.*)$/))) {
        flush();
        root.appendChild(h('div.ai-h.ai-h' + Math.min(hd[1].length, 4), inline(hd[2])));
        i++;
      } else if (LIST.test(l)) {
        flush();
        var list = h(/^\s*\d/.test(l) ? 'ol' : 'ul');
        // An item's indented lines that are not items themselves continue it; items indented more than the first are a
        // list inside the item before them. / 条目后缩进且本身不是条目的行是它的延续；比第一项缩进更多的条目构成前一项内的子列表。
        var base = l.match(/^\s*/)[0].length, last = null, sub = null;
        while (i < lines.length && LIST.test(lines[i])) {
          var line = lines[i], ind = line.match(/^\s*/)[0].length, item = line.replace(LIST, '');
          for (i++; i < lines.length && /^\s+\S/.test(lines[i]) && !LIST.test(lines[i]); i++) item += ' ' + lines[i].trim();
          if (ind > base && last) {
            if (!sub) { sub = h(/^\s*\d/.test(line) ? 'ol' : 'ul'); last.appendChild(sub); }
            sub.appendChild(h('li', inline(item)));
          } else {
            last = h('li', inline(item));
            sub = null;
            list.appendChild(last);
          }
        }
        root.appendChild(list);
      } else if (/^\s*>/.test(l)) {
        flush();
        var q = [];
        for (; i < lines.length && /^\s*>/.test(lines[i]); i++) q.push(lines[i].replace(/^\s*>\s?/, ''));
        root.appendChild(h('blockquote', markdown(UI, q.join('\n'))));
      } else if (/^\s*\|.*\|\s*$/.test(l) && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1] || '')) {
        flush();
        var head = cells(l), rows = [];
        for (i += 2; i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i]); i++) rows.push(cells(lines[i]));
        root.appendChild(h('div.table-wrap', h('table.table', h('thead', h('tr', head.map(function (c) { return h('th', inline(c)); }))),
          h('tbody', rows.map(function (r) { return h('tr', r.map(function (c) { return h('td', inline(c)); })); })))));
      } else if (/^\s*(-{3,}|\*{3,})\s*$/.test(l)) {
        flush();
        root.appendChild(h('hr'));
        i++;
      } else if (!l.trim()) {
        flush();
        i++;
      } else {
        para.push(l);
        i++;
      }
    }
    flush();
    return root;
  }

  // pretty shows a tool's arguments or result: JSON indented, anything else as it is. / 美化工具的参数或结果。
  function pretty(s) {
    if (typeof s !== 'string') s = JSON.stringify(s);
    try { return JSON.stringify(JSON.parse(s), null, 2); } catch (e) { return s; }
  }

  var CONV_KEY = 'fit_ai_conv';
  function load(k) { try { return sessionStorage.getItem(k) || ''; } catch (e) { return ''; } }
  function save(k, v) { try { if (v) sessionStorage.setItem(k, v); else sessionStorage.removeItem(k); } catch (e) { /* ignore / 忽略 */ } }

  // panel builds the panel inside shell and returns its button for the top bar. / 在 shell 中构建面板，返回顶栏按钮。
  function panel(UI, shell) {
    var t = UI.t;
    // skill: the chip's skill; convSkill: the one the conversation runs, as the server says; inputs: a skill's inputs,
    // sent once with it; skills: the enabled skills, read when the panel opens.
    // skill：标签上的技能；convSkill：服务端所说对话运行的技能；inputs：技能的输入，随其发送一次；skills：已启用的技能，打开面板时读取。
    var st = { open: false, loaded: false, conv: load(CONV_KEY), convs: [], streaming: false, ctrl: null, info: null, sendPage: true, last: null,
      skill: '', convSkill: '', inputs: null, skills: null, pickAt: 0, matches: [] };
    var pick = h('select.select.select--sm.ai-pick', { 'aria-label': t('p.pick'), onchange: function () { open(pick.value); if (!pick.value) setSkill(''); } });
    var del = h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: t('p.delete'), 'aria-label': t('p.delete'), onclick: remove }, UI.icon('trash'));
    var head = h('div.ai-head', UI.icon('chat'), h('b', t('p.open')), pick,
      h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: t('p.new'), 'aria-label': t('p.new'), onclick: function () { open(''); setSkill(''); } }, UI.icon('plus')),
      del, h('button.btn.btn--ghost.btn--icon.btn--sm', { type: 'button', title: t('p.close'), 'aria-label': t('p.close'), onclick: toggle }, UI.icon('x')));
    var note = h('div.callout.callout--warn.ai-note', { hidden: true });
    var log = h('div.ai-log', { role: 'log', 'aria-live': 'polite' });
    // The conversation's pending proposal, above the composer (ai/proposals.js draws it). / 对话的待处理提案，显示在输入框上方。
    var propBox = h('div.ai-propbox', { hidden: true });
    var pageBox = h('input', { type: 'checkbox', checked: true, onchange: function () { st.sendPage = pageBox.checked; } });
    var pageLabel = h('span.ellipsis');
    var input = h('textarea.textarea.ai-input', {
      rows: 3, placeholder: t('p.placeholder'), 'aria-label': t('p.placeholder'),
      onkeydown: function (e) {
        if (pickKey(e)) return;
        if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); submit(); }
      },
      oninput: function () { typed(); }
    });
    var sendBtn = h('button.btn.btn--primary.ai-send', { type: 'button', onclick: function () { if (st.streaming) stop(); else submit(); } });
    var foot = h('div.ai-foot.small.muted');
    // The skill: a button that opens the picker, and the chip of the skill the next message runs.
    // 技能：打开选择器的按钮，以及下一条消息所运行技能的标签。
    var picker = h('div.ai-skillpick', { hidden: true, role: 'listbox', 'aria-label': t('p.skills') });
    var chip = h('span.ai-skillchip');
    var skillBtn = h('button.btn.btn--ghost.btn--sm', { type: 'button', title: t('p.skillsHint'), 'aria-label': t('p.skills'),
      onclick: function () { if (picker.hidden) { st.pickAt = -1; showPicker(''); } else hidePicker(); } }, UI.icon('book'), t('p.skills'));
    var skillBar = h('div.ai-skillbar.small', skillBtn, chip);
    var box = h('aside.ai-panel', { hidden: true, 'aria-label': t('p.open'), onkeydown: function (e) {
      if (e.key !== 'Escape') return;
      if (!picker.hidden) { hidePicker(); return; }
      toggle();
    } },
      head, note, log, propBox, h('label.check.ai-ctx.small', pageBox, pageLabel), skillBar, h('div.ai-composewrap', picker, h('div.ai-compose', input, sendBtn)), foot);
    shell.appendChild(box);
    var button = h('button.btn.btn--ghost.btn--icon.ai-toggle', { type: 'button', title: t('p.open'), 'aria-label': t('p.open'), onclick: toggle }, UI.icon('chat'));
    setBusy(false);

    // The button is hidden for a user whose level is off, and so are the pages' Ask AI buttons (ai/ask.js).
    // 级别为 off 的用户看不到按钮，各页面的“问 AI”按钮也一样（ai/ask.js）。
    if (window.FitAI.setAvailable) FitAI.setAvailable(null, shell);
    UI.api('GET', '/api/tools').then(function (info) {
      st.info = info;
      button.hidden = info.level === 'off';
      if (window.FitAI.setAvailable) FitAI.setAvailable(info.level !== 'off', shell);
      showInfo();
    }, function () { /* the panel says so when opened / 打开面板时再提示 */ });

    function showInfo() {
      var i = st.info;
      note.hidden = true;
      if (!i) return;
      var msg = i.level === 'off' ? t('p.off') : !i.model ? t('p.noModel') : !i.use_tools ? t('p.noTools') : '';
      if (msg) { note.textContent = msg; note.hidden = false; }
    }
    function footer(tokens) {
      var i = st.info || {};
      UI.mount(foot, i.model ? t('p.model', { model: i.provider + ' / ' + i.model }) : '', tokens != null ? ' · ' + t('p.today', { n: tokens }) : '',
        ' · ', i.store ? t('p.kept') : t('p.notKept'));
    }
    function toggle() {
      st.open = !st.open;
      box.hidden = !st.open;
      button.classList.toggle('is-active', st.open);
      if (!st.open) return;
      context();
      loadSkills();
      if (!st.loaded) { st.loaded = true; list().then(function () { open(st.conv); }); }
      setTimeout(function () { input.focus(); }, 30);
    }

    // ---- Skills / 技能 ----
    // loadSkills reads the enabled skills, each time the panel opens, as the Skills page may have changed them.
    // loadSkills 每次打开面板时读取已启用的技能（技能页可能已修改它们）。
    function loadSkills() {
      return UI.api('GET', '/api/skills').then(function (b) {
        st.skills = (b.skills || []).filter(function (s) { return s.enabled; });
        drawChip();
      }, function () { st.skills = st.skills || []; });
    }
    function skillOf(name) { return (st.skills || []).filter(function (s) { return s.name === name; })[0]; }
    function skillTitle(name) { var s = skillOf(name); return s && FitAI.skillWords ? FitAI.skillWords(UI, s).title : name; }
    function drawChip() {
      if (!st.skill) { UI.mount(chip, h('span.muted', t('p.noSkill'))); return; }
      UI.mount(chip, h('span.badge.ai-skillchip__on', { title: '/' + st.skill }, UI.icon('book'), ' ', skillTitle(st.skill),
        h('button.ai-skillchip__x', { type: 'button', title: t('p.clearSkill'), 'aria-label': t('p.clearSkill'), onclick: function () { setSkill(''); input.focus(); } }, UI.icon('x'))));
    }
    function setSkill(name, inputs) { st.skill = name || ''; st.inputs = inputs || null; drawChip(); }
    // showPicker lists the enabled skills whose name or title holds filter. / 列出名称或标题包含 filter 的已启用技能。
    function showPicker(filter) {
      var f = String(filter || '').toLowerCase();
      st.matches = (st.skills || []).filter(function (s) {
        var w = FitAI.skillWords ? FitAI.skillWords(UI, s) : s;
        return !f || s.name.indexOf(f) >= 0 || String(w.title).toLowerCase().indexOf(f) >= 0;
      });
      if (st.pickAt >= st.matches.length) st.pickAt = st.matches.length - 1;
      UI.mount(picker, st.matches.length ? st.matches.map(function (s, i) {
        var w = FitAI.skillWords ? FitAI.skillWords(UI, s) : s;
        return h('button.ai-skillpick__item' + (i === st.pickAt ? '.is-active' : ''), { type: 'button', role: 'option', 'aria-selected': i === st.pickAt ? 'true' : 'false',
          onmousedown: function (e) { e.preventDefault(); }, onclick: function () { choose(s); } },
          h('div', h('b', w.title), ' ', h('code.small', '/' + s.name)), h('div.small.muted', w.description));
      }) : h('div.small.muted.ai-skillpick__none', t('p.noMatch')));
      picker.hidden = false;
      var a = picker.querySelector('.is-active');
      if (a && a.scrollIntoView) a.scrollIntoView({ block: 'nearest' });
    }
    function hidePicker() { picker.hidden = true; st.matches = []; }
    // choose picks s: the chip shows it, and a /name typed is taken out of the message. / 选择 s：标签显示它，并去掉已输入的 /name。
    function choose(s) {
      setSkill(s.name);
      input.value = input.value.replace(/^\/[a-z0-9-]*\s*/, '');
      hidePicker();
      input.focus();
    }
    // typed shows the picker while the message is /name and nothing after it. / 消息为 /name 且其后无内容时显示选择器。
    function typed() {
      var m = input.value.match(/^\/([a-z0-9-]*)$/);
      if (!m) { if (!picker.hidden) hidePicker(); return; }
      st.pickAt = 0;
      showPicker(m[1]);
    }
    // pickKey moves through the picker with the arrows, and picks with Enter or Tab. / 方向键在选择器中移动，Enter 或 Tab 选择。
    function pickKey(e) {
      if (picker.hidden || !st.matches.length) return false;
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        st.pickAt = (Math.max(st.pickAt, -1) + (e.key === 'ArrowDown' ? 1 : st.matches.length - 1) + st.matches.length) % st.matches.length;
        showPicker(input.value.match(/^\/([a-z0-9-]*)$/) ? input.value.slice(1) : '');
        return true;
      }
      if ((e.key === 'Enter' || e.key === 'Tab') && !e.shiftKey && !e.isComposing && st.pickAt >= 0) {
        e.preventDefault();
        choose(st.matches[st.pickAt]);
        return true;
      }
      return false;
    }
    // useSkill opens the panel on a new conversation with skill name picked, its inputs and a message filled in; with
    // o.send (an Ask AI button's, I128) the message goes at once, with the page as context, once the enabled skills are
    // read: a skill not enabled is left out, and said so. / useSkill 以选好的技能 name 打开面板并新建对话，预填其输入与
    // 消息；带 o.send 时（“问 AI”按钮，I128）在读取已启用的技能后立即连同页面上下文发送：未启用的技能不使用，并加以说明。
    function useSkill(name, o) {
      o = o || {};
      if (st.streaming) return false;
      if (!st.open) toggle();
      open('');
      setSkill(name, o.inputs);
      if (o.message != null) input.value = o.message;
      setTimeout(function () { input.focus(); }, 30);
      if (o.send && input.value.trim()) {
        st.sendPage = pageBox.checked = true;
        (st.skills ? Promise.resolve() : loadSkills()).then(function () {
          if (st.streaming || st.skill !== name) return;
          var off = !skillOf(name);
          if (off) setSkill('');
          submit();
          if (off) log.insertBefore(h('div.callout.callout--warn.small', t('p.skillOff', { skill: '/' + name })), log.firstChild);
        });
      }
      return true;
    }
    window.FitAI.useSkill = useSkill;
    // context shows which page goes as context. / 显示作为上下文发送的页面。
    function context() {
      var p = window.WB && WB.page ? WB.page() : {};
      pageLabel.textContent = p.title ? t('p.page', { title: p.title }) : t('p.pageNone');
    }
    window.addEventListener('hashchange', function () { if (st.open) context(); });

    function list() {
      return UI.api('GET', '/api/conversations').then(function (b) {
        st.convs = b.conversations || [];
        footer(b.tokens_today);
        drawPick();
      }, UI.fail);
    }
    function drawPick() {
      var opts = [h('option', { value: '' }, t('p.untitled'))].concat(st.convs.map(function (c) {
        return h('option', { value: c.id, selected: c.id === st.conv }, c.title || c.id);
      }));
      UI.mount(pick, opts);
      pick.value = st.conv || '';
      del.disabled = !st.conv;
    }
    // open shows conversation id, or a new one for '', whose skill is the chip's. / 显示对话 id，'' 表示新对话（技能为标签上的）。
    function open(id) {
      if (st.streaming) return;
      st.conv = id || '';
      st.convSkill = '';
      save(CONV_KEY, st.conv);
      drawPick();
      UI.clear(log);
      drawProposal(null);
      if (!st.conv) { log.appendChild(UI.empty(t('p.empty'), 'chat')); return; }
      UI.api('GET', '/api/conversations/' + encodeURIComponent(st.conv)).then(function (c) { drawConversation(c); if (c.proposal) refreshProposal(); }, function (e) {
        if (e && e.status === 404) { open(''); return; }
        UI.fail(e);
      });
    }
    function remove() {
      var c = st.convs.filter(function (x) { return x.id === st.conv; })[0];
      if (!c || st.streaming) return;
      UI.confirm(t('p.deleteQ', { title: c.title }), t('p.delete'), true).then(function (ok) {
        if (!ok) return;
        UI.api('DELETE', '/api/conversations/' + encodeURIComponent(c.id)).then(function () { open(''); setSkill(''); return list(); }, UI.fail);
      });
    }

    // ---- The proposal / 提案 ----
    // refreshProposal reads the conversation's pending proposal, checked on the running config, and draws it.
    // refreshProposal 读取对话的待处理提案（已在运行中的配置上检查）并绘制。
    function refreshProposal() {
      var conv = st.conv;
      if (!conv || !window.FitAI.proposal) { drawProposal(null); return; }
      UI.api('GET', '/api/proposals/' + encodeURIComponent(conv)).then(function (b) {
        if (conv === st.conv) drawProposal(b && b.proposal);
      }, function () { /* the card stays as it was / 卡片保持原样 */ });
    }
    function drawProposal(v) {
      if (!v) { UI.clear(propBox); propBox.hidden = true; return; }
      UI.mount(propBox, FitAI.proposal.card(UI, v, {
        busy: function () { return st.streaming; },
        done: function (view, msg, kind) {
          if (msg) {
            log.appendChild(h('div.callout.small' + (kind === 'ok' ? '.callout--ok' : kind ? '.callout--' + kind : ''), msg));
            scrollDown(true);
          }
          drawProposal(view);
        }
      }));
      propBox.hidden = false;
    }

    // ---- Drawing a conversation / 绘制对话 ----
    // stick: the log follows what comes while it is scrolled to its end, and stays where the user scrolled it else.
    // stick：日志滚动到底部时跟随新内容；用户向上滚动后保持其位置。
    var stick = true;
    log.addEventListener('scroll', function () { stick = log.scrollHeight - log.scrollTop - log.clientHeight < 80; });
    function scrollDown(force) {
      if (force || stick) log.scrollTop = log.scrollHeight;
    }
    function userMsg(text) {
      var el = h('div.ai-msg.ai-msg--user', h('div.ai-bubble', text));
      log.appendChild(el);
      return el;
    }
    // turn is an answer as it is drawn: text, thinking, tools and a question, in the order they come.
    // turn 是正在绘制的一个回答：文字、思考、工具与问题，按到达顺序排列。
    function turn() {
      var el = h('div.ai-msg.ai-msg--bot'), text = null, buf = '', think = null, tools = {}, timer = null;
      log.appendChild(el);
      function draw() { timer = null; if (text) UI.mount(text, markdown(UI, buf)); scrollDown(); }
      var T = {
        el: el,
        text: function (s) {
          if (!text) { text = h('div.ai-text'); el.appendChild(text); buf = ''; }
          buf += s;
          if (!timer) timer = setTimeout(draw, 40);
        },
        flush: function () { if (timer) { clearTimeout(timer); draw(); } },
        think: function (s) {
          if (!think) {
            think = h('pre.ai-think');
            el.appendChild(h('details.ai-fold', h('summary', UI.icon('info'), ' ', t('p.thinking')), think));
          }
          think.textContent += s;
        },
        tool: function (id, name, args) {
          T.flush();
          text = null;
          var status = h('span.small.muted', t('p.running')), res = h('pre.ai-pre');
          var d = h('details.ai-fold.ai-tool', h('summary', UI.icon('code'), ' ', h('code', name), ' ', status),
            h('div.small.muted', t('p.args')), h('pre.ai-pre', pretty(args || '{}')), h('div.small.muted', t('p.result')), res);
          el.appendChild(d);
          tools[id] = { status: status, res: res, d: d };
          scrollDown();
        },
        // onPage marks a page tool's call as running on the page. / 标记页面工具的调用正在页面上执行。
        onPage: function (id) { if (tools[id]) tools[id].status.textContent = t('p.onPage'); },
        result: function (id, name, content, isErr, cut) {
          var x = tools[id];
          if (!x) { T.tool(id, name, '{}'); x = tools[id]; }
          x.res.textContent = pretty(content);
          x.status.textContent = isErr ? t('p.failed') : cut ? t('p.cut') : '';
          x.d.classList.toggle('is-error', !!isErr);
        },
        ask: function (ev, live) {
          T.flush();
          text = null;
          if (tools[ev.id]) tools[ev.id].status.textContent = t('p.waiting');
          var btns = h('div.btn-row');
          (ev.options || []).forEach(function (o) {
            btns.appendChild(h('button.btn.btn--sm', { type: 'button', disabled: !live, onclick: function () {
              Array.prototype.forEach.call(btns.children, function (b) { b.disabled = true; });
              answer(ev.id, o);
            } }, o));
          });
          el.appendChild(h('div.ai-ask', h('div', markdown(UI, ev.question)), btns));
          scrollDown(true);
        },
        note: function (msg, kind) { T.flush(); text = null; el.appendChild(h('div.callout.small' + (kind ? '.callout--' + kind : ''), msg)); scrollDown(); },
        retry: function () {
          el.appendChild(h('div.btn-row.ai-retry', h('button.btn.btn--sm', { type: 'button', onclick: function (e) { e.currentTarget.parentNode.remove(); retry(T); } },
            UI.icon('refresh'), t('p.retry'))));
        }
      };
      return T;
    }
    function drawConversation(c) {
      UI.clear(log);
      st.convSkill = c.skill || '';
      setSkill(st.convSkill);
      var T = null, msgs = c.messages || [];
      msgs.forEach(function (m, i) {
        if (c.folded && i === c.folded) log.appendChild(h('div.ai-divider.small.muted', t('p.summarized')));
        if (m.role === 'user') { T = null; userMsg(m.content); return; }
        if (!T) T = turn();
        if (m.role === 'assistant') {
          if (m.content) { T.text(m.content); T.flush(); }
          (m.tool_calls || []).forEach(function (tc) { T.tool(tc.id, tc.name, tc.arguments); });
          if (m.stopped) T.note(t('p.stopped'));
          if (m.error) T.note(m.error, 'danger');
        } else if (m.role === 'tool') {
          if (m.tool_name === 'ask_user' && !m.is_error) T.note(t('p.chose', { choice: m.content }));
          T.result(m.tool_call_id, m.tool_name, m.content, m.is_error, m.cut);
        }
      });
      if (c.ask) { if (!T) T = turn(); T.ask(c.ask, true); }
      st.last = T;
      var lastMsg = msgs[msgs.length - 1];
      if (T && !c.ask && !c.busy && lastMsg && (lastMsg.role !== 'user')) T.retry();
      else if (lastMsg && lastMsg.role === 'user' && !c.busy) { T = turn(); st.last = T; T.retry(); }
      scrollDown(true);
    }

    // ---- Chatting / 对话 ----
    function setBusy(on) {
      st.streaming = on;
      UI.mount(sendBtn, UI.icon(on ? 'stop' : 'send'), on ? t('p.stop') : t('p.send'));
      sendBtn.classList.toggle('btn--danger', on);
      // The proposal waits while the answer may still change it. / 回答进行中提案可能还会变，暂不可操作。
      propBox.classList.toggle('is-busy', on);
      pick.disabled = on;
      input.disabled = false;
    }
    // page is the context sent with a message: the page shown, and the text selected on it (not in the panel).
    // page 是随消息发送的上下文：当前页面及其上选中的文字（不含面板内的）。
    function page() {
      if (!st.sendPage || !window.WB || !WB.page) return undefined;
      var p = WB.page(), sel = '';
      try {
        var s = window.getSelection();
        if (s && s.rangeCount && !box.contains(s.anchorNode)) sel = String(s).slice(0, 4000);
      } catch (e) { /* none / 无 */ }
      return { module: p.module, page: p.page, title: p.title, selection: p.selection || undefined, text: sel || undefined,
        editable: p.editable || undefined };
    }
    // pageCall runs a page tool's call on the page shown and sends its answer back; the chat waits for it.
    // pageCall 在当前页面上执行页面工具的调用并回传其应答；对话会等待它。
    function pageCall(T, ev) {
      var conv = st.conv;
      T.onPage(ev.id);
      var run = window.WB && WB.pageTool ? WB.pageTool(ev.name, ev.arguments || {}) : Promise.reject(new Error('the workbench has no page tools'));
      run.then(function (res) {
        if (ev.name === 'page_edit' && res && res.drafted) T.note(t('p.drafted'), 'ok');
        if (ev.name === 'page_revert' && res && res.reverted) T.note(t('p.reverted'));
        return { conversation: conv, id: ev.id, result: res == null ? {} : res };
      }, function (e) {
        return { conversation: conv, id: ev.id, error: (e && e.message) || String(e) };
      }).then(function (body) {
        return UI.api('POST', '/api/chat/page', body);
      }).catch(function () { /* the chat's own timeout ends the wait / 由对话自身的超时结束等待 */ });
    }
    function submit() {
      var msg = input.value.trim();
      if (!msg || st.streaming) return;
      // /name, an enabled skill's, picks it; the rest is the message, and nothing is sent without one.
      // /name（某个已启用技能）选择该技能；其余部分为消息，没有消息则不发送。
      var m = msg.match(/^\/([a-z0-9-]+)(?:\s+([\s\S]*))?$/);
      if (m && skillOf(m[1])) {
        setSkill(m[1]);
        msg = (m[2] || '').trim();
        hidePicker();
        if (!msg) { input.value = ''; return; }
      }
      if (!st.conv) UI.clear(log);
      input.value = '';
      log.querySelectorAll('.ai-retry').forEach(function (r) { r.remove(); });
      userMsg(msg);
      scrollDown(true);
      var body = { conversation: st.conv || undefined, message: msg };
      if (st.skill !== st.convSkill || st.inputs) {
        body.skill = st.skill;
        if (st.skill && st.inputs) body.inputs = st.inputs;
      }
      run(body);
    }
    function answer(id, choice) {
      if (st.streaming) return;
      run({ conversation: st.conv, answer: { id: id, choice: choice } });
    }
    function retry(T) {
      if (st.streaming || !st.conv) return;
      if (T && T.el) T.el.remove();
      run({ conversation: st.conv, retry: true });
    }
    function stop() {
      if (st.conv) UI.api('POST', '/api/chat/stop', { conversation: st.conv }).catch(function () { /* the abort ends it / 中止即结束 */ });
      if (st.ctrl) st.ctrl.abort();
    }
    function run(body) {
      body.page = page();
      body.lang = UI.lang();
      var T = turn();
      st.last = T;
      st.ctrl = typeof AbortController !== 'undefined' ? new AbortController() : null;
      setBusy(true);
      var fresh = !body.conversation;
      UI.stream('/api/chat', body, function (ev) {
        switch (ev.type) {
          case 'start':
            st.conv = ev.conversation; save(CONV_KEY, st.conv);
            st.convSkill = ev.skill || '';
            setSkill(st.convSkill);
            if (fresh) { st.convs.unshift({ id: ev.conversation, title: ev.title }); drawPick(); }
            break;
          case 'text': T.text(ev.text); break;
          case 'thinking': T.think(ev.text); break;
          case 'tool_call': T.tool(ev.id, ev.name, ev.arguments); break;
          case 'tool_result': T.result(ev.id, ev.name, ev.content, ev.error, ev.cut); break;
          case 'page_call': pageCall(T, ev); break;
          case 'ask': T.ask(ev, true); break;
          case 'usage': footer(ev.today); break;
          case 'summary': T.note(t('p.summary')); break;
          case 'proposal': T.note(t('p.step', { tool: t('tool.' + ev.name), summary: ev.text || '' })); break;
          case 'skill': st.convSkill = ev.skill; setSkill(ev.skill); T.note(t('p.skillLoaded', { skill: skillTitle(ev.skill) })); break;
        }
      }, { signal: st.ctrl ? st.ctrl.signal : undefined }).then(function (end) {
        T.flush();
        var why = end && end.reason;
        if (why === 'stopped') T.note(t('p.stopped'));
        else if (why === 'max_tool_calls' || why === 'max_tokens_per_day') T.note(t('p.limit.' + why), 'warn');
        if (why !== 'ask') T.retry();
      }, function (e) {
        T.flush();
        if (e && e.name === 'AbortError') T.note(t('p.stopped'));
        else T.note(e && e.message ? e.message : String(e), 'danger');
        if (st.conv) T.retry();
      }).then(function () {
        setBusy(false);
        st.ctrl = null;
        list();
        refreshProposal();
        scrollDown();
      });
    }
    return button;
  }

  window.FitAI = window.FitAI || {};
  window.FitAI.panel = panel;
  window.FitAI.markdown = markdown;
})();
