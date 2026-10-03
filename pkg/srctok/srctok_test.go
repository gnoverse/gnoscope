package srctok

import (
	"encoding/json"
	"strings"
	"testing"
)

// joined is what a renderer that ignored every class would draw.
func joined(lines [][]Segment) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		var b strings.Builder
		for _, s := range l {
			b.WriteString(s.Text)
		}
		out[i] = b.String()
	}
	return out
}

// mustRoundTrip asserts the one guarantee the package makes: the segments of
// every line are exactly that line, and there are exactly as many lines as
// the frontend's own split gives.
func mustRoundTrip(t *testing.T, body string, lines [][]Segment) {
	t.Helper()
	want := strings.Split(body, "\n")
	got := joined(lines)
	if len(got) != len(want) {
		t.Fatalf("line count: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d does not round-trip:\n got %q\nwant %q", i+1, got[i], want[i])
		}
	}
}

func tokenize(t *testing.T, name string, files ...File) [][]Segment {
	t.Helper()
	lines, ok := NewIndex(files).Tokenize(name)
	if !ok {
		t.Fatalf("no file %s", name)
	}
	return lines
}

// classOf finds the first segment on line (1-based) whose text is text.
func classOf(t *testing.T, lines [][]Segment, line int, text string) Segment {
	t.Helper()
	for _, s := range lines[line-1] {
		if s.Text == text {
			return s
		}
	}
	t.Fatalf("line %d has no segment %q: %v", line, text, lines[line-1])
	return Segment{}
}

const fileA = `package app

import (
	"std"
	"strings"

	"gno.land/p/demo/avl"
	ufmt "gno.land/p/nt/ufmt/v0"
)

// Counter counts.
type Counter struct {
	Name string
	n    int
}

var total = 0x1F

func (c *Counter) Inc(cur realm) {
	c.n++
	total++
}

func Hello(name string) string {
	tree := avl.NewTree()
	_ = tree
	_ = std.Address("g1")
	return ufmt.Sprintf("%s", strings.ToUpper(name)) + helper()
}
`

const fileB = "package app\n\nfunc helper() string {\n\ttotal := 1 // shadows the package var\n\t_ = total\n\tc := Counter{Name: `x\ny`}\n\tc.Inc(cross)\n\treturn Hello(\"\") + string(rune(len(\"ab\")))\n}\n"

func TestTokenizeClasses(t *testing.T) {
	files := []File{{"a.gno", fileA}, {"b.gno", fileB}}
	a := tokenize(t, "a.gno", files...)
	b := tokenize(t, "b.gno", files...)
	mustRoundTrip(t, fileA, a)
	mustRoundTrip(t, fileB, b)

	cases := []struct {
		name  string
		lines [][]Segment
		line  int
		text  string
		class string
		key   string
	}{
		{"keyword", a, 1, "package", Keyword, ""},
		{"stdlib import is a string, not a link", a, 4, `"std"`, String, ""},
		{"p/ import is a link", a, 7, `"gno.land/p/demo/avl"`, Import, ""},
		{"named import's alias is a package name", a, 8, "ufmt", Pkg, ""},
		{"named p/ import is a link", a, 8, `"gno.land/p/nt/ufmt/v0"`, Import, ""},
		{"comment", a, 11, "// Counter counts.", Comment, ""},
		{"type declaration", a, 12, "Counter", Decl, ""},
		{"struct field is not a declaration", a, 13, "Name", Ident, ""},
		{"predeclared type", a, 13, "string", Type, ""},
		{"var declaration", a, 17, "total", Decl, ""},
		{"hex number", a, 17, "0x1F", Number, ""},
		{"method declaration carries its key", a, 19, "Inc", Decl, "Counter.Inc"},
		{"receiver type is a reference", a, 19, "Counter", Ref, ""},
		{"realm is a predeclared type", a, 19, "realm", Type, ""},
		{"package var in the same file", a, 21, "total", Ref, ""},
		{"func declaration", a, 24, "Hello", Decl, ""},
		{"package qualifier", a, 25, "avl", Pkg, ""},
		{"selected and called", a, 25, "NewTree", Func, ""},
		{"stdlib qualifier", a, 27, "std", Pkg, ""},
		{"/vN import is used by its parent name", a, 28, "ufmt", Pkg, ""},
		{"func in another file", a, 28, "helper", Ref, ""},
		{"string literal", a, 28, `"%s"`, String, ""},

		{"declared in b", b, 3, "helper", Decl, ""},
		{"a local shadowing a package var is not a reference", b, 4, "total", Ident, ""},
		{"trailing comment", b, 4, "// shadows the package var", Comment, ""},
		{"local use stays local", b, 5, "total", Ident, ""},
		{"type from another file", b, 6, "Counter", Ref, ""},
		{"composite literal key is not free", b, 6, "Name", Ident, ""},
		{"raw string, first line", b, 6, "`x", String, ""},
		{"raw string, second line", b, 7, "y`", String, ""},
		{"method call", b, 8, "Inc", Func, ""},
		{"cross is a builtin", b, 8, "cross", Builtin, ""},
		{"func from another file", b, 9, "Hello", Ref, ""},
		{"conversion is a type, not a call", b, 9, "string", Type, ""},
		{"len is a builtin", b, 9, "len", Builtin, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := classOf(t, c.lines, c.line, c.text)
			if s.Class != c.class || s.Key != c.key {
				t.Errorf("%q on line %d: got (%q, %q), want (%q, %q)", c.text, c.line, s.Class, s.Key, c.class, c.key)
			}
		})
	}

	ix := NewIndex(files)
	for key, want := range map[string]Location{
		"Counter":     {"a.gno", 12},
		"total":       {"a.gno", 17},
		"Counter.Inc": {"a.gno", 19},
		"Hello":       {"a.gno", 24},
		"helper":      {"b.gno", 3},
	} {
		if got := ix.Decls[key]; got != want {
			t.Errorf("Decls[%q] = %v, want %v", key, got, want)
		}
	}
	if len(ix.Decls) != 5 {
		t.Errorf("Decls has %d entries, want 5: %v", len(ix.Decls), ix.Decls)
	}
}

