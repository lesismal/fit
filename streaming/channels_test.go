package streaming

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/flv"
	"github.com/lesismal/fib/media/hub"
	"github.com/lesismal/fib/media/mpegts"
	"github.com/lesismal/fib/media/rtc"
	"github.com/lesismal/fib/media/rtmp"
	"github.com/lesismal/fib/media/rtsp"
	"github.com/lesismal/fib/media/srt"

	"github.com/lesismal/fit/streaming/channels"
)

// timeline records the packets a player gets: their DTS by track, and how
// many times the tracks were set.
type timeline struct {
	mu     sync.Mutex
	tracks int
	dts    map[int][]time.Duration
	codec  map[int]av.CodecID
	closed bool
}

func newTimeline() *timeline {
	return &timeline{dts: map[int][]time.Duration{}, codec: map[int]av.CodecID{}}
}

func (tl *timeline) OnTracks(tracks []*av.Track) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.tracks++
	for i, t := range tracks {
		tl.codec[i] = t.Codec
	}
}

func (tl *timeline) OnPacket(p *av.Packet) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.dts[p.Track] = append(tl.dts[p.Track], p.DTS)
}

func (tl *timeline) OnClose(error) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.closed = true
}

// video returns the DTS of the video track's packets.
func (tl *timeline) video() []time.Duration {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	for i, c := range tl.codec {
		if c.IsVideo() {
			return append([]time.Duration(nil), tl.dts[i]...)
		}
	}
	return nil
}

// checkContinuous checks that dts runs on, from one packet to the next,
// never going back and never jumping more than gap, over at least span.
func checkContinuous(t *testing.T, what string, dts []time.Duration, span, gap time.Duration) {
	t.Helper()
	if len(dts) < 2 || dts[len(dts)-1]-dts[0] < span {
		t.Fatalf("%s: %d packets, over less than %s", what, len(dts), span)
	}
	for i := 1; i < len(dts); i++ {
		if d := dts[i] - dts[i-1]; d <= 0 || d > gap {
			t.Fatalf("%s: DTS %s after %s, packet %d of %d", what, dts[i], dts[i-1], i, len(dts))
		}
	}
}

