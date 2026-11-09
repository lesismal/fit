#!/usr/bin/env python3
"""Summarizes bench/bt.sh's results: the median of each scenario's rounds,
fit beside anacrolix and fib alone, as a Markdown table.

usage: bench/bt/summary.py <results dir>
"""
import glob
import json
import os
import statistics
import sys


def main():
    d = sys.argv[1]
    runs = {}
    for p in sorted(glob.glob(os.path.join(d, "*.jsonl"))):
        for line in open(p):
            line = line.strip()
            if not line:
                continue
            r = json.loads(line)
            key = (r["scenario"], "mse" if r["mse"] else "plain", r["impl"])
            runs.setdefault(key, []).append(r)
    settings = os.path.join(d, "settings.txt")
    if os.path.exists(settings):
        print("```")
        print(open(settings).read().strip())
        print("```")
        print()
    print("| Scenario | Connections | Client | Rounds | MiB/s | CPU % | CPU s per GiB | Peak RSS MiB |")
    print("| --- | --- | --- | --- | --- | --- | --- | --- |")
    med = {}
    for key in sorted(runs):
        rs = runs[key]
        m = {f: statistics.median(r[f] for r in rs) for f in ("mib_per_sec", "cpu_percent", "cpu_seconds_per_gib", "peak_rss_mib")}
        med[key] = m
        print("| %s | %s | %s | %d | %.0f | %.0f | %.2f | %.0f |" % (key[0], key[1], key[2], len(rs),
              m["mib_per_sec"], m["cpu_percent"], m["cpu_seconds_per_gib"], m["peak_rss_mib"]))
    print()
    print("| Scenario | Connections | fit / anacrolix MiB/s | fit / anacrolix CPU per GiB | fit / fib MiB/s | fit / fib CPU per GiB |")
    print("| --- | --- | --- | --- | --- | --- |")

    def ratio(s, c, other, field):
        if (s, c, other) not in med:
            return "-"
        return "%.2f" % (med[(s, c, "fit")][field] / med[(s, c, other)][field])

    for (s, c, i) in sorted(med):
        if i != "fit":
            continue
        print("| %s | %s | %s | %s | %s | %s |" % (s, c,
              ratio(s, c, "anacrolix", "mib_per_sec"), ratio(s, c, "anacrolix", "cpu_seconds_per_gib"),
              ratio(s, c, "fib", "mib_per_sec"), ratio(s, c, "fib", "cpu_seconds_per_gib")))

if __name__ == "__main__":
    main()
