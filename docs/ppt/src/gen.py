#!/usr/bin/env python3
# Generate the fit introduction deck page SVGs (960x540 design canvas).
import math, os, sys

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)))

INK    = "#0B1220"
BODY   = "#3B4A61"
MUTED  = "#6B7A90"
FAINT  = "#A6B3C6"
LINE   = "#E3E9F2"
BG     = "#F7F9FC"
WHITE  = "#FFFFFF"
BLUE   = "#2F6BFF"
BLUE_BG= "#EAF0FF"
BLUE_D = "#1D4ED8"
CYAN   = "#0891B2"
CYAN_BG= "#E4F6FA"
GREEN  = "#0FA97A"
GREEN_BG="#E6F7F1"
AMBER  = "#C2740A"
AMBER_BG="#FDF3E3"
ROSE   = "#D93654"
ROSE_BG= "#FCE9ED"
NAVY   = "#0A1020"
NAVY2  = "#141D33"
NAVY_LINE = "#243554"
NAVY_TXT  = "#E9EFFA"
NAVY_MUT  = "#8C9DBB"
NAVY_BLUE = "#4D8DFF"
NAVY_CYAN = "#22D3EE"

F = "PingFang SC"
M = "Menlo"

def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")

def cw(ch):
    o = ord(ch)
    if o < 128:
        if ch == " ": return 0.30
        if ch.isupper(): return 0.63
        if ch.islower(): return 0.52
        if ch.isdigit(): return 0.56
        if ch in ".,:;'|!()[]": return 0.30
        if ch in "-–—": return 0.40
        if ch in "/\\%": return 0.55
        return 0.56
    if 0x4E00 <= o <= 0x9FFF: return 1.0
    if 0x3000 <= o <= 0x303F: return 1.0   # CJK punctuation
    if 0xFF00 <= o <= 0xFF60: return 1.0   # fullwidth
    if ch in "·—…": return 0.55
    return 0.62

def text_w(s, size):
    return sum(cw(c) for c in s) * size

NO_HEAD = "，。、；：）」』】%！？·"

def wrap(s, width, size):
    lines, cur, curw = [], "", 0.0
    toks = []
    buf = ""
    for ch in s:
        if ch == " " or cw(ch) >= 0.95:
            if buf:
                toks.append(buf); buf = ""
            if ch == " ":
                toks.append(" ")
            else:
                toks.append(ch)
        else:
            buf += ch
    if buf: toks.append(buf)
    for t in toks:
        w = text_w(t, size)
        if cur and curw + w > width:
            if t in NO_HEAD or t == " ":
                # punctuation should not start a line: keep it on the previous one
                cur += t; curw += w
                continue
            lines.append(cur.rstrip())
            cur, curw = t, w
        else:
            cur += t; curw += w
    if cur.strip():
        lines.append(cur.rstrip())
    return lines

def T(x, y, s, size=12.5, color=BODY, weight=400, anchor="start", font=F, extra=""):
    a = f' text-anchor="{anchor}"' if anchor != "start" else ""
    return (f'<text x="{x:g}" y="{y:g}" font-family="{font}" font-size="{size}" '
            f'font-weight="{weight}" fill="{color}"{a}{extra}>{esc(s)}</text>')

def para(x, y, s, width, size=12.5, color=BODY, weight=400, lh=None, anchor="start"):
    lh = lh or size * 1.55
    out = []
    for i, ln in enumerate(wrap(s, width, size)):
        out.append(T(x, y + i * lh, ln, size, color, weight, anchor))
    return "\n".join(out), y + len(wrap(s, width, size)) * lh

def card(x, y, w, h, fill=WHITE, stroke=LINE, r=10, extra=""):
    return f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{fill}" stroke="{stroke}" stroke-width="1"{extra}/>'

def bullet(x, y, s, width, size=12, color=BODY, dot=BLUE, lh=None):
    lh = lh or size * 1.6
    lines = wrap(s, width, size)
    out = [f'<circle cx="{x+3}" cy="{y-size*0.32}" r="2.6" fill="{dot}"/>']
    for i, ln in enumerate(lines):
        out.append(T(x + 14, y + i * lh, ln, size, color))
    return "\n".join(out), y + len(lines) * lh

def header(no, eyebrow, title, subtitle=None):
    out = [f'<rect x="56" y="42" width="18" height="3" rx="1.5" fill="{BLUE}"/>',
           T(82, 52, eyebrow, 10.5, MUTED, 500),
           T(56, 96, title, 32, INK, 600)]
    if subtitle:
        out.append(T(56, 126, subtitle, 14, BODY))
        out.append(f'<line x1="56" y1="150" x2="904" y2="150" stroke="{LINE}" stroke-width="1"/>')
    else:
        out.append(f'<line x1="56" y1="126" x2="904" y2="126" stroke="{LINE}" stroke-width="1"/>')
    return "\n".join(out)

def footer(no, total=16):
    return "\n".join([
        T(56, 516, "fit · 基于 fib 的反向代理、负载均衡与 API 网关", 9.5, FAINT),
        T(904, 516, f"{no:02d} / {total}", 9.5, FAINT, 500, "end"),
    ])

