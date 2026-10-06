package srcdiff

import (
	"math/rand"
	"strings"
	"testing"
)

func src(body string) []File { return []File{{Name: "a.gno", Body: "package a\n\n" + body}} }

func TestDiffAPI(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		from, to                string
		added, removed, changed []string
		same                    int
	}{
		{
			name:    "param type change shows both signatures",
			from:    "func Get(id int) string { return \"\" }\n",
			to:      "func Get(id string) string { return \"\" }\n",
			changed: []string{"func Get"},
		},
		{
			name:  "added and removed",
			from:  "func Old() {}\nfunc Kept() {}\n",
			to:    "func New() {}\nfunc Kept() {}\n",
			added: []string{"func New"}, removed: []string{"func Old"}, same: 1,
		},
		{
			name:    "method on a type, and the same name on another type is another symbol",
			from:    "type A struct{}\ntype B struct{}\nfunc (a *A) Get() int { return 0 }\nfunc (b B) Get() int { return 0 }\n",
			to:      "type A struct{}\ntype B struct{}\nfunc (a *A) Get() string { return \"\" }\nfunc (b B) Get() int { return 1 }\n",
			changed: []string{"method A.Get"}, same: 3,
		},
		{
			name: "reordering, a body change, a comment and reflowing are not changes",
			from: "// Doc.\nfunc A(x int,\n\ty int) {}\nfunc B() { println(1) }\nconst C = 1\n",
			to:   "const C = 1\n\n// Other doc.\nfunc B() { println(2) }\nfunc A(x int, y int) {}\n",
			same: 3,
		},
		{
			name: "unexported declarations, fields and methods are ignored",
			from: "func helper() {}\ntype T struct {\n\tA int\n\tb int\n}\nfunc (T) inner() {}\ntype t struct{}\nfunc (t) Out() {}\n",
			to:   "func helper2(x int) {}\ntype T struct {\n\tA int\n\tc string\n}\n",
			same: 1,
		},
		{
			name:    "an exported field change is a type change; a const value is part of its declaration",
			from:    "type T struct{ A int }\nconst Max = 10\nvar V int\n",
			to:      "type T struct{ A string }\nconst Max = 20\nvar V int = 3\n",
			changed: []string{"const Max", "type T"}, same: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := DiffAPI(src(tc.from), src(tc.to))
			names := func(ds []Decl) []string {
				var out []string
				for _, d := range ds {
					out = append(out, label(d.Kind, d.Recv, d.Name))
				}
				return out
			}
			var changed []string
			for _, c := range a.Changed {
				changed = append(changed, label(c.Kind, c.Recv, c.Name))
				if same(c.Old, c.New) || c.Old == "" || c.New == "" {
					t.Errorf("%s: old %q new %q", c.Name, c.Old, c.New)
				}
			}
			eq(t, "added", names(a.Added), tc.added)
			eq(t, "removed", names(a.Removed), tc.removed)
			eq(t, "changed", changed, tc.changed)
			if a.Same != tc.same {
				t.Errorf("same = %d, want %d", a.Same, tc.same)
			}
			if want := len(tc.added)+len(tc.removed)+len(tc.changed) > 0; a.ExportsChanged != want {
				t.Errorf("ExportsChanged = %v, want %v", a.ExportsChanged, want)
			}
		})
	}
}

func TestDiffAPIShowsTheSignatures(t *testing.T) {
	a := DiffAPI(src("func Get(id int) string { return \"\" }\n"), src("func Get(id string) string { return \"\" }\n"))
	if len(a.Changed) != 1 || a.Changed[0].Old != "func Get(id int) string" || a.Changed[0].New != "func Get(id string) string" {
		t.Errorf("changed = %+v", a.Changed)
	}
}

