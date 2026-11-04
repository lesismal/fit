// The gateway's feature pages: the config's streaming, media-auth, WebRTC, tunnel-role and LLM-role members, each on a
// page of its own in the menu instead of under Global. Each page edits one top-level member through config.js's shared
// draft, and so has the UI/.conf/.yaml/.json tabs; a problem of a save links to the page where that member is edited,
// whichever page is being saved. The tunnel and LLM pages say that their roles start when the config is applied, and
// the streaming page points at the routes where its handlers are set. Needs ui.js, workbench.js and config.js.
//
// 网关的功能页面：配置的流媒体、媒体认证、WebRTC、隧道角色与 LLM 角色成员，各自在菜单中独立成页，而不再收在“全局配置”
// 之下。每页经 config.js 的共享草稿编辑一个顶层成员，因而有 UI/.conf/.yaml/.json 标签页；保存的问题会链接到编辑该成员的
// 页面，无论保存的是哪个页面。隧道与 LLM 页面说明其角色在配置应用后启动，流媒体页面指向设置其处理器的路由页面。
// 依赖 ui.js、workbench.js 与 config.js。
(function () {
  'use strict';

  // [key, en, zh] / [键, 英文, 中文]
  var WORDS = [
    ['s.streaming', 'Streaming', '流媒体'], ['s.mediaauth', 'Media auth', '媒体认证'], ['s.webrtc', 'WebRTC', 'WebRTC'],
    ['s.tunnel', 'Tunnel roles', '隧道角色'], ['s.llm', 'LLM roles', 'LLM 角色'],
    ['note.streaming', 'The streaming handlers themselves — HLS, HTTP-FLV and WHIP/WHEP pushing — are set on routes.',
      '流媒体处理器本身（HLS、HTTP-FLV 与 WHIP/WHEP 推流）在路由中设置。'],
    ['note.routes', 'Set them on the routes page', '在路由页面设置'],
    ['role.note', 'A role set here starts when the config is applied, and stops when it is left out. Its own pages show in this menu once it runs.',
      '在这里设置的角色在配置应用后启动，移除后停止。角色运行后，其页面会出现在菜单中。']
  ];

  // PAGES maps the config member each page edits to the page's route: a save's problems link there, wherever they are
  // found. Registered before any page draws, so that it holds for a save from any page.
  // PAGES 为各页编辑的配置成员与其页面路由：保存的问题从任意页面链接到这里。在绘制任何页面之前注册，故对任何页面的保存都有效。
  var PAGES = {
    streaming: '#/gw.streaming/streaming', media_auth: '#/gw.mediaauth/media_auth', webrtc: '#/gw.webrtc/webrtc',
    tunnel: '#/gw.tunnel/tunnel', llm: '#/gw.llm/llm'
  };
  FitConfig.with(UI.scope('admin', 'gw.features')).place(function (path) {
    var m = /^([A-Za-z_]+)/.exec(path || '');
    return m && PAGES[m[1]] ? PAGES[m[1]] : null;
  });

  // MODS are the pages: the module, its page of the config, the label and the menu icon. / 各页：模块、所编辑的配置成员、
  // 标签与菜单图标。
  var MODS = [
    { id: 'gw.streaming', icon: 'play', order: 8, page: 'streaming', member: 'streaming', label: 's.streaming' },
    { id: 'gw.mediaauth', icon: 'key', order: 9, page: 'media_auth', member: 'media_auth', label: 's.mediaauth' },
    { id: 'gw.webrtc', icon: 'radio', order: 10, page: 'webrtc', member: 'webrtc', label: 's.webrtc' },
    { id: 'gw.tunnel', icon: 'tunnel', order: 11, page: 'tunnel', member: 'tunnel', label: 's.tunnel' },
    { id: 'gw.llm', icon: 'cpu', order: 12, page: 'llm', member: 'llm', label: 's.llm' }
  ];

  // drawPage draws a feature page: the note that belongs to the member, then its form (and raw tabs, from C.page).
  // drawPage 绘制一个功能页面：该成员对应的提示，然后是其表单（及 C.page 提供的原始文本标签页）。
  function drawPage(UI, sh, def) {
    var h = UI.h, t = UI.t, C = FitConfig.with(UI);
    var el = sh.page(def.page, t(def.label));
    return C.page(el, sh, function (body) {
      if (def.member === 'tunnel' || def.member === 'llm') {
        body.appendChild(h('div.callout.callout--warn', UI.icon('info'), ' ', t('role.note')));
      }
      if (def.member === 'streaming') {
        body.appendChild(h('div.callout', UI.icon('info'), ' ', t('note.streaming'), ' ',
          h('a', { href: '#/gw.reverse/routes' }, t('note.routes'))));
      }
      body.appendChild(C.section(def.member));
    }, { members: [def.member] });
  }

  MODS.forEach(function (def) {
    WB.module({
      id: def.id, server: 'admin', group: 'gateway', order: def.order, icon: def.icon, label: def.label, words: WORDS,
      nav: [{ id: def.page, icon: def.icon, label: def.label }],
      setup: function (UI, sh) {
        var routes = {};
        routes[def.page] = function () { drawPage(UI, sh, def); };
        return { routes: routes };
      }
    });
  });
})();
