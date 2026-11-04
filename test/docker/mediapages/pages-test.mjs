/*
 * Browser test of fit's media pages: two headless Chromiums in a Linux
 * container, one with fake devices and one with none at all, driven over
 * DevTools. alice and bob (fake devices) and carol (no devices) log in and
 * join a room, which starts with the microphone and camera off as the config
 * says; they switch them on after joining, and each checks what the others
 * see, that no PeerConnection was renegotiated or restarted, that no WHEP
 * request was refused or retried, and that the pages logged no errors. They
 * then use the room chat: a text message and a small file reach the other
 * member, the file downloads intact, a file past max_file_bytes is refused
 * with a hint, and leaving clears the window while joining fills it from the
 * room's history. The chat sits beside the tiles on a wide window and below
 * them on a narrow one.
 *
 *   node pages-test.mjs [out-dir]
 *
 * e2e.sh starts the container and runs it.
 */
import { writeFileSync, mkdirSync } from 'node:fs';

const OUT = process.argv[2] || '.';
const FAKE = 29301; // DevTools of the Chromium with fake devices
const NONE = 29302; // and of the one with none
const BASE = 'http://localhost:8080/media/';
const ROOM = 'e2e';
mkdirSync(OUT, { recursive: true });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let failures = 0;
function check(ok, what, detail) {
  console.log((ok ? 'ok   ' : 'FAIL ') + what + (ok || detail === undefined ? '' : ': ' + JSON.stringify(detail)));
  if (!ok) failures++;
}

class CDP {
  constructor(url) {
    this.ws = new WebSocket(url);
    this.id = 1;
    this.pending = new Map();
    this.errors = [];
    this.ready = new Promise((res, rej) => {
      this.ws.addEventListener('open', () => res());
      this.ws.addEventListener('error', () => rej(new Error('websocket')));
    });
    this.ws.addEventListener('message', (ev) => {
      const m = JSON.parse(ev.data);
      if (m.id) {
        const p = this.pending.get(m.id);
        this.pending.delete(m.id);
        if (p) (m.error ? p.rej(new Error(JSON.stringify(m.error))) : p.res(m.result));
        return;
      }
      if (m.method === 'Runtime.exceptionThrown') {
        const d = m.params.exceptionDetails || {};
        this.errors.push('exception: ' + (d.exception?.description || d.text));
      } else if (m.method === 'Runtime.consoleAPICalled' && (m.params.type === 'error' || m.params.type === 'warning')) {
        this.errors.push('console.' + m.params.type + ': ' + m.params.args.map((a) => a.value ?? a.description ?? '').join(' '));
      } else if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') {
        this.errors.push('log: ' + m.params.entry.text + ' ' + (m.params.entry.url || ''));
      }
    });
    // A dead connection fails what waits on it now, not at its timeout.
    this.ws.addEventListener('close', () => {
      for (const p of this.pending.values()) p.rej(new Error('websocket closed'));
      this.pending.clear();
    });
  }
  // send runs one protocol command; a command Chromium never answers (a
  // screenshot on a slow software renderer does) rejects after timeoutMs
  // instead of hanging the whole test.
  async send(method, params = {}, timeoutMs = 120000) {
    await this.ready;
    const id = this.id++;
    const p = new Promise((res, rej) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        rej(new Error('timed out after ' + timeoutMs + 'ms: ' + method));
      }, timeoutMs);
      this.pending.set(id, {
        res: (v) => { clearTimeout(timer); res(v); },
        rej: (e) => { clearTimeout(timer); rej(e); },
      });
    });
    this.ws.send(JSON.stringify({ id, method, params }));
    return p;
  }
  async eval(expr) {
    const r = await this.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
    return r.result.value;
  }
}

