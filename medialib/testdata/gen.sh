#!/bin/sh
# Writes the library the tests scan, lib/, and what ffprobe says of each of
# its media files, lib.probe. Five files are fib's (media/mp4/testdata, a
# second each of a 64x48 test pattern at 25 fps and a mono 48 kHz tone,
# written by ffmpeg 7.1); the 360p rendition and the French audio are made
# here with the ffmpeg on the PATH. Run from medialib/testdata, with FIB the
# path of a fib checkout at the commit fit's go.mod names:
#
#   FIB=../../../fib ./gen.sh
#
# lib.probe has a line for each media file, "file|<path>|<format duration>",
# and one for each of its streams, "stream|<path>|codec_name=...|...", its
# codec, picture size or sample rate and channels, start_time and duration,
# as ffprobe writes them.
set -eu

FIB=${FIB:-../../../fib}
src=$FIB/media/mp4/testdata
rm -rf lib
mkdir -p lib/films/subs lib/films/art lib/clips
cp "$src/h264_aac.mp4" lib/films/bbb.mp4
cp "$src/h265_aac.mp4" lib/films/sintel-hd.mp4
cp "$src/h264_opus.mp4" lib/films/sintel-sd.mp4
cp "$src/av1_opus.mp4" "lib/clips/Av1 Opus.mp4"
cp "$src/h264_aac_delay.mp4" lib/clips/late.mp4

ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=128x96:rate=25 -f lavfi -i sine=frequency=440:sample_rate=48000 \
	-t 1 -pix_fmt yuv420p -c:v libx264 -preset veryfast -crf 45 -bf 2 -g 10 -c:a aac -b:a 24k -ac 1 -movflags +faststart lib/films/bbb.360p.mp4
ffmpeg -hide_banner -loglevel error -y -f lavfi -i sine=frequency=660:sample_rate=48000 \
	-t 1 -c:a aac -b:a 24k -ac 1 -metadata:s:a:0 language=fra lib/films/bbb.audio-fr.m4a
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=blue:size=8x8 -frames:v 1 lib/films/bbb.jpg
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=red:size=8x8 -frames:v 1 lib/films/art/sintel.png
# An MP4 file of a codec the library does not serve, outside lib/.
mkdir -p odd
ffmpeg -hide_banner -loglevel error -y -f lavfi -i color=c=green:size=8x8:rate=1 -frames:v 1 -c:v mjpeg odd/mjpeg.mp4
# Not an MP4 file: the start of one, cut short before its moov.
head -c 200 "$src/h264_aac.mp4" >lib/broken.mp4

printf '1\r\n00:00:00,100 --> 00:00:00,900\r\nHello\r\n\r\n2\r\n00:00:01,000 --> 00:00:02,5 X1:10\r\nWorld --> on\r\n' >lib/films/bbb.en.srt
printf 'WEBVTT\n\n00:00.100 --> 00:00.900\nBonjour\n' >lib/films/bbb.fr.forced.vtt
printf '1\n00:00:00,000 --> 00:00:01,000\n你好\n' >lib/films/subs/sintel.zh.srt
printf 'just notes\n' >lib/notes.txt
cat >lib/films/sintel.json <<'EOF'
{
  "id": "sintel",
  "title": "Sintel",
  "poster": "art/sintel.png",
  "renditions": [
    {"file": "sintel-hd.mp4", "name": "hd"},
    {"file": "sintel-sd.mp4", "name": "sd"}
  ],
  "subtitles": [{"file": "subs/sintel.zh.srt", "name": "中文", "language": "zh", "default": true}]
}
EOF

: >lib.probe
find lib -name '*.mp4' -o -name '*.m4a' | sort | while read -r f; do
	case $f in lib/broken.mp4) continue ;; esac
	rel=${f#lib/}
	printf 'file|%s|%s\n' "$rel" "$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$f")" >>lib.probe
	ffprobe -v error -show_entries stream=codec_name,width,height,sample_rate,channels,start_time,duration -of compact=p=0 "$f" |
		sed "s#^#stream|$rel|#" >>lib.probe
done
ls -l lib lib/*
