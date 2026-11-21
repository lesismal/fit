import re, statistics as st, sys
rows = {}
for line in open(sys.argv[1]):
    m = re.match(r'(\S+)\.r(\d) (fit|nginx) rps_line: (.*) srvcpu=([\d.]+)/(\d+)', line)
    if not m: continue
    tag, r, srv, rest, cpu, n = m.groups()
    d = dict(kv.split('=', 1) for kv in rest.split() if '=' in kv)
    def ms(x):
        if x is None: return None
        x = x.rstrip(',')
        if x.endswith('us'): return float(x[:-2]) / 1000
        if x.endswith('ms'): return float(x[:-2])
        if x.endswith('s'): return float(x[:-1]) * 1000
        return None
    rows.setdefault((tag, srv), []).append((float(d['rps']), float(cpu), int(n), ms(d.get('99%')), ms(d.get('mean'))))
order = []
for (tag, srv) in rows:
    if tag not in order: order.append(tag)
print(f"{'point':14} {'fit QPS':>10} {'nginx QPS':>10} {'fit/nginx':>9} {'fit p99/mean':>12} {'nginx p99/mean':>14} {'cpu fit/nginx':>14}")
for tag in order:
    f = rows.get((tag, 'fit')); g = rows.get((tag, 'nginx'))
    if not f or not g: continue
    med = lambda v, i: st.median([x[i] for x in v if x[i] is not None]) if any(x[i] is not None for x in v) else None
    fq, gq = med(f, 0), med(g, 0)
    fl = med(f, 3) if med(f, 3) is not None else med(f, 4)
    gl = med(g, 3) if med(g, 3) is not None else med(g, 4)
    kind = 'p99' if med(f, 3) is not None else 'mean'
    print(f"{tag:14} {fq:10.0f} {gq:10.0f} {fq/gq*100:8.1f}% {kind}{fl:8.2f} {kind}{gl:10.2f} {med(f,1):6.2f}/{med(g,1):.2f}")