def page(body, bg=BG):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="960" height="540" viewBox="0 0 960 540">\n'
            f'<rect width="960" height="540" fill="{bg}"/>\n{body}\n</svg>\n')

def chip(x, y, label, fg=BLUE, bg=BLUE_BG, size=10.5, ph=22, padx=9, weight=500):
    w = text_w(label, size) + padx * 2
    return (f'<rect x="{x:g}" y="{y:g}" width="{w:.1f}" height="{ph}" rx="{ph/2}" fill="{bg}"/>'
            + T(x + padx, y + ph * 0.70, label, size, fg, weight)), w

# ---------------------------------------------------------------- P1 cover
def p01():
    s = []
    s.append(f'<rect width="960" height="540" fill="{NAVY}"/>')
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
    s.append(T(72, 250, "基于 fib 的反向代理、负载均衡与 API 网关", 17, "#C9D5EA"))
    s.append(T(72, 284, "小响应吞吐对齐 nginx，默认安全学 Caddy，概念模型学 Traefik；", 12, NAVY_MUT))
    s.append(T(72, 304, "再补上它们都没有的 WebRTC、内建控制台、tunnel 与 LLM 共享。", 12, NAVY_MUT))

    s.append(f'<line x1="72" y1="324" x2="904" y2="324" stroke="#22304C" stroke-width="1"/>')
    feats = [
        ("反向代理与负载均衡", "9 种策略 · 主动与被动健康检查", NAVY_BLUE),
        ("TLS 终结与自动 HTTPS", "ACME 签发 · OCSP · mTLS · SNI", NAVY_CYAN),
        ("静态文件与响应缓存", "sendfile · 内存 + 磁盘两级缓存", GREEN),
        ("API 网关治理", "认证 · 限流 · 熔断 · 重写", NAVY_BLUE),
        ("配置热更新，不重启", "Admin API 原子换图，不中断在途请求", NAVY_CYAN),
        ("Web 管理控制台", "登录改全部配置 · 加密 store", GREEN),
        ("WebRTC 与流媒体", "TURN · WHIP/WHEP · 媒体服务器代理", NAVY_BLUE),
        ("tunnel 与 LLM 共享", "访问 NAT 后的服务 · 多机共享模型", NAVY_CYAN),
        ("多协议单端口", "HTTP · TLS · gRPC · SOCKS 按首字节分流", GREEN),
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
    s = [header(2, "01 / 背景", "三者的长处，恰好是另一方的短处",
                "nginx、Caddy、Traefik 各自把一件事做到极致，也各自留下了明显的缺口。")]
    cols = [
        ("nginx", "NGINX · 2004 · C", CYAN,
         ["事件循环与回调模型", "静态文件与缓存最强", "空闲连接占用极低"],
         ["配置是自研 DSL，难校验", "改配置要 reload 进程", "自动 HTTPS 等能力要 Plus"]),
        ("Caddy", "CADDY · 2015 · GO", BLUE,
         ["默认 HTTPS，开箱即用", "严格可编程的 JSON 配置", "原子替换的 Admin API"],
         ["每连接一个 goroutine", "高并发下内存与 GC 开销大", "缓存、限流、四层靠插件"]),
        ("Traefik", "TRAEFIK · 2016 · GO", "#7C5CFC",
         ["provider 驱动，容器与 K8s 原生", "内置面板、指标与追踪", "清晰的四层概念模型"],
         ["通常是三者中最慢、最重", "不是 Web 服务器：无静态文件", "没有内置响应缓存"]),
    ]
    xs = [56, 347, 638]
    for x, (name, tag, col, pro, con) in zip(xs, cols):
        s.append(card(x, 172, 266, 268))
        s.append(f'<circle cx="{x+27}" cy="{172+38}" r="5" fill="{col}"/>')
        s.append(T(x + 42, 172 + 46, name, 20, INK, 600))
        s.append(T(x + 42, 172 + 64, tag, 9, MUTED, 500))
        s.append(f'<line x1="{x+24}" y1="{172+80}" x2="{x+242}" y2="{172+80}" stroke="{LINE}" stroke-width="1"/>')
        s.append(T(x + 24, 172 + 102, "强项", 9.5, GREEN, 600))
        for i, t in enumerate(pro):
            s.append(f'<circle cx="{x+27}" cy="{172+120+i*20-4}" r="2.4" fill="{GREEN}"/>')
            s.append(T(x + 38, 172 + 120 + i * 20, t, 11.5, "#22304A"))
        s.append(T(x + 24, 172 + 194, "缺口", 9.5, ROSE, 600))
        for i, t in enumerate(con):
            s.append(f'<circle cx="{x+27}" cy="{172+212+i*20-4}" r="2.4" fill="{ROSE}"/>')
            s.append(T(x + 38, 172 + 212 + i * 20, t, 11.5, BODY))
    s.append(f'<rect x="56" y="456" width="848" height="42" rx="8" fill="{INK}"/>')
    s.append(T(76, 483, "fit 的选择：", 12.5, "#6FA8FF", 600))
    s.append(T(160, 483, "把三者最擅长的部分放进同一套数据面，再用 fib 补上它们共同的短板。", 12.5, "#C9D5EA"))
    s.append(footer(2))
    return page("\n".join(s))

# ---------------------------------------------------------------- P3 overview
def p03():
    s = [header(3, "02 / 概览", "一套数据面，六件主要工作",
                "listener → router → middleware → service → upstream，HTTP 与四层共用同一套概念。")]
    cx, cy = 480, 320
    nodes = [
        (225, 320, "反向代理", "HTTP · WebSocket · gRPC · TCP · UDP"),
        (735, 320, "负载均衡", "9 种策略 · 主动 + 被动健康检查"),
        (352, 202, "TLS 终结", "ACME 签发 · OCSP · mTLS · SNI"),
        (608, 202, "静态文件与缓存", "sendfile · 两级缓存 · Cache Lock"),
        (352, 438, "API 网关治理", "认证 · 限流 · 熔断 · 重写"),
        (608, 438, "动态配置", "Admin API · provider · 控制台"),
    ]
    for x, y, _, _ in nodes:
        s.append(f'<line x1="{cx}" y1="{cy}" x2="{x}" y2="{y}" stroke="#CBD6E8" stroke-width="1.2"/>')
    s.append(f'<ellipse cx="{cx}" cy="{cy}" rx="96" ry="50" fill="{BLUE}"/>')
    s.append(T(cx, cy - 2, "fit", 28, WHITE, 700, "middle"))
    s.append(T(cx, cy + 20, "网关与边缘代理", 9.5, "#DCE6FF", 400, "middle"))
    for x, y, name, sub in nodes:
        w, h = 200, 62
        s.append(card(x - w / 2, y - h / 2, w, h, WHITE, "#DCE4F0", 9))
        s.append(T(x, y - 3, name, 14, INK, 600, "middle"))
        s.append(T(x, y + 16, sub, 9.5, MUTED, 400, "middle"))
    s.append(T(56, 494, "同一引擎上还跑着 WebRTC/TURN、tunnel 与 LLM 共享 —— 见第 12 页。", 11, MUTED))
    s.append(footer(3))
    return page("\n".join(s))

# ---------------------------------------------------------------- P4 architecture
def p04():
    s = [header(4, "03 / 架构", "控制面与数据面分离",
                "配置只编译成不可变的运行时图；数据面通过一个原子指针读它，请求路径上没有阻塞调用。")]
    s.append(T(56, 178, "控制面 · CONTROL PLANE", 9.5, MUTED, 600))
    a = [
        ("配置来源", "文件 · Admin API · provider"),
        ("适配与严格校验", "错误带路径与位置"),
        ("provision 模块", "证书、健康检查、连接池"),
        ("不可变运行时图", "v(n+1) 整体构建"),
    ]
    axs = [56, 273, 490, 707]
    for x, (t, sub) in zip(axs, a):
        s.append(card(x, 190, 196, 62, WHITE, "#DCE4F0", 9))
        s.append(T(x + 16, 216, t, 13.5, INK, 600))
        s.append(T(x + 16, 236, sub, 9.5, MUTED))
    for x in axs[:-1]:
        s.append(f'<line x1="{x+198}" y1="221" x2="{x+212}" y2="221" stroke="{BLUE}" stroke-width="1.6"/>')
        s.append(f'<path d="M{x+209} 216.5 l5 4.5 l-5 4.5" stroke="{BLUE}" stroke-width="1.6" fill="none"/>')
    # control plane -> data plane
    s.append(f'<line x1="480" y1="252" x2="480" y2="316" stroke="{BLUE}" stroke-width="1.6"/>')
    s.append(f'<path d="M480 324 l-5 -9 l10 0 z" fill="{BLUE}"/>')
    s.append(T(496, 278, "原子指针替换", 11, BLUE, 600))
    s.append(T(496, 295, "监听器差量增删（AddBinding / RemoveBinding）", 9.5, MUTED))

    s.append(T(56, 344, "数据面 · DATA PLANE（fib 事件循环）", 9.5, MUTED, 600))
    d = [
        ("listeners", "TCP · UDP · TLS · QUIC"),
        ("mux", "按首字节分流协议"),
        ("routers", "host · path · header · SNI"),
        ("middlewares", "认证 · 限流 · 缓存"),
        ("services", "负载均衡 · 镜像 · 故障转移"),
        ("上游连接池", "fib 异步客户端"),
    ]
    dxs = [56, 200, 344, 488, 632, 776]
    for x, (t, sub) in zip(dxs, d):
        s.append(card(x, 356, 128, 64, "#F1F5FF", "#D6E2FF", 9))
        s.append(T(x + 12, 382, t, 12, INK, 600))
        s.append(T(x + 12, 402, sub, 9, MUTED))
    for x in dxs[:-1]:
        s.append(f'<path d="M{x+133} 384 l4.5 4 l-4.5 4" stroke="#93AEE8" stroke-width="1.4" fill="none"/>')
    notes = ["配置变更不影响在途请求", "长连接留在旧图，排空后释放", "请求路径上没有阻塞调用"]
    for i, n in enumerate(notes):
        x = 56 + i * 284
        s.append(f'<circle cx="{x+6}" cy="446" r="6" fill="{GREEN_BG}"/>')
        s.append(f'<path d="M{x+2.5} 446 l2.6 2.8 l4.2 -5" stroke="{GREEN}" stroke-width="1.5" fill="none"/>')
        s.append(T(x + 20, 450, n, 11.5, BODY, 500))
    s.append(footer(4))
    return page("\n".join(s))

# ---------------------------------------------------------------- P5 engine
def p05():
    s = [header(5, "04 / 性能来源", "请求路径上，没有一个 goroutine",
                "fit 不把每个连接交给一个 goroutine —— 数据面直接跑在 fib 的事件循环上。")]
    items = [
        ("空闲连接几乎不占内存", "只保留一个连接结构；读写缓冲借用一轮即归还，没有常驻 bufio 栈。"),
        ("背压是端到端单向的", "HoldReads 把慢客户端的等待传回上游；写 watermark 与全局待发字节预算兜底。"),
        ("静态文件接近零拷贝", "sendfile、内存文件缓存与预压缩孪生文件，命中即写。"),
        ("可阻塞的工作有界外置", "外部认证、插件与磁盘缓存未命中放在各自的池里，池间单向等待。"),
        ("需要时可 prefork", "按 CPU 隔离堆与 GC，SO_REUSEPORT 共享端口，行为与 nginx 的 worker 一致。"),
    ]
    pos = [(56, 190), (56, 286), (56, 382), (500, 190), (500, 286)]
    for i, ((x, y), (t, d)) in enumerate(zip(pos, items)):
        s.append(T(x, y - 6, f"{i+1:02d}", 10, BLUE, 600))
        s.append(T(x + 26, y, t, 15, INK, 600))
        p, _ = para(x + 26, y + 22, d, 380 if x < 400 else 380, 11.5, BODY, 400, 18)
        s.append(p)
    s.append(card(500, 356, 404, 112, "#F1F5FF", "#D6E2FF", 10))
    s.append(T(520, 380, "对比", 9.5, BLUE, 600))
    p, _ = para(520, 402, "40,000 个空闲 TLS 连接：fit 475 MiB，nginx 1,305 MiB。Caddy 与 Traefik 的差距来自 net/http —— 每连接一个 goroutine。", 364, 11.5, "#2A3D5C", 400, 18)
    s.append(p)
    s.append(footer(5))
    return page("\n".join(s))

# ---------------------------------------------------------------- P6 protocols
def p06():
    s = [header(6, "05 / 协议", "一套模型，覆盖七层与四层",
                "入口协议与上游协议各自可选；四层服务与 HTTP 路由共用 listener → service 的模型。")]
    rows = [
        ("HTTP/1.1", "双向流式代理", "逐跳头清理 · X-Forwarded-* / Forwarded · 可信代理列表"),
        ("HTTP/2", "h2 / h2c 入口与上游", "流控与背压联动 · gRPC 双向流与 trailers"),
        ("HTTP/3", "QUIC · TLS 1.3", "不依赖 quic-go，与 fib 同引擎"),
        ("WebSocket", "h1 / h2 / h3 入口", "代理到 HTTP/1.1 上游，升级与双向隧道"),
        ("TCP / UDP", "四层服务", "按 SNI 透传不终止 · PROXY protocol"),
        ("SOCKS / CONNECT", "正向代理", "SOCKS5 与 HTTP CONNECT，带客户端与目标访问规则"),
        ("多协议单端口", "按首字节分流", "HTTP、TLS、SOCKS、裸 TCP 共存于一个 listener"),
    ]
    top, hh, rh = 176, 36, 40
    s.append(f'<rect x="56" y="{top}" width="848" height="{hh}" rx="8" fill="{BLUE_BG}"/>')
    s.append(T(76, top + 23, "协议", 10.5, BLUE, 600))
    s.append(T(246, top + 23, "能力", 10.5, BLUE, 600))
    s.append(T(496, top + 23, "关键点", 10.5, BLUE, 600))
    for i, (a, b, c) in enumerate(rows):
        y = top + hh + i * rh
        if i % 2 == 1:
            s.append(f'<rect x="56" y="{y}" width="848" height="{rh}" fill="#FFFFFF"/>')
        s.append(f'<line x1="56" y1="{y+rh}" x2="904" y2="{y+rh}" stroke="#EDF1F7" stroke-width="1"/>')
        s.append(T(76, y + 26, a, 13, INK, 600))
        s.append(T(246, y + 26, b, 11.5, BODY, 500))
        s.append(T(496, y + 26, c, 10.5, MUTED))
    s.append(footer(6))
    return page("\n".join(s))

# ---------------------------------------------------------------- P7 governance
def p07():
    s = [header(7, "06 / 治理", "把流量交给策略，而不是脚本",
                "负载均衡、健康检查、韧性与准入控制都是配置里的字段，不是外挂的 Lua 或插件。")]
    quads = [
        ("负载均衡", "9 种策略", BLUE, BLUE_BG,
         "轮询 · 加权轮询 · 最少连接 · 随机 · P2C · IP / URI / Header / Cookie 一致性哈希 · 粘性 cookie"),
        ("健康检查", "主动 + 被动", GREEN, GREEN_BG,
         "主动 HTTP / TCP 探测；被动 max_fails 与 fail_timeout；连续失败即出轮，恢复后自动回归。"),
        ("韧性", "重试 · 熔断 · 超时", AMBER, AMBER_BG,
         "预算内重试、熔断器、连接 / 响应 / 空闲超时；按权重镜像与故障转移到备用上游。"),
        ("准入控制", "限流 · 并发 · 体积", ROSE, ROSE_BG,
         "按客户端 IP、Header 或路由限流；并发上限、请求体上限、连接上限与写停顿超时。"),
    ]
    for i, (t, badge, col, cbg, d) in enumerate(quads):
        x = 56 + (i % 2) * 424
        y = 172 + (i // 2) * 152
        s.append(card(x, y, 424, 136))
        s.append(f'<rect x="{x}" y="{y+18}" width="4" height="100" rx="2" fill="{col}"/>')
        s.append(T(x + 28, y + 40, t, 16, INK, 600))
        c, w = chip(x + 28 + text_w(t, 16) + 12, y + 23, badge, col, cbg, 10, 20)
        s.append(c)
        p, _ = para(x + 28, y + 70, d, 368, 11.5, BODY, 400, 18)
        s.append(p)
    s.append(footer(7))
    return page("\n".join(s))

# ---------------------------------------------------------------- P8 benchmark
def p08():
    s = [header(8, "07 / 基准", "在 nginx 的主场上与它打平",
                "固定响应、HTTP/1.1、500 在途：fit 671,426 rps / p99 0.87 ms，nginx 668,401 rps / p99 2.09 ms。")]
    s.append(T(56, 182, "吞吐 · 请求/秒", 10, MUTED, 600))
    bars = [("fit", 671426, BLUE), ("nginx", 668401, "#AFC0DA"), ("Envoy", 260701, "#C9D5E7"), ("Caddy", 256938, "#C9D5E7")]
    scale = 296 / 680000
    for i, (n, v, col) in enumerate(bars):
        y = 198 + i * 44
        s.append(T(120, y + 17, n, 12, INK if n == "fit" else MUTED, 600 if n == "fit" else 400, "end"))
        w = v * scale
        s.append(f'<rect x="132" y="{y}" width="{w:.1f}" height="26" rx="4" fill="{col}"/>')
        s.append(T(132 + w + 8, y + 18, f"{v:,}", 11.5, INK if n == "fit" else MUTED, 600))
    s.append(T(132, 392, "p99：fit 0.87 ms · nginx 2.09 ms · Envoy 2.32 ms · Caddy 9.81 ms", 10, MUTED))

    s.append(card(500, 172, 404, 232))
    s.append(T(520, 198, "更多读数", 10, MUTED, 600))
    more = [
        ("h2c 400×25", "3,450,552", "nginx 488,523"),
        ("site HTTPS/1.1", "98,552", "nginx 81,462（+21%）"),
        ("proxy-download 1 MiB", "14,411", "nginx 5,105"),
        ("40,000 空闲 TLS 内存", "475 MiB", "nginx 1,305 MiB"),
    ]
    for i, (a, b, c) in enumerate(more):
        y = 226 + i * 46
        s.append(T(520, y, a, 10.5, MUTED))
        s.append(T(884, y, b, 12.5, INK, 600, "end"))
        s.append(T(884, y + 16, c, 9.5, FAINT, 400, "end"))
        if i < 3:
            s.append(f'<line x1="520" y1="{y+26}" x2="884" y2="{y+26}" stroke="#EDF1F7" stroke-width="1"/>')

    s.append(T(56, 428, "环境：AMD Ryzen 7 7800X3D（16 CPU）· Ubuntu 24.04 · nginx 1.31.4 / Caddy 2.10.2 / Traefik 3.5.0；", 9, FAINT))
    s.append(T(56, 444, "单机回环，服务器 6 CPU，压测机 Linux host 网络；数据：bench/results/plan-2026-10-30。", 9, FAINT))
    s.append(T(56, 466, "诚实读数：代理到后端的 API 场景仍在 nginx 的 66–78%，40k 空闲连接吞吐也低于 nginx。", 9, AMBER, 500))
    s.append(footer(8))
    return page("\n".join(s))

def render(idx):
    fn = globals()[f"p{idx:02d}"]
    svg = fn()
    path = os.path.join(OUT, f"page-{idx:02d}.svg")
    with open(path, "w") as f:
        f.write(svg)
    return path


# ---------------------------------------------------------------- helpers (later pages)
KW  = "#6FA8FF"   # keyword on dark
VAL = "#7EE3B0"   # value on dark
PN  = "#5D6F92"   # punctuation on dark
STR = "#7EE3B0"
DTX = "#C7D3E8"   # plain code text

def code_card(x, y, w, h, title, lines, lh=16.5, size=10.5, start=44):
    out = [card(x, y, w, h, NAVY, "#1B2740", 10)]
    out.append(T(x + 20, y + 26, title, 9.5, "#7E92B4", 500, font=M))
    for i in (0, 1, 2):
        out.append(f'<circle cx="{x+w-20-i*12}" cy="{y+22}" r="2.6" fill="#2A3A5C"/>')
    out.append(f'<line x1="{x}" y1="{y+38}" x2="{x+w}" y2="{y+38}" stroke="#1B2740" stroke-width="1"/>')
    for i, segs in enumerate(lines):
        cx = x + 20
        by = y + start + i * lh
        for txt, col in segs:
            if not txt:
                continue
            out.append(T(cx, by, txt, size, col, 400, font=M, extra=' xml:space="preserve"'))
            cx += sum(size * (0.6 if ord(ch) < 128 else 1.0) for ch in txt)
    return "\n".join(out)

# ---------------------------------------------------------------- P9 config
def p09():
    s = [header(9, "08 / 配置", "一份严格配置，三种写法",
                "格式不同，字段同一套；每个字段都有类型、默认值与说明，解码错误直接指出位置。")]
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
        ("严格解码", "未知字段、类型错误都带路径与位置报错；fit validate 不启动服务就能查。"),
        ("同一份 schema", "fit schema 输出 JSON Schema，编辑器提示与运行时校验共用它。"),
        ("可拆分", "fit run -c base.conf -c conf.d：多文件与目录按顺序合并。"),
        ("可迁移", "fit adapt 在 .conf / YAML / JSON 之间互转，也能读 nginx 与 Caddyfile，并列出未转换的部分。"),
    ]
    for i, (t, d) in enumerate(pts):
        y = 236 + i * 58
        s.append(T(510, y, t, 13, INK, 600))
        p, _ = para(510, y + 20, d, 384, 11, BODY, 400, 16)
        s.append(p)
    s.append(f'<line x1="56" y1="474" x2="904" y2="474" stroke="{LINE}" stroke-width="1"/>')
    s.append(T(56, 496, "SIGHUP 或 -watch 重载：地址不变的 listener 原地保留；新配置有错时继续用正在运行的配置。", 10, MUTED))
    s.append(footer(9))
    return page("\n".join(s))

# ---------------------------------------------------------------- P10 ops
def p10():
    s = [header(10, "09 / 运维", "改配置不用重启，也不用担心改坏",
                "每一次变更都走同一条路径：先构建完整的新图，成功了才替换。")]
    steps = [
        ("校验", "fit validate 或 schema 不通过，就根本不换。"),
        ("provision 新图", "证书、健康检查、连接池全部就绪。"),
        ("原子替换", "指针一换，新请求即走新图。"),
        ("旧图排空", "在途请求跑完，引用归零后释放。"),
    ]
    xs = [56, 273, 490, 707]
    for i, ((x, (t, d))) in enumerate(zip(xs, steps)):
        s.append(f'<circle cx="{x+19}" cy="198" r="17" fill="{BLUE_BG}"/>')
        s.append(T(x + 19, 203, str(i + 1), 12, BLUE, 600, "middle"))
        if i < 3:
            s.append(f'<line x1="{x+42}" y1="198" x2="{x+190}" y2="198" stroke="#CBD6E8" stroke-width="1" stroke-dasharray="3 4"/>')
        s.append(T(x + 46, 194, t, 13.5, INK, 600))
        p, _ = para(x + 46, 214, d, 142, 10.5, BODY, 400, 15.5)
        s.append(p)
    s.append(card(56, 282, 848, 54, "#F1F5FF", "#D6E2FF", 8))
    s.append(T(76, 315, "2,000 次热切换 / 6 秒 / 8 个并发客户端，约 337,000 个请求与 32 条长下载，零失败。", 11.5, "#2A3D5C", 500))

    cards = [
        ("Admin API", "GET / PUT / PATCH / DELETE /config；ETag 防止丢更新；历史版本与回滚；/config/effective 看生效配置及来源。"),
        ("加密 store", "配置加密保存在 admin.store（Argon2id + XChaCha20-Poly1305）；fit store export / import 迁移。"),
        ("Web 控制台", "登录后修改全部配置，所有页面合成一个 workbench；改完写回 store，与文件配置同一套校验。"),
    ]
    for i, (t, d) in enumerate(cards):
        x = 56 + i * 291
        s.append(card(x, 356, 266, 120))
        s.append(T(x + 22, 384, t, 13, INK, 600))
        p, _ = para(x + 22, 404, d, 222, 10.5, BODY, 400, 15.5)
        s.append(p)
    s.append(footer(10))
    return page("\n".join(s))

# ---------------------------------------------------------------- P11 observability
def p11():
    s = [header(11, "10 / 可观测", "出问题时，先看清发生了什么",
                "日志、指标、链路都内置，不需要另外拼一套边车。")]
    cols = [
        ("ACCESS LOG", "日志", BLUE, [
            "JSON、logfmt、line 三种格式",
            "有界缓冲写入，不占事件循环",
            "按 logger 分级，可只记 4xx / 5xx",
        ]),
        ("METRICS", "指标", GREEN, [
            "Prometheus /metrics，按 listener 与路由",
            "/status 列出连接、上游健康与证书",
            "缓存命中、限流与熔断计数",
        ]),
        ("TRACING", "链路", AMBER, [
            "OpenTelemetry，W3C trace context",
            "trace 与采样决策透传给上游",
            "OTLP/HTTP 导出 span 与指标",
        ]),
    ]
    for i, (en, t, col, items) in enumerate(cols):
        x = 56 + i * 291
        s.append(card(x, 172, 266, 190))
        s.append(f'<rect x="{x}" y="{172+22}" width="4" height="32" rx="2" fill="{col}"/>')
        s.append(T(x + 24, 172 + 36, en, 8.5, col, 600))
        s.append(T(x + 24, 172 + 60, t, 17, INK, 600))
        y = 172 + 96
        for it in items:
            s.append(f'<circle cx="{x+27}" cy="{y-4}" r="2.4" fill="{col}"/>')
            p, y2 = para(x + 38, y, it, 206, 10.5, BODY, 400, 15.5)
            s.append(p)
            y = y2 + 4

    s.append(card(56, 386, 848, 84, "#F1F5FF", "#D6E2FF", 10))
    s.append(T(76, 412, "仪表盘与 /status", 11.5, INK, 600))
    facts = [("listener 与连接数", 76), ("上游与后端健康", 300), ("证书与到期时间", 524), ("配置版本与来源", 730)]
    for t, x in facts:
        s.append(T(x, 442, t, 10.5, "#2A3D5C"))
    s.append(T(56, 496, "一条请求的完整链路：接入 → 路由 → 中间件 → 上游，日志里都有 trace id。", 10, MUTED))
    s.append(footer(11))
    return page("\n".join(s))

# ---------------------------------------------------------------- P12 edge roles
def p12():
    s = [header(12, "11 / 同一引擎上的其它角色", "网关之外，还是媒体服务器、隧道与模型网关",
                "不用再部署第二套服务：这些角色与网关共用 listener、配置与热重载。")]
    bands = [
        ("WebRTC", "WEBRTC · MEDIA", CYAN,
         "TURN 服务器（UDP / TCP / TLS）+ 以 fit 为媒体服务器的 WHIP / WHEP + 单 UDP 端口代理其它媒体服务器。",
         ["TURN", "WHIP / WHEP", "webrtc_proxy"]),
        ("tunnel", "TUNNEL · REACH BEHIND NAT", BLUE,
         "hub / node / proxy 三角色，走 TCP、WebSocket 或 QUIC 链路，把 NAT 后的服务安全暴露到边缘。",
         ["TCP", "WebSocket", "QUIC"]),
        ("LLM", "LLM · SHARE MODELS", GREEN,
         "hub / node / proxy 在机器间共享模型，向 agent 提供 OpenAI、Anthropic、Gemini、Ollama、Cohere 兼容接口。",
         ["OpenAI 兼容", "Anthropic", "Ollama"]),
    ]
    for i, (name, tag, col, d, chips_) in enumerate(bands):
        y = 176 + i * 92
        s.append(card(56, y, 848, 84))
        s.append(f'<rect x="56" y="{y+14}" width="4" height="56" rx="2" fill="{col}"/>')
        s.append(T(84, y + 38, name, 19, INK, 600))
        s.append(T(84, y + 58, tag, 7.5, MUTED, 600))
        p, _ = para(280, y + 32, d, 600, 11, BODY, 400, 16.5)
        s.append(p)
        cx = 280
        for c in chips_:
            ch, w = chip(cx, y + 56, c, col, "#F1F5F9", 9, 19, 9)
            s.append(ch)
            cx += w + 7
    s.append(T(56, 486, "一条配置、一个二进制、一套引擎；控制台里按角色分别开关。", 11, MUTED))
    s.append(footer(12))
    return page("\n".join(s))

# ---------------------------------------------------------------- P13 extensions
def p13():
    s = [header(13, "12 / 扩展", "用 Go 扩展，用 WASM 热插拔",
                "模块就是 Go 包：编进二进制后跑在 fib 的 worker 上，和内置模块没有区别。")]
    steps = [
        "写一个 Go 包，实现 Handler 或 Middleware",
        'init 里 module.Register("http.middleware.hello", …)',
        "fit build -with github.com/you/fit-hello 编入二进制",
    ]
    for i, t in enumerate(steps):
        y = 190 + i * 44
        s.append(f'<circle cx="{69}" cy="{y-4}" r="12" fill="{BLUE_BG}"/>')
        s.append(T(69, y, str(i + 1), 10.5, BLUE, 600, "middle"))
        s.append(T(92, y, t, 12, INK, 500))
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
        ("22 个内置模块", BLUE, "8 个 handler + 14 个 middleware；参数严格解码，字段说明直接进 JSON Schema。"),
        ("WASM 热插拔", CYAN, "wazero 沙箱内运行，给插件自己的有界池；兼容 Proxy-Wasm 过滤器。"),
        ("零中间层", GREEN, "模块直接跑在 fib worker 上；fit build -without 还能去掉用不到的 provider。"),
    ]
    for i, (t, col, d) in enumerate(cards):
        y = 172 + i * 100
        s.append(card(500, y, 404, 88))
        s.append(f'<circle cx="524" cy="{y+26}" r="4" fill="{col}"/>')
        s.append(T(538, y + 30, t, 13, INK, 600))
        p, _ = para(524, y + 54, d, 356, 10.5, BODY, 400, 15.5)
        s.append(p)
    s.append(footer(13))
    return page("\n".join(s))

