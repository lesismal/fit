#!/bin/sh
# host-settings.sh sets a Linux host's kernel and CPU settings to those the
# 2026-10-30 plan ran under on the benchmark host (report.md, "The test
# environment"), so that a later run, here or on another machine, starts from
# the same ones. Run it as root.
#
#   host-settings.sh            set them now, until the next boot
#   host-settings.sh -check     compare the host's settings with them, change nothing
#   host-settings.sh -persist   set them now and write them to
#                               /etc/sysctl.d/98-fitbench-plan-2026-10-30.conf
#
# The sysctl.d file is named to come after 90-fitbench-udp-buffers.conf and
# before 99-fib-test.conf, which on the benchmark host set the same keys;
# with -persist on such a host, check after a boot that 99-fib-test.conf
# gives what this file does (it does for every key but rmem_max and
# wmem_max, which bench/plan.sh sets back to 7,500,000 before every run).
#
# CPU frequency and transparent huge pages are set where the kernel offers
# them, and never persisted: they are set again after a boot, by hand.
set -eu

mode=set
case "${1:-}" in
"") ;;
-check) mode=check ;;
-persist) mode=persist ;;
*)
	echo "usage: $0 [-check | -persist]" >&2
	exit 2
	;;
esac

# The values the plan ran under. Tabs separate the three numbers of the
# tcp_rmem, tcp_wmem, tcp_mem, udp_mem and port range keys, as the kernel
# prints them.
settings='
net.core.somaxconn=32768
net.ipv4.tcp_max_syn_backlog=32768
net.core.netdev_max_backlog=250000
net.core.rmem_max=7500000
net.core.wmem_max=7500000
net.core.rmem_default=262144
net.core.wmem_default=262144
net.core.optmem_max=131072
net.ipv4.tcp_rmem=4096	262144	16777216
net.ipv4.tcp_wmem=4096	262144	16777216
net.ipv4.tcp_mem=131072	262144	524288
net.ipv4.udp_rmem_min=16384
net.ipv4.udp_wmem_min=16384
net.ipv4.ip_local_port_range=1024	65535
net.ipv4.ip_local_reserved_ports=8080-8081,8443-8445,9000-9005
net.ipv4.tcp_tw_reuse=1
net.ipv4.tcp_fin_timeout=60
net.ipv4.tcp_congestion_control=cubic
net.core.default_qdisc=fq_codel
net.ipv4.tcp_slow_start_after_idle=1
net.ipv4.tcp_autocorking=1
net.ipv4.tcp_syncookies=1
net.ipv4.tcp_timestamps=1
net.ipv4.tcp_sack=1
net.ipv4.tcp_window_scaling=1
net.netfilter.nf_conntrack_max=2000500
fs.file-max=2000500
fs.nr_open=2000500
vm.swappiness=60
vm.overcommit_memory=1
kernel.pid_max=4194304
'

# The CPU and memory settings, as sysfs files: every CPU's frequency
# governor and energy preference (amd-pstate-epp on the benchmark host), the
# frequency boost, and transparent huge pages.
governor=powersave
epp=balance_performance
boost=1
thp=madvise

if [ "$(id -u)" != 0 ] && [ "$mode" != check ]; then
	echo "$0: run as root" >&2
	exit 1
fi

# show prints one setting's state: what the host has, and what this script
# wants when they differ.
show() {
	if [ "$2" = "$3" ]; then
		printf 'same     %s = %s\n' "$1" "$2"
	else
		printf 'differs  %s = %s, wants %s\n' "$1" "$2" "$3"
	fi
}

# The keys with several numbers are compared with their whitespace folded,
# since sysctl prints tabs and a file may hold spaces.
fold() { printf '%s' "$1" | tr -s ' \t' '  '; }

echo "$settings" | while IFS= read -r line; do
	[ -n "$line" ] || continue
	key=${line%%=*}
	want=${line#*=}
	if ! have=$(sysctl -n "$key" 2>/dev/null); then
		printf 'missing  %s (not in this kernel, or its module not loaded)\n' "$key"
		continue
	fi
	if [ "$mode" = check ]; then
		show "$key" "$(fold "$have")" "$(fold "$want")"
	elif [ "$(fold "$have")" != "$(fold "$want")" ]; then
		sysctl -q -w "$key=$want"
		printf 'set      %s = %s (was %s)\n' "$key" "$(fold "$want")" "$(fold "$have")"
	fi
done

# sysfs writes one setting to every file the glob matches, if any exists.
sysfs() {
	name=$1 want=$2
	shift 2
	found=0
	for f in "$@"; do
		[ -f "$f" ] || continue
		found=1
		have=$(cat "$f")
		# THP's file lists every choice, the one in force bracketed.
		case "$have" in *"[$want]"*) have=$want ;; *"["*) have=$(printf '%s' "$have" | sed 's/.*\[\(.*\)\].*/\1/') ;; esac
		if [ "$mode" = check ]; then
			show "$f" "$have" "$want"
		elif [ "$have" != "$want" ]; then
			echo "$want" > "$f"
			printf 'set      %s = %s (was %s)\n' "$f" "$want" "$have"
		fi
	done
	[ "$found" = 1 ] || printf 'missing  %s (this kernel has no such file)\n' "$name"
}

sysfs scaling_governor "$governor" /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor
sysfs energy_performance_preference "$epp" /sys/devices/system/cpu/cpu*/cpufreq/energy_performance_preference
sysfs boost "$boost" /sys/devices/system/cpu/cpufreq/boost
sysfs transparent_hugepage "$thp" /sys/kernel/mm/transparent_hugepage/enabled

if [ "$mode" = persist ]; then
	file=/etc/sysctl.d/98-fitbench-plan-2026-10-30.conf
	{
		echo "# The kernel settings of fit's 2026-10-30 load test plan;"
		echo "# written by bench/results/plan-2026-10-30/host-settings.sh."
		echo "$settings" | while IFS= read -r line; do
			[ -n "$line" ] || continue
			printf '%s = %s\n' "${line%%=*}" "$(fold "${line#*=}")"
		done
	} > "$file"
	echo "wrote $file"
fi
