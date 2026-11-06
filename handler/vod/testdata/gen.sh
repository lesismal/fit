#!/bin/sh
# Writes the library handler/vod's tests serve, lib/, with the ffmpeg on the
# PATH (7.1 or later, with libx264, libx265, libsvtav1 and libopus). Each
# file is a few seconds of a small test pattern and a tone, keyframes every
# second unless said, so that its segments are few and small:
#
#   films/movie.mp4           6s H.264 64x48 25 fps with B-frames, AAC mono 48 kHz, moov at the end
#   films/movie.360p.mp4      6s H.264 128x96, AAC, moov at the front: a second rendition
#   films/movie.audio-fr.m4a  6s AAC, French: the item's other audio
#   films/movie.en.srt        English subtitles
#   clips/hevc.mp4            4s H.265 (hvc1) and AAC
#   clips/av1.mp4             4s AV1 and Opus, which MPEG-TS does not carry
#   clips/longgop.mp4         6s H.264 with one keyframe, and AAC
#   music/song.m4a            5s AAC stereo 44.1 kHz: an item of audio alone
#   clips/silent.mp4          2s H.264 without audio
#   music/old.mp4             2s MP3, which the library lists and HLS does not carry here
#
# Run from handler/vod/testdata: ./gen.sh
set -eu

rm -rf lib
mkdir -p lib/films lib/clips lib/music
q="-hide_banner -loglevel error -y"
x264="-c:v libx264 -preset veryfast -crf 45 -bf 2 -g 25 -keyint_min 25 -sc_threshold 0 -pix_fmt yuv420p"
aac="-c:a aac -b:a 24k -ac 1"

ffmpeg $q -f lavfi -i testsrc2=size=64x48:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 \
	-t 6 $x264 $aac -metadata:s:a:0 language=eng lib/films/movie.mp4
ffmpeg $q -f lavfi -i testsrc2=size=128x96:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 \
	-t 6 $x264 $aac -metadata:s:a:0 language=eng -movflags +faststart lib/films/movie.360p.mp4
ffmpeg $q -f lavfi -i sine=frequency=660:sample_rate=48000 -t 6 $aac -metadata:s:a:0 language=fra lib/films/movie.audio-fr.m4a
ffmpeg $q -f lavfi -i testsrc2=size=64x48:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 \
	-t 4 -c:v libx265 -preset veryfast -crf 45 -x265-params keyint=25:min-keyint=25:scenecut=0:bframes=2:log-level=error \
	-tag:v hvc1 -pix_fmt yuv420p $aac lib/clips/hevc.mp4
SVT_LOG=1 ffmpeg $q -f lavfi -i testsrc2=size=64x48:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 \
	-t 4 -c:v libsvtav1 -preset 12 -crf 60 -g 25 -svtav1-params scd=0 -pix_fmt yuv420p \
	-c:a libopus -b:a 24k -ac 1 lib/clips/av1.mp4
ffmpeg $q -f lavfi -i testsrc2=size=64x48:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 \
	-t 6 -c:v libx264 -preset veryfast -crf 45 -bf 2 -g 1000 -keyint_min 1000 -sc_threshold 0 -pix_fmt yuv420p \
	$aac lib/clips/longgop.mp4
ffmpeg $q -f lavfi -i sine=frequency=330:sample_rate=44100 -t 5 -c:a aac -b:a 32k -ac 2 lib/music/song.m4a
ffmpeg $q -f lavfi -i testsrc2=size=64x48:rate=25 -t 2 $x264 lib/clips/silent.mp4
ffmpeg $q -f lavfi -i sine=frequency=330:sample_rate=44100 -t 2 -c:a libmp3lame -b:a 32k -ac 1 lib/music/old.mp4
printf '1\n00:00:00,500 --> 00:00:02,000\nHello\n\n2\n00:00:03,000 --> 00:00:05,000\nWorld\n' >lib/films/movie.en.srt
ls -l lib/*