# ---------------------------------------------------------------- P14 status / numbers
def p14():
    s = [header(14, "13 / 现状", "M0–M5 与 1.0 的迭代全部完成",
                "每个里程碑都满足自己的退出标准；外部安全审计是 1.0 之前唯一的外部事项。")]
    stats = [
        ("98.0%", "覆盖率门槛", "race + fuzz 进 CI 门禁"),
        ("1,564", "测试函数", "另有 11 个 fuzz 目标"),
        ("56", "可运行示例", "三种格式各一份"),
        ("22", "内置模块", "8 handler + 14 middleware"),
    ]
    for i, (num, lab, sub) in enumerate(stats):
        x = 56 + i * 215
        s.append(card(x, 172, 200, 152))
        s.append(T(x + 24, 172 + 62, num, 38, BLUE, 700))
        s.append(T(x + 24, 172 + 94, lab, 12, INK, 600))
        s.append(T(x + 24, 172 + 116, sub, 9.5, MUTED))
    chips_ = ["4 个 provider", "9 个角色", "16.4 万行 Go", "21.1 MB 单二进制", "Apache-2.0"]
    cx = 56
    for c in chips_:
        ch, w = chip(cx, 348, c, "#46536B", "#EDF1F7", 10, 24, 12)
        s.append(ch)
        cx += w + 9
    s.append(card(56, 392, 848, 72))
    rows = [("已完成", GREEN, "M0–M5 与 1.0 迭代全部完成"),
            ("待办", AMBER, "1.0 的外部安全审计（代码之外）"),
            ("进行中", BLUE, "P2P 与 VPN（fib 的 WebRTC/TURN 已就位）")]
    for i, (lab, col, val) in enumerate(rows):
        x = 56 + i * 283
        s.append(f'<circle cx="{x+26}" cy="{392+28}" r="4" fill="{col}"/>')
        s.append(T(x + 38, 392 + 32, lab, 10, MUTED, 600))
        s.append(T(x + 26, 392 + 52, val, 10.5, INK, 500))
    s.append(footer(14))
    return page("\n".join(s))

