#!/usr/bin/env python3
"""The video site's end-to-end test: headless Chromium, driven over the
DevTools protocol on a pipe (--remote-debugging-pipe), through the example's
page and fit's media pages, as run.sh serves them on 127.0.0.1:8080.

The site's page (the example's API and its page with hls.js):
  - a viewer logs in to the site's API, and plays a film through the URL
    the API issues, which fit's media_tokens signed; seeks to 60% and plays
    on there;
  - once the URL expires, it is refused (403), while the player plays on, on
    the grant its playlists handed it; once the grant expires too, a segment
    fetched with it is refused (403);
  - plays the channel through a URL the API issues, and is refused once
    the URL and its grant expire: the page stops the player and says so.
fit's media pages (media_pages' library and player pages):
  - a member of the policy logs in on the library page, which lists the
    films with their posters and the channel; plays a film through a URL the
    pages sign, seeks; plays the channel, and is refused once the URL and its
    grant expire.
No page may throw. What it found goes to OUT/result.json; it exits 0 when
every check held.

    drive.py OUT
"""
import fcntl
import json
import os
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request

OUT = sys.argv[1] if len(sys.argv) > 1 else "."
BASE = os.environ.get("FIT_BASE", "http://127.0.0.1:8080")
CHROMIUM = os.environ.get("CHROMIUM", "chromium")
PROFILE = os.environ.get("CHROME_PROFILE", "/tmp/fit-videosite-chrome")
results = []
failures = 0


def check(ok, what, detail=None):
    global failures
    print(("ok   " if ok else "FAIL ") + what + ("" if ok or detail is None else ": " + json.dumps(detail)), flush=True)
    results.append({"ok": bool(ok), "what": what, "detail": detail})
    if not ok:
        failures += 1
    return ok


class Chrome:
    """Chromium over --remote-debugging-pipe: commands go in on its fd 3 and
    answers and events come out on its fd 4, each a JSON message ended by a
    NUL byte."""

    def __init__(self):
        to_r, to_w = os.pipe()
        from_r, from_w = os.pipe()
        # Out of the way of 3 and 4, where the child wants them; closed on
        # exec, as the pipes are, while dup2 leaves 3 and 4 open.
        to_r2 = fcntl.fcntl(to_r, fcntl.F_DUPFD_CLOEXEC, 100)
        from_w2 = fcntl.fcntl(from_w, fcntl.F_DUPFD_CLOEXEC, 100)

        def pre():
            os.dup2(to_r2, 3)
            os.dup2(from_w2, 4)

        args = [CHROMIUM, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
                "--no-first-run", "--mute-audio", "--autoplay-policy=no-user-gesture-required",
                "--user-data-dir=" + PROFILE, "--remote-debugging-pipe", "about:blank"]
        self.log = open(os.path.join(OUT, "chromium.log"), "w")
        self.p = subprocess.Popen(args, preexec_fn=pre, close_fds=False, stdout=self.log, stderr=self.log)
        for fd in (to_r, from_w, to_r2, from_w2):
            os.close(fd)
        self.w, self.r = to_w, from_r
        self.id = 0
        self.lock = threading.Lock()
        self.pending = {}
        self.errors = {}  # by session: what its page threw or logged as an error
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        buf = b""
        while True:
            data = os.read(self.r, 1 << 16)
            if not data:
                for ev in list(self.pending.values()):
                    ev["error"] = "chromium closed the pipe"
                    ev["done"].set()
                return
            buf += data
            while b"\0" in buf:
                raw, buf = buf.split(b"\0", 1)
                m = json.loads(raw)
                if "id" in m:
                    ev = self.pending.pop(m["id"], None)
                    if ev:
                        ev["msg"] = m
                        ev["done"].set()
                    continue
                sess = m.get("sessionId")
                p = m.get("params", {})
                if m.get("method") == "Runtime.exceptionThrown":
                    d = p.get("exceptionDetails", {})
                    self.errors.setdefault(sess, []).append("exception: " + str((d.get("exception") or {}).get("description") or d.get("text")))
                elif m.get("method") == "Log.entryAdded" and p["entry"]["level"] == "error":
                    e = p["entry"]
                    # The refusals the test waits for are logged as failed
                    # loads; they are what it checks, not errors.
                    if e.get("source") == "network" and ("403" in e.get("text", "") or "401" in e.get("text", "")):
                        continue
                    self.errors.setdefault(sess, []).append("log: %s %s" % (e.get("text"), e.get("url", "")))

    def send(self, method, params=None, session=None, timeout=60):
        with self.lock:
            self.id += 1
            i = self.id
            ev = {"done": threading.Event()}
            self.pending[i] = ev
            m = {"id": i, "method": method, "params": params or {}}
            if session:
                m["sessionId"] = session
            os.write(self.w, json.dumps(m).encode() + b"\0")
        if not ev["done"].wait(timeout):
            raise RuntimeError("no answer to %s in %ss" % (method, timeout))
        if "error" in ev:
            raise RuntimeError(ev["error"])
        msg = ev["msg"]
        if "error" in msg:
            raise RuntimeError("%s: %s" % (method, msg["error"]))
        return msg.get("result", {})

    def page(self, url):
        tid = self.send("Target.createTarget", {"url": "about:blank"})["targetId"]
        sess = self.send("Target.attachToTarget", {"targetId": tid, "flatten": True})["sessionId"]
        for m in ("Runtime.enable", "Log.enable", "Page.enable"):
            self.send(m, session=sess)
        self.send("Page.navigate", {"url": url}, session=sess)
        return Page(self, sess)

    def close(self):
        try:
            self.send("Browser.close", timeout=5)
        except Exception:
            pass
        try:
            self.p.wait(5)
        except subprocess.TimeoutExpired:
            self.p.kill()


