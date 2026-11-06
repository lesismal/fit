/*
 * fit media pages: a room call, publishing and playing over fit's WHIP and
 * WHEP endpoints, and the room API of the media_pages handler.
 *
 * The switches of the microphone, the camera and the screen never
 * renegotiate: every publisher offers one audio and one video transceiver
 * from the start, whether or not the devices exist, and a switch only
 * replaces the track a sender sends (RTCRtpSender.replaceTrack). Switched
 * on, a sender sends the device's track, or a synthetic one when there is no
 * device: silence, or a frame with the member's name. Switched off, it sends
 * nothing and the device is released. What a member switches is announced
 * through the room API, so that the others show its avatar, "microphone
 * off" or "no camera", rather than wait on media that does not come; and a
 * member's stream is played only once it is announced as published, so that
 * nobody asks for a stream that is not there.
 *
 * The microphone and camera start as the config's call.mic and call.cam say
 * (off unless configured), with the URL's mic=0|1 and cam=0|1 over them; the
 * user switches them afterwards, still without a renegotiation. When the
 * config's chat is on, the call page carries the room chat: text messages and
 * small files carried by the room API, its history on joining, and a file
 * downloaded with the page's credentials.
 *
 * With the config's library, the library page lists the media libraries'
 * items and the channels the viewer may play, and the player page plays one
 * with hls.js (vendor/hls.min.js) through the URL api/play gives it.
 */
'use strict';

