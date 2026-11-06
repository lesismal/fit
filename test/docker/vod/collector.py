#!/usr/bin/env python3
"""Serves player.html and hls.min.js from its directory, and takes the
results the page POSTs to /result: prints them, writes them to the file
OUT names, and exits 0 when every item played (or was skipped), 1 when one
did not, 2 when no result came within the timeout.

It also stands for a video site's API: GET /sign?path=P&ttl=T asks fit's
media_tokens for a playback URL of P (POST SIGN_URL, with SIGN_AUTH, user:
password, as Basic) and answers what fit answered.

    collector.py PORT OUT [TIMEOUT_SECONDS]
"""
import base64
import http.server
import json
import os
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request

port, out = int(sys.argv[1]), sys.argv[2]
timeout = float(sys.argv[3]) if len(sys.argv) > 3 else 600
here = os.path.dirname(os.path.abspath(__file__))
status = {"code": 2}
done = threading.Event()


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=here, **kw)

    def do_GET(self):
        u = urllib.parse.urlparse(self.path)
        if u.path != "/sign":
            return super().do_GET()
        q = urllib.parse.parse_qs(u.query)
        body = json.dumps({"subject": "viewer-1", "ttl": q.get("ttl", ["10m"])[0], "paths": [q["path"][0]]}).encode()
        req = urllib.request.Request(os.environ["SIGN_URL"], data=body, method="POST",
                                     headers={"Content-Type": "application/json", "Authorization": "Basic " +
                                              base64.b64encode(os.environ["SIGN_AUTH"].encode()).decode()})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                status, out = resp.status, resp.read()
        except urllib.error.HTTPError as e:
            status, out = e.code, e.read()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def do_POST(self):
        if self.path != "/result":
            self.send_error(404)
            return
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.send_response(204)
        self.end_headers()
        res = json.loads(body)
        with open(out, "w") as f:
            json.dump(res, f, indent=1)
        failed = 0
        print("hls.js", res["hls"], "in", res["ua"])
        for r in res["results"]:
            state = "skip" if r.get("skipped") else ("ok  " if r.get("ok") else "FAIL")
            failed += state == "FAIL"
            print(state, r["item"], r["container"], "aes-128 keys=%s" % r.get("keys") if r.get("encrypted") else "clear",
                  r.get("codecs"), r.get("skipped") or
                  "levels=%s audio=%s subs=%s %sx%s seek %s->%s errors=%s%s" % (
                      r.get("levels"), r.get("audioTracks"), r.get("subtitleTracks"), r.get("width"),
                      r.get("height"), r.get("seekTo"), r.get("seekLanded"), r.get("errors"),
                      "" if "unsignedStatus" not in r else
                      " unsigned=%s expired=%s (seek after expiry on the grant)" % (r.get("unsignedStatus"), r.get("expiredStatus"))))
        status["code"] = 1 if failed else 0
        done.set()

    def log_message(self, *a):
        pass


srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
done.wait(timeout)
srv.shutdown()
sys.exit(status["code"])