class Page:
    def __init__(self, chrome, sess):
        self.c, self.s = chrome, sess

    def eval(self, expr, timeout=60):
        r = self.c.send("Runtime.evaluate", {"expression": expr, "awaitPromise": True, "returnByValue": True},
                        session=self.s, timeout=timeout)
        if "exceptionDetails" in r:
            d = r["exceptionDetails"]
            raise RuntimeError("%s: %s" % (expr[:80], (d.get("exception") or {}).get("description") or d.get("text")))
        return r.get("result", {}).get("value")

    def wait(self, expr, seconds, what):
        end = time.time() + seconds
        last = None
        while time.time() < end:
            try:
                last = self.eval("!!(%s)" % expr)
                if last:
                    return last
            except RuntimeError as e:
                last = str(e)
            time.sleep(0.2)
        raise RuntimeError("timed out after %ss waiting for %s (last: %r)" % (seconds, what, last))

    def errors(self):
        return self.c.errors.get(self.s, [])


def wait_ready():
    end = time.time() + 30
    while time.time() < end:
        try:
            with urllib.request.urlopen(BASE + "/media/api/config", timeout=2) as r:
                if r.status == 200:
                    with urllib.request.urlopen(BASE + "/api/catalog", timeout=2) as r2:
                        if r2.status == 200:
                            return
        except Exception:
            pass
        time.sleep(0.3)
    raise RuntimeError("fit and the site's API never answered")


# status fetches a URL in the page, with its cookies, and gives its status.
def status(page, url):
    return page.eval("fetch(%s, {credentials: 'same-origin'}).then(r => r.status)" % json.dumps(url))


# segment_of returns the URL of the first segment of a playlist URL's first
# variant, as a player fetches it: with the grant the playlists hand on.
SEGMENT_OF = """
(async (u) => {
  const abs = (x, base) => new URL(x, base).toString();
  const text = async (x) => { const r = await fetch(x); if (r.status !== 200) throw new Error(x + ': ' + r.status); return r.text(); };
  const firstURI = (t) => t.split('\\n').map((l) => l.trim()).find((l) => l && !l.startsWith('#'));
  const master = abs(u, location.href);
  const variant = abs(firstURI(await text(master)), master);
  return abs(firstURI(await text(variant)), variant);
})(%s)
"""


