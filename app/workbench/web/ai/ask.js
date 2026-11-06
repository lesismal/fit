// Ask AI (I128): a button where the work is — a save's errors, a Consistency difference, an Engine port or route, a
// form field, a metrics spike, an upstream down, a link map — that opens the assistant's panel on a new conversation
// with a skill picked, its inputs and a message filled in, the thing asked about given to the page as its selection
// (WB.select), and the message sent at once: the first answer is about that thing. Any module may make one:
//
//   FitAI.askButton(spec, {label, cls}) → a button, or null while the assistant is off for this user
//   FitAI.ask(spec) → whether the panel took it
//   spec, or a function returning it at the click: {skill, message, inputs: {name: text}, selection}
//
// A button exists only when the workbench may show it: the console serves the module ai (its panel is built) and the
// user's level is not off. The panel tells it with FitAI.setAvailable(on, shell); none is made while no panel is shown
// or once the level is known to be off, and until the level is known the buttons are kept out of sight by the class
// fit-ai-on on <html> (ai.css). Inputs go
// as the skill's inputs (at most 2 KiB each, cut here); the selection goes with the page as its context, bounded here
// and redacted by the server as all page context is.
//
// “问 AI”（I128）：工作所在之处的按钮——保存的错误、一致性差异、引擎的端口或路由、表单字段、指标突增、上游不可用、链路图——
// 点击后以选好的技能打开助手面板并新建对话，预填技能输入与消息，把所问的对象作为页面的选中项（WB.select），并立即发送：
// 第一个回答即针对该对象。任何模块都可以创建：FitAI.askButton(spec, {label, cls}) 返回按钮（本用户的助手关闭时为 null），
// FitAI.ask(spec) 返回面板是否接受；spec（或点击时返回它的函数）为 {skill, message, inputs, selection}。只有控制台提供
// ai 模块（面板已构建）且用户级别不是 off 时按钮才会出现：面板以 FitAI.setAvailable(on, shell) 告知；没有显示中的面板或
// 已知级别为 off 时不创建按钮，级别尚未知道时 <html> 上没有 fit-ai-on 类，按钮不可见（ai.css）。输入作为技能的输入发送（每项至多 2 KiB，在此截断），
// 选中项随页面作为上下文发送，在此限定大小，并像所有页面上下文一样由服务端脱敏。
(function () {
  'use strict';
  var h = UI.h, t = UI.t;

  UI.addI18n({
    'en': {
      'ask.ai': 'Ask AI', 'ask.title': 'Ask the assistant about this ({skill})',
      'ask.busy': 'The assistant is still answering: wait for it, or stop it, then ask again.'
    },
    'zh-CN': {
      'ask.ai': '问 AI', 'ask.title': '就此询问助手（{skill}）',
      'ask.busy': '助手仍在回答：请等待或停止后再问。'
    }
  });

  // MAX_INPUT is a skill input's bound, the server's; MAX_SELECTION keeps the selection well inside the page context's
  // 16 KiB. / MAX_INPUT 为技能输入的上限（与服务端一致）；MAX_SELECTION 让选中项远小于页面上下文的 16 KiB。
  var MAX_INPUT = 2048, MAX_SELECTION = 8192, MAX_STRING = 1500, MAX_ITEMS = 40;

  // state: on, whether the assistant may be asked; known, whether the panel has said; shell, the shell whose panel said.
  // state：on 表示可以询问助手；known 表示面板已告知；shell 为告知的面板所在的外框。
  var state = { on: false, known: false, shell: null };
  function mark() { document.documentElement.classList.toggle('fit-ai-on', state.on); }
  // current tells whether the panel that said is the one shown: a workbench rebuilt without the module ai has none.
  // current 判断告知的面板是否仍在显示：重建后不再提供 ai 模块的工作台没有面板。
  function current() { return !!state.shell && document.body.contains(state.shell); }
  function available() {
    if (state.on && !current()) { state.on = false; state.known = false; mark(); }
    return state.on && !!(window.FitAI && FitAI.useSkill);
  }

  // cut returns s cut to at most n bytes of UTF-8, at a character's edge. / 把 s 截断为至多 n 字节的 UTF-8（不拆字符）。
  function cut(s, n) {
    s = String(s == null ? '' : s);
    if (s.length * 3 <= n) return s;
    var out = '', size = 0;
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i), w = c < 0x80 ? 1 : c < 0x800 ? 2 : (c >= 0xd800 && c < 0xdc00) ? 4 : 3;
      if (size + w > n) break;
      if (w === 4) { out += s[i] + (s[i + 1] || ''); i++; } else out += s[i];
      size += w;
    }
    return out;
  }
  // bound keeps a selection small: long strings and lists are cut, and what is still too long goes as its JSON text,
  // cut. / bound 限定选中项的大小：截断过长的字符串与列表，仍然过长时以截断后的 JSON 文本发送。
  function bound(v) {
    function walk(x, depth) {
      if (typeof x === 'string') return x.length > MAX_STRING ? x.slice(0, MAX_STRING) + '…' : x;
      if (!x || typeof x !== 'object') return x;
      if (depth > 6) return '…';
      if (Array.isArray(x)) {
        var a = x.slice(0, MAX_ITEMS).map(function (y) { return walk(y, depth + 1); });
        if (x.length > MAX_ITEMS) a.push('… ' + (x.length - MAX_ITEMS) + ' more');
        return a;
      }
      var o = {};
      Object.keys(x).forEach(function (k) { if (x[k] !== undefined) o[k] = walk(x[k], depth + 1); });
      return o;
    }
    var out = walk(v, 0), text;
    try { text = JSON.stringify(out); } catch (e) { return null; }
    return text.length > MAX_SELECTION ? { cut: true, json: text.slice(0, MAX_SELECTION) } : out;
  }

  window.FitAI = window.FitAI || {};

  // setAvailable is the panel's word: on (true or false), or null while its user's level is not known yet.
  // setAvailable 由面板调用：on 为 true 或 false；用户级别尚未知道时为 null。
  FitAI.setAvailable = function (on, shell) {
    state.on = on === true;
    state.known = on != null;
    if (shell) state.shell = shell;
    mark();
  };
  FitAI.available = available;

  // ask opens the panel with spec: its skill, inputs and message, the selection given to the page, and sends it.
  // ask 以 spec 打开面板：技能、输入与消息，把选中项交给页面，并发送。
  FitAI.ask = function (spec) {
    if (typeof spec === 'function') { try { spec = spec(); } catch (e) { UI.fail(e); return false; } }
    if (!spec || !spec.skill || !available()) return false;
    var inputs = {}, any = false;
    Object.keys(spec.inputs || {}).forEach(function (k) {
      var v = spec.inputs[k];
      if (v == null || v === '') return;
      inputs[k] = cut(v, MAX_INPUT);
      any = true;
    });
    if (window.WB && WB.select) WB.select(spec.selection == null ? null : bound(spec.selection));
    var ok = FitAI.useSkill(spec.skill, { message: spec.message || '', inputs: any ? inputs : null, send: spec.send !== false });
    if (!ok) UI.toast(t('ask.busy'), 'warn');
    return ok;
  };

  // askButton makes the Ask AI button of spec: a small ghost button, its label shown unless o.label is false. Its
  // click never reaches what holds it (a link, a summary that would fold). / 创建 spec 的“问 AI”按钮：小号幽灵按钮，
  // o.label 为 false 时只显示图标；其点击不会传给容器（链接、会折叠的 summary）。
  FitAI.askButton = function (spec, o) {
    o = o || {};
    // No panel shown (the console does not serve the module ai), or a level known to be off: no button.
    // 没有显示中的面板（控制台不提供 ai 模块），或已知级别为 off：不创建按钮。
    if (!current() || (state.known && !state.on)) return null;
    var skill = typeof spec === 'function' ? '' : (spec && spec.skill) || '';
    var title = skill ? t('ask.title', { skill: '/' + skill }) : t('ask.ai');
    return h('button.btn.btn--ghost.btn--sm.ai-askbtn' + (o.label === false ? '.btn--icon' : '') + (o.cls ? '.' + o.cls : ''), {
      type: 'button', title: title, 'aria-label': title, 'data-skill': skill || null,
      onclick: function (e) { e.preventDefault(); e.stopPropagation(); FitAI.ask(spec); }
    }, UI.icon('chat'), o.label === false ? null : h('span', t('ask.ai')));
  };
})();
