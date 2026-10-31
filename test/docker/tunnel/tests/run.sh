#!/usr/bin/env bash
# run.sh tests every feature of fit's tunnel roles in the docker test
# environment, from the tester container (see ../compose.yml), on the API of
# every instance's console and through their tunnels:
#
#   run.sh all            set everything up, then test every feature
#   run.sh setup          only set up hub-1, node lab and client alice
#   run.sh features       only the feature tests (after setup)
#   run.sh restart-check  after the instances restarted: their settings came
#                         back from the configs in their stores, and
#                         everything works again
#
# The consoles are behind fit's logins: each page is logged into once, its
# session cookie and CSRF token kept in /tmp. It prints PASS or FAIL per
# check and exits 1 if any failed. State it keeps across runs (the QUIC
# fingerprints) is in /state.
#
# run.sh 在 docker 测试环境的测试容器中（见 ../compose.yml），通过每个实例控制台
# 的 API 和它们的隧道测试 fit 隧道角色的全部功能：
#
#   run.sh all            先完成配置，再测试全部功能
#   run.sh setup          只配置 hub-1、节点 lab 和客户端 alice
#   run.sh features       只运行功能测试（配置之后）
#   run.sh restart-check  实例重启之后：设置从存储中的配置恢复，一切照常工作
#
# 控制台在 fit 的登录之后：每个页面登录一次，会话 cookie 和 CSRF token 保存在
# /tmp。每项检查打印 PASS 或 FAIL，有失败则退出码为 1。跨运行保留的状态（QUIC
# 指纹）存放在 /state。
set -uo pipefail

HUB_PASSWORD=${HUB_PASSWORD:?} BODY_PASSWORD=${BODY_PASSWORD:?} WS_PASSWORD=${WS_PASSWORD:?}
STATE=/state
mkdir -p "$STATE"

# The consoles, and each one's page name and password (user admin).
HUB1=https://hub-1:7080 HUB2=https://hub-2:7080
LAB=https://node-lab:7180 OFFICE=https://node-office:7180
ALICE=https://alice:7280 BOB=https://bob:7280
declare -A PW=([hub1]=hub-1-web-pw [hub2]=hub-2-web-pw [lab]=lab-web-pw [office]=office-web-pw [alice]=alice-web-pw [bob]=bob-web-pw
	[proxy1]=hub-1-web-pw [proxy2]=hub-2-web-pw [bobproxy]=hub-2-web-pw [tester]=lab-web-pw)

# ---- helpers / 工具 ---------------------------------------------------------

if [[ -t 1 ]]; then G=$'\e[32m' R=$'\e[31m' B=$'\e[1m' N=$'\e[0m'; else G= R= B= N=; fi
pass=0 fail=0 failed=()

section() { printf '\n%s== %s ==%s\n' "$B" "$*" "$N"; }

# check runs "$@" and counts it as a PASS or a FAIL named $desc.
check() {
	local desc=$1
	shift
	local out
	if out=$("$@" 2>&1); then
		pass=$((pass + 1))
		printf '  %sPASS%s %s\n' "$G" "$N" "$desc"
	else
		fail=$((fail + 1))
		failed+=("$desc")
		printf '  %sFAIL%s %s\n' "$R" "$N" "$desc"
		[[ -n $out ]] && printf '%s\n' "$out" | tail -n 8 | sed 's/^/       /'
	fi
	return 0
}

# eventually retries "$@" for up to $1 seconds.
eventually() {
	local deadline=$((SECONDS + $1))
	shift
	while true; do
		"$@" >/dev/null 2>&1 && return 0
		((SECONDS < deadline)) || break
		sleep 0.5
	done
	"$@"
}

# login URL PAGE logs into the console at URL as admin with PAGE's password,
# keeping the session's cookie in /tmp/jar.PAGE and its CSRF token in
# /tmp/csrf.PAGE.
login() {
	local out
	out=$(curl -ksS --max-time 15 -c "/tmp/jar.$2" -H 'Content-Type: application/json' \
		-d "$(jq -n -c --arg p "${PW[$2]}" '{username: "admin", password: $p}')" "$1/api/login") || return 1
	jq -e -r .csrf <<<"$out" >"/tmp/csrf.$2"
}

# get URL PAGE PATH prints the JSON of a GET in PAGE's session.
get() { curl -kfsS --max-time 15 -b "/tmp/jar.$2" "$1$3"; }

