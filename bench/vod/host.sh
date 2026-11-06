#!/bin/bash
# Runs on the benchmark host, from bench/vod.sh: the vod handler's segments a
# second, and its memory, against nginx serving the same files cut beforehand
# into HLS with ffmpeg. DIR holds fit (linux/amd64), segments.lua and
# nginx.conf, and gets the library, the cut files and the results.
#
#   host.sh DIR
#
# The library is FILMS films of SECS seconds, each in three renditions (1080p
# at 5 Mb/s, 720p at 2.5 Mb/s and 360p at 800 kb/s, H.264 with a keyframe
# every 2s and AAC at 128 kb/s), made once with the host's ffmpeg. nginx
# serves them cut with ffmpeg's hls muxer into fMP4 segments of 4s; fit
# serves the files themselves through vod, segment_duration 4s, in two
# setups: hot, its cache larger than every segment, so that each is muxed
# once, and cold, its cache of COLD_CACHE bytes (64 MiB), so that nearly
# every request reads and muxes its segment; what is being built holds the
# cache too, so that a request finding it full of builds is answered 503,
# which the summary counts apart. wrk asks for every segment of every rendition at
# random, CONNS connections, ROUNDS rounds of DUR seconds each, alternating
# the servers, after a warm-up that asks for each segment once. The server
# runs pinned to SERVER_CPUS, wrk to CLIENT_CPUS, both on the host's network.
# The servers' memory is their processes' resident set (VmRSS, VmHWM), read
# from /proc every second.
#
# Wrap it in flock /root/bench.lock: another session may use the host.
set -euo pipefail

dir=$(cd "$1" && pwd)
cd "$dir"
FILMS=${FILMS:-4}
SECS=${SECS:-120}
DUR=${DUR:-15}
ROUNDS=${ROUNDS:-3}
CONNS=${CONNS:-64}
SERVER_CPUS=${SERVER_CPUS:-0-3}
CLIENT_CPUS=${CLIENT_CPUS:-8-15}
NGINX_IMAGE=${NGINX_IMAGE:-nginx:1.31.4}
FIT_IMAGE=${FIT_IMAGE:-ubuntu:24.04}
LOADGEN_IMAGE=${LOADGEN_IMAGE:-fitbench-loadgen:latest}
workers=$(( $(echo "$SERVER_CPUS" | awk -F- '{print ($2==""?1:$2-$1+1)}') ))
out=$dir/results
mkdir -p "$out" lib/films cut

# The library, once.
if [ ! -f lib/.done-$FILMS-$SECS ]; then
	rm -rf lib cut && mkdir -p lib/films cut
	for i in $(seq 1 "$FILMS"); do
		for r in "1920x1080 5000k 1080p" "1280x720 2500k 720p" "640x360 800k 360p"; do
			set -- $r
			ffmpeg -hide_banner -loglevel error -y -nostdin -f lavfi -i "testsrc2=size=$1:rate=25" \
				-f lavfi -i "sine=frequency=$((300 + 40 * i)):sample_rate=48000" -t "$SECS" \
				-c:v libx264 -preset ultrafast -b:v "$2" -maxrate "$2" -bufsize "$2" -g 50 -keyint_min 50 -sc_threshold 0 \
				-pix_fmt yuv420p -c:a aac -b:a 128k -ac 2 "lib/films/film$i.$3.mp4"
		done
	done
	touch "lib/.done-$FILMS-$SECS"
