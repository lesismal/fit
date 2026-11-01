#!/usr/bin/env bash
# fit-tunnel-docker.sh starts, stops and tests the docker test environment
# of compose.yml: tunnel hubs hub-1 and hub-2, nodes lab and office,
# proxies alice and bob, each a fit of its own, the devices dev-lab and
# dev-office behind the nodes, and a tester that drives every feature
# through the consoles' API and the tunnels. It is a functional test, not a
# load test: it measures nothing.
#
#   fit-tunnel-docker.sh start      build the image from this checkout and
#                                   start a fresh environment (new stores)
#   fit-tunnel-docker.sh setup      set up hub-1, node lab and proxy alice
#                                   through their consoles, as the test does
#   fit-tunnel-docker.sh test       start afresh, set up, test every feature,
#                                   restart every instance, check they came
#                                   back from their stores
#   fit-tunnel-docker.sh stop       stop and remove it, stores included
#   fit-tunnel-docker.sh status     the containers and the consoles' addresses
#   fit-tunnel-docker.sh logs [svc] follow the logs
#   fit-tunnel-docker.sh restart    restart the fit instances (keeps stores)
#   fit-tunnel-docker.sh tester [phase]  run.sh in the tester: all, setup,
#                                   features or restart-check
#   fit-tunnel-docker.sh sh SVC     a shell in a container
#
# test exits 1 if any check failed. --no-build skips building the image;
# GOPROXY is passed to the build. The consoles are published on 127.0.0.1
# over HTTPS with a self-signed certificate (see status);
# FIT_TUNNEL_DOCKER_PROJECT names the compose project (default
# fit-tunnel-test).
#
# fit-tunnel-docker.sh 启动、停止和测试 compose.yml 描述的 docker 测试环境：
# 隧道 hub hub-1、hub-2，节点 lab、office，proxy alice、bob（各自是一个
# fit），节点后面的设备 dev-lab、dev-office，以及通过控制台 API 和隧道测试全部
# 功能的测试容器。这是功能测试，不是压力测试。
#
#   start    从本仓库构建镜像并启动一个全新的环境（新的加密存储）
#   setup    像测试那样通过控制台配置 hub-1、节点 lab 和 proxy alice
#   test     全新启动、配置、测试全部功能、重启所有实例、检查设置从存储恢复
#   stop     停止并删除环境，包括存储
#   status   容器状态和各控制台地址
#   logs     跟踪日志；restart 重启 fit 实例（保留存储）
#   tester   在测试容器中运行 run.sh 的某个阶段；sh 进入某个容器的 shell
#
# 有检查失败时 test 退出码为 1。--no-build 跳过构建镜像；GOPROXY 会传给构建。
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
export FIT_TUNNEL_DOCKER_PROJECT=${FIT_TUNNEL_DOCKER_PROJECT:-fit-tunnel-test}
export FIT_TUNNEL_DOCKER_IMAGE=${FIT_TUNNEL_DOCKER_IMAGE:-fit-tunnel-test:latest}
INSTANCES=(hub-1 hub-2 node-lab node-office alice bob)

die() {
	echo "fit-tunnel-docker: $*" >&2
	exit 1
}

compose() { docker compose -f "$here/compose.yml" "$@"; }

build=1
args=()
for a in "$@"; do
	case $a in
	--no-build) build=0 ;;
	-h | --help) sed -n '2,/^set -eu/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//' && exit 0 ;;
	*) args+=("$a") ;;
	esac
done
set -- "${args[@]+"${args[@]}"}"
cmd=${1:-help}
[[ $# -gt 0 ]] && shift

command -v docker >/dev/null || die "docker is not installed"
docker compose version >/dev/null 2>&1 || die "docker compose (v2) is not available"

build_image() {
	((build)) || return 0
	echo "building $FIT_TUNNEL_DOCKER_IMAGE from $root"
	DOCKER_BUILDKIT=1 docker build -f "$here/Dockerfile" -t "$FIT_TUNNEL_DOCKER_IMAGE" \
		${GOPROXY:+--build-arg GOPROXY="$GOPROXY"} "$root"
}

start() {
	build_image
	compose --profile test down -v --remove-orphans >/dev/null 2>&1 || true
	compose up -d
	echo
	status
}

stop() {
	compose --profile test down -v --remove-orphans
}

tester() {
	compose --profile test run --rm tester "${1:-all}"
}

status() {
	compose ps
	cat <<EOF2

Consoles, over HTTPS with a self-signed certificate (user admin):
控制台（HTTPS，自签名证书，用户 admin）:
  hub-1        https://127.0.0.1:${HUB1_WEB_PORT:-27080}   hub-1-web-pw
  hub-2        https://127.0.0.1:${HUB2_WEB_PORT:-27090}   hub-2-web-pw
  node lab     https://127.0.0.1:${NODE_LAB_WEB_PORT:-27180}   lab-web-pw
  node office  https://127.0.0.1:${NODE_OFFICE_WEB_PORT:-27190}   office-web-pw
  alice        https://127.0.0.1:${ALICE_WEB_PORT:-27280}   alice-web-pw
  bob          https://127.0.0.1:${BOB_WEB_PORT:-27290}   bob-web-pw
alice's forwards :7301-7309 are on 127.0.0.1:${ALICE_FORWARD_PORTS:-27301-27309}, e.g. lab/http after setup:
  http://127.0.0.1:27302/   (alice 的转发映射到本机端口，配置后可访问)
hub-1's console through alice's web proxy: https://127.0.0.1:27381/
Store password / 存储密码: store-pass-1234
EOF2
}

case $cmd in
start) start ;;
stop) stop ;;
status) status ;;
logs) compose logs -f --tail=100 "$@" ;;
restart) compose restart "${INSTANCES[@]}" ;;
setup) tester setup ;;
tester) tester "${1:-all}" ;;
sh) compose exec "${1:?which container?}" sh ;;
test)
	start
	rc=0
	tester all || rc=1
	echo
	echo "restarting every fit instance / 重启所有 fit 实例"
	compose restart "${INSTANCES[@]}"
	tester restart-check || rc=1
	echo
	if ((rc == 0)); then
		echo "ALL TESTS PASSED / 全部测试通过"
	else
		echo "SOME TESTS FAILED / 有测试失败; logs: $0 logs" >&2
	fi
	exit $rc
	;;
help) sed -n '2,/^set -eu/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//' ;;
*) die "unknown command $cmd (see $0 --help)" ;;
esac