async function tab(port, url) {
  const res = await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: 'PUT' });
  const t = await res.json();
  const ws = t.webSocketDebuggerUrl.replace(/ws:\/\/[^/]+/, `ws://127.0.0.1:${port}`);
  const c = new CDP(ws);
  await c.ready;
  await c.send('Runtime.enable');
  await c.send('Log.enable');
  await c.send('Page.enable');
  await c.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
  await c.send('Emulation.setFocusEmulationEnabled', { enabled: true }).catch(() => {});
  await c.send('Page.navigate', { url });
  await waitFor(() => c.eval('!!(window.fitMedia && document.getElementById("brand-title"))'), 'the page of ' + url);
  await sleep(300);
  return c;
}

async function waitFor(fn, what, ms = 20000) {
  const start = Date.now();
  let last;
  for (;;) {
    try {
      last = await fn();
      if (last) return last;
    } catch (e) { last = String(e); }
    if (Date.now() - start > ms) throw new Error('timed out waiting for ' + what + ' (last: ' + JSON.stringify(last) + ')');
    await sleep(200);
  }
}

const status = (c) => c.eval('JSON.stringify(window.fitMedia.status())').then(JSON.parse);

// login logs in through the page's form.
async function login(c, user) {
  await c.eval(`(() => {
    if (!document.getElementById('login').open) document.getElementById('login-open').click();
    document.getElementById('login-name').value = ${JSON.stringify(user)};
    document.getElementById('login-password').value = ${JSON.stringify(user + '-password')};
    document.getElementById('login-form').requestSubmit();
  })()`);
  await waitFor(() => c.eval(`document.getElementById('who').textContent === ${JSON.stringify(user)}`), user + ' logged in');
}

// screenshot saves a diagnostic picture. A renderer that never answers the
// capture must not hang the test: the screenshot is skipped and said so.
async function screenshot(c, name) {
  let r;
  try {
    r = await c.send('Page.captureScreenshot', { format: 'png' }, 120000);
  } catch (e) {
    console.log('     screenshot skipped (' + name + '): ' + e.message);
    return;
  }
  const path = OUT + '/' + name + '.png';
  writeFileSync(path, Buffer.from(r.data, 'base64'));
  console.log('     screenshot ' + path);
}

// sees waits for viewer c to see member m in a state cond accepts.
function sees(c, m, cond, what, ms) {
  return waitFor(async () => {
    const s = await status(c);
    const x = s.members[m];
    return x && cond(x) ? x : null;
  }, what, ms);
}

// playing waits for c to play m's video, its frames advancing.
async function playing(c, m, what) {
  await sees(c, m, (x) => x.viewer && x.viewer.connected && x.viewer.tracks === 2 && x.videoSize !== '0x0', what);
  const t0 = await c.eval(`document.querySelector('[data-member="${m}"] video').currentTime`);
  await sleep(1500);
  const t1 = await c.eval(`document.querySelector('[data-member="${m}"] video').currentTime`);
  return t1 > t0;
}