def grant_expiry(url):
    q = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
    return int(q["exp"][0]), q.get("g", [""])[0]


def play_on(page, state, seconds, what):
    """Waits for the player to play `seconds` more."""
    start = page.eval(state + ".currentTime")
    page.wait("(s => !s.paused && !s.seeking && s.currentTime >= %f)(%s)" % (start + seconds, state), 30, what)


def site(chrome):
    print("--- the site's page", flush=True)
    page = chrome.page(BASE + "/")
    page.wait("document.readyState === 'complete' && !document.getElementById('login').hidden", 20, "the login form")
    page.eval("document.getElementById('user').value = 'alice'; document.getElementById('password').value = 'example-password';"
              "document.getElementById('login').requestSubmit(); true")
    page.wait("document.querySelector('a[data-id=\"films/testcard\"]')", 20, "the catalog")
    check(page.eval("window.site.state().user") == "alice", "a viewer logs in to the site's API")

    # A film, through the URL the API issues.
    page.eval("document.querySelector('a[data-id=\"films/testcard\"]').click(); true")
    page.wait("window.site.state().currentTime >= 2", 30, "two seconds of the film")
    st = page.eval("window.site.state()")
    q = urllib.parse.parse_qs(urllib.parse.urlparse(st["url"]).query)
    check(q.get("sub") == ["v-1001"] and q.get("ip") == ["127.0.0.1"] and "sig" in q and "sid" in q,
          "the film plays through a URL signed for the viewer's opaque ID and address", st["url"].split("&sig=")[0])
    seg = page.eval(SEGMENT_OF % json.dumps(st["url"]))
    gexp, g = grant_expiry(seg)
    check(g == "1" and gexp > st["expires"], "its playlists hand the segments a grant that outlives the URL", {"url": st["expires"], "grant": gexp})
    check(status(page, "/vod/films/films/testcard/index.m3u8") == 401, "the playlist without the URL's signature is refused 401")
    target = round(st["duration"] * 0.6, 1)
    page.eval("window.site.seek(%f); true" % target)
    page.wait("!window.site.state().seeking", 15, "the seek")
    landed = page.eval("window.site.state().currentTime")
    play_on(page, "window.site.state()", 1, "a second played after the seek")
    check(abs(landed - target) < 0.5, "the film seeks to 60%", {"to": target, "landed": landed})

    while time.time() < st["expires"] + 1.5:
        time.sleep(0.2)
    code = status(page, st["url"])
    st2 = page.eval("window.site.state()")
    check(code == 403 and not st2["refused"] and not st2["error"],
          "once the URL expires it is refused 403, and the player plays on, on its grant", {"status": code, "state": st2})
    while time.time() < gexp + 1.5:
        time.sleep(0.2)
    code = status(page, seg)
    check(code == 403, "once the grant expires too, a segment fetched with it is refused 403", {"status": code})

    # The channel, through a URL the API issues.
    page.eval("document.querySelector('a[data-kind=\"channel\"]').click(); true")
    page.wait("window.site.state().playing.kind === 'channel' && window.site.state().url !== ''", 20, "the channel's URL")
    play_on(page, "window.site.state()", 2, "two seconds of the channel")
    st = page.eval("window.site.state()")
    check(st["url"].startswith("/hls/tv/films/index.m3u8?"), "the channel plays through a URL the API issues", st["url"].split("&sig=")[0])
    # Its grant lives play_grace (4s) past the URL; the player's next
    # playlist past that is refused, and the page says so.
    page.wait("window.site.state().refused", st["expires"] - time.time() + 4 + 20, "the channel to be refused")
    refused_at = time.time()
    msg = page.eval("document.getElementById('status').textContent")
    check(refused_at >= st["expires"] + 4 and "refused" in msg and page.eval("!document.getElementById('again').hidden"),
          "the channel is refused once its URL and grant expire, and the page offers to play again",
          {"after_expiry": round(refused_at - st["expires"], 1), "message": msg})
    check(not page.errors(), "the site's page threw nothing", page.errors())