// channelDoc is a config with the library of medialib's test files, the
// policy live, a channel live/tv of items, every listener and every handler.
func channelDoc(t *testing.T, channel string) string {
	root, err := filepath.Abs("../medialib/testdata/lib")
	if err != nil {
		t.Fatal(err)
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": [` + livePolicy(t) + `],
  "media_library": [{"name": "lib", "roots": [` + fmt.Sprintf("%q", root) + `]}],
  "streaming": {
    "rtmp": {"listen": "127.0.0.1:0", "auth": "live"},
    "rtsp": {"listen": "127.0.0.1:0", "auth": "live"},
    "srt": {"listen": "127.0.0.1:0", "auth": "live"},
    "webrtc": {"listen": "127.0.0.1:0"},
    "channels": [` + channel + `]
  },
  "routes": [
    {"match": {"path": ["/flv/*"]}, "handler": {"module": "httpflv", "params": {"prefix": "/flv/", "auth": "live"}}},
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "live", "target_duration": "1s"}}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "stream_whep", "params": {"auth": "live"}}}
  ]
}`
}

// TestChannelEgresses plays a channel of an item played again and again,
// live/tv, over RTMP, RTSP, SRT, HTTP-FLV, HLS and WHEP, under the role's
// auth: its tracks are set once, and every player's timestamps run on from
// one item to the next without a gap or a jump. A channel of two items
// whose tracks differ, live/mix, has its tracks set at every item, which
// HLS marks with a discontinuity.
func TestChannelEgresses(t *testing.T) {
	f := startFit(t, channelDoc(t, `{"name": "live/tv", "library": "lib", "items": ["films/bbb", "films/bbb"]},
	  {"name": "live/mix", "library": "lib", "items": ["films/bbb", "clips/late"]}`))
	e := startEngine(t)
	f.waitLive(t, "live/tv")
	f.waitLive(t, "live/mix")
	// A frame is 40ms: a gap is a frame or more missing.
	const span, gap = 2500 * time.Millisecond, 60 * time.Millisecond

	playRTMPInto := func(path string, tl *timeline) {
		ready := make(chan error, 1)
		cc, err := rtmp.Play(e, "rtmp://"+f.addr(t, "rtmp")+path+"?token="+viewToken, rtmp.ClientConfig{}, tl,
			func(_ *rtmp.ClientConn, err error) { ready <- err })
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cc.Close() })
		if err := <-ready; err != nil {
			t.Fatal(err)
		}
	}
	rtmpTL, mixTL := newTimeline(), newTimeline()
	playRTMPInto("/live/tv", rtmpTL)
	playRTMPInto("/live/mix", mixTL)
	if _, _, err := playRTMP(t, f, "/live/tv"); rtmpCode(err) != "NetStream.Play.Failed" {
		t.Errorf("an RTMP play without credentials: %v", err)
	}

	// RTSP, over TCP.
	rtspTL := newTimeline()
	rc, err := rtsp.Play(rtsp.ClientConfig{Engine: e, OnTracks: rtspTL.OnTracks, OnPacket: rtspTL.OnPacket},
		"rtsp://"+f.addr(t, "rtsp")+"/live/tv?token="+viewToken)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	// SRT.
	srtTL := newTimeline()
	ss, err := srt.Play(e, f.addr(t, "srt"), nil, "", srt.ClientConfig{StreamID: "live/tv?token=" + viewToken,
		Demuxer: mpegts.DemuxerConfig{OnTracks: srtTL.OnTracks, OnPacket: srtTL.OnPacket}})
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	// WHEP: video alone, AAC being what WebRTC cannot carry.
	var whep atomic.Int64
	viewer, err := rtc.Pull(rtc.ClientConfig{Engine: e, Token: viewToken, OnPacket: func(*av.Packet) { whep.Add(1) }}, f.base+"/whep/live/tv")
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()

	// HTTP-FLV, read for span and more.
	flvTL := newTimeline()
	flvDone := make(chan error, 1)
	go func() { flvDone <- readFLV(f.base+"/flv/live/tv.flv?token="+viewToken, span+time.Second, flvTL) }()

	// HLS: live/tv's playlist runs on, live/mix's marks each item with a
	// discontinuity.
	waitFor(t, "an HLS discontinuity", func() bool {
		resp, body := get(t, f.base+"/hls/live/mix/media.m3u8?token="+viewToken, 1<<16, nil)
		return resp.StatusCode == 200 && bytes.Contains(body, []byte("#EXT-X-DISCONTINUITY"))
	})
	waitFor(t, "HLS segments of live/tv", func() bool {
		resp, body := get(t, f.base+"/hls/live/tv/media.m3u8?token="+viewToken, 1<<16, nil)
		return resp.StatusCode == 200 && bytes.Count(body, []byte("#EXTINF")) >= 2 && !bytes.Contains(body, []byte("#EXT-X-DISCONTINUITY"))
	})
	if resp, _ := get(t, f.base+"/hls/live/tv/index.m3u8", 1<<16, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("HLS without credentials: %d", resp.StatusCode)
	}

	if err := <-flvDone; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "every player's packets", func() bool {
		return len(rtmpTL.video()) > 70 && len(rtspTL.video()) > 70 && len(srtTL.video()) > 70 && len(mixTL.video()) > 70 && whep.Load() > 50
	})
	for name, tl := range map[string]*timeline{"rtmp": rtmpTL, "flv": flvTL, "srt": srtTL} {
		checkContinuous(t, name, tl.video(), span, gap)
		tl.mu.Lock()
		n := tl.tracks
		tl.mu.Unlock()
		if n != 1 {
			t.Errorf("%s: the tracks were set %d times", name, n)
		}
	}
	// RTSP's timestamps are RTP's, from the first packet: continuous too.
	checkContinuous(t, "rtsp", rtspTL.video(), 1500*time.Millisecond, gap)
	// live/mix: the tracks set again at each item, the times running on
	// across them all the same (fib request A68).
	checkContinuous(t, "rtmp live/mix", mixTL.video(), span, gap)
	if mixTL.tracks < 2 {
		t.Errorf("live/mix: the tracks were set %d times", mixTL.tracks)
	}

	st := channels.Lookup(f.up).Channel("live/tv").Status()
	if st.State != channels.StatePlaying || st.Played < 3 || st.Failed != 0 {
		t.Errorf("%+v", st)
	}
}

// readFLV reads an HTTP-FLV stream for d into tl.
func readFLV(url string, d time.Duration, tl *timeline) error {
	client := &http.Client{Timeout: d}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP-FLV: %d", resp.StatusCode)
	}
	dec := flv.NewDecoder(flv.DecoderConfig{OnTracks: tl.OnTracks, OnPacket: tl.OnPacket})
	dmx := flv.NewDemuxer(flv.DemuxerConfig{})
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if ferr := dmx.Feed(buf[:n], func(tag flv.Tag) error { return dec.WriteTag(tag.Type, tag.Timestamp, tag.Data) }); ferr != nil {
				return ferr
			}
		}
		if err != nil {
			if err == io.EOF || strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "deadline") {
				return nil
			}
			return err
		}
	}
}

// pushTarget is a fit that takes streams over RTMP and SRT on fixed ports,
// without auth, as an outside server; it can be stopped and started again.
func pushTargetDoc(rtmpAddr, srtAddr string) string {
	return `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
	  "streaming": {"rtmp": {"listen": "` + rtmpAddr + `"}, "srt": {"listen": "` + srtAddr + `"}}}`
}

// freeUDP returns a UDP address free now.
func freeUDP(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().String()
}

// TestChannelPush relays a channel to fit itself and to another fit over
// RTMP and SRT, and once that one restarts, again.
func TestChannelPush(t *testing.T) {
	rtmpAddr, srtAddr, srcAddr := freePort(t), freeUDP(t), freePort(t)
	target := startFit(t, pushTargetDoc(rtmpAddr, srtAddr))
	root, _ := filepath.Abs("../medialib/testdata/lib")
	srcDoc := func(retryMax string) string {
		return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_library": [{"name": "lib", "roots": [` + fmt.Sprintf("%q", root) + `]}],
  "streaming": {
    "rtmp": {"listen": "` + srcAddr + `"},
    "channels": [{"name": "live/tv", "library": "lib", "items": ["films/bbb", "sintel"],
      "push": [{"name": "to-target", "url": "rtmp://` + rtmpAddr + `/relay/tv", "retry_min": "100ms", "retry_max": "` + retryMax + `"},
               {"url": "srt://` + srtAddr + `?streamid=%23!::r=relay/srt,m=publish&latency=50", "retry_min": "100ms", "retry_max": "500ms"}]}],
    "push": [{"name": "self", "stream": "live/tv", "url": "rtmp://` + srcAddr + `/relay/self", "retry_min": "100ms", "retry_max": "200ms"}]
  }
}`
	}
	src := startFit(t, srcDoc("500ms"))
	m := channels.Lookup(src.up)
	live := func(f *fit, path string) func() bool {
		return func() bool {
			h := f.role().Hub()
			if h == nil {
				return false
			}
			st, ok := h.Stream(path)
			return ok && st.Publishing && st.PacketsIn > 20
		}
	}
	waitFor(t, "the push to fit itself", live(src, "relay/self"))
	waitFor(t, "the RTMP push to the target", live(target, "relay/tv"))
	waitFor(t, "the SRT push to the target", live(target, "relay/srt"))
	for _, p := range m.Pushes() {
		// Never the stream's name, nor SRT's stream ID: a CDN's key.
		if p.State != channels.PushConnected || p.Connects == 0 || p.Bytes == 0 || strings.Contains(p.URL, "/tv") ||
			strings.Contains(p.URL, "self") || strings.Contains(p.URL, "streamid") {
			t.Errorf("%+v", p)
		}
	}
	// The stream's targets, named in order when they have no name: self,
	// to-target, and the channel's SRT target, its third.
	if ps := m.Channel("live/tv").Status().Push; len(ps) != 3 || ps[0].Name != "live/tv#3" || ps[0].Protocol != "srt" {
		t.Errorf("the channel's push targets: %+v", ps)
	}
	// A reload that changes only a target's retries keeps it connected.
	src.reload(t, srcDoc("400ms"))
	if channels.Lookup(src.up) != m {
		t.Fatal("the reload made the channels again")
	}
	if st := m.Push("to-target").Status(); st.State != channels.PushConnected || st.Connects != 1 {
		t.Errorf("after a reload: %+v", st)
	}

	// The target restarts: the pushes retry, and get the stream back to it.
	target.stop()
	waitFor(t, "the pushes to notice", func() bool {
		st := m.Push("to-target").Status()
		return st.State == channels.PushRetrying && st.Failures > 0
	})
	target2 := startFit(t, pushTargetDoc(rtmpAddr, srtAddr))
	waitFor(t, "the RTMP push after the restart", live(target2, "relay/tv"))
	waitFor(t, "the SRT push after the restart", live(target2, "relay/srt"))

	// A play of what was pushed, through the target.
	tl := newTimeline()
	e := startEngine(t)
	ready := make(chan error, 1)
	cc, err := rtmp.Play(e, "rtmp://"+rtmpAddr+"/relay/tv", rtmp.ClientConfig{}, tl, func(_ *rtmp.ClientConn, err error) { ready <- err })
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the relayed stream", func() bool { return len(tl.video()) > 40 })

	// Stopped from the Admin API, a push lets go of its target's stream.
	m.Push("to-target").Stop()
	waitFor(t, "the stopped push to end", func() bool {
		st, ok := target2.role().Hub().Stream("relay/tv")
		return !ok || !st.Publishing
	})
	if st := m.Push("to-target").Status(); st.State != channels.PushStopped {
		t.Errorf("%+v", st)
	}
	m.Push("to-target").Restart()
	waitFor(t, "the restarted push", live(target2, "relay/tv"))
	_ = hub.ErrNotFound
}
