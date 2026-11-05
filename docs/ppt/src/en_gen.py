#!/usr/bin/env python3
# English edition of the fit introduction deck: same layout system as gen.py.
import math, os, sys

import gen
from gen import (T, para, card, header, footer, page, chip, code_card, wrap, text_w,
                 INK, BODY, MUTED, FAINT, LINE, BG, WHITE, BLUE, BLUE_BG, CYAN, CYAN_BG,
                 GREEN, GREEN_BG, AMBER, AMBER_BG, ROSE, ROSE_BG, NAVY, NAVY2,
                 NAVY_LINE, NAVY_TXT, NAVY_MUT, NAVY_BLUE, NAVY_CYAN, KW, VAL, PN, STR, DTX)

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)))
MONO = "Menlo"

def footer(no, total=16):
    return "\n".join([
        T(56, 516, "fit · a reverse proxy, load balancer and API gateway built on fib", 9.5, FAINT),
        T(904, 516, f"{no:02d} / {total}", 9.5, FAINT, 500, "end"),
    ])

# ---------------------------------------------------------------- P1 cover
def p01():
    s = [f'<rect width="960" height="540" fill="{NAVY}"/>']
    cx, cy, R = 770, 182, 128
    s.append(f'<circle cx="{cx}" cy="{cy}" r="{R}" fill="#0E1730" opacity="0.55"/>')
    for r in (R, R - 34, R - 68):
        s.append(f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="none" stroke="{NAVY_LINE}" stroke-width="1" opacity="0.8"/>')
    for ang, col in ((90, NAVY_BLUE), (30, NAVY_CYAN), (-30, NAVY_BLUE), (-90, GREEN), (-150, NAVY_CYAN), (150, "#D97706")):
        a = math.radians(ang)
        s.append(f'<circle cx="{cx + R*math.cos(a):.1f}" cy="{cy - R*math.sin(a):.1f}" r="4" fill="{col}"/>')
    s.append(f'<circle cx="{cx}" cy="{cy}" r="46" fill="#121C33" stroke="{BLUE}" stroke-width="1.5"/>')
    s.append(T(cx, cy - 1, "fib", 21, "#EAF1FF", 600, "middle"))
    s.append(T(cx, cy + 18, "engine", 8.5, "#7E92B4", 400, "middle"))

    s.append(T(72, 92, "APACHE-2.0 · GO · FIB ENGINE", 10.5, NAVY_BLUE, 500))
    s.append(T(66, 196, "fit", 96, "#FFFFFF", 700))
    s.append(f'<rect x="74" y="212" width="56" height="4" rx="2" fill="{BLUE}"/>')
    s.append(T(72, 250, "A reverse proxy, load balancer and API gateway on fib", 16.5, "#C9D5EA"))
    s.append(T(72, 284, "nginx-level throughput on small responses, Caddy's secure defaults,", 12, NAVY_MUT))
    s.append(T(72, 304, "Traefik's model — plus WebRTC, a console and LLM sharing they lack.", 12, NAVY_MUT))

    s.append(f'<line x1="72" y1="324" x2="904" y2="324" stroke="#22304C" stroke-width="1"/>')
    feats = [
        ("Reverse proxy & balancing", "9 policies · active + passive checks", NAVY_BLUE),
        ("TLS termination & auto HTTPS", "ACME · OCSP · mTLS · SNI", NAVY_CYAN),
        ("Static files & response cache", "sendfile · memory + disk tiers", GREEN),
        ("API gateway policy", "auth · rate limits · breaker · rewrite", NAVY_BLUE),
        ("Config reload, no restart", "atomic graph swap, nothing in flight is cut", NAVY_CYAN),
        ("Web management console", "edit any config behind a login · encrypted store", GREEN),
        ("WebRTC & streaming media", "TURN · WHIP/WHEP · media server proxy", NAVY_BLUE),
        ("tunnel & LLM sharing", "reach services behind NAT · share models", NAVY_CYAN),
        ("Many protocols, one port", "HTTP · TLS · gRPC · SOCKS by first bytes", GREEN),
    ]
    for i, (t, d, col) in enumerate(feats):
        x = 72 + (i % 3) * 277
        ty = 358 + (i // 3) * 54
        s.append(f'<circle cx="{x+3}" cy="{ty-4}" r="2.8" fill="{col}"/>')
        s.append(T(x + 14, ty, t, 12, "#E9EFFA", 600))
        s.append(T(x + 14, ty + 17, d, 9.5, "#8296B8"))

    s.append(T(72, 516, "github.com/lesismal/fit", 10, "#4F6182"))
    s.append(T(904, 516, "v1.0.0 · Apache-2.0 · 2026.11", 10, "#4F6182", 400, "end"))
    return page("\n".join(s), NAVY)

# ---------------------------------------------------------------- P2 problem
def p02():
    s = [header(2, "01 / Background", "Three strengths, three blind spots",
                "nginx, Caddy and Traefik each nail one thing — and each leaves a different gap.")]
    cols = [
        ("nginx", "NGINX · 2004 · C", CYAN,
         ["Event loop, one per core", "Best static files and cache", "Tiny cost per idle conn"],
         ["Own DSL, hard to validate", "Changes need a reload", "Auto HTTPS etc. is Plus"]),
        ("Caddy", "CADDY · 2015 · GO", BLUE,
         ["HTTPS on by default", "Strict, programmable JSON", "Atomic Admin API"],
         ["One goroutine per connection", "Memory and GC cost at scale", "Cache, limits, L4 via plugins"]),
        ("Traefik", "TRAEFIK · 2016 · GO", "#7C5CFC",
         ["Provider-driven, K8s native", "Dashboard, metrics, tracing", "Clean four-layer model"],
         ["Usually slowest and heaviest", "Not a web server: no static", "No built-in response cache"]),
    ]
    for x, (name, tag, col, pro, con) in zip([56, 347, 638], cols):
        s.append(card(x, 172, 266, 268))
        s.append(f'<circle cx="{x+27}" cy="{172+38}" r="5" fill="{col}"/>')
        s.append(T(x + 42, 172 + 46, name, 20, INK, 600))
        s.append(T(x + 42, 172 + 64, tag, 9, MUTED, 500))
        s.append(f'<line x1="{x+24}" y1="{172+80}" x2="{x+242}" y2="{172+80}" stroke="{LINE}"/>')
        s.append(T(x + 24, 172 + 102, "STRENGTHS", 9, GREEN, 600))
        for i, t in enumerate(pro):
            s.append(f'<circle cx="{x+27}" cy="{172+120+i*20-4}" r="2.4" fill="{GREEN}"/>')
            s.append(T(x + 38, 172 + 120 + i * 20, t, 11, "#22304A"))
        s.append(T(x + 24, 172 + 194, "GAPS", 9, ROSE, 600))
        for i, t in enumerate(con):
            s.append(f'<circle cx="{x+27}" cy="{172+212+i*20-4}" r="2.4" fill="{ROSE}"/>')
            s.append(T(x + 38, 172 + 212 + i * 20, t, 11, BODY))
    s.append(f'<rect x="56" y="456" width="848" height="42" rx="8" fill="{INK}"/>')
    s.append(T(76, 483, "fit's take:", 12, "#6FA8FF", 600))
    s.append(T(158, 483, "take the best of each into one data plane, then let fib close the gaps they share.", 12, "#C9D5EA"))
    s.append(footer(2))
    return page("\n".join(s))

# ---------------------------------------------------------------- P3 overview
def p03():
    s = [header(3, "02 / Overview", "One data plane, six jobs",
                "listener → router → middleware → service → upstream, the same model for HTTP and L4.")]
    cx, cy = 480, 320
    nodes = [
        (225, 320, "Reverse proxy", "HTTP · WebSocket · gRPC · TCP/UDP"),
        (735, 320, "Load balancing", "9 policies · active + passive checks"),
        (352, 202, "TLS termination", "ACME · OCSP · mTLS · SNI"),
        (608, 202, "Static files & cache", "sendfile · two tiers · cache lock"),
        (352, 438, "API gateway", "auth · limits · breaker · rewrite"),
        (608, 438, "Dynamic config", "Admin API · providers · console"),
    ]
    for x, y, _, _ in nodes:
        s.append(f'<line x1="{cx}" y1="{cy}" x2="{x}" y2="{y}" stroke="#CBD6E8" stroke-width="1.2"/>')
    s.append(f'<ellipse cx="{cx}" cy="{cy}" rx="100" ry="50" fill="{BLUE}"/>')
    s.append(T(cx, cy - 2, "fit", 28, WHITE, 700, "middle"))
    s.append(T(cx, cy + 20, "gateway & edge proxy", 9, "#DCE6FF", 400, "middle"))
    for x, y, name, sub in nodes:
        w, h = 206, 62
        s.append(card(x - w / 2, y - h / 2, w, h, WHITE, "#DCE4F0", 9))
        s.append(T(x, y - 3, name, 13.5, INK, 600, "middle"))
        s.append(T(x, y + 16, sub, 9, MUTED, 400, "middle"))
    s.append(T(56, 494, "The same engine also runs WebRTC/TURN, tunnel and LLM sharing — see page 12.", 11, MUTED))
    s.append(footer(3))
    return page("\n".join(s))

# ---------------------------------------------------------------- P4 architecture
def p04():
    s = [header(4, "03 / Architecture", "Control plane, data plane",
                "Config only compiles into an immutable graph; the data plane reads it through one pointer, with no blocking call on the request path.")]
    s.append(T(56, 178, "CONTROL PLANE", 9, MUTED, 600))
    a = [
        ("Config sources", "files · Admin API · providers"),
        ("Adapt & validate", "errors carry path and position"),
        ("Provision modules", "certs, health checks, pools"),
        ("Immutable graph", "v(n+1), built whole"),
    ]
    axs = [56, 273, 490, 707]
    for x, (t, sub) in zip(axs, a):
        s.append(card(x, 190, 196, 62, WHITE, "#DCE4F0", 9))
        s.append(T(x + 16, 216, t, 13, INK, 600))
        s.append(T(x + 16, 236, sub, 9, MUTED))
    for x in axs[:-1]:
        s.append(f'<line x1="{x+198}" y1="221" x2="{x+212}" y2="221" stroke="{BLUE}" stroke-width="1.6"/>')
        s.append(f'<path d="M{x+209} 216.5 l5 4.5 l-5 4.5" stroke="{BLUE}" stroke-width="1.6" fill="none"/>')
    s.append(f'<line x1="480" y1="252" x2="480" y2="316" stroke="{BLUE}" stroke-width="1.6"/>')
    s.append(f'<path d="M480 324 l-5 -9 l10 0 z" fill="{BLUE}"/>')
    s.append(T(496, 278, "Atomic pointer swap", 11, BLUE, 600))
    s.append(T(496, 295, "listeners added or removed in place (AddBinding / RemoveBinding)", 9, MUTED))

    s.append(T(56, 344, "DATA PLANE (fib event loops)", 9, MUTED, 600))
    d = [
        ("listeners", "TCP · UDP · TLS"),
        ("mux", "split by first bytes"),
        ("routers", "host · path · SNI"),
        ("middlewares", "auth · limits · cache"),
        ("services", "balancing · mirroring"),
        ("upstream pools", "fib async clients"),
    ]
    dxs = [56, 200, 344, 488, 632, 776]
    for x, (t, sub) in zip(dxs, d):
        s.append(card(x, 356, 128, 64, "#F1F5FF", "#D6E2FF", 9))
        s.append(T(x + 12, 382, t, 11.5, INK, 600))
        s.append(T(x + 12, 402, sub, 8.5, MUTED))
    for x in dxs[:-1]:
        s.append(f'<path d="M{x+133} 384 l4.5 4 l-4.5 4" stroke="#93AEE8" stroke-width="1.4" fill="none"/>')
    notes = ["In-flight requests see no change", "The old graph drains, then frees", "Nothing blocks the request path"]
    for i, n in enumerate(notes):
        x = 56 + i * 284
        s.append(f'<circle cx="{x+6}" cy="446" r="6" fill="{GREEN_BG}"/>')
        s.append(f'<path d="M{x+2.5} 446 l2.6 2.8 l4.2 -5" stroke="{GREEN}" stroke-width="1.5" fill="none"/>')
        s.append(T(x + 20, 450, n, 11, BODY, 500))
    s.append(footer(4))
    return page("\n".join(s))

# ---------------------------------------------------------------- P5 engine
def p05():
    s = [header(5, "04 / Why it is fast", "No goroutine on the request path",
                "fit does not hand each connection a goroutine: the data plane runs on fib's event loops.")]
    items = [
        ("Idle connections cost almost nothing", "One connection struct; read and write buffers are borrowed for a round and returned. No resident bufio stacks."),
        ("Backpressure is end to end", "HoldReads carries a slow client's wait back to the upstream; a write watermark and a global pending-bytes budget cap the rest."),
        ("Static files, nearly zero copy", "sendfile, an in-memory file cache and precompressed twins: a hit is just a write."),
        ("Blocking work lives in bounded pools", "External auth, plugins and disk cache misses run in pools of their own, with one-way waits."),
        ("prefork when you want it", "One process per CPU with its own heap and GC, sharing the port through SO_REUSEPORT, as nginx's workers do."),
    ]
    pos = [(56, 190), (56, 286), (56, 382), (500, 190), (500, 286)]
    for i, ((x, y), (t, d)) in enumerate(zip(pos, items)):
        s.append(T(x, y - 6, f"{i+1:02d}", 10, BLUE, 600))
        s.append(T(x + 26, y, t, 14, INK, 600))
        p, _ = para(x + 26, y + 22, d, 376, 11, BODY, 400, 17)
        s.append(p)
    s.append(card(500, 356, 404, 112, "#F1F5FF", "#D6E2FF", 10))
    s.append(T(520, 380, "FOR COMPARISON", 9, BLUE, 600))
    p, _ = para(520, 402, "40,000 idle TLS connections: fit 475 MiB, nginx 1,305 MiB. Caddy and Traefik pay for net/http — one goroutine per connection.", 364, 10.5, "#2A3D5C", 400, 16.5)
    s.append(p)
    s.append(footer(5))
    return page("\n".join(s))

# ---------------------------------------------------------------- P6 protocols
def p06():
    s = [header(6, "05 / Protocols", "One model, from L7 down to L4",
                "Inbound and upstream protocols are chosen separately; Layer 4 services reuse the same listener → service model.")]
    rows = [
        ("HTTP/1.1", "Streaming proxy both ways", "hop-by-hop headers stripped · X-Forwarded-* / Forwarded · trusted proxies"),
        ("HTTP/2", "h2 / h2c inbound and out", "flow control and backpressure linked · gRPC streams and trailers"),
        ("HTTP/3", "QUIC · TLS 1.3", "no quic-go: the same engine as fib"),
        ("WebSocket", "h1 / h2 / h3 inbound", "proxied to an HTTP/1.1 upstream, upgrade and tunnel"),
        ("TCP / UDP", "Layer 4 services", "SNI pass-through without termination · PROXY protocol"),
        ("SOCKS / CONNECT", "Forward proxy", "SOCKS5 and HTTP CONNECT with client and target rules"),
        ("Many protocols, one port", "Split by first bytes", "HTTP, TLS, SOCKS and raw TCP on a single listener"),
    ]
    top, hh, rh = 176, 36, 40
    s.append(f'<rect x="56" y="{top}" width="848" height="{hh}" rx="8" fill="{BLUE_BG}"/>')
    s.append(T(76, top + 23, "PROTOCOL", 10, BLUE, 600))
    s.append(T(256, top + 23, "WHAT IT DOES", 10, BLUE, 600))
    s.append(T(506, top + 23, "NOTES", 10, BLUE, 600))
    for i, (a, b, c) in enumerate(rows):
        y = top + hh + i * rh
        if i % 2 == 1:
            s.append(f'<rect x="56" y="{y}" width="848" height="{rh}" fill="#FFFFFF"/>')
        s.append(f'<line x1="56" y1="{y+rh}" x2="904" y2="{y+rh}" stroke="#EDF1F7"/>')
        s.append(T(76, y + 26, a, 12.5, INK, 600))
        s.append(T(256, y + 26, b, 11, BODY, 500))
        s.append(T(506, y + 26, c, 10, MUTED))
    s.append(footer(6))
    return page("\n".join(s))

# ---------------------------------------------------------------- P7 governance
def p07():
    s = [header(7, "06 / Traffic policy", "Policy, not scripts",
                "Load balancing, health checks, resilience and admission are config fields — not Lua or plugins bolted on.")]
    quads = [
        ("Load balancing", "9 policies", BLUE, BLUE_BG,
         "Round robin · weighted · least connections · random · P2C · consistent hash by IP / URI / header / cookie · sticky cookie"),
        ("Health checks", "active + passive", GREEN, GREEN_BG,
         "Active HTTP / TCP probes; passive max_fails and fail_timeout; a failing backend leaves rotation and returns on its own."),
        ("Resilience", "retry · breaker · timeouts", AMBER, AMBER_BG,
         "Retries within a budget, a circuit breaker, connect / response / idle timeouts; weighted mirroring and failover."),
        ("Admission", "rate · concurrency · size", ROSE, ROSE_BG,
         "Rate limits by client IP, header or route; concurrency caps, body and connection limits, write-stall timeouts."),
    ]
    for i, (t, badge, col, cbg, d) in enumerate(quads):
        x = 56 + (i % 2) * 424
        y = 172 + (i // 2) * 152
        s.append(card(x, y, 424, 136))
        s.append(f'<rect x="{x}" y="{y+18}" width="4" height="100" rx="2" fill="{col}"/>')
        s.append(T(x + 28, y + 40, t, 15, INK, 600))
        c, _ = chip(x + 28 + text_w(t, 15) + 12, y + 23, badge, col, cbg, 9.5, 20)
        s.append(c)
        p, _ = para(x + 28, y + 70, d, 368, 11, BODY, 400, 17)
        s.append(p)
    s.append(footer(7))
    return page("\n".join(s))

# ---------------------------------------------------------------- P8 benchmark
def p08():
    s = [header(8, "07 / Benchmark", "Level with nginx on its home turf",
                "Fixed response, HTTP/1.1, 500 in flight: fit 671,426 rps / p99 0.87 ms, nginx 668,401 rps / p99 2.09 ms.")]
    s.append(T(56, 182, "THROUGHPUT · REQUESTS PER SECOND", 9, MUTED, 600))
    bars = [("fit", 671426, BLUE), ("nginx", 668401, "#AFC0DA"), ("Envoy", 260701, "#C9D5E7"), ("Caddy", 256938, "#C9D5E7")]
    scale = 296 / 680000
    for i, (n, v, col) in enumerate(bars):
        y = 198 + i * 44
        s.append(T(120, y + 17, n, 12, INK if n == "fit" else MUTED, 600 if n == "fit" else 400, "end"))
        w = v * scale
        s.append(f'<rect x="132" y="{y}" width="{w:.1f}" height="26" rx="4" fill="{col}"/>')
        s.append(T(132 + w + 8, y + 18, f"{v:,}", 11.5, INK if n == "fit" else MUTED, 600))
    s.append(T(132, 392, "p99: fit 0.87 ms · nginx 2.09 ms · Envoy 2.32 ms · Caddy 9.81 ms", 9.5, MUTED))

    s.append(card(500, 172, 404, 232))
    s.append(T(520, 198, "MORE READINGS", 9, MUTED, 600))
    more = [
        ("h2c 400×25", "3,450,552", "nginx 488,523"),
        ("site HTTPS/1.1", "98,552", "nginx 81,462 (+21%)"),
        ("proxy-download 1 MiB", "14,411", "nginx 5,105"),
        ("40,000 idle TLS — memory", "475 MiB", "nginx 1,305 MiB"),
    ]
    for i, (a, b, c) in enumerate(more):
        y = 226 + i * 46
        s.append(T(520, y, a, 10, MUTED))
        s.append(T(884, y, b, 12.5, INK, 600, "end"))
        s.append(T(884, y + 16, c, 9, FAINT, 400, "end"))
        if i < 3:
            s.append(f'<line x1="520" y1="{y+26}" x2="884" y2="{y+26}" stroke="#EDF1F7"/>')

    s.append(T(56, 426, "Setup: AMD Ryzen 7 7800X3D (16 CPUs) · Ubuntu 24.04 · nginx 1.31.4 / Caddy 2.10.2 / Traefik 3.5.0;", 9, FAINT))
    s.append(T(56, 442, "one host, loopback, 6 server CPUs, Linux host networking; data: bench/results/plan-2026-10-30.", 9, FAINT))
    s.append(T(56, 464, "Straight reading: proxying to a backend is still 66–78% of nginx, and 40k idle-connection throughput trails it.", 9, AMBER, 500))
    s.append(footer(8))
    return page("\n".join(s))

# ---------------------------------------------------------------- P9 config
def p09():
    s = [header(9, "08 / Configuration", "One strict config, three syntaxes",
                "The same fields in every format, each typed and documented; a decode error names its path and position.")]
    lines = [
        [("listener", KW), (" web ", DTX), ("{", PN)],
        [("    address", DTX), (" :8080", VAL)],
        [("    protocols", DTX), (" http/1.1 h2c", VAL)],
        [("}", PN)],
        [],
        [("route", KW), (" hello ", DTX), ("{", PN)],
        [("    match ", DTX), ("{", PN)],
        [("        path", DTX), (" /hello", VAL)],
        [("    }", PN)],
        [("    handler", KW), (" respond ", DTX), ("{", PN)],
        [("        body", DTX), (' "hello from fit\\n"', STR)],
        [("    }", PN)],
        [("}", PN)],
    ]
    s.append(code_card(56, 172, 430, 276, "hello.conf", lines))
    cx = 510
    for lab, fg, bg in ((".conf", WHITE, BLUE), ("YAML", BLUE, BLUE_BG), ("JSON", BLUE, BLUE_BG)):
        c, w = chip(cx, 176, lab, fg, bg, 10, 22, 11)
        s.append(c)
        cx += w + 8
    pts = [
        ("Strict decoding", "Unknown fields and wrong types fail with a path and a position; fit validate checks without starting the server."),
        ("One schema", "fit schema prints the JSON Schema that editor hints and the runtime check share."),
        ("Split it up", "fit run -c base.conf -c conf.d merges files and directories in order."),
        ("Migrate", "fit adapt converts between .conf, YAML and JSON, reads nginx configs and Caddyfiles, and lists what it could not convert."),
    ]
    for i, (t, d) in enumerate(pts):
        y = 236 + i * 58
        s.append(T(510, y, t, 12.5, INK, 600))
        p, _ = para(510, y + 20, d, 384, 10.5, BODY, 400, 15.5)
        s.append(p)
    s.append(f'<line x1="56" y1="474" x2="904" y2="474" stroke="{LINE}"/>')
    s.append(T(56, 496, "SIGHUP or -watch reloads: listeners on unchanged addresses stay as they are; a bad config keeps the running one.", 9.5, MUTED))
    s.append(footer(9))
    return page("\n".join(s))

# ---------------------------------------------------------------- P10 ops
def p10():
    s = [header(10, "09 / Operations", "Change config without a restart",
                "Every change takes one path: build the whole new graph first, and swap only once it works.")]
    steps = [
        ("Validate", "fit validate or the schema fails: nothing changes."),
        ("Provision", "certs, health checks and pools are all ready."),
        ("Atomic swap", "one pointer; new requests take the new graph."),
        ("Drain", "in-flight requests finish, then the graph is freed."),
    ]
    xs = [56, 273, 490, 707]
    for i, ((x, (t, d))) in enumerate(zip(xs, steps)):
        s.append(f'<circle cx="{x+19}" cy="198" r="17" fill="{BLUE_BG}"/>')
        s.append(T(x + 19, 203, str(i + 1), 12, BLUE, 600, "middle"))
        if i < 3:
            s.append(f'<line x1="{x+42}" y1="198" x2="{x+190}" y2="198" stroke="#CBD6E8" stroke-width="1" stroke-dasharray="3 4"/>')
        s.append(T(x + 46, 194, t, 13, INK, 600))
        p, _ = para(x + 46, 214, d, 142, 10, BODY, 400, 15)
        s.append(p)
    s.append(card(56, 282, 848, 54, "#F1F5FF", "#D6E2FF", 8))
    s.append(T(76, 315, "2,000 hot swaps / 6 seconds / 8 concurrent clients: about 337,000 requests and 32 long downloads, zero failures.", 10.5, "#2A3D5C", 500))
    cards = [
        ("Admin API", "GET / PUT / PATCH / DELETE /config; ETag against lost updates; versions and rollback; /config/effective shows the live config and where each part came from."),
        ("Encrypted store", "The config is encrypted in admin.store (Argon2id + XChaCha20-Poly1305); fit store export / import moves it."),
        ("Web console", "Every setting editable behind a login, all pages in one workbench; saved back to the store through the same validation."),
    ]
    for i, (t, d) in enumerate(cards):
        x = 56 + i * 291
        s.append(card(x, 356, 266, 120))
        s.append(T(x + 22, 384, t, 12.5, INK, 600))
        p, _ = para(x + 22, 404, d, 222, 10, BODY, 400, 15)
        s.append(p)
    s.append(footer(10))
    return page("\n".join(s))

# ---------------------------------------------------------------- P11 observability
def p11():
    s = [header(11, "10 / Observability", "When it breaks, see what happened",
                "Logs, metrics and traces are built in — there is no sidecar to assemble.")]
    cols = [
        ("ACCESS LOG", "Logs", BLUE, [
            "JSON, logfmt or line format",
            "Bounded buffers, off the event loop",
            "Levels per logger; only 4xx/5xx if you want",
        ]),
        ("METRICS", "Metrics", GREEN, [
            "Prometheus /metrics, by listener and route",
            "/status: connections, upstream health, certs",
            "Cache hits, rate limits, breaker counts",
        ]),
        ("TRACING", "Traces", AMBER, [
            "OpenTelemetry, W3C trace context",
            "Trace and sampling decision reach upstream",
            "Spans and metrics exported over OTLP/HTTP",
        ]),
    ]
    for i, (en, t, col, items) in enumerate(cols):
        x = 56 + i * 291
        s.append(card(x, 172, 266, 190))
        s.append(f'<rect x="{x}" y="{172+22}" width="4" height="32" rx="2" fill="{col}"/>')
        s.append(T(x + 24, 172 + 36, en, 8.5, col, 600))
        s.append(T(x + 24, 172 + 60, t, 16, INK, 600))
        y = 172 + 96
        for it in items:
            s.append(f'<circle cx="{x+27}" cy="{y-4}" r="2.4" fill="{col}"/>')
            p, y2 = para(x + 38, y, it, 206, 10, BODY, 400, 15)
            s.append(p)
            y = y2 + 4

    s.append(card(56, 386, 848, 84, "#F1F5FF", "#D6E2FF", 10))
    s.append(T(76, 412, "Dashboard and /status", 11, INK, 600))
    facts = [("listeners and connections", 76), ("upstream and backend health", 300), ("certificates and expiry", 524), ("config versions and source", 730)]
    for t, x in facts:
        s.append(T(x, 442, t, 10, "#2A3D5C"))
    s.append(T(56, 496, "One trace id follows a request: accept → route → middleware → upstream.", 9.5, MUTED))
    s.append(footer(11))
    return page("\n".join(s))

# ---------------------------------------------------------------- P12 edge roles
def p12():
    s = [header(12, "11 / Other roles on the same engine", "Beyond the gateway: media, tunnels, a model gateway",
                "No second deployment: these roles share the gateway's listeners, config and hot reload.")]
    bands = [
        ("WebRTC", "WEBRTC · MEDIA", CYAN,
         "A TURN server over UDP / TCP / TLS, WHIP and WHEP with fit as the media server, one UDP port proxying to other media servers.",
         ["TURN", "WHIP / WHEP", "webrtc_proxy"]),
        ("tunnel", "TUNNEL · REACH BEHIND NAT", BLUE,
         "hub / node / proxy roles over TCP, WebSocket or QUIC links, exposing services behind NAT at the edge.",
         ["TCP", "WebSocket", "QUIC"]),
        ("LLM", "LLM · SHARE MODELS", GREEN,
         "hub / node / proxy share models across machines and serve agents OpenAI, Anthropic, Gemini, Ollama and Cohere compatible APIs.",
         ["OpenAI-compatible", "Anthropic", "Ollama"]),
    ]
    for i, (name, tag, col, d, chips_) in enumerate(bands):
        y = 176 + i * 92
        s.append(card(56, y, 848, 84))
        s.append(f'<rect x="56" y="{y+14}" width="4" height="56" rx="2" fill="{col}"/>')
        s.append(T(84, y + 38, name, 18, INK, 600))
        s.append(T(84, y + 58, tag, 7.5, MUTED, 600))
        p, _ = para(280, y + 30, d, 600, 10.5, BODY, 400, 15.5)
        s.append(p)
        cx = 280
        for c in chips_:
            ch, w = chip(cx, y + 56, c, col, "#F1F5F9", 9, 19, 9)
            s.append(ch)
            cx += w + 7
    s.append(T(56, 486, "One config, one binary, one engine; roles are switched on per page in the console.", 10.5, MUTED))
    s.append(footer(12))
    return page("\n".join(s))

# ---------------------------------------------------------------- P13 extensions
def p13():
    s = [header(13, "12 / Extending", "Extend in Go, hot-swap in WASM",
                "A module is a Go package: compiled in, it runs on fib's workers with nothing between it and the request.")]
    steps = [
        "Write a Go package implementing Handler or Middleware",
        'Register it in init: module.Register("http.middleware.hello", …)',
        "fit build -with github.com/you/fit-hello compiles it in",
    ]
    for i, t in enumerate(steps):
        y = 190 + i * 44
        s.append(f'<circle cx="{69}" cy="{y-4}" r="12" fill="{BLUE_BG}"/>')
        s.append(T(69, y, str(i + 1), 10.5, BLUE, 600, "middle"))
        s.append(T(92, y, t, 11.5, INK, 500))
    code = [
        [("func init() ", KW), ("{", PN)],
        [("    module.Register(module.Info{", DTX)],
        [("        ID:  ", DTX), ('module.Middlewares + ".hello"', STR)],
        [("        New: ", DTX), ("func() any { return new(Hello) },", DTX)],
        [("    })", PN)],
        [("}", PN)],
    ]
    s.append(code_card(56, 316, 430, 156, "hello.go", code, 18, 10, 40))
    cards = [
        ("22 built-in modules", BLUE, "8 handlers + 14 middleware; params decoded strictly and documented into the JSON Schema."),
        ("WASM at runtime", CYAN, "Runs in a wazero sandbox on a bounded pool of its own; Proxy-Wasm filters compatible."),
        ("No layer in between", GREEN, "Modules run on fib's workers; fit build -without drops providers you do not use."),
    ]
    for i, (t, col, d) in enumerate(cards):
        y = 172 + i * 100
        s.append(card(500, y, 404, 88))
        s.append(f'<circle cx="524" cy="{y+26}" r="4" fill="{col}"/>')
        s.append(T(538, y + 30, t, 12.5, INK, 600))
        p, _ = para(524, y + 54, d, 356, 10, BODY, 400, 15)
        s.append(p)
    s.append(footer(13))
    return page("\n".join(s))

# ---------------------------------------------------------------- P14 status
def p14():
    s = [header(14, "13 / Status", "Every milestone through 1.0 is done",
                "Each milestone met its exit criteria; the external security audit is the only thing left before 1.0.")]
    stats = [
        ("98.0%", "coverage floor", "race + fuzz gate CI"),
        ("1,564", "test functions", "plus 11 fuzz targets"),
        ("56", "runnable examples", "every feature, three formats"),
        ("22", "built-in modules", "8 handlers + 14 middleware"),
    ]
    for i, (num, lab, sub) in enumerate(stats):
        x = 56 + i * 215
        s.append(card(x, 172, 200, 152))
        s.append(T(x + 24, 172 + 62, num, 38, BLUE, 700))
        s.append(T(x + 24, 172 + 94, lab, 11.5, INK, 600))
        s.append(T(x + 24, 172 + 116, sub, 9, MUTED))
    chips_ = ["4 providers", "9 roles", "164k lines of Go", "21.1 MB binary", "Apache-2.0"]
    cx = 56
    for c in chips_:
        ch, w = chip(cx, 348, c, "#46536B", "#EDF1F7", 10, 24, 12)
        s.append(ch)
        cx += w + 9
    s.append(card(56, 392, 848, 72))
    rows = [("DONE", GREEN, "M0–M5 and every 1.0 iteration"),
            ("TO DO", AMBER, "1.0's external security audit (outside the code)"),
            ("IN PROGRESS", BLUE, "P2P and VPN (fib's WebRTC/TURN is in place)")]
    for i, (lab, col, val) in enumerate(rows):
        x = 56 + i * 283
        s.append(f'<circle cx="{x+26}" cy="{392+28}" r="4" fill="{col}"/>')
        s.append(T(x + 38, 392 + 32, lab, 8.5, MUTED, 600))
        s.append(T(x + 26, 392 + 52, val, 9.5, INK, 500))
    s.append(footer(14))
    return page("\n".join(s))

# ---------------------------------------------------------------- P15 getting started
def p15():
    s = [header(15, "14 / Getting started", "Three commands to a site over HTTPS",
                "No DSL to learn first: the smallest config is a listener and a route.")]
    steps = [
        ("Install", "go install github.com/lesismal/fit/cmd/fit@latest"),
        ("Validate", "fit validate -c examples/hello/fit.conf"),
        ("Run", "fit run -c examples/hello/fit.conf"),
    ]
    for i, (t, cmd) in enumerate(steps):
        y = 200 + i * 82
        s.append(f'<circle cx="{72}" cy="{y-6}" r="15" fill="{BLUE_BG}"/>')
        s.append(T(72, y, str(i + 1), 11, BLUE, 600, "middle"))
        s.append(T(100, y - 2, t, 12, INK, 600))
        s.append(f'<rect x="100" y="{y+10}" width="372" height="30" rx="6" fill="{NAVY}"/>')
        s.append(T(114, y + 30, cmd, 9, "#B8C7E0", 400, font=MONO))
    lines = [
        [("listener", KW), (" web ", DTX), ("{", PN)],
        [("    address", DTX), (" :8080", VAL)],
        [("    protocols", DTX), (" http/1.1 h2c", VAL)],
        [("}", PN)],
        [],
        [("route", KW), (" hello ", DTX), ("{", PN)],
        [("    match ", DTX), ("{", PN)],
        [("        path", DTX), (" /hello", VAL)],
        [("    }", PN)],
        [("    handler", KW), (" respond ", DTX), ("{", PN)],
        [("        body", DTX), (' "hello from fit\\n"', STR)],
        [("    }", PN)],
        [("}", PN)],
    ]
    s.append(code_card(500, 176, 404, 264, "examples/hello/fit.conf", lines, 15.5, 9.5, 42))
    s.append(T(56, 470, "Also prebuilt binaries for Linux / macOS / Windows / FreeBSD, the ghcr.io/lesismal/fit image,", 10, MUTED))
    s.append(T(56, 488, "a Helm chart and systemd units; SIGHUP applies a config change.", 10, MUTED))
    s.append(footer(15))
    return page("\n".join(s))

# ---------------------------------------------------------------- P16 closing
def p16():
    s = [f'<rect width="960" height="540" fill="{NAVY}"/>']
    cx, cy = 820, 400
    for r in (240, 190, 140):
        s.append(f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="none" stroke="{NAVY_LINE}" stroke-width="1" opacity="0.55"/>')
    s.append(T(72, 250, "One binary,", 34, "#FFFFFF", 600))
    s.append(T(72, 300, "doing what each of them does best.", 34, "#FFFFFF", 600))
    s.append(f'<rect x="74" y="318" width="60" height="4" rx="2" fill="{BLUE}"/>')
    s.append(T(72, 358, "Reverse proxy, load balancer, TLS terminator, static files and API gateway on one data plane.", 12.5, NAVY_MUT))
    s.append(T(72, 412, "REPOSITORY", 9.5, "#5D6F92", 600))
    s.append(T(72, 436, "github.com/lesismal/fit", 12.5, "#C9D5EA"))
    s.append(T(72, 460, "Apache-2.0 · 2026.11", 11, "#5D6F92"))
    s.append(T(420, 412, "DOCS", 9.5, "#5D6F92", 600))
    s.append(T(420, 436, "docs/getting-started.md", 12.5, "#C9D5EA"))
    s.append(T(420, 460, "examples/ · bench/", 11, "#5D6F92"))
    s.append(T(72, 504, "fit 1.0 · built on fib", 10, "#3F4F6E"))
    return page("\n".join(s), NAVY)

def render(idx):
    fn = globals()[f"p{idx:02d}"]
    path = os.path.join(OUT, f"en-page-{idx:02d}.svg")
    with open(path, "w") as f:
        f.write(fn())
    return path

if __name__ == "__main__":
    pages = sys.argv[1:] or [str(i) for i in range(1, 17)]
    for p in pages:
        print(render(int(p)))
