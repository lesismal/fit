import re, statistics as st, sys
SRV = sys.argv[2:] or ['fit-sweep', 'fit-cliff', 'fit-cliff4k', 'nginx']
rows = {}
def ms(x):
    if x is None: return None
    x = x.rstrip(',')
    if x.endswith('us'): return float(x[:-2]) / 1000
    if x.endswith('ms'): return float(x[:-2])
    if x.endswith('s'): return float(x[:-1]) * 1000
    return None
for line in open(sys.argv[1]):
    m = re.match(r'size-(\d+)\.r(\d) (\S+) rps_line: (.*) srvcpu=([\d.]+)/(\d+)', line)
    if not m: continue
    size, r, srv, rest, cpu, n = m.groups()
    d = dict(kv.split('=', 1) for kv in rest.split() if '=' in kv)
    rows.setdefault((int(size), srv), []).append((float(d['rps']), float(cpu), ms(d.get('99%'))))
sizes = sorted({s for s, _ in rows})
print(f"{'size':>6} " + ' '.join(f"{s:>12}" for s in SRV) + '  2nd/1st   2nd/4th   3rd/2nd   p99 by column (ms)')
for size in sizes:
    q = {}; p = {}
    for s in SRV:
        v = rows.get((size, s))
        if v:
            q[s] = st.median([x[0] for x in v]); p[s] = st.median([x[2] for x in v])
    def ratio(a, b):
        return f"{q[a]/q[b]*100:10.1f}%" if a in q and b in q else f"{'-':>11}"
    print(f"{size//1024:>4}K  " + ' '.join(f"{q.get(s, 0):12.0f}" for s in SRV) +
          ' ' + ratio(SRV[1], SRV[0]) + ratio(SRV[1], SRV[-1]) + (ratio(SRV[2], SRV[1]) if len(SRV) > 3 else '') +
          '  ' + '/'.join(f"{p[s]:.2f}" if s in p else '-' for s in SRV))