# post URL PAGE PATH JSON posts JSON in PAGE's session and prints the
# answer; fails on non-2xx.
post() {
	local body code
	body=$(curl -ksS --max-time 30 -b "/tmp/jar.$2" -H "X-Fit-Csrf: $(cat "/tmp/csrf.$2" 2>/dev/null)" \
		-H 'Content-Type: application/json' -d "$4" -w '\n%{http_code}' "$1$3") || return 1
	code=${body##*$'\n'}
	body=${body%$'\n'*}
	printf '%s\n' "$body"
	[[ $code == 2* ]] || {
		echo "HTTP $code from POST $1$3" >&2
		return 1
	}
}

# refused URL PAGE PATH JSON succeeds when the POST is answered with 4xx/5xx.
refused() { ! post "$@" >/dev/null 2>&1; }

# js URL PAGE JQ prints jq JQ of the status, and fails on null or false.
js() { get "$1" "$2" /api/status | jq -e -r "$3"; }

# code URL [curl args...] prints the HTTP status of a GET.
code() {
	local u=$1
	shift
	curl -ks -o /dev/null --max-time 10 -w '%{http_code}' "$@" "$u"
}

is() { [[ $1 == "$2" ]] || { echo "got '$1', want '$2'" >&2; return 1; }; }

# t CODE evaluates CODE, for checks written inline.
t() { eval "$1"; }

# echo_once HOST PORT sends a line through a forward to an echo service and
# reads it back.
echo_once() {
	local fd msg="ping $RANDOM $RANDOM" line
	exec {fd}<>"/dev/tcp/$1/$2" || return 1
	printf '%s\n' "$msg" >&"$fd"
	IFS= read -r -t 10 -u "$fd" line
	exec {fd}>&-
	is "$line" "$msg"
}

# echo_big HOST PORT sends 4 MiB through an echo service and checks what
# comes back.
echo_big() {
	local fd f=/tmp/big.$$ want got
	head -c $((4 << 20)) /dev/urandom >"$f"
	want=$(sha256sum <"$f" | cut -d' ' -f1)
	exec {fd}<>"/dev/tcp/$1/$2" || return 1
	cat "$f" >&"$fd" &
	got=$(timeout 60 head -c $((4 << 20)) <&"$fd" | sha256sum | cut -d' ' -f1)
	wait
	exec {fd}>&-
	rm -f "$f"
	is "$got" "$want"
}

# http_has HOST PORT TEXT: the page / through a forward has TEXT.
http_has() { curl -fsS --max-time 15 "http://$1:$2/" | grep -q "$3"; }

# http_big HOST PORT downloads big.bin through a forward and checks its
# SHA-256 against big.sha256.
http_big() {
	local want got
	want=$(curl -fsS --max-time 15 "http://$1:$2/big.sha256") || return 1
	got=$(curl -fsS --max-time 60 "http://$1:$2/big.bin" | sha256sum | cut -d' ' -f1)
	is "$got" "$want"
}

# closed_by_peer FD: the connection on FD gets EOF within 10 seconds.
closed_by_peer() { timeout 10 cat <&"$1" >/dev/null; }

# no_connect HOST PORT: nothing accepts there.
no_connect() { ! timeout 3 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; }

# conn_dropped HOST PORT: a connection there is closed without an answer.
conn_dropped() {
	local fd out rc
	exec {fd}<>"/dev/tcp/$1/$2" || return 0
	printf 'hello\n' >&"$fd" 2>/dev/null
	out=$(timeout 10 cat <&"$fd")
	rc=$?
	exec {fd}>&-
	[[ $rc == 0 && -z $out ]] || {
		echo "not closed at once (exit $rc, read '$out')" >&2
		return 1
	}
}

# peer URL PAGE NAME TYPE prints the server's peer of that name and type
# (node or client).
peer() { get "$1" "$2" /api/status | jq -e -c --arg n "$3" --arg t "$4" '.peers[] | select(.info.name == $n and .info.type == $t)'; }
peer_id() { peer "$@" | jq -r .id; }
kick() { post "$1" "$2" /api/peers/kick "{\"id\":$(peer_id "$@")}" >/dev/null; }

# protos URL PAGE SERVER prints the protocols of the uplink to SERVER, as
# "proto:count ...".
protos() { get "$1" "$2" /api/status | jq -r --arg s "$3" '.servers[] | select(.name == $s) | .protos // {} | to_entries | map("\(.key):\(.value)") | sort | join(" ")'; }
protos_are() { is "$(protos "$1" "$2" "$3")" "$4"; }
registered() { is "$(get "$1" "$2" /api/status | jq -r --arg s "$3" '.servers[] | select(.name == $s) | .state')" registered; }

server_json() { # NAME HOST FINGERPRINT
	jq -n -c --arg n "$1" --arg h "$2" --arg fp "$3" --arg pw "$HUB_PASSWORD" --arg body "$BODY_PASSWORD" '{
		name: $n, password: $pw, body_password: $body, pool_size: 2, quic_pin: $fp,
		endpoints: [
			{protocol: "quic", address: "\($h):7443", enabled: true},
			{protocol: "ws", address: "ws://\($h):7001/fibhub", enabled: true},
			{protocol: "tcp", address: "\($h):7000", enabled: true}]}'
}

services_json() { # NAME:ADDRESS[:DESCRIPTION]...
	local s out=()
	for s in "$@"; do
		IFS='|' read -r n a d <<<"$s"
		out+=("$(jq -n -c --arg n "$n" --arg a "$a" --arg d "${d:-}" '{name: $n, address: $a, description: $d}')")
	done
	(IFS=,; echo "[${out[*]}]")
}

LAB_SERVICES=("echo|dev-lab:7|echo service of dev-lab" "http|dev-lab:80|web server of dev-lab" "dead|dev-lab:9|nothing listens")

wait_pages() {
	local u
	for u in "$HUB1|hub1" "$HUB2|hub2" "$LAB|lab" "$OFFICE|office" "$ALICE|alice" "$BOB|bob"; do
		eventually 60 login "${u%%|*}" "${u#*|}" || {
			echo "${u%%|*} is not up" >&2
			return 1
		}
	done
}

fingerprints() {
	FP1=$(get "$HUB1" hub1 /api/status | jq -r .quic_fingerprint)
	FP2=$(get "$HUB2" hub2 /api/status | jq -r .quic_fingerprint)
	[[ -n $FP1 && -n $FP2 ]]
}

# ---- setup / 配置 -----------------------------------------------------------