// pcStats counts c's PeerConnections ever made, from the status.
async function main() {
  // A page with no credentials: the room API refuses, and the login opens.
  // The URL's mic=1 and cam=1 override the config's join defaults (both off).
  const anon = await tab(FAKE, BASE + 'call?room=' + ROOM + '&mic=1&cam=1');
  await waitFor(() => anon.eval('window.fitMedia.chatEnabled() === true'), 'the config');
  check(await anon.eval(`document.getElementById('call-mic').getAttribute('aria-pressed') === 'true' && document.getElementById('call-cam').getAttribute('aria-pressed') === 'true'`), "the URL's mic=1 and cam=1 override the join defaults");
  check(await anon.eval('window.fitMedia.chatEnabled() === true'), 'the room chat is enabled by the config');
  check(await anon.eval(`document.getElementById('login').open`), 'a page without credentials opens the login');
  const anonJoin = await anon.eval(`fetch('api/rooms/${ROOM}/join', {method: 'POST', body: '{"name":"x"}'}).then(r => r.status)`);
  check(anonJoin === 401, 'the room API refuses a join without credentials', anonJoin);
  const anonWhip = await anon.eval(`fetch('/whip/${ROOM}-x', {method: 'POST', headers: {'Content-Type': 'application/sdp'}, body: 'v=0'}).then(r => r.status)`);
  check(anonWhip === 401, 'WHIP refuses an offer without credentials', anonWhip);

  // alice and bob, fake devices; carol, none.
  const alice = await tab(FAKE, BASE + 'call?room=' + ROOM);
  const bob = await tab(FAKE, BASE + 'call?room=' + ROOM);
  const carol = await tab(NONE, BASE + 'call?room=' + ROOM);
  for (const [c, u] of [[alice, 'alice'], [bob, 'bob'], [carol, 'carol']]) await login(c, u);
  const cookie = await alice.eval('document.cookie');
  check(!/fit_media_token/.test(cookie), 'the token cookie is HttpOnly', cookie);

  // A deployment with the chat off serves no chat window, and one whose
  // mic_default is on starts the call with the microphone on: the same page
  // under another media_pages prefix with its own params.
  const nochat = await tab(FAKE, BASE.replace('/media/', '/nochat/') + 'call?room=' + ROOM);
  await waitFor(() => nochat.eval('window.fitMedia.status().chat.maxFileBytes === 65536'), 'the chat-off config');
  check(await nochat.eval('window.fitMedia.chatEnabled() === false'), 'a chat-off deployment serves chat.enabled false');
  check(await nochat.eval(`document.getElementById('call-chat').hidden`), 'a chat-off deployment shows no chat window');
  await login(nochat, 'alice'); // the page's own origin: the session token is per tab
  await nochat.eval(`window.fitMedia.join('${ROOM}', 'nora')`);
  const ns = await waitFor(async () => {
    const st = await status(nochat);
    return st.joined && st.local ? st : null;
  }, 'nora joined');
  check(ns.local.audioOn === true && ns.local.videoOn === false, 'mic_default true and cam_default false start the join so', ns.local);
  check(await nochat.eval(`document.getElementById('call-mic').getAttribute('aria-pressed') === 'true' && document.getElementById('call-cam').getAttribute('aria-pressed') === 'false'`), "the switches show mic_default and cam_default");
  check(await nochat.eval('window.fitMedia.chatEnabled() === false'), 'a chat-off deployment still has no chat after joining');
  check(await nochat.eval(`document.getElementById('call-chat').hidden`), 'a chat-off deployment shows no chat window after joining');
  check(await nochat.eval('window.fitMedia.messages().length === 0'), 'a chat-off deployment has no messages');
  await nochat.eval('window.fitMedia.leave()');

  await alice.eval(`window.fitMedia.join('${ROOM}', 'alice')`);
  await bob.eval(`window.fitMedia.join('${ROOM}', 'bob')`);
  await carol.eval(`window.fitMedia.join('${ROOM}', 'carol')`);

  // The call starts with the microphone and camera off, as mic_default and
  // cam_default say, and its switches show that.
  for (const [c, n] of [[alice, 'alice'], [bob, 'bob'], [carol, 'carol']]) {
    const st = await waitFor(async () => {
      const s = await status(c);
      return s.joined && s.local && s.senders.audio === 'none' && s.senders.video === 'none' ? s : null;
    }, n + ' joins silent');
    check(st.local.audioOn === false && st.local.videoOn === false, n + "'s microphone and camera start off", st.local);
    check(await c.eval(`document.getElementById('call-mic').getAttribute('aria-pressed') === 'false' && document.getElementById('call-cam').getAttribute('aria-pressed') === 'false'`), n + "'s switches show them off");
    check(await c.eval(`!document.getElementById('call-chat').hidden && window.fitMedia.chatEnabled()`), n + "'s chat window is shown");
  }
  // Switched on after joining, for the checks that follow: no renegotiation.
  for (const [c, n, kind] of [[alice, 'alice', 'device'], [bob, 'bob', 'device'], [carol, 'carol', 'synthetic']]) {
    await c.eval('window.fitMedia.mic(true)');
    await c.eval('window.fitMedia.cam(true)');
    const st = await waitFor(async () => {
      const s = await status(c);
      return s.senders.audio === kind && s.senders.video === kind ? s : null;
    }, n + ' switched on');
    check(st.local.audioOn === true && st.local.videoOn === true, n + "'s switches turn the microphone and camera on", st.local);
    check(await c.eval(`document.getElementById('call-mic').getAttribute('aria-pressed') === 'true' && document.getElementById('call-cam').getAttribute('aria-pressed') === 'true'`), n + "'s switches show them on");
  }

  // Everyone plays everyone.
  for (const [c, me, others] of [[alice, 'alice', ['bob', 'carol']], [bob, 'bob', ['alice', 'carol']], [carol, 'carol', ['alice', 'bob']]]) {
    for (const o of others) check(await playing(c, o, me + ' plays ' + o), me + ' plays ' + o + ', its frames advancing');
  }
  let s = await status(alice);
  check(s.senders.audio === 'device' && s.senders.video === 'device', 'alice sends her devices', s.senders);
  let sc = await status(carol);
  check(sc.local.has.audio === false && sc.local.has.video === false, 'carol has no devices', sc.local.has);
  check(sc.senders.audio === 'synthetic' && sc.senders.video === 'synthetic', 'carol sends synthetic tracks', sc.senders);
  const carolAtAlice = (await status(alice)).members.carol;
  check(carolAtAlice.state.audio && !carolAtAlice.state.audioDevice && carolAtAlice.state.video && !carolAtAlice.state.videoDevice,
    'alice sees carol on, with no devices', carolAtAlice.state);
  check(/no microphone/.test(carolAtAlice.note) && /no camera/.test(carolAtAlice.note), 'alice shows carol has no microphone and no camera', carolAtAlice.note);
  check(carolAtAlice.videoSize === '320x180', "alice plays carol's synthetic frame", carolAtAlice.videoSize);
  await screenshot(alice, 'call-all-on');

  // The number of PeerConnections each page made so far: switching must
  // make no more.
  const pcsBefore = {};
  for (const [c, n] of [[alice, 'alice'], [bob, 'bob'], [carol, 'carol']]) {
    const st = await status(c);
    pcsBefore[n] = { pub: st.pub.posts, views: Object.fromEntries(Object.entries(st.members).map(([k, v]) => [k, v.viewer.posts])) };
  }

  // alice switches her microphone off: bob and carol see it.
  await alice.eval('window.fitMedia.mic(false)');
  s = await status(alice);
  check(s.senders.audio === 'none' && s.local.state.audio === false, 'alice sends no audio', s.senders);
  for (const c of [bob, carol]) {
    const x = await sees(c, 'alice', (x) => x.state.audio === false, 'alice muted');
    check(/microphone off/.test(x.note), 'the others show alice\'s microphone off', x.note);
  }
  // alice switches her camera off: the others show her avatar, the device
  // is released.
  await alice.eval('window.fitMedia.cam(false)');
  s = await status(alice);
  check(s.senders.video === 'none', 'alice sends no video', s.senders);
  for (const c of [bob, carol]) {
    const x = await sees(c, 'alice', (x) => x.state.video === false && x.avatar, 'alice\'s camera off');
    check(x.avatar && /camera off/.test(x.note), 'the others show alice\'s avatar', x);
  }
  await sleep(4000); // longer than a few heartbeats' worth of quiet media
  await screenshot(bob, 'call-alice-off');
  for (const c of [bob, carol]) {
    const x = (await status(c)).members.alice;
    check(x.viewer.phase === 'live' && x.viewer.connected, 'a silent alice stays connected', x.viewer);
  }
  // On again: her video plays again, without a new PeerConnection.
  await alice.eval('window.fitMedia.cam(true)');
  await alice.eval('window.fitMedia.mic(true)');
  for (const [c, n] of [[bob, 'bob'], [carol, 'carol']]) {
    await sees(c, 'alice', (x) => x.state.video && x.state.audio && !x.avatar, 'alice back on');
    check(await playing(c, 'alice', n + ' plays alice again'), n + ' plays alice again, its frames advancing');
  }

  // carol, without devices, switches her camera off and on.
  await carol.eval('window.fitMedia.cam(false)');
  await sees(alice, 'carol', (x) => !x.state.video && x.avatar, "carol's camera off");
  sc = await status(carol);
  check(sc.senders.video === 'none', 'carol sends no video', sc.senders);
  await carol.eval('window.fitMedia.cam(true)');
  await sees(alice, 'carol', (x) => x.state.video && !x.avatar, "carol's camera on");
  check(await playing(alice, 'carol', 'alice plays carol again'), 'alice plays carol again, its frames advancing');

  // No PeerConnection was made anew, no WHEP request was refused.
  for (const [c, n] of [[alice, 'alice'], [bob, 'bob'], [carol, 'carol']]) {
    const st = await status(c);
    check(st.pub.posts === pcsBefore[n].pub && st.pub.phase === 'live', n + ' published once', st.pub);
    for (const [k, v] of Object.entries(st.members)) {
      check(v.viewer.posts === pcsBefore[n].views[k] && v.viewer.posts === 1 && v.viewer.notFound === 0, n + ' played ' + k + ' with one WHEP request', v.viewer);
    }
  }

  // dave joins with his microphone and camera off, in the browser with
  // devices: the others show him off, he publishes, and nobody retries.
  const dave = await tab(FAKE, BASE + 'call?room=' + ROOM + '&mic=0&cam=0');
  await dave.eval(`fetch('api/login', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({user: 'alice', password: 'alice-password'})}).then(r => r.json()).then(a => sessionStorage.setItem('fitMediaToken', JSON.stringify(a)))`);
  await dave.send('Page.reload');
  await waitFor(() => dave.eval('!!window.fitMedia'), 'dave reloaded');
  await sleep(300);
  await dave.eval(`window.fitMedia.join('${ROOM}', 'dave')`);
  const d = await sees(bob, 'dave', (x) => x.state.publishing && !x.state.audio && !x.state.video, 'dave, off');
  check(d.avatar, 'bob shows dave\'s avatar', d);
  const ds = await status(dave);
  check(ds.senders.audio === 'none' && ds.senders.video === 'none' && ds.local.has.video, 'dave sends nothing, his devices left closed', ds);
  check(ds.local.audioOn === false && ds.local.videoOn === false, "dave's URL mic=0 and cam=0 keep the defaults off", ds.local);
  await sleep(2000);
  const dv = (await status(bob)).members.dave.viewer;
  check(dv.connected && dv.posts === 1 && dv.notFound === 0, 'bob plays a silent dave with one request', dv);
  await dave.eval('window.fitMedia.cam(true)');
  check(await playing(bob, 'dave', 'bob plays dave once on'), 'bob plays dave once his camera is on');
  await screenshot(carol, 'call-no-devices');

  // The room chat: a text message from alice, its local echo deduplicated
  // against the SSE echo, a small file that downloads intact, and a file
  // past max_file_bytes (65536 in fit.json) refused with a hint.
  const said = 'hello from alice, ' + Date.now();
  const echoed = await alice.eval(`window.fitMedia.chat(${JSON.stringify(said)})`);
  check(echoed && echoed.id && echoed.text === said && echoed.from === 'alice', 'alice sends a text message', echoed);
  const heard = await waitFor(async () => {
    const ms = await bob.eval('JSON.stringify(window.fitMedia.messages())').then(JSON.parse);
    return ms.find((m) => m.text === said) || null;
  }, 'bob hears alice');
  check(heard.from === 'alice' && heard.at > 0 && !!heard.atText, 'bob sees the message, its sender and time', heard);
  await sleep(800); // past the SSE echo of alice's own message
  for (const [c, n] of [[alice, 'alice'], [bob, 'bob']]) {
    const ms = await c.eval('JSON.stringify(window.fitMedia.messages())').then(JSON.parse);
    check(ms.filter((m) => m.text === said).length === 1, n + ' has the message once, not echoing it twice', ms.length);
  }
  await screenshot(alice, 'call-chat');

  // A small file: bob sees its name and size, and its download control gets
  // the bytes back, saved as a blob under the file's name.
  const small = Array.from({ length: 2048 }, (_, i) => (i * 7 + 3) & 255);
  const sentFile = await alice.eval(`window.fitMedia.file('note.txt', ${JSON.stringify(small)})`);
  check(sentFile && sentFile.file && sentFile.file.name === 'note.txt' && sentFile.file.size === 2048, 'alice sends a small file', sentFile);
  const fileAtBob = await waitFor(async () => {
    const ms = await bob.eval('JSON.stringify(window.fitMedia.messages())').then(JSON.parse);
    return ms.find((m) => m.file && m.file.name === 'note.txt') || null;
  }, 'bob sees the file');
  check(fileAtBob.from === 'alice' && fileAtBob.file.size === 2048, 'bob sees the file, its name and size', fileAtBob);
  await bob.eval(`(() => {
    window.__dl = null; window.__dlBlob = null;
    const create = URL.createObjectURL.bind(URL);
    window.__createObjectURL = URL.createObjectURL;
    URL.createObjectURL = function (b) { window.__dlBlob = b; return create(b); };
    window.__anchorClick = HTMLAnchorElement.prototype.click;
    HTMLAnchorElement.prototype.click = function () {
      if (this.download) { window.__dl = { name: this.download, href: this.href }; return; }
      return window.__anchorClick.call(this);
    };
  })()`);
  await bob.eval(`document.querySelector('#chat-log [data-id=${JSON.stringify(fileAtBob.id)}] button.chat__download').click()`);
  const downloaded = await waitFor(() => bob.eval('window.__dl && window.__dl.name'), 'the download');
  check(downloaded === 'note.txt', 'the download saves the file under its name', downloaded);
  const got = await bob.eval('window.__dlBlob.arrayBuffer().then((b) => Array.from(new Uint8Array(b)))');
  check(got.length === small.length && got.every((v, i) => v === small[i]), 'the downloaded file is byte for byte the file sent', got.length);
  await bob.eval('URL.createObjectURL = window.__createObjectURL; HTMLAnchorElement.prototype.click = window.__anchorClick');

  // Bigger than max_file_bytes: refused at once with a hint, never sent.
  const refused = await alice.eval(`window.fitMedia.file('big.bin', new Uint8Array(70000)).then(() => 'sent', (e) => e.message)`);
  check(/larger than/.test(refused), 'an oversized file is refused before sending', refused);
  const chatHint = await alice.eval(`document.getElementById('chat-hint').textContent`);
  check(/larger than/.test(chatHint), 'the refusal shows the size hint', chatHint);
  await sleep(300);
  check(await bob.eval(`window.fitMedia.messages().some((m) => m.file && m.file.name === 'big.bin') === false`), 'the oversized file was not sent');

  // Layout: the chat window is beside the tiles on a wide window, below them
  // on a narrow one, and the spotlight still works with it up.
  const boxes = `(() => {
    const t = document.getElementById('call-tiles').getBoundingClientRect();
    const c = document.getElementById('call-chat').getBoundingClientRect();
    return { tiles: { left: t.left, right: t.right, top: t.top, bottom: t.bottom }, chat: { left: c.left, right: c.right, top: c.top, bottom: c.bottom }, width: innerWidth };
  })()`;
  const wide = await alice.eval(boxes);
  check(wide.width >= 1000 && wide.chat.left >= wide.tiles.right - 2 && Math.abs(wide.chat.top - wide.tiles.top) < 40, 'wide: the chat window is beside the tiles', wide);
  await alice.send('Emulation.setDeviceMetricsOverride', { width: 760, height: 860, deviceScaleFactor: 1, mobile: false });
  await sleep(400);
  const narrow = await alice.eval(boxes);
  check(narrow.chat.top >= narrow.tiles.bottom - 2, 'narrow: the chat window is below the tiles', narrow);
  await alice.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 860, deviceScaleFactor: 1, mobile: false });
  await sleep(400);
  await alice.eval(`window.fitMedia.spot('bob')`);
  await waitFor(async () => (await status(alice)).spot === 'bob' && (await alice.eval(`document.querySelector('[data-member="bob"]').classList.contains('tile--spot')`)), 'bob in the spotlight');
  check(true, 'the spotlight works with the chat window up');
  await alice.eval(`window.fitMedia.spot('bob')`);

  // A member leaves: the others drop its tile, and the chat window clears;
  // joining again fills it from the room's history.
  await bob.eval('window.fitMedia.leave()');
  await waitFor(async () => !(await status(alice)).members.bob, 'bob gone at alice');
  check(true, 'alice drops bob once he leaves');
  check(await bob.eval('window.fitMedia.messages().length === 0'), "bob's chat window clears when he leaves");
  check(await bob.eval(`document.getElementById('call-chat').hidden`), "bob's chat window hides when he leaves");
  await bob.eval(`window.fitMedia.join('${ROOM}', 'bob')`);
  const history = await waitFor(async () => {
    const ms = await bob.eval('JSON.stringify(window.fitMedia.messages())').then(JSON.parse);
    return ms.find((m) => m.text === said) && ms.find((m) => m.file && m.file.name === 'note.txt') ? ms : null;
  }, "bob's history");
  check(history.length >= 2 && (await bob.eval(`!document.getElementById('call-chat').hidden`)), 'joining again fills the chat from the room history', history.length);

  // The publish and play pages.
  await alice.eval('window.fitMedia.leave()');
  await alice.send('Page.navigate', { url: BASE + 'publish?stream=e2e-solo' });
  await waitFor(() => alice.eval('!!window.fitMedia'), 'publish page');
  await sleep(300);
  await alice.eval(`document.getElementById('pub-start').click()`);
  await waitFor(() => alice.eval(`document.getElementById('pub-state').classList.contains('ok')`), 'publishing solo');
  await carol.eval('window.fitMedia.leave()');
  await carol.send('Page.navigate', { url: BASE + 'play?stream=e2e-solo' });
  await waitFor(() => carol.eval('!!window.fitMedia'), 'play page');
  await sleep(300);
  await carol.eval(`document.getElementById('play-start').click()`);
  await waitFor(() => carol.eval(`(() => { const v = document.querySelector('#play-tile video'); return v && v.videoWidth > 0; })()`), 'carol plays solo');
  check(true, 'the play page plays what the publish page publishes');
  const links = await alice.eval(`document.getElementById('pub-links').textContent`);
  check(/WHIP.*\/whip\/e2e-solo/.test(links) && /WHEP.*\/whep\/e2e-solo/.test(links), 'the publish page shows the URLs', links);
  await screenshot(alice, 'publish');
  await screenshot(carol, 'play');
  await carol.eval(`document.getElementById('play-stop').click()`);
  // Play a stream nobody publishes: a few tries, then it stops.
  await carol.eval(`document.getElementById('play-stream').value = 'e2e-nobody'; document.getElementById('play-start').click()`);
  await waitFor(() => carol.eval(`/not published/.test(document.getElementById('play-hint').textContent)`), 'the not-published hint');
  await screenshot(carol, 'play-waiting');
  await carol.eval(`document.getElementById('play-stop').click()`);

  // The home page.
  await bob.send('Page.navigate', { url: BASE });
  await waitFor(() => bob.eval('!!window.fitMedia && document.getElementById("home-info").children.length > 0'), 'home');
  await screenshot(bob, 'home');

  for (const [c, n] of [[alice, 'alice'], [bob, 'bob'], [carol, 'carol'], [dave, 'dave'], [nochat, 'nochat']]) {
    // play-waiting's 404s are the WHEP requests for a stream nobody
    // publishes, which the play page asked for: they are expected there.
    const errs = c.errors.filter((e) => !(n === 'carol' && /whep\/e2e-nobody/.test(e)));
    check(errs.length === 0, n + "'s page logged no errors", errs);
    const st = await status(c);
    check(st.problems.length === 0, n + "'s page met no problems", st.problems);
  }
  console.log(failures ? failures + ' FAILED' : 'PASS');
  process.exit(failures ? 1 : 0);
}

main().catch((e) => { console.error('FAIL:', e.message); process.exit(1); });
