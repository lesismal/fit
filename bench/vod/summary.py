#!/usr/bin/env python3
"""Summarizes a run of bench/vod.sh: for each server and round, segments a
second (answered 200), MB/s, p99, and the server's resident memory, the
largest sampled (RSS) and the processes' peaks (VmHWM); then each server's
median over the rounds.

    summary.py DIR
"""
import glob
import json
import os
import statistics
import sys

out = sys.argv[1]
rows = {}
print("%-9s %5s %10s %9s %8s %8s %9s %s" % ("server", "round", "segments/s", "MB/s", "p99 ms", "RSS MiB", "peak MiB", ""))
for path in sorted(glob.glob(os.path.join(out, "run-*.txt"))):
    name = os.path.basename(path)[len("run-"):-len(".txt")]
    server, rnd = name.rsplit("-", 1)
    line = [l for l in open(path) if l.startswith("FITBENCH ")]
    if not line:
        continue
    j = json.loads(line[0][len("FITBENCH "):])
    secs = j["duration_us"] / 1e6
    mem = [l.split() for l in open(os.path.join(out, "mem-%s-%s.txt" % (server, rnd))) if l.strip()]
    rss = max(int(m[0]) for m in mem) / 1024 if mem else 0
    hwm = max(int(m[1]) for m in mem) / 1024 if mem else 0
    bad = j["errors"]["status"]
    ok = (j["requests"] - bad) / secs
    mbs = j["bytes"] / secs / 1e6
    p99 = j["latency_us"]["p99"] / 1000
    note = "not 2xx %.1f%%" % (100.0 * bad / j["requests"]) if bad else ""
    print("%-9s %5s %10.0f %9.0f %8.1f %8.0f %9.0f %s" % (server, rnd, ok, mbs, p99, rss, hwm, note))
    rows.setdefault(server, []).append((ok, mbs, p99, rss, hwm))
print()
print("median of the rounds")
for server, rs in rows.items():
    med = [statistics.median(c) for c in zip(*rs)]
    print("%-9s %5s %10.0f %9.0f %8.1f %8.0f %9.0f" % (server, len(rs), *med))