setup() {
	section "setup: hub-1 through its console / 通过控制台配置 hub-1"
	check "hub-1 starts empty: no listeners, no password" \
		js "$HUB1" hub1 '(.listeners | length) == 0 and (.settings.has_password | not)'
	check "set hub-1's passwords and console socket" \
		post "$HUB1" hub1 /api/settings "$(jq -n -c --arg p "$HUB_PASSWORD" --arg b "$BODY_PASSWORD" --arg w "$WS_PASSWORD" \
			'{password: $p, body_password: $b, socket_password: $w, socket_address: ":7081"}')"
	check "add hub-1 TCP listener :7000" post "$HUB1" hub1 /api/listeners '{"protocol":"tcp","address":":7000"}'
	check "add hub-1 WebSocket listener :7001/fibhub" post "$HUB1" hub1 /api/listeners '{"protocol":"ws","address":":7001","path":"/fibhub"}'
	check "add hub-1 QUIC listener :7443" post "$HUB1" hub1 /api/listeners '{"protocol":"quic","address":":7443"}'
	check "a listener added twice is refused" refused "$HUB1" hub1 /api/listeners '{"protocol":"tcp","address":":7000"}'
	check "a listener of an unknown protocol is refused" refused "$HUB1" hub1 /api/listeners '{"protocol":"udp","address":":7999"}'
	check "hub-1 has 3 open listeners and its settings" \
		js "$HUB1" hub1 '(.listeners | length) == 3 and all(.listeners[]; .error == null and .url != null) and .settings.has_password and .settings.has_body_password and .settings.has_socket_password and .body_encrypted'

	check "both servers have a QUIC fingerprint" eventually 10 fingerprints
	fingerprints
	echo "$FP1" >"$STATE/fp-hub-1"
	echo "$FP2" >"$STATE/fp-hub-2"

	section "setup: node lab through its console / 通过控制台配置节点 lab"
	check "node lab starts with no servers" js "$LAB" lab '(.servers | length) == 0'
	check "add server hub-1 (QUIC pinned, WebSocket, TCP)" post "$LAB" lab /api/servers "$(server_json hub-1 hub-1 "$FP1")"
	check "add server hub-2 (QUIC pinned, WebSocket, TCP)" post "$LAB" lab /api/servers "$(server_json hub-2 hub-2 "$FP2")"
	check "a server added twice is refused" refused "$LAB" lab /api/servers "$(server_json hub-1 hub-1 "$FP1")"
	check "set services echo, http and dead" post "$LAB" lab /api/services "$(services_json "${LAB_SERVICES[@]}")"

	section "setup: client alice through its console / 通过控制台配置客户端 alice"
	check "add server hub-1" post "$ALICE" alice /api/servers "$(server_json hub-1 hub-1 "$FP1")"
	check "add server hub-2" post "$ALICE" alice /api/servers "$(server_json hub-2 hub-2 "$FP2")"
	local f
	for f in '7301 lab echo' '7302 lab http' '7303 office echo' '7304 office http' '7305 lab echo hub-1' '7306 lab dead' '7307 lab http hub-2'; do
		set -- $f
		check "add forward :$1 → $2/$3${4:+@$4}" post "$ALICE" alice /api/forwards \
			"$(jq -n -c --arg l "0.0.0.0:$1" --arg n "$2" --arg s "$3" --arg v "${4:-}" '{listen: $l, node: $n, service: $s, server: $v}')"
	done
	check "a forward on a port already used is refused" refused "$ALICE" alice /api/forwards '{"listen":"0.0.0.0:7301","node":"lab","service":"echo"}'
	check "alice's forwards all listen" js "$ALICE" alice '(.forwards | length) == 7 and all(.forwards[]; .error == null)'
}

# ---- features / 功能 --------------------------------------------------------

t_pages() {
	section "consoles and logins / 控制台与登录"
	local u name url
	for u in "hub-1|$HUB1|hub1" "hub-2|$HUB2|hub2" "lab|$LAB|lab" "office|$OFFICE|office" "alice|$ALICE|alice" "bob|$BOB|bob"; do
		IFS='|' read -r name url pg <<<"$u"
		check "$name: no login → 401" is "$(code "$url/api/status")" 401
		check "$name: a wrong password → 401" is "$(curl -ks -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -d '{"username":"admin","password":"wrong"}' "$url/api/login")" 401
		check "$name: logged in, the page and its status" t "[[ \$(get '$url' $pg /) == *'<html'* ]] && get '$url' $pg /api/status | jq -e .name"
		check "$name: static assets" is "$(code "$url/static/base.css")" 200
		check "$name: a change without the CSRF token → 403" is "$(curl -ks -o /dev/null -w '%{http_code}' -b "/tmp/jar.$pg" -H 'Content-Type: application/json' -d '{}' "$url/api/tunnels/close")" 403
		check "$name: no inline scripts, which the CSP refuses" t "! get '$url' $pg / | grep -E -q 'on(click|submit|change)=|<script>'"
	done
}

t_servers() {
	section "servers / 服务端"
	check "hub-2 (from its config) has TCP, WebSocket and QUIC listeners" \
		js "$HUB2" hub2 '([.listeners[].protocol] | sort) == ["quic","tcp","ws"] and all(.listeners[]; .error == null)'
	check "hub-2 (from its config) has its passwords" js "$HUB2" hub2 '.settings.has_password and .settings.has_body_password and .settings.has_socket_password'
	check "both servers have a QUIC fingerprint" fingerprints
	local h name url pg
	for h in "hub-1|$HUB1|hub1" "hub-2|$HUB2|hub2"; do
		IFS='|' read -r name url pg <<<"$h"
		for p in "lab node" "office node" "alice client" "bob client"; do
			set -- $p
			check "$name has $2 $1 registered" eventually 60 peer "$url" "$pg" "$1" "$2"
		done
	done
}

