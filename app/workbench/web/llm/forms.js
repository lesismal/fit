// Settings: cards that read a group of a role's own settings with one method and write them with another, through
// the config's write-back (the server checks them as the config does and answers 400 with the reason). Shared by the
// node, hub and proxy consoles, with the bounds on every role's links (Settings.limits). Needs ui.js.
//
// Settings：用一个方法读取、另一个方法写入角色自身一组设置的卡片，经配置写回（服务端按配置的规则校验，不合法时以 400 返回
// 原因）。node、hub、proxy 控制台共用，含每个角色的连接上限（Settings.limits）。依赖 ui.js。
//
// Settings.card({title, get, set, fields: [{key, label, hint, kind: 'int'|'dur'|'text', placeholder}], note, remove:
// {label, confirm, req}, after(cur) → node}) → element
(function () {
  'use strict';
  // make(UI) builds it on a module's UI (UI.scope), whose requests go to the module's API; Settings.with(UI) is
  // make. / make(UI) 基于模块的 UI（UI.scope）构建，其请求发往该模块的 API；Settings.with(UI) 即 make。
  function make(UI) {    var h = UI.h, t = UI.t;

    UI.addI18n({
      'en': {
        'st.limits': 'Link limits', 'st.limits_note': 'Bounds on this role\'s mesh links (limits). Links made from now on take them; links already up keep theirs.',
        'st.max_conns': 'Max links taken', 'st.max_conns_hint': 'Links from peers at once; 1024 by default',
        'st.max_inflight': 'Max calls per link', 'st.max_inflight_hint': 'Calls and streams a peer may run on one link; 1024 by default',
        'st.ping': 'Ping interval', 'st.ping_hint': 'At least 100ms; 15s by default', 'st.slow': 'Slow timeout', 'st.slow_hint': '30s by default',
        'st.retry': 'Retry interval', 'st.retry_hint': 'At least 100ms; 5s by default', 'st.idle': 'Stream idle', 'st.idle_hint': 'At least 1s; 2m by default',
        'st.default': 'empty or 0: the default'
      },
      'zh-CN': {
        'st.limits': '连接上限', 'st.limits_note': '本角色互联连接的上限（limits）。此后建立的连接采用新值；已建立的连接保持原值。',
        'st.max_conns': '接受的连接数上限', 'st.max_conns_hint': '同时接受对端的连接数；默认 1024',
        'st.max_inflight': '每连接调用上限', 'st.max_inflight_hint': '对端在一条连接上同时运行的调用与流；默认 1024',
        'st.ping': '心跳间隔', 'st.ping_hint': '至少 100ms；默认 15s', 'st.slow': '慢连接超时', 'st.slow_hint': '默认 30s',
        'st.retry': '重连间隔上限', 'st.retry_hint': '至少 100ms；默认 5s', 'st.idle': '流空闲超时', 'st.idle_hint': '至少 1s；默认 2m',
        'st.default': '留空或 0：默认值'
      }
    });

    function card(o) {
      var body = h('div');
      function value(f, el) {
        var v = el.value.trim();
        if (f.kind === 'int') return v === '' ? 0 : Number(v);
        return v;
      }
      function draw(cur) {
        cur = cur || {};
        var inputs = {};
        var fields = o.fields.map(function (f) {
          var v = cur[f.key];
          var el = h('input.input' + (f.kind === 'text' ? '' : '.mono'), {
            type: f.kind === 'int' ? 'number' : 'text', min: f.kind === 'int' ? 0 : null, autocomplete: 'off', spellcheck: 'false',
            placeholder: f.placeholder || t('st.default'), 'data-fill': !!f.placeholder, value: v == null || v === 0 && f.kind !== 'int' ? '' : String(v)
          });
          inputs[f.key] = el;
          return UI.field(f.label, el, f.hint);
        });
        var save = h('button.btn.btn--primary', { type: 'submit' }, t('save'));
        var rm = o.remove ? UI.btn(o.remove.label, 'trash', function () {
          UI.confirm(o.remove.confirm, o.remove.label, true).then(function (ok) {
            if (ok) UI.busy(rm, UI.rpc(o.set, o.remove.req)).then(function (r) { UI.toast(t('saved')); draw(r); if (o.onSaved) o.onSaved(r); }).catch(UI.fail);
          });
        }, 'danger') : null;
        var form = h('form.form', {
          onsubmit: function (e) {
            e.preventDefault();
            var req = {};
            o.fields.forEach(function (f) { req[f.key] = value(f, inputs[f.key]); });
            UI.busy(save, UI.rpc(o.set, req)).then(function (r) { UI.toast(t('saved')); draw(r); if (o.onSaved) o.onSaved(r); }).catch(UI.fail);
          }
        }, o.note ? h('div.small.muted', o.note) : null, h('div.form-row', fields), o.after ? o.after(cur) : null, h('div.btn-row', save, rm));
        UI.mount(body, form);
      }
      UI.mount(body, h('div.muted.small', t('loading')));
      UI.rpc(o.get).then(draw).catch(UI.fail);
      return UI.card(o.title, body);
    }

    function limits() {
      return card({
        title: t('st.limits'), get: '/mesh/limits/get', set: '/mesh/limits/set', note: t('st.limits_note'),
        fields: [
          { key: 'max_conns', kind: 'int', label: t('st.max_conns'), hint: t('st.max_conns_hint'), placeholder: '1024' },
          { key: 'max_inflight', kind: 'int', label: t('st.max_inflight'), hint: t('st.max_inflight_hint'), placeholder: '1024' },
          { key: 'ping_interval', kind: 'dur', label: t('st.ping'), hint: t('st.ping_hint'), placeholder: '15s' },
          { key: 'slow_timeout', kind: 'dur', label: t('st.slow'), hint: t('st.slow_hint'), placeholder: '30s' },
          { key: 'retry_interval', kind: 'dur', label: t('st.retry'), hint: t('st.retry_hint'), placeholder: '5s' },
          { key: 'stream_idle', kind: 'dur', label: t('st.idle'), hint: t('st.idle_hint'), placeholder: '2m' }
        ]
      });
    }

    return { card: card, limits: limits };
  }
  window.Settings = make(window.UI);
  window.Settings.with = make;
})();
