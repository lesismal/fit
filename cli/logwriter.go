package cli

import (
	"bytes"
	"io"
	"log/slog"
	"strings"

	"github.com/lesismal/fit/logs"
)

// logWriter is an io.Writer that writes each line written to it as a
// message of log's at level, for what serve prints with fmt once the log is
// open, without its own "fit: " or "fit run: ".
type logWriter struct {
	log   *logs.Logger
	level slog.Level
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(bytes.TrimRight(p, "\n"), []byte("\n")) {
		msg := string(line)
		if m, ok := strings.CutPrefix(msg, "fit run: "); ok {
			msg = m
		} else if m, ok := strings.CutPrefix(msg, "fit: "); ok {
			msg = m
		}
		w.log.Msg(w.level, msg)
	}
	return len(p), nil
}

// warnWriter returns w, a log's writer, writing at warn instead, or w as it
// is when it is not a log's.
func warnWriter(w io.Writer) io.Writer {
	if lw, ok := w.(logWriter); ok {
		lw.level = logs.LevelWarn
		return lw
	}
	return w
}
