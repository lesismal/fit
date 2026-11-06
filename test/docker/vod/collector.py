#!/usr/bin/env python3
"""Serves player.html and hls.min.js from its directory, and takes the
results the page POSTs to /result: prints them, writes them to the file
OUT names, and exits 0 when every item played (or was skipped), 1 when one
did not, 2 when no result came within the timeout.

    collector.py PORT OUT [TIMEOUT_SECONDS]
"""
import http.server
import json
import os
import sys
import threading

port, out = int(sys.argv[1]), sys.argv[2]
timeout = float(sys.argv[3]) if len(sys.argv) > 3 else 600
here = os.path.dirname(os.path.abspath(__file__))
status = {"code": 2}
done = threading.Event()


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **kw):
        super().__init__(*a, directory=here, **kw)

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
            print(state, r["item"], r["container"], r.get("codecs"), r.get("skipped") or
                  "levels=%s audio=%s subs=%s %sx%s seek %s->%s errors=%s" % (
                      r.get("levels"), r.get("audioTracks"), r.get("subtitleTracks"), r.get("width"),
                      r.get("height"), r.get("seekTo"), r.get("seekLanded"), r.get("errors")))
        status["code"] = 1 if failed else 0
        done.set()

    def log_message(self, *a):
        pass


srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
done.wait(timeout)
srv.shutdown()
sys.exit(status["code"])
