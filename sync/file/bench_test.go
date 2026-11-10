package syncfile

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// BenchmarkRead reads a file of 128-byte lines once, as the sync
// benchmark's files scenarios do, its records dropped.
func BenchmarkRead(b *testing.B) {
	for _, codec := range []string{"", "json"} {
		name := "line"
		if codec != "" {
			name = codec
		}
		b.Run(name, func(b *testing.B) {
			const lines = 100_000
			dir := b.TempDir()
			var sb strings.Builder
			for i := range lines {
				l := strconv.Itoa(1_000_000_000_000_000 + i)
				if codec == "json" {
					sb.WriteString(`{"n":"` + l + `","msg":"` + strings.Repeat("a", 98) + `"}` + "\n")
				} else {
					sb.WriteString(strings.Repeat("a", 108) + l + "\n")
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "0.log"), []byte(sb.String()), 0o644); err != nil {
				b.Fatal(err)
			}
			params := `{"paths": [` + jsonString(filepath.Join(dir, "*.log")) + `]`
			if codec != "" {
				params += `, "codec": {"module": "` + codec + `"}`
			}
			params += `}`
			src := `{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate", "params": ` + params + `}, "sinks": [{"name": "s", "sink": {"module": "memory"}}]}]}}`
			c, err := config.Load([]byte(src))
			if err != nil {
				b.Fatal(err)
			}
			v, err := module.Load(fitsync.Sources, config.ModuleRef{Module: "file", Params: c.Sync.Pipelines[0].Source.Params}, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer module.Cleanup(v)
			s := v.(*Source)
			n := 0
			b.ReportAllocs()
			b.SetBytes(int64(sb.Len()))
			for b.Loop() {
				if err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { n++; return nil }}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n), "ns/line")
		})
	}
}
