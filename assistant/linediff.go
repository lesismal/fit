package assistant

import "github.com/lesismal/fit/internal/linediff"

// unifiedDiff returns the unified diff of a and b, texts, labelled from and
// to; "" when they are the same (package linediff).
func unifiedDiff(from, to, a, b string) string { return linediff.Unified(from, to, a, b) }
