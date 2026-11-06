package assistant

import (
	"fmt"
	"strings"
)

// diffContext is the unchanged lines shown around a change.
const diffContext = 3

// maxDiffEdits bounds the edits the diff looks for, past the lines both
// texts begin and end with: past it, what lies between is shown as removed
// and added whole, rather than the work growing with it. A variable for
// tests to lower.
var maxDiffEdits = 1000

// diffOp is a line of a diff: ' ' kept, '-' removed, '+' added.
type diffOp struct {
	kind byte
	line string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// unifiedDiff returns the unified diff of a and b, texts, labelled from and
// to; "" when they are the same.
func unifiedDiff(from, to, a, b string) string {
	if a == b {
		return ""
	}
	ops := diffLines(splitLines(a), splitLines(b))
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
	// Each hunk runs from diffContext lines before a change to diffContext
	// after the last change within 2*diffContext of the one before.
	aLine, bLine := 1, 1
	pos := make([][2]int, len(ops)) // the lines of a and b each op is at
	for i, op := range ops {
		pos[i] = [2]int{aLine, bLine}
		if op.kind != '+' {
			aLine++
		}
		if op.kind != '-' {
			bLine++
		}
	}
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-diffContext)
		end := i
		for j := i; j < len(ops); j++ {
			if ops[j].kind != ' ' {
				end = j
			} else if j-end > 2*diffContext {
				break
			}
		}
		end = min(len(ops)-1, end+diffContext)
		na, nb := 0, 0
		for _, op := range ops[start : end+1] {
			if op.kind != '+' {
				na++
			}
			if op.kind != '-' {
				nb++
			}
		}
		as, bs := pos[start][0], pos[start][1]
		if na == 0 {
			as--
		}
		if nb == 0 {
			bs--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", as, na, bs, nb)
		for _, op := range ops[start : end+1] {
			out.WriteByte(op.kind)
			out.WriteString(op.line)
			out.WriteByte('\n')
		}
		i = end + 1
	}
	return out.String()
}

// diffLines returns the edits from a to b: the lines both begin and end
// with kept, and those between by Myers' algorithm, within maxDiffEdits.
func diffLines(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []diffOp
	for _, l := range a[:pre] {
		ops = append(ops, diffOp{' ', l})
	}
	ops = append(ops, myers(a[pre:len(a)-suf], b[pre:len(b)-suf])...)
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops
}

// myers returns the shortest edits from a to b, or, past maxDiffEdits, a
// removed whole and b added whole.
func myers(a, b []string) []diffOp {
	n, m := len(a), len(b)
	whole := func() []diffOp {
		ops := make([]diffOp, 0, n+m)
		for _, l := range a {
			ops = append(ops, diffOp{'-', l})
		}
		for _, l := range b {
			ops = append(ops, diffOp{'+', l})
		}
		return ops
	}
	if n == 0 || m == 0 {
		return whole()
	}
	limit := min(n+m, maxDiffEdits)
	off := limit + 1
	v := make([]int, 2*limit+3)
	// trace[d] is v's diagonals -d..d as round d began.
	var trace [][]int
	for d := 0; d <= limit; d++ {
		snap := make([]int, 2*d+1)
		copy(snap, v[off-d:off+d+1])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[off+k-1] < v[off+k+1] {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(a, b, trace, d)
			}
		}
	}
	return whole()
}

// backtrack walks trace back from the end, d edits away, into the edits.
func backtrack(a, b []string, trace [][]int, d int) []diffOp {
	x, y := len(a), len(b)
	var rev []diffOp
	for ; d >= 0; d-- {
		vd := trace[d]
		at := func(k int) int { return vd[k+d] }
		k := x - y
		var pk int
		if k == -d || k != d && at(k-1) < at(k+1) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := 0
		if d > 0 {
			px = at(pk)
		}
		py := px - pk
		for x > px && y > py {
			rev = append(rev, diffOp{' ', a[x-1]})
			x--
			y--
		}
		if d > 0 {
			if x == px {
				rev = append(rev, diffOp{'+', b[py]})
			} else {
				rev = append(rev, diffOp{'-', a[px]})
			}
		}
		x, y = px, py
	}
	ops := make([]diffOp, len(rev))
	for i, op := range rev {
		ops[len(rev)-1-i] = op
	}
	return ops
}
