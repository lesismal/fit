#!/usr/bin/env python3
"""Summarize a comparison bench/ab.sh ran: requests a second and CPU per
request of A and B, by scenario and protocol, the change, and whether it is
past the spread of the rounds.

Usage: python3 bench/abreport.py DIR, where DIR holds a-<round>.json and
b-<round>.json, as bench/ab.sh -report fetches them.
"""
import glob
import json
import os
import sys
from collections import defaultdict


NOISE = float(os.environ.get("AB_NOISE", "1.0"))


def load(d):
    runs = defaultdict(lambda: defaultdict(list))  # (scenario, protocol) -> side -> [(rps, us/req, errors)]
    for path in sorted(glob.glob(os.path.join(d, "[ab]-*.json"))):
        side = os.path.basename(path)[0]
        for r in json.load(open(path)).get("results", []):
            if "requests_per_sec" not in r:
                continue
            rps = r["requests_per_sec"]
            us = r.get("cpu_cores", 0) / rps * 1e6 if rps else 0
            runs[(r["scenario"], r["protocol"])][side].append((rps, us, r.get("errors", 0)))
    return runs


def mean(xs):
    return sum(xs) / len(xs) if xs else 0


def main():
    d = sys.argv[1]
    for side in "ab":
        p = os.path.join(d, side + ".ref")
        if os.path.exists(p):
            print(f"{side.upper()}: {open(p).read().strip()}")
    p = os.path.join(d, "params")
    if os.path.exists(p):
        print(open(p).read().strip())
    runs = load(d)
    if not runs:
        print("no results yet")
        return
    print()
    print(f"{'scenario':8} {'protocol':9} {'A req/s':>10} {'B req/s':>10} {'change':>8}  "
          f"{'A µs/req':>8} {'B µs/req':>8}  {'rounds':>6}  verdict")
    for (scenario, protocol), sides in sorted(runs.items()):
        a, b = sides.get("a", []), sides.get("b", [])
        if not a or not b:
            continue
        ar, br = [x[0] for x in a], [x[0] for x in b]
        ma, mb = mean(ar), mean(br)
        change = (mb / ma - 1) * 100 if ma else 0
        # Past the noise when the two sides' ranges do not overlap and the
        # change is at least NOISE%: two builds of the same code differ by up
        # to about 0.7% on the host (R07e's bisection).
        if min(br) > max(ar) and change >= NOISE:
            verdict = "faster"
        elif max(br) < min(ar) and change <= -NOISE:
            verdict = "SLOWER"
        else:
            verdict = "within noise"
        errors = sum(x[2] for x in a + b)
        if errors:
            verdict += f", {errors} errors"
        print(f"{scenario:8} {protocol:9} {ma/1000:9.0f}k {mb/1000:9.0f}k {change:+7.1f}%  "
              f"{mean([x[1] for x in a]):8.2f} {mean([x[1] for x in b]):8.2f}  "
              f"{len(a)}/{len(b):<4}  {verdict}")


if __name__ == "__main__":
    main()