fi
# The same files cut beforehand, for nginx.
if [ ! -f cut/.done-$FILMS-$SECS ]; then
	for f in lib/films/*.mp4; do
		b=$(basename "$f" .mp4)
		d=cut/films/${b%%.*}/${b#*.}
		mkdir -p "$d"
		ffmpeg -hide_banner -loglevel error -y -nostdin -i "$f" -c copy -f hls -hls_time 4 -hls_playlist_type vod \
			-hls_segment_type fmp4 -hls_fmp4_init_filename init.mp4 -hls_segment_filename "$d/%d.m4s" "$d/index.m3u8"
	done
	touch "cut/.done-$FILMS-$SECS"
fi
(cd cut && find . -name '*.m4s' | sed 's|^\.||' | sort) >urls-nginx.txt
du -sh lib cut | tee "$out/sizes.txt"

stop() { docker rm -f vodb-nginx vodb-fit >/dev/null 2>&1 || true; }
trap stop EXIT
stop

# fit's config of a setup: the cache's bound.
fitconf() {
	cat >"$dir/fit-$1.json" <<EOF
{"listeners": [{"name": "web", "address": "127.0.0.1:18091"}],
 "media_library": [{"name": "films", "roots": ["/w/lib"]}],
 "routes": [{"match": {"path": ["/vod/*"]}, "handler": {"module": "vod", "params": {"library": "films", "segment_duration": "4s", "max_cache_bytes": $2}}}]}
EOF
}
fitconf hot $((4 << 30))
fitconf cold "${COLD_CACHE:-$((64 << 20))}"
sed "s/{{WORKERS}}/$workers/" nginx.conf >nginx-run.conf

start() {
	case $1 in
	nginx)
		docker run -d --name vodb-nginx --network host --cpuset-cpus "$SERVER_CPUS" \
			-v "$dir/nginx-run.conf:/etc/nginx/nginx.conf:ro" -v "$dir/cut:/srv/cut:ro" "$NGINX_IMAGE" >/dev/null
		port=18090
		;;
	fit-*)
		docker run -d --name vodb-fit --network host --cpuset-cpus "$SERVER_CPUS" -v "$dir:/w" \
			"$FIT_IMAGE" /w/fit run -c "/w/fit-${1#fit-}.json" >/dev/null
		port=18091
		;;
	esac
	for _ in $(seq 1 100); do
		# Any answer: it listens.
		curl -s -o /dev/null "http://127.0.0.1:$port/" && break
		sleep 0.1
	done
}

# fit's segment URLs, from its playlists, once its scan has ended.
fiturls() {
	: >urls-fit.txt
	for item in $(ls lib/films | sed 's/\..*//' | sort -u); do
		base=http://127.0.0.1:18091/vod/films/films/$item
		for _ in $(seq 1 100); do
			curl -sf -o /dev/null "$base/index.m3u8" && break
			sleep 0.2
		done
		for v in $(curl -sf "$base/index.m3u8" | grep -v '^#'); do
			curl -sf "$base/$v" | grep -v '^#' | sed "s|^|/vod/films/films/$item/|" >>urls-fit.txt
		done
	done
}

# The resident memory of the container's processes, every second, the
# largest seen and the last.
sample() {
	local ctr=$1 file=$2
	: >"$file"
	while docker inspect "$ctr" >/dev/null 2>&1; do
		local rss=0 hwm=0
		for pid in $(docker top "$ctr" -eo pid | tail -n +2); do
			r=$(awk '/VmRSS/ {print $2}' "/proc/$pid/status" 2>/dev/null || echo 0)
			h=$(awk '/VmHWM/ {print $2}' "/proc/$pid/status" 2>/dev/null || echo 0)
			rss=$((rss + ${r:-0}))
			hwm=$((hwm + ${h:-0}))
		done
		echo "$rss $hwm" >>"$file"
		sleep 1
	done
}

wrkrun() {
	docker run --rm --network host --cpuset-cpus "$CLIENT_CPUS" -v "$dir:/vod" "$LOADGEN_IMAGE" \
		wrk -t8 -c"$CONNS" -d"$2" --latency -s /vod/segments.lua "http://127.0.0.1:$1" -- "/vod/$3"
}

for round in $(seq 1 "$ROUNDS"); do
	for server in nginx fit-hot fit-cold; do
		start "$server"
		ctr=vodb-nginx
		urls=urls-nginx.txt
		if [ "$server" != nginx ]; then
			ctr=vodb-fit
			urls=urls-fit.txt
			[ -s urls-fit.txt ] || fiturls
		fi
		sample "$ctr" "$out/mem-$server-$round.txt" &
		sampler=$!
		# Warm-up: every segment once, as the page cache and fit's cache
		# would have them in a while.
		wrkrun "$port" 5s "$urls" >"$out/warm-$server-$round.txt" 2>&1 || true
		wrkrun "$port" "${DUR}s" "$urls" >"$out/run-$server-$round.txt" 2>&1
		sleep 1
		docker rm -f "$ctr" >/dev/null
		wait "$sampler" || true
		echo "$server round $round: $(grep -o '"requests":[0-9]*' "$out/run-$server-$round.txt") $(grep 'Transfer/sec' "$out/run-$server-$round.txt")"
	done
done
wc -l urls-nginx.txt urls-fit.txt >"$out/urls.txt"
