#!/bin/sh
# Writes the library the vod interop checks play into DIR, with the ffmpeg
# on the PATH (libx264, libx265, libsvtav1 or libaom-av1, and libopus): a
# film in two renditions with a French audio track and English subtitles,
# clips of each codec pair the vod handler serves, one whose keyframes do not
# fall on the segment duration, and an item of audio alone. Pictures are
# ffmpeg's test patterns, sound its sine tones; the films carry B-frames and
# AAC's priming, so that their edit lists matter.
#
#   test/docker/vod/genlib.sh DIR [SECONDS]
#
# SECONDS (20 by default) is how long the film is; the clips last 12s.
set -eu

dir=$1
secs=${2:-20}
mkdir -p "$dir/films" "$dir/clips" "$dir/music"
q="-hide_banner -loglevel error -y -nostdin"
pat() { echo "-f lavfi -i testsrc2=size=$1:rate=$2 -f lavfi -i sine=frequency=440:sample_rate=48000:beep_factor=4"; }
x264="-c:v libx264 -preset veryfast -bf 3 -pix_fmt yuv420p -sc_threshold 0"
aac="-c:a aac -b:a 96k -ac 2"
av1enc=libsvtav1
ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libsvtav1 || av1enc=libaom-av1
export SVT_LOG=1

# The film: 360p with its moov at the end, as ffmpeg writes by default, and
# 720p with it at the front; keyframes every 2s.
ffmpeg $q $(pat 640x360 25) -t "$secs" $x264 -b:v 600k -g 50 -keyint_min 50 $aac \
	-metadata:s:a:0 language=eng "$dir/films/bbb.mp4"
ffmpeg $q $(pat 1280x720 25) -t "$secs" $x264 -b:v 1500k -g 50 -keyint_min 50 $aac \
	-metadata:s:a:0 language=eng -movflags +faststart "$dir/films/bbb.720p.mp4"
ffmpeg $q -f lavfi -i sine=frequency=660:sample_rate=48000:beep_factor=4 -t "$secs" $aac \
	-metadata:s:a:0 language=fra "$dir/films/bbb.audio-fr.m4a"
printf '1\n00:00:01,000 --> 00:00:03,000\nHello\n\n2\n00:00:05,000 --> 00:00:08,000\nWorld\n' >"$dir/films/bbb.en.srt"

# Clips of 12s: H.265 with AAC at 44.1 kHz, AV1 with Opus, H.264 with Opus,
# and H.264 whose keyframes come every 3s, cut at 4s.
ffmpeg $q $(pat 640x360 30) -t 12 -c:v libx265 -preset veryfast -b:v 500k \
	-x265-params keyint=60:min-keyint=60:scenecut=0:bframes=3:log-level=error -tag:v hvc1 -pix_fmt yuv420p \
	-c:a aac -b:a 64k -ar 44100 "$dir/clips/hevc.mp4"
if [ "$av1enc" = libsvtav1 ]; then
	av1="-c:v libsvtav1 -preset 10 -crf 45 -g 50 -svtav1-params scd=0"
else
	av1="-c:v libaom-av1 -cpu-used 8 -crf 50 -g 50 -keyint_min 50 -row-mt 1"
fi
ffmpeg $q $(pat 640x360 25) -t 12 $av1 -pix_fmt yuv420p -c:a libopus -b:a 64k "$dir/clips/av1.mp4"
ffmpeg $q $(pat 640x360 25) -t 12 $x264 -b:v 600k -g 50 -keyint_min 50 -c:a libopus -b:a 64k "$dir/clips/opus.mp4"
ffmpeg $q $(pat 640x360 25) -t 12 $x264 -b:v 600k -g 75 -keyint_min 75 $aac "$dir/clips/gop3s.mp4"

# Audio alone.
ffmpeg $q -f lavfi -i sine=frequency=330:sample_rate=44100:beep_factor=2 -t 15 -c:a aac -b:a 128k -ac 2 "$dir/music/song.m4a"