(function () {
  var TOKEN_KEY = 'fitMediaToken';
  var LANG_KEY = 'fitMediaLang';
  var NAME_RE = /^[A-Za-z0-9._~-]{1,63}$/;
  var STREAM_RE = /^[A-Za-z0-9._~-]{1,128}$/;
  var ROOM_RE = /^[A-Za-z0-9._~-]{1,64}$/;
  // Retries are bounded: a failed attempt waits twice as long as the last,
  // up to MAX_BACKOFF, and a viewer gives up after VIEW_TRIES until the
  // publisher announces itself again.
  var MAX_BACKOFF = 15000;
  var VIEW_TRIES = 6;

  /* ---------- text ---------- */

  var TEXT = {
    en: {
      'nav.home': 'Home', 'nav.call': 'Call', 'nav.publish': 'Publish', 'nav.play': 'Play',
      'home.lead': "Calls, publishing and playing over fit's WebRTC media server.",
      'home.call': 'A room call: everyone publishes their camera and microphone and plays the others. Switch either off at any time; the others see it.',
      'home.publish': "Send a camera, a microphone or a screen to a stream with WHIP, and get the stream's URLs for other tools.",
      'home.play': 'Play a stream with WHEP, or HLS where the browser plays it.',
      'home.server': 'This server', 'home.auth': 'Credentials', 'home.none': 'none needed', 'home.needed': 'needed for',
      'home.you': 'You', 'home.anon': 'not logged in', 'home.may': 'may publish {pub}; play {play}',
      'call.room': 'Room', 'call.name': 'My name', 'call.join': 'Join', 'call.leave': 'Leave',
      'call.joined': 'In room {room} as {name}.', 'call.left': 'Left the room.', 'call.joining': 'Joining…',
      'call.badname': 'A room and a name are 1 to 64 letters, digits, ".", "_", "~" and "-".',
      'call.taken': 'Someone in the room has that name already.', 'call.nopub': 'You may watch this room, not publish in it.',
      'tools.mic': 'Microphone', 'tools.cam': 'Camera', 'tools.screen': 'Screen',
      'tile.me': 'me', 'tile.micoff': 'microphone off', 'tile.nomic': 'no microphone', 'tile.camoff': 'camera off',
      'tile.nocam': 'no camera', 'tile.screen': 'screen', 'tile.waiting': 'not publishing', 'tile.connecting': 'connecting',
      'tile.retry': 'retrying', 'tile.gaveup': 'not reachable',
      'tile.expand': 'Enlarge', 'tile.shrink': 'Back to the grid',
      'pub.stream': 'Stream', 'pub.start': 'Start publishing', 'pub.stop': 'Stop', 'pub.live': 'publishing',
      'pub.connecting': 'connecting', 'pub.off': 'not publishing', 'pub.retry': 'retrying',
      'play.mode': 'Over', 'play.start': 'Play', 'play.nohls': 'This browser does not play HLS by itself; use the URL below in a player.',
      'play.waiting': 'The stream is not published; trying again in {s}s.', 'play.gaveup': 'The stream is not published.',
      'play.playing': 'Playing {stream}.',
      'links.title': "The stream's URLs",
      'auth.login': 'Log in', 'auth.logout': 'Log out', 'auth.title': 'Log in', 'auth.user': 'User', 'auth.password': 'Password',
      'auth.token': 'Token', 'auth.or': 'or a token', 'auth.cancel': 'Cancel', 'auth.bad': 'The user or password is not right.',
      'auth.needed': 'Log in first: this needs credentials.', 'auth.denied': 'Your credentials do not allow this.',
      'err.409': 'The stream is published already.', 'err.503': 'The server is busy; trying again.',
      'err.422': 'The browser and the server share no codec.', 'err.media': 'The media could not be opened: {e}',
      'copy': 'Copy', 'copied': 'Copied.',
      'chat.title': 'Chat', 'chat.empty': 'No messages yet.', 'chat.message': 'Message',
      'chat.send': 'Send', 'chat.choose': 'Choose file', 'chat.sendfile': 'Send file',
      'chat.nofile': 'No file chosen', 'chat.download': 'Download',
      'chat.toobig': 'The file is {size}, larger than the room chat\'s limit of {max}.',
      'chat.nojoin': 'Join a room to chat.', 'chat.sendfail': 'The message could not be sent: {e}',
      'chat.filefail': 'The file could not be sent: {e}', 'chat.downloadfail': 'The file could not be downloaded: {e}',
      'nav.library': 'Library', 'home.library': 'Films and episodes on demand, and channels that play them as live streams.',
      'lib.search': 'Search', 'lib.go': 'Search', 'lib.channels': 'Channels', 'lib.more': 'More',
      'lib.loading': 'Loading…', 'lib.empty': 'Nothing here you may play.', 'lib.nochannels': 'No channel you may play.',
      'lib.notready': 'The library is being scanned; trying again.', 'lib.now': 'Now: {t}', 'lib.next': 'Next: {t}',
      'lib.live': 'LIVE', 'lib.count': '{n} titles',
      'watch.back': 'Back to the library', 'watch.again': 'Play again', 'watch.loading': 'Getting a playback URL…',
      'watch.playing': 'playing', 'watch.paused': 'paused', 'watch.ended': 'ended', 'watch.refused': 'refused',
      'watch.expired': 'The server refused the playback URL: it has expired, or was ended. Play again for a new one.',
      'watch.nohls': 'This browser plays neither HLS nor Media Source Extensions.', 'watch.error': 'Playback failed: {e}',
      'watch.until': 'The playback URL is good until {t}; the player plays on after it.',
    },
    zh: {
      'nav.home': '首页', 'nav.call': '通话', 'nav.publish': '推流', 'nav.play': '播放',
      'home.lead': '基于 fit 的 WebRTC 媒体服务器的通话、推流与播放。',
      'home.call': '房间通话：每人推送自己的摄像头和麦克风并观看其他人；随时可以关掉任一项，其他人都能看到。',
      'home.publish': '用 WHIP 把摄像头、麦克风或屏幕推送成一路流，并取得这路流给其他工具用的地址。',
      'home.play': '用 WHEP 播放一路流，浏览器支持时也可用 HLS。',
      'home.server': '本服务器', 'home.auth': '凭据', 'home.none': '不需要', 'home.needed': '需要凭据：',
      'home.you': '你', 'home.anon': '未登录', 'home.may': '可推流 {pub}；可播放 {play}',
      'call.room': '房间', 'call.name': '我的名字', 'call.join': '加入', 'call.leave': '离开',
      'call.joined': '已以 {name} 加入房间 {room}。', 'call.left': '已离开房间。', 'call.joining': '正在加入…',
      'call.badname': '房间和名字由 1 到 64 个字母、数字、"."、"_"、"~"、"-" 组成。',
      'call.taken': '房间里已有人用这个名字。', 'call.nopub': '你可以观看这个房间，但不能在其中推流。',
      'tools.mic': '麦克风', 'tools.cam': '摄像头', 'tools.screen': '屏幕',
      'tile.me': '我', 'tile.micoff': '麦克风已关', 'tile.nomic': '没有麦克风', 'tile.camoff': '摄像头已关',
      'tile.nocam': '没有摄像头', 'tile.screen': '屏幕共享', 'tile.waiting': '未推流', 'tile.connecting': '连接中',
      'tile.retry': '重试中', 'tile.gaveup': '连接不上',
      'tile.expand': '放大', 'tile.shrink': '还原',
      'pub.stream': '流名', 'pub.start': '开始推流', 'pub.stop': '停止', 'pub.live': '推流中',
      'pub.connecting': '连接中', 'pub.off': '未推流', 'pub.retry': '重试中',
      'play.mode': '方式', 'play.start': '播放', 'play.nohls': '这个浏览器不能直接播放 HLS，请把下面的地址交给播放器。',
      'play.waiting': '这路流还没有推送；{s} 秒后重试。', 'play.gaveup': '这路流还没有推送。',
      'play.playing': '正在播放 {stream}。',
      'links.title': '这路流的地址',
      'auth.login': '登录', 'auth.logout': '退出', 'auth.title': '登录', 'auth.user': '用户', 'auth.password': '密码',
      'auth.token': '令牌', 'auth.or': '或者使用令牌', 'auth.cancel': '取消', 'auth.bad': '用户名或密码不对。',
      'auth.needed': '请先登录：这需要凭据。', 'auth.denied': '你的凭据不允许这样做。',
      'err.409': '这路流已经有人在推了。', 'err.503': '服务器忙，正在重试。',
      'err.422': '浏览器和服务器没有共同的编码。', 'err.media': '无法打开媒体：{e}',
      'copy': '复制', 'copied': '已复制。',
      'chat.title': '聊天', 'chat.empty': '还没有消息。', 'chat.message': '消息',
      'chat.send': '发送', 'chat.choose': '选择文件', 'chat.sendfile': '发送文件',
      'chat.nofile': '未选择文件', 'chat.download': '下载',
      'chat.toobig': '这个文件有 {size}，超过了房间聊天的 {max} 上限。',
      'chat.nojoin': '加入房间后才能聊天。', 'chat.sendfail': '消息发送失败：{e}',
      'chat.filefail': '文件发送失败：{e}', 'chat.downloadfail': '文件下载失败：{e}',
      'nav.library': '片库', 'home.library': '点播的电影和剧集，以及把它们当作直播播放的频道。',
      'lib.search': '搜索', 'lib.go': '搜索', 'lib.channels': '频道', 'lib.more': '更多',
      'lib.loading': '加载中…', 'lib.empty': '这里没有你可以播放的内容。', 'lib.nochannels': '没有你可以播放的频道。',
      'lib.notready': '片库正在扫描，稍后重试。', 'lib.now': '正在播放：{t}', 'lib.next': '下一个：{t}',
      'lib.live': '直播', 'lib.count': '{n} 部',
      'watch.back': '返回片库', 'watch.again': '重新播放', 'watch.loading': '正在获取播放地址…',
      'watch.playing': '播放中', 'watch.paused': '已暂停', 'watch.ended': '已结束', 'watch.refused': '被拒绝',
      'watch.expired': '服务器拒绝了这个播放地址：它已过期或已被结束。请重新播放以获取新的地址。',
      'watch.nohls': '这个浏览器既不能播放 HLS，也不支持 Media Source Extensions。', 'watch.error': '播放失败：{e}',
      'watch.until': '播放地址在 {t} 前有效；过期后已开始的播放会继续。',
    },
  };
  var lang = 'en';
  function t(key, vars) {
    var s = (TEXT[lang] && TEXT[lang][key]) || TEXT.en[key] || key;
    if (vars) Object.keys(vars).forEach(function (k) { s = s.split('{' + k + '}').join(String(vars[k])); });
    return s;
  }
  function applyText() {
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    document.querySelectorAll('[data-i18n]').forEach(function (el) { el.textContent = t(el.getAttribute('data-i18n')); });
    $('lang').textContent = lang === 'zh' ? 'English' : '中文';
    $('call-chat').setAttribute('aria-label', t('chat.title'));
    updateChatFilename();
    renderChatLog();
  }

  /* ---------- small helpers ---------- */

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }
  function store(kind) {
    try { return window[kind]; } catch (e) { return null; }
  }
  function load(kind, key) {
    try { var s = store(kind); return s ? s.getItem(key) : null; } catch (e) { return null; }
  }
  function save(kind, key, v) {
    try { var s = store(kind); if (!s) return; if (v == null) s.removeItem(key); else s.setItem(key, v); } catch (e) { /* storage off */ }
  }
  var toastTimer = 0;
  function toast(msg) {
    var e = $('toast');
    e.textContent = msg;
    e.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { e.hidden = true; }, 3500);
  }
  function hint(id, msg, kind) {
    var e = $(id);
    e.textContent = msg || '';
    e.className = 'hint' + (kind ? ' ' + kind : '');
  }
  function randomName() {
    var b = new Uint8Array(2);
    crypto.getRandomValues(b);
    return 'guest-' + Array.prototype.map.call(b, function (x) { return ('0' + x.toString(16)).slice(-2); }).join('');
  }

  // A log of what went wrong, for the status the tests read; bounded.
  var problems = [];
  function problem(what) {
    problems.push(String(what));
    if (problems.length > 50) problems.shift();
  }


  // playWhenReady starts a media element, and tries it again after a
  // short wait when the browser refused it for a load that had not
  // finished, or restarted under the call: a video element whose source
  // is set and played at once drops that first play sometimes. A refusal
  // by the autoplay policy is left to the click that plays it.
  function playWhenReady(video) {
    if (!video) return;
    var tries = 0;
    function attempt() {
      if (!video.paused) return;
      var p = video.play();
      if (p && p.catch) {
        p.catch(function (e) {
          if (e && e.name === 'NotAllowedError') return;
          if (video.paused && ++tries <= 10) setTimeout(attempt, 300);
        });
      }
    }
    attempt();
  }

  var ICONS = {
    mic: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10a7 7 0 0 0 14 0M12 17v5"/></svg>',
    micoff: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M2 2l20 20M9 9v1a3 3 0 0 0 5.1 2.1M15 9.3V5a3 3 0 0 0-5.9-.7M19 10a7 7 0 0 1-1.1 3.8M5 10a7 7 0 0 0 11 5.7M12 17v5"/></svg>',
    cam: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 10l4.55-2.28A1 1 0 0 1 21 8.62v6.76a1 1 0 0 1-1.45.9L15 14"/><rect x="3" y="6" width="12" height="12" rx="2"/></svg>',
    camoff: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M2 2l20 20M15 10l4.55-2.28A1 1 0 0 1 21 8.62v6.76M15 15v1a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h1"/></svg>',
    screen: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="4" width="20" height="13" rx="2"/><path d="M8 21h8M12 17v4"/></svg>',
    expand: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg>',
    shrink: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M4 14h6v6M20 10h-6V4M14 10l7-7M3 21l7-7"/></svg>',
  };
  function icon(name) { var i = el('i'); i.innerHTML = ICONS[name]; return i; }

  /* ---------- config and credentials ---------- */

  var S = {
    conf: null,
    auth: null, // {token, expires, subject, publish, play}
    joinDefaults: { mic: false, cam: false }, // conf.call, the URL's mic=0|1 and cam=0|1 over it
  };

  function loadAuth() {
    try {
      var a = JSON.parse(load('sessionStorage', TOKEN_KEY) || 'null');
      if (a && a.token && (!a.expires || a.expires * 1000 > Date.now())) S.auth = a;
    } catch (e) { S.auth = null; }
  }
  function setAuth(a) {
    S.auth = a;
    save('sessionStorage', TOKEN_KEY, a ? JSON.stringify(a) : null);
    renderAuth();
  }
  function token() { return S.auth && S.auth.token ? S.auth.token : ''; }

  // headers adds the page's token: the login's cookie goes along by itself
  // to this server, but not to endpoints on another, nor a pasted token.
  function headers(h) {
    h = h || {};
    if (token()) h.Authorization = 'Bearer ' + token();
    return h;
  }

  // needsCredentials reports whether an action needs credentials the page
  // does not have.
  function needsCredentials(action) {
    var c = S.conf;
    return c && c.auth && !c.public[action] && !token();
  }

  async function api(path, body) {
    var opts = { method: body === undefined ? 'GET' : 'POST', headers: headers({}), credentials: 'same-origin' };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    var res = await fetch('api/' + path, opts);
    var text = await res.text().catch(function () { return ''; });
    var data = null;
    try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
    return { status: res.status, data: data, text: text, retryAfter: Number(res.headers.get('Retry-After')) || 0 };
  }

  function endpoint(tmpl, stream) {
    return tmpl.split('{stream}').join(encodeURIComponent(stream));
  }

  function absolute(u) {
    try { return new URL(u, location.href).toString(); } catch (e) { return u; }
  }

  // fill fills a link's template for a stream.
  function fill(tmpl, stream) {
    var s = tmpl.split('{stream}').join(encodeURIComponent(stream)).split('{host}').join(location.hostname);
    s = s.split('{token}').join(encodeURIComponent(token()));
    // A token parameter left empty is dropped.
    s = s.replace(/([?&])token=(&|$)/, function (m, a, b) { return b === '&' ? a : ''; });
    return absolute(s);
  }

  function showLogin(reason) {
    var c = S.conf || {};
    $('login-user').hidden = !c.login;
    $('login-or').hidden = !c.login;
    $('login-error').textContent = reason || '';
    $('login-error').className = 'hint' + (reason ? ' error' : '');
    var d = $('login');
    if (!d.open) {
      try { d.showModal(); } catch (e) { d.setAttribute('open', ''); }
    }
  }

  async function submitLogin(ev) {
    ev.preventDefault();
    var c = S.conf || {};
    var user = $('login-name').value.trim();
    var tok = $('login-token').value.trim();
    if (c.login && user) {
      var r = await api('login', { user: user, password: $('login-password').value });
      $('login-password').value = '';
      if (r.status !== 200 || !r.data) {
        $('login-error').textContent = r.status === 401 ? t('auth.bad') : (r.text || ('HTTP ' + r.status));
        $('login-error').className = 'hint error';
        return;
      }
      setAuth({ token: r.data.token, expires: r.data.expires, subject: r.data.subject, publish: r.data.publish, play: r.data.play });
    } else if (tok) {
      setAuth({ token: tok, expires: 0, subject: '', publish: null, play: null });
    } else {
      return;
    }
    $('login').close();
    renderHome();
    if (view === 'library') renderLibrary();
    if (view === 'watch') startWatch();
  }

  async function logout() {
    setAuth(null);
    if (S.conf && S.conf.login) await api('logout', {}).catch(function () {});
    renderHome();
  }

  function renderAuth() {
    var c = S.conf;
    var who = $('who');
    var needed = c && c.auth && (!c.public.publish || !c.public.play);
    $('login-open').hidden = !needed || !!token();
    $('logout').hidden = !token();
    if (token()) {
      who.hidden = false;
      who.textContent = S.auth.subject || t('auth.token');
      who.className = 'badge ok';
    } else {
      who.hidden = true;
    }
  }

  /* ---------- local media ---------- */

  // Local is the microphone, the camera and the screen of a page, with
  // synthetic stand-ins for devices that are not there.
  function Local(name) {
    this.name = name;
    this.audioOn = true;
    this.videoOn = true;
    this.screenOn = false;
    this.has = { audio: false, video: false };
    this.mic = null;
    this.cam = null;
    this.screen = null;
    this.fakeAudio = null;
    this.fakeVideo = null;
    this.canvas = null;
    this.drawTimer = 0;
    this.audioCtx = null;
    this.onchange = null;
  }

  // probe finds which devices exist: their kinds are listed even before
  // the page may use them.
  Local.prototype.probe = async function () {
    try {
      var list = await navigator.mediaDevices.enumerateDevices();
      this.has.audio = list.some(function (d) { return d.kind === 'audioinput'; });
      this.has.video = list.some(function (d) { return d.kind === 'videoinput'; });
    } catch (e) {
      this.has.audio = this.has.video = false;
    }
  };

  // audioTrack is the track the audio sender sends now: the microphone's,
  // silence without one, none while switched off.
  Local.prototype.audioTrack = async function () {
    if (!this.audioOn) {
      if (this.mic) { this.mic.stop(); this.mic = null; }
      return null;
    }
    if (!this.mic && this.has.audio) {
      try {
        var s = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
        this.mic = s.getAudioTracks()[0] || null;
      } catch (e) {
        // Refused or gone: the member has no microphone it may use.
        this.has.audio = false;
        problem('microphone: ' + e.name);
      }
      if (!this.audioOn && this.mic) { this.mic.stop(); this.mic = null; return null; }
    }
    return this.mic || this.silence();
  };

  // videoTrack is the track the video sender sends now: the screen's, the
  // camera's, a frame with the name without a camera, none while off.
  Local.prototype.videoTrack = async function () {
    if (this.screenOn && this.screen && this.screen.readyState === 'live') return this.screen;
    if (!this.videoOn) {
      if (this.cam) { this.cam.stop(); this.cam = null; }
      return null;
    }
    if (!this.cam && this.has.video) {
      try {
        var s = await navigator.mediaDevices.getUserMedia({ video: { width: { ideal: 1280 }, height: { ideal: 720 }, frameRate: { ideal: 30 } } });
        this.cam = s.getVideoTracks()[0] || null;
      } catch (e) {
        this.has.video = false;
        problem('camera: ' + e.name);
      }
      if (!this.videoOn && this.cam) { this.cam.stop(); this.cam = null; return null; }
    }
    return this.cam || this.frame();
  };

  Local.prototype.startScreen = async function () {
    var self = this;
    var s = await navigator.mediaDevices.getDisplayMedia({ video: { frameRate: { ideal: 15 } }, audio: false });
    this.screen = s.getVideoTracks()[0];
    this.screen.addEventListener('ended', function () {
      self.screen = null;
      self.screenOn = false;
      if (self.onchange) self.onchange();
    });
  };

  Local.prototype.stopScreen = function () {
    if (this.screen) { this.screen.stop(); this.screen = null; }
  };

  Local.prototype.silence = function () {
    if (this.fakeAudio && this.fakeAudio.readyState === 'live') return this.fakeAudio;
    var Ctx = window.AudioContext || window.webkitAudioContext;
    if (!Ctx) return null;
    this.audioCtx = this.audioCtx || new Ctx();
    var osc = this.audioCtx.createOscillator();
    var gain = this.audioCtx.createGain();
    gain.gain.value = 0;
    var dst = this.audioCtx.createMediaStreamDestination();
    osc.connect(gain).connect(dst);
    osc.start();
    this.audioCtx.resume().catch(function () {});
    this.fakeAudio = dst.stream.getAudioTracks()[0];
    return this.fakeAudio;
  };

  Local.prototype.frame = function () {
    if (this.fakeVideo && this.fakeVideo.readyState === 'live') return this.fakeVideo;
    var c = document.createElement('canvas');
    c.width = 320;
    c.height = 180;
    var name = this.name;
    var g = c.getContext('2d');
    var n = 0;
    function draw() {
      n++;
      g.fillStyle = '#0f1220';
      g.fillRect(0, 0, c.width, c.height);
      g.fillStyle = '#6e6ef0';
      g.beginPath();
      g.arc(160, 74, 34, 0, Math.PI * 2);
      g.fill();
      g.fillStyle = '#fff';
      g.font = 'bold 30px sans-serif';
      g.textAlign = 'center';
      g.textBaseline = 'middle';
      g.fillText((name[0] || '?').toUpperCase(), 160, 76);
      g.font = '14px sans-serif';
      g.fillStyle = '#d5dbef';
      g.fillText(name, 160, 136);
      // A pixel that changes, so that frames keep coming.
      g.fillStyle = n % 2 ? '#0f1220' : '#10131f';
      g.fillRect(0, 0, 2, 2);
    }
    draw();
    this.canvas = c;
    this.drawTimer = setInterval(draw, 500);
    this.fakeVideo = c.captureStream(2).getVideoTracks()[0];
    return this.fakeVideo;
  };

  // state is what the member announces of its media.
  Local.prototype.state = function () {
    return {
      audio: this.audioOn, video: this.videoOn || this.screenOn,
      audioDevice: !!this.mic || (this.audioOn ? false : this.has.audio),
      videoDevice: !!this.cam || !!this.screen || (this.videoOn ? false : this.has.video),
      screen: this.screenOn && !!this.screen,
    };
  };

  Local.prototype.close = function () {
    [this.mic, this.cam, this.screen, this.fakeAudio, this.fakeVideo].forEach(function (tr) { if (tr) tr.stop(); });
    this.mic = this.cam = this.screen = this.fakeAudio = this.fakeVideo = null;
    clearInterval(this.drawTimer);
    if (this.audioCtx) { this.audioCtx.close().catch(function () {}); this.audioCtx = null; }
  };

  /* ---------- WHIP and WHEP ---------- */

  function pcConfig() {
    return { iceServers: (S.conf && S.conf.iceServers) || [], bundlePolicy: 'max-bundle' };
  }

  async function postOffer(url, sdp) {
    var res = await fetch(url, {
      method: 'POST', credentials: 'same-origin',
      headers: headers({ 'Content-Type': 'application/sdp' }), body: sdp,
    });
    var body = await res.text().catch(function () { return ''; });
    return {
      status: res.status, body: body, retryAfter: Number(res.headers.get('Retry-After')) || 0,
      location: res.headers.get('Location') ? new URL(res.headers.get('Location'), absolute(url)).toString() : '',
    };
  }

  // hangUp ends a session with a DELETE of its URL, and then closes its
  // PeerConnection, if given: closed first, the server could end the
  // session on the DTLS close before the DELETE came, and answer it 404.
  // The PeerConnection is closed after at most 2 seconds all the same.
  function hangUp(url, pc) {
    var closed = false;
    function close() {
      if (closed || !pc) return;
      closed = true;
      try { pc.close(); } catch (e) { /* closed */ }
    }
    if (!url) { close(); return; }
    var timer = setTimeout(close, 2000);
    fetch(url, { method: 'DELETE', keepalive: true, credentials: 'same-origin', headers: headers({}) })
      .catch(function () {})
      .then(function () { clearTimeout(timer); close(); });
  }

  // detach takes a session's PeerConnection from it, to close once its
  // DELETE is answered.
  function detach(x) {
    var pc = x.pc;
    x.pc = null;
    return pc;
  }

  function ended(pc) {
    return new Promise(function (resolve) {
      var since = 0;
      var timer = setInterval(check, 1000);
      function check() {
        var s = pc.connectionState;
        if (s === 'failed' || s === 'closed') return done();
        if (s === 'disconnected') {
          if (!since) since = Date.now();
          else if (Date.now() - since > 10000) return done();
        } else {
          since = 0;
        }
      }
      function done() {
        clearInterval(timer);
        pc.removeEventListener('connectionstatechange', check);
        resolve();
      }
      pc.addEventListener('connectionstatechange', check);
    });
  }

  // Publisher publishes a Local to a stream with WHIP, again after a
  // failure, with a bounded backoff, until stopped.
  function Publisher(local, stream, onstate) {
    this.local = local;
    this.stream = stream;
    this.onstate = onstate || function () {};
    this.pc = null;
    this.audio = null;
    this.video = null;
    this.location = '';
    this.active = false;
    this.phase = 'off'; // off | connecting | live | retry | refused
    this.connected = false;
    this.posts = 0;
    this.error = '';
    this.wake = null;
  }

  Publisher.prototype.set = function (phase, error) {
    this.phase = phase;
    this.error = error || '';
    this.onstate(this);
  };

  Publisher.prototype.start = function () {
    if (this.active) return;
    this.active = true;
    this.run();
  };

  Publisher.prototype.run = async function () {
    var wait = 1000;
    while (this.active) {
      var r;
      try {
        r = await this.connect();
      } catch (e) {
        // A connect the page stopped, as leaving does, is not a problem.
        if (!this.active) break;
        r = { retry: true, error: String(e && e.message || e) };
        problem(this.stream + ': ' + r.error);
      }
      if (!this.active) break;
      if (r.ok) {
        wait = 1000;
        this.set('live');
        await ended(this.pc);
        if (!this.active) break;
        hangUp(this.location);
        this.location = '';
        this.close();
        this.set('retry', 'the connection was lost');
        continue;
      }
      this.close();
      if (!r.retry) { this.active = false; this.set('refused', r.error); break; }
      this.set('retry', r.error);
      var ms = r.retryAfter ? r.retryAfter * 1000 : wait;
      wait = Math.min(wait * 2, MAX_BACKOFF);
      await this.sleep(ms);
    }
  };

  Publisher.prototype.sleep = function (ms) {
    var self = this;
    return new Promise(function (resolve) {
      var timer = setTimeout(done, ms);
      self.wake = done;
      function done() { clearTimeout(timer); self.wake = null; resolve(); }
    });
  };

  Publisher.prototype.connect = async function () {
    var self = this;
    this.set('connecting');
    var pc = new RTCPeerConnection(pcConfig());
    this.pc = pc;
    // One audio and one video transceiver, always: a switch only replaces
    // the track a sender sends.
    this.audio = pc.addTransceiver('audio', { direction: 'sendonly' });
    this.video = pc.addTransceiver('video', { direction: 'sendonly' });
    await this.apply();
    if (!this.active || this.pc !== pc) return { retry: false }; // left while connecting
    pc.addEventListener('connectionstatechange', function () {
      self.connected = pc.connectionState === 'connected';
      self.onstate(self);
    });
    var offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    if (!this.active || this.pc !== pc) return { retry: false }; // left while connecting
    this.posts++;
    var res = await postOffer(endpoint(S.conf.whip, this.stream), pc.localDescription.sdp);
    if (res.status !== 201) return failure(res);
    this.location = res.location;
    if (!this.active || this.pc !== pc) { hangUp(res.location); return { retry: false }; }
    try {
      await pc.setRemoteDescription({ type: 'answer', sdp: res.body });
    } catch (e) {
      // The session was made: it goes before the next try.
      hangUp(res.location);
      this.location = '';
      throw e;
    }
    return { ok: true };
  };

  // apply puts the tracks the switches say on the senders.
  Publisher.prototype.apply = async function () {
    var a = await this.local.audioTrack();
    var v = await this.local.videoTrack();
    if (this.audio && this.audio.sender.track !== a) await this.audio.sender.replaceTrack(a);
    if (this.video && this.video.sender.track !== v) await this.video.sender.replaceTrack(v);
  };

  Publisher.prototype.close = function () {
    if (this.pc) {
      try { this.pc.close(); } catch (e) { /* closed */ }
      this.pc = null;
    }
    this.connected = false;
    this.audio = this.video = null;
  };

  Publisher.prototype.stop = function () {
    this.active = false;
    if (this.wake) this.wake();
    hangUp(this.location, detach(this));
    this.location = '';
    this.close();
    this.set('off');
  };

  function failure(res) {
    var msg = res.body.trim() || ('HTTP ' + res.status);
    switch (res.status) {
      case 401: return { retry: false, auth: true, status: 401, error: t('auth.needed') };
      case 403: return { retry: false, auth: true, status: 403, error: t('auth.denied') };
      case 404: return { retry: true, status: 404, error: msg };
      case 409: return { retry: true, status: 409, error: t('err.409') };
      case 422: return { retry: false, status: 422, error: t('err.422') };
      case 503: return { retry: true, status: 503, error: t('err.503'), retryAfter: res.retryAfter };
      default: return { retry: res.status >= 500, status: res.status, error: msg };
    }
  }

  // Viewer plays a stream with WHEP into a video element, again after a
  // failure, at most VIEW_TRIES times in a row, with a bounded backoff.
  function Viewer(stream, video, onstate) {
    this.stream = stream;
    this.videoEl = video;
    this.onstate = onstate || function () {};
    this.pc = null;
    this.location = '';
    this.active = false;
    this.phase = 'off'; // off | connecting | live | retry | gaveup | refused
    this.connected = false;
    this.tracks = 0;
    this.posts = 0;
    this.notFound = 0;
    this.error = '';
    this.wake = null;
    this.nextTry = 0;
  }

  Viewer.prototype.set = Publisher.prototype.set;
  Viewer.prototype.sleep = Publisher.prototype.sleep;

  Viewer.prototype.start = function () {
    if (this.active) return;
    this.active = true;
    this.run();
  };

  Viewer.prototype.run = async function () {
    var wait = 1000;
    var tries = 0;
    while (this.active) {
      var r;
      try {
        r = await this.connect();
      } catch (e) {
        // A connect the page stopped, as leaving does, is not a problem.
        if (!this.active) break;
        r = { retry: true, error: String(e && e.message || e) };
        problem(this.stream + ': ' + r.error);
      }
      if (!this.active) break;
      if (r.ok) {
        wait = 1000;
        tries = 0;
        this.set('live');
        await ended(this.pc);
        if (!this.active) break;
        hangUp(this.location);
        this.location = '';
        this.close();
        this.set('retry', 'the connection was lost');
        continue;
      }
      this.close();
      if (r.status === 404) this.notFound++;
      tries++;
      if (!r.retry) { this.active = false; this.set('refused', r.error); break; }
      if (tries >= VIEW_TRIES) { this.active = false; this.set('gaveup', r.error); break; }
      var ms = r.retryAfter ? r.retryAfter * 1000 : wait;
      wait = Math.min(wait * 2, MAX_BACKOFF);
      this.nextTry = Date.now() + ms;
      this.set('retry', r.error);
      await this.sleep(ms);
    }
  };

  Viewer.prototype.connect = async function () {
    var self = this;
    this.set('connecting');
    var pc = new RTCPeerConnection(pcConfig());
    this.pc = pc;
    var stream = new MediaStream();
    pc.addTransceiver('audio', { direction: 'recvonly' });
    pc.addTransceiver('video', { direction: 'recvonly' });
    pc.addEventListener('track', function (ev) {
      stream.addTrack(ev.track);
      self.tracks = stream.getTracks().length;
      if (self.videoEl.srcObject !== stream) self.videoEl.srcObject = stream;
      playWhenReady(self.videoEl);
      self.onstate(self);
    });
    pc.addEventListener('connectionstatechange', function () {
      self.connected = pc.connectionState === 'connected';
      self.onstate(self);
    });
    var offer = await pc.createOffer();
    await pc.setLocalDescription(offer);
    if (!this.active || this.pc !== pc) return { retry: false }; // left while connecting
    this.posts++;
    var res = await postOffer(endpoint(S.conf.whep, this.stream), pc.localDescription.sdp);
    if (res.status !== 201) return failure(res);
    this.location = res.location;
    if (!this.active || this.pc !== pc) { hangUp(res.location); return { retry: false }; }
    try {
      await pc.setRemoteDescription({ type: 'answer', sdp: res.body });
    } catch (e) {
      hangUp(res.location);
      this.location = '';
      throw e;
    }
    return { ok: true };
  };

  Viewer.prototype.close = function () {
    if (this.pc) {
      try { this.pc.close(); } catch (e) { /* closed */ }
      this.pc = null;
    }
    this.connected = false;
    this.tracks = 0;
  };

  // stop stops playing; ended says the publisher has gone, which ended the
  // session on the server already, so that a DELETE would find nothing.
  Viewer.prototype.stop = function (ended) {
    this.active = false;
    if (this.wake) this.wake();
    if (!ended) hangUp(this.location, detach(this));
    this.location = '';
    this.close();
    if (this.videoEl) this.videoEl.srcObject = null;
    this.set('off');
  };

  /* ---------- tiles ---------- */

  function Tile(name, mine) {
    this.name = name;
    this.root = el('div', 'tile');
    this.video = el('video');
    this.video.autoplay = true;
    this.video.playsInline = true;
    if (mine) this.video.muted = true;
    this.avatar = el('div', 'tile__avatar');
    this.avatar.appendChild(el('span', '', (name[0] || '?')));
    this.note = el('div', 'tile__note');
    var bar = el('div', 'tile__bar');
    this.label = el('span', 'tile__name', mine ? name + ' (' + t('tile.me') + ')' : name);
    this.icons = el('span', 'tile__icons');
    this.stateEl = el('span', 'tile__state');
    bar.appendChild(this.label);
    bar.appendChild(this.icons);
    bar.appendChild(this.stateEl);
    this.root.appendChild(this.video);
    this.root.appendChild(this.avatar);
    this.root.appendChild(this.note);
    this.root.appendChild(bar);
    this.root.dataset.member = name;
  }

  // zoomable gives a tile of the call a button, and a double click, that
  // puts it in the spotlight: across the top, centred, the other tiles in
  // the grid below. Again puts it back. One tile at a time; the video
  // stays where it is in the page, so it plays on.
  Tile.prototype.zoomable = function () {
    var self = this;
    this.zoom = el('button', 'tile__zoom');
    this.zoom.type = 'button';
    this.zoom.addEventListener('click', function () { spotlight(self); });
    this.root.addEventListener('dblclick', function () { spotlight(self); });
    this.root.appendChild(this.zoom);
    this.drawZoom();
  };

  Tile.prototype.drawZoom = function () {
    if (!this.zoom) return;
    var on = call.spot === this;
    this.zoom.textContent = '';
    this.zoom.appendChild(icon(on ? 'shrink' : 'expand'));
    this.zoom.title = t(on ? 'tile.shrink' : 'tile.expand');
    this.zoom.setAttribute('aria-label', this.zoom.title);
    this.zoom.setAttribute('aria-pressed', on ? 'true' : 'false');
  };

  // spotlight puts tile in the spotlight, or takes it out when it is there
  // already; null takes out whichever is.
  function spotlight(tile) {
    var was = call.spot;
    call.spot = tile && tile !== was ? tile : null;
    if (was) { was.root.classList.remove('tile--spot'); was.drawZoom(); }
    if (call.spot) {
      call.spot.root.classList.add('tile--spot');
      call.spot.drawZoom();
      call.spot.root.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
    $('call-tiles').classList.toggle('tiles--spot', !!call.spot);
  }

  // show draws a member's announced state: its avatar while its camera is
  // off, and what is off or missing.
  Tile.prototype.show = function (st, status) {
    st = st || {};
    this.icons.textContent = '';
    var mic = icon(st.audio ? 'mic' : 'micoff');
    if (!st.audio) mic.className = 'off';
    mic.title = !st.audio ? t('tile.micoff') : (st.audioDevice ? '' : t('tile.nomic'));
    this.icons.appendChild(mic);
    var cam = icon(st.video ? (st.screen ? 'screen' : 'cam') : 'camoff');
    if (!st.video) cam.className = 'off';
    this.icons.appendChild(cam);
    var notes = [];
    if (!st.audio) notes.push(t('tile.micoff'));
    else if (!st.audioDevice) notes.push(t('tile.nomic'));
    if (!st.video) notes.push(t('tile.camoff'));
    else if (st.screen) notes.push(t('tile.screen'));
    else if (!st.videoDevice) notes.push(t('tile.nocam'));
    this.note.textContent = notes.join(' · ');
    this.root.classList.toggle('tile--screen', !!st.screen);
    var hasVideo = !!this.video.srcObject && this.video.srcObject.getVideoTracks().length > 0;
    this.avatar.hidden = !!st.video && hasVideo;
    this.stateEl.textContent = status || '';
  };

  /* ---------- the call ---------- */

  var call = {
    active: false,
    room: '', name: '', key: '',
    local: null,
    pub: null,
    members: {}, // name -> {state, tile, viewer}
    me: null, // my tile
    spot: null, // the tile in the spotlight
    events: null,
    eventsRetry: 0,
    heartbeat: 0,
    seq: 0,
    joinedAt: 0,
    mayPublish: true,
    gen: 0,
    chatMsgs: [], // the room chat's messages, oldest first
    chatIds: {}, // the ids shown, so that the SSE echo of a sent message is not drawn twice
  };

  function callStatus(v) {
    if (!v) return t('tile.waiting');
    switch (v.phase) {
      case 'live': return v.connected && v.tracks ? '' : t('tile.connecting');
      case 'connecting': return t('tile.connecting');
      case 'retry': return t('tile.retry');
      case 'gaveup': case 'refused': return t('tile.gaveup');
    }
    return '';
  }

  function renderCallControls() {
    var on = call.active;
    $('call-join').disabled = on;
    $('call-leave').disabled = !on;
    $('call-room').disabled = on;
    $('call-name').disabled = on;
    var l = call.local;
    var d = S.joinDefaults;
    press('call-mic', l ? l.audioOn : d.mic);
    press('call-cam', l ? l.videoOn : d.cam);
    press('call-screen', l ? l.screenOn : false);
    $('call-screen').disabled = !navigator.mediaDevices || !navigator.mediaDevices.getDisplayMedia;
  }

  function press(id, on) {
    $(id).setAttribute('aria-pressed', on ? 'true' : 'false');
    var ic = $(id).querySelector('.toggle__icon');
    var base = ic.getAttribute('data-icon');
    ic.innerHTML = ICONS[on || base === 'screen' ? base : base + 'off'];
  }

  function renderMe() {
    if (!call.me) return;
    var st = call.local.state();
    call.me.show(st, call.pub ? (call.pub.phase === 'live' ? '' : t('pub.' + (call.pub.phase === 'refused' ? 'off' : call.pub.phase))) : '');
    var v = call.pub && call.pub.video && call.pub.video.sender.track;
    var cur = call.me.video.srcObject;
    if (v && (!cur || cur.getVideoTracks()[0] !== v)) call.me.video.srcObject = new MediaStream([v]);
    if (!v && cur) call.me.video.srcObject = null;
    call.me.avatar.hidden = !!v && st.video;
  }

  async function joinCall(ev) {
    if (ev) ev.preventDefault();
    if (call.active) return;
    var room = $('call-room').value.trim();
    var name = $('call-name').value.trim();
    if (!ROOM_RE.test(room) || !NAME_RE.test(name) || (room + '-' + name).length > 128) {
      hint('call-hint', t('call.badname'), 'error');
      return;
    }
    if (needsCredentials('play')) { showLogin(t('auth.needed')); return; }
    save('localStorage', 'fitMediaRoom', room);
    save('localStorage', 'fitMediaName', name);
    var url = new URL(location.href);
    url.searchParams.set('room', room);
    try { history.replaceState(null, '', url.toString()); } catch (e) { /* nothing */ }

    call.active = true;
    call.gen++;
    call.room = room;
    call.name = name;
    call.key = load('sessionStorage', 'fitMediaKey:' + room + ':' + name) || '';
    call.members = {};
    clearChat();
    hint('call-hint', t('call.joining'));
    var local = call.local || newLocal(name, 'call-mic', 'call-cam');
    local.name = name;
    call.local = local;
    local.onchange = function () { mediaChanged(); };
    await local.probe();
    renderCallControls();

    var r = await sendJoin(false);
    if (!r) { await leaveCall(true); return; }
    call.me = new Tile(name, true);
    call.me.zoomable();
    $('call-tiles').appendChild(call.me.root);
    if (call.mayPublish) {
      call.pub = new Publisher(local, room + '-' + name, function (p) {
        // Announced as publishing only while a session exists, so that a
        // viewer's WHEP request finds the stream.
        var publishing = p.phase === 'live';
        if (publishing !== call.publishing) {
          call.publishing = publishing;
          sendState();
        }
        if (p.phase === 'refused') hint('call-hint', p.error, 'error');
        renderMe();
      });
      call.pub.start();
    } else {
      hint('call-hint', t('call.nopub'), 'error');
    }
    openEvents();
    call.heartbeat = setInterval(sendState, (S.conf.room && S.conf.room.heartbeatMs) || 10000);
    renderMe();
    renderCallControls();
    renderChatPanel();
    hint('call-hint', t('call.joined', { room: room, name: name }), 'ok');
  }

  function memberState() {
    var s = call.local.state();
    s.publishing = !!call.publishing;
    return s;
  }

  // sendJoin joins the room, or takes the name back with the key.
  async function sendJoin(again) {
    var body = memberState();
    body.name = call.name;
    body.key = call.key;
    var r = await api('rooms/' + encodeURIComponent(call.room) + '/join', body);
    if (r.status === 200 && r.data) {
      call.key = r.data.key;
      call.mayPublish = !!r.data.publish;
      save('sessionStorage', 'fitMediaKey:' + call.room + ':' + call.name, call.key);
      return true;
    }
    problem('join: ' + r.status);
    if (again) return false;
    if (r.status === 401) showLogin(t('auth.needed'));
    hint('call-hint', r.status === 409 ? t('call.taken') : r.status === 403 ? t('auth.denied') : (r.text || ('HTTP ' + r.status)), 'error');
    return false;
  }

  var stateBusy = false;
  var stateAgain = false;
  // sendState announces the member's state, and is its heartbeat; a member
  // the room forgot joins again.
  async function sendState() {
    if (!call.active || !call.key) return;
    if (stateBusy) { stateAgain = true; return; }
    stateBusy = true;
    try {
      do {
        stateAgain = false;
        var body = memberState();
        body.key = call.key;
        var gen = call.gen;
        var r = await api('rooms/' + encodeURIComponent(call.room) + '/members/' + encodeURIComponent(call.name), body);
        if (gen !== call.gen) break;
        if (r.status === 404) await sendJoin(true);
        else if (r.status !== 200) problem('state: ' + r.status);
      } while (stateAgain && call.active);
    } catch (e) {
      problem('state: ' + e);
    } finally {
      stateBusy = false;
    }
  }

  function openEvents() {
    if (!call.active) return;
    var u = 'api/rooms/' + encodeURIComponent(call.room) + '/events';
    // EventSource sends the login's cookie, but no header: a pasted token
    // goes in the query.
    if (token() && !(S.auth && S.auth.subject)) u += '?token=' + encodeURIComponent(token());
    var es = new EventSource(u);
    call.events = es;
    es.addEventListener('room', function (ev) {
      call.eventsRetry = 0;
      var data;
      try { data = JSON.parse(ev.data); } catch (e) { return; }
      call.seq = data.seq;
      onMembers(data.members || []);
    });
    // The room's chat, when it has one: its history first, then each new
    // message; a sent message comes back here too, and is dropped by its id.
    es.addEventListener('history', function (ev) {
      call.eventsRetry = 0;
      onChatHistory(ev.data);
    });
    es.addEventListener('chat', function (ev) {
      call.eventsRetry = 0;
      var m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      addChatMessage(m);
    });
    es.addEventListener('error', function () {
      if (es.readyState !== EventSource.CLOSED || call.events !== es) return;
      // Refused, or the server went: try again, more slowly each time.
      var ms = Math.min(2000 * Math.pow(2, call.eventsRetry++), 30000);
      problem('events closed');
      setTimeout(function () { if (call.active && call.events === es) openEvents(); }, ms);
    });
  }

  // onMembers reconciles the tiles with the room's members: a member's
  // stream is played once it is announced as published, and stopped once
  // it is not.
  function onMembers(list) {
    var seen = {};
    list.forEach(function (m) {
      if (m.name === call.name) return;
      seen[m.name] = true;
      var x = call.members[m.name];
      if (!x) {
        x = { name: m.name, state: m, tile: new Tile(m.name, false), viewer: null };
        x.tile.zoomable();
        call.members[m.name] = x;
        $('call-tiles').appendChild(x.tile.root);
      }
      var was = x.state;
      x.state = m;
      if (m.publishing && (!x.viewer || (!x.viewer.active && !was.publishing))) {
        if (x.viewer) x.viewer.stop();
        x.viewer = new Viewer(call.room + '-' + m.name, x.tile.video, function () { drawMember(x); });
        x.viewer.start();
      } else if (!m.publishing && x.viewer) {
        x.viewer.stop(true);
        x.viewer = null;
      }
      drawMember(x);
    });
    Object.keys(call.members).forEach(function (name) {
      if (seen[name]) return;
      var x = call.members[name];
      if (x.viewer) x.viewer.stop(true);
      if (call.spot === x.tile) spotlight(null);
      x.tile.root.remove();
      delete call.members[name];
    });
  }

  function drawMember(x) {
    x.tile.show(x.state, x.state.publishing ? callStatus(x.viewer) : t('tile.waiting'));
  }

  // mediaChanged puts the switches' tracks on the publisher, and announces
  // them.
  async function mediaChanged() {
    renderCallControls();
    if (call.pub) {
      try { await call.pub.apply(); } catch (e) { problem('replaceTrack: ' + e); }
    }
    renderMe();
    sendState();
  }

  async function toggle(local, what, after) {
    if (!local) return;
    if (what === 'screen') {
      if (!local.screenOn) {
        try { await local.startScreen(); local.screenOn = true; } catch (e) { problem('screen: ' + e.name); return; }
      } else {
        local.screenOn = false;
        local.stopScreen();
      }
    } else if (what === 'audio') {
      local.audioOn = !local.audioOn;
    } else {
      local.videoOn = !local.videoOn;
    }
    await after();
  }

  async function leaveCall(quiet) {
    if (!call.active) return;
    call.active = false;
    call.gen++;
    clearInterval(call.heartbeat);
    if (call.events) { call.events.close(); call.events = null; }
    if (call.pub) { call.pub.stop(); call.pub = null; }
    Object.keys(call.members).forEach(function (n) {
      var x = call.members[n];
      if (x.viewer) x.viewer.stop();
    });
    call.members = {};
    if (call.key) {
      await api('rooms/' + encodeURIComponent(call.room) + '/leave', { name: call.name, key: call.key }).catch(function () {});
      save('sessionStorage', 'fitMediaKey:' + call.room + ':' + call.name, null);
    }
    call.key = '';
    call.publishing = false;
    if (call.local) { call.local.close(); call.local = null; }
    clearChat();
    renderChatPanel();
    spotlight(null);
    call.me = null;
    $('call-tiles').textContent = '';
    renderCallControls();
    if (!quiet) hint('call-hint', t('call.left'));
  }

  /* ---------- the room chat ---------- */

  // The page keeps at most this many messages: the server's history is 64,
  // so this only bounds a long-lived window and a stream that keeps sending.
  var CHAT_MAX_MESSAGES = 200;

  var chatFileChosen = null; // the file picked, not sent yet

  function chatConf() { return (S.conf && S.conf.chat) || {}; }
  function chatEnabled() { return !!chatConf().enabled; }
  function chatMaxFile() { return Number(chatConf().maxFileBytes) || 0; }

  // formatBytes is a file's size for the page: 5 MiB, 68.4 KiB, 100 B.
  function formatBytes(n) {
    n = Number(n) || 0;
    var units = ['B', 'KiB', 'MiB', 'GiB'];
    var i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i === 0 || n >= 10 ? Math.round(n * 10) / 10 : n.toFixed(1)) + ' ' + units[i];
  }

  function timeText(at) {
    var d = new Date(Number(at) || Date.now());
    try { return d.toLocaleTimeString(); } catch (e) { return d.toISOString().slice(11, 19); }
  }

  // updateChatFilename shows the picked file, or that there is none.
  function updateChatFilename() {
    var e = $('chat-filename');
    if (e) e.textContent = chatFileChosen ? chatFileChosen.name : t('chat.nofile');
  }

  // renderChatLog draws every message again, as a language change does.
  function renderChatLog() {
    var log = $('chat-log');
    if (!log) return;
    log.textContent = '';
    call.chatMsgs.forEach(function (m) { log.appendChild(chatRow(m)); });
    renderChatEmpty();
    updateChatCount();
    log.scrollTop = log.scrollHeight;
  }

  function renderChatEmpty() {
    $('chat-empty').hidden = call.chatMsgs.length > 0;
  }

  function updateChatCount() {
    var b = $('chat-count');
    b.textContent = call.chatMsgs.length ? String(call.chatMsgs.length) : '';
    b.className = 'badge';
  }

  // renderChatPanel shows the chat only for a room call whose config has it.
  function renderChatPanel() {
    var on = chatEnabled() && call.active;
    $('call-chat').hidden = !on;
    $('call-stage').classList.toggle('call-stage--chat', on);
    if (on) {
      var max = Number((S.conf.room && S.conf.room.maxMessage) || 1024);
      $('chat-text').maxLength = Math.max(32, max - 16); // room for {"text":...} in the JSON body
      renderChatEmpty();
      updateChatCount();
    }
  }

  // chatRow builds one message: who and when, then its text, or a file's
  // name and size with a download control. User text only ever goes in with
  // textContent.
  function chatRow(m) {
    var row = el('div', 'chat__msg' + (m.from === call.name ? ' chat__msg--mine' : ''));
    if (m.id) row.dataset.id = String(m.id);
    var meta = el('div', 'chat__meta');
    meta.appendChild(el('span', 'chat__from', m.from || ''));
    meta.appendChild(el('span', 'chat__at', timeText(m.at)));
    row.appendChild(meta);
    if (m.file && typeof m.file === 'object') {
      var box = el('div', 'chat__file');
      box.appendChild(el('span', 'chat__file-name', m.file.name || 'file'));
      box.appendChild(el('span', 'chat__file-size', formatBytes(m.file.size)));
      var b = el('button', 'btn btn--sm btn--ghost chat__download', t('chat.download'));
      b.type = 'button';
      b.addEventListener('click', function () { downloadChatFile(m); });
      box.appendChild(b);
      row.appendChild(box);
    } else {
      row.appendChild(el('div', 'chat__text', m.text || ''));
    }
    return row;
  }

  // addChatMessage puts a message at the end, unless its id was drawn
  // already: the answer to a POST and its SSE echo are the same message. It
  // scrolls to the bottom only while the window is at the bottom.
  function addChatMessage(m) {
    if (!m || typeof m !== 'object') return false;
    var id = m.id == null ? '' : String(m.id);
    if (id) {
      if (call.chatIds[id]) return false;
      call.chatIds[id] = true;
    }
    call.chatMsgs.push(m);
    while (call.chatMsgs.length > CHAT_MAX_MESSAGES) {
      var old = call.chatMsgs.shift();
      if (old.id) delete call.chatIds[String(old.id)];
    }
    var log = $('chat-log');
    var atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 48;
    log.appendChild(chatRow(m));
    renderChatEmpty();
    updateChatCount();
    if (atBottom) log.scrollTop = log.scrollHeight;
    return true;
  }

  // onChatHistory fills the window from a history event, oldest first. A
  // reconnected stream sends it again, so the window starts over from it.
  function onChatHistory(raw) {
    var data;
    try { data = JSON.parse(raw); } catch (e) { return; }
    var list = data && data.messages;
    if (!Array.isArray(list)) return;
    call.chatMsgs = [];
    call.chatIds = {};
    $('chat-log').textContent = '';
    list.forEach(function (m) { addChatMessage(m); });
    $('chat-log').scrollTop = $('chat-log').scrollHeight;
  }

  // clearChat empties the window: leaving a room clears the chat.
  function clearChat() {
    call.chatMsgs = [];
    call.chatIds = {};
    $('chat-log').textContent = '';
    hint('chat-hint', '');
    $('chat-file').value = '';
    chatFileChosen = null;
    updateChatFilename();
    renderChatEmpty();
    updateChatCount();
  }

  // chatRequest posts to the room's chat API with the member's headers: the
  // page's credentials go with it, and the member's name and key say who
  // sends.
  async function chatRequest(path, opts) {
    opts = opts || {};
    var h = headers(opts.headers || {});
    h['X-Fit-Member'] = call.name;
    h['X-Fit-Room-Key'] = call.key;
    opts.method = opts.method || 'POST';
    opts.credentials = 'same-origin';
    opts.headers = h;
    var res = await fetch('api/rooms/' + encodeURIComponent(call.room) + '/' + path, opts);
    var text = await res.text().catch(function () { return ''; });
    var data = null;
    try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
    return { status: res.status, data: data, text: text };
  }

  function chatError(r) {
    var msg = (r.data && (r.data.error || r.data.message)) || (r.text || '').trim();
    return msg || ('HTTP ' + r.status);
  }

  // sendChatText sends a text message and shows it at once from the answer;
  // the SSE echo of it is dropped by its id. It resolves with the message,
  // and with null when the message could not be sent.
  async function sendChatText(text) {
    text = text == null ? '' : String(text);
    if (!text.trim()) return null;
    if (!call.active || !call.key || !chatEnabled()) {
      hint('chat-hint', t('chat.nojoin'), 'error');
      return null;
    }
    try {
      var r = await chatRequest('chat', { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text: text }) });
      if (r.status === 200 && r.data && r.data.id) {
        hint('chat-hint', '');
        addChatMessage(r.data);
        return r.data;
      }
      hint('chat-hint', t('chat.sendfail', { e: chatError(r) }), 'error');
      problem('chat: ' + r.status);
      return null;
    } catch (e) {
      hint('chat-hint', t('chat.sendfail', { e: String((e && e.message) || e) }), 'error');
      problem('chat: ' + e);
      return null;
    }
  }

  // fileType guesses a file's Content-Type from its name; the server stores
  // it and says it back on the download.
  function fileType(name) {
    var ext = (String(name).split('.').pop() || '').toLowerCase();
    var types = {
      txt: 'text/plain', md: 'text/markdown', csv: 'text/csv', json: 'application/json',
      png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp',
      svg: 'image/svg+xml', pdf: 'application/pdf', zip: 'application/zip',
    };
    return types[ext] || 'application/octet-stream';
  }

  // chatBytes takes the forms a test or the picker may give: a file, bytes,
  // an array of byte values, a string, or a number of bytes.
  async function chatBytes(bytes) {
    if (bytes == null) return new Uint8Array(0);
    if (bytes instanceof Uint8Array) return bytes;
    if (bytes instanceof ArrayBuffer) return new Uint8Array(bytes);
    if (Array.isArray(bytes)) return Uint8Array.from(bytes);
    if (typeof bytes === 'number') {
      var n = Math.max(0, Math.min(Math.floor(bytes), 1 << 28));
      var b = new Uint8Array(n);
      for (var i = 0; i < n; i++) b[i] = (i * 7 + 3) & 255;
      return b;
    }
    if (typeof bytes === 'string') return new TextEncoder().encode(bytes);
    if (typeof Blob !== 'undefined' && bytes instanceof Blob) return new Uint8Array(await bytes.arrayBuffer());
    try { return new Uint8Array(bytes); } catch (e) { return new Uint8Array(0); }
  }

  // sendChatFile sends a file, refusing one larger than conf.chat.maxFileBytes
  // before it goes, with a hint. It resolves with the file's message.
  async function sendChatFile(name, bytes, type) {
    name = String(name == null ? '' : name).trim();
    var data = await chatBytes(bytes);
    var max = chatMaxFile();
    if (max > 0 && data.length > max) {
      var tooBig = t('chat.toobig', { size: formatBytes(data.length), max: formatBytes(max) });
      hint('chat-hint', tooBig, 'error');
      throw new Error(tooBig);
    }
    if (!name) {
      hint('chat-hint', t('chat.nofile'), 'error');
      throw new Error(t('chat.nofile'));
    }
    if (!call.active || !call.key || !chatEnabled()) {
      hint('chat-hint', t('chat.nojoin'), 'error');
      throw new Error(t('chat.nojoin'));
    }
    var shown = false; // the answer's error was shown already
    try {
      var r = await chatRequest('files?file=' + encodeURIComponent(name), {
        headers: { 'Content-Type': type || fileType(name) }, body: data,
      });
      if (r.status === 200 && r.data && r.data.id) {
        hint('chat-hint', '');
        addChatMessage(r.data);
        return r.data;
      }
      hint('chat-hint', t('chat.filefail', { e: chatError(r) }), 'error');
      shown = true;
      problem('files: ' + r.status);
      throw new Error(chatError(r));
    } catch (e) {
      if (!shown) {
        // A network failure, not the answer's error: say so.
        hint('chat-hint', t('chat.filefail', { e: String((e && e.message) || e) }), 'error');
        problem('files: ' + e);
      }
      throw e;
    }
  }

  // downloadChatFile fetches a file with the page's credentials (its cookie,
  // or a pasted token as Authorization) and saves the answer as a blob under
  // the file's name.
  async function downloadChatFile(m) {
    if (!m || !m.id || !call.active) return;
    try {
      var res = await fetch('api/rooms/' + encodeURIComponent(call.room) + '/files/' + encodeURIComponent(String(m.id)), {
        credentials: 'same-origin', headers: headers({}),
      });
      if (!res.ok) throw new Error('HTTP ' + res.status);
      var blob = await res.blob();
      var name = (m.file && m.file.name) || String(m.id);
      var url = URL.createObjectURL(blob);
      var a = el('a');
      a.href = url;
      a.download = name;
      a.rel = 'noopener';
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () { URL.revokeObjectURL(url); }, 10000);
    } catch (e) {
      hint('chat-hint', t('chat.downloadfail', { e: String((e && e.message) || e) }), 'error');
      problem('download: ' + e);
    }
  }

  // chatMessages is what the page has of the chat, for the tests.
  function chatMessages() {
    return call.chatMsgs.map(function (m) {
      return {
        id: m.id == null ? '' : String(m.id),
        from: m.from || '',
        text: m.file ? '' : (m.text || ''),
        file: m.file && typeof m.file === 'object' ? { name: m.file.name || '', size: Number(m.file.size) || 0, type: m.file.type || '' } : null,
        at: Number(m.at) || 0,
        atText: timeText(m.at),
      };
    });
  }

  /* ---------- publish and play pages ---------- */

  var pubPage = { local: null, pub: null, tile: null };

  function renderPub() {
    var p = pubPage.pub;
    var active = !!(p && p.active);
    $('pub-start').disabled = active;
    $('pub-stop').disabled = !active;
    $('pub-stream').disabled = active;
    var l = pubPage.local;
    press('pub-mic', l ? l.audioOn : true);
    press('pub-cam', l ? l.videoOn : true);
    press('pub-screen', l ? l.screenOn : false);
    var b = $('pub-state');
    var phase = p ? p.phase : 'off';
    b.textContent = t('pub.' + (phase === 'refused' ? 'off' : phase));
    b.className = 'badge' + (phase === 'live' ? ' ok' : phase === 'off' ? '' : ' warn');
    if (p && p.error) hint('pub-hint', p.error, 'error');
    else hint('pub-hint', '');
    if (pubPage.tile && l) {
      var v = p && p.video && p.video.sender.track;
      if (v && (!pubPage.tile.video.srcObject || pubPage.tile.video.srcObject.getVideoTracks()[0] !== v)) pubPage.tile.video.srcObject = new MediaStream([v]);
      if (!v) pubPage.tile.video.srcObject = null;
      pubPage.tile.show(l.state(), '');
    }
    renderLinks('pub-links', $('pub-stream').value.trim());
  }

  async function startPub() {
    var stream = $('pub-stream').value.trim();
    if (!STREAM_RE.test(stream)) { hint('pub-hint', t('call.badname'), 'error'); return; }
    if (needsCredentials('publish')) { showLogin(t('auth.needed')); return; }
    save('localStorage', 'fitMediaStream', stream);
    var l = pubPage.local || newLocal(stream, 'pub-mic', 'pub-cam');
    l.name = stream;
    pubPage.local = l;
    l.onchange = pubChanged;
    await l.probe();
    pubPage.tile = new Tile(stream, true);
    $('pub-tile').textContent = '';
    $('pub-tile').appendChild(pubPage.tile.root);
    pubPage.pub = new Publisher(l, stream, function (p) {
      if (p.phase === 'refused' && p.error === t('auth.needed')) showLogin(p.error);
      renderPub();
    });
    pubPage.pub.start();
    renderPub();
  }

  async function pubChanged() {
    if (pubPage.pub) {
      try { await pubPage.pub.apply(); } catch (e) { problem('replaceTrack: ' + e); }
    }
    renderPub();
  }

  function stopPub() {
    if (pubPage.pub) { pubPage.pub.stop(); pubPage.pub = null; }
    if (pubPage.local) { pubPage.local.close(); pubPage.local = null; }
    $('pub-tile').textContent = '';
    pubPage.tile = null;
    renderPub();
  }

  var playPage = { viewer: null, tile: null, timer: 0, hls: false, hlsErr: null, hlsTimer: 0, hlsTries: 0 };

  function playTile(name) {
    playPage.tile = new Tile(name, false);
    $('play-tile').textContent = '';
    $('play-tile').appendChild(playPage.tile.root);
    playPage.tile.avatar.hidden = true;
  }

  function renderPlay() {
    var v = playPage.viewer;
    var active = !!(v && v.active) || !!playPage.hls;
    $('play-start').disabled = active;
    $('play-stop').disabled = !active;
    $('play-stream').disabled = active;
    var stream = $('play-stream').value.trim();
    if (v) {
      switch (v.phase) {
        case 'live': hint('play-hint', t('play.playing', { stream: v.stream }), 'ok'); break;
        case 'retry':
          if (v.notFound) hint('play-hint', t('play.waiting', { s: Math.max(1, Math.round((v.nextTry - Date.now()) / 1000)) }));
          else hint('play-hint', v.error, 'error');
          break;
        case 'gaveup': hint('play-hint', v.notFound ? t('play.gaveup') : v.error, 'error'); break;
        case 'refused':
          hint('play-hint', v.error, 'error');
          if (v.error === t('auth.needed')) showLogin(v.error);
          break;
      }
      if (playPage.tile) {
        var hasVideo = v.videoEl.srcObject && v.videoEl.srcObject.getVideoTracks().length > 0;
        playPage.tile.avatar.hidden = !!hasVideo;
        playPage.tile.stateEl.textContent = callStatus(v);
      }
    }
    renderLinks('play-links', stream);
  }

  function startPlay(ev) {
    if (ev) ev.preventDefault();
    var stream = $('play-stream').value.trim();
    if (!STREAM_RE.test(stream)) { hint('play-hint', t('call.badname'), 'error'); return; }
    if (needsCredentials('play')) { showLogin(t('auth.needed')); return; }
    save('localStorage', 'fitMediaStream', stream);
    playTile(stream);
    if ($('play-mode').value === 'hls' && S.conf.hls) {
      var video = playPage.tile.video;
      if (!video.canPlayType('application/vnd.apple.mpegurl')) {
        hint('play-hint', t('play.nohls'), 'error');
        return;
      }
      var u = endpoint(S.conf.hls, stream);
      if (token() && !(S.auth && S.auth.subject)) u += (u.indexOf('?') < 0 ? '?' : '&') + 'token=' + encodeURIComponent(token());
      // An HLS load fails now and then, as the playlist of a stream that
      // has just started has no segment to parse yet: load it again.
      playPage.hlsTries = 0;
      playPage.hlsErr = function () {
        if (!playPage.hls || playPage.hlsTries >= 10) return;
        playPage.hlsTries++;
        clearTimeout(playPage.hlsTimer);
        playPage.hlsTimer = setTimeout(function () {
          if (!playPage.hls || !playPage.tile) return;
          playPage.tile.video.load();
          playWhenReady(playPage.tile.video);
        }, 1000);
      };
      video.addEventListener('error', playPage.hlsErr);
      video.src = u;
      playWhenReady(video);
      playPage.hls = true;
      renderPlay();
      return;
    }
    playPage.viewer = new Viewer(stream, playPage.tile.video, renderPlay);
    playPage.viewer.start();
    clearInterval(playPage.timer);
    playPage.timer = setInterval(renderPlay, 1000);
    renderPlay();
  }

  function stopPlay() {
    if (playPage.viewer) { playPage.viewer.stop(); playPage.viewer = null; }
    if (playPage.tile) {
      if (playPage.hlsErr) playPage.tile.video.removeEventListener('error', playPage.hlsErr);
      playPage.tile.video.removeAttribute('src');
      playPage.tile.video.load();
    }
    playPage.hls = false;
    playPage.hlsErr = null;
    clearTimeout(playPage.hlsTimer);
    clearInterval(playPage.timer);
    $('play-tile').textContent = '';
    playPage.tile = null;
    hint('play-hint', '');
    renderPlay();
  }

  function renderLinks(id, stream) {
    var dl = $(id);
    dl.textContent = '';
    if (!S.conf || !STREAM_RE.test(stream)) return;
    var list = [
      { name: 'WHIP', url: absolute(endpoint(S.conf.whip, stream)) },
      { name: 'WHEP', url: absolute(endpoint(S.conf.whep, stream)) },
    ];
    if (S.conf.hls) list.push({ name: 'HLS', url: fill(S.conf.hls, stream) });
    (S.conf.links || []).forEach(function (l) { list.push({ name: l.name, url: fill(l.url, stream) }); });
    if (token()) list.push({ name: t('auth.token'), url: token() });
    list.forEach(function (l) {
      dl.appendChild(el('dt', '', l.name));
      var dd = el('dd');
      dd.appendChild(el('code', '', l.url));
      var b = el('button', 'btn btn--sm btn--ghost', t('copy'));
      b.type = 'button';
      b.addEventListener('click', function () {
        if (navigator.clipboard) navigator.clipboard.writeText(l.url).then(function () { toast(t('copied')); }, function () {});
      });
      dd.appendChild(b);
      dl.appendChild(dd);
    });
  }

  /* ---------- the library and the player ---------- */

  // The library page lists what the viewer may play, a page of each
  // library's items at a time and the channels; the player page asks
  // api/play for the URL of one, a playback URL signed for the viewer when
  // the policy signs them, and plays it with hls.js (vendor/hls.min.js,
  // loaded once it is needed), or with the browser's own HLS where there
  // are no Media Source Extensions. A URL the server refuses, as once it and
  // the grant its playlists handed on have expired, stops the player, which
  // offers to play again with a new one.

  var PAGE_ITEMS = 60;
  var libPage = { q: '', shelves: {}, gen: 0 };
  var watchPage = { hls: null, gen: 0, state: '', url: '', signed: false, expires: 0, kind: '', title: '', refused: false, error: '' };

  function libraryOn() { return !!(S.conf && S.conf.library); }

  function pad2(n) { return (n < 10 ? '0' : '') + n; }
  function formatDuration(s) {
    s = Math.max(0, Math.round(s || 0));
    var h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), x = s % 60;
    return h ? h + ':' + pad2(m) + ':' + pad2(x) : m + ':' + pad2(x);
  }

  // go shows a view at a URL of the pages with a query of its own.
  function go(v, search) { show(v, true, search); }

  function libraryHint(r) {
    if (r.status === 401) { showLogin(t('auth.needed')); return t('auth.needed'); }
    if (r.status === 403) return t('auth.denied');
    return r.text ? r.text.trim() : 'HTTP ' + r.status;
  }

  async function renderLibrary() {
    if (!libraryOn()) return;
    var gen = ++libPage.gen;
    $('lib-q').value = libPage.q;
    if (needsCredentials('play')) {
      hint('lib-hint', t('auth.needed'), 'error');
      showLogin(t('auth.needed'));
      return;
    }
    hint('lib-hint', t('lib.loading'));
    var r = await api('library');
    if (gen !== libPage.gen) return;
    if (r.status !== 200 || !r.data) { hint('lib-hint', libraryHint(r), 'error'); return; }
    hint('lib-hint', '');
    var chans = $('lib-channels');
    chans.textContent = '';
    $('lib-channels-card').hidden = !S.conf.library.channels;
    var q = libPage.q.toLowerCase();
    r.data.channels.filter(function (ch) { return !q || ch.name.toLowerCase().indexOf(q) >= 0 || (ch.now || '').toLowerCase().indexOf(q) >= 0; })
      .forEach(function (ch) { chans.appendChild(channelCard(ch)); });
    if (!chans.firstChild) chans.appendChild(el('p', 'muted', t('lib.nochannels')));
    var box = $('lib-shelves');
    box.textContent = '';
    libPage.shelves = {};
    r.data.libraries.forEach(function (l) {
      var card = el('div', 'card');
      var head = el('div', 'card__head card__head--row');
      var count = el('span', 'badge');
      head.appendChild(el('h3', '', l.name));
      head.appendChild(count);
      var body = el('div', 'card__body');
      var grid = el('div', 'shelf');
      var note = el('p', 'muted');
      var more = el('button', 'btn btn--sm shelf__more', t('lib.more'));
      more.type = 'button';
      more.hidden = true;
      body.appendChild(grid);
      body.appendChild(note);
      body.appendChild(more);
      card.appendChild(head);
      card.appendChild(body);
      card.setAttribute('data-library', l.name);
      box.appendChild(card);
      var sh = { name: l.name, grid: grid, note: note, more: more, count: count, offset: 0, total: 0, tries: 0 };
      libPage.shelves[l.name] = sh;
      more.addEventListener('click', function () { loadShelf(sh, gen); });
      loadShelf(sh, gen);
    });
  }

  async function loadShelf(sh, gen) {
    var path = 'library/' + encodeURIComponent(sh.name) + '?offset=' + sh.offset + '&limit=' + PAGE_ITEMS +
      (libPage.q ? '&q=' + encodeURIComponent(libPage.q) : '');
    var r = await api(path);
    if (gen !== libPage.gen) return;
    // A library not scanned yet is asked again, a bounded number of times.
    if (r.status === 503 && sh.tries < 30) {
      sh.tries++;
      sh.note.textContent = t('lib.notready');
      setTimeout(function () { if (gen === libPage.gen) loadShelf(sh, gen); }, Math.max(1, r.retryAfter) * 1000);
      return;
    }
    if (r.status !== 200 || !r.data) { sh.note.textContent = libraryHint(r); return; }
    sh.note.textContent = '';
    r.data.items.forEach(function (it) { sh.grid.appendChild(itemCard(sh.name, it)); });
    sh.offset += r.data.items.length;
    sh.total = r.data.total;
    sh.count.textContent = t('lib.count', { n: sh.total });
    sh.more.hidden = sh.offset >= sh.total;
    if (!sh.total) sh.note.textContent = t('lib.empty');
  }

  function posterCard(href, art, title, meta, onclick) {
    var a = el('a', 'poster');
    a.href = href;
    a.title = title;
    a.appendChild(art);
    a.appendChild(el('span', 'poster__title', title));
    a.appendChild(el('span', 'poster__meta', meta));
    a.addEventListener('click', function (ev) {
      if (ev.metaKey || ev.ctrlKey || ev.shiftKey) return;
      ev.preventDefault();
      onclick();
    });
    return a;
  }

  function itemCard(lib, it) {
    var art = el('span', 'poster__art', (it.title || it.id).charAt(0).toUpperCase());
    if (it.poster) {
      // The poster is the vod handler's, let in by the login's cookie; one
      // that does not load leaves the title's initial.
      var img = el('img');
      img.alt = '';
      img.loading = 'lazy';
      img.addEventListener('error', function () { img.remove(); });
      img.src = it.poster;
      art.appendChild(img);
    }
    var meta = [formatDuration(it.duration)];
    if (it.height) meta.push(it.height + 'p');
    if (it.renditions.length > 1) meta.push(it.renditions.length + '×');
    var search = '?library=' + encodeURIComponent(lib) + '&id=' + encodeURIComponent(it.id);
    var card = posterCard('watch' + search, art, it.title || it.id, meta.join(' · '), function () { go('watch', search); });
    card.setAttribute('data-item', it.id);
    return card;
  }

  function channelCard(ch) {
    var art = el('span', 'poster__art poster__art--live', t('lib.live'));
    var meta = ch.now ? t('lib.now', { t: ch.now }) : ch.state;
    if (ch.next) meta += ' · ' + t('lib.next', { t: ch.next });
    var search = '?channel=' + encodeURIComponent(ch.name);
    var card = posterCard('watch' + search, art, ch.name, meta, function () { go('watch', search); });
    card.setAttribute('data-channel', ch.name);
    return card;
  }

  // loadHls loads hls.js once, from the pages' own copy.
  var hlsScript = null;
  function loadHls() {
    if (window.Hls) return Promise.resolve(true);
    if (!hlsScript) {
      hlsScript = new Promise(function (resolve) {
        var s = document.createElement('script');
        s.src = 'vendor/hls.min.js';
        s.onload = function () { resolve(!!window.Hls); };
        s.onerror = function () { hlsScript = null; resolve(false); };
        document.head.appendChild(s);
      });
    }
    return hlsScript;
  }

  function renderWatch() {
    var w = watchPage;
    $('watch-title').textContent = w.title;
    var v = $('watch-video');
    var state = '';
    if (w.refused) state = 'refused';
    else if (!w.error && w.state === 'started') state = v.ended ? 'ended' : v.paused ? 'paused' : 'playing';
    var badge = $('watch-state');
    badge.textContent = state ? t('watch.' + state) : '';
    badge.className = 'badge' + (state === 'refused' ? ' err' : state === 'playing' ? ' ok' : '');
    $('watch-again').hidden = !(w.refused || w.error);
    if (w.refused) hint('watch-hint', t('watch.expired'), 'error');
    else if (w.error) hint('watch-hint', w.error, 'error');
    else if (w.state === 'loading') hint('watch-hint', t('watch.loading'));
    else if (w.signed && w.expires) hint('watch-hint', t('watch.until', { t: new Date(w.expires * 1000).toLocaleTimeString() }));
    else hint('watch-hint', '');
  }

  function stopWatch() {
    watchPage.gen++;
    if (watchPage.hls) { try { watchPage.hls.destroy(); } catch (e) { /* gone already */ } }
    watchPage.hls = null;
    var v = $('watch-video');
    if (v.getAttribute('src') || v.currentSrc) {
      v.removeAttribute('src');
      try { v.load(); } catch (e) { /* nothing loaded */ }
    }
  }

  // refuse stops a player the server refused: its URL, and the grant its
  // playlists handed on, expired or were ended.
  function refuse(status) {
    if (watchPage.refused) return;
    stopWatch();
    watchPage.refused = true;
    problem('playback refused: HTTP ' + status);
    renderWatch();
  }

  async function startWatch() {
    stopWatch();
    var gen = watchPage.gen;
    var p = new URLSearchParams(location.search);
    var body = p.get('channel') ? { channel: p.get('channel') } : { library: p.get('library') || '', id: p.get('id') || '' };
    Object.assign(watchPage, { state: 'loading', url: '', signed: false, expires: 0, refused: false, error: '',
      kind: body.channel ? 'channel' : 'vod', title: body.channel || body.id });
    renderWatch();
    if (needsCredentials('play')) {
      watchPage.error = t('auth.needed');
      renderWatch();
      showLogin(t('auth.needed'));
      return;
    }
    var r = await api('play', body);
    if (gen !== watchPage.gen) return;
    if (r.status !== 200 || !r.data) { watchPage.state = ''; watchPage.error = libraryHint(r); renderWatch(); return; }
    Object.assign(watchPage, { url: r.data.url, signed: r.data.signed, expires: r.data.expires || 0, title: r.data.title, kind: r.data.kind });
    var hasHls = await loadHls();
    if (gen !== watchPage.gen) return;
    var v = $('watch-video');
    var url = r.data.url;
    watchPage.state = 'started';
    if (hasHls && window.Hls.isSupported()) {
      var sameOrigin = new URL(url, location.href).origin === location.origin;
      var tok = !r.data.signed && sameOrigin && token() ? token() : '';
      var h = new window.Hls({
        // A token pasted in, rather than the login's cookie, goes along as
        // a header; a signed URL carries its own grant.
        xhrSetup: function (xhr) { if (tok) xhr.setRequestHeader('Authorization', 'Bearer ' + tok); },
      });
      watchPage.hls = h;
      var recoveries = 0;
      h.on(window.Hls.Events.ERROR, function (_, d) {
        if (gen !== watchPage.gen) return;
        var code = d && d.response && d.response.code;
        if (code === 401 || code === 403) { refuse(code); return; }
        if (d && d.fatal) {
          if (d.type === window.Hls.ErrorTypes.MEDIA_ERROR && recoveries++ < 2) { h.recoverMediaError(); return; }
          problem('hls: ' + d.details);
          stopWatch();
          watchPage.error = t('watch.error', { e: d.details });
          renderWatch();
        }
      });
      h.on(window.Hls.Events.MANIFEST_PARSED, function () { if (gen === watchPage.gen) playWhenReady(v); });
      h.attachMedia(v);
      h.loadSource(url);
    } else if (v.canPlayType('application/vnd.apple.mpegurl')) {
      v.src = url;
      playWhenReady(v);
    } else {
      watchPage.error = t('watch.nohls');
    }
    renderWatch();
  }

  /* ---------- views ---------- */

  var view = 'home';

  function viewOf(path) {
    var base = new URL(document.baseURI).pathname;
    var rest = path.indexOf(base) === 0 ? path.slice(base.length) : '';
    if (rest === 'library' || rest === 'watch') return libraryOn() ? rest : 'home';
    return rest === 'call' || rest === 'publish' || rest === 'play' ? rest : 'home';
  }

  function show(v, push, search) {
    if (view === 'watch' && v !== 'watch') stopWatch();
    view = v;
    ['home', 'call', 'publish', 'play', 'library', 'watch'].forEach(function (n) { $('view-' + n).hidden = n !== v; });
    document.querySelectorAll('.tab').forEach(function (a) { a.classList.toggle('is-active', a.getAttribute('data-nav') === v); });
    if (push) {
      var u = new URL(v === 'home' ? './' : v, document.baseURI);
      u.search = search !== undefined ? search : location.search;
      try { history.pushState(null, '', u.toString()); } catch (e) { /* nothing */ }
    }
    if (v === 'home') renderHome();
    if (v === 'publish') renderPub();
    if (v === 'play') renderPlay();
    if (v === 'library') renderLibrary();
    if (v === 'watch') startWatch();
  }

  function renderHome() {
    var c = S.conf;
    renderAuth();
    if (!c) return;
    var dl = $('home-info');
    dl.textContent = '';
    function row(k, v) { dl.appendChild(el('dt', '', k)); var dd = el('dd'); dd.appendChild(el('code', '', v)); dl.appendChild(dd); }
    // The templates as they are, {stream} unescaped.
    function shown(tmpl) { return tmpl.charAt(0) === '/' && tmpl.charAt(1) !== '/' ? location.origin + tmpl : tmpl; }
    row('WHIP', shown(c.whip));
    row('WHEP', shown(c.whep));
    if (c.hls) row('HLS', shown(c.hls));
    var needed = [];
    if (c.auth && !c.public.publish) needed.push('publish');
    if (c.auth && !c.public.play) needed.push('play');
    row(t('home.auth'), needed.length ? t('home.needed') + ' ' + needed.join(', ') : t('home.none'));
    if (S.auth && S.auth.publish) {
      row(t('home.you'), (S.auth.subject || '') + ': ' + t('home.may', { pub: S.auth.publish.join(' ') || '-', play: S.auth.play.join(' ') || '-' }));
    } else if (c.auth) {
      row(t('home.you'), token() ? t('auth.token') : t('home.anon'));
    }
  }

  /* ---------- start ---------- */

  async function init() {
    lang = load('localStorage', LANG_KEY) || ((navigator.language || '').toLowerCase().indexOf('zh') === 0 ? 'zh' : 'en');
    applyText();
    loadAuth();
    var r = await api('config');
    if (r.status !== 200 || !r.data) {
      hint('call-hint', 'config: HTTP ' + r.status, 'error');
      return;
    }
    S.conf = r.data;
    document.title = S.conf.title;
    $('play-mode-field').hidden = !S.conf.hls;
    $('nav-library').hidden = !libraryOn();
    $('home-library').hidden = !libraryOn();
    renderAuth();
    $('login-token-field').hidden = false;

    var params = new URLSearchParams(location.search);
    $('call-room').value = params.get('room') || load('localStorage', 'fitMediaRoom') || 'lobby';
    $('call-name').value = params.get('name') || load('localStorage', 'fitMediaName') || (S.auth && NAME_RE.test(S.auth.subject || '') ? S.auth.subject : randomName());
    $('pub-stream').value = params.get('stream') || load('localStorage', 'fitMediaStream') || $('call-name').value;
    $('play-stream').value = params.get('stream') || load('localStorage', 'fitMediaStream') || '';

    document.querySelectorAll('[data-nav]').forEach(function (a) {
      a.addEventListener('click', function (ev) {
        if (ev.metaKey || ev.ctrlKey || ev.shiftKey) return;
        ev.preventDefault();
        show(a.getAttribute('data-nav'), true);
      });
    });
    window.addEventListener('popstate', function () { show(viewOf(location.pathname), false); });
    $('lang').addEventListener('click', function () {
      lang = lang === 'zh' ? 'en' : 'zh';
      save('localStorage', LANG_KEY, lang);
      applyText();
      show(view, false);
    });
    $('login-open').addEventListener('click', function () { showLogin(''); });
    $('logout').addEventListener('click', logout);
    $('login-form').addEventListener('submit', submitLogin);
    $('login-cancel').addEventListener('click', function () { $('login').close(); });

    $('call-form').addEventListener('submit', joinCall);
    $('call-leave').addEventListener('click', function () { leaveCall(false); });
    $('call-mic').addEventListener('click', function () { if (call.local) toggle(call.local, 'audio', mediaChanged); else flip('call-mic'); });
    $('call-cam').addEventListener('click', function () { if (call.local) toggle(call.local, 'video', mediaChanged); else flip('call-cam'); });
    $('call-screen').addEventListener('click', function () { if (call.local) toggle(call.local, 'screen', mediaChanged); });

    $('chat-form').addEventListener('submit', async function (ev) {
      ev.preventDefault();
      var input = $('chat-text');
      if (!input.value.trim()) return;
      var m = await sendChatText(input.value);
      if (m) input.value = '';
    });
    $('chat-file').addEventListener('change', function () {
      var f = this.files && this.files[0];
      chatFileChosen = f || null;
      updateChatFilename();
      if (f && chatMaxFile() > 0 && f.size > chatMaxFile()) {
        hint('chat-hint', t('chat.toobig', { size: formatBytes(f.size), max: formatBytes(chatMaxFile()) }), 'error');
        this.value = '';
        chatFileChosen = null;
        updateChatFilename();
      } else {
        hint('chat-hint', '');
      }
    });
    $('chat-file-send').addEventListener('click', async function () {
      var f = $('chat-file').files && $('chat-file').files[0];
      if (!f) { hint('chat-hint', t('chat.nofile'), 'error'); return; }
      try {
        await sendChatFile(f.name, f, f.type);
        $('chat-file').value = '';
        chatFileChosen = null;
        updateChatFilename();
      } catch (e) { /* the hint is shown */ }
    });

    $('pub-start').addEventListener('click', startPub);
    $('pub-stop').addEventListener('click', stopPub);
    $('pub-stream').addEventListener('input', function () { renderLinks('pub-links', $('pub-stream').value.trim()); });
    $('pub-mic').addEventListener('click', function () { if (pubPage.local) toggle(pubPage.local, 'audio', pubChanged); else flip('pub-mic'); });
    $('pub-cam').addEventListener('click', function () { if (pubPage.local) toggle(pubPage.local, 'video', pubChanged); else flip('pub-cam'); });
    $('pub-screen').addEventListener('click', function () { if (pubPage.local) toggle(pubPage.local, 'screen', pubChanged); });

    $('lib-form').addEventListener('submit', function (ev) {
      ev.preventDefault();
      libPage.q = $('lib-q').value.trim();
      renderLibrary();
    });
    $('watch-again').addEventListener('click', function () { startWatch(); });
    $('watch-video').addEventListener('play', renderWatch);
    $('watch-video').addEventListener('pause', renderWatch);
    $('watch-video').addEventListener('ended', renderWatch);
    $('play-form').addEventListener('submit', startPlay);
    $('play-stop').addEventListener('click', stopPlay);
    $('play-stream').addEventListener('input', function () { renderLinks('play-links', $('play-stream').value.trim()); });

    // A click anywhere plays what the browser held back until a gesture.
    document.addEventListener('click', function () {
      document.querySelectorAll('video').forEach(function (v) { if ((v.srcObject || v.currentSrc) && v.paused) playWhenReady(v); });
    });
    window.addEventListener('pagehide', function () {
      if (call.active && call.key && navigator.sendBeacon) {
        var body = new Blob([JSON.stringify({ name: call.name, key: call.key })], { type: 'text/plain' });
        navigator.sendBeacon('api/rooms/' + encodeURIComponent(call.room) + '/leave', body);
      }
      if (call.pub) hangUp(call.pub.location);
      if (pubPage.pub) hangUp(pubPage.pub.location);
    });

    S.joinDefaults = joinDefaults();
    renderCallControls();
    // The URL's mic=0|1 and cam=0|1 also set the publish page's switches.
    var micParam = params.get('mic'), camParam = params.get('cam');
    if (micParam === '0' || micParam === '1') press('pub-mic', micParam === '1');
    if (camParam === '0' || camParam === '1') press('pub-cam', camParam === '1');
    clearChat();
    renderChatPanel();
    show(viewOf(location.pathname), false);
    if (needsCredentials('play') && (needsCredentials('publish') || view === 'library' || view === 'watch')) showLogin('');
    if (params.get('auto') === '1' && view === 'call') joinCall();
  }

  // joinDefaults is how the microphone and camera start when a member joins:
  // conf.call's mic and cam, off unless configured, and the URL's mic=0|1 and
  // cam=0|1 over them.
  function joinDefaults() {
    var c = (S.conf && S.conf.call) || {};
    var d = { mic: c.mic === true, cam: c.cam === true };
    var p = new URLSearchParams(location.search);
    if (p.get('mic') === '1') d.mic = true;
    else if (p.get('mic') === '0') d.mic = false;
    if (p.get('cam') === '1') d.cam = true;
    else if (p.get('cam') === '0') d.cam = false;
    return d;
  }

  // newLocal opens a Local with the switches as they are before it starts:
  // conf.call's defaults, the URL's mic=0|1 and cam=0|1 over them, and what
  // the user flipped before joining.
  function newLocal(name, mic, cam) {
    var l = new Local(name);
    l.audioOn = $(mic).getAttribute('aria-pressed') === 'true';
    l.videoOn = $(cam).getAttribute('aria-pressed') === 'true';
    return l;
  }

  // flip turns a switch before the media is open: the call starts so.
  function flip(id) {
    var on = $(id).getAttribute('aria-pressed') !== 'true';
    press(id, on);
  }

  /* ---------- what the tests read ---------- */

  window.fitMedia = {
    version: 1,
    join: function (room, name) {
      if (room) $('call-room').value = room;
      if (name) $('call-name').value = name;
      return joinCall();
    },
    leave: function () { return leaveCall(false); },
    spot: function (name) {
      var x = name === call.name ? { tile: call.me } : call.members[name];
      spotlight(x ? x.tile : null);
    },
    mic: function (on) { if (call.local && call.local.audioOn !== on) return toggle(call.local, 'audio', mediaChanged); },
    cam: function (on) { if (call.local && call.local.videoOn !== on) return toggle(call.local, 'video', mediaChanged); },
    chat: function (text) { return sendChatText(text); },
    file: function (name, bytes) { return sendChatFile(name, bytes); },
    messages: function () { return chatMessages(); },
    chatEnabled: function () { return chatEnabled(); },
    // The player page: what it plays, and how far.
    watch: function () {
      var v = $('watch-video');
      var w = watchPage;
      return {
        state: w.state, kind: w.kind, title: w.title, url: w.url, signed: w.signed, expires: w.expires,
        refused: w.refused, error: w.error, hls: !!w.hls,
        currentTime: v.currentTime, duration: v.duration, paused: v.paused, seeking: v.seeking,
        width: v.videoWidth, height: v.videoHeight,
      };
    },
    seek: function (to) { $('watch-video').currentTime = to; },
    status: function () {
      var l = call.local;
      var spot = call.spot ? (call.spot === call.me ? call.name : call.spot.name) : '';
      var members = {};
      Object.keys(call.members).forEach(function (n) {
        var x = call.members[n];
        var v = x.viewer;
        members[n] = {
          state: x.state,
          viewer: v ? { phase: v.phase, connected: v.connected, tracks: v.tracks, posts: v.posts, notFound: v.notFound, error: v.error } : null,
          avatar: !x.tile.avatar.hidden,
          note: x.tile.note.textContent,
          videoSize: x.tile.video.videoWidth + 'x' + x.tile.video.videoHeight,
        };
      });
      var senders = {};
      if (call.pub && call.pub.audio) {
        var a = call.pub.audio.sender.track;
        var vv = call.pub.video.sender.track;
        senders = {
          audio: a ? (a === (l && l.mic) ? 'device' : 'synthetic') : 'none',
          video: vv ? (vv === (l && l.cam) ? 'device' : vv === (l && l.screen) ? 'screen' : 'synthetic') : 'none',
        };
      }
      return {
        view: view,
        joined: call.active && !!call.key,
        room: call.room, name: call.name,
        local: l ? { audioOn: l.audioOn, videoOn: l.videoOn, has: l.has, state: l.state() } : null,
        pub: call.pub ? { phase: call.pub.phase, connected: call.pub.connected, posts: call.pub.posts, error: call.pub.error } : null,
        senders: senders,
        members: members,
        spot: spot,
        chat: {
          enabled: chatEnabled(),
          maxFileBytes: chatMaxFile(),
          count: call.chatMsgs.length,
          panel: !$('call-chat').hidden,
          hint: $('chat-hint').textContent,
        },
        problems: problems.slice(),
      };
    },
  };

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