t_links() {
	section "links, protocols and pools / 链路、协议与连接池"
	check "lab → hub-1 over QUIC, pool of 2" eventually 30 protos_are "$LAB" lab hub-1 "quic:2"
	check "lab → hub-2 over QUIC, pool of 2" eventually 30 protos_are "$LAB" lab hub-2 "quic:2"
	check "alice → hub-1 over QUIC" eventually 30 protos_are "$ALICE" alice hub-1 "quic:2"
	check "alice → hub-2 over QUIC" eventually 30 protos_are "$ALICE" alice hub-2 "quic:2"
	check "office → hub-2 over QUIC (insecure, from its config)" eventually 30 protos_are "$OFFICE" office hub-2 "quic:2"
	check "office → hub-1 over TCP (from its config)" eventually 30 protos_are "$OFFICE" office hub-1 "tcp:2"
	check "bob → hub-1 over TCP with the default pool of 10" eventually 30 protos_are "$BOB" bob hub-1 "tcp:10"
	check "hub-1 counts 2 links of lab" t "[[ \$(peer '$HUB1' hub1 lab node | jq .conns) == 2 ]]"
	check "hub-1 counts 10 links of bob" t "[[ \$(peer '$HUB1' hub1 bob client | jq .conns) == 10 ]]"
	check "bodies encrypted on every uplink" t "for u in '$LAB|lab' '$ALICE|alice' '$OFFICE|office' '$BOB|bob'; do get \"\${u%%|*}\" \"\${u#*|}\" /api/status | jq -e 'all(.servers[]; .body_encrypted)' >/dev/null || exit 1; done"
	check "latency measured on the links (pings every 5s)" eventually 20 js "$LAB" lab 'all(.servers[]; .rtt.samples > 0)'
}

t_discovery() {
	section "nodes and services seen by clients / 客户端看到的节点与服务"
	check "alice sees lab and office on hub-1" eventually 30 js "$ALICE" alice \
		'[.servers[] | select(.name == "hub-1") | .nodes[].name] | sort == ["lab","office"]'
	check "alice sees lab's services echo, http, dead" js "$ALICE" alice \
		'[.servers[] | select(.name == "hub-2") | .nodes[] | select(.name == "lab") | .services[].name] | sort == ["dead","echo","http"]'
	check "alice's share of lab over the two servers adds up to 1" js "$ALICE" alice \
		'([.servers[] | .nodes[] | select(.name == "lab") | .share] | add) as $s | $s > 0.99 and $s < 1.01'
	check "node office has its services from its config" js "$OFFICE" office '[.services[].name] | sort == ["echo","http"]'
}

t_tunnels() {
	section "tunnels / 隧道"
	check "the devices are out of the tester's reach" no_connect dev-lab 80
	check "alice → lab/echo" eventually 30 echo_once alice 7301
	check "alice → lab/http" eventually 30 http_has alice 7302 "device dev-lab"
	check "alice → office/echo" eventually 30 echo_once alice 7303
	check "alice → office/http" eventually 30 http_has alice 7304 "device dev-office"
	check "alice → lab/echo@hub-1" eventually 30 echo_once alice 7305
	check "alice → lab/http@hub-2" eventually 30 http_has alice 7307 "device dev-lab"
	check "bob → office/echo (forward from its config)" eventually 30 echo_once bob 7301
	check "bob → lab/http (forward from its config)" eventually 30 http_has bob 7302 "device dev-lab"
	check "8 MiB download through alice → lab/http intact" http_big alice 7302
	check "8 MiB download through alice → office/http intact" http_big alice 7304
	check "8 MiB download through bob → lab/http intact" http_big bob 7302
	check "4 MiB echoed through alice → lab/echo intact" echo_big alice 7301
	check "4 MiB echoed through bob → office/echo intact" echo_big bob 7301
	check "30 tunnels at once through alice → lab/echo" parallel_echo alice 7301 30
	check "a service that refuses: the tunnel closes" conn_dropped alice 7306
}

parallel_echo() {
	local i pids=() bad=0
	for ((i = 0; i < $3; i++)); do
		echo_once "$1" "$2" &
		pids+=($!)
	done
	for i in "${pids[@]}"; do wait "$i" || bad=$((bad + 1)); done
	is "$bad failed" "0 failed"
}

# hold N HOST PORT opens N echo tunnels and keeps them on fds HELD.
hold() {
	local i fd line
	HELD=()
	for ((i = 0; i < $1; i++)); do
		exec {fd}<>"/dev/tcp/$2/$3" || return 1
		printf 'hold\n' >&"$fd"
		IFS= read -r -t 10 -u "$fd" line || return 1
		HELD+=("$fd")
	done
}
release() {
	local fd
	for fd in "${HELD[@]}"; do exec {fd}>&-; done
	HELD=()
}

# tunnels_of URL PAGE JQ-FILTER counts a status's tunnels matching it.
tunnels_of() { get "$1" "$2" /api/status | jq "[.tunnels[] | select($3)] | length"; }