# ---------------------------------------------------------------- P15 getting started
def p15():
    s = [header(15, "14 / 上手", "三条命令，一个 HTTPS 站点",
                "不用先学一套 DSL：最小配置就是 listener 与 route 两段。")]
    steps = [
        ("安装", "go install github.com/lesismal/fit/cmd/fit@latest"),
        ("校验配置", "fit validate -c examples/hello/fit.conf"),
        ("运行", "fit run -c examples/hello/fit.conf"),
    ]
    for i, (t, cmd) in enumerate(steps):
        y = 200 + i * 82
        s.append(f'<circle cx="{72}" cy="{y-6}" r="15" fill="{BLUE_BG}"/>')
        s.append(T(72, y, str(i + 1), 11, BLUE, 600, "middle"))
        s.append(T(100, y - 2, t, 12.5, INK, 600))
        s.append(f'<rect x="100" y="{y+10}" width="372" height="30" rx="6" fill="{NAVY}"/>')
        s.append(T(114, y + 30, cmd, 9.5, "#B8C7E0", 400, font=M))
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
    s.append(T(56, 470, "另有 Linux / macOS / Windows / FreeBSD 预编译二进制、ghcr.io/lesismal/fit 镜像、", 10.5, MUTED))
    s.append(T(56, 488, "Helm chart 与 systemd unit；改完配置 SIGHUP 即生效。", 10.5, MUTED))
    s.append(footer(15))
    return page("\n".join(s))