// On-chain source is attacker-controlled. Whatever arrives has to come back
// as the same bytes, in valid JSON that cannot close a script tag, without a
// panic, and in time.
func TestTokenizeHostile(t *testing.T) {
	long := "package x\nvar s = \"" + strings.Repeat("A&lt;", 200_000) + "\"\n"
	cases := []struct {
		name string
		body string
	}{
		{"script close in a string and a comment", "package x\n\nvar s = \"</script><script>alert(1)</script>\" // </script>\n/* </SCRIPT > */\n"},
		{"html entities and markup as code", "package x\n\nvar a = \"&amp; &lt;b&gt; &#x3c;\"\nvar b = 1 &^ 2 < 3\n<div onclick=x>\n"},
		{"very long line", long},
		{"invalid utf-8", "package x\n\nvar s = \"\xff\xfe\"\nvar \xc3\x28 = 1\n// \xed\xa0\x80\n"},
		{"unterminated string", "package x\n\nvar s = \"never closed\nfunc F() {}\n"},
		{"unterminated raw string", "package x\n\nvar s = `never\nclosed\n"},
		{"unterminated block comment", "package x\n/* never\nclosed"},
		{"unterminated rune", "package x\nvar r = 'a\n"},
		{"carriage returns", "package x\r\n\r\n// c\r\nvar s = `a\r\nb`\r\n/* x\r\n*/\r\n"},
		{"byte order mark", "\xef\xbb\xbfpackage x\n"},
		{"not go at all", "#!/bin/sh\n@@@ $$$ \x00\x01 ```\n"},
		{"empty", ""},
		{"only newlines", "\n\n\n"},
		{"deep nesting", "package x\nvar v = " + strings.Repeat("(", 20_000) + "1" + strings.Repeat(")", 20_000) + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := tokenize(t, "x.gno", File{"x.gno", c.body})
			mustRoundTrip(t, c.body, lines)
			out, err := json.Marshal(map[string]any{"lines": lines})
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) {
				t.Fatal("invalid JSON")
			}
			if strings.Contains(strings.ToLower(string(out)), "</script") {
				t.Fatal("a script close tag survived into the JSON")
			}
		})
	}
}

func TestTokenizeNonGnoFileIsPlain(t *testing.T) {
	body := "module = \"gno.land/r/x\"\n\n// not a comment here\n"
	lines := tokenize(t, "gnomod.toml", File{"gnomod.toml", body}, File{"x.gno", "package x\n"})
	mustRoundTrip(t, body, lines)
	for i, l := range lines {
		for _, s := range l {
			if s.Class != "" {
				t.Errorf("line %d: %q classed %q in a non-gno file", i+1, s.Text, s.Class)
			}
		}
	}
	if _, ok := NewIndex(nil).Tokenize("nope.gno"); ok {
		t.Error("Tokenize of a missing file reported ok")
	}
}

func TestSegmentJSON(t *testing.T) {
	got, err := json.Marshal([]Segment{{"", " ", ""}, {Keyword, "func", ""}, {Decl, "M", "T.M"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `[" ",["kw","func"],["decl","M","T.M"]]`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
	loc, _ := json.Marshal(Location{"a.gno", 3})
	if string(loc) != `["a.gno",3]` {
		t.Errorf("Location: got %s", loc)
	}
}

// Adjacent punctuation is one segment; adjacent names never are.
func TestTokenizeMerges(t *testing.T) {
	lines := tokenize(t, "x.gno", File{"x.gno", "package x\nfunc F() {}\n"})
	got, _ := json.Marshal(lines[1])
	if want := `[["kw","func"]," ",["decl","F"],["op","()"]," ",["op","{}"]]`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