t_tunnel_views() {
	section "tunnels on the pages, closing them / 页面上的隧道与关闭"
	local id fd
	hold 1 alice 7305 # lab/echo@hub-1
	fd=${HELD[0]}
	check "hub-1 lists alice → lab/echo with bytes both ways" eventually 10 js "$HUB1" hub1 \
		'any(.tunnels[]; .client == "alice" and .node == "lab" and .service == "echo" and .up > 0 and .down > 0)'
	check "node lab lists it" js "$LAB" lab 'any(.tunnels[]; .server == "hub-1" and .service == "echo")'
	check "alice lists it" js "$ALICE" alice 'any(.tunnels[]; .server == "hub-1" and .node == "lab" and .service == "echo")'
	check "hub-2 does not (the forward names hub-1)" is "$(tunnels_of "$HUB2" hub2 '.client == "alice"')" 0
	id=$(get "$HUB1" hub1 /api/status | jq -r '[.tunnels[] | select(.client == "alice" and .service == "echo")][0].id')
	check "close it from hub-1's console" post "$HUB1" hub1 /api/tunnels/close "{\"id\":\"$id\"}"
	check "  … the connection closes" closed_by_peer "$fd"
	check "closing an unknown tunnel is refused" refused "$HUB1" hub1 /api/tunnels/close '{"id":"999999"}'
	release

	hold 1 alice 7305
	fd=${HELD[0]}
	id=$(get "$LAB" lab /api/status | jq -r '[.tunnels[] | select(.server == "hub-1" and .service == "echo")][0].id')
	check "close it from node lab's console" post "$LAB" lab /api/tunnels/close "{\"server\":\"hub-1\",\"id\":\"$id\"}"
	check "  … the connection closes" closed_by_peer "$fd"
	release

	hold 1 alice 7305
	fd=${HELD[0]}
	id=$(get "$ALICE" alice /api/status | jq -r '[.tunnels[] | select(.server == "hub-1" and .service == "echo")][0].id')
	check "close it from alice's console" post "$ALICE" alice /api/tunnels/close "{\"server\":\"hub-1\",\"id\":\"$id\"}"
	check "  … the connection closes" closed_by_peer "$fd"
	release
	check "no tunnels left on hub-1" eventually 10 is "$(tunnels_of "$HUB1" hub1 'true')" 0
}

t_balance() {
	section "load balancing over two servers / 两台服务端之间的负载均衡"
	hold 16 alice 7301 # lab/echo, no server named
	local n1 n2
	n1=$(tunnels_of "$HUB1" hub1 '.client == "alice"')
	n2=$(tunnels_of "$HUB2" hub2 '.client == "alice"')
	check "16 tunnels to lab spread over hub-1 ($n1) and hub-2 ($n2)" t "(( $n1 > 0 && $n2 > 0 && $n1 + $n2 == 16 ))"
	release
}

t_node_admin() {
	section "nodes managed from servers and clients / 从服务端和客户端管理节点"
	local id
	id=$(peer_id "$HUB1" hub1 office node)
	check "hub-1 queries node office" t "post '$HUB1' hub1 /api/peers/refresh '{\"id\":$id}' | jq -e '[.services[].name] | sort == [\"echo\",\"http\"]'"
	check "hub-1 sets node office's services (adds http2)" post "$HUB1" hub1 /api/peers/services \
		"{\"id\":$id,\"services\":$(services_json "echo|dev-office:7" "http|dev-office:80" "http2|dev-office:80|added from hub-1")}"
	check "  … node office has http2" eventually 10 js "$OFFICE" office 'any(.services[]; .name == "http2")'
	check "  … hub-2 hears of it" eventually 15 js "$HUB2" hub2 'any(.peers[] | select(.info.name == "office") | .info.services[]; .name == "http2")'
	check "querying a client as a node is refused" refused "$HUB1" hub1 /api/peers/refresh "{\"id\":$(peer_id "$HUB1" hub1 alice client)}"
	check "alice refreshes hub-2's nodes" t "post '$ALICE' alice /api/nodes/refresh '{\"server\":\"hub-2\"}' | jq -e '[.nodes[].name] | sort == [\"lab\",\"office\"]'"
	check "alice sets node office's services through hub-2 (removes http2)" post "$ALICE" alice /api/nodes/services \
		"{\"server\":\"hub-2\",\"node\":\"office\",\"services\":$(services_json "echo|dev-office:7|echo service of dev-office" "http|dev-office:80|web server of dev-office")}"
	check "  … node office no longer has http2" eventually 10 js "$OFFICE" office '[.services[].name] | sort == ["echo","http"]'
	check "node lab edits its own services (adds tmp)" post "$LAB" lab /api/services "$(services_json "${LAB_SERVICES[@]}" "tmp|dev-lab:7")"
	check "  … hub-1 hears of it" eventually 15 js "$HUB1" hub1 'any(.peers[] | select(.info.name == "lab") | .info.services[]; .name == "tmp")'
	check "  … a forward to the new service works" forward_round alice 7308 lab tmp
	check "node lab removes it again" post "$LAB" lab /api/services "$(services_json "${LAB_SERVICES[@]}")"
	check "  … a forward to it is now dropped" forward_dropped alice 7308 lab tmp
	check "a service that is not host:port is refused" refused "$LAB" lab /api/services '[{"name":"bad","address":"nowhere"}]'
}

# forward_round HOST PORT NODE SERVICE adds a forward on alice, echoes
# through it and removes it.
forward_round() {
	post "$ALICE" alice /api/forwards "{\"listen\":\"0.0.0.0:$2\",\"node\":\"$3\",\"service\":\"$4\"}" >/dev/null || return 1
	# In a subshell: a connect that fails ends the shell it runs in.
	(eventually 20 echo_once "$1" "$2")
	local rc=$?
	post "$ALICE" alice /api/forwards/delete "{\"listen\":\"0.0.0.0:$2\"}" >/dev/null || return 1
	return $rc
}
forward_dropped() {
	post "$ALICE" alice /api/forwards "{\"listen\":\"0.0.0.0:$2\",\"node\":\"$3\",\"service\":\"$4\"}" >/dev/null || return 1
	sleep 1
	(conn_dropped "$1" "$2")
	local rc=$?
	post "$ALICE" alice /api/forwards/delete "{\"listen\":\"0.0.0.0:$2\"}" >/dev/null || return 1
	return $rc
}