# ---------------------------------------------------------------- P16 closing
def p16():
    s = [f'<rect width="960" height="540" fill="{NAVY}"/>']
    cx, cy = 820, 400
    for r in (240, 190, 140):
        s.append(f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="none" stroke="{NAVY_LINE}" stroke-width="1" opacity="0.55"/>')
    s.append(T(72, 250, "一个二进制，", 34, "#FFFFFF", 600))
    s.append(T(72, 300, "把三者最擅长的事做回来。", 34, "#FFFFFF", 600))
    s.append(f'<rect x="74" y="318" width="60" height="4" rx="2" fill="{BLUE}"/>')
    s.append(T(72, 358, "反向代理、负载均衡、TLS 终结、静态文件与 API 网关，一套数据面。", 13.5, NAVY_MUT))
    s.append(T(72, 412, "仓库", 9.5, "#5D6F92", 600))
    s.append(T(72, 436, "github.com/lesismal/fit", 12.5, "#C9D5EA"))
    s.append(T(72, 460, "Apache-2.0 · 2026.11", 11, "#5D6F92"))
    s.append(T(420, 412, "文档", 9.5, "#5D6F92", 600))
    s.append(T(420, 436, "docs/getting-started.md", 12.5, "#C9D5EA"))
    s.append(T(420, 460, "examples/ · bench/", 11, "#5D6F92"))
    s.append(T(72, 504, "fit 1.0 · built on fib", 10, "#3F4F6E"))
    return page("\n".join(s), NAVY)

if __name__ == "__main__":
    pages = sys.argv[1:] or [str(i) for i in range(1, 17)]
    for p in pages:
        print(render(int(p)))
