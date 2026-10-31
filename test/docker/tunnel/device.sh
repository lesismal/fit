#!/usr/bin/env bash
# device.sh runs a device of the tunnel's docker test environment, whose
# services a node reaches: an echo service on TCP port 7 and a web server on
# port 80 whose pages name the device, with big.bin (8 MiB of random bytes)
# and big.sha256 to check a tunnel's data. Port 9 is closed, a service that
# refuses connects.
#
# device.sh 运行隧道 docker 测试环境中的一台设备，节点访问它的服务：TCP 7 端口
# 的 echo 服务，和 80 端口的 web 服务（页面写着设备名），以及 big.bin（8 MiB
# 随机数据）和 big.sha256 用来校验隧道传输的数据。9 端口不监听，作为拒绝连接的
# 服务。
set -eu

name=${DEVICE_NAME:-$(hostname)}
mkdir -p /www
echo "device $name" >/www/index.html
head -c $((8 << 20)) /dev/urandom >/www/big.bin
sha256sum /www/big.bin | cut -d' ' -f1 >/www/big.sha256

socat TCP-LISTEN:7,fork,reuseaddr EXEC:cat &
busybox-extras httpd -f -p 80 -h /www &
echo "device $name: echo on :7, http on :80"
wait -n