func label(kind, recv, name string) string {
	if recv != "" {
		return kind + " " + recv + "." + name
	}
	return kind + " " + name
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// rebuild reads both versions back out of a diff: the old one from kept and
// removed lines, the new one from kept and added lines.
func rebuild(t *testing.T, fd FileDiff, a, b []string) {
	t.Helper()
	// Hunks hold only the changed regions; fill the gaps from the inputs.
	var gotA, gotB []string
	i, j := 0, 0
	for _, h := range fd.Hunks {
		for i+1 < h.OldStart && len(h.Lines) > 0 && h.Lines[0].Old != 0 || (h.OldLines > 0 && i+1 < h.OldStart) {
			gotA = append(gotA, a[i])
			gotB = append(gotB, b[j])
			i++
			j++
		}
		for _, l := range h.Lines {
			switch l.Op {
			case " ":
				gotA, gotB = append(gotA, l.Text), append(gotB, l.Text)
				i, j = l.Old, l.New
			case "-":
				gotA = append(gotA, l.Text)
				i = l.Old
			case "+":
				gotB = append(gotB, l.Text)
				j = l.New
			}
		}
	}
	for i < len(a) {
		gotA = append(gotA, a[i])
		gotB = append(gotB, b[j])
		i++
		j++
	}
	if strings.Join(gotA, "\n") != strings.Join(a, "\n") || strings.Join(gotB, "\n") != strings.Join(b, "\n") {
		t.Fatalf("diff does not rebuild its inputs\n a=%q\n b=%q\n hunks=%+v", a, b, fd.Hunks)
	}
}

// lcs is the length of the longest common subsequence, by the textbook
// table: what a minimal diff keeps.
func lcs(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func TestDiffTextFixtures(t *testing.T) {
	a := "package a\n\nimport \"std\"\n\nfunc A() {}\n\nfunc B() {}\n\nfunc C() {}\n\nfunc D() {}\n\nfunc E() {}\n"
	b := "package a\n\nimport \"std\"\n\nfunc A() {}\n\nfunc B2() {}\n\nfunc C() {}\n\nfunc D() {}\n\nfunc E() {}\n\nfunc F() {}\n"
	fd := DiffText(a, b)
	if fd.Added != 3 || fd.Removed != 1 {
		t.Errorf("added %d removed %d, want 3 and 1", fd.Added, fd.Removed)
	}
	// Six unchanged lines between the two changes: their contexts touch,
	// so one hunk, as git would print it.
	if len(fd.Hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(fd.Hunks))
	}
	h := fd.Hunks[0]
	if h.OldStart != 4 || h.OldLines != 10 || h.NewStart != 4 || h.NewLines != 12 {
		t.Errorf("hunk 0 = -%d,%d +%d,%d, want -4,10 +4,12", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	}
	rebuild(t, fd, splitLines(a), splitLines(b))
	// Two more unchanged lines between them and they are two hunks.
	a2 := strings.Replace(a, "func D() {}\n", "func D() {}\n\nfunc D2() {}\n", 1)
	b2 := strings.Replace(b, "func D() {}\n", "func D() {}\n\nfunc D2() {}\n", 1)
	if fd := DiffText(a2, b2); len(fd.Hunks) != 2 {
		t.Errorf("eight lines apart: %d hunks, want 2", len(fd.Hunks))
	} else {
		rebuild(t, fd, splitLines(a2), splitLines(b2))
	}
	rebuild(t, fd, splitLines(a), splitLines(b))

	if fd := DiffText(a, a); len(fd.Hunks) != 0 || fd.Added+fd.Removed != 0 {
		t.Errorf("identical: %+v", fd)
	}
	if fd := DiffText("", "x\ny\n"); fd.Added != 2 || fd.Hunks[0].OldStart != 0 || fd.Hunks[0].NewStart != 1 {
		t.Errorf("from empty: %+v", fd)
	}
}

// Random edits: every diff rebuilds both inputs and is minimal.
func TestDiffTextRandom(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", "c", "d", "}", ""}
	gen := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = alphabet[r.Intn(len(alphabet))]
		}
		return out
	}
	for iter := 0; iter < 400; iter++ {
		a, b := gen(r.Intn(30)), gen(r.Intn(30))
		join := func(s []string) string {
			if len(s) == 0 {
				return ""
			}
			return strings.Join(s, "\n") + "\n"
		}
		fd := DiffText(join(a), join(b))
		if fd.TooLarge {
			t.Fatal("small input marked too large")
		}
		if want := len(a) + len(b) - 2*lcs(a, b); fd.Added+fd.Removed != want {
			t.Fatalf("edits %d, minimal is %d\n a=%q\n b=%q", fd.Added+fd.Removed, want, a, b)
		}
		rebuild(t, fd, splitLines(join(a)), splitLines(join(b)))
	}
}

func TestDiffTextTooLarge(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < MaxEdits; i++ {
		a.WriteString("a\n")
		b.WriteString("b\n")
	}
	fd := DiffText(a.String(), b.String())
	if !fd.TooLarge || fd.Added != MaxEdits || fd.Removed != MaxEdits {
		t.Errorf("too large: %v added %d removed %d", fd.TooLarge, fd.Added, fd.Removed)
	}
}

func TestDiffFilesStatus(t *testing.T) {
	from := []File{{"a.gno", "x\n"}, {"gone.gno", "y\n"}, {"same.gno", "z\n"}}
	to := []File{{"a.gno", "x2\n"}, {"new.gno", "w\n"}, {"same.gno", "z\n"}}
	got := map[string]string{}
	for _, f := range DiffFiles(from, to) {
		got[f.Name] = f.Status
	}
	want := map[string]string{"a.gno": "modified", "gone.gno": "removed", "new.gno": "added", "same.gno": "unchanged"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}