def pages(chrome):
    print("--- fit's media pages", flush=True)
    page = chrome.page(BASE + "/media/library")
    page.wait("document.getElementById('login').open", 20, "the login dialog")
    page.eval("document.getElementById('login-name').value = 'staff';"
              "document.getElementById('login-password').value = 'example-password';"
              "document.getElementById('login-form').requestSubmit(); true")
    page.wait("document.querySelector('[data-item=\"films/testcard\"]') && document.querySelector('[data-channel=\"tv/films\"]')",
              20, "the library's films and the channel")
    page.wait("[...document.querySelectorAll('.poster img')].length === 2 && [...document.querySelectorAll('.poster img')].every((i) => i.complete && i.naturalWidth > 0)",
              10, "the posters")
    titles = page.eval("[...document.querySelectorAll('[data-item] .poster__title')].map((e) => e.textContent).join(',')")
    check(titles == "Colour bars,Test card", "the library page lists the films, with their posters, and the channel", titles)

    page.eval("document.querySelector('[data-item=\"films/testcard\"]').click(); true")
    page.wait("window.fitMedia.watch().currentTime >= 2", 30, "two seconds of the film")
    w = page.eval("window.fitMedia.watch()")
    check(w["signed"] and w["kind"] == "vod" and "sub=staff" in w["url"], "the player plays the film through a URL the pages sign",
          w["url"].split("&sig=")[0])
    target = round(w["duration"] * 0.6, 1)
    page.eval("window.fitMedia.seek(%f); true" % target)
    page.wait("!window.fitMedia.watch().seeking", 15, "the seek")
    landed = page.eval("window.fitMedia.watch().currentTime")
    play_on(page, "window.fitMedia.watch()", 1, "a second played after the seek")
    check(abs(landed - target) < 0.5, "the player seeks to 60%", {"to": target, "landed": landed})

    page.eval("document.querySelector('a[data-nav=\"library\"]').click(); true")
    page.wait("document.querySelector('[data-channel=\"tv/films\"]')", 20, "the library again")
    page.eval("document.querySelector('[data-channel=\"tv/films\"]').click(); true")
    page.wait("window.fitMedia.watch().kind === 'channel' && window.fitMedia.watch().state === 'started'", 20, "the channel")
    play_on(page, "window.fitMedia.watch()", 2, "two seconds of the channel")
    w = page.eval("window.fitMedia.watch()")
    page.wait("window.fitMedia.watch().refused", w["expires"] - time.time() + 4 + 20, "the channel to be refused")
    refused_at = time.time()
    badge = page.eval("document.getElementById('watch-state').textContent")
    check(refused_at >= w["expires"] + 4 and badge == "refused" and page.eval("!document.getElementById('watch-again').hidden"),
          "the channel is refused once its URL and grant expire, and the player offers to play again",
          {"after_expiry": round(refused_at - w["expires"], 1), "badge": badge})
    problems = page.eval("window.fitMedia.status().problems")
    check(problems == ["playback refused: HTTP 403"], "the pages met no other problem", problems)
    check(not page.errors(), "the media pages threw nothing", page.errors())


def main():
    wait_ready()
    chrome = Chrome()
    try:
        chrome.send("Browser.getVersion")
        for part in (site, pages):
            try:
                part(chrome)
            except Exception as e:
                check(False, part.__name__ + " ran to its end", str(e))
    finally:
        chrome.close()
        with open(os.path.join(OUT, "result.json"), "w") as f:
            json.dump(results, f, indent=1)
    print("%d checks, %d failed" % (len(results), failures), flush=True)
    sys.exit(1 if failures else 0)


main()
