package logs

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestRing: the log keeps its latest messages in a bounded ring, by logger
// and level, with the attributes they carry, each cut to its bound; what a
// level filters out is not kept.
func TestRing(t *testing.T) {
	r, err := New(&config.Log{Level: config.LevelInfo}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	srv, tls := r.Logger(config.LoggerServer), r.Logger(config.LoggerTLS)
	srv.Debugf("not kept")
	srv.Infof("listening on %s", ":8080")
	tls.Warnf("certificate for %s expires soon", "example.com")
	srv.Errorf("upstream api: connection refused")
	slog.New(r.Handler(config.LoggerEngine)).With("conn", 7).WithGroup("g").Info("engine says", "n", 3)
	srv.Infof("%s", strings.Repeat("é", RingMessageBytes))

	all := r.Recent(Filter{})
	if len(all) != 5 {
		t.Fatalf("%d records: %+v", len(all), all)
	}
	if all[0].Message != "listening on :8080" || all[0].Logger != "server" || all[0].Level != "info" || all[0].Time.IsZero() {
		t.Errorf("%+v", all[0])
	}
	if all[3].Logger != "engine" || all[3].Message != "engine says conn=7 n=3" {
		t.Errorf("%+v", all[3])
	}
	if m := all[4].Message; len(m) > RingMessageBytes || !strings.HasPrefix(m, "éé") || strings.ContainsRune(m, '�') {
		t.Errorf("a long message is kept as %d bytes", len(m))
	}
	if got := r.Recent(Filter{Level: "warn"}); len(got) != 2 || got[0].Level != "warn" || got[1].Level != "error" {
		t.Errorf("warn and up: %+v", got)
	}
	if got := r.Recent(Filter{Logger: "tls"}); len(got) != 1 || !strings.Contains(got[0].Message, "example.com") {
		t.Errorf("tls: %+v", got)
	}
	if got := r.Recent(Filter{Contains: "REFUSED"}); len(got) != 1 || got[0].Level != "error" {
		t.Errorf("contains: %+v", got)
	}
	if got := r.Recent(Filter{Limit: 2}); len(got) != 2 || got[1].Message != all[4].Message {
		t.Errorf("the latest 2: %+v", got)
	}

	// The ring holds RingSize, the latest.
	for i := range RingSize + 10 {
		srv.Infof("line %d", i)
	}
	got := r.Recent(Filter{Limit: RingSize * 2})
	if len(got) != RingSize || got[len(got)-1].Message != "line 1009" || got[0].Message != "line 10" {
		t.Errorf("%d records, %q to %q", len(got), got[0].Message, got[len(got)-1].Message)
	}
	if levelName(LevelDebug) != "debug" || levelRank("debug") != 0 || clipUTF8("abc", 5) != "abc" {
		t.Error("helpers")
	}
	var b strings.Builder
	appendAttr(&b, slog.Attr{})
	if b.Len() != 0 {
		t.Error("an empty attribute is written")
	}
}
