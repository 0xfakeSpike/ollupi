package shell

import (
	"fmt"
	"strings"
)

const diffContext = 3

type diffOp struct {
	kind byte
	line string
}

// lineDiff is Myers' O(ND) diff over lines.
func lineDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	maxD := n + m
	v := make([]int, 2*maxD+2)
	var trace [][]int
	off := maxD + 1
	for d := 0; d <= maxD; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, off, d)
			}
		}
	}
	return nil
}

func backtrack(trace [][]int, a, b []string, off, d int) []diffOp {
	var ops []diffOp
	x, y := len(a), len(b)
	for ; d > 0; d-- {
		v := trace[d]
		k := x - y
		prev := k - 1
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prev = k + 1
		}
		px := v[off+prev]
		py := px - prev
		for x > px && y > py {
			x, y = x-1, y-1
			ops = append(ops, diffOp{' ', a[x]})
		}
		if x == px {
			y--
			ops = append(ops, diffOp{'+', b[y]})
		} else {
			x--
			ops = append(ops, diffOp{'-', a[x]})
		}
	}
	for x > 0 && y > 0 {
		x, y = x-1, y-1
		ops = append(ops, diffOp{' ', a[x]})
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// unifiedDiff renders the change from before to after as a unified diff of path, and counts the lines added and removed.
func unifiedDiff(path, before, after string, created bool) (string, int, int) {
	ops := lineDiff(splitLines(before), splitLines(after))
	var b strings.Builder
	from := "a/" + path
	if created {
		from = "/dev/null"
	}
	fmt.Fprintf(&b, "--- %s\n+++ b/%s\n", from, path)
	added, removed := 0, 0
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-diffContext)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end = min(run, end+diffContext)
				break
			}
			end = run
		}
		aStart, bStart := 1, 1
		for _, op := range ops[:start] {
			if op.kind != '+' {
				aStart++
			}
			if op.kind != '-' {
				bStart++
			}
		}
		aLen, bLen := 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				aLen++
			}
			if op.kind != '-' {
				bLen++
			}
		}
		if aLen == 0 {
			aStart--
		}
		if bLen == 0 {
			bStart--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, op := range ops[start:end] {
			switch op.kind {
			case '+':
				added++
			case '-':
				removed++
			}
			b.WriteByte(op.kind)
			b.WriteString(op.line)
			if !strings.HasSuffix(op.line, "\n") {
				b.WriteString("\n\\ No newline at end of file\n")
			}
		}
		i = end
	}
	return b.String(), added, removed
}