t_forwards() {
	section "forwards added and removed while running / 运行时增删转发"
	check "add forward :7309 → office/echo" post "$ALICE" alice /api/forwards '{"listen":"0.0.0.0:7309","node":"office","service":"echo"}'
	check "  … it works" eventually 20 echo_once alice 7309
	check "remove it" post "$ALICE" alice /api/forwards/delete '{"listen":"0.0.0.0:7309"}'
	check "  … nothing listens there" no_connect alice 7309
	check "removing an unknown forward is refused" refused "$ALICE" alice /api/forwards/delete '{"listen":"0.0.0.0:7399"}'
	check "a forward to an unknown node drops its connections" forward_dropped alice 7308 nonode echo
	check "a forward to a service the node does not have drops its connections" forward_dropped alice 7308 lab noservice
	check "a forward through an unknown server is refused" refused "$ALICE" alice /api/forwards '{"listen":"0.0.0.0:7308","node":"lab","service":"echo","server":"nope"}'
}

t_meta() {
	section "meta / 元数据"
	check "alice sets her meta" post "$ALICE" alice /api/meta '{"team":"qa","owner":"alice"}'
	check "  … hub-1 sees it" eventually 15 t "peer '$HUB1' hub1 alice client | jq -e '.info.meta.team == \"qa\"'"
	check "hub-2 sees office's meta from its config" t "peer '$HUB2' hub2 office node | jq -e '.info.meta.site == \"office\"'"
	check "hub-1 sees bob's meta from its config" t "peer '$HUB1' hub1 bob client | jq -e '.info.meta.owner == \"bob\"'"
}

t_webproxy() {
	section "server consoles through clients' encrypted web proxies / 通过客户端加密 web 代理访问服务端控制台"
	check "alice :7381 serves hub-1's console, which asks for its login" eventually 20 is "$(code https://alice:7381/api/status)" 401
	check "  … logged in through it, hub-1's status" t "login https://alice:7381 proxy1 && get https://alice:7381 proxy1 /api/status | jq -e '.name == \"hub-1\"'"
	check "alice :7382 serves hub-2's console" t "login https://alice:7382 proxy2 && get https://alice:7382 proxy2 /api/status | jq -e '.name == \"hub-2\"'"
	check "bob :7381 (from its config) serves hub-2's console" t "login https://bob:7381 bobproxy && get https://bob:7381 bobproxy /api/status | jq -e '.name == \"hub-2\"'"
	check "a proxy with the wrong password gets nothing" t "! curl -kfsS --max-time 10 https://alice:7389/"
	check "alice shows her proxies" js "$ALICE" alice '[.web_proxies[].name] | sort == ["hub-1","hub-1-bad","hub-2"]'
	check "hub-1 changes its console socket's password" post "$HUB1" hub1 /api/settings '{"socket_password":"another-ws-pass","socket_address":":7081"}'
	check "  … alice's proxy no longer gets through" eventually 10 t "! curl -kfsS --max-time 10 https://alice:7381/"
	check "  … and does again once set back" post "$HUB1" hub1 /api/settings "{\"socket_password\":\"$WS_PASSWORD\",\"socket_address\":\":7081\"}"
	check "  … alice :7381 serves hub-1's console again" eventually 10 is "$(code https://alice:7381/static/base.css)" 200
	check "hub-1 kept its other passwords" js "$HUB1" hub1 '.settings.has_password and .settings.has_body_password'
}

t_fallback() {
	section "listeners changed while running, protocol fallback / 运行时修改监听与协议回退"
	check "hub-1 removes its QUIC listener" post "$HUB1" hub1 /api/listeners/delete '{"protocol":"quic","address":":7443"}'
	check "  … lab falls back to WebSocket" eventually 40 protos_are "$LAB" lab hub-1 "ws:2"
	check "  … alice falls back to WebSocket" eventually 40 protos_are "$ALICE" alice hub-1 "ws:2"
	check "  … tunnels through hub-1 work" eventually 20 echo_once alice 7305
	check "hub-1 removes its WebSocket listener" post "$HUB1" hub1 /api/listeners/delete '{"protocol":"ws","address":":7001","path":"/fibhub"}'
	# Only a QUIC listener's removal closes its links; kick the others.
	check "  … and kicks lab and alice off it" t "kick '$HUB1' hub1 lab node && kick '$HUB1' hub1 alice client"
	check "  … lab falls back to TCP" eventually 40 protos_are "$LAB" lab hub-1 "tcp:2"
	check "  … alice falls back to TCP" eventually 40 protos_are "$ALICE" alice hub-1 "tcp:2"
	check "  … tunnels through hub-1 work" eventually 20 echo_once alice 7305
	check "hub-1 moves its TCP listener to :7010" post "$HUB1" hub1 /api/listeners/update \
		'{"old":{"protocol":"tcp","address":":7000"},"new":{"protocol":"tcp","address":":7010"}}'
	check "  … it shows" js "$HUB1" hub1 '[.listeners[].address] == [":7010"]'
	check "  … and kicks office off it" kick "$HUB1" hub1 office node
	check "  … office (TCP :7000 only) cannot get back to hub-1" t "sleep 8; ! registered '$OFFICE' office hub-1"
	check "hub-1 puts everything back" t "
		post '$HUB1' hub1 /api/listeners/update '{\"old\":{\"protocol\":\"tcp\",\"address\":\":7010\"},\"new\":{\"protocol\":\"tcp\",\"address\":\":7000\"}}' &&
		post '$HUB1' hub1 /api/listeners '{\"protocol\":\"ws\",\"address\":\":7001\",\"path\":\"/fibhub\"}' &&
		post '$HUB1' hub1 /api/listeners '{\"protocol\":\"quic\",\"address\":\":7443\"}'"
	check "  … office registers with hub-1 again" eventually 60 registered "$OFFICE" office hub-1
	check "  … the QUIC fingerprint is unchanged (kept in the config)" js "$HUB1" hub1 ".quic_fingerprint == \"$(cat "$STATE/fp-hub-1")\""
	local id
	id=$(peer_id "$HUB1" hub1 alice client)
	check "hub-1 kicks lab and alice" t "kick '$HUB1' hub1 lab node && kick '$HUB1' hub1 alice client"
	check "  … lab comes back over QUIC" eventually 40 protos_are "$LAB" lab hub-1 "quic:2"
	check "  … alice comes back over QUIC" eventually 40 protos_are "$ALICE" alice hub-1 "quic:2"
	check "  … with a new peer id" t "[[ \$(peer_id '$HUB1' hub1 alice client) != $id ]]"
	check "  … tunnels through hub-1 work" eventually 20 echo_once alice 7305
	check "kicking an unknown peer is refused" refused "$HUB1" hub1 /api/peers/kick '{"id":99999}'
}

