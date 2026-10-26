package project

import (
	"fmt"
	"strings"
)

// Diff is a unified diff of a (labelled from) and b (labelled to), with
// context lines of context around each change; "" if they're the same.
// Very different large files get a note instead of the lines.
func Diff(a, b, from, to string, context int) string {
	if a == b {
		return ""
	}
	x, y := splitLines(a), splitLines(b)
	ops, ok := diffLines(x, y)
	if !ok {
		return fmt.Sprintf("--- %s\n+++ %s\n(too different to show line by line: %d lines → %d lines)\n", from, to, len(x), len(y))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
	// Hunks: runs of changes with up to context lines either side (two
	// changes closer than twice that share a hunk).
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-context)
		end := i
		for j := i; j < len(ops); j++ {
			if ops[j].kind != ' ' {
				end = j
			} else if j-end > 2*context {
				break
			}
		}
		end = min(len(ops), end+context+1)
		// Line numbers where the hunk starts, and how many lines it has, on each side.
		ai, bi := 1, 1
		for _, o := range ops[:start] {
			if o.kind != '+' {
				ai++
			}
			if o.kind != '-' {
				bi++
			}
		}
		an, bn := 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				an++
			}
			if o.kind != '-' {
				bn++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(ai, an), hunkRange(bi, bn))
		for _, o := range ops[start:end] {
			out.WriteByte(o.kind)
			out.WriteString(o.line)
			out.WriteByte('\n')
		}
		i = end
	}
	return out.String()
}

// DiffStat counts a unified diff's added and removed lines.
func DiffStat(d string) (added, removed int) {
	for _, l := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start-1)
	}
	if n == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type diffOp struct {
	kind byte // ' ', '-' or '+'
	line string
}

// diffLines is the line-by-line edit from x to y (a longest common
// subsequence, after the common start and end); false if what's left to
// compare is too big.
func diffLines(x, y []string) ([]diffOp, bool) {
	var ops []diffOp
	p := 0
	for p < len(x) && p < len(y) && x[p] == y[p] {
		ops = append(ops, diffOp{' ', x[p]})
		p++
	}
	s := 0
	for s < len(x)-p && s < len(y)-p && x[len(x)-1-s] == y[len(y)-1-s] {
		s++
	}
	mx, my := x[p:len(x)-s], y[p:len(y)-s]
	n, m := len(mx), len(my)
	if n*m > 4_000_000 {
		return nil, false
	}
	// lcs[i][j]: the longest common subsequence of mx[i:] and my[j:].
	w := m + 1
	lcs := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if mx[i] == my[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && mx[i] == my[j]:
			ops = append(ops, diffOp{' ', mx[i]})
			i, j = i+1, j+1
		case i < n && (j == m || lcs[(i+1)*w+j] >= lcs[i*w+j+1]):
			ops = append(ops, diffOp{'-', mx[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', my[j]})
			j++
		}
	}
	for _, l := range x[len(x)-s:] {
		ops = append(ops, diffOp{' ', l})
	}
	return ops, true
}
