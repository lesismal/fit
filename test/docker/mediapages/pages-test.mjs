/*
 * Browser test of fit's media pages: two headless Chromiums in a Linux
 * container, one with fake devices and one with none at all, driven over
 * DevTools. alice and bob (fake devices) and carol (no devices) log in and
 * join a room, switch their microphones and cameras off and on, and each
 * checks what the others see, that no PeerConnection was renegotiated or
 * restarted, that no WHEP request was refused or retried, and that the
 * pages logged no errors.
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
  }
  async send(method, params = {}) {
    await this.ready;
    const id = this.id++;
    const p = new Promise((res, rej) => this.pending.set(id, { res, rej }));
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

async function screenshot(c, name) {
  const r = await c.send('Page.captureScreenshot', { format: 'png' });
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
  const anon = await tab(FAKE, BASE + 'call?room=' + ROOM);
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

  await alice.eval(`window.fitMedia.join('${ROOM}', 'alice')`);
  await bob.eval(`window.fitMedia.join('${ROOM}', 'bob')`);
  await carol.eval(`window.fitMedia.join('${ROOM}', 'carol')`);

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
  await sleep(2000);
  const dv = (await status(bob)).members.dave.viewer;
  check(dv.connected && dv.posts === 1 && dv.notFound === 0, 'bob plays a silent dave with one request', dv);
  await dave.eval('window.fitMedia.cam(true)');
  check(await playing(bob, 'dave', 'bob plays dave once on'), 'bob plays dave once his camera is on');
  await screenshot(carol, 'call-no-devices');

  // A member leaves: the others drop its tile.
  await bob.eval('window.fitMedia.leave()');
  await waitFor(async () => !(await status(alice)).members.bob, 'bob gone at alice');
  check(true, 'alice drops bob once he leaves');

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

  for (const [c, n] of [[alice, 'alice'], [bob, 'bob'], [carol, 'carol'], [dave, 'dave']]) {
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