t_uplink_edit() {
	section "servers edited on nodes and clients / 在节点和客户端上编辑服务端"
	check "alice edits hub-2: QUIC off, passwords left empty are kept" post "$ALICE" alice /api/servers/update \
		"$(jq -n -c --arg fp "$(cat "$STATE/fp-hub-2")" '{name: "hub-2", server: {name: "hub-2", password: "", pool_size: 3, quic_pin: $fp, endpoints: [
			{protocol: "quic", address: "hub-2:7443", enabled: false}, {protocol: "ws", address: "ws://hub-2:7001/fibhub", enabled: true}, {protocol: "tcp", address: "hub-2:7000", enabled: true}]}}')"
	check "  … alice → hub-2 over WebSocket, pool of 3" eventually 40 protos_are "$ALICE" alice hub-2 "ws:3"
	check "  … tunnels through hub-2 work" eventually 20 http_has alice 7307 "device dev-lab"
	check "alice puts hub-2 back" post "$ALICE" alice /api/servers/update \
		"$(jq -n -c --argjson s "$(server_json hub-2 hub-2 "$(cat "$STATE/fp-hub-2")")" '{name: "hub-2", server: ($s | .password = "" | .body_password = "")}')"
	check "  … over QUIC again" eventually 40 protos_are "$ALICE" alice hub-2 "quic:2"
	check "lab adds a server with a wrong QUIC pin and nothing else" post "$LAB" lab /api/servers \
		"$(jq -n -c --arg pw "$HUB_PASSWORD" --arg b "$BODY_PASSWORD" '{name: "badpin", password: $pw, body_password: $b, pool_size: 1, quic_pin: ("00" * 32), endpoints: [{protocol: "quic", address: "hub-1:7443", enabled: true}]}')"
	check "  … it fails on the pin" eventually 30 t "get '$LAB' lab /api/status | jq -e '.servers[] | select(.name == \"badpin\") | .state != \"registered\" and (.error // \"\" | test(\"pin|certificate\"; \"i\"))'"
	check "lab removes it" post "$LAB" lab /api/servers/delete '{"name":"badpin"}'
	check "removing an unknown server is refused" refused "$LAB" lab /api/servers/delete '{"name":"nope"}'
}

# t_peers runs fits of its own in the tester, each a node: right and wrong
# passwords, and a node over a WebSocket URL.
t_peers() {
	section "peers with wrong secrets, a node in the tester / 错误密钥的对端，测试容器中的节点"
	local con=http://127.0.0.1:7180 dir=/tmp/peers
	mkdir -p "$dir"
	run_peer() { # NAME SERVER-JSON [SERVICES-JSON]
		jq -n --arg n "$1" --argjson s "$2" --argjson v "${3:-[]}" --arg h "$(jq -r '.tunnel.node.console.users[0].password_hash' /conf/node-lab.json)" \
			'{tunnel: {node: {name: $n, servers: [$s], services: $v, console: {address: "127.0.0.1:7180", users: [{username: "admin", password_hash: $h}]}}}}' >"$dir/$1.json"
		fit run -c "$dir/$1.json" >"$dir/$1.log" 2>&1 &
		PEER_PID=$!
		eventually 10 login "$con" tester >/dev/null
	}
	stop_peer() { kill "$PEER_PID" 2>/dev/null; wait "$PEER_PID" 2>/dev/null; }
	uplink_error() { get "$con" tester /api/status | jq -e -r --arg re "$1" '.servers[0] | .state != "registered" and (.error // "" | test($re; "i"))'; }
	up() { # ADDRESS PASSWORD [BODY]
		jq -n -c --arg a "$1" --arg p "$2" --arg b "${3:-}" '{name: "hub", endpoints: [{address: $a}], password: $p, retry_interval: "1s"} + (if $b == "" then {} else {body_password: $b} end)'
	}

	run_peer intruder "$(up hub-1:7000 wrong-password "$BODY_PASSWORD")"
	check "a wrong registration password: authentication fails" eventually 20 uplink_error "auth"
	check "  … hub-1 has no such peer" t "! peer '$HUB1' hub1 intruder node"
	stop_peer

	run_peer intruder "$(up hub-1:7000 "$HUB_PASSWORD" wrong-body-pass)"
	check "a wrong body password: never registers" t "sleep 5; get '$con' tester /api/status | jq -e '.servers[0].state != \"registered\"'"
	check "  … hub-1 has no such peer" t "! peer '$HUB1' hub1 intruder node"
	stop_peer

	run_peer intruder "$(up hub-1:7000 "$HUB_PASSWORD")"
	check "no body password where the server has one: never registers" t "sleep 5; get '$con' tester /api/status | jq -e '.servers[0].state != \"registered\"'"
	stop_peer

	run_peer intruder "$(jq -n -c --arg p "$HUB_PASSWORD" --arg b "$BODY_PASSWORD" '{name: "hub", endpoints: [{address: "quic://hub-1:7443"}], password: $p, body_password: $b, quic_pin: ("0" * 64)}')"
	check "a wrong QUIC pin: refused" eventually 20 uplink_error "pin|certificate"
	stop_peer

	socat TCP-LISTEN:7777,fork,reuseaddr EXEC:cat &
	local echo_pid=$!
	# Left without a name, the server is named by its address.
	run_peer tnode "$(jq -n -c --arg p "$HUB_PASSWORD" --arg b "$BODY_PASSWORD" '{endpoints: [{address: "ws://hub-2:7001/fibhub"}], password: $p, body_password: $b, pool_size: 1}')" \
		'[{"name":"echo","address":"127.0.0.1:7777"}]'
	check "a node over a WebSocket URL registers" eventually 20 t "registered '$con' tester ws://hub-2:7001/fibhub"
	check "  … hub-2 has it" eventually 10 peer "$HUB2" hub2 tnode node
	check "  … alice reaches its service" forward_round alice 7308 tnode echo
	stop_peer
	kill "$echo_pid" 2>/dev/null
	check "  … once stopped, hub-2 drops it" eventually 20 t "! peer '$HUB2' hub2 tnode node"
}

