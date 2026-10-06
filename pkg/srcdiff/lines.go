package srcdiff

import (
	"sort"
	"strings"
)

// Line is one line of a hunk. Op is " " kept, "-" only in the old version,
// "+" only in the new one. Old and New are 1-based line numbers in each
// version, zero on the side the line is not in.
type Line struct {
	Op   string `json:"op"`
	Text string `json:"text"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}

// Hunk is a run of changed lines with up to Context unchanged lines around
// it, in the shape of a unified diff's @@ -a,b +c,d @@.
type Hunk struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Lines    []Line `json:"lines"`
}

// FileDiff is one file's change between two versions.
//
// Status is added, removed, modified or unchanged. Added and Removed count
// lines. Hunks is empty for an unchanged file.
type FileDiff struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	OldLines int    `json:"old_lines"`
	NewLines int    `json:"new_lines"`
	Hunks    []Hunk `json:"hunks"`
	// TooLarge is set when the two versions differ by more lines than a
	// diff is computed for (MaxEdits): the file is then shown as removed
	// whole and added whole.
	TooLarge bool `json:"too_large,omitempty"`
}

// Context is how many unchanged lines a hunk keeps around a change.
const Context = 3

// MaxEdits bounds the Myers search. Its time is the lines times the edits
// and its memory the edits squared (the frontier of every round is kept to
// walk the path back), so 2,000 edits is at most 16 MB; a file changed more
// than that is better shown as rewritten than computed at length.
const MaxEdits = 2000

// splitLines splits a body into lines, with no empty last line for a final
// newline: "a\nb\n" is two lines, the way an editor numbers them.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// DiffFiles compares every file of two versions, sorted by name.
func DiffFiles(from, to []File) []FileDiff {
	old := map[string]string{}
	for _, f := range from {
		old[f.Name] = f.Body
	}
	nw := map[string]string{}
	for _, f := range to {
		nw[f.Name] = f.Body
	}
	names := map[string]bool{}
	for n := range old {
		names[n] = true
	}
	for n := range nw {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	out := make([]FileDiff, 0, len(sorted))
	for _, n := range sorted {
		ob, inOld := old[n]
		nb, inNew := nw[n]
		fd := DiffText(ob, nb)
		fd.Name = n
		switch {
		case !inOld:
			fd.Status = "added"
		case !inNew:
			fd.Status = "removed"
		case ob == nb:
			fd.Status = "unchanged"
		default:
			fd.Status = "modified"
		}
		out = append(out, fd)
	}
	return out
}

// DiffText diffs two bodies line by line. Name and Status are left to the
// caller.
func DiffText(a, b string) FileDiff {
	al, bl := splitLines(a), splitLines(b)
	fd := FileDiff{OldLines: len(al), NewLines: len(bl), Hunks: []Hunk{}}
	if a == b {
		return fd
	}
	ops, ok := myers(al, bl, MaxEdits)
	if !ok {
		fd.TooLarge = true
		ops = make([]byte, 0, len(al)+len(bl))
		for range al {
			ops = append(ops, '-')
		}
		for range bl {
			ops = append(ops, '+')
		}
	}
	// Walk the edit script into numbered lines.
	lines := make([]Line, 0, len(ops))
	i, j := 0, 0
	for _, op := range ops {
		switch op {
		case ' ':
			lines = append(lines, Line{Op: " ", Text: al[i], Old: i + 1, New: j + 1})
			i++
			j++
		case '-':
			lines = append(lines, Line{Op: "-", Text: al[i], Old: i + 1})
			i++
			fd.Removed++
		case '+':
			lines = append(lines, Line{Op: "+", Text: bl[j], New: j + 1})
			j++
			fd.Added++
		}
	}
	fd.Hunks = hunks(lines)
	return fd
}

// hunks groups changed lines with Context unchanged lines on either side,
// merging two groups whose context would touch.
func hunks(lines []Line) []Hunk {
	out := []Hunk{}
	n := len(lines)
	k := 0
	for k < n {
		// Find the next change.
		for k < n && lines[k].Op == " " {
			k++
		}
		if k == n {
			break
		}
		start := max(0, k-Context)
		end := k
		for {
			// Extend over this change.
			for end < n && lines[end].Op != " " {
				end++
			}
			// Unchanged run after it: if another change comes within
			// 2*Context lines, the two share a hunk.
			run := end
			for run < n && lines[run].Op == " " {
				run++
			}
			if run < n && run-end <= 2*Context {
				end = run
				continue
			}
			end = min(n, end+Context)
			break
		}
		h := Hunk{Lines: append([]Line(nil), lines[start:end]...)}
		for _, l := range h.Lines {
			if l.Op != "+" {
				if h.OldStart == 0 {
					h.OldStart = l.Old
				}
				h.OldLines++
			}
			if l.Op != "-" {
				if h.NewStart == 0 {
					h.NewStart = l.New
				}
				h.NewLines++
			}
		}
		// A hunk with no line on one side starts, by unified-diff
		// convention, at the line before it.
		if h.OldLines == 0 {
			h.OldStart = prevNumber(lines[:start], true)
		}
		if h.NewLines == 0 {
			h.NewStart = prevNumber(lines[:start], false)
		}
		out = append(out, h)
		k = end
	}
	return out
}

func prevNumber(before []Line, old bool) int {
	for i := len(before) - 1; i >= 0; i-- {
		if old && before[i].Old > 0 {
			return before[i].Old
		}
		if !old && before[i].New > 0 {
			return before[i].New
		}
	}
	return 0
}

// myers returns the shortest edit script turning a into b as one byte per
// step (' ', '-', '+'), or false when it needs more than maxD edits.
//
// The O(ND) algorithm from Myers' 1986 paper, keeping each round's frontier
// so the path can be walked back.
func myers(a, b []string, maxD int) ([]byte, bool) {
	n, m := len(a), len(b)
	limit := min(n+m, maxD)
	off := limit + 1
	v := make([]int32, 2*limit+3)
	// trace[d] is v[off-d-1 .. off+d+1] as it was before round d ran: the
	// only part round d reads, and the only part the walk back needs.
	var trace [][]int32
	found := false
	for d := 0; d <= limit && !found; d++ {
		trace = append(trace, append([]int32(nil), v[off-d-1:off+d+2]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = int(v[off+k+1])
			} else {
				x = int(v[off+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = int32(x)
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	if !found {
		return nil, false
	}
	var rev []byte
	x, y := n, m
	for d := len(trace) - 1; d > 0; d-- {
		vp := trace[d]
		at := func(k int) int { return int(vp[k+d+1]) }
		k := x - y
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, ' ')
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, '+')
		} else {
			rev = append(rev, '-')
		}
		x, y = prevX, prevY
	}
	for x > 0 && y > 0 {
		rev = append(rev, ' ')
		x--
		y--
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}