t_secrets() {
	section "no secrets on the pages / 页面不泄露密钥"
	local u name url pg
	for u in "hub-1|$HUB1|hub1" "hub-2|$HUB2|hub2" "lab|$LAB|lab" "office|$OFFICE|office" "alice|$ALICE|alice" "bob|$BOB|bob"; do
		IFS='|' read -r name url pg <<<"$u"
		check "$name: status has none of the passwords or keys" t "! get '$url' $pg /api/status | grep -E -q '$HUB_PASSWORD|$BODY_PASSWORD|$WS_PASSWORD|PRIVATE KEY'"
	done
}

t_cli() {
	section "command line / 命令行"
	check "fit version" fit version
	local f
	for f in /conf/*.json; do
		check "fit validate $(basename "$f")" fit validate -c "$f"
	done
}

restart_check() {
	section "after a restart: settings from the stores / 重启之后：设置从存储中恢复"
	check "every console is up" wait_pages
	check "hub-1 kept its listeners and passwords" eventually 30 js "$HUB1" hub1 \
		'([.listeners[].protocol] | sort) == ["quic","tcp","ws"] and all(.listeners[]; .error == null) and .settings.has_password and .settings.has_body_password and .settings.has_socket_password'
	check "hub-1 kept its QUIC certificate" js "$HUB1" hub1 ".quic_fingerprint == \"$(cat "$STATE/fp-hub-1")\""
	check "hub-2 kept its QUIC certificate" js "$HUB2" hub2 ".quic_fingerprint == \"$(cat "$STATE/fp-hub-2")\""
	check "lab kept its servers and services" js "$LAB" lab '([.servers[].name] | sort) == ["hub-1","hub-2"] and ([.services[].name] | sort) == ["dead","echo","http"]'
	check "alice kept her servers, forwards and meta" js "$ALICE" alice \
		'([.servers[].name] | sort) == ["hub-1","hub-2"] and (.forwards | length) == 7 and .meta.team == "qa"'
	check "office kept its services" js "$OFFICE" office '[.services[].name] | sort == ["echo","http"]'
	check "lab → hub-1 over QUIC again" eventually 60 protos_are "$LAB" lab hub-1 "quic:2"
	check "alice → hub-2 over QUIC again" eventually 60 protos_are "$ALICE" alice hub-2 "quic:2"
	check "office → hub-1 over TCP again" eventually 60 protos_are "$OFFICE" office hub-1 "tcp:2"
	check "alice → lab/echo" eventually 60 echo_once alice 7301
	check "alice → office/http" eventually 60 http_has alice 7304 "device dev-office"
	check "alice → lab/echo@hub-1" eventually 60 echo_once alice 7305
	check "bob → office/echo" eventually 60 echo_once bob 7301
	check "8 MiB download through alice → lab/http intact" http_big alice 7302
	check "alice's proxy serves hub-1's console" eventually 20 t "login https://alice:7381 proxy1 && get https://alice:7381 proxy1 /api/status | jq -e '.name == \"hub-1\"'"
}

features() {
	fingerprints || {
		echo "no QUIC fingerprints; run setup first" >&2
		exit 1
	}
	t_pages
	t_servers
	t_links
	t_discovery
	t_tunnels
	t_tunnel_views
	t_balance
	t_node_admin
	t_forwards
	t_meta
	t_webproxy
	t_fallback
	t_uplink_edit
	t_peers
	t_secrets
	t_cli
}

# ---- main / 主流程 ----------------------------------------------------------

phase=${1:-all}
section "waiting for every console / 等待所有控制台"
check "every console is up" wait_pages
case $phase in
all) setup && features ;;
setup) setup ;;
features) features ;;
restart-check) restart_check ;;
*)
	echo "usage: run.sh all|setup|features|restart-check" >&2
	exit 2
	;;
esac

printf '\n%s%d passed, %d failed%s\n' "$B" "$pass" "$fail" "$N"
if ((fail > 0)); then
	printf '  %sFAIL%s %s\n' "$R" "$N" "${failed[@]}"
	exit 1
fi
